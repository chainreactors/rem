//go:build tinygo

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/protocol/serve/portforward"
	"github.com/chainreactors/rem/protocol/serve/raw"
	"github.com/chainreactors/rem/protocol/serve/socks"
	"github.com/chainreactors/rem/protocol/tunnel/memory"
	"github.com/chainreactors/rem/protocol/tunnel/tcp"
	"github.com/chainreactors/rem/protocol/wrapper"
	"github.com/chainreactors/rem/runner"
	"github.com/chainreactors/rem/x/netdev"
	"github.com/chainreactors/rem/x/netdev/wasm"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

var initOnce sync.Once

// activeAgent holds the agent created by RemDial for polling in WasmResume.
var activeAgent *agent.Agent

var virtualBridgeSeq uint64

var virtualConnsByBridge sync.Map // map[uint64]*virtualMemoryConn

type virtualAddr string

func (a virtualAddr) Network() string { return "memory" }
func (a virtualAddr) String() string  { return string(a) }

type virtualMemoryConn struct {
	id          uint64
	source      string
	destination string
	agent       *agent.Agent

	recvBuf      []byte
	remoteClosed bool
	closed       bool
}

var maxVirtualPacketData = 64 * 1024

func (c *virtualMemoryConn) appendPacket(data []byte) {
	c.recvBuf = append(c.recvBuf, data...)
}

func (c *virtualMemoryConn) markRemoteClosed() {
	c.remoteClosed = true
}

func (c *virtualMemoryConn) isRemoteClosed() bool {
	return c.remoteClosed
}

func (c *virtualMemoryConn) tryConsumeRelayReply() (done bool, ok bool, err error) {
	if len(c.recvBuf) < 4 {
		return false, false, nil
	}
	if c.recvBuf[0] != 0x05 {
		return true, false, fmt.Errorf("invalid SOCKS version: %d", c.recvBuf[0])
	}

	atyp := c.recvBuf[3]
	total := 0
	switch atyp {
	case 0x01: // IPv4
		total = 4 + 4 + 2
	case 0x04: // IPv6
		total = 4 + 16 + 2
	case 0x03: // FQDN
		if len(c.recvBuf) < 5 {
			return false, false, nil
		}
		total = 4 + 1 + int(c.recvBuf[4]) + 2
	default:
		return true, false, fmt.Errorf("unsupported SOCKS ATYP: %d", atyp)
	}

	if len(c.recvBuf) < total {
		return false, false, nil
	}

	reply := c.recvBuf[:total]
	c.recvBuf = c.recvBuf[total:]

	if reply[1] != 0x00 {
		return true, false, fmt.Errorf("SOCKS connect failed, REP=%d", reply[1])
	}
	return true, true, nil
}

func (c *virtualMemoryConn) Read(p []byte) (int, error) {
	if len(c.recvBuf) == 0 {
		if c.closed || c.remoteClosed {
			return 0, io.EOF
		}
		return 0, io.EOF
	}

	n := copy(p, c.recvBuf)
	c.recvBuf = c.recvBuf[n:]
	return n, nil
}

func (c *virtualMemoryConn) Write(p []byte) (int, error) {
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}

	written := 0
	for written < len(p) {
		end := written + maxVirtualPacketData
		if end > len(p) {
			end = len(p)
		}
		payload := make([]byte, end-written)
		copy(payload, p[written:end])
		if err := c.agent.Send(message.Wrap(c.source, c.destination, &message.Packet{ID: c.id, Data: payload})); err != nil {
			if written > 0 {
				return written, err
			}
			return 0, err
		}
		written = end
	}

	return written, nil
}

func (c *virtualMemoryConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true

	_ = c.agent.Send(message.Wrap(c.source, c.destination, &message.ConnEnd{ID: c.id}))
	return nil
}

func (c *virtualMemoryConn) LocalAddr() net.Addr              { return virtualAddr("memory://local") }
func (c *virtualMemoryConn) RemoteAddr() net.Addr             { return virtualAddr("memory://remote") }
func (c *virtualMemoryConn) SetDeadline(time.Time) error      { return nil }
func (c *virtualMemoryConn) SetReadDeadline(time.Time) error  { return nil }
func (c *virtualMemoryConn) SetWriteDeadline(time.Time) error { return nil }

func bridgeEndpoints(a *agent.Agent) (source string, destination string, err error) {
	if a == nil || a.Controller == nil {
		return "", "", fmt.Errorf("agent/controller not initialized")
	}
	control := a.Controller
	if a.Redirect == control.Destination {
		destination = control.Destination
		source = control.Source
	} else {
		destination = control.Source
		source = control.Destination
	}
	return source, destination, nil
}

// wasmTCPDialer bypasses TinyGo's broken net.DialTCP which rejects 16-byte
// IPv4 addresses (Go's net.ParseIP returns 16-byte IPv6-mapped form, but
// TinyGo checks len(IP) != 4). WasmNetdev.DialTCP normalizes to 4-byte IPv4.
type wasmTCPDialer struct {
	meta core.Metas
	dev  *wasm.WasmNetdev
}

func (d *wasmTCPDialer) Dial(dst string) (net.Conn, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	d.meta["url"] = u
	return d.dev.DialTCP(u.Host)
}

func ensureInit() {
	initOnce.Do(func() {
		dev := wasm.New()
		netdev.UseNetdev(dev)
		utils.Log = logs.NewLogger(100)
		proxyclient.InitBuiltinSchemes()

		// Override TCP dialer — TinyGo's net.DialTCP rejects 16-byte IPv4
		// addresses (len(IP) != 4) because Go's net.ParseIP returns 16-byte
		// IPv6-mapped form. WasmNetdev.DialTCP normalizes to 4-byte IPv4.
		core.DialerRegister(core.TCPTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
			return &wasmTCPDialer{meta: core.GetMetas(ctx), dev: dev}, nil
		})

		// Override net.Dial for SOCKS5 outbound (NetDialer) — same 16-byte issue.
		core.NetDialFunc = func(network, address string) (net.Conn, error) {
			return dev.DialTCP(address)
		}

		// Override time functions — TinyGo WASM's time.Sleep/time.After panic
		// with nil pointer dereference (scheduler timer infrastructure not initialized).
		// Use host_sleep_ms in short bursts interleaved with channel-based yield.
		// NOTE: runtime.Gosched() is unsafe in TinyGo WASM exported paths,
		// so we only use host-based sleep plus wasm.MaybeGosched().
		utils.Sleep = func(d time.Duration) {
			ms := d.Milliseconds()
			for ms > 0 {
				step := int64(10) // 10ms max per iteration
				if step > ms {
					step = ms
				}
				wasm.SleepMs(int(step))
				ms -= step
				wasm.MaybeGosched()
			}
		}
		utils.After = func(d time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			go func() {
				utils.Sleep(d)
				ch <- time.Now()
			}()
			return ch
		}

		// TinyGo WASM loses all package-level state set during init().
		// Re-register everything explicitly here.

		// Tunnels (TCP dialer already registered above with wasmTCPDialer)
		core.ListenerRegister(core.TCPTunnel, func(ctx context.Context) (core.TunnelListener, error) {
			return tcp.NewTCPListener(ctx), nil
		})
		core.DialerRegister(core.MemoryTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
			return memory.NewMemoryDialer(ctx), nil
		})
		core.ListenerRegister(core.MemoryTunnel, func(ctx context.Context) (core.TunnelListener, error) {
			return memory.NewMemoryListener(ctx), nil
		})
		proxyclient.RegisterScheme("MEMORY", memory.NewMemoryProxyClient)

		// Services
		core.InboundRegister(core.RawServe, raw.NewRawInbound)
		core.OutboundRegister(core.Socks5Serve, socks.NewSocks5Outbound)
		core.InboundRegister(core.Socks5Serve, socks.NewSocksInbound)
		core.OutboundRegister(core.RawServe, socks.NewRelaySocks5Outbound)
		core.InboundRegister(core.PortForwardServe, portforward.NewForwardInbound)
		core.OutboundRegister(core.PortForwardServe, portforward.NewForwardOutbound)
		// HTTP serve removed to reduce WASM binary size (~200KB savings)

		// Wrappers
		core.WrapperRegister(core.CryptorWrapper, func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
			return wrapper.NewCryptorWrapper(r, w, opt)
		})
		core.WrapperRegister(core.PaddingWrapper, func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
			return wrapper.NewPaddingWrapper(r, w, opt), nil
		})
		core.AvailableWrappers = append(core.AvailableWrappers, core.CryptorWrapper)
	})
}

// gostring converts a null-terminated C string to a Go string.
func gostring(ptr *byte) string {
	if ptr == nil {
		return ""
	}
	n := 0
	for {
		if *(*byte)(unsafe.Add(unsafe.Pointer(ptr), n)) == 0 {
			break
		}
		n++
	}
	return string(unsafe.Slice(ptr, n))
}

// cstring converts a Go string to a null-terminated C string.
// The caller is responsible for freeing the memory via FreeCString.
var pinned [][]byte

func cstring(s string) *byte {
	buf := make([]byte, len(s)+1)
	copy(buf, s)
	pinned = append(pinned, buf) // prevent GC
	return &buf[0]
}

//export FreeCString
func FreeCString(ptr *byte) {
	for i, buf := range pinned {
		if &buf[0] == ptr {
			pinned = append(pinned[:i], pinned[i+1:]...)
			return
		}
	}
}

// splitArgs splits a command line string into arguments, handling basic quoting.
func splitArgs(s string) []string {
	var args []string
	var current strings.Builder
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			} else {
				current.WriteByte(c)
			}
		} else if c == '"' || c == '\'' {
			inQuote = c
		} else if c == ' ' || c == '\t' {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// parseOptions parses command line args into runner.Options using flag package.
func parseOptions(args []string) (*runner.Options, error) {
	var opt runner.Options
	fs := flag.NewFlagSet("rem", flag.ContinueOnError)

	fs.StringVar(&opt.Mod, "m", "", "")
	var localAddr, remoteAddr stringSlice
	fs.Var(&localAddr, "l", "")
	fs.Var(&remoteAddr, "r", "")
	fs.StringVar(&opt.Key, "k", "", "")
	fs.BoolVar(&opt.Quiet, "q", false, "")
	fs.BoolVar(&opt.Debug, "debug", false, "")
	fs.BoolVar(&opt.Detail, "detail", false, "")
	fs.StringVar(&opt.Alias, "a", "", "")
	fs.StringVar(&opt.Redirect, "d", "", "")
	fs.BoolVar(&opt.ConnectOnly, "n", false, "")
	fs.StringVar(&opt.IP, "i", "", "")

	var clientAddr, serverAddr stringSlice
	fs.Var(&clientAddr, "c", "")
	fs.Var(&serverAddr, "s", "")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	opt.ClientAddr = []string(clientAddr)
	opt.ServerAddr = []string(serverAddr)
	opt.LocalAddr = []string(localAddr)
	opt.RemoteAddr = []string(remoteAddr)
	return &opt, nil
}

//export InitDialer
func InitDialer() int32 {
	ensureInit()
	return 0
}

//export RemDial
func RemDial(cmdline *byte) (retPtr *byte, retCode int32) {
	ensureInit()

	cmdStr := gostring(cmdline)
	args := splitArgs(cmdStr)
	option, err := parseOptions(args)
	if err != nil {
		return nil, ErrCmdParseFailed
	}

	// Memory bridge requires proxy mode so that CLIENT has Inbound (reads SOCKS5
	// from memory pipe, relays through bridge) and SERVER has Outbound (dials target).
	// If no local listener is specified, default to memory+socks5 for MemoryDial support.
	if len(option.LocalAddr) == 0 {
		option.LocalAddr = []string{"memory+socks5://:@memory"}
		if option.Mod == "" || option.Mod == "reverse" {
			option.Mod = "proxy"
		}
	}

	// _start mode uses stdout as a binary RPC transport. Any textual logs to
	// stdout/stderr can corrupt framing and cause random host-side error codes.
	// Force quiet logging for this entry regardless of cmdline flags.
	option.Debug = false
	option.Detail = false
	option.Quiet = true
	utils.Log = logs.NewLogger(100)

	// Prevent Prepare() from calling utils.Log.Init() which opens a log file
	// and crashes in WASM (no filesystem). We handle log level above instead.
	savedDebug := option.Debug
	option.Debug = false
	r, err := option.Prepare()
	option.Debug = savedDebug
	if err != nil {
		return nil, ErrPrepareFailed
	}
	if len(r.ConsoleURLs) == 0 {
		return nil, ErrNoConsoleURL
	}

	conURL := r.ConsoleURLs[0]
	console, err := runner.NewConsole(r, r.NewURLs(conURL))
	if err != nil {
		return nil, ErrCreateConsole
	}

	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		return nil, ErrDialFailed
	}

	// Run initialization synchronously. HandlerInit in proxy mode calls
	// handlerInbound → ListenAndServe, which sets up the memory listener
	// and Inbound. The serve goroutine created by ListenAndServe won't run
	// (TinyGo WASM can't switch goroutines in exported functions), but that's
	// OK — PollOnce handles accept polling instead.
	a.Recover = nil

	err = a.HandlerInit()
	if err != nil {
		return nil, ErrDialFailed
	}

	// Don't start background goroutines — TinyGo WASM's asyncify scheduler
	// cannot switch goroutines within exported function contexts. Instead,
	// WasmResume calls agent.PollOnce() to drive send/receive/dispatch/accept.

	// Enable SchedReady so maybeGosched (now just hostSleepMs) works in
	// WasmTCPConn busy-wait loops during PollOnce's ReadMsg/WriteMsg.
	wasm.SchedReady = true

	// Store agent for WasmResume polling
	activeAgent = a
	activeAgent.SetPollHooks(func(id uint64, data []byte) bool {
		v, ok := virtualConnsByBridge.Load(id)
		if !ok {
			return false
		}
		v.(*virtualMemoryConn).appendPacket(data)
		return true
	}, func(id uint64) bool {
		v, ok := virtualConnsByBridge.Load(id)
		if !ok {
			return false
		}
		v.(*virtualMemoryConn).markRemoteClosed()
		return true
	})

	return cstring(a.ID), 0
}

//export WasmResume
func WasmResume(iterations int32) int32 {
	if activeAgent == nil {
		return -1
	}

	for i := int32(0); i < iterations; i++ {
		activeAgent.PollOnce()
		// Sleep less frequently to reduce scheduler overhead while still
		// avoiding hot busy-spin when the host calls large iteration counts.
		if (i & 0x3F) == 0x3F { // every 64 iterations
			wasm.SleepMs(1)
		}
	}
	return 0
}

//export MemoryDial
func MemoryDial(memhandle *byte, dst *byte) (int32, int32) {
	ensureInit()
	dstStr := gostring(dst)
	memhandleStr := gostring(memhandle)
	if dstStr == "" {
		return 0, ErrArgsParseFailed
	}
	if memhandleStr == "" {
		return 0, ErrArgsParseFailed
	}
	if activeAgent == nil {
		return 0, ErrDialFailed
	}

	source, destination, err := bridgeEndpoints(activeAgent)
	if err != nil {
		return 0, ErrDialFailed
	}

	bridgeID := atomic.AddUint64(&virtualBridgeSeq, 1)
	vconn := &virtualMemoryConn{
		id:          bridgeID,
		source:      source,
		destination: destination,
		agent:       activeAgent,
	}
	virtualConnsByBridge.Store(bridgeID, vconn)

	startMsg := message.Wrap(source, destination, &message.ConnStart{
		ID:          bridgeID,
		Source:      source,
		Destination: destination,
	})
	if err := activeAgent.Send(startMsg); err != nil {
		virtualConnsByBridge.Delete(bridgeID)
		return 0, ErrDialFailed
	}

	relayReq, err := socks5.NewRelay(dstStr)
	if err != nil {
		virtualConnsByBridge.Delete(bridgeID)
		return 0, ErrArgsParseFailed
	}
	if _, err := vconn.Write(relayReq.BuildRelay()); err != nil {
		virtualConnsByBridge.Delete(bridgeID)
		return 0, ErrDialFailed
	}

	const maxHandshakeTicks = 10000 // ~10s
	for i := 0; i < maxHandshakeTicks; i++ {
		activeAgent.PollOnce()
		done, ok, err := vconn.tryConsumeRelayReply()
		if err != nil {
			vconn.Close()
			virtualConnsByBridge.Delete(bridgeID)
			return 0, ErrDialFailed
		}
		if done {
			if !ok {
				vconn.Close()
				virtualConnsByBridge.Delete(bridgeID)
				return 0, ErrDialFailed
			}
			connHandle := int32(rand.Intn(0x7FFFFFFF))
			conns.Store(connHandle, vconn)
			return connHandle, 0
		}
		if vconn.isRemoteClosed() {
			vconn.Close()
			virtualConnsByBridge.Delete(bridgeID)
			return 0, ErrDialFailed
		}
		if (i & 0x3F) == 0x3F { // every 64 iterations
			wasm.SleepMs(1)
		}
	}

	vconn.Close()
	virtualConnsByBridge.Delete(bridgeID)
	return 0, ErrDialFailed
}

//export MemoryRead
func MemoryRead(chandle int32, buf unsafe.Pointer, size int32) (int32, int32) {
	conn, ok := conns.Load(chandle)
	if !ok {
		return 0, ErrArgsParseFailed
	}
	if size <= 0 {
		return 0, 0
	}

	buffer := (*[1 << 30]byte)(buf)[:int(size):int(size)]
	var n int
	var err error

	switch c := conn.(type) {
	case *virtualMemoryConn:
		const maxReadTicks = 10000 // ~10s
		for i := 0; i < maxReadTicks; i++ {
			if activeAgent != nil {
				activeAgent.PollOnce()
			}
			n, err = c.Read(buffer)
			if n > 0 {
				break
			}
			if err != nil && err != io.EOF {
				return 0, ErrDialFailed
			}
			if c.isRemoteClosed() {
				return 0, 0
			}
			if (i & 0x3F) == 0x3F { // every 64 iterations
				wasm.SleepMs(1)
			}
		}
	default:
		n, err = c.(net.Conn).Read(buffer)
		if err != nil && err != io.EOF {
			return 0, ErrDialFailed
		}
	}

	return int32(n), 0
}

//export MemoryWrite
func MemoryWrite(chandle int32, buf unsafe.Pointer, size int32) (int32, int32) {
	conn, ok := conns.Load(chandle)
	if !ok {
		return 0, ErrArgsParseFailed
	}
	if size <= 0 {
		return 0, 0
	}

	buffer := (*[1 << 30]byte)(buf)[:int(size):int(size)]

	var n int
	var err error
	switch c := conn.(type) {
	case *virtualMemoryConn:
		n, err = c.Write(buffer)
		if err == nil && activeAgent != nil {
			activeAgent.PollOnce()
		}
	default:
		n, err = c.(net.Conn).Write(buffer)
	}
	if err != nil {
		return 0, ErrDialFailed
	}

	return int32(n), 0
}

//export MemoryClose
func MemoryClose(chandle int32) int32 {
	conn, ok := conns.Load(chandle)
	if !ok {
		return ErrArgsParseFailed
	}

	switch c := conn.(type) {
	case *virtualMemoryConn:
		if err := c.Close(); err != nil {
			return ErrDialFailed
		}
		virtualConnsByBridge.Delete(c.id)
	default:
		err := c.(net.Conn).Close()
		if err != nil {
			return ErrDialFailed
		}
	}

	conns.Delete(chandle)
	return 0
}

//export CleanupAgent
func CleanupAgent() {
	conns.Range(func(key, value interface{}) bool {
		switch c := value.(type) {
		case *virtualMemoryConn:
			_ = c.Close()
			virtualConnsByBridge.Delete(c.id)
		case net.Conn:
			_ = c.Close()
		}
		conns.Delete(key)
		return true
	})

	agent.Agents.Map.Range(func(key, value interface{}) bool {
		if a, ok := value.(*agent.Agent); ok {
			a.ClearPollHooks()
			a.Close(nil)
		}
		return true
	})
	activeAgent = nil
}
