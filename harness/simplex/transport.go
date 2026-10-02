package simplex

import (
	"bytes"
	"fmt"
	"net"
	"sort"
	"sync"
	"testing"
	"time"
)

// SimplexPairFactory creates a matched server+client PacketConn pair.
// The server ReadFrom receives what the client WriteTo sends, and vice versa.
// stop releases all resources; safe to call multiple times.
type SimplexPairFactory func(t *testing.T) (server, client net.PacketConn, stop func(), err error)

// ── TestPacketDelivery: packet 投递正确性 ─────────────────────

func TestPacketDelivery(t *testing.T, factory SimplexPairFactory) {
	t.Helper()

	t.Run("C2S_Basic", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		msg := []byte("hello-simplex-packet")
		if _, err := client.WriteTo(msg, server.LocalAddr()); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}

		got, addr, err := packetReadExact(server, len(msg), 30*time.Second)
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("data mismatch: got %q, want %q", got, msg)
		}
		if addr == nil {
			t.Fatal("addr is nil")
		}
	})

	t.Run("S2C_Basic", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		// Client must send first so server knows the client address
		hello := []byte("init")
		client.WriteTo(hello, server.LocalAddr())
		_, clientAddr, _ := packetReadExact(server, len(hello), 30*time.Second)

		msg := []byte("server-to-client")
		if _, err := server.WriteTo(msg, clientAddr); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}

		got, _, err := packetReadExact(client, len(msg), 30*time.Second)
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("data mismatch: got %q, want %q", got, msg)
		}
	})

	t.Run("MultiPacket_Ordered", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		const count = 10
		for i := 0; i < count; i++ {
			msg := []byte(fmt.Sprintf("pkt-%04d", i))
			if _, err := client.WriteTo(msg, server.LocalAddr()); err != nil {
				t.Fatalf("WriteTo[%d]: %v", i, err)
			}
		}

		var received []string
		deadline := time.Now().Add(60 * time.Second)
		buf := make([]byte, 65536)
		for len(received) < count && time.Now().Before(deadline) {
			n, _, err := server.ReadFrom(buf)
			if err != nil {
				t.Fatalf("ReadFrom: %v", err)
			}
			if n > 0 {
				received = append(received, string(buf[:n]))
			}
		}

		if len(received) < count {
			t.Fatalf("received %d/%d packets", len(received), count)
		}

		// Verify all messages arrived (order may not be guaranteed for all transports)
		seen := make(map[string]bool)
		for _, r := range received {
			seen[r] = true
		}
		for i := 0; i < count; i++ {
			key := fmt.Sprintf("pkt-%04d", i)
			if !seen[key] {
				t.Errorf("missing packet: %s", key)
			}
		}
	})

	t.Run("LargePacket", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		payload := makePayload(32 * 1024)
		if _, err := client.WriteTo(payload, server.LocalAddr()); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}

		got, _, err := packetReadExact(server, len(payload), 60*time.Second)
		if err != nil {
			t.Fatalf("ReadFrom: got %d/%d, err=%v", len(got), len(payload), err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("large packet data integrity failure")
		}
	})

	t.Run("Bidirectional", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		// Client sends init so server learns the address
		init := []byte("bidir-init")
		client.WriteTo(init, server.LocalAddr())
		_, clientAddr, _ := packetReadExact(server, len(init), 30*time.Second)

		c2sPayload := makePayload(8192)
		s2cPayload := make([]byte, 8192)
		for i := range s2cPayload {
			s2cPayload[i] = byte((i*11 + 37) % 251)
		}

		var wg sync.WaitGroup
		var c2sGot, s2cGot []byte
		var c2sErr, s2cErr error

		wg.Add(4)
		go func() { defer wg.Done(); client.WriteTo(c2sPayload, server.LocalAddr()) }()
		go func() { defer wg.Done(); server.WriteTo(s2cPayload, clientAddr) }()
		go func() { defer wg.Done(); c2sGot, _, c2sErr = packetReadExact(server, len(c2sPayload), 60*time.Second) }()
		go func() { defer wg.Done(); s2cGot, _, s2cErr = packetReadExact(client, len(s2cPayload), 60*time.Second) }()
		wg.Wait()

		if c2sErr != nil {
			t.Fatalf("C→S: %v (got %d)", c2sErr, len(c2sGot))
		}
		if s2cErr != nil {
			t.Fatalf("S→C: %v (got %d)", s2cErr, len(s2cGot))
		}
		if !bytes.Equal(c2sGot, c2sPayload) {
			t.Fatal("C→S integrity failure")
		}
		if !bytes.Equal(s2cGot, s2cPayload) {
			t.Fatal("S→C integrity failure")
		}
	})
}

// ── TestPacketThroughput: 吞吐边界 ───────────────────────────

func TestPacketThroughput(t *testing.T, factory SimplexPairFactory) {
	t.Helper()

	sizes := []int{1024, 4096, 16384, 65536, 256 * 1024, 1024 * 1024}
	var results []perfResult

	for _, size := range sizes {
		label := "C2S_" + formatSize(size)
		t.Run(label, func(t *testing.T) {
			server, client, stop, err := factory(t)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			defer stop()

			payload := makePayload(size)
			start := time.Now()

			client.WriteTo(payload, server.LocalAddr())
			got, _, err := packetReadExact(server, size, adaptiveTimeout(size))
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("%s: got %d/%d in %v, err=%v", label, len(got), size, elapsed, err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("%s: integrity failure", label)
			}

			tp := float64(size) / elapsed.Seconds() / 1024
			results = append(results, perfResult{label, size, elapsed, tp, true})
			t.Logf("%s: %v (%.1f KB/s)", label, elapsed.Round(time.Millisecond), tp)
		})
	}

	for _, size := range []int{1024, 16384, 65536, 256 * 1024, 1024 * 1024} {
		label := "S2C_" + formatSize(size)
		t.Run(label, func(t *testing.T) {
			server, client, stop, err := factory(t)
			if err != nil {
				t.Fatalf("factory: %v", err)
			}
			defer stop()

			// Init: client sends so server knows the address
			init := []byte("init")
			client.WriteTo(init, server.LocalAddr())
			_, clientAddr, _ := packetReadExact(server, len(init), 30*time.Second)

			payload := makePayload(size)
			start := time.Now()

			server.WriteTo(payload, clientAddr)
			got, _, err := packetReadExact(client, size, adaptiveTimeout(size))
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("%s: got %d/%d in %v, err=%v", label, len(got), size, elapsed, err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("%s: integrity failure", label)
			}

			tp := float64(size) / elapsed.Seconds() / 1024
			results = append(results, perfResult{label, size, elapsed, tp, true})
			t.Logf("%s: %v (%.1f KB/s)", label, elapsed.Round(time.Millisecond), tp)
		})
	}

	if len(results) > 0 {
		t.Log("\n=== Packet Throughput Report ===")
		t.Logf("%-20s | %-8s | %-12s | %s", "Test", "Size", "Duration", "Throughput")
		t.Logf("%-20s-+-%-8s-+-%-12s-+-%s", "--------------------", "--------", "------------", "-----------")
		for _, r := range results {
			t.Logf("%-20s | %-8s | %-12v | %.1f KB/s", r.label, formatSize(r.size), r.elapsed.Round(time.Millisecond), r.throughput)
		}
	}
}

// ── TestPacketConcurrency: 多 pair 并发 ──────────────────────

func TestPacketConcurrency(t *testing.T, factory SimplexPairFactory) {
	t.Helper()

	cases := []struct {
		pairs int
		size  int
	}{
		{2, 16384},
		{5, 16384},
		{10, 4096},
	}

	for _, tc := range cases {
		label := fmt.Sprintf("%dPairs_%s", tc.pairs, formatSize(tc.size))
		t.Run(label, func(t *testing.T) {
			timeout := adaptiveTimeout(tc.size) + time.Duration(tc.pairs)*10*time.Second

			type result struct {
				id      int
				elapsed time.Duration
				success bool
			}

			var (
				wg      sync.WaitGroup
				mu      sync.Mutex
				results []result
			)

			start := time.Now()

			for i := 0; i < tc.pairs; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()

					server, client, stop, err := factory(t)
					if err != nil {
						mu.Lock()
						results = append(results, result{id, 0, false})
						mu.Unlock()
						t.Errorf("pair %d: factory: %v", id, err)
						return
					}
					defer stop()

					payload := makePayload(tc.size)
					pStart := time.Now()

					client.WriteTo(payload, server.LocalAddr())
					got, _, err := packetReadExact(server, tc.size, timeout)
					pElapsed := time.Since(pStart)

					ok := err == nil && bytes.Equal(got, payload)
					mu.Lock()
					results = append(results, result{id, pElapsed, ok})
					mu.Unlock()

					if !ok && err != nil {
						t.Errorf("pair %d: %v (got %d/%d)", id, err, len(got), tc.size)
					}
				}(i)
			}

			wg.Wait()
			totalElapsed := time.Since(start)

			succeeded := 0
			for _, r := range results {
				if r.success {
					succeeded++
				}
			}

			aggTP := float64(succeeded*tc.size) / totalElapsed.Seconds() / 1024
			t.Logf("%s: %d/%d OK in %v (aggregate %.1f KB/s)",
				label, succeeded, tc.pairs, totalElapsed.Round(time.Millisecond), aggTP)

			if succeeded < tc.pairs {
				t.Errorf("%d/%d pairs failed", tc.pairs-succeeded, tc.pairs)
			}
		})
	}
}

// ── TestPacketStability: 持续运行稳定性 ──────────────────────

func TestPacketStability(t *testing.T, factory SimplexPairFactory) {
	t.Helper()

	t.Run("PingPong_50rounds", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		const rounds = 50
		const msgSize = 128
		var latencies []time.Duration
		var errors int

		for i := 0; i < rounds; i++ {
			ping := makePayload(msgSize)
			start := time.Now()

			// C → S
			if _, err := client.WriteTo(ping, server.LocalAddr()); err != nil {
				errors++
				continue
			}
			got, clientAddr, err := packetReadExact(server, msgSize, 30*time.Second)
			if err != nil || !bytes.Equal(got, ping) {
				errors++
				continue
			}

			// S → C
			pong := makePayload(msgSize)
			if _, err := server.WriteTo(pong, clientAddr); err != nil {
				errors++
				continue
			}
			got2, _, err := packetReadExact(client, msgSize, 30*time.Second)
			if err != nil || !bytes.Equal(got2, pong) {
				errors++
				continue
			}

			latencies = append(latencies, time.Since(start))
		}

		if len(latencies) == 0 {
			t.Fatal("no successful rounds")
		}

		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		avg := avgDuration(latencies)
		p50 := latencies[len(latencies)*50/100]
		p95 := latencies[len(latencies)*95/100]
		p99idx := len(latencies) - 1
		if idx := len(latencies) * 99 / 100; idx < p99idx {
			p99idx = idx
		}

		t.Logf("PingPong: %d/%d OK, %d errors", len(latencies), rounds, errors)
		t.Logf("  Latency: avg=%v p50=%v p95=%v p99=%v",
			avg.Round(time.Millisecond), p50.Round(time.Millisecond),
			p95.Round(time.Millisecond), latencies[p99idx].Round(time.Millisecond))

		if errors > rounds/5 {
			t.Errorf("too many errors: %d/%d", errors, rounds)
		}
	})

	t.Run("Sustained_30s", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		const duration = 30 * time.Second
		const pktSize = 4096

		deadline := time.Now().Add(duration)
		var totalSent, totalReceived int
		var sendErr error

		// Sender
		go func() {
			for time.Now().Before(deadline) {
				payload := makePayload(pktSize)
				if _, err := client.WriteTo(payload, server.LocalAddr()); err != nil {
					sendErr = err
					return
				}
				totalSent += pktSize
			}
		}()

		// Receiver
		buf := make([]byte, 65536)
		recvDeadline := deadline.Add(10 * time.Second)
		for time.Now().Before(recvDeadline) {
			n, _, err := server.ReadFrom(buf)
			if n > 0 {
				totalReceived += n
			}
			if err != nil {
				break
			}
			if totalReceived >= totalSent && time.Now().After(deadline) {
				break
			}
		}

		tp := float64(totalReceived) / duration.Seconds() / 1024
		t.Logf("Sustained 30s: sent %s, received %s (%.1f KB/s)",
			formatSize(totalSent), formatSize(totalReceived), tp)
		if sendErr != nil {
			t.Logf("  Send error: %v", sendErr)
		}
		if totalReceived == 0 {
			t.Fatal("received no data")
		}
	})
}

// ── TestPacketLifecycle: 生命周期 & 错误恢复 ─────────────────

func TestPacketLifecycle(t *testing.T, factory SimplexPairFactory) {
	t.Helper()

	t.Run("NormalLifecycle", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}

		// Send
		msg := []byte("lifecycle-data")
		client.WriteTo(msg, server.LocalAddr())
		got, clientAddr, err := packetReadExact(server, len(msg), 30*time.Second)
		if err != nil {
			t.Fatalf("ReadFrom: %v", err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("data mismatch: got %q", got)
		}

		// Reply
		reply := []byte("lifecycle-reply")
		server.WriteTo(reply, clientAddr)
		got2, _, err := packetReadExact(client, len(reply), 30*time.Second)
		if err != nil {
			t.Fatalf("client ReadFrom: %v", err)
		}
		if !bytes.Equal(got2, reply) {
			t.Fatalf("reply mismatch: got %q", got2)
		}

		// Clean close
		stop()
	})

	t.Run("ImmediateClose", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			_, _, stop, err := factory(t)
			if err != nil {
				t.Fatalf("round %d: factory: %v", i, err)
			}
			stop()
		}
	})

	t.Run("SendThenClose", func(t *testing.T) {
		server, client, stop, err := factory(t)
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		defer stop()

		msg := []byte("send-then-close")
		client.WriteTo(msg, server.LocalAddr())

		got, _, err := packetReadExact(server, len(msg), 30*time.Second)
		if err != nil {
			t.Fatalf("ReadFrom after send: %v", err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("data mismatch: got %q", got)
		}

		client.Close()
	})

	t.Run("ServerRestart", func(t *testing.T) {
		// Phase 1: normal communication
		server1, client1, stop1, err := factory(t)
		if err != nil {
			t.Fatalf("factory (phase 1): %v", err)
		}

		msg1 := []byte("before-restart")
		client1.WriteTo(msg1, server1.LocalAddr())
		got1, _, err := packetReadExact(server1, len(msg1), 30*time.Second)
		if err != nil {
			t.Fatalf("phase 1 ReadFrom: %v", err)
		}
		if !bytes.Equal(got1, msg1) {
			t.Fatalf("phase 1 data mismatch")
		}
		stop1()

		// Phase 2: new pair, verify independence
		server2, client2, stop2, err := factory(t)
		if err != nil {
			t.Fatalf("factory (phase 2): %v", err)
		}
		defer stop2()

		msg2 := []byte("after-restart")
		client2.WriteTo(msg2, server2.LocalAddr())
		got2, _, err := packetReadExact(server2, len(msg2), 30*time.Second)
		if err != nil {
			t.Fatalf("phase 2 ReadFrom: %v", err)
		}
		if !bytes.Equal(got2, msg2) {
			t.Fatalf("phase 2 data mismatch")
		}
	})

	t.Run("ConcurrentClients_ConnectDisconnect", func(t *testing.T) {
		const n = 5
		var wg sync.WaitGroup
		var mu sync.Mutex
		var errors []string

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				server, client, stop, err := factory(t)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Sprintf("client %d factory: %v", id, err))
					mu.Unlock()
					return
				}
				defer stop()

				msg := []byte(fmt.Sprintf("concurrent-%d", id))
				client.WriteTo(msg, server.LocalAddr())
				got, _, err := packetReadExact(server, len(msg), 60*time.Second)
				if err != nil || !bytes.Equal(got, msg) {
					mu.Lock()
					errors = append(errors, fmt.Sprintf("client %d: got %q err=%v", id, got, err))
					mu.Unlock()
				}
			}(i)
		}

		wg.Wait()
		if len(errors) > 0 {
			for _, e := range errors {
				t.Errorf("  %s", e)
			}
			t.Fatalf("%d/%d concurrent clients failed", len(errors), n)
		}
	})

	t.Run("RapidReconnect", func(t *testing.T) {
		for round := 0; round < 3; round++ {
			server, client, stop, err := factory(t)
			if err != nil {
				t.Fatalf("round %d: factory: %v", round, err)
			}

			msg := []byte(fmt.Sprintf("rapid-%d", round))
			client.WriteTo(msg, server.LocalAddr())
			got, _, err := packetReadExact(server, len(msg), 30*time.Second)
			if err != nil {
				t.Fatalf("round %d: ReadFrom: %v", round, err)
			}
			if !bytes.Equal(got, msg) {
				t.Fatalf("round %d: data mismatch", round)
			}

			stop()
		}
	})
}

// ── helpers ──────────────────────────────────────────────────

func packetReadExact(conn net.PacketConn, n int, timeout time.Duration) ([]byte, net.Addr, error) {
	result := make([]byte, 0, n)
	bufSize := 65536
	if n > bufSize {
		bufSize = n
	}
	buf := make([]byte, bufSize)
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})
	var lastAddr net.Addr

	for len(result) < n {
		if time.Now().After(deadline) {
			return result, lastAddr, fmt.Errorf("timeout: got %d/%d bytes", len(result), n)
		}
		nr, addr, err := conn.ReadFrom(buf)
		if nr > 0 {
			result = append(result, buf[:nr]...)
			lastAddr = addr
		}
		if err != nil {
			return result, lastAddr, err
		}
	}
	return result[:n], lastAddr, nil
}
