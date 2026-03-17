package socks5

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/chainreactors/rem/protocol/cio"
)

const (
	ConnectCommand   = uint8(1)
	BindCommand      = uint8(2)
	AssociateCommand = uint8(3)
	ipv4Address      = uint8(1)
	fqdnAddress      = uint8(3)
	ipv6Address      = uint8(4)
)

const (
	SuccessReply uint8 = iota
	ServerFailure
	RuleFailure
	NetworkUnreachable
	HostUnreachable
	ConnectionRefused
	TTLExpired
	CommandNotSupported
	AddrTypeNotSupported
	Unknown
)

var (
	unrecognizedAddrType = fmt.Errorf("Unrecognized address type")
)

// AddressRewriter is used to rewrite a destination transparently
type AddressRewriter interface {
	Rewrite(ctx context.Context, request *Request) (context.Context, *AddrSpec)
}

// AddrSpec is used to return the target AddrSpec
// which may be specified as IPv4, IPv6, or a FQDN
type AddrSpec struct {
	FQDN string
	IP   net.IP
	Port int
}

func (a *AddrSpec) String() string {
	if a.FQDN != "" {
		return fmt.Sprintf("%s (%s):%d", a.FQDN, a.IP, a.Port)
	}
	return fmt.Sprintf("%s:%d", a.IP, a.Port)
}

// Address returns a string suitable to dial; prefer returning IP-based
// address, fallback to FQDN
func (a AddrSpec) Address() string {
	if 0 != len(a.IP) {
		return net.JoinHostPort(a.IP.String(), strconv.Itoa(a.Port))
	}
	return net.JoinHostPort(a.FQDN, strconv.Itoa(a.Port))
}

// A Request represents request received by a server
type Request struct {
	// Protocol version
	Version uint8
	// Requested command
	Command uint8
	// AuthContext provided during negotiation
	AuthContext *AuthContext
	// AddrSpec of the the network that sent the request
	RemoteAddr *AddrSpec
	// AddrSpec of the desired destination
	DestAddr *AddrSpec
	// AddrSpec of the actual destination (might be affected by rewrite)
	realDestAddr *AddrSpec
}

func (req *Request) BuildRequest() []byte {
	buf := &bytes.Buffer{}

	if req.AuthContext.Method == UserPassAuth {
		buf.Write([]byte{
			0x05, // VER
			0x01, // NMETHODS
			0x02, // METHOD (Username/Password)
		})

		username := req.AuthContext.Payload["Username"]
		buf.Write([]byte{
			0x01,                // VER
			byte(len(username)), // ULEN
		})
		buf.WriteString(username)

		password := req.AuthContext.Payload["Password"]
		buf.Write([]byte{
			byte(len(password)), // PLEN
		})
		buf.WriteString(password)
	} else {
		// 无认证
		buf.Write([]byte{
			0x05, // VER
			0x01, // NMETHODS
			0x00, // METHOD (No Auth)
		})
	}

	buf.Write([]byte{
		0x05,        // VER
		req.Command, // CMD
		0x00,        // RSV
	})

	if req.DestAddr.IP != nil {
		if ip4 := req.DestAddr.IP.To4(); ip4 != nil {
			buf.WriteByte(0x01) // IPv4
			buf.Write(ip4)
		} else {
			buf.WriteByte(0x04) // IPv6
			buf.Write(req.DestAddr.IP)
		}
	} else {
		buf.WriteByte(0x03) // Domain
		buf.WriteByte(byte(len(req.DestAddr.FQDN)))
		buf.WriteString(req.DestAddr.FQDN)
	}

	buf.Write([]byte{
		byte(req.DestAddr.Port >> 8),
		byte(req.DestAddr.Port),
	})

	return buf.Bytes()
}

func (req *Request) BuildRelay() []byte {
	buf := &bytes.Buffer{}

	buf.Write([]byte{
		0x05, // VER
		0x01, // NMETHODS
		0x03, // METHOD (Relay Auth)
	})
	buf.Write([]byte{
		0x05,        // VER
		req.Command, // CMD
		0x00,        // RSV
	})

	if req.DestAddr.IP != nil {
		if ip4 := req.DestAddr.IP.To4(); ip4 != nil {
			buf.WriteByte(0x01) // IPv4
			buf.Write(ip4)
		} else {
			buf.WriteByte(0x04) // IPv6
			buf.Write(req.DestAddr.IP)
		}
	} else {
		buf.WriteByte(0x03) // Domain
		buf.WriteByte(byte(len(req.DestAddr.FQDN)))
		buf.WriteString(req.DestAddr.FQDN)
	}

	buf.Write([]byte{
		byte(req.DestAddr.Port >> 8),
		byte(req.DestAddr.Port),
	})

	return buf.Bytes()
}

type conn interface {
	Write([]byte) (int, error)
	RemoteAddr() net.Addr
}

// NewRequest creates a new Request from the tcp connection
func NewRequest(bufConn io.Reader) (*Request, error) {
	// Read the version byte
	header := []byte{0, 0, 0}
	if _, err := io.ReadAtLeast(bufConn, header, 3); err != nil {
		return nil, fmt.Errorf("Failed to get command version: %v", err)
	}

	// Ensure we are compatible
	if header[0] != socks5Version {
		return nil, fmt.Errorf("Unsupported command version: %v", header[0])
	}

	// Read in the destination address
	dest, err := readAddrSpec(bufConn)
	if err != nil {
		return nil, err
	}

	request := &Request{
		Version:  socks5Version,
		Command:  header[1],
		DestAddr: dest,
	}

	return request, nil
}

func (s *Server) Dial(ctx context.Context, req *Request, conn net.Conn) (net.Conn, error) {
	// Check if this is allowed
	if ctx_, ok := s.Config.Rules.Allow(ctx, req); !ok {
		if err := s.SendReply(conn, RuleFailure, nil); err != nil {
			return nil, fmt.Errorf("Failed to send reply: %v", err)
		}
		return nil, fmt.Errorf("Connect to %v blocked by rules", req.DestAddr)
	} else {
		ctx = ctx_
	}

	// Attempt to connect
	dial := s.Config.Dial
	if dial == nil {
		dial = func(ctx context.Context, net_, addr string) (net.Conn, error) {
			return net.Dial(net_, addr)
		}
	}
	target, err := dial(ctx, "tcp", req.realDestAddr.Address())
	if err != nil {
		msg := err.Error()
		resp := HostUnreachable
		if strings.Contains(msg, "refused") {
			resp = ConnectionRefused
		} else if strings.Contains(msg, "network is unreachable") {
			resp = NetworkUnreachable
		}
		if err := s.SendReply(conn, resp, nil); err != nil {
			return nil, fmt.Errorf("Failed to send reply: %v", err)
		}
		return nil, fmt.Errorf("Connect to %v failed: %v", req.DestAddr, err)
	}
	return target, nil
}

// HandleConnect is used to handle a connect command
func (s *Server) HandleConnect(conn, target net.Conn) error {
	defer target.Close()

	// Send success
	var bindAddr *AddrSpec
	if local, ok := target.LocalAddr().(*net.TCPAddr); ok && local != nil {
		bindAddr = &AddrSpec{IP: local.IP, Port: local.Port}
	}

	if err := s.SendReply(conn, SuccessReply, bindAddr); err != nil {
		return fmt.Errorf("Failed to send reply: %v", err)
	}

	cio.Join(conn, target)

	// Wait
	//for i := 0; i < 2; i++ {
	//	e := <-errCh
	//	if e != nil {
	//		// return from this function closes target (and conn).
	//		return e
	//	}
	//}
	return nil
}

// handleBind is used to handle a connect command
func (s *Server) handleBind(ctx context.Context, conn conn, req *Request) error {
	// Check if this is allowed
	if ctx_, ok := s.Config.Rules.Allow(ctx, req); !ok {
		if err := s.SendReply(conn, RuleFailure, nil); err != nil {
			return fmt.Errorf("Failed to send reply: %v", err)
		}
		return fmt.Errorf("Bind to %v blocked by rules", req.DestAddr)
	} else {
		ctx = ctx_
	}

	// TODO: Support bind
	if err := s.SendReply(conn, CommandNotSupported, nil); err != nil {
		return fmt.Errorf("Failed to send reply: %v", err)
	}
	return nil
}

// 暂时不能处理的统一放置
func (s *Server) handleUnSupport(ctx context.Context, conn conn, req *Request) (string, error) {
	if ctx_, ok := s.Config.Rules.Allow(ctx, req); !ok {
		if err := s.SendReply(conn, RuleFailure, nil); err != nil {
			return "", fmt.Errorf("Failed to send reply: %v", err)
		}
		return "", fmt.Errorf("Bind to %v blocked by rules", req.DestAddr)
	} else {
		ctx = ctx_
	}

	// TODO: Support bind
	if err := s.SendReply(conn, CommandNotSupported, nil); err != nil {
		return "", fmt.Errorf("Failed to send reply: %v", err)
	}
	return "", nil
}

// handleAssociate is used to handle a connect command
func (s *Server) handleAssociate(ctx context.Context, conn conn, req *Request) error {
	// Check if this is allowed
	if ctx_, ok := s.Config.Rules.Allow(ctx, req); !ok {
		if err := s.SendReply(conn, RuleFailure, nil); err != nil {
			return fmt.Errorf("Failed to send reply: %v", err)
		}
		return fmt.Errorf("Associate to %v blocked by rules", req.DestAddr)
	} else {
		ctx = ctx_
	}

	// TODO: Support associate
	if err := s.SendReply(conn, CommandNotSupported, nil); err != nil {
		return fmt.Errorf("Failed to send reply: %v", err)
	}
	return nil
}

// readAddrSpec is used to read AddrSpec.
// Expects an address type byte, follwed by the address and port
func readAddrSpec(r io.Reader) (*AddrSpec, error) {
	d := &AddrSpec{}

	// Get the address type
	addrType := []byte{0}
	if _, err := r.Read(addrType); err != nil {
		return nil, err
	}

	// Handle on a per type basis
	switch addrType[0] {
	case ipv4Address:
		addr := make([]byte, 4)
		if _, err := io.ReadAtLeast(r, addr, len(addr)); err != nil {
			return nil, err
		}
		d.IP = net.IP(addr)

	case ipv6Address:
		addr := make([]byte, 16)
		if _, err := io.ReadAtLeast(r, addr, len(addr)); err != nil {
			return nil, err
		}
		d.IP = net.IP(addr)

	case fqdnAddress:
		if _, err := r.Read(addrType); err != nil {
			return nil, err
		}
		addrLen := int(addrType[0])
		fqdn := make([]byte, addrLen)
		if _, err := io.ReadAtLeast(r, fqdn, addrLen); err != nil {
			return nil, err
		}
		d.FQDN = string(fqdn)

	default:
		return nil, unrecognizedAddrType
	}

	// Read the port
	port := []byte{0, 0}
	if _, err := io.ReadAtLeast(r, port, 2); err != nil {
		return nil, err
	}
	d.Port = (int(port[0]) << 8) | int(port[1])

	return d, nil
}

func BuildReply(resp uint8, addr *AddrSpec) ([]byte, error) {
	var addrType uint8
	var addrBody []byte
	var addrPort uint16
	switch {
	case addr == nil:
		addrType = ipv4Address
		addrBody = []byte{0, 0, 0, 0}
		addrPort = 0

	case addr.FQDN != "":
		addrType = fqdnAddress
		addrBody = append([]byte{byte(len(addr.FQDN))}, addr.FQDN...)
		addrPort = uint16(addr.Port)

	case addr.IP.To4() != nil:
		addrType = ipv4Address
		addrBody = []byte(addr.IP.To4())
		addrPort = uint16(addr.Port)

	case addr.IP.To16() != nil:
		addrType = ipv6Address
		addrBody = []byte(addr.IP.To16())
		addrPort = uint16(addr.Port)

	default:
		return nil, fmt.Errorf("Failed to format address: %v", addr)
	}

	// Format the message
	msg := make([]byte, 6+len(addrBody))
	msg[0] = socks5Version
	msg[1] = resp
	msg[2] = 0 // Reserved
	msg[3] = addrType
	copy(msg[4:], addrBody)
	msg[4+len(addrBody)] = byte(addrPort >> 8)
	msg[4+len(addrBody)+1] = byte(addrPort & 0xff)
	return msg, nil
}

// DefaultSendReply is used to send a reply message
func DefaultSendReply(w io.Writer, resp uint8, addr *AddrSpec) error {
	msg, err := BuildReply(resp, addr)
	if err != nil {
		return err
	}
	// Send the message
	_, err = w.Write(msg)
	return err
}

func ReadReply(conn io.ReadWriteCloser) (uint8, *AddrSpec, error) {
	// 先只读取头部信息
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return Unknown, nil, fmt.Errorf("failed to read SOCKS5 reply header: %v", err)
	}

	// 检查协议版本是否为 SOCKS5
	if header[0] != socks5Version {
		return Unknown, nil, fmt.Errorf("unsupported SOCKS version: %d", header[0])
	}

	// 获取响应代码（REP）
	rep := header[1]
	if rep != 0x00 {
		return rep, nil, fmt.Errorf("SOCKS5 request failed with REP code: %d", rep)
	}

	// 获取地址类型（ATYP）
	atyp := header[3]

	addrSpec := &AddrSpec{}

	// 根据地址类型读取不同长度的地址
	switch atyp {
	case ipv4Address:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return Unknown, nil, fmt.Errorf("failed to read IPv4 address: %v", err)
		}
		addrSpec.IP = net.IP(addr)

	case ipv6Address:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return Unknown, nil, fmt.Errorf("failed to read IPv6 address: %v", err)
		}
		addrSpec.IP = net.IP(addr)

	case fqdnAddress:
		fqdnLen := make([]byte, 1)
		if _, err := io.ReadFull(conn, fqdnLen); err != nil {
			return Unknown, nil, fmt.Errorf("failed to read FQDN length: %v", err)
		}
		if fqdnLen[0] == 0 {
			return Unknown, nil, fmt.Errorf("zero-length FQDN")
		}
		fqdn := make([]byte, fqdnLen[0])
		if _, err := io.ReadFull(conn, fqdn); err != nil {
			return Unknown, nil, fmt.Errorf("failed to read FQDN: %v", err)
		}
		addrSpec.FQDN = string(fqdn)

	default:
		return Unknown, nil, fmt.Errorf("unsupported address type: %d", atyp)
	}

	// 读取端口
	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return Unknown, nil, fmt.Errorf("failed to read port: %v", err)
	}
	addrSpec.Port = (int(port[0]) << 8) | int(port[1])

	return rep, addrSpec, nil
}

type closeWriter interface {
	CloseWrite() error
}

// proxy is used to suffle data from src to destination, and sends errors
// down a dedicated channel
func proxy(dst io.Writer, src io.Reader, errCh chan error) {
	_, err := io.Copy(dst, src)
	if tcpConn, ok := dst.(closeWriter); ok {
		tcpConn.CloseWrite()
	}
	errCh <- err
}
