//go:build oss

package simplex

import (
	"fmt"
	"testing"
	"time"
)

// --- Boundary Tests ---

// TestOSSFile_NetworkDisconnect tests network failure and recovery.
func TestOSSFile_NetworkDisconnect(t *testing.T) {
	server, client, mock := ossFileTestSetup(t)

	// Phase 1: Normal communication
	msg1 := []byte("before-disconnect")
	client.Send(sendPacket(msg1), client.Addr())
	pkt1, _ := ossFilePollServerUntilReceived(t, server, 5*time.Second)
	if string(pkt1.Data) != string(msg1) {
		t.Fatalf("phase 1: got %q, want %q", pkt1.Data, msg1)
	}

	// Phase 2: Simulate network disconnect
	mock.setFault(true, true)

	// Send during disconnect (should be queued in pendingData)
	msg2 := []byte("during-disconnect")
	client.Send(sendPacket(msg2), client.Addr())

	// Wait a few polling cycles
	time.Sleep(300 * time.Millisecond)

	// Phase 3: Recover network
	mock.setFault(false, false)

	// Data sent during disconnect should eventually arrive
	pkt2, _ := ossFilePollServerUntilReceived(t, server, 10*time.Second)
	if string(pkt2.Data) != string(msg2) {
		t.Fatalf("phase 3: got %q, want %q", pkt2.Data, msg2)
	}
}

// TestOSSFile_ConsecutiveFailureShutdown tests auto-close after too many failures.
func TestOSSFile_ConsecutiveFailureShutdown(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	config.Interval = 20 * time.Millisecond // fast polling for test speed

	addr := ossFileTestAddr(config)
	client, err := NewOSSFileClient(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Set permanent failure
	mock.setFault(true, false)

	// Send data to trigger consecutive failures
	client.Send(sendPacket([]byte("will-fail")), client.Addr())

	// Wait for maxConsecutiveFailures (10) to trigger shutdown
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-client.ctx.Done():
			// Client shut down as expected
			return
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatal("client did not shut down after consecutive failures")
}

// TestOSSFile_ServerRestartMidTransfer tests server restart during data transfer.
func TestOSSFile_ServerRestartMidTransfer(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server1, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	clientAddr.options.Set("client_id", "mid-transfer")
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Send multiple messages rapidly
	for i := 0; i < 3; i++ {
		msg := []byte(fmt.Sprintf("msg-%d", i))
		client.Send(sendPacket(msg), client.Addr())
	}

	// Receive first message on server1
	pkt, _ := ossFilePollServerUntilReceived(t, server1, 5*time.Second)
	t.Logf("server1 received: %s", pkt.Data)

	// Kill server1 mid-transfer
	server1.Close()

	// Start server2
	server2, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer (restart): %v", err)
	}
	defer server2.Close()

	// Send a new message and verify server2 receives it
	newMsg := []byte("after-mid-restart")
	client.Send(sendPacket(newMsg), client.Addr())

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pkt, _, err := server2.Receive()
		if err != nil {
			t.Fatalf("server2.Receive: %v", err)
		}
		if pkt != nil && string(pkt.Data) == string(newMsg) {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server2 did not receive message after restart")
}

// TestOSSFile_ClientRestartWithPendingData tests client restart with queued data.
func TestOSSFile_ClientRestartWithPendingData(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	// Create client and queue data
	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	clientAddr.options.Set("client_id", "pending-test")
	client1, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}

	// Prevent send file from being uploaded (simulate slow network)
	mock.setFault(true, false)
	client1.Send(sendPacket([]byte("pending-data")), client1.Addr())
	time.Sleep(100 * time.Millisecond) // let monitoring try and fail

	// Close client (pendingData is lost)
	client1.Close()
	mock.setFault(false, false)

	// Create new client and verify it can communicate
	clientAddr2 := ossFileTestAddr(&clientConfig)
	clientAddr2.options.Set("client_id", "pending-test-v2")
	client2, err := NewOSSFileClient(clientAddr2, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient v2: %v", err)
	}
	defer client2.Close()

	msg := []byte("fresh-data")
	client2.Send(sendPacket(msg), client2.Addr())
	pkt, _ := ossFilePollServerUntilReceived(t, server, 10*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("got %q, want %q", pkt.Data, msg)
	}
}

// TestOSSFile_HighLatency tests communication with high latency.
func TestOSSFile_HighLatency(t *testing.T) {
	mock := newMockOSSConnector()
	mock.latency = 500 * time.Millisecond // 500ms per operation

	config := ossFileTestConfig()
	config.Interval = 1 * time.Second // slower polling to accommodate latency
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Send and receive with high latency
	msg := []byte("high-latency-data")
	client.Send(sendPacket(msg), client.Addr())

	pkt, addr2 := ossFilePollServerUntilReceived(t, server, 30*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("got %q, want %q", pkt.Data, msg)
	}

	reply := []byte("high-latency-reply")
	server.Send(sendPacket(reply), addr2)

	pkt2 := ossFilePollClientUntilReceived(t, client, 30*time.Second)
	if string(pkt2.Data) != string(reply) {
		t.Fatalf("got %q, want %q", pkt2.Data, reply)
	}
}

// TestOSSFile_HighPacketLoss tests communication with 30% loss rate.
func TestOSSFile_HighPacketLoss(t *testing.T) {
	mock := newMockOSSConnector()
	mock.lossRate = 0.3

	config := ossFileTestConfig()
	config.Interval = 50 * time.Millisecond
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	clientConfig := *config
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// With 30% loss, data should eventually arrive via retries
	msg := []byte("lossy-data")
	client.Send(sendPacket(msg), client.Addr())

	pkt, _ := ossFilePollServerUntilReceived(t, server, 30*time.Second)
	if string(pkt.Data) != string(msg) {
		t.Fatalf("got %q, want %q", pkt.Data, msg)
	}
}

// TestOSSFile_BurstSend tests sending 10 messages in rapid succession.
func TestOSSFile_BurstSend(t *testing.T) {
	server, client, _ := ossFileTestSetup(t)

	const burstCount = 10
	// Send burst
	for i := 0; i < burstCount; i++ {
		msg := []byte(fmt.Sprintf("burst-%d", i))
		client.Send(sendPacket(msg), client.Addr())
	}

	// Collect all messages
	received := make(map[string]bool)
	deadline := time.Now().Add(15 * time.Second)
	for len(received) < burstCount && time.Now().Before(deadline) {
		pkt, _, err := server.Receive()
		if err != nil {
			t.Fatalf("server.Receive: %v", err)
		}
		if pkt != nil {
			received[string(pkt.Data)] = true
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i := 0; i < burstCount; i++ {
		key := fmt.Sprintf("burst-%d", i)
		if !received[key] {
			t.Errorf("missing burst message: %s", key)
		}
	}
}

// TestOSSFile_MaxBodySizeRespect tests server-side outBuffer chunking respects MaxBodySize.
func TestOSSFile_MaxBodySizeRespect(t *testing.T) {
	mock := newMockOSSConnector()
	config := ossFileTestConfig()
	config.MaxBodySize = 512 // Small limit to force chunking on server side
	addr := ossFileTestAddr(config)

	server, err := NewOSSFileServer(addr, config, mock)
	if err != nil {
		t.Fatalf("NewOSSFileServer: %v", err)
	}
	defer server.Close()

	clientConfig := *config
	clientConfig.MaxBodySize = MaxOSSMessageSize // Client uses large limit (no split issue)
	clientAddr := ossFileTestAddr(&clientConfig)
	client, err := NewOSSFileClient(clientAddr, &clientConfig, mock)
	if err != nil {
		t.Fatalf("NewOSSFileClient: %v", err)
	}
	defer client.Close()

	// Client sends one message, server receives, then sends large reply
	msg := []byte("request")
	client.Send(sendPacket(msg), client.Addr())
	_, srvAddr := ossFilePollServerUntilReceived(t, server, 5*time.Second)

	// Server sends multiple packets that exceed MaxBodySize
	// handleClient will chunk them across multiple recv files
	for i := 0; i < 5; i++ {
		data := make([]byte, 200)
		for j := range data {
			data[j] = byte(i)
		}
		server.Send(sendPacket(data), srvAddr)
	}

	// Client should receive all packets eventually (server sends them in chunks)
	received := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pkt, _, err := client.Receive()
		if err != nil {
			t.Fatalf("client.Receive: %v", err)
		}
		if pkt != nil {
			received++
		}
		if received >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if received < 5 {
		t.Fatalf("received %d packets, want 5", received)
	}
}
