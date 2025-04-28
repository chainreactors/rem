package core

var (
	MaxPacketSize = 1024 * 128
	DefaultKey    = "nonenonenonenone"

	DefaultScheme       = "default"
	DefaultConsoleProto = "tcp"
	DefaultConsolePort  = "34996"
	DefaultUsername     = "remno1"
	DefaultPassword     = "0onmer"

	LOCALHOST = "127.0.0.1"
)

const (
	InboundPlugin  = "inbound"
	OutboundPlugin = "outbound"
)

// proto
const (
	RawServe = "raw"
	//RelayServe = "relay" // custom socks5 protocol
	Socks5Serve       = "socks5"
	HTTPServe         = "http"
	ShadowSocksServe  = "ss"
	TrojanServe       = "trojan"
	PortForwardServe  = "forward"
	CobaltStrikeServe = "cs"
)

// transport
const (
	ICMPTunnel      = "icmp"
	HTTPTunnel      = "http"
	UDPTunnel       = "udp"
	TCPTunnel       = "tcp"
	UNIXTunnel      = "unix"
	WebSocketTunnel = "ws"
	WireGuardTunnel = "wireguard"
	MemoryTunnel    = "memory"
)

func Normalize(s string) string {
	switch s {
	case "socks5", "s5", "socks":
		return Socks5Serve
	case "ss", "shadowsocks":
		return ShadowSocksServe
	case "trojan":
		return TrojanServe
	case "forward", "port", "pf", "portfoward":
		return PortForwardServe
	case "http", "https":
		return HTTPServe
	case "raw":
		return RawServe
	case "smb", "pipe", "unix", "sock":
		return UNIXTunnel
	case "ws", "websocket", "wss":
		return WebSocketTunnel
	case "wireguard", "wg":
		return WireGuardTunnel

	default:
		return s
	}
}

// wrapper
const (
	XORWrapper     = "xor"
	AESWrapper     = "aes"
	PaddingWrapper = "padding"
)

const (
	Reverse = "reverse"
	Proxy   = "proxy"
	Bind    = "bind"
	Connect = "connect"
)

// agent type
const (
	SERVER = "server"

	CLIENT = "client"

	Redirect = "redirect"
)
