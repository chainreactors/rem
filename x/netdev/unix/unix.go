//go:build tinygo && !wasip1 && darwin

package unix

/*
#cgo LDFLAGS: -lc
#include <sys/types.h>
#include <sys/socket.h>
#include <netdb.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <poll.h>
#include <fcntl.h>
#include <unistd.h>
#include <string.h>
#include <stdlib.h>
#include <errno.h>
#include <sys/time.h>

static int errno_value() {
	return errno;
}

static int sock_create(int af, int type, int protocol) {
	return socket(af, type, protocol);
}

static void fill_addr(struct sockaddr_in *sa, unsigned char ip0, unsigned char ip1, unsigned char ip2, unsigned char ip3, unsigned short port) {
	memset(sa, 0, sizeof(*sa));
	sa->sin_family = AF_INET;
	sa->sin_port = htons(port);
	unsigned char ip[4] = {ip0, ip1, ip2, ip3};
	memcpy(&sa->sin_addr, ip, 4);
}

static int sock_connect(int fd, unsigned char ip0, unsigned char ip1, unsigned char ip2, unsigned char ip3, unsigned short port) {
	struct sockaddr_in addr;
	fill_addr(&addr, ip0, ip1, ip2, ip3, port);
	return connect(fd, (struct sockaddr*)&addr, sizeof(addr));
}

static int sock_connect_result(int fd) {
	int err = 0;
	socklen_t len = sizeof(err);
	if (getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &len) != 0) {
		return -1;
	}
	return err;
}

static int sock_bind(int fd, unsigned char ip0, unsigned char ip1, unsigned char ip2, unsigned char ip3, unsigned short port) {
	struct sockaddr_in addr;
	fill_addr(&addr, ip0, ip1, ip2, ip3, port);
	return bind(fd, (struct sockaddr*)&addr, sizeof(addr));
}

static int sock_listen(int fd, int backlog) {
	return listen(fd, backlog);
}

static int sock_set_nonblock(int fd, int nonblock) {
	int flags = fcntl(fd, F_GETFL, 0);
	if (flags < 0) {
		return -1;
	}
	if (nonblock) {
		flags |= O_NONBLOCK;
	} else {
		flags &= ~O_NONBLOCK;
	}
	return fcntl(fd, F_SETFL, flags);
}

static int sock_accept(int fd, unsigned char *ip_out, unsigned short *port_out) {
	struct sockaddr_in addr;
	socklen_t addrlen = sizeof(addr);
	int ns = accept(fd, (struct sockaddr*)&addr, &addrlen);
	if (ns < 0) {
		return -1;
	}
	memcpy(ip_out, &addr.sin_addr, 4);
	*port_out = ntohs(addr.sin_port);
	return ns;
}

static int sock_send(int fd, const char *buf, int len, int flags) {
	return send(fd, buf, len, flags);
}

static int sock_recv(int fd, char *buf, int len, int flags) {
	return recv(fd, buf, len, flags);
}

static int sock_close(int fd) {
	return close(fd);
}

static int sock_setopt_int(int fd, int level, int optname, int val) {
	return setsockopt(fd, level, optname, &val, sizeof(val));
}

static int sock_set_timeout(int fd, int optname, int ms) {
	struct timeval tv;
	if (ms < 0) {
		ms = 0;
	}
	tv.tv_sec = ms / 1000;
	tv.tv_usec = (ms % 1000) * 1000;
	return setsockopt(fd, SOL_SOCKET, optname, &tv, sizeof(tv));
}

static int sock_poll(int fd, int events, int timeout_ms) {
	struct pollfd pfd;
	pfd.fd = fd;
	pfd.events = (short)events;
	pfd.revents = 0;
	return poll(&pfd, 1, timeout_ms);
}

static int resolve_host(const char *name, unsigned char *ip_out) {
	struct addrinfo hints, *res, *p;
	memset(&hints, 0, sizeof(hints));
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;
	int ret = getaddrinfo(name, NULL, &hints, &res);
	if (ret != 0) return -1;
	for (p = res; p != NULL; p = p->ai_next) {
		if (p->ai_family == AF_INET) {
			struct sockaddr_in *sa = (struct sockaddr_in *)p->ai_addr;
			memcpy(ip_out, &sa->sin_addr, 4);
			freeaddrinfo(res);
			return 0;
		}
	}
	freeaddrinfo(res);
	return -1;
}

static int local_addr(unsigned char *ip_out) {
	struct ifaddrs *ifaddr, *ifa;
	if (getifaddrs(&ifaddr) == -1) {
		return -1;
	}
	for (ifa = ifaddr; ifa != NULL; ifa = ifa->ifa_next) {
		if (ifa->ifa_addr == NULL) {
			continue;
		}
		if (ifa->ifa_addr->sa_family != AF_INET) {
			continue;
		}
		if ((ifa->ifa_flags & IFF_UP) == 0 || (ifa->ifa_flags & IFF_LOOPBACK) != 0) {
			continue;
		}
		struct sockaddr_in *sa = (struct sockaddr_in *)ifa->ifa_addr;
		memcpy(ip_out, &sa->sin_addr, 4);
		freeifaddrs(ifaddr);
		return 0;
	}
	freeifaddrs(ifaddr);
	return -1;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
	"unsafe"
)

const connectTimeout = 30 * time.Second

// UnixNetdev implements Netdever using POSIX sockets on Linux/macOS.
type UnixNetdev struct{}

// New creates a UnixNetdev.
func New() *UnixNetdev {
	return &UnixNetdev{}
}

func (d *UnixNetdev) GetHostByName(name string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(name); err == nil {
		if addr.Is4() || addr.Is4In6() {
			return netip.AddrFrom4(addr.As4()), nil
		}
		return netip.Addr{}, fmt.Errorf("only IPv4 supported: %s", name)
	}
	if name == "" {
		return netip.Addr{}, errors.New("resolve: empty host")
	}

	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	var ip [4]C.uchar
	ret := C.resolve_host(cname, (*C.uchar)(unsafe.Pointer(&ip[0])))
	if ret != 0 {
		return netip.Addr{}, fmt.Errorf("DNS resolve failed: %s", name)
	}
	return netip.AddrFrom4([4]byte{byte(ip[0]), byte(ip[1]), byte(ip[2]), byte(ip[3])}), nil
}

func (d *UnixNetdev) Addr() (netip.Addr, error) {
	var ip [4]C.uchar
	ret := C.local_addr((*C.uchar)(unsafe.Pointer(&ip[0])))
	if ret == 0 {
		return netip.AddrFrom4([4]byte{byte(ip[0]), byte(ip[1]), byte(ip[2]), byte(ip[3])}), nil
	}
	return netip.MustParseAddr("127.0.0.1"), nil
}

func (d *UnixNetdev) Socket(domain int, stype int, protocol int) (int, error) {
	fd := C.sock_create(C.int(domain), C.int(stype), C.int(protocol))
	if fd < 0 {
		return -1, errnoError("socket")
	}
	if C.sock_set_nonblock(fd, 1) != 0 {
		_ = C.sock_close(fd)
		return -1, errnoError("fcntl(O_NONBLOCK)")
	}
	return int(fd), nil
}

func (d *UnixNetdev) Bind(sockfd int, ip netip.AddrPort) error {
	b, err := ipv4Bytes(ip.Addr(), "bind")
	if err != nil {
		return err
	}
	ret := C.sock_bind(C.int(sockfd),
		C.uchar(b[0]), C.uchar(b[1]), C.uchar(b[2]), C.uchar(b[3]),
		C.ushort(ip.Port()))
	if ret != 0 {
		return errnoError("bind")
	}
	return nil
}

func (d *UnixNetdev) Connect(sockfd int, host string, ip netip.AddrPort) error {
	_ = host
	b, err := ipv4Bytes(ip.Addr(), "connect")
	if err != nil {
		return err
	}
	ret := C.sock_connect(C.int(sockfd),
		C.uchar(b[0]), C.uchar(b[1]), C.uchar(b[2]), C.uchar(b[3]),
		C.ushort(ip.Port()))
	if ret == 0 {
		return nil
	}

	code := lastErrno()
	if code != errInProgress && !isWouldBlock(code) {
		return errnoWithCode("connect", code)
	}

	deadline := time.Now().Add(connectTimeout)
	for {
		pr := C.sock_poll(C.int(sockfd), C.int(pollOut), 0)
		if pr > 0 {
			soerr := int(C.sock_connect_result(C.int(sockfd)))
			if soerr == 0 {
				return nil
			}
			if soerr < 0 {
				return errnoError("connect")
			}
			return errnoWithCode("connect", soerr)
		}
		if pr < 0 {
			code = lastErrno()
			if code == errIntr {
				continue
			}
			return errnoWithCode("connect poll", code)
		}
		if time.Now().After(deadline) {
			return errors.New("connect: timeout")
		}
		time.Sleep(time.Millisecond)
	}
}

func (d *UnixNetdev) Listen(sockfd int, backlog int) error {
	ret := C.sock_listen(C.int(sockfd), C.int(backlog))
	if ret != 0 {
		return errnoError("listen")
	}
	return nil
}

func (d *UnixNetdev) Accept(sockfd int) (int, netip.AddrPort, error) {
	var ipOut [4]C.uchar
	var portOut C.ushort

	for {
		newfd := C.sock_accept(C.int(sockfd),
			(*C.uchar)(unsafe.Pointer(&ipOut[0])),
			(*C.ushort)(unsafe.Pointer(&portOut)))
		if newfd >= 0 {
			if C.sock_set_nonblock(C.int(newfd), 1) != 0 {
				_ = C.sock_close(C.int(newfd))
				return -1, netip.AddrPort{}, errnoError("accept set nonblock")
			}
			addr := netip.AddrFrom4([4]byte{byte(ipOut[0]), byte(ipOut[1]), byte(ipOut[2]), byte(ipOut[3])})
			return int(newfd), netip.AddrPortFrom(addr, uint16(portOut)), nil
		}

		code := lastErrno()
		if isWouldBlock(code) || code == errIntr {
			time.Sleep(time.Millisecond)
			continue
		}
		return -1, netip.AddrPort{}, errnoWithCode("accept", code)
	}
}

func (d *UnixNetdev) Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	for {
		n := C.sock_send(C.int(sockfd), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)), C.int(flags))
		if n > 0 {
			return int(n), nil
		}

		if n == 0 {
			if !deadline.IsZero() && time.Now().After(deadline) {
				return 0, errors.New("send: deadline exceeded")
			}
			time.Sleep(time.Millisecond)
			continue
		}

		code := lastErrno()
		if isWouldBlock(code) || code == errIntr {
			if !deadline.IsZero() && time.Now().After(deadline) {
				return 0, errors.New("send: deadline exceeded")
			}
			time.Sleep(time.Millisecond)
			continue
		}
		return 0, errnoWithCode("send", code)
	}
}

func (d *UnixNetdev) Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	for {
		n := C.sock_recv(C.int(sockfd), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)), C.int(flags))
		if n > 0 {
			return int(n), nil
		}
		if n == 0 {
			return 0, errors.New("EOF")
		}

		code := lastErrno()
		if isWouldBlock(code) || code == errIntr {
			if !deadline.IsZero() && time.Now().After(deadline) {
				return 0, errors.New("recv: deadline exceeded")
			}
			time.Sleep(time.Millisecond)
			continue
		}
		return 0, errnoWithCode("recv", code)
	}
}

func (d *UnixNetdev) Close(sockfd int) error {
	ret := C.sock_close(C.int(sockfd))
	if ret != 0 {
		return errnoError("close")
	}
	return nil
}

func (d *UnixNetdev) SetSockOpt(sockfd int, level int, opt int, value interface{}) error {
	switch v := value.(type) {
	case int:
		ret := C.sock_setopt_int(C.int(sockfd), C.int(level), C.int(opt), C.int(v))
		if ret != 0 {
			return errnoError("setsockopt")
		}
	case bool:
		val := 0
		if v {
			val = 1
		}
		ret := C.sock_setopt_int(C.int(sockfd), C.int(level), C.int(opt), C.int(val))
		if ret != 0 {
			return errnoError("setsockopt")
		}
	case time.Duration:
		ms := int(v / time.Millisecond)
		if v > 0 && ms == 0 {
			ms = 1
		}
		ret := C.sock_set_timeout(C.int(sockfd), C.int(opt), C.int(ms))
		if ret != 0 {
			return errnoError("setsockopt")
		}
	default:
		return fmt.Errorf("unsupported setsockopt value type: %T", value)
	}
	return nil
}

func ipv4Bytes(addr netip.Addr, op string) ([4]byte, error) {
	if addr.Is4() || addr.Is4In6() {
		return addr.As4(), nil
	}
	return [4]byte{}, fmt.Errorf("%s: only IPv4 supported (%s)", op, addr)
}

func lastErrno() int {
	return int(C.errno_value())
}

func isWouldBlock(code int) bool {
	return code == errAgain || code == errWouldBlock
}

func errnoError(op string) error {
	return errnoWithCode(op, lastErrno())
}

func errnoWithCode(op string, code int) error {
	return fmt.Errorf("%s: errno %d", op, code)
}
