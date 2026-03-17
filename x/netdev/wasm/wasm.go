//go:build wasip1

package wasm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"
	"unsafe"
)

const (
	defaultSleepMs = 1
	defaultPollMs  = 10
	prefetchSize   = 64 * 1024
	maxPollBackoff = 2 // max skip: (1<<2)-1 = 3 PollRead calls
	connectTimeout = 30 * time.Second
)

const (
	wasiErrSuccess    = 0
	wasiErrAgain      = 6
	wasiErrInprogress = 26

	wasiAFInet4       = 1
	wasiSockTypeStream = 1
	wasiSockProtoTCP   = 6

	wasiFdflagNonblock = 1 << 2

	wasiRiFlagPeek = 1 << 0

	wasiEventTypeClock   = 0
	wasiEventTypeFdRead  = 1
	wasiEventTypeFdWrite = 2

	wasiClockMonotonic = 1

	wasiSockOptNoDelay   = 3
	wasiSockOptKeepAlive = 12

	wasiShutdownRead  = 1
	wasiShutdownWrite = 2
	wasiShutdownBoth  = wasiShutdownRead | wasiShutdownWrite

	wasiBoolFalse = 0
	wasiBoolTrue  = 1
)

// SchedReady gates cooperative yielding in TinyGo WASM loops.
// It stays false during synchronous setup and is enabled by the export layer
// once the agent is ready to process traffic.
var SchedReady bool

// maybeGosched yields control briefly via host sleep. TinyGo WASM scheduler
// can panic in exported contexts when forcing goroutine switches.
func maybeGosched() {
	if !SchedReady {
		return
	}
	// Intentionally no-op in TinyGo WASM polling mode.
}

// MaybeGosched is the exported version of maybeGosched for use by other packages.
func MaybeGosched() {
	maybeGosched()
}

type wasiAddr struct {
	Tag     uint8
	Padding uint8
	Octets  [16]byte
}

type wasiAddrPort struct {
	Tag     uint8
	Padding uint8
	Octets  [18]byte
}

type wasiIovec struct {
	Buf    *byte
	BufLen uint32
}

type wasiCiovec struct {
	Buf    *byte
	BufLen uint32
}

type wasiSubscription struct {
	UserData  uint64
	EventType uint8
	_         [7]byte
	U         [32]byte
}

type wasiEvent struct {
	UserData  uint64
	Errno     uint16
	EventType uint8
	_         [5]byte
	NBytes    uint64
	Flags     uint16
	_         [6]byte
}

// WASIX imports.

//go:wasmimport wasix_32v1 resolve
func wasixResolve(hostPtr unsafe.Pointer, hostLen int32, port int32, addrsPtr unsafe.Pointer, naddrs int32, retNaddrsPtr unsafe.Pointer) int32

//go:wasmimport wasix_32v1 sock_open
func wasixSockOpen(af int32, stype int32, protocol int32, roSockPtr unsafe.Pointer) int32

//go:wasmimport wasix_32v1 sock_connect
func wasixSockConnect(fd int32, addrPtr unsafe.Pointer) int32

//go:wasmimport wasix_32v1 sock_bind
func wasixSockBind(fd int32, addrPtr unsafe.Pointer) int32

//go:wasmimport wasix_32v1 sock_listen
func wasixSockListen(fd int32, backlog int32) int32

//go:wasmimport wasix_32v1 sock_accept_v2
func wasixSockAcceptV2(fd int32, fdFlags int32, roFdPtr unsafe.Pointer, roAddrPtr unsafe.Pointer) int32

//go:wasmimport wasix_32v1 sock_send
func wasixSockSend(fd int32, siDataPtr unsafe.Pointer, siDataLen int32, siFlags int32, retDataLenPtr unsafe.Pointer) int32

//go:wasmimport wasix_32v1 sock_recv
func wasixSockRecv(fd int32, riDataPtr unsafe.Pointer, riDataLen int32, riFlags int32, retDataLenPtr unsafe.Pointer, retFlagsPtr unsafe.Pointer) int32

//go:wasmimport wasix_32v1 sock_set_opt_flag
func wasixSockSetOptFlag(fd int32, opt int32, flag int32) int32

//go:wasmimport wasix_32v1 sock_shutdown
func wasixSockShutdown(fd int32, how int32) int32

//go:wasmimport wasix_32v1 thread_sleep
func wasixThreadSleep(durationNs int64) int32

// WASI preview1 imports used by WASIX sockets.

//go:wasmimport wasi_snapshot_preview1 fd_close
func wasiFdClose(fd int32) int32

//go:wasmimport wasi_snapshot_preview1 fd_fdstat_set_flags
func wasiFdFdstatSetFlags(fd int32, flags int32) int32

//go:wasmimport wasi_snapshot_preview1 poll_oneoff
func wasiPollOneoff(inPtr unsafe.Pointer, outPtr unsafe.Pointer, nsubscriptions int32, neventsPtr unsafe.Pointer) int32

func errnoString(errno int32) string {
	switch errno {
	case wasiErrSuccess:
		return "success"
	case wasiErrAgain:
		return "again"
	case wasiErrInprogress:
		return "inprogress"
	default:
		return "unknown"
	}
}

func errnoError(op string, errno int32) error {
	return fmt.Errorf("%s: errno %d (%s)", op, errno, errnoString(errno))
}

func sleepUntilReady(deadline time.Time, op string) error {
	if !deadline.IsZero() && time.Now().After(deadline) {
		return fmt.Errorf("%s: deadline exceeded", op)
	}
	SleepMs(defaultSleepMs)
	maybeGosched()
	return nil
}

func fillPollFdSub(out *wasiSubscription, userData uint64, eventType uint8, fd int32) {
	*out = wasiSubscription{}
	out.UserData = userData
	out.EventType = eventType
	binary.LittleEndian.PutUint32(out.U[0:4], uint32(fd))
}

func fillPollClockSub(out *wasiSubscription, userData uint64, timeoutMs int32) {
	if timeoutMs < 0 {
		timeoutMs = 0
	}
	*out = wasiSubscription{}
	out.UserData = userData
	out.EventType = wasiEventTypeClock
	binary.LittleEndian.PutUint32(out.U[0:4], uint32(wasiClockMonotonic))
	timeoutNs := uint64(timeoutMs) * uint64(time.Millisecond)
	binary.LittleEndian.PutUint64(out.U[8:16], timeoutNs)
	binary.LittleEndian.PutUint64(out.U[16:24], 0)
	binary.LittleEndian.PutUint16(out.U[24:26], 0)
}

func pollFdEvent(fd int32, eventType uint8, timeoutMs int32) (bool, error) {
	var subs [2]wasiSubscription
	fillPollFdSub(&subs[0], 1, eventType, fd)
	fillPollClockSub(&subs[1], 2, timeoutMs)

	var events [2]wasiEvent
	var nevents uint32
	errno := wasiPollOneoff(
		unsafe.Pointer(&subs[0]),
		unsafe.Pointer(&events[0]),
		2,
		unsafe.Pointer(&nevents),
	)
	if errno != wasiErrSuccess {
		return false, errnoError("poll_oneoff", errno)
	}

	for i := 0; i < int(nevents) && i < len(events); i++ {
		ev := events[i]
		if ev.UserData == 1 {
			if ev.Errno != 0 {
				return false, errnoError("poll_oneoff.fd", int32(ev.Errno))
			}
			return true, nil
		}
	}

	return false, nil
}

func waitFdReady(fd int32, eventType uint8, deadline time.Time, op string) error {
	timeoutMs := int32(defaultPollMs)
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("%s: deadline exceeded", op)
		}
		remainingMs := int32(remaining / time.Millisecond)
		if remainingMs <= 0 {
			remainingMs = 1
		}
		if remainingMs < timeoutMs {
			timeoutMs = remainingMs
		}
	}

	ready, err := pollFdEvent(fd, eventType, timeoutMs)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	if !deadline.IsZero() && time.Now().After(deadline) {
		return fmt.Errorf("%s: deadline exceeded", op)
	}
	return nil
}

func setNonblock(fd int32, enabled bool) error {
	flags := int32(0)
	if enabled {
		flags = wasiFdflagNonblock
	}
	errno := wasiFdFdstatSetFlags(fd, flags)
	if errno != wasiErrSuccess {
		return errnoError("fd_fdstat_set_flags", errno)
	}
	return nil
}

func fillAddrPortIPv4(out *wasiAddrPort, addr netip.AddrPort) {
	*out = wasiAddrPort{}
	out.Tag = wasiAFInet4
	ip := addr.Addr().As4()
	binary.LittleEndian.PutUint16(out.Octets[0:2], addr.Port())
	copy(out.Octets[2:6], ip[:])
}

func parseAddrPortIPv4(in *wasiAddrPort) (netip.AddrPort, error) {
	if in.Tag != wasiAFInet4 {
		return netip.AddrPort{}, fmt.Errorf("unsupported address family tag: %d", in.Tag)
	}
	addr := netip.AddrFrom4([4]byte{in.Octets[2], in.Octets[3], in.Octets[4], in.Octets[5]})
	portBE := binary.BigEndian.Uint16(in.Octets[0:2])
	portLE := binary.LittleEndian.Uint16(in.Octets[0:2])
	port := portBE
	if port == 0 && portLE != 0 {
		port = portLE
	}
	return netip.AddrPortFrom(addr, port), nil
}

func parseResolvedIPv4(in *wasiAddr) (netip.Addr, bool) {
	if in.Tag != wasiAFInet4 {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{in.Octets[0], in.Octets[1], in.Octets[2], in.Octets[3]}), true
}

func sockSend(fd int32, buf []byte, flags int32) (int32, int32) {
	if len(buf) == 0 {
		return 0, wasiErrSuccess
	}
	ciov := wasiCiovec{Buf: &buf[0], BufLen: uint32(len(buf))}
	var sent uint32
	errno := wasixSockSend(fd, unsafe.Pointer(&ciov), 1, flags, unsafe.Pointer(&sent))
	return int32(sent), errno
}

func sockRecv(fd int32, buf []byte, flags int32) (int32, int32) {
	if len(buf) == 0 {
		return 0, wasiErrSuccess
	}
	iov := wasiIovec{Buf: &buf[0], BufLen: uint32(len(buf))}
	var read uint32
	var roFlags uint16
	errno := wasixSockRecv(fd, unsafe.Pointer(&iov), 1, flags, unsafe.Pointer(&read), unsafe.Pointer(&roFlags))
	return int32(read), errno
}

// WasmNetdev implements Netdever using native WASIX socket APIs.
type WasmNetdev struct{}

// New creates a WasmNetdev.
func New() *WasmNetdev {
	return &WasmNetdev{}
}

// SleepMs sleeps for the given number of milliseconds using WASIX thread_sleep.
func SleepMs(ms int) {
	if ms <= 0 {
		return
	}
	_ = wasixThreadSleep(int64(ms) * int64(time.Millisecond))
}

func (d *WasmNetdev) GetHostByName(name string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(name); err == nil {
		if addr.Is4() || addr.Is4In6() {
			return netip.AddrFrom4(addr.As4()), nil
		}
		return netip.Addr{}, fmt.Errorf("only IPv4 supported: %s", name)
	}
	if name == "" {
		return netip.Addr{}, errors.New("resolve: empty host")
	}

	nameBytes := []byte(name)
	var addrs [8]wasiAddr
	var naddrs uint32
	errno := wasixResolve(
		unsafe.Pointer(&nameBytes[0]),
		int32(len(nameBytes)),
		0,
		unsafe.Pointer(&addrs[0]),
		int32(len(addrs)),
		unsafe.Pointer(&naddrs),
	)
	if errno != wasiErrSuccess {
		return netip.Addr{}, errnoError("resolve", errno)
	}

	for i := 0; i < int(naddrs) && i < len(addrs); i++ {
		if addr, ok := parseResolvedIPv4(&addrs[i]); ok {
			return addr, nil
		}
	}

	return netip.Addr{}, fmt.Errorf("resolve: no IPv4 result for %q", name)
}

func (d *WasmNetdev) Addr() (netip.Addr, error) {
	return netip.MustParseAddr("127.0.0.1"), nil
}

func (d *WasmNetdev) Socket(domain int, stype int, protocol int) (int, error) {
	if domain != 2 {
		return -1, fmt.Errorf("socket: unsupported domain %d", domain)
	}

	fd := int32(-1)
	errno := wasixSockOpen(int32(wasiAFInet4), int32(stype), int32(protocol), unsafe.Pointer(&fd))
	if errno != wasiErrSuccess {
		return -1, errnoError("sock_open", errno)
	}
	if err := setNonblock(fd, true); err != nil {
		_ = wasiFdClose(fd)
		return -1, fmt.Errorf("socket: set nonblock failed: %w", err)
	}
	return int(fd), nil
}

func (d *WasmNetdev) Bind(sockfd int, ip netip.AddrPort) error {
	addr := ip.Addr()
	if !(addr.Is4() || addr.Is4In6()) {
		return fmt.Errorf("bind: only IPv4 supported (%s)", ip)
	}
	var ap wasiAddrPort
	fillAddrPortIPv4(&ap, netip.AddrPortFrom(netip.AddrFrom4(addr.As4()), ip.Port()))
	errno := wasixSockBind(int32(sockfd), unsafe.Pointer(&ap))
	if errno != wasiErrSuccess {
		return errnoError("sock_bind", errno)
	}
	return nil
}

func (d *WasmNetdev) Connect(sockfd int, host string, ip netip.AddrPort) error {
	addr := ip.Addr()
	if !(addr.Is4() || addr.Is4In6()) {
		return fmt.Errorf("connect: only IPv4 supported (%s)", ip)
	}
	var ap wasiAddrPort
	fillAddrPortIPv4(&ap, netip.AddrPortFrom(netip.AddrFrom4(addr.As4()), ip.Port()))
	deadline := time.Now().Add(connectTimeout)
	for {
		errno := wasixSockConnect(int32(sockfd), unsafe.Pointer(&ap))
		if errno == wasiErrSuccess {
			return nil
		}
		if errno == wasiErrAgain || errno == wasiErrInprogress {
			if err := waitFdReady(int32(sockfd), wasiEventTypeFdWrite, deadline, "connect"); err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return errors.New("connect: timeout")
			}
			continue
		}
		return errnoError("sock_connect", errno)
	}
}

func (d *WasmNetdev) Listen(sockfd int, backlog int) error {
	errno := wasixSockListen(int32(sockfd), int32(backlog))
	if errno != wasiErrSuccess {
		return errnoError("sock_listen", errno)
	}
	if err := setNonblock(int32(sockfd), true); err != nil {
		return fmt.Errorf("listen: set nonblock failed: %w", err)
	}
	return nil
}

func (d *WasmNetdev) Accept(sockfd int) (int, netip.AddrPort, error) {
	for {
		newfd := int32(-1)
		var peer wasiAddrPort
		errno := wasixSockAcceptV2(
			int32(sockfd),
			wasiFdflagNonblock,
			unsafe.Pointer(&newfd),
			unsafe.Pointer(&peer),
		)
		if errno == wasiErrSuccess {
			_ = setNonblock(newfd, true)
			peerAddr, err := parseAddrPortIPv4(&peer)
			if err != nil {
				_ = wasiFdClose(newfd)
				return -1, netip.AddrPort{}, err
			}
			return int(newfd), peerAddr, nil
		}
		if errno == wasiErrAgain {
			if err := waitFdReady(int32(sockfd), wasiEventTypeFdRead, time.Time{}, "accept"); err != nil {
				return -1, netip.AddrPort{}, err
			}
			continue
		}
		return -1, netip.AddrPort{}, errnoError("sock_accept_v2", errno)
	}
}

func (d *WasmNetdev) Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	for {
		n, errno := sockSend(int32(sockfd), buf, int32(flags))
		if errno == wasiErrSuccess {
			return int(n), nil
		}
		if errno == wasiErrAgain {
			if err := waitFdReady(int32(sockfd), wasiEventTypeFdWrite, deadline, "send"); err != nil {
				return 0, err
			}
			continue
		}
		return 0, errnoError("sock_send", errno)
	}
}

func (d *WasmNetdev) Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	for {
		n, errno := sockRecv(int32(sockfd), buf, int32(flags))
		if errno == wasiErrSuccess {
			if n == 0 {
				return 0, errors.New("EOF")
			}
			return int(n), nil
		}
		if errno == wasiErrAgain {
			if err := waitFdReady(int32(sockfd), wasiEventTypeFdRead, deadline, "recv"); err != nil {
				return 0, err
			}
			continue
		}
		return 0, errnoError("sock_recv", errno)
	}
}

func (d *WasmNetdev) Close(sockfd int) error {
	_ = wasixSockShutdown(int32(sockfd), wasiShutdownBoth)
	errno := wasiFdClose(int32(sockfd))
	if errno != wasiErrSuccess {
		return errnoError("fd_close", errno)
	}
	return nil
}

func (d *WasmNetdev) SetSockOpt(sockfd int, level int, opt int, value interface{}) error {
	// Minimal mapping for common options used by TinyGo net stack.
	if level == 6 && opt == 1 {
		flag := int32(wasiBoolFalse)
		switch v := value.(type) {
		case int:
			if v != 0 {
				flag = wasiBoolTrue
			}
		case bool:
			if v {
				flag = wasiBoolTrue
			}
		default:
			return fmt.Errorf("unsupported TCP_NODELAY value type: %T", value)
		}
		errno := wasixSockSetOptFlag(int32(sockfd), wasiSockOptNoDelay, flag)
		if errno != wasiErrSuccess {
			return errnoError("sock_set_opt_flag(TCP_NODELAY)", errno)
		}
		return nil
	}

	if level == 1 && opt == 9 {
		flag := int32(wasiBoolFalse)
		switch v := value.(type) {
		case int:
			if v != 0 {
				flag = wasiBoolTrue
			}
		case bool:
			if v {
				flag = wasiBoolTrue
			}
		default:
			return fmt.Errorf("unsupported SO_KEEPALIVE value type: %T", value)
		}
		errno := wasixSockSetOptFlag(int32(sockfd), wasiSockOptKeepAlive, flag)
		if errno != wasiErrSuccess {
			return errnoError("sock_set_opt_flag(SO_KEEPALIVE)", errno)
		}
		return nil
	}

	// Keep behavior permissive for options we don't explicitly map.
	return nil
}

// WasmTCPConn implements net.Conn over WASIX socket functions,
// bypassing TinyGo's broken net.Dial (AsSlice bug in WASM).
type WasmTCPConn struct {
	fd            int32
	readDeadline  time.Time
	writeDeadline time.Time
	remoteAddr    string
	readBuf       []byte
	readPos       int
	readEnd       int
	readEOF       bool
	readErr       int32
	readPollSkip  int
	readPollExp   uint8
}

type wasmAddr struct {
	addr string
}

func (a wasmAddr) Network() string { return "tcp" }
func (a wasmAddr) String() string  { return a.addr }

func (c *WasmTCPConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	if c.readPos < c.readEnd {
		n := copy(b, c.readBuf[c.readPos:c.readEnd])
		c.readPos += n
		if c.readPos == c.readEnd {
			c.readPos = 0
			c.readEnd = 0
		}
		return n, nil
	}
	if c.readEOF {
		return 0, errors.New("EOF")
	}
	if c.readErr != 0 {
		errCode := c.readErr
		c.readErr = 0
		return 0, errnoError("read", errCode)
	}

	fetchSize := len(b)
	if fetchSize < prefetchSize {
		fetchSize = prefetchSize
	}
	if cap(c.readBuf) < fetchSize {
		c.readBuf = make([]byte, fetchSize)
	} else {
		c.readBuf = c.readBuf[:fetchSize]
	}

	for {
		n, errno := sockRecv(c.fd, c.readBuf, 0)
		if errno == wasiErrSuccess {
			if n == 0 {
				c.readEOF = true
				return 0, errors.New("EOF")
			}
			c.readPollSkip = 0
			c.readPollExp = 0
			c.readPos = 0
			c.readEnd = int(n)
			copied := copy(b, c.readBuf[c.readPos:c.readEnd])
			c.readPos += copied
			if c.readPos == c.readEnd {
				c.readPos = 0
				c.readEnd = 0
			}
			return copied, nil
		}
		if errno == wasiErrAgain {
			if err := waitFdReady(c.fd, wasiEventTypeFdRead, c.readDeadline, "read"); err != nil {
				return 0, err
			}
			continue
		}
		return 0, errnoError("read", errno)
	}
}

func (c *WasmTCPConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(b) {
		chunk := b[written:]
		n, errno := sockSend(c.fd, chunk, 0)
		if errno == wasiErrSuccess {
			if n <= 0 {
				if err := waitFdReady(c.fd, wasiEventTypeFdWrite, c.writeDeadline, "write"); err != nil {
					return written, err
				}
				continue
			}
			written += int(n)
			continue
		}
		if errno == wasiErrAgain {
			if err := waitFdReady(c.fd, wasiEventTypeFdWrite, c.writeDeadline, "write"); err != nil {
				return written, err
			}
			continue
		}
		return written, errnoError("write", errno)
	}
	return written, nil
}

func (c *WasmTCPConn) Close() error {
	_ = wasixSockShutdown(c.fd, wasiShutdownBoth)
	errno := wasiFdClose(c.fd)
	if errno != wasiErrSuccess {
		return errnoError("fd_close", errno)
	}
	return nil
}

func (c *WasmTCPConn) LocalAddr() net.Addr  { return wasmAddr{"0.0.0.0:0"} }
func (c *WasmTCPConn) RemoteAddr() net.Addr { return wasmAddr{c.remoteAddr} }

func (c *WasmTCPConn) SetDeadline(t time.Time) error {
	c.readDeadline = t
	c.writeDeadline = t
	return nil
}

func (c *WasmTCPConn) SetReadDeadline(t time.Time) error {
	c.readDeadline = t
	return nil
}

func (c *WasmTCPConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline = t
	return nil
}

// PollRead checks if data is available for reading without blocking.
// Returns true if data is available, false otherwise.
func (c *WasmTCPConn) PollRead(timeoutMs int) bool {
	if c.readPos < c.readEnd || c.readEOF || c.readErr != 0 {
		c.readPollSkip = 0
		c.readPollExp = 0
		return true
	}

	if c.readPollSkip > 0 {
		c.readPollSkip--
		return false
	}

	check := func() bool {
		fetchSize := prefetchSize
		if fetchSize < 1024 {
			fetchSize = 1024
		}
		if cap(c.readBuf) < fetchSize {
			c.readBuf = make([]byte, fetchSize)
		} else {
			c.readBuf = c.readBuf[:fetchSize]
		}

		n, errno := sockRecv(c.fd, c.readBuf, 0)
		if errno == wasiErrSuccess {
			c.readPollSkip = 0
			c.readPollExp = 0
			if n == 0 {
				c.readEOF = true
				c.readPos = 0
				c.readEnd = 0
				return true
			}
			c.readPos = 0
			c.readEnd = int(n)
			return true
		}
		if errno == wasiErrAgain {
			return false
		}
		c.readErr = errno
		return true
	}

	if timeoutMs <= 0 {
		if check() {
			return true
		}
		if c.readPollExp < maxPollBackoff {
			c.readPollExp++
		}
		c.readPollSkip = (1 << c.readPollExp) - 1
		return false
	}

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for {
		if check() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		SleepMs(defaultSleepMs)
	}
}

// DialTCP connects to a TCP address directly using WASIX socket functions,
// bypassing TinyGo's net.Dial which has a broken AsSlice() in WASM.
func (d *WasmNetdev) DialTCP(addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("DialTCP: invalid address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("DialTCP: invalid port %q: %w", portStr, err)
	}

	ip, err := d.GetHostByName(host)
	if err != nil {
		return nil, fmt.Errorf("DialTCP: resolve %q: %w", host, err)
	}

	fd, err := d.Socket(2, wasiSockTypeStream, wasiSockProtoTCP)
	if err != nil {
		return nil, fmt.Errorf("DialTCP: socket: %w", err)
	}

	addrPort := netip.AddrPortFrom(ip, uint16(port))
	if err := d.Connect(fd, host, addrPort); err != nil {
		d.Close(fd)
		return nil, fmt.Errorf("DialTCP: connect %s: %w", addr, err)
	}

	return &WasmTCPConn{fd: int32(fd), remoteAddr: addr}, nil
}
