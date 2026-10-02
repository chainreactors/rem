package simplex

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	hnetconn "github.com/chainreactors/rem/harness/netconn"
)

// Config describes a full Simplex harness run.
//
// Provide PacketConn to run the packet-level suites, ARQConn to run the
// stream-level suites, or both. Full enables the heavier throughput,
// concurrency, and stability suites. Perf optionally runs a single directed
// large-transfer benchmark.
type Config struct {
	Name        string
	PacketConn  SimplexPairFactory
	ARQConn     MakePipeline
	Full        bool
	SkipNetConn bool
	Perf        *PerfOptions
}

// Direction identifies the data direction for a directed performance test.
type Direction string

const (
	DirectionC2S Direction = "c2s"
	DirectionS2C Direction = "s2c"
)

// PerfOptions configures TestPerfLargeTransfer.
type PerfOptions struct {
	Label          string
	Size           int
	Direction      Direction
	Timeout        time.Duration
	ChunkSize      int
	TheoreticalMiB func(server, client net.Conn) float64
	Stats          func(conn net.Conn) string
}

// TestAll runs the configured Simplex harness suites.
func TestAll(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.Name != "" {
		t.Run(cfg.Name, func(t *testing.T) {
			cfg.Name = ""
			TestAll(t, cfg)
		})
		return
	}
	if cfg.PacketConn == nil && cfg.ARQConn == nil {
		t.Fatal("simplex harness: PacketConn and ARQConn are both nil")
	}

	if cfg.PacketConn != nil {
		if cfg.Full {
			TestPacketConnFull(t, cfg.PacketConn)
		} else {
			TestPacketConn(t, cfg.PacketConn)
		}
	}

	if cfg.ARQConn != nil {
		testARQConn(t, cfg.ARQConn, cfg.Full, cfg.SkipNetConn)
		if cfg.Perf != nil {
			TestPerfLargeTransfer(t, cfg.ARQConn, *cfg.Perf)
		}
	} else if cfg.Perf != nil {
		t.Fatal("simplex harness: Perf requires ARQConn")
	}
}

// TestPacketConn runs the core packet-level conformance tests.
func TestPacketConn(t *testing.T, factory SimplexPairFactory) {
	t.Helper()
	TestPacketDelivery(t, factory)
	TestPacketLifecycle(t, factory)
}

// TestPacketConnFull runs all packet-level harness suites.
func TestPacketConnFull(t *testing.T, factory SimplexPairFactory) {
	t.Helper()
	TestPacketConn(t, factory)
	TestPacketThroughput(t, factory)
	TestPacketConcurrency(t, factory)
	TestPacketStability(t, factory)
}

// TestARQConn runs the core stream-level conformance tests, including net.Conn.
func TestARQConn(t *testing.T, makePipeline MakePipeline) {
	t.Helper()
	testARQConn(t, makePipeline, false, false)
}

// TestARQConnFull runs all stream-level harness suites, including net.Conn.
func TestARQConnFull(t *testing.T, makePipeline MakePipeline) {
	t.Helper()
	testARQConn(t, makePipeline, true, false)
}

func testARQConn(t *testing.T, makePipeline MakePipeline, full, skipNetConn bool) {
	t.Helper()
	TestTransfer(t, makePipeline)
	TestStreamSemantics(t, makePipeline)
	if full {
		TestThroughput(t, makePipeline)
		TestConcurrency(t, makePipeline)
		TestStability(t, makePipeline)
	}
	if !skipNetConn {
		t.Run("NetConnConformance", func(t *testing.T) {
			hnetconn.TestConn(t, func() (net.Conn, net.Conn, func(), error) {
				return makePipeline(t)
			})
		})
	}
}

// TestPerfLargeTransfer runs a single directed large transfer and logs
// throughput, optional theoretical throughput, and optional connection stats.
func TestPerfLargeTransfer(t *testing.T, makePipeline MakePipeline, opts PerfOptions) {
	t.Helper()
	if opts.Size <= 0 {
		opts.Size = 1024 * 1024
	}
	if opts.Direction == "" {
		opts.Direction = DirectionC2S
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = opts.Size
	}
	if opts.Label == "" {
		opts.Label = fmt.Sprintf("PerfLargeTransfer_%s_%s", strings.ToUpper(string(opts.Direction)), formatSize(opts.Size))
	}

	t.Run(opts.Label, func(t *testing.T) {
		server, client, stop, err := makePipeline(t)
		if err != nil {
			t.Fatalf("MakePipeline: %v", err)
		}
		defer stop()

		payload := makePayload(opts.Size)
		start := time.Now()
		writeErrCh := make(chan error, 1)

		var received []byte
		switch opts.Direction {
		case DirectionS2C:
			go func() { writeErrCh <- writeInChunks(server, payload, opts.ChunkSize) }()
			received, err = readExact(client, opts.Size, opts.Timeout)
		case DirectionC2S:
			go func() { writeErrCh <- writeInChunks(client, payload, opts.ChunkSize) }()
			received, err = readExact(server, opts.Size, opts.Timeout)
		default:
			t.Fatalf("unsupported direction %q", opts.Direction)
		}

		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("%s: got %d/%d in %v, err=%v; server=%s client=%s",
				opts.Direction, len(received), opts.Size, elapsed, err,
				perfStats(opts, server), perfStats(opts, client))
		}
		select {
		case writeErr := <-writeErrCh:
			if writeErr != nil {
				t.Fatalf("%s: writer failed after %v: %v; server=%s client=%s",
					opts.Direction, elapsed, writeErr, perfStats(opts, server), perfStats(opts, client))
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: writer did not finish after receiver completed; server=%s client=%s",
				opts.Direction, perfStats(opts, server), perfStats(opts, client))
		}
		if !bytesEqual(received, payload) {
			t.Fatalf("%s: data integrity failure", opts.Direction)
		}

		mibPerSec := float64(opts.Size) / elapsed.Seconds() / 1024 / 1024
		if opts.TheoreticalMiB != nil {
			theoretical := opts.TheoreticalMiB(server, client)
			efficiency := 0.0
			if theoretical > 0 {
				efficiency = mibPerSec / theoretical * 100
			}
			t.Logf("%s: elapsed=%v throughput=%.2f MiB/s (%.1f Mbps), theoretical=%.2f MiB/s, efficiency=%.1f%%",
				opts.Direction, elapsed.Round(time.Millisecond), mibPerSec, mibPerSec*8, theoretical, efficiency)
			return
		}
		t.Logf("%s: elapsed=%v throughput=%.2f MiB/s (%.1f Mbps)",
			opts.Direction, elapsed.Round(time.Millisecond), mibPerSec, mibPerSec*8)
	})
}

func perfStats(opts PerfOptions, conn net.Conn) string {
	if opts.Stats == nil {
		return "{}"
	}
	return opts.Stats(conn)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
