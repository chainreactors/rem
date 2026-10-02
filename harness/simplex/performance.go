package simplex

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
	"time"
)

// ── TestThroughput: 带宽边界测试 ──────────────────────────────

func TestThroughput(t *testing.T, makePipeline MakePipeline) {
	t.Helper()

	sizes := []int{1024, 4096, 16384, 65536, 256 * 1024}

	var results []perfResult

	for _, size := range sizes {
		label := "C2S_" + formatSize(size)
		t.Run(label, func(t *testing.T) {
			server, client, stop, err := makePipeline(t)
			if err != nil {
				t.Fatalf("MakePipeline: %v", err)
			}
			defer stop()

			payload := makePayload(size)
			timeout := adaptiveTimeout(size)
			start := time.Now()

			go writeInChunks(client, payload, 32*1024)
			received, err := readExact(server, size, timeout)
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("%s: got %d/%d in %v, err=%v", label, len(received), size, elapsed, err)
			}
			if !bytes.Equal(received, payload) {
				t.Fatalf("%s: data integrity failure", label)
			}

			tp := float64(size) / elapsed.Seconds() / 1024
			results = append(results, perfResult{label, size, elapsed, tp, true})
			t.Logf("%s: %v (%.1f KB/s)", label, elapsed, tp)
		})
	}

	for _, size := range []int{1024, 16384, 65536, 256 * 1024} {
		label := "S2C_" + formatSize(size)
		t.Run(label, func(t *testing.T) {
			server, client, stop, err := makePipeline(t)
			if err != nil {
				t.Fatalf("MakePipeline: %v", err)
			}
			defer stop()

			payload := makePayload(size)
			timeout := adaptiveTimeout(size)
			start := time.Now()

			go writeInChunks(server, payload, 32*1024)
			received, err := readExact(client, size, timeout)
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("%s: got %d/%d in %v, err=%v", label, len(received), size, elapsed, err)
			}
			if !bytes.Equal(received, payload) {
				t.Fatalf("%s: data integrity failure", label)
			}

			tp := float64(size) / elapsed.Seconds() / 1024
			results = append(results, perfResult{label, size, elapsed, tp, true})
			t.Logf("%s: %v (%.1f KB/s)", label, elapsed, tp)
		})
	}

	for _, size := range []int{16384, 65536} {
		label := "Bidir_" + formatSize(size)
		t.Run(label, func(t *testing.T) {
			server, client, stop, err := makePipeline(t)
			if err != nil {
				t.Fatalf("MakePipeline: %v", err)
			}
			defer stop()

			c2sPayload := makePayload(size)
			s2cPayload := make([]byte, size)
			for i := range s2cPayload {
				s2cPayload[i] = byte((i*11 + 37) % 251)
			}

			timeout := adaptiveTimeout(size)
			start := time.Now()

			var wg sync.WaitGroup
			var c2sRcv, s2cRcv []byte
			var c2sErr, s2cErr error

			wg.Add(4)
			go func() { defer wg.Done(); writeInChunks(client, c2sPayload, 32*1024) }()
			go func() { defer wg.Done(); writeInChunks(server, s2cPayload, 32*1024) }()
			go func() { defer wg.Done(); c2sRcv, c2sErr = readExact(server, size, timeout) }()
			go func() { defer wg.Done(); s2cRcv, s2cErr = readExact(client, size, timeout) }()
			wg.Wait()

			elapsed := time.Since(start)
			if c2sErr != nil {
				t.Fatalf("C→S: %v (got %d/%d)", c2sErr, len(c2sRcv), size)
			}
			if s2cErr != nil {
				t.Fatalf("S→C: %v (got %d/%d)", s2cErr, len(s2cRcv), size)
			}
			if !bytes.Equal(c2sRcv, c2sPayload) || !bytes.Equal(s2cRcv, s2cPayload) {
				t.Fatal("bidirectional data integrity failure")
			}

			tp := float64(size*2) / elapsed.Seconds() / 1024
			results = append(results, perfResult{label, size * 2, elapsed, tp, true})
			t.Logf("%s: %v (%.1f KB/s aggregate)", label, elapsed, tp)
		})
	}

	if len(results) > 0 {
		t.Log("\n=== Throughput Report ===")
		t.Logf("%-20s | %-8s | %-12s | %s", "Test", "Size", "Duration", "Throughput")
		t.Logf("%-20s-+-%-8s-+-%-12s-+-%s", "--------------------", "--------", "------------", "-----------")
		for _, r := range results {
			t.Logf("%-20s | %-8s | %-12v | %.1f KB/s", r.label, formatSize(r.size), r.elapsed.Round(time.Millisecond), r.throughput)
		}
	}
}

// ── TestConcurrency: 并发客户端测试 ──────────────────────────

func TestConcurrency(t *testing.T, makePipeline MakePipeline) {
	t.Helper()

	type concurrencyCase struct {
		clients int
		size    int
	}

	cases := []concurrencyCase{
		{2, 16384},
		{5, 16384},
		{10, 4096},
	}

	for _, tc := range cases {
		label := fmt.Sprintf("%dClients_%s", tc.clients, formatSize(tc.size))
		t.Run(label, func(t *testing.T) {
			timeout := adaptiveTimeout(tc.size) + time.Duration(tc.clients)*10*time.Second

			type clientResult struct {
				id       int
				elapsed  time.Duration
				success  bool
				received int
			}

			var (
				wg      sync.WaitGroup
				mu      sync.Mutex
				cResults []clientResult
			)

			start := time.Now()

			for i := 0; i < tc.clients; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()

					server, client, stop, err := makePipeline(t)
					if err != nil {
						mu.Lock()
						cResults = append(cResults, clientResult{id, 0, false, 0})
						mu.Unlock()
						t.Errorf("client %d: MakePipeline: %v", id, err)
						return
					}
					defer stop()

					payload := makePayload(tc.size)
					cStart := time.Now()

					go writeInChunks(client, payload, 32*1024)
					received, err := readExact(server, tc.size, timeout)
					cElapsed := time.Since(cStart)

					success := err == nil && bytes.Equal(received, payload)

					mu.Lock()
					cResults = append(cResults, clientResult{id, cElapsed, success, len(received)})
					mu.Unlock()

					if !success && err != nil {
						t.Errorf("client %d: %v (got %d/%d)", id, err, len(received), tc.size)
					}
				}(i)
			}

			wg.Wait()
			totalElapsed := time.Since(start)

			succeeded := 0
			var totalBytes int
			var maxLatency time.Duration
			for _, r := range cResults {
				if r.success {
					succeeded++
					totalBytes += tc.size
				}
				if r.elapsed > maxLatency {
					maxLatency = r.elapsed
				}
			}

			aggThroughput := float64(totalBytes) / totalElapsed.Seconds() / 1024

			t.Logf("%s: %d/%d succeeded in %v", label, succeeded, tc.clients, totalElapsed.Round(time.Millisecond))
			t.Logf("  Aggregate: %.1f KB/s | Max latency: %v", aggThroughput, maxLatency.Round(time.Millisecond))

			if succeeded < tc.clients {
				t.Errorf("%d/%d clients failed", tc.clients-succeeded, tc.clients)
			}
		})
	}
}

// ── TestStability: 持续负载稳定性 ────────────────────────────

func TestStability(t *testing.T, makePipeline MakePipeline) {
	t.Helper()

	t.Run("PingPong_50rounds", func(t *testing.T) {
		server, client, stop, err := makePipeline(t)
		if err != nil {
			t.Fatalf("MakePipeline: %v", err)
		}
		defer stop()

		const rounds = 50
		const msgSize = 256
		var latencies []time.Duration
		var errors int

		for i := 0; i < rounds; i++ {
			msg := []byte(fmt.Sprintf("ping-%04d-%s", i, string(makePayload(msgSize))))

			start := time.Now()

			if _, err := client.Write(msg); err != nil {
				errors++
				continue
			}

			got, err := readExact(server, len(msg), 30*time.Second)
			if err != nil || !bytes.Equal(got, msg) {
				errors++
				continue
			}

			reply := []byte(fmt.Sprintf("pong-%04d-%s", i, string(makePayload(msgSize))))
			if _, err := server.Write(reply); err != nil {
				errors++
				continue
			}

			got2, err := readExact(client, len(reply), 30*time.Second)
			if err != nil || !bytes.Equal(got2, reply) {
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
		p99idx := len(latencies)*99/100
		if p99idx >= len(latencies) {
			p99idx = len(latencies) - 1
		}
		p99 := latencies[p99idx]

		t.Logf("PingPong: %d/%d rounds OK, %d errors", len(latencies), rounds, errors)
		t.Logf("  Latency: avg=%v p50=%v p95=%v p99=%v",
			avg.Round(time.Millisecond), p50.Round(time.Millisecond),
			p95.Round(time.Millisecond), p99.Round(time.Millisecond))

		if errors > rounds/5 {
			t.Errorf("too many errors: %d/%d", errors, rounds)
		}
	})

	t.Run("SustainedTransfer_30s", func(t *testing.T) {
		server, client, stop, err := makePipeline(t)
		if err != nil {
			t.Fatalf("MakePipeline: %v", err)
		}
		defer stop()

		const chunkSize = 4096
		const duration = 30 * time.Second

		deadline := time.Now().Add(duration)
		var totalSent int
		var sendErr error

		// Sender goroutine
		go func() {
			for time.Now().Before(deadline) {
				payload := makePayload(chunkSize)
				if _, err := client.Write(payload); err != nil {
					sendErr = err
					return
				}
				totalSent += chunkSize
			}
		}()

		// Receiver: read until deadline
		var totalReceived int
		buf := make([]byte, 65536)
		server.SetReadDeadline(deadline.Add(10 * time.Second))

		for time.Now().Before(deadline.Add(5 * time.Second)) {
			n, err := server.Read(buf)
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

		throughput := float64(totalReceived) / duration.Seconds() / 1024

		t.Logf("Sustained 30s: sent %s, received %s (%.1f KB/s)",
			formatSize(totalSent), formatSize(totalReceived), throughput)

		if sendErr != nil {
			t.Logf("  Send error: %v", sendErr)
		}

		if totalReceived == 0 {
			t.Fatal("received no data during sustained transfer")
		}
	})
}

// ── helpers ──────────────────────────────────────────────────

type perfResult struct {
	label      string
	size       int
	elapsed    time.Duration
	throughput float64 // KB/s
	success    bool
}

func adaptiveTimeout(size int) time.Duration {
	// Base: 60s, plus 2s per KB for large payloads
	extra := time.Duration(size/1024) * 2 * time.Second
	timeout := 60*time.Second + extra
	if timeout < 60*time.Second {
		timeout = 60 * time.Second
	}
	if timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	return timeout
}

func avgDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var sum float64
	for _, d := range ds {
		sum += float64(d)
	}
	return time.Duration(math.Round(sum / float64(len(ds))))
}
