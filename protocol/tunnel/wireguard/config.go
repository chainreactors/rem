package wireguard

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/chainreactors/rem/protocol/core"
	"golang.zx2c4.com/wireguard/device"
)

// wgConfig holds parsed dial-side WireGuard parameters.
type wgConfig struct {
	endpoint      string
	port          uint16
	privateKey    string
	peerPublicKey string
	tunIP         string
	peerIP        string // server's tun IP to connect to
	allowedIPs    string
	netstackPort  int
	mtu           int
	keepalive     int
	dns           string
	psk           string // preshared key hex
	logLevel      int    // device.LogLevel*
}

// parseDialQuery extracts wgConfig from a dial URL's query parameters.
func parseDialQuery(u *core.URL) wgConfig {
	q := u.Query()
	cfg := wgConfig{
		tunIP:        DefaultClientTunIP,
		peerIP:       DefaultTunIP,
		allowedIPs:   "0.0.0.0/0",
		netstackPort: DefaultNetstackPort,
		mtu:          DefaultMTU,
		keepalive:    DefaultKeepalive,
		dns:          DefaultDNS,
		logLevel:     device.LogLevelSilent,
	}
	cfg.privateKey = q.Get("private_key")
	cfg.peerPublicKey = q.Get("peer_public_key")
	cfg.psk = q.Get("psk")
	cfg.logLevel = parseLogLevel(q.Get("log_level"))

	if v := q.Get("tun_ip"); v != "" {
		cfg.tunIP = v
	} else if cfg.privateKey != "" {
		if pub, err := pubKeyFromPrivHex(cfg.privateKey); err == nil {
			cfg.tunIP = tunIPFromPubKey(pub)
		}
	}
	if v := q.Get("peer_ip"); v != "" {
		cfg.peerIP = v
	}
	if v := q.Get("allowed_ips"); v != "" {
		cfg.allowedIPs = v
	}
	if v := q.Get("netstack_port"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.netstackPort = p
		}
	}
	if v := q.Get("mtu"); v != "" {
		if m, err := strconv.Atoi(v); err == nil {
			cfg.mtu = m
		}
	}
	if v := q.Get("keepalive"); v != "" {
		if k, err := strconv.Atoi(v); err == nil {
			cfg.keepalive = k
		}
	}
	if v := q.Get("dns"); v != "" {
		cfg.dns = v
	}
	return cfg
}

// buildClientURL constructs a client-facing wireguard:// URL.
// The client tun_ip is intentionally omitted and derived from private_key.
func (c *WireguardListener) buildClientURL(clientPrivKey string) string {
	clientURL := &url.URL{
		Scheme: "wireguard",
		Host:   c.listenHost,
	}
	cq := clientURL.Query()
	if clientPrivKey != "" {
		cq.Set("private_key", clientPrivKey)
	}
	cq.Set("peer_public_key", c.serverPubKey)
	if c.serverTunIP != DefaultTunIP {
		cq.Set("peer_ip", c.serverTunIP)
	}
	if c.netstackPort != DefaultNetstackPort {
		cq.Set("netstack_port", strconv.Itoa(c.netstackPort))
	}
	if c.mtu != DefaultMTU {
		cq.Set("mtu", strconv.Itoa(c.mtu))
	}
	if c.keepalive != DefaultKeepalive {
		cq.Set("keepalive", strconv.Itoa(c.keepalive))
	}
	if c.psk != "" {
		cq.Set("psk", c.psk)
	}
	clientURL.RawQuery = cq.Encode()
	return clientURL.String()
}

func parseLogLevel(s string) int {
	switch strings.ToLower(s) {
	case "verbose":
		return device.LogLevelVerbose
	case "error":
		return device.LogLevelError
	default:
		return device.LogLevelSilent
	}
}

func parseDNSAddrs(dns string) []netip.Addr {
	var addrs []netip.Addr
	for _, s := range strings.Split(dns, ",") {
		s = strings.TrimSpace(s)
		if addr, err := netip.ParseAddr(s); err == nil {
			addrs = append(addrs, addr)
		}
	}
	if len(addrs) == 0 {
		addrs = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	}
	return addrs
}
