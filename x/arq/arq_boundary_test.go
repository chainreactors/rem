package arq

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

// arq_boundary_test.go exercises extreme configurations that the standard
// test matrix does not cover: ultra-slow polling, minimum window, maximum
// MTU, extreme loss, RTO/interval mismatches, and large transfers.

// runSimulatedARQTransferWithConfig is a configurable variant of
// runSimulatedARQTransfer that allows overriding MTU, WndSize, and other
// ARQ parameters.
func runSimulatedARQTransferWithConfig(t *testing.T, cfg ARQConfig, interval, oneWayDelay time.Duration, maxBodySize, size int, deadline time.Duration) {
	t.Helper()

	clock := newSimulatedClock()
	oldNowFunc := nowFunc
	nowFunc = clock.Now
	defer func() { nowFunc = oldNowFunc }()

	if maxBodySize <= 0 {
		maxBodySize = 190000
	}
	link := newSimulatedPollingLink(clock, interval, oneWayDelay, maxBodySize)

	if cfg.RTO <= 0 {
		cfg.RTO = int((interval * 5) / time.Millisecond)
		if cfg.RTO <= 0 {
			cfg.RTO = 1
		}
	}

	sender := NewARQWithConfig(link.enqueueA, cfg)
	receiver := NewARQWithConfig(link.enqueueB, cfg)

	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte((i*7 + 13) % 251)
	}
	sender.Send(payload)

	step := simulatedStepForInterval(interval)
	limit := clock.Now().Add(deadline)
	var received []byte

	for len(received) < len(payload) {
		if !clock.Now().Before(limit) {
			st := sender.Stats()
			t.Fatalf("timeout: got %d/%d bytes (sndq=%d sndbuf=%d snd=%d/%d)",
				len(received), len(payload), st.SndQueue, st.SndBuf, st.SndUna, st.SndNxt)
		}

		clock.Advance(step)
		now := clock.Now()

		link.deliverDue(now, sender, receiver)
		sender.Update()
		receiver.Update()
		link.flushPending(now)

		if data := receiver.Recv(); len(data) > 0 {
			received = append(received, data...)
		}
	}

	if !bytes.Equal(received, payload) {
		t.Fatal("payload mismatch")
	}
}

// runSimulatedARQTransferWithLoss adds probabilistic loss on top of the
// configurable transfer. dropRatePercent applies equally to both directions.
func runSimulatedARQTransferWithLoss(t *testing.T, cfg ARQConfig, interval, oneWayDelay time.Duration, maxBodySize, size int, deadline time.Duration, dropRatePercent int) {
	t.Helper()

	clock := newSimulatedClock()
	oldNowFunc := nowFunc
	nowFunc = clock.Now
	defer func() { nowFunc = oldNowFunc }()

	if maxBodySize <= 0 {
		maxBodySize = 190000
	}
	link := newSimulatedPollingLink(clock, interval, oneWayDelay, maxBodySize)

	if cfg.RTO <= 0 {
		cfg.RTO = int((interval * 5) / time.Millisecond)
		if cfg.RTO <= 0 {
			cfg.RTO = 1
		}
	}

	sender := NewARQWithConfig(link.enqueueA, cfg)
	receiver := NewARQWithConfig(link.enqueueB, cfg)

	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte((i*7 + 13) % 251)
	}
	sender.Send(payload)

	step := simulatedStepForInterval(interval)
	limit := clock.Now().Add(deadline)
	var received []byte
	dropCounter := 0
	filteredAtoB := 0
	filteredBtoA := 0

	for len(received) < len(payload) {
		if !clock.Now().Before(limit) {
			st := sender.Stats()
			t.Fatalf("timeout: got %d/%d bytes (sndq=%d sndbuf=%d snd=%d/%d drops=%d)",
				len(received), len(payload), st.SndQueue, st.SndBuf, st.SndUna, st.SndNxt, dropCounter)
		}

		clock.Advance(step)
		now := clock.Now()

		// Drop only newly added batches (not already filtered)
		dropCounter += dropNewBatches(&link.aToBInFlight, &filteredAtoB, dropRatePercent, &dropCounter)
		dropCounter += dropNewBatches(&link.bToAInFlight, &filteredBtoA, dropRatePercent, &dropCounter)

		link.deliverDue(now, sender, receiver)
		sender.Update()
		receiver.Update()
		link.flushPending(now)

		if data := receiver.Recv(); len(data) > 0 {
			received = append(received, data...)
		}
	}

	if !bytes.Equal(received, payload) {
		t.Fatal("payload mismatch")
	}

	t.Logf("delivered %d bytes with %d%% loss, %d drops, interval=%v delay=%v",
		size, dropRatePercent, dropCounter, interval, oneWayDelay)
}

// dropNewBatches filters only batches that haven't been filtered yet.
// *filtered tracks how many batches from the head of the queue were already
// processed. After deliverDue removes delivered batches, the counter is
// adjusted by the caller via queue length tracking.
func dropNewBatches(queue *[]queuedBatch, filtered *int, ratePercent int, counter *int) int {
	if ratePercent <= 0 {
		return 0
	}
	// deliverDue may have removed batches from the head; adjust.
	if *filtered > len(*queue) {
		*filtered = len(*queue)
	}
	dropped := 0
	for i := *filtered; i < len(*queue); i++ {
		remaining := (*queue)[i].packets[:0]
		for _, pkt := range (*queue)[i].packets {
			*counter++
			if (*counter % 100) < ratePercent {
				dropped++
				continue
			}
			remaining = append(remaining, pkt)
		}
		(*queue)[i].packets = remaining
	}
	*filtered = len(*queue)
	return dropped
}

// === Boundary Tests ===

func TestBoundary_UltraSlowPolling(t *testing.T) {
	// 10s interval, 5s RTT — extreme slow polling transport (satellite, etc.)
	runSimulatedARQTransferWithConfig(t, ARQConfig{
		MTU: ARQ_MTU,
		RTO: 50000, // 50s
	}, 10*time.Second, 5*time.Second, 190000, 64*1024, 60*time.Minute)
}

func TestBoundary_MinWindow(t *testing.T) {
	// Window=1: stop-and-wait — each segment must be ACKed before next sends
	runSimulatedARQTransferWithConfig(t, ARQConfig{
		MTU:     ARQ_MTU,
		WndSize: 1,
	}, 50*time.Millisecond, 25*time.Millisecond, 190000, 16*1024, 10*time.Minute)
}

func TestBoundary_LargeWindowLargeMTU(t *testing.T) {
	// WndSize=1024, MTU=max — high-bandwidth scenario
	runSimulatedARQTransferWithConfig(t, ARQConfig{
		MTU:     ARQ_MAX_MTU,
		WndSize: 1024,
	}, 10*time.Millisecond, 5*time.Millisecond, ARQ_MAX_MTU*2, 10*1024*1024, 30*time.Minute)
}

func TestBoundary_ExtremeLoss90Percent(t *testing.T) {
	// 90% loss — protocol must either deliver or correctly fail
	cfg := ARQConfig{
		MTU:                ARQ_MTU,
		RTO:                500, // 500ms — fast retransmit for loss recovery
		MaxRetransmissions: 30,  // generous budget for 90% loss
	}
	runSimulatedARQTransferWithLoss(t, cfg,
		100*time.Millisecond, 50*time.Millisecond,
		190000, 4*1024, 60*time.Minute, 90)
}

func TestBoundary_FastRTO(t *testing.T) {
	// RTO (200ms) << interval (1s) — RTO fires multiple times per interval
	runSimulatedARQTransferWithLoss(t, ARQConfig{
		MTU: ARQ_MTU,
		RTO: 200,
	}, 1*time.Second, 500*time.Millisecond, 190000, 32*1024, 30*time.Minute, 10)
}

func TestBoundary_SlowRTO(t *testing.T) {
	// RTO (30s) >> interval (50ms) — NACK is the only fast recovery path
	runSimulatedARQTransferWithLoss(t, ARQConfig{
		MTU:                ARQ_MTU,
		RTO:                30000,
		MaxRetransmissions: 10,
	}, 50*time.Millisecond, 25*time.Millisecond, 190000, 32*1024, 60*time.Minute, 10)
}

func TestBoundary_MinMTU(t *testing.T) {
	// MTU=12 (MSS=1 byte) — extreme fragmentation
	runSimulatedARQTransferWithConfig(t, ARQConfig{
		MTU: ARQ_OVERHEAD + 1, // 12 bytes MTU, 1 byte payload per segment
	}, 10*time.Millisecond, 5*time.Millisecond, 1024, 256, 30*time.Minute)
}

func TestBoundary_LargeTransfer10MB(t *testing.T) {
	// 10MB through ARQ — stress snd_queue, memory, segment numbering
	runSimulatedARQTransferWithConfig(t, ARQConfig{
		MTU: ARQ_MTU,
	}, 10*time.Millisecond, 5*time.Millisecond, 190000, 10*1024*1024, 30*time.Minute)
}

// TestBoundary_Matrix runs a compact matrix across multiple boundary dimensions.
func TestBoundary_Matrix(t *testing.T) {
	cases := []struct {
		name     string
		cfg      ARQConfig
		interval time.Duration
		delay    time.Duration
		bodySize int
		dataSize int
		deadline time.Duration
	}{
		{
			name:     "SmallWindow4",
			cfg:      ARQConfig{MTU: ARQ_MTU, WndSize: 4},
			interval: 50 * time.Millisecond, delay: 25 * time.Millisecond,
			bodySize: 190000, dataSize: 64 * 1024, deadline: 10 * time.Minute,
		},
		{
			name:     "Window256",
			cfg:      ARQConfig{MTU: ARQ_MTU, WndSize: 256},
			interval: 10 * time.Millisecond, delay: 5 * time.Millisecond,
			bodySize: 190000, dataSize: 1024 * 1024, deadline: 10 * time.Minute,
		},
		{
			name:     "LargeMTU9000",
			cfg:      ARQConfig{MTU: 9000},
			interval: 10 * time.Millisecond, delay: 5 * time.Millisecond,
			bodySize: 190000, dataSize: 256 * 1024, deadline: 10 * time.Minute,
		},
		{
			name:     "Interval5s",
			cfg:      ARQConfig{MTU: ARQ_MTU, RTO: 25000},
			interval: 5 * time.Second, delay: 2500 * time.Millisecond,
			bodySize: 190000, dataSize: 32 * 1024, deadline: 60 * time.Minute,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSimulatedARQTransferWithConfig(t, tc.cfg, tc.interval, tc.delay, tc.bodySize, tc.dataSize, tc.deadline)
		})
	}
}

func init() {
	// Ensure formatSize is available (defined in arq_extreme_test.go)
	_ = fmt.Sprintf
}
