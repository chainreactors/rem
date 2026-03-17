//go:build tinygo && !wasip1

package windows

/*
#cgo LDFLAGS: -lws2_32
#include <winsock2.h>
#include <ws2tcpip.h>
#include <string.h>
#include <stdlib.h>

static int wsa_init() {
	WSADATA d;
	return WSAStartup(MAKEWORD(2, 2), &d);
}

static int wsa_last_error() {
	return WSAGetLastError();
}

static long long sock_create(int af, int type, int protocol) {
	SOCKET s = socket(af, type, protocol);
	if (s == INVALID_SOCKET) return -1;
	return (long long)s;
}

static void fill_addr(struct sockaddr_in *sa, unsigned char ip0, unsigned char ip1, unsigned char ip2, unsigned char ip3, unsigned short port) {
	memset(sa, 0, sizeof(*sa));
	sa->sin_family = AF_INET;
	sa->sin_port = htons(port);
	unsigned char ip[4] = {ip0, ip1, ip2, ip3};
	memcpy(&sa->sin_addr, ip, 4);
}

static int sock_connect(long long fd, unsigned char ip0, unsigned char ip1, unsigned char ip2, unsigned char ip3, unsigned short port) {
	struct sockaddr_in addr;
	fill_addr(&addr, ip0, ip1, ip2, ip3, port);
	return connect((SOCKET)fd, (struct sockaddr*)&addr, sizeof(addr));
}

static int sock_bind(long long fd, unsigned char ip0, unsigned char ip1, unsigned char ip2, unsigned char ip3, unsigned short port) {
	struct sockaddr_in addr;
	fill_addr(&addr, ip0, ip1, ip2, ip3, port);
	return bind((SOCKET)fd, (struct sockaddr*)&addr, sizeof(addr));
}

static int sock_listen(long long fd, int backlog) {
	return listen((SOCKET)fd, backlog);
}

static int sock_set_nonblock(long long fd, int nonblock) {
	u_long mode = (u_long)nonblock;
	return ioctlsocket((SOCKET)fd, FIONBIO, &mode);
}

// accept returns new socket fd via return value, remote ip/port via out params
static long long sock_accept(long long fd, unsigned char *ip_out, unsigned short *port_out) {
	struct sockaddr_in addr;
	int addrlen = sizeof(addr);
	SOCKET ns = accept((SOCKET)fd, (struct sockaddr*)&addr, &addrlen);
	if (ns == INVALID_SOCKET) return -1;
	memcpy(ip_out, &addr.sin_addr, 4);
	*port_out = ntohs(addr.sin_port);
	return (long long)ns;
}

static int sock_send(long long fd, const char *buf, int len, int flags) {
	return send((SOCKET)fd, buf, len, flags);
}

static int sock_recv(long long fd, char *buf, int len, int flags) {
	return recv((SOCKET)fd, buf, len, flags);
}

static int sock_close(long long fd) {
	return closesocket((SOCKET)fd);
}

static int sock_setsockopt(long long fd, int level, int optname, const char *optval, int optlen) {
	return setsockopt((SOCKET)fd, level, optname, optval, optlen);
}

// set recv/send timeout in milliseconds
static int sock_set_timeout(long long fd, int opt, int ms) {
	DWORD timeout = (DWORD)ms;
	return setsockopt((SOCKET)fd, SOL_SOCKET, opt, (const char*)&timeout, sizeof(timeout));
}

// set int-valued socket option
static int sock_setopt_int(long long fd, int level, int optname, int val) {
	return setsockopt((SOCKET)fd, level, optname, (const char*)&val, sizeof(val));
}

// poll socket for readability/writability, timeout in ms, returns >0 if ready
static int sock_poll(long long fd, int events, int timeout_ms) {
	WSAPOLLFD pfd;
	pfd.fd = (SOCKET)fd;
	pfd.events = (short)events;
	pfd.revents = 0;
	return WSAPoll(&pfd, 1, timeout_ms);
}

// DNS resolution via getaddrinfo, returns first IPv4 address
// ip_out must be at least 4 bytes, returns 0 on success
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
*/
import "C"

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
	"unsafe"
)

const (
	wsaeWouldBlock = 10035
	pollIn         = 0x0100 // POLLRDNORM
	pollOut        = 0x0010 // POLLWRNORM
	soRcvTimeo     = 0x1006
	soSndTimeo     = 0x1005
)

// WinsockNetdev implements Netdever using Windows Winsock2 API.
type WinsockNetdev struct{}

// New creates a WinsockNetdev and initializes Winsock2.
func New() *WinsockNetdev {
	if ret := C.wsa_init(); ret != 0 {
		panic(fmt.Sprintf("WSAStartup failed: %d", int(ret)))
	}
	return &WinsockNetdev{}
}

func (d *WinsockNetdev) GetHostByName(name string) (netip.Addr, error) {
	// Try parsing as IP first
	if addr, err := netip.ParseAddr(name); err == nil {
		return addr, nil
	}
	// DNS resolution via getaddrinfo
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var ip [4]C.uchar
	ret := C.resolve_host(cname, (*C.uchar)(unsafe.Pointer(&ip[0])))
	if ret != 0 {
		return netip.Addr{}, fmt.Errorf("DNS resolve failed: %s", name)
	}
	return netip.AddrFrom4([4]byte{byte(ip[0]), byte(ip[1]), byte(ip[2]), byte(ip[3])}), nil
}

func (d *WinsockNetdev) Addr() (netip.Addr, error) {
	return netip.MustParseAddr("127.0.0.1"), nil
}

func (d *WinsockNetdev) Socket(domain int, stype int, protocol int) (int, error) {
	fd := C.sock_create(C.int(domain), C.int(stype), C.int(protocol))
	if fd < 0 {
		return -1, wsaError("socket")
	}
	// Set non-blocking mode so we don't block TinyGo's cooperative scheduler
	C.sock_set_nonblock(C.longlong(fd), 1)
	return int(fd), nil
}

func (d *WinsockNetdev) Bind(sockfd int, ip netip.AddrPort) error {
	b := ip.Addr().As4()
	ret := C.sock_bind(C.longlong(sockfd),
		C.uchar(b[0]), C.uchar(b[1]), C.uchar(b[2]), C.uchar(b[3]),
		C.ushort(ip.Port()))
	if ret != 0 {
		return wsaError("bind")
	}
	return nil
}

func (d *WinsockNetdev) Connect(sockfd int, host string, ip netip.AddrPort) error {
	b := ip.Addr().As4()
	ret := C.sock_connect(C.longlong(sockfd),
		C.uchar(b[0]), C.uchar(b[1]), C.uchar(b[2]), C.uchar(b[3]),
		C.ushort(ip.Port()))
	if ret != 0 {
		code := int(C.wsa_last_error())
		// WSAEWOULDBLOCK (10035) or WSAEINPROGRESS (10036) means connect is in progress
		if code == wsaeWouldBlock || code == 10036 {
			// Poll for writability to know when connect completes.
			// Use non-blocking poll + time.Sleep for proper cooperative scheduling
			// in multi-goroutine TinyGo programs.
			deadline := time.Now().Add(30 * time.Second)
			for {
				pr := C.sock_poll(C.longlong(sockfd), C.int(pollOut), 0)
				if pr > 0 {
					return nil
				}
				if pr < 0 {
					return fmt.Errorf("connect poll: winsock error %d", int(C.wsa_last_error()))
				}
				if time.Now().After(deadline) {
					return errors.New("connect: timeout")
				}
				time.Sleep(time.Millisecond)
			}
		}
		return fmt.Errorf("connect: winsock error %d", code)
	}
	return nil
}

func (d *WinsockNetdev) Listen(sockfd int, backlog int) error {
	ret := C.sock_listen(C.longlong(sockfd), C.int(backlog))
	if ret != 0 {
		return wsaError("listen")
	}
	return nil
}

func (d *WinsockNetdev) Accept(sockfd int) (int, netip.AddrPort, error) {
	var ipOut [4]C.uchar
	var portOut C.ushort

	for {
		newfd := C.sock_accept(C.longlong(sockfd),
			(*C.uchar)(unsafe.Pointer(&ipOut[0])),
			(*C.ushort)(unsafe.Pointer(&portOut)))
		if newfd >= 0 {
			// New connection accepted - set it non-blocking too
			C.sock_set_nonblock(C.longlong(newfd), 1)
			addr := netip.AddrFrom4([4]byte{byte(ipOut[0]), byte(ipOut[1]), byte(ipOut[2]), byte(ipOut[3])})
			return int(newfd), netip.AddrPortFrom(addr, uint16(portOut)), nil
		}
		code := int(C.wsa_last_error())
		if code == wsaeWouldBlock {
			// No pending connection, sleep to yield properly in cooperative scheduler
			time.Sleep(time.Millisecond)
			continue
		}
		return -1, netip.AddrPort{}, fmt.Errorf("accept: winsock error %d", code)
	}
}

func (d *WinsockNetdev) Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	for {
		n := C.sock_send(C.longlong(sockfd), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)), C.int(flags))
		if n > 0 {
			return int(n), nil
		}
		code := int(C.wsa_last_error())
		if n == 0 || code == wsaeWouldBlock {
			if !deadline.IsZero() && time.Now().After(deadline) {
				return 0, errors.New("send: deadline exceeded")
			}
			time.Sleep(time.Millisecond)
			continue
		}
		return 0, fmt.Errorf("send: winsock error %d", code)
	}
}

func (d *WinsockNetdev) Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	for {
		n := C.sock_recv(C.longlong(sockfd), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf)), C.int(flags))
		if n > 0 {
			return int(n), nil
		}
		if n == 0 {
			return 0, errors.New("EOF")
		}
		code := int(C.wsa_last_error())
		if code == wsaeWouldBlock {
			if !deadline.IsZero() && time.Now().After(deadline) {
				return 0, errors.New("recv: deadline exceeded")
			}
			time.Sleep(time.Millisecond)
			continue
		}
		return 0, fmt.Errorf("recv: winsock error %d", code)
	}
}

func (d *WinsockNetdev) Close(sockfd int) error {
	ret := C.sock_close(C.longlong(sockfd))
	if ret != 0 {
		return wsaError("closesocket")
	}
	return nil
}

func (d *WinsockNetdev) SetSockOpt(sockfd int, level int, opt int, value interface{}) error {
	switch v := value.(type) {
	case int:
		ret := C.sock_setopt_int(C.longlong(sockfd), C.int(level), C.int(opt), C.int(v))
		if ret != 0 {
			return wsaError("setsockopt")
		}
	case bool:
		val := 0
		if v {
			val = 1
		}
		ret := C.sock_setopt_int(C.longlong(sockfd), C.int(level), C.int(opt), C.int(val))
		if ret != 0 {
			return wsaError("setsockopt")
		}
	case time.Duration:
		ms := int(v / time.Millisecond)
		if ms <= 0 {
			ms = 1
		}
		ret := C.sock_set_timeout(C.longlong(sockfd), C.int(opt), C.int(ms))
		if ret != 0 {
			return wsaError("setsockopt")
		}
	default:
		return fmt.Errorf("unsupported setsockopt value type: %T", value)
	}
	return nil
}

func wsaError(op string) error {
	code := int(C.wsa_last_error())
	return fmt.Errorf("%s: winsock error %d", op, code)
}
