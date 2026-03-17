package wireguard

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
	"golang.org/x/net/proxy"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func init() {
	utils.Log = logs.NewLogger(logs.DebugLevel)
}

// ---------- helpers ----------

func freeUDPPort(t *testing.T) int {
	t.Helper()
	a, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	l, err := net.ListenUDP("udp", a)
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	return port
}

// wgTunnel holds both ends of a WireGuard tunnel for testing.
type wgTunnel struct {
	serverDev    *device.Device
	clientDev    *device.Device
	serverNet    *netstack.Net
	clientNet    *netstack.Net
	serverTunIP  string
	clientTunIP  string
	netstackPort int
}

func (wt *wgTunnel) Close() {
	if wt.clientDev != nil {
		wt.clientDev.Down()
	}
	if wt.serverDev != nil {
		wt.serverDev.Down()
	}
	time.Sleep(100 * time.Millisecond)
	if wt.clientDev != nil {
		wt.clientDev.Close()
	}
	if wt.serverDev != nil {
		wt.serverDev.Close()
	}
}

type wgClientPeer struct {
	dev   *device.Device
	net   *netstack.Net
	tunIP string
}

type wgMultiPeerTunnel struct {
	serverDev   *device.Device
	serverNet   *netstack.Net
	serverTunIP string
	clients     []wgClientPeer
}

func (wt *wgMultiPeerTunnel) Close() {
	for _, client := range wt.clients {
		if client.dev != nil {
			client.dev.Down()
		}
	}
	if wt.serverDev != nil {
		wt.serverDev.Down()
	}
	time.Sleep(100 * time.Millisecond)
	for _, client := range wt.clients {
		if client.dev != nil {
			client.dev.Close()
		}
	}
	if wt.serverDev != nil {
		wt.serverDev.Close()
	}
}

// setupWGTunnel creates a full WireGuard tunnel between server and client,
// returning both netstack.Net handles so each side can dial/listen freely.
func setupWGTunnel(t *testing.T, netstackPort int) *wgTunnel {
	t.Helper()
	wgPort := freeUDPPort(t)
	serverTunIP := "100.64.0.1"
	clientTunIP := "100.64.0.2"

	serverPriv, serverPub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}
	clientPriv, clientPub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}

	// --- server side ---
	sTun, sNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(serverTunIP)},
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		1420,
	)
	if err != nil {
		t.Fatal(err)
	}
	sDev := device.NewDevice(sTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-srv] "))

	sCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(sCfg, "private_key=%s\n", serverPriv)
	fmt.Fprintf(sCfg, "listen_port=%d\n", wgPort)
	fmt.Fprintf(sCfg, "public_key=%s\n", clientPub)
	fmt.Fprintf(sCfg, "allowed_ip=%s/32\n", clientTunIP)
	if err := sDev.IpcSetOperation(bufio.NewReader(sCfg)); err != nil {
		t.Fatal(err)
	}
	if err := sDev.Up(); err != nil {
		t.Fatal(err)
	}

	// --- client side ---
	cTun, cNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(clientTunIP)},
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		1420,
	)
	if err != nil {
		t.Fatal(err)
	}
	cDev := device.NewDevice(cTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-cli] "))

	cCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(cCfg, "private_key=%s\n", clientPriv)
	fmt.Fprintf(cCfg, "public_key=%s\n", serverPub)
	fmt.Fprintf(cCfg, "endpoint=127.0.0.1:%d\n", wgPort)
	fmt.Fprintf(cCfg, "allowed_ip=0.0.0.0/0\n")
	if err := cDev.IpcSetOperation(bufio.NewReader(cCfg)); err != nil {
		t.Fatal(err)
	}
	if err := cDev.Up(); err != nil {
		t.Fatal(err)
	}

	return &wgTunnel{
		serverDev:    sDev,
		clientDev:    cDev,
		serverNet:    sNet,
		clientNet:    cNet,
		serverTunIP:  serverTunIP,
		clientTunIP:  clientTunIP,
		netstackPort: netstackPort,
	}
}

func setupWGMultiPeerTunnel(t *testing.T, numClients int) *wgMultiPeerTunnel {
	t.Helper()
	if numClients <= 0 {
		t.Fatal("numClients must be positive")
	}

	wgPort := freeUDPPort(t)
	serverTunIP := "100.64.0.1"

	serverPriv, serverPub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}

	type peerConfig struct {
		priv  string
		pub   string
		tunIP string
	}
	peers := make([]peerConfig, 0, numClients)
	for i := 0; i < numClients; i++ {
		clientPriv, clientPub, err := genWGKeys()
		if err != nil {
			t.Fatal(err)
		}
		peers = append(peers, peerConfig{
			priv:  clientPriv,
			pub:   clientPub,
			tunIP: tunIPFromPubKey(clientPub),
		})
	}

	sTun, sNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(serverTunIP)},
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		1420,
	)
	if err != nil {
		t.Fatal(err)
	}
	sDev := device.NewDevice(sTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-srv] "))

	sCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(sCfg, "private_key=%s\n", serverPriv)
	fmt.Fprintf(sCfg, "listen_port=%d\n", wgPort)
	for _, peer := range peers {
		fmt.Fprintf(sCfg, "public_key=%s\n", peer.pub)
		fmt.Fprintf(sCfg, "allowed_ip=%s/32\n", peer.tunIP)
	}
	if err := sDev.IpcSetOperation(bufio.NewReader(sCfg)); err != nil {
		t.Fatal(err)
	}
	if err := sDev.Up(); err != nil {
		t.Fatal(err)
	}

	clients := make([]wgClientPeer, 0, len(peers))
	for i, peer := range peers {
		cTun, cNet, err := netstack.CreateNetTUN(
			[]netip.Addr{netip.MustParseAddr(peer.tunIP)},
			[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
			1420,
		)
		if err != nil {
			t.Fatal(err)
		}
		cDev := device.NewDevice(cTun, conn.NewDefaultBind(),
			device.NewLogger(device.LogLevelSilent, fmt.Sprintf("[wg-cli-%d] ", i+1)))

		cCfg := bytes.NewBuffer(nil)
		fmt.Fprintf(cCfg, "private_key=%s\n", peer.priv)
		fmt.Fprintf(cCfg, "public_key=%s\n", serverPub)
		fmt.Fprintf(cCfg, "endpoint=127.0.0.1:%d\n", wgPort)
		fmt.Fprintf(cCfg, "allowed_ip=0.0.0.0/0\n")
		if err := cDev.IpcSetOperation(bufio.NewReader(cCfg)); err != nil {
			t.Fatal(err)
		}
		if err := cDev.Up(); err != nil {
			t.Fatal(err)
		}

		clients = append(clients, wgClientPeer{
			dev:   cDev,
			net:   cNet,
			tunIP: peer.tunIP,
		})
	}

	time.Sleep(200 * time.Millisecond)

	return &wgMultiPeerTunnel{
		serverDev:   sDev,
		serverNet:   sNet,
		serverTunIP: serverTunIP,
		clients:     clients,
	}
}

// startSOCKS5OnServer runs a SOCKS5 server accepting connections on the
// server's netstack TCP port. Returns the listener for cleanup.
func startSOCKS5OnServer(t *testing.T, wt *wgTunnel) net.Listener {
	t.Helper()
	ln, err := wt.serverNet.ListenTCP(&net.TCPAddr{
		IP:   net.ParseIP(wt.serverTunIP),
		Port: wt.netstackPort,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := socks5.New(&socks5.Config{
		Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}

	go srv.Serve(ln)
	return ln
}

// startLocalSOCKS5Relay creates a local TCP listener. For every accepted
// connection it dials through the client's netstack to the server's SOCKS5
// port and relays data bidirectionally.
func startLocalSOCKS5Relay(t *testing.T, wt *wgTunnel) (socksAddr string, closer func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socksAddr = ln.Addr().String()

	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go func(local net.Conn) {
				remote, err := wt.clientNet.Dial("tcp",
					fmt.Sprintf("%s:%d", wt.serverTunIP, wt.netstackPort))
				if err != nil {
					local.Close()
					return
				}
				relay(local, remote)
			}(local)
		}
	}()

	return socksAddr, func() { ln.Close() }
}

func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		dst.Close()
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
}

// startHTTPServer starts a local HTTP server with two endpoints:
//
//	GET /ping          → "pong"
//	GET /data?size=N   → N bytes of random data (seeded per-request)
func startHTTPServer(t *testing.T) (addr string, closer func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})
	mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
		sizeStr := r.URL.Query().Get("size")
		size, _ := strconv.Atoi(sizeStr)
		if size <= 0 {
			size = 1024
		}
		data := make([]byte, size)
		rand.Read(data)
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.Write(data)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	return ln.Addr().String(), func() {
		srv.Close()
		ln.Close()
	}
}

func newNetstackHTTPClient(tNet *netstack.Net, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return tNet.DialContext(ctx, network, addr)
			},
		},
		Timeout: timeout,
	}
}

// ---------- setup helper: full WG + SOCKS5 stack ----------

type testStack struct {
	wt        *wgTunnel
	socksAddr string
	httpAddr  string
	cleanup   func()
}

func setupFullStack(t *testing.T) *testStack {
	t.Helper()
	netstackPort := 8888

	wt := setupWGTunnel(t, netstackPort)
	socksLn := startSOCKS5OnServer(t, wt)
	socksAddr, relayClose := startLocalSOCKS5Relay(t, wt)
	httpAddr, httpClose := startHTTPServer(t)

	// give the WG handshake a moment
	time.Sleep(200 * time.Millisecond)

	cleanup := func() {
		relayClose()
		socksLn.Close()
		httpClose()
		wt.Close()
	}

	t.Logf("HTTP server  : %s", httpAddr)
	t.Logf("SOCKS5 relay : %s  (→ WG tunnel → SOCKS5 server)", socksAddr)
	return &testStack{wt: wt, socksAddr: socksAddr, httpAddr: httpAddr, cleanup: cleanup}
}

// ---------- benchmark helpers ----------

// setupBenchTunnel creates a WG tunnel with a simple echo/sink TCP server
// on the server side for pure throughput/latency measurement (no SOCKS5, no curl).
type benchStack struct {
	wt       *wgTunnel
	echoAddr string // server-side echo service address (tunIP:port)
	cleanup  func()
}

func setupBenchStack(b *testing.B) *benchStack {
	b.Helper()
	netstackPort := 8888
	wgPort := freeUDPPortB(b)
	serverTunIP := "100.64.0.1"
	clientTunIP := "100.64.0.2"

	serverPriv, serverPub, err := genWGKeys()
	if err != nil {
		b.Fatal(err)
	}
	clientPriv, clientPub, err := genWGKeys()
	if err != nil {
		b.Fatal(err)
	}

	// server
	sTun, sNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(serverTunIP)},
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		1420,
	)
	if err != nil {
		b.Fatal(err)
	}
	sDev := device.NewDevice(sTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-srv] "))
	sCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(sCfg, "private_key=%s\n", serverPriv)
	fmt.Fprintf(sCfg, "listen_port=%d\n", wgPort)
	fmt.Fprintf(sCfg, "public_key=%s\n", clientPub)
	fmt.Fprintf(sCfg, "allowed_ip=%s/32\n", clientTunIP)
	if err := sDev.IpcSetOperation(bufio.NewReader(sCfg)); err != nil {
		b.Fatal(err)
	}
	if err := sDev.Up(); err != nil {
		b.Fatal(err)
	}

	// client
	cTun, cNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(clientTunIP)},
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		1420,
	)
	if err != nil {
		b.Fatal(err)
	}
	cDev := device.NewDevice(cTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-cli] "))
	cCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(cCfg, "private_key=%s\n", clientPriv)
	fmt.Fprintf(cCfg, "public_key=%s\n", serverPub)
	fmt.Fprintf(cCfg, "endpoint=127.0.0.1:%d\n", wgPort)
	fmt.Fprintf(cCfg, "allowed_ip=0.0.0.0/0\n")
	if err := cDev.IpcSetOperation(bufio.NewReader(cCfg)); err != nil {
		b.Fatal(err)
	}
	if err := cDev.Up(); err != nil {
		b.Fatal(err)
	}

	wt := &wgTunnel{
		serverDev:    sDev,
		clientDev:    cDev,
		serverNet:    sNet,
		clientNet:    cNet,
		serverTunIP:  serverTunIP,
		clientTunIP:  clientTunIP,
		netstackPort: netstackPort,
	}

	// start echo server on server netstack
	ln, err := sNet.ListenTCP(&net.TCPAddr{
		IP:   net.ParseIP(serverTunIP),
		Port: netstackPort,
	})
	if err != nil {
		b.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(c, c) // echo
		}
	}()

	time.Sleep(200 * time.Millisecond)

	return &benchStack{
		wt:       wt,
		echoAddr: fmt.Sprintf("%s:%d", serverTunIP, netstackPort),
		cleanup: func() {
			ln.Close()
			wt.Close()
		},
	}
}

func freeUDPPortB(b *testing.B) int {
	b.Helper()
	a, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	l, err := net.ListenUDP("udp", a)
	if err != nil {
		b.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	return port
}

// ==================== Tests ====================

// TestWG_SOCKS5 downloads 1 MB through WG+SOCKS5 and verifies SHA256 integrity.
func TestWG_SOCKS5(t *testing.T) {
	st := setupFullStack(t)
	defer st.cleanup()

	const size = 1 << 20 // 1 MB
	expected := make([]byte, size)
	rand.Read(expected)
	expectedHash := sha256.Sum256(expected)

	mux := http.NewServeMux()
	mux.HandleFunc("/bigfile", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.Write(expected)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	target := fmt.Sprintf("http://%s/bigfile", ln.Addr().String())

	dialer, err := proxy.SOCKS5("tcp", st.socksAddr, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{Dial: dialer.Dial},
		Timeout:   60 * time.Second,
	}

	resp, err := httpClient.Get(target)
	if err != nil {
		t.Fatalf("HTTP GET failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != size {
		t.Fatalf("expected %d bytes, got %d", size, len(body))
	}
	actualHash := sha256.Sum256(body)
	if actualHash != expectedHash {
		t.Fatal("SHA256 mismatch on 1MB download")
	}
}

// TestWG_SOCKS5_Concurrent fires 10 parallel HTTP requests through the
// WG+SOCKS5 stack and verifies all succeed.
func TestWG_SOCKS5_Concurrent(t *testing.T) {
	st := setupFullStack(t)
	defer st.cleanup()

	const numRequests = 10
	target := fmt.Sprintf("http://%s/ping", st.httpAddr)

	dialer, err := proxy.SOCKS5("tcp", st.socksAddr, nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{Dial: dialer.Dial},
		Timeout:   15 * time.Second,
	}

	var wg sync.WaitGroup
	errs := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := httpClient.Get(target)
			if err != nil {
				errs <- fmt.Errorf("request %d: %w", idx, err)
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- fmt.Errorf("request %d read: %w", idx, err)
				return
			}
			if string(body) != "pong" {
				errs <- fmt.Errorf("request %d: got %q", idx, body)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestWG_MultiPeerConcurrentHTTP(t *testing.T) {
	wt := setupWGMultiPeerTunnel(t, 2)
	defer wt.Close()

	const httpPort = 8081
	ln, err := wt.serverNet.ListenTCP(&net.TCPAddr{
		IP:   net.ParseIP(wt.serverTunIP),
		Port: httpPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("multi-peer-ok"))
	})}
	go srv.Serve(ln)
	defer srv.Close()

	target := fmt.Sprintf("http://%s:%d/ping", wt.serverTunIP, httpPort)
	clients := make([]*http.Client, 0, len(wt.clients))
	for _, peer := range wt.clients {
		clients = append(clients, newNetstackHTTPClient(peer.net, 15*time.Second))
	}

	for i, client := range clients {
		resp, err := client.Get(target)
		if err != nil {
			t.Fatalf("client%d warmup GET: %v", i+1, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("client%d warmup read: %v", i+1, err)
		}
		if string(body) != "multi-peer-ok" {
			t.Fatalf("client%d warmup: got %q", i+1, body)
		}
	}

	const (
		rounds       = 5
		numPerClient = 4
	)
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		errs := make(chan error, len(clients)*numPerClient)

		for clientIdx, client := range clients {
			for reqIdx := 0; reqIdx < numPerClient; reqIdx++ {
				wg.Add(1)
				go func(clientIdx, reqIdx int, client *http.Client) {
					defer wg.Done()

					resp, err := client.Get(target)
					if err != nil {
						errs <- fmt.Errorf("round %d client%d req%d: %w", round, clientIdx+1, reqIdx, err)
						return
					}
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						errs <- fmt.Errorf("round %d client%d req%d read: %w", round, clientIdx+1, reqIdx, err)
						return
					}
					if string(body) != "multi-peer-ok" {
						errs <- fmt.Errorf("round %d client%d req%d: got %q", round, clientIdx+1, reqIdx, body)
					}
				}(clientIdx, reqIdx, client)
			}
		}

		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	}

	stats, err := parseDeviceStats(wt.serverDev)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != len(wt.clients) {
		t.Fatalf("expected %d peers in stats, got %d", len(wt.clients), len(stats))
	}
	for i, stat := range stats {
		if stat.LastHandshake.IsZero() {
			t.Fatalf("peer %d has no handshake timestamp", i)
		}
		if stat.TxBytes == 0 || stat.RxBytes == 0 {
			t.Fatalf("peer %d has no traffic: tx=%d rx=%d", i, stat.TxBytes, stat.RxBytes)
		}
	}
}

// ==================== Benchmarks ====================

// BenchmarkWG_Throughput measures raw TCP throughput through the WG tunnel.
func BenchmarkWG_Throughput(b *testing.B) {
	bs := setupBenchStack(b)
	defer bs.cleanup()

	conn, err := bs.wt.clientNet.Dial("tcp", bs.echoAddr)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	const chunkSize = 32 * 1024
	buf := make([]byte, chunkSize)
	rand.Read(buf)
	recvBuf := make([]byte, chunkSize)

	b.SetBytes(chunkSize)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := conn.Write(buf); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(conn, recvBuf); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWG_Latency measures round-trip latency for small messages.
func BenchmarkWG_Latency(b *testing.B) {
	bs := setupBenchStack(b)
	defer bs.cleanup()

	conn, err := bs.wt.clientNet.Dial("tcp", bs.echoAddr)
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	msg := []byte("ping")
	recvBuf := make([]byte, len(msg))

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := conn.Write(msg); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(conn, recvBuf); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWG_ConnSetup measures the cost of establishing a new TCP connection
// through an existing WG tunnel.
func BenchmarkWG_ConnSetup(b *testing.B) {
	bs := setupBenchStack(b)
	defer bs.cleanup()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		c, err := bs.wt.clientNet.Dial("tcp", bs.echoAddr)
		if err != nil {
			b.Fatal(err)
		}
		c.Close()
	}
}

// ==================== New Feature Tests ====================

// setupWGTunnelWithOpts is like setupWGTunnel but supports custom DNS and PSK.
type wgTunnelOpts struct {
	serverTunIP string
	clientTunIP string
	clientDNS   string // DNS for client netstack, default "127.0.0.1"
	psk         string // hex PSK, empty = disabled
}

func setupWGTunnelWithOpts(t *testing.T, netstackPort int, opts wgTunnelOpts) *wgTunnel {
	t.Helper()
	wgPort := freeUDPPort(t)
	if opts.serverTunIP == "" {
		opts.serverTunIP = "100.64.0.1"
	}
	if opts.clientTunIP == "" {
		opts.clientTunIP = "100.64.0.2"
	}
	if opts.clientDNS == "" {
		opts.clientDNS = "127.0.0.1"
	}

	serverPriv, serverPub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}
	clientPriv, clientPub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}

	// --- server side ---
	sTun, sNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(opts.serverTunIP)},
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		1420,
	)
	if err != nil {
		t.Fatal(err)
	}
	sDev := device.NewDevice(sTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-srv] "))

	sCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(sCfg, "private_key=%s\n", serverPriv)
	fmt.Fprintf(sCfg, "listen_port=%d\n", wgPort)
	fmt.Fprintf(sCfg, "public_key=%s\n", clientPub)
	fmt.Fprintf(sCfg, "allowed_ip=%s/32\n", opts.clientTunIP)
	if opts.psk != "" {
		fmt.Fprintf(sCfg, "preshared_key=%s\n", opts.psk)
	}
	if err := sDev.IpcSetOperation(bufio.NewReader(sCfg)); err != nil {
		t.Fatal(err)
	}
	if err := sDev.Up(); err != nil {
		t.Fatal(err)
	}

	// --- client side ---
	cTun, cNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(opts.clientTunIP)},
		[]netip.Addr{netip.MustParseAddr(opts.clientDNS)},
		1420,
	)
	if err != nil {
		t.Fatal(err)
	}
	cDev := device.NewDevice(cTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-cli] "))

	cCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(cCfg, "private_key=%s\n", clientPriv)
	fmt.Fprintf(cCfg, "public_key=%s\n", serverPub)
	fmt.Fprintf(cCfg, "endpoint=127.0.0.1:%d\n", wgPort)
	fmt.Fprintf(cCfg, "allowed_ip=0.0.0.0/0\n")
	if opts.psk != "" {
		fmt.Fprintf(cCfg, "preshared_key=%s\n", opts.psk)
	}
	if err := cDev.IpcSetOperation(bufio.NewReader(cCfg)); err != nil {
		t.Fatal(err)
	}
	if err := cDev.Up(); err != nil {
		t.Fatal(err)
	}

	return &wgTunnel{
		serverDev:    sDev,
		clientDev:    cDev,
		serverNet:    sNet,
		clientNet:    cNet,
		serverTunIP:  opts.serverTunIP,
		clientTunIP:  opts.clientTunIP,
		netstackPort: netstackPort,
	}
}

// Step 1: UDP echo through WireGuard tunnel
func TestWG_UDP_Echo(t *testing.T) {
	wt := setupWGTunnel(t, 8888)
	defer wt.Close()

	udpAddr := &net.UDPAddr{IP: net.ParseIP(wt.serverTunIP), Port: 9999}

	// server: ListenUDP echo
	sConn, err := wt.serverNet.ListenUDP(udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer sConn.Close()

	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := sConn.ReadFrom(buf)
			if err != nil {
				return
			}
			sConn.WriteTo(buf[:n], addr)
		}
	}()

	// client: DialUDP
	cConn, err := wt.clientNet.DialUDP(nil, udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer cConn.Close()

	msg := []byte("hello-udp")
	cConn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := cConn.Write(msg); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 1500)
	n, err := cConn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != string(msg) {
		t.Fatalf("expected %q, got %q", msg, buf[:n])
	}
	t.Logf("UDP echo OK: %q", buf[:n])
}

// Health check after handshake
func TestWG_HealthCheck(t *testing.T) {
	wt := setupWGTunnel(t, 8888)
	defer wt.Close()

	// Start echo server to trigger handshake
	ln, err := wt.serverNet.ListenTCP(&net.TCPAddr{
		IP: net.ParseIP(wt.serverTunIP), Port: wt.netstackPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.Copy(c, c)
			c.Close()
		}
	}()

	// Client dials to trigger handshake
	c, err := wt.clientNet.Dial("tcp",
		fmt.Sprintf("%s:%d", wt.serverTunIP, wt.netstackPort))
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hi"))
	c.Close()
	time.Sleep(500 * time.Millisecond)

	hs, err := checkHealthFromDevice(wt.clientDev)
	if err != nil {
		t.Fatal(err)
	}
	if !hs.Healthy {
		t.Fatalf("expected healthy tunnel, got stale=%v", hs.StaleDuration)
	}
	t.Logf("HealthCheck: healthy=%v lastHandshake=%v stale=%v OK",
		hs.Healthy, hs.LastHandshake, hs.StaleDuration)
}

// Step 5: Graceful shutdown — Close() doesn't block
func TestWG_GracefulShutdown(t *testing.T) {
	wt := setupWGTunnel(t, 8888)

	done := make(chan struct{})
	go func() {
		wt.Close()
		close(done)
	}()

	select {
	case <-done:
		t.Log("GracefulShutdown OK")
	case <-time.After(10 * time.Second):
		t.Fatal("Close() blocked for >10s")
	}
}

// Step 6: Context cancellation returns immediately
func TestWG_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	d := NewWireguardDialer(ctx)
	// Generate valid keys so we get past the key check
	priv, pub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("wireguard://127.0.0.1:51820?private_key=%s&peer_public_key=%s", priv, pub)

	start := time.Now()
	_, err = d.DialContext(ctx, url)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("DialContext took %v, expected immediate return", elapsed)
	}
	t.Logf("ContextCancel: returned in %v with err=%v OK", elapsed, err)
}

// Step 7: RemovePeer — add then remove a peer
func TestWG_RemovePeer(t *testing.T) {
	wgPort := freeUDPPort(t)
	url := fmt.Sprintf("wireguard://127.0.0.1:%d", wgPort)

	listener := NewWireguardListener(context.Background())
	_, err := listener.Listen(url)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Listen() creates 1 peer
	stats1, err := listener.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats1) != 1 {
		t.Fatalf("expected 1 peer after Listen, got %d", len(stats1))
	}

	// AddPeer creates a 2nd peer
	_, err = listener.AddPeer()
	if err != nil {
		t.Fatal(err)
	}
	stats2, err := listener.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats2) != 2 {
		t.Fatalf("expected 2 peers after AddPeer, got %d", len(stats2))
	}

	// Remove the 2nd peer
	err = listener.RemovePeer(stats2[1].PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	stats3, err := listener.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats3) != 1 {
		t.Fatalf("expected 1 peer after RemovePeer, got %d", len(stats3))
	}
	t.Logf("RemovePeer: 1→2→1 peers OK")
}

// Step 8: PSK — tunnel with preshared key
func TestWG_PSK(t *testing.T) {
	// Generate a 32-byte PSK
	pskBytes := make([]byte, 32)
	rand.Read(pskBytes)
	pskHex := hex.EncodeToString(pskBytes)

	wt := setupWGTunnelWithOpts(t, 8888, wgTunnelOpts{psk: pskHex})
	defer wt.Close()

	// Echo server on server side
	ln, err := wt.serverNet.ListenTCP(&net.TCPAddr{
		IP: net.ParseIP(wt.serverTunIP), Port: wt.netstackPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.Copy(c, c)
			c.Close()
		}
	}()

	// Client connects through PSK tunnel
	c, err := wt.clientNet.Dial("tcp",
		fmt.Sprintf("%s:%d", wt.serverTunIP, wt.netstackPort))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	c.SetDeadline(time.Now().Add(10 * time.Second))
	msg := []byte("psk-test")
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("expected %q, got %q", msg, buf)
	}
	t.Logf("PSK tunnel echo OK")
}

// Step 9: Log level — verbose doesn't panic
func TestWG_LogLevel(t *testing.T) {
	wgPort := freeUDPPort(t)
	url := fmt.Sprintf("wireguard://127.0.0.1:%d?log_level=verbose", wgPort)

	listener := NewWireguardListener(context.Background())
	_, err := listener.Listen(url)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Log("LogLevel=verbose: no panic OK")
}

// Step 10: Custom subnet — tunnel with 10.0.0.x
func TestWG_CustomSubnet(t *testing.T) {
	serverTunIP := "10.0.0.1"
	clientTunIP := "10.0.0.2"

	wt := setupWGTunnelWithOpts(t, 8888, wgTunnelOpts{
		serverTunIP: serverTunIP,
		clientTunIP: clientTunIP,
	})
	defer wt.Close()

	// Echo server
	ln, err := wt.serverNet.ListenTCP(&net.TCPAddr{
		IP: net.ParseIP(serverTunIP), Port: wt.netstackPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.Copy(c, c)
			c.Close()
		}
	}()

	c, err := wt.clientNet.Dial("tcp",
		fmt.Sprintf("%s:%d", serverTunIP, wt.netstackPort))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	c.SetDeadline(time.Now().Add(10 * time.Second))
	msg := []byte("subnet-test")
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("expected %q, got %q", msg, buf)
	}
	t.Logf("CustomSubnet 10.0.0.x echo OK")
}

// Step 11: Pre-configured keys — listener uses externally generated keys
func TestWG_PreConfiguredKeys(t *testing.T) {
	// Pre-generate server and client key pairs
	serverPriv, serverPub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}
	clientPriv, clientPub, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}

	wgPort := freeUDPPort(t)
	serverTunIP := "100.64.0.1"
	netstackPort := 8888

	// Start listener with pre-configured keys
	listenURL := fmt.Sprintf("wireguard://127.0.0.1:%d?private_key=%s&peer_public_key=%s",
		wgPort, serverPriv, clientPub)

	listener := NewWireguardListener(context.Background())
	_, err = listener.Listen(listenURL)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Verify server public key matches what we expect
	expectedPub, err := pubKeyFromPrivHex(serverPriv)
	if err != nil {
		t.Fatal(err)
	}
	if listener.serverPubKey != expectedPub {
		t.Fatalf("serverPubKey mismatch: got %s, want %s", listener.serverPubKey, expectedPub)
	}
	if listener.serverPubKey != serverPub {
		t.Fatalf("serverPubKey != serverPub: got %s, want %s", listener.serverPubKey, serverPub)
	}

	// Start echo handler on the listener
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				io.Copy(c, c)
				c.Close()
			}(c)
		}
	}()

	// Build client side with the pre-generated client keys
	// tun_ip is derived from public key hash (must match server's allowed_ip)
	clientTunIP := tunIPFromPubKey(clientPub)
	cTun, cNet, err := netstack.CreateNetTUN(
		[]netip.Addr{netip.MustParseAddr(clientTunIP)},
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		1420,
	)
	if err != nil {
		t.Fatal(err)
	}
	cDev := device.NewDevice(cTun, conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "[wg-cli] "))
	defer func() {
		cDev.Down()
		time.Sleep(100 * time.Millisecond)
		cDev.Close()
	}()

	cCfg := bytes.NewBuffer(nil)
	fmt.Fprintf(cCfg, "private_key=%s\n", clientPriv)
	fmt.Fprintf(cCfg, "public_key=%s\n", serverPub)
	fmt.Fprintf(cCfg, "endpoint=127.0.0.1:%d\n", wgPort)
	fmt.Fprintf(cCfg, "allowed_ip=0.0.0.0/0\n")
	if err := cDev.IpcSetOperation(bufio.NewReader(cCfg)); err != nil {
		t.Fatal(err)
	}
	if err := cDev.Up(); err != nil {
		t.Fatal(err)
	}

	// Connect through the tunnel
	conn, err := cNet.Dial("tcp", fmt.Sprintf("%s:%d", serverTunIP, netstackPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	msg := []byte("preconfig-key-test")
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("expected %q, got %q", msg, buf)
	}
	t.Logf("PreConfiguredKeys echo OK (serverPub verified)")
}

func TestWG_ListenerAutoPeerTunIPsFromPublicKeys(t *testing.T) {
	wgPort := freeUDPPort(t)
	serverPriv, _, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, clientPub1, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}
	_, clientPub2, err := genWGKeys()
	if err != nil {
		t.Fatal(err)
	}

	listenURL := fmt.Sprintf("wireguard://127.0.0.1:%d?private_key=%s&peer_public_key=%s,%s",
		wgPort, serverPriv, clientPub1, clientPub2)

	listener := NewWireguardListener(context.Background())
	_, err = listener.Listen(listenURL)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	stats, err := listener.GetStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(stats))
	}
	gotAllowedIPs := make(map[string]struct{}, len(stats))
	for _, stat := range stats {
		if len(stat.AllowedIPs) != 1 {
			t.Fatalf("expected exactly 1 allowed IP per peer, got %v", stat.AllowedIPs)
		}
		gotAllowedIPs[stat.AllowedIPs[0]] = struct{}{}
	}
	for _, want := range []string{
		tunIPFromPubKey(clientPub1) + "/32",
		tunIPFromPubKey(clientPub2) + "/32",
	} {
		if _, ok := gotAllowedIPs[want]; !ok {
			t.Fatalf("missing allowed IP %s in %v", want, gotAllowedIPs)
		}
	}
}

func TestWG_BuildClientURLOmitsDerivedTunIP(t *testing.T) {
	listener := &WireguardListener{
		serverPubKey: "server-pub",
		serverTunIP:  DefaultTunIP,
		listenHost:   "127.0.0.1:51820",
		netstackPort: DefaultNetstackPort,
		mtu:          DefaultMTU,
		keepalive:    DefaultKeepalive,
	}

	clientURL := listener.buildClientURL("client-priv")
	u, err := url.Parse(clientURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("private_key") != "client-priv" {
		t.Fatalf("unexpected private_key in %q", clientURL)
	}
	if q.Get("peer_public_key") != "server-pub" {
		t.Fatalf("unexpected peer_public_key in %q", clientURL)
	}
	if q.Get("tun_ip") != "" {
		t.Fatalf("client URL should not expose tun_ip: %q", clientURL)
	}
	if q.Get("allowed_ips") != "" {
		t.Fatalf("client URL should not expose default allowed_ips: %q", clientURL)
	}
	if q.Get("netstack_port") != "" {
		t.Fatalf("client URL should not expose default netstack_port: %q", clientURL)
	}
	if q.Get("peer_ip") != "" {
		t.Fatalf("client URL should not expose default peer_ip: %q", clientURL)
	}
}
