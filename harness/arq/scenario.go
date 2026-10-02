package arq

import (
	"bytes"
	"testing"
	"time"
)

// TestAsymmetricScenarios verifies ARQ delivery under real-world asymmetric
// network conditions where the two directions have different loss rates.
// Requires a FaultController that implements AsymFaultController.
func TestAsymmetricScenarios(t *testing.T, makeFaultyPipe MakeFaultyPipe) {
	t.Helper()

	requireAsym := func(t *testing.T, faults FaultController) AsymFaultController {
		t.Helper()
		asym, ok := faults.(AsymFaultController)
		if !ok {
			t.Skip("transport does not support AsymFaultController")
		}
		return asym
	}

	// Scenario 1: Data flows fine, but 80% of ACKs are lost.
	// ARQ must rely on RTO retransmission to make progress.
	t.Run("ACK_Blackhole_80pct", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		asym := requireAsym(t, faults)
		asym.SetDropRateAtoB(0)
		asym.SetDropRateBtoA(80)

		size := 8 * 1024
		payload := makePayload(size)
		go func() { writeInChunks(c1, payload, 1024) }()

		received, err := readExact(c2, size, 120*time.Second)
		if err != nil {
			t.Fatalf("ACK blackhole: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("ACK blackhole: data integrity failure")
		}
	})

	// Scenario 2: Upload path has 30% loss, ACK path has 5% loss.
	t.Run("Upload_Loss_30pct", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		asym := requireAsym(t, faults)
		asym.SetDropRateAtoB(30)
		asym.SetDropRateBtoA(5)

		size := 32 * 1024
		payload := makePayload(size)
		go func() { writeInChunks(c1, payload, 4096) }()

		received, err := readExact(c2, size, 120*time.Second)
		if err != nil {
			t.Fatalf("upload loss: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("upload loss: data integrity failure")
		}
	})

	// Scenario 3: Moderate data loss + heavy ACK loss.
	// NACKs also get lost; RTO must be the safety net.
	t.Run("Bidirectional_Asymmetric", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		asym := requireAsym(t, faults)
		asym.SetDropRateAtoB(10)
		asym.SetDropRateBtoA(40)

		size := 64 * 1024
		payload := makePayload(size)
		go func() { writeInChunks(c1, payload, 8192) }()

		received, err := readExact(c2, size, 120*time.Second)
		if err != nil {
			t.Fatalf("bidir asym: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("bidir asym: data integrity failure")
		}
	})

	// Scenario 4: Data path burst loss (50% for 1s), ACK path clean.
	t.Run("BurstLoss_DataPath", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		asym := requireAsym(t, faults)
		asym.SetDropRateAtoB(0)
		asym.SetDropRateBtoA(0)

		size := 32 * 1024
		payload := makePayload(size)

		go func() {
			time.Sleep(200 * time.Millisecond)
			asym.SetDropRateAtoB(50)
			time.Sleep(1 * time.Second)
			asym.SetDropRateAtoB(0)
		}()

		go func() { writeInChunks(c1, payload, 4096) }()

		received, err := readExact(c2, size, 120*time.Second)
		if err != nil {
			t.Fatalf("burst data loss: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("burst data loss: data integrity failure")
		}
	})

	// Scenario 5: ACK path burst loss (50% for 1s), data path clean.
	// Window temporarily stalls; must recover when ACKs resume.
	t.Run("BurstLoss_ACKPath", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		asym := requireAsym(t, faults)
		asym.SetDropRateAtoB(0)
		asym.SetDropRateBtoA(0)

		size := 32 * 1024
		payload := makePayload(size)

		go func() {
			time.Sleep(200 * time.Millisecond)
			asym.SetDropRateBtoA(50)
			time.Sleep(1 * time.Second)
			asym.SetDropRateBtoA(0)
		}()

		go func() { writeInChunks(c1, payload, 4096) }()

		received, err := readExact(c2, size, 120*time.Second)
		if err != nil {
			t.Fatalf("burst ACK loss: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("burst ACK loss: data integrity failure")
		}
	})

	// Scenario 6: Data loss alternates between 0% and 30% every 2s.
	t.Run("Alternating_Loss", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		asym := requireAsym(t, faults)
		done := make(chan struct{})
		go func() {
			toggle := false
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if toggle {
						asym.SetDropRateAtoB(30)
					} else {
						asym.SetDropRateAtoB(0)
					}
					toggle = !toggle
				}
			}
		}()

		size := 64 * 1024
		payload := makePayload(size)
		go func() { writeInChunks(c1, payload, 8192) }()

		received, err := readExact(c2, size, 120*time.Second)
		close(done)
		asym.SetDropRateAtoB(0)

		if err != nil {
			t.Fatalf("alternating loss: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("alternating loss: data integrity failure")
		}
	})

	// Scenario 7: Heavy symmetric loss (50% both directions).
	// End-to-end success probability per segment = 50% × 50% = 25% per attempt.
	// With MaxRetrans=30 and RTO=500ms, budget = 15s per segment — should deliver.
	t.Run("Heavy_Symmetric_Loss_50pct", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		faults.SetDropRate(50)

		size := 4 * 1024
		payload := makePayload(size)
		go func() { writeInChunks(c1, payload, 512) }()

		received, err := readExact(c2, size, 120*time.Second)
		faults.SetDropRate(0)

		if err != nil {
			t.Fatalf("heavy symmetric loss: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("heavy symmetric loss: data integrity failure")
		}
	})

	// Scenario 8: One-direction complete partition for 3s, then recovery.
	// Data cannot reach receiver but ACKs flow back normally.
	t.Run("One_Direction_Partition", func(t *testing.T) {
		c1, c2, faults, stop, err := makeFaultyPipe(t)
		if err != nil {
			t.Fatalf("MakeFaultyPipe: %v", err)
		}
		defer stop()

		asym := requireAsym(t, faults)

		size := 16 * 1024
		payload := makePayload(size)

		go func() {
			time.Sleep(300 * time.Millisecond)
			asym.SetDropRateAtoB(100)
			time.Sleep(3 * time.Second)
			asym.SetDropRateAtoB(0)
		}()

		go func() { writeInChunks(c1, payload, 2048) }()

		received, err := readExact(c2, size, 120*time.Second)
		if err != nil {
			t.Fatalf("one-dir partition: got %d/%d, err=%v", len(received), size, err)
		}
		if !bytes.Equal(received, payload) {
			t.Fatal("one-dir partition: data integrity failure")
		}
	})
}
