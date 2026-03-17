package wireguard

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

// Defaults for WireGuard tunnel parameters.
var (
	DefaultTunIP        = "100.64.0.1"
	DefaultClientTunIP  = "100.64.0.2"
	DefaultNetstackPort = 8888
	DefaultMTU          = 1420
	DefaultKeepalive    = 25 // seconds, 0 = disabled
	DefaultDNS          = "127.0.0.1"
)

func init() {
	core.DialerRegister(core.WireGuardTunnel, func(ctx context.Context) (core.TunnelDialer, error) {
		return NewWireguardDialer(ctx), nil
	}, "wg")

	core.ListenerRegister(core.WireGuardTunnel, func(ctx context.Context) (core.TunnelListener, error) {
		return NewWireguardListener(ctx), nil
	}, "wg")
}

// PeerStats holds per-peer statistics from the WireGuard device.
type PeerStats struct {
	PublicKey     string
	Endpoint      string
	TxBytes       uint64
	RxBytes       uint64
	LastHandshake time.Time
	Keepalive     int
	AllowedIPs    []string
}

// HealthStatus represents the health state of a WireGuard tunnel.
type HealthStatus struct {
	Healthy       bool
	LastHandshake time.Time
	StaleDuration time.Duration
}

func splitCSVQuery(v string) []string {
	var items []string
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

// ---------------------------------------------------------------------------
// WireguardDialer — client side
// ---------------------------------------------------------------------------

// WireguardDialer implements core.TunnelDialer for WireGuard.
type WireguardDialer struct {
	net.Conn
	meta   core.Metas
	device *device.Device
	tNet   *netstack.Net
	tunIP  string
}

func NewWireguardDialer(ctx context.Context) *WireguardDialer {
	return &WireguardDialer{meta: core.GetMetas(ctx)}
}

func (c *WireguardDialer) Close() error {
	if c.device != nil {
		c.device.Down()
		select {
		case <-c.device.Wait():
		case <-time.After(5 * time.Second):
		}
		c.device.Close()
	}
	return nil
}

func (c *WireguardDialer) NetStack() *netstack.Net { return c.tNet }

func (c *WireguardDialer) GetStats() ([]PeerStats, error) {
	if c.device == nil {
		return nil, fmt.Errorf("wireguard device not initialized")
	}
	return parseDeviceStats(c.device)
}

func (c *WireguardDialer) DialUDP(laddr, raddr *net.UDPAddr) (*gonet.UDPConn, error) {
	if c.tNet == nil {
		return nil, fmt.Errorf("wireguard not connected")
	}
	return c.tNet.DialUDP(laddr, raddr)
}

func (c *WireguardDialer) LookupHost(host string) ([]string, error) {
	if c.tNet == nil {
		return nil, fmt.Errorf("wireguard not connected")
	}
	return c.tNet.LookupHost(host)
}

func (c *WireguardDialer) Ping(addr netip.Addr, timeout time.Duration) (time.Duration, error) {
	laddr, _ := netip.ParseAddr(c.tunIP)
	return pingViaTNet(c.tNet, laddr, addr, timeout)
}

func (c *WireguardDialer) CheckHealth() (*HealthStatus, error) {
	return checkHealthFromDevice(c.device)
}

// Dial connects to a WireGuard endpoint.
func (c *WireguardDialer) Dial(dst string) (net.Conn, error) {
	return c.DialContext(context.Background(), dst)
}

// DialContext connects with context support for timeout/cancellation.
func (c *WireguardDialer) DialContext(ctx context.Context, dst string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	if c.meta != nil {
		c.meta["url"] = u
	}

	cfg := parseDialQuery(u)
	if cfg.privateKey == "" || cfg.peerPublicKey == "" {
		return nil, fmt.Errorf("wireguard dial requires private_key and peer_public_key query parameters")
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "51820"
	}
	portInt, _ := strconv.Atoi(port)
	cfg.endpoint = host
	cfg.port = uint16(portInt)

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dev, tNet, err := bringUpWGInterface(cfg)
	if err != nil {
		return nil, fmt.Errorf("wireguard interface setup failed: %w", err)
	}
	c.device = dev
	c.tNet = tNet
	c.tunIP = cfg.tunIP

	if err := ctx.Err(); err != nil {
		dev.Close()
		return nil, err
	}

	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard device up failed: %w", err)
	}

	if err := ctx.Err(); err != nil {
		dev.Close()
		return nil, err
	}

	connection, err := tNet.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", cfg.peerIP, cfg.netstackPort))
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard netstack dial failed: %w", err)
	}

	utils.Log.Infof("wireguard connected to %s:%d via tun %s (mtu=%d, keepalive=%d)",
		host, portInt, cfg.tunIP, cfg.mtu, cfg.keepalive)
	return connection, nil
}

// ---------------------------------------------------------------------------
// WireguardListener — server side
// ---------------------------------------------------------------------------

// WireguardListener implements core.TunnelListener for WireGuard.
type WireguardListener struct {
	listener      net.Listener
	device        *device.Device
	tNet          *netstack.Net
	meta          core.Metas
	serverPrivKey string
	serverPubKey  string
	serverTunIP   string
	listenHost    string
	mtu           int
	keepalive     int
	netstackPort  int
	psk           string
	logLevel      int
	mu            sync.Mutex
}

func NewWireguardListener(ctx context.Context) *WireguardListener {
	return &WireguardListener{meta: core.GetMetas(ctx)}
}

// Listen starts a WireGuard listener.
func (c *WireguardListener) Listen(dst string) (net.Listener, error) {
	u, err := core.NewURL(dst)
	if err != nil {
		return nil, err
	}
	if c.meta != nil {
		c.meta["url"] = u
	}

	q := u.Query()

	// TUN IP & basic params
	tunIP := q.Get("tun_ip")
	if tunIP == "" {
		tunIP = DefaultTunIP
	}
	c.netstackPort = DefaultNetstackPort
	if ps := q.Get("netstack_port"); ps != "" {
		if p, err := strconv.Atoi(ps); err == nil {
			c.netstackPort = p
		}
	}
	c.mtu = DefaultMTU
	if ms := q.Get("mtu"); ms != "" {
		if m, err := strconv.Atoi(ms); err == nil {
			c.mtu = m
		}
	}
	c.keepalive = DefaultKeepalive
	if ks := q.Get("keepalive"); ks != "" {
		if k, err := strconv.Atoi(ks); err == nil {
			c.keepalive = k
		}
	}
	dns := DefaultDNS
	if ds := q.Get("dns"); ds != "" {
		dns = ds
	}
	c.psk = q.Get("psk")
	c.logLevel = parseLogLevel(q.Get("log_level"))

	// Server key pair
	var serverPrivKey, serverPubKey string
	if pk := q.Get("private_key"); pk != "" {
		serverPrivKey = pk
		pub, err := pubKeyFromPrivHex(pk)
		if err != nil {
			return nil, fmt.Errorf("wireguard server private_key: %w", err)
		}
		serverPubKey = pub
	} else {
		var err error
		serverPrivKey, serverPubKey, err = genWGKeys()
		if err != nil {
			return nil, fmt.Errorf("wireguard keygen failed: %w", err)
		}
	}
	c.serverPrivKey = serverPrivKey
	c.serverPubKey = serverPubKey
	c.serverTunIP = tunIP
	c.listenHost = u.Host

	// Peers
	type peerInfo struct {
		pubKey, privKey, clientTunIP string
	}
	var peers []peerInfo
	peerPubKeys := splitCSVQuery(q.Get("peer_public_key"))
	if len(peerPubKeys) > 0 {
		for _, pub := range peerPubKeys {
			clientTunIP := tunIPFromPubKey(pub)
			peers = append(peers, peerInfo{
				pubKey:      pub,
				clientTunIP: clientTunIP,
			})
		}
	} else {
		clientPrivKey, clientPubKey, err := genWGKeys()
		if err != nil {
			return nil, fmt.Errorf("wireguard keygen failed: %w", err)
		}
		peers = append(peers, peerInfo{
			pubKey:      clientPubKey,
			privKey:     clientPrivKey,
			clientTunIP: tunIPFromPubKey(clientPubKey),
		})
	}

	// Create netstack TUN
	tunDev, tNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(tunIP)},
		parseDNSAddrs(dns),
		c.mtu,
	)
	if err != nil {
		return nil, fmt.Errorf("wireguard create tun failed: %w", err)
	}
	c.tNet = tNet

	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(c.logLevel, "[wg] "))

	wgConf := bytes.NewBuffer(nil)
	fmt.Fprintf(wgConf, "private_key=%s\n", serverPrivKey)
	fmt.Fprintf(wgConf, "listen_port=%s\n", u.Port())
	for _, p := range peers {
		fmt.Fprintf(wgConf, "public_key=%s\n", p.pubKey)
		fmt.Fprintf(wgConf, "allowed_ip=%s/32\n", p.clientTunIP)
		if c.keepalive > 0 {
			fmt.Fprintf(wgConf, "persistent_keepalive_interval=%d\n", c.keepalive)
		}
		if c.psk != "" {
			fmt.Fprintf(wgConf, "preshared_key=%s\n", c.psk)
		}
	}

	if err := dev.IpcSetOperation(bufio.NewReader(wgConf)); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard ipc set failed: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard device up failed: %w", err)
	}
	c.device = dev

	listener, err := tNet.ListenTCP(&net.TCPAddr{
		IP:   net.ParseIP(tunIP),
		Port: c.netstackPort,
	})
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("wireguard netstack listen failed: %w", err)
	}
	c.listener = listener

	// Store client URLs in meta
	if c.meta != nil {
		c.meta["client_url"] = c.buildClientURL(peers[0].privKey)
		if len(peers) > 1 {
			var urls []string
			for _, p := range peers {
				urls = append(urls, c.buildClientURL(p.privKey))
			}
			c.meta["client_urls"] = strings.Join(urls, "\n")
		}
	}

	utils.Log.Infof("wireguard listening on %s, tun %s (mtu=%d, keepalive=%d, peers=%d)",
		u.Host, tunIP, c.mtu, c.keepalive, len(peers))
	for i, p := range peers {
		utils.Log.Infof("  peer[%d] tun_ip=%s pub=%s...", i, p.clientTunIP, p.pubKey[:8])
	}
	return listener, nil
}

// AddPeer dynamically adds a new peer to the WireGuard device.
func (c *WireguardListener) AddPeer() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.device == nil {
		return "", fmt.Errorf("wireguard device not initialized")
	}

	clientPrivKey, clientPubKey, err := genWGKeys()
	if err != nil {
		return "", fmt.Errorf("wireguard keygen failed: %w", err)
	}
	clientTunIP := tunIPFromPubKey(clientPubKey)

	wgConf := bytes.NewBuffer(nil)
	fmt.Fprintf(wgConf, "public_key=%s\n", clientPubKey)
	fmt.Fprintf(wgConf, "allowed_ip=%s/32\n", clientTunIP)
	if c.keepalive > 0 {
		fmt.Fprintf(wgConf, "persistent_keepalive_interval=%d\n", c.keepalive)
	}
	if c.psk != "" {
		fmt.Fprintf(wgConf, "preshared_key=%s\n", c.psk)
	}

	if err := c.device.IpcSetOperation(bufio.NewReader(wgConf)); err != nil {
		return "", fmt.Errorf("wireguard add peer failed: %w", err)
	}

	clientURL := c.buildClientURL(clientPrivKey)
	utils.Log.Infof("wireguard added peer %s (tun %s)", clientPubKey[:8]+"...", clientTunIP)
	return clientURL, nil
}

// RemovePeer removes a peer by its public key hex.
func (c *WireguardListener) RemovePeer(publicKeyHex string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.device == nil {
		return fmt.Errorf("wireguard device not initialized")
	}
	wgConf := bytes.NewBuffer(nil)
	fmt.Fprintf(wgConf, "public_key=%s\n", publicKeyHex)
	fmt.Fprintf(wgConf, "remove=true\n")
	return c.device.IpcSetOperation(bufio.NewReader(wgConf))
}

func (c *WireguardListener) GetStats() ([]PeerStats, error) {
	if c.device == nil {
		return nil, fmt.Errorf("wireguard device not initialized")
	}
	return parseDeviceStats(c.device)
}

func (c *WireguardListener) DialUDP(laddr, raddr *net.UDPAddr) (*gonet.UDPConn, error) {
	if c.tNet == nil {
		return nil, fmt.Errorf("wireguard not connected")
	}
	return c.tNet.DialUDP(laddr, raddr)
}

func (c *WireguardListener) ListenUDP(laddr *net.UDPAddr) (*gonet.UDPConn, error) {
	if c.tNet == nil {
		return nil, fmt.Errorf("wireguard not connected")
	}
	return c.tNet.ListenUDP(laddr)
}

func (c *WireguardListener) LookupHost(host string) ([]string, error) {
	if c.tNet == nil {
		return nil, fmt.Errorf("wireguard not connected")
	}
	return c.tNet.LookupHost(host)
}

func (c *WireguardListener) Ping(addr netip.Addr, timeout time.Duration) (time.Duration, error) {
	laddr, _ := netip.ParseAddr(c.serverTunIP)
	return pingViaTNet(c.tNet, laddr, addr, timeout)
}

func (c *WireguardListener) CheckHealth() (*HealthStatus, error) {
	return checkHealthFromDevice(c.device)
}

func (c *WireguardListener) NetStack() *netstack.Net { return c.tNet }

func (c *WireguardListener) Accept() (net.Conn, error) { return c.listener.Accept() }

func (c *WireguardListener) Close() error {
	if c.device != nil {
		c.device.Down()
		select {
		case <-c.device.Wait():
		case <-time.After(5 * time.Second):
		}
		c.device.Close()
	}
	if c.listener != nil {
		return c.listener.Close()
	}
	return nil
}

func (c *WireguardListener) Addr() net.Addr {
	if c.meta != nil {
		return c.meta.URL()
	}
	return nil
}
