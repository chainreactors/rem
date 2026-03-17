//go:build tinygo && !wasip1 && linux

package musl

/*
// See tinygo's builder/musl.go for explanation on the flags, with the following exceptions:
// - These break the build: -std=c99 -D_XOPEN_SOURCE=700
// - These are restricted by cgo: -Qunused-arguments
#cgo CFLAGS: -Werror -Wno-logical-op-parentheses -Wno-bitwise-op-parentheses -Wno-shift-op-parentheses -Wno-ignored-attributes -Wno-string-plus-int -Wno-ignored-pragmas -Wno-tautological-constant-out-of-range-compare
#cgo LDFLAGS:

#include <sys/types.h>
#include <sys/socket.h>
#include <sys/ioctl.h>
#include <netdb.h>
#include <arpa/inet.h>
#include <string.h>
#include <unistd.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <stdlib.h>
#include <fcntl.h>
#include <sys/select.h>
#include <errno.h>

int get_socket_flags(int sockfd) {
    return fcntl(sockfd, F_GETFL, 0);
}

*/
import "C"

import (
	"errors"
	"net/netip"
	"syscall"
	"time"
	"unsafe"
)

type netdever struct{}

var _ Netdever = (*netdever)(nil)

func NewNetdever() Netdever {
	return &netdever{}
}

type Netdever interface {
	GetHostByName(name string) (netip.Addr, error)
	Addr() (netip.Addr, error)
	Socket(domain int, stype int, protocol int) (int, error)
	Bind(sockfd int, ip netip.AddrPort) error
	Connect(sockfd int, host string, ip netip.AddrPort) error
	Listen(sockfd int, backlog int) error
	Accept(sockfd int) (int, netip.AddrPort, error)
	Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error)
	Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error)
	Close(sockfd int) error
	SetSockOpt(sockfd int, level int, opt int, value interface{}) error
}

var (
	C_sizeof_struct_addrinfo    = (_Cgo_ulong)(unsafe.Sizeof(C.struct_addrinfo{}))
	C_sizeof_struct_sockaddr_in = (_Cgo_uint)(unsafe.Sizeof(C.struct_sockaddr_in{}))
	C_sizeof_struct_timeval     = (_Cgo_uint)(unsafe.Sizeof(C.struct_timeval{}))
	C_sizeof_int                = (_Cgo_uint)(unsafe.Sizeof(C.int(0)))
)

func (d *netdever) GetHostByName(name string) (netip.Addr, error) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	var hints C.struct_addrinfo
	var result *C.struct_addrinfo
	C.memset(unsafe.Pointer(&hints), 0, C_sizeof_struct_addrinfo)
	hints.ai_family = C.AF_INET // IPv4 only for now.

	ret := C.getaddrinfo(cname, nil, &hints, &result)
	if ret != 0 {
		return netip.Addr{}, errors.New(C.GoString(C.gai_strerror(ret)))
	}
	defer C.freeaddrinfo(result)

	for p := result; p != nil; p = p.ai_next {
		if p.ai_family == C.AF_INET {
			addr := (*C.struct_sockaddr_in)(unsafe.Pointer(p.ai_addr))
			ip := (*[4]byte)(unsafe.Pointer(&addr.sin_addr.s_addr))
			return netip.AddrFrom4(*ip), nil
		}
	}

	return netip.Addr{}, errors.New("no IPv4 address found for host")
}

func (d *netdever) Addr() (netip.Addr, error) {
	var ifaddr, ifa *C.struct_ifaddrs
	if C.getifaddrs(&ifaddr) == -1 {
		return netip.Addr{}, errors.New("getifaddrs failed")
	}
	defer C.freeifaddrs(ifaddr)

	for ifa = ifaddr; ifa != nil; ifa = ifa.ifa_next {
		if ifa.ifa_addr == nil {
			continue
		}

		// Check for IPv4 and that the interface is up and not a loopback.
		if ifa.ifa_addr.sa_family == C.AF_INET {
			if (ifa.ifa_flags&C.IFF_UP) == 0 || (ifa.ifa_flags&C.IFF_LOOPBACK) != 0 {
				continue
			}

			addr := (*C.struct_sockaddr_in)(unsafe.Pointer(ifa.ifa_addr))
			ip := (*[4]byte)(unsafe.Pointer(&addr.sin_addr.s_addr))
			return netip.AddrFrom4(*ip), nil
		}
	}

	return netip.Addr{}, errors.New("no suitable network interface found")
}

func (d *netdever) Socket(domain int, stype int, protocol int) (int, error) {
	fd, err := C.socket(C.int(domain), C.int(stype), C.int(protocol))
	if fd < 0 {
		return -1, err
	}
	return int(fd), nil
}

func (d *netdever) Bind(sockfd int, ip netip.AddrPort) error {
	var sa C.struct_sockaddr_in
	sa.sin_family = C.AF_INET
	sa.sin_port = C.htons(C.uint16_t(ip.Port()))

	addrBytes := ip.Addr().As4()
	C.memcpy(unsafe.Pointer(&sa.sin_addr.s_addr), unsafe.Pointer(&addrBytes[0]), 4)

	ret, err := C.bind(C.int(sockfd), (*C.struct_sockaddr)(unsafe.Pointer(&sa)), C.socklen_t(C_sizeof_struct_sockaddr_in))
	if ret < 0 {
		return err
	}
	return nil
}

func (d *netdever) Connect(sockfd int, host string, ip netip.AddrPort) error {
	var sa C.struct_sockaddr_in
	sa.sin_family = C.AF_INET
	sa.sin_port = C.htons(C.uint16_t(ip.Port()))

	addrBytes := ip.Addr().As4()
	C.memcpy(unsafe.Pointer(&sa.sin_addr.s_addr), unsafe.Pointer(&addrBytes[0]), 4)

	ret, err := C.connect(C.int(sockfd), (*C.struct_sockaddr)(unsafe.Pointer(&sa)), C.socklen_t(C_sizeof_struct_sockaddr_in))
	if ret < 0 {
		return err
	}
	return nil
}

func (d *netdever) Listen(sockfd int, backlog int) error {
	ret, err := C.listen(C.int(sockfd), C.int(backlog))
	if ret < 0 {
		return err
	}
	return nil
}

func (d *netdever) Accept(sockfd int) (int, netip.AddrPort, error) {
	var sa C.struct_sockaddr_in
	var salen C.socklen_t = C_sizeof_struct_sockaddr_in

	newfd, err := C.accept(C.int(sockfd), (*C.struct_sockaddr)(unsafe.Pointer(&sa)), &salen)
	if newfd < 0 {
		return -1, netip.AddrPort{}, err
	}

	ip := (*[4]byte)(unsafe.Pointer(&sa.sin_addr.s_addr))
	port := C.ntohs(sa.sin_port)
	addr := netip.AddrFrom4(*ip)
	addrPort := netip.AddrPortFrom(addr, uint16(port))

	return int(newfd), addrPort, nil
}

func (d *netdever) Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	if !deadline.IsZero() {
		err := d.setWriteDeadline(sockfd, deadline)
		if err != nil {
			return 0, err
		}
	}

	n, err := C.send(C.int(sockfd), unsafe.Pointer(&buf[0]), C.size_t(len(buf)), C.int(flags))
	if n < 0 {
		return 0, err
	}
	return int(n), nil
}

func (d *netdever) Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	if !deadline.IsZero() {
		err := d.setReadDeadline(sockfd, deadline)
		if err != nil {
			return 0, err
		}
	}

	n, err := C.recv(C.int(sockfd), unsafe.Pointer(&buf[0]), C.size_t(len(buf)), C.int(flags))
	if n < 0 {
		return 0, err
	}
	return int(n), nil
}

func (d *netdever) Close(sockfd int) error {
	ret, err := C.close(C.int(sockfd))
	if ret < 0 {
		return err
	}
	return nil
}

func (d *netdever) SetSockOpt(sockfd int, level int, opt int, value interface{}) error {
	var val unsafe.Pointer
	var vallen C.socklen_t

	switch v := value.(type) {
	case int:
		val = unsafe.Pointer(&v)
		vallen = C_sizeof_int
	case time.Duration:
		tv := durationToTimeval(v)
		val = unsafe.Pointer(&tv)
		vallen = C_sizeof_struct_timeval
	case C.struct_timeval:
		val = unsafe.Pointer(&v)
		vallen = C_sizeof_struct_timeval
	default:
		return errors.New("unsupported socket option type")
	}

	ret, err := C.setsockopt(C.int(sockfd), C.int(level), C.int(opt), val, vallen)
	if ret < 0 {
		return err
	}
	return nil
}

func (d *netdever) setReadDeadline(sockfd int, t time.Time) error {
	return d.setDeadline(sockfd, t, syscall.SO_RCVTIMEO)
}

func (d *netdever) setWriteDeadline(sockfd int, t time.Time) error {
	return d.setDeadline(sockfd, t, syscall.SO_SNDTIMEO)
}

func (d *netdever) setDeadline(sockfd int, t time.Time, opt int) error {
	tv := C.struct_timeval{tv_sec: 0, tv_usec: 0}
	if !t.IsZero() {
		timeout := time.Until(t)
		if timeout <= 0 {
			tv.tv_usec = 1
		} else {
			tv = durationToTimeval(timeout)
		}
	}
	return d.SetSockOpt(sockfd, syscall.SOL_SOCKET, opt, tv)
}

func durationToTimeval(d time.Duration) C.struct_timeval {
	if d <= 0 {
		return C.struct_timeval{tv_sec: 0, tv_usec: 0}
	}
	return C.struct_timeval{
		tv_sec:  C.time_t(d / time.Second),
		tv_usec: C.suseconds_t(d % time.Second / time.Microsecond),
	}
}
