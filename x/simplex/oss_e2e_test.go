//go:build oss

package simplex

import (
	"fmt"
	"net/url"
	"testing"
	"time"
)

// --- Test helpers ---

func ossFileTestConfig() *OSSConfig {
	return &OSSConfig{
		Host:        "mock.oss.com",
		Bucket:      "test",
		Prefix:      "/test/",
		Interval:    50 * time.Millisecond,
		MaxBodySize: MaxOSSMessageSize,
		Timeout:     5 * time.Second,
		Mode:        "file",
	}
}

func ossFileTestAddr(config *OSSConfig) *SimplexAddr {
	u, _ := url.Parse("oss://mock.oss.com/test/")
	addr := &SimplexAddr{
		URL:         u,
		id:          "test",
		interval:    config.Interval,
		maxBodySize: config.MaxBodySize,
		options:     url.Values{},
	}
	addr.SetConfig(config)
	return addr
}

func ossFileTestSetup(t *testing.T) (*OSSFileServer, *OSSFileClient, *mockOSSConnector) {
	t.Helper()
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}

	clientConfig := *config // copy
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}

	t.Cleanup(func() {
		client.Close()
		server.Close()
	})

	return server, client, mock
}

func ossFilePollServerUntilReceived(t *testing.T, server *OSSFileServer, timeout time.Duration) (*SimplexPacket, *SimplexAddr) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pkt, addr, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive error: %v", err)
		}
		if pkt != nil {
			return pkt, addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for server to receive packet")
	return nil, nil
}

func ossFilePollClientUntilReceived(t *testing.T, client *OSSFileClient, timeout time.Duration) *SimplexPacket {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pkt, _, err := client.Receive()
		if err != nil {
			t.Fatalf("client.Receive error: %v", err)
		}
		if pkt != nil {
			return pkt
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timeout waiting for client to receive packet")
	return nil
}

func sendPacket(data []byte) *SimplexPackets {
	return &SimplexPackets{
		Packets: []*SimplexPacket{
			{PacketType: SimplexPacketTypeDATA, Data: data},
		},
	}
}

// --- E2E Tests ---

func TestOSSFile_ServerClientFullRoundTrip(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	// Client → Server
	msg := []byte("hello from client")
	if _, err := client.Send(sendPacket(msg), client.Addr()); err != nil {
		t.Fatalf("client.Send: %v", err)
	}

	pkt, addr := ossFilePollServerUntilReceived(t, server, 5*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("server received wrong data: got %q, want %q", pkt.Data, msg)
	}

	// Server → Client
	reply := []byte("hello from server")
	if _, err := server.Send(sendPacket(reply), addr); err != nil {
		t.Fatalf("server.Send: %v", err)
	}

	pkt2 := ossFilePollClientUntilReceived(t, client, 5*time.Second)
	if string(pkt2.Data) != string(reply) {
		t.Fatalf("client received wrong data: got %q, want %q", pkt2.Data, reply)
	}
}

func TestOSSFile_MultipleClients(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	const numClients = 3
	clients := make([]*OSSFileClient, numClients)
	for i := 0; i < numClients; i++ {
		clientConfig := *config
		clientAddr := ossFileTestAddr(&clientConfig)
		clientAddr.options.Set("client_id", fmt.Sprintf("client%d", i))
		c, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
		if err != nil {
			t.Fatalf("NewOSSFileClient[%d]: %v", i, err)
		}
		clients[i] = c
		defer c.Close()
	}

	// Each client sends a unique message
	for i, c := range clients {
		msg := []byte(fmt.Sprintf("msg-from-client-%d", i))
		if _, err := c.Send(sendPacket(msg), c.Addr()); err != nil {
			t.Fatalf("client[%d].Send: %v", i, err)
		}
	}

	// Server should receive all messages
	received := make(map[string]bool)
	deadline := time.Now().Add(10 * time.Second)
	for len(received) < numClients && time.Now().Before(deadline) {
		pkt, _, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive error: %v", err)
		}
		if pkt != nil {
			received[string(pkt.Data)] = true
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i := 0; i < numClients; i++ {
		key := fmt.Sprintf("msg-from-client-%d", i)
		if !received[key] {
			t.Errorf("server did not receive message from client %d", i)
		}
	}
}

func TestOSSFile_BidirectionalMultiRound(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	for round := 0; round < 5; round++ {
		// Client → Server
		c2s := []byte(fmt.Sprintf("c2s-round-%d", round))
		if _, err := client.Send(sendPacket(c2s), client.Addr()); err != nil {
			t.Fatalf("round %d: client.Send: %v", round, err)
		}

		pkt, addr := ossFilePollServerUntilReceived(t, server, 5*time.Second)
		if string(pkt.Data) != string(c2s) {
			t.Fatalf("round %d: server got %q, want %q", round, pkt.Data, c2s)
		}

		// Server → Client
		s2c := []byte(fmt.Sprintf("s2c-round-%d", round))
		if _, err := server.Send(sendPacket(s2c), addr); err != nil {
			t.Fatalf("round %d: server.Send: %v", round, err)
		}

		pkt2 := ossFilePollClientUntilReceived(t, client, 5*time.Second)
		if string(pkt2.Data) != string(s2c) {
			t.Fatalf("round %d: client got %q, want %q", round, pkt2.Data, s2c)
		}
	}
}

func TestOSSFile_LargePayload(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	// 100KB payload
	payload := make([]byte, 100*1024)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	pkts := NewSimplexPacketWithMaxSize(payload, SimplexPacketTypeDATA, client.Addr().MaxBodySize())
	if _, err := client.Send(pkts, client.Addr()); err != nil {
		t.Fatalf("client.Send: %v", err)
	}

	// Collect all packets
	var received []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pkt, _, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive error: %v", err)
		}
		if pkt != nil {
			received = append(received, pkt.Data...)
		}
		if len(received) >= len(payload) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if len(received) != len(payload) {
		t.Fatalf("received %d bytes, want %d", len(received), len(payload))
	}
	for i := range payload {
		if received[i] != payload[i] {
			t.Fatalf("data mismatch at byte %d: got %d, want %d", i, received[i], payload[i])
		}
	}
}

func TestOSSFile_ServerRestart(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server1, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	clientAddr.options.Set("client_id", "stable-client")
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Phase 1: Normal communication
	msg1 := []byte("before-restart")
	client.Send(sendPacket(msg1), client.Addr())
	pkt1, _ := ossFilePollServerUntilReceived(t, server1, 5*time.Second)
	if string(pkt1.Data) != string(msg1) {
		t.Fatalf("phase 1: got %q, want %q", pkt1.Data, msg1)
	}

	// Phase 2: Restart server
	server1.Close()

	server2, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer (restart): %v", err)
	}
	defer server2.Close()

	// Phase 3: Communication should recover
	msg2 := []byte("after-restart")
	client.Send(sendPacket(msg2), client.Addr())
	pkt2, _ := ossFilePollServerUntilReceived(t, server2, 10*time.Second)
	if string(pkt2.Data) != string(msg2) {
		t.Fatalf("phase 3: got %q, want %q", pkt2.Data, msg2)
	}
}

func TestOSSFile_ClientRestart(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	// Phase 1: Create client1 and communicate
	clientConfig := *config
	clientAddr1 := ossFileTestAddr(&clientConfig)
	clientAddr1.options.Set("client_id", "client-v1")
	client1, err := NewOSSFileClient(clientAddr1, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient v1: %v", err)
	}

	msg1 := []byte("from-client-v1")
	client1.Send(sendPacket(msg1), client1.Addr())
	pkt1, _ := ossFilePollServerUntilReceived(t, server, 5*time.Second)
	if string(pkt1.Data) != string(msg1) {
		t.Fatalf("phase 1: got %q, want %q", pkt1.Data, msg1)
	}

	// Phase 2: Close client1, create client2 (different clientID)
	client1.Close()

	clientAddr2 := ossFileTestAddr(&clientConfig)
	clientAddr2.options.Set("client_id", "client-v2")
	client2, err := NewOSSFileClient(clientAddr2, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient v2: %v", err)
	}
	defer client2.Close()

	// Phase 3: Communication with new client
	msg2 := []byte("from-client-v2")
	client2.Send(sendPacket(msg2), client2.Addr())
	pkt2, _ := ossFilePollServerUntilReceived(t, server, 10*time.Second)
	if string(pkt2.Data) != string(msg2) {
		t.Fatalf("phase 3: got %q, want %q", pkt2.Data, msg2)
	}
}
