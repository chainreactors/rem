//go:build dns && !tinygo

package runner

import (
	"fmt"
	"net"
	"os"
	"runtime/pprof"
	"sync/atomic"
	"testing"
	"time"
)

func TestDNSConsoleStartsLocalSOCKS(t *testing.T) {
	dnsPort := freeUDPPort(t)
	socksPort := freePort(t)
	alias := fmt.Sprintf("dns_console_%d", atomic.AddUint32(&testCounter, 1))
	serverURL := fmt.Sprintf("dns://0.0.0.0:%d/?wrapper=raw&domain=rem.test.local&interval=50", dnsPort)
	clientURL := fmt.Sprintf("dns://127.0.0.1:%d/?wrapper=raw&domain=rem.test.local&interval=50", dnsPort)
	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)

	server, err := NewConsoleWithCMD(fmt.Sprintf("--debug -s %s -i 127.0.0.1 --no-sub", serverURL))
	if err != nil {
		t.Fatalf("new server console: %v", err)
	}
	defer server.Close()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run()
	}()
	time.Sleep(200 * time.Millisecond)

	client, err := NewConsoleWithCMD(fmt.Sprintf("-c %s -l socks5://%s -a %s --debug", clientURL, socksAddr, alias))
	if err != nil {
		t.Fatalf("new client console: %v", err)
	}
	defer client.Close()
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- client.Run()
	}()

	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case err := <-serverErr:
			t.Fatalf("server exited before SOCKS ready: %v", err)
		case err := <-clientErr:
			t.Fatalf("client exited before SOCKS ready: %v", err)
		default:
		}

		conn, err := net.DialTimeout("tcp", socksAddr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}

	_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
	t.Fatalf("SOCKS %s not ready: %v", socksAddr, lastErr)
}
