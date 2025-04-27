package shadowsocks

import (
	"fmt"
	"github.com/chainreactors/rem/x/utils"
	"io"
	"net"
	"strconv"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/socks5"
	ss "github.com/shadowsocks/go-shadowsocks2/core"
	"github.com/shadowsocks/go-shadowsocks2/socks"
)

func init() {
	core.InboundRegister(core.ShadowSocksServe, NewShadowSocksInbound)
	core.OutboundRegister(core.ShadowSocksServe, NewShadowSocksOutbound)

}

const (
	ErrGeneralFailure       = Error(1)
	ErrConnectionNotAllowed = Error(2)
	ErrNetworkUnreachable   = Error(3)
	ErrHostUnreachable      = Error(4)
	ErrConnectionRefused    = Error(5)
	ErrTTLExpired           = Error(6)
	ErrCommandNotSupported  = Error(7)
	ErrAddressNotSupported  = Error(8)
	InfoUDPAssociate        = Error(9)
)

// MaxAddrLen is the maximum size of SOCKS address in bytes.
const MaxAddrLen = 1 + 1 + 255 + 2

// Addr represents a SOCKS address as defined in RFC 1928 section 5.
type Addr []byte

// String serializes SOCKS address a to string form.
func (a Addr) String() string {
	var host, port string

	switch a[0] { // address type
	case AtypDomainName:
		host = string(a[2 : 2+int(a[1])])
		port = strconv.Itoa((int(a[2+int(a[1])]) << 8) | int(a[2+int(a[1])+1]))
	case AtypIPv4:
		host = net.IP(a[1 : 1+net.IPv4len]).String()
		port = strconv.Itoa((int(a[1+net.IPv4len]) << 8) | int(a[1+net.IPv4len+1]))
	case AtypIPv6:
		host = net.IP(a[1 : 1+net.IPv6len]).String()
		port = strconv.Itoa((int(a[1+net.IPv6len]) << 8) | int(a[1+net.IPv6len+1]))
	}

	return net.JoinHostPort(host, port)
}

var UDPEnabled = false

// SOCKS request commands as defined in RFC 1928 section 4.
const (
	CmdConnect      = 1
	CmdBind         = 2
	CmdUDPAssociate = 3
)

// SOCKS address types as defined in RFC 1928 section 5.
const (
	AtypIPv4       = 1
	AtypDomainName = 3
	AtypIPv6       = 4
)

// Error represents a SOCKS error
type Error byte

func (err Error) Error() string {
	return "SOCKS error: " + strconv.Itoa(int(err))
}

func ReadAddr(r io.Reader, b []byte) (*socks5.AddrSpec, error) {
	if len(b) < MaxAddrLen {
		return nil, io.ErrShortBuffer
	}
	// 读取地址类型 (ATYP)
	_, err := io.ReadFull(r, b[:1])
	if err != nil {
		return nil, fmt.Errorf("failed to read ATYP: %v", err)
	}

	atyp := b[0]
	addrSpec := &socks5.AddrSpec{}

	switch atyp {
	case AtypDomainName:
		// 读取域名长度
		_, err = io.ReadFull(r, b[1:2])
		if err != nil {
			return nil, fmt.Errorf("failed to read domain length: %v", err)
		}
		domainLength := int(b[1])

		// 读取域名和端口
		if len(b) < 2+domainLength+2 {
			return nil, io.ErrShortBuffer
		}
		_, err = io.ReadFull(r, b[2:2+domainLength+2])
		if err != nil {
			return nil, fmt.Errorf("failed to read domain and port: %v", err)
		}
		addrSpec.FQDN = string(b[2 : 2+domainLength])
		addrSpec.Port = int(b[2+domainLength])<<8 | int(b[2+domainLength+1])

	case AtypIPv4:
		// 读取 IPv4 地址和端口
		if len(b) < 1+net.IPv4len+2 {
			return nil, io.ErrShortBuffer
		}
		_, err = io.ReadFull(r, b[1:1+net.IPv4len+2])
		if err != nil {
			return nil, fmt.Errorf("failed to read IPv4 and port: %v", err)
		}
		addrSpec.IP = net.IP(b[1 : 1+net.IPv4len])
		addrSpec.Port = int(b[1+net.IPv4len])<<8 | int(b[1+net.IPv4len+1])

	case AtypIPv6:
		// 读取 IPv6 地址和端口
		if len(b) < 1+net.IPv6len+2 {
			return nil, io.ErrShortBuffer
		}
		_, err = io.ReadFull(r, b[1:1+net.IPv6len+2])
		if err != nil {
			return nil, fmt.Errorf("failed to read IPv6 and port: %v", err)
		}
		addrSpec.IP = net.IP(b[1 : 1+net.IPv6len])
		addrSpec.Port = int(b[1+net.IPv6len])<<8 | int(b[1+net.IPv6len+1])

	default:
		return nil, fmt.Errorf("unsupported address type: %d", atyp)
	}
	return addrSpec, nil
}

func NewShadowSocksOutbound(options map[string]string, dial core.ContextDialer) (core.Outbound, error) {
	pwd := options["password"]
	var cip string
	var ok bool
	if cip, ok = options["cipher"]; !ok {
		options["cipher"] = "aes-256-gcm"
		cip = "aes-256-gcm"
	}

	cipher, err := ss.PickCipher(cip, nil, pwd)
	if err != nil {
		return nil, err
	}
	base := core.NewPluginOption(options, core.OutboundPlugin, core.ShadowSocksServe)
	utils.Log.Importantf("[agent.inbound] shadowsocks serving: %s , %s", base.String(), base.URL())
	return &ShadowSocksPlugin{Server: cipher, PluginOption: base, dial: dial}, nil

}
func NewShadowSocksInbound(options map[string]string) (core.Inbound, error) {
	pwd := options["password"]
	var cip string
	var ok bool
	if cip, ok = options["cipher"]; !ok {
		options["cipher"] = "aes-256-gcm"
		cip = "aes-256-gcm"
	}

	cipher, err := ss.PickCipher(cip, nil, pwd)
	if err != nil {
		return nil, err
	}
	base := core.NewPluginOption(options, core.InboundPlugin, core.ShadowSocksServe)
	utils.Log.Importantf("[agent.inbound] shadowsocks serving: %s, %s", base.String(), base.URL())
	return &ShadowSocksPlugin{Server: cipher, PluginOption: base}, nil
}

type ShadowSocksPlugin struct {
	Server ss.Cipher
	*core.PluginOption
	dial core.ContextDialer
}

func (plug *ShadowSocksPlugin) Handle(conn io.ReadWriteCloser, realConn net.Conn) (net.Conn, error) {
	wrapConn := cio.WrapConn(realConn, conn)

	ssConn := plug.Server.StreamConn(wrapConn)
	tgt, err := socks.ReadAddr(ssConn)
	if err != nil {
		return nil, err
	}
	return net.Dial("tcp", tgt.String())
}

func (plug *ShadowSocksPlugin) Relay(conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error) {
	ssConn := plug.Server.StreamConn(conn)
	addr, err := ReadAddr(ssConn, make([]byte, MaxAddrLen))
	if err != nil {
		return nil, err
	}

	req := &socks5.RelayRequest{DestAddr: addr}
	_, err = bridge.Write(req.BuildRelay())
	if err != nil {
		return nil, err
	}

	_, _, err = socks5.ReadReply(bridge)
	if err != nil {
		return nil, err
	}

	return ssConn, nil
}

func (plug *ShadowSocksPlugin) Name() string {
	return core.ShadowSocksServe
}
