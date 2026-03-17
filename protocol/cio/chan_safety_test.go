package cio

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/message"
)

// ---------------------------------------------------------------------------
// Channel.Close — sync.Once correctness & no-hang guarantees
// ---------------------------------------------------------------------------

// TestChannel_CloseUnblocksSender verifies that a Sender goroutine blocked on
// writing to the channel is unblocked when Close() is called, preventing hangs.
func TestChannel_CloseUnblocksSender(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()

	ch := NewChan("test-sender-unblock", 0) // unbuffered — Sender blocks on chan write

	done := make(chan error, 1)
	go func() {
		done <- ch.Sender(c1)
	}()

	// Write a message so Sender has something to send
	go func() {
		WriteMsg(c2, &message.Ping{Ping: "trigger"})
	}()

	// Let Sender start processing
	time.Sleep(50 * time.Millisecond)

	// Close should unblock Sender
	ch.Close()
	c1.Close()

	select {
	case <-done:
		// OK — Sender returned
	case <-time.After(3 * time.Second):
		t.Fatal("Sender did not unblock after Close() — potential deadlock")
	}
}

// TestChannel_CloseUnblocksReceiver verifies that a Receiver goroutine blocked
// on ReadMsg is unblocked when the underlying conn is closed via Close().
func TestChannel_CloseUnblocksReceiver(t *testing.T) {
	c1, c2 := net.Pipe()

	ch := NewChan("test-receiver-unblock", 10)

	done := make(chan error, 1)
	go func() {
		done <- ch.Receiver(c2)
	}()

	// Let Receiver block on ReadMsg
	time.Sleep(50 * time.Millisecond)

	// Close the conn to unblock ReadMsg, then close channel
	c1.Close()
	ch.Close()

	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("Receiver did not unblock — potential deadlock")
	}
}

// TestChannel_SendDuringClose verifies that concurrent Send and Close don't
// cause a panic or deadlock.
func TestChannel_SendDuringClose(t *testing.T) {
	ch := NewChan("test-send-during-close", 10)

	var wg sync.WaitGroup
	// Multiple senders
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				err := ch.Send(1, &message.Ping{Ping: "x"})
				if err != nil {
					return // channel closed, expected
				}
			}
		}()
	}

	// Close after a small delay
	time.Sleep(10 * time.Millisecond)
	ch.Close()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent Send+Close caused a hang")
	}
}

// TestChannel_SenderWriteErrorClosesCleanly verifies that when the connection
// has a write error, Sender closes the channel and returns without hanging.
func TestChannel_SenderWriteErrorClosesCleanly(t *testing.T) {
	c1, c2 := net.Pipe()
	c2.Close() // close reader side — writes will fail

	ch := NewChan("test-sender-write-err", 10)
	ch.C <- &Message{ID: 1, Message: &message.Ping{Ping: "x"}}

	done := make(chan error, 1)
	go func() {
		done <- ch.Sender(c1)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from Sender on broken conn")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Sender hung on write error")
	}

	// Channel should be closed
	select {
	case <-ch.StopCh:
	default:
		t.Fatal("StopCh not closed after Sender write error")
	}
}

// TestChannel_ReceiverClosesOnReadError verifies that Receiver properly closes
// the channel and returns when the connection breaks.
func TestChannel_ReceiverClosesOnReadError(t *testing.T) {
	c1, c2 := net.Pipe()

	ch := NewChan("test-receiver-read-err", 10)

	done := make(chan error, 1)
	go func() {
		done <- ch.Receiver(c2)
	}()

	// Close writer side — Receiver's ReadMsg will fail
	c1.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from Receiver on broken conn")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Receiver hung on read error")
	}

	// Channel should be closed
	select {
	case <-ch.StopCh:
	default:
		t.Fatal("StopCh not closed after Receiver read error")
	}
}

// TestChannel_UnbufferedSendBlocksUntilReceive verifies that unbuffered
// channel Send blocks until someone reads, and Close properly unblocks it.
func TestChannel_UnbufferedSendBlocksUntilReceive(t *testing.T) {
	ch := NewChan("test-unbuf", 0) // unbuffered

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- ch.Send(1, &message.Ping{Ping: "block"})
	}()

	// Send should be blocking
	select {
	case <-sendDone:
		t.Fatal("Send on unbuffered channel should block")
	case <-time.After(50 * time.Millisecond):
		// Expected — still blocking
	}

	// Close should unblock Send
	ch.Close()

	select {
	case err := <-sendDone:
		if err == nil {
			t.Fatal("Send should return error after Close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Send still blocked after Close — deadlock")
	}
}

// TestChannel_SenderAndReceiverConcurrentClose verifies that running Sender
// and Receiver on the same channel, then closing, doesn't hang.
func TestChannel_SenderAndReceiverConcurrentClose(t *testing.T) {
	serverC, clientC := net.Pipe()
	recvC1, recvC2 := net.Pipe()

	ch := NewChan("test-dual-close", 10)

	var wg sync.WaitGroup
	wg.Add(2)

	// Receiver reads from recvC2
	go func() {
		defer wg.Done()
		ch.Receiver(recvC2)
	}()

	// Sender writes to serverC
	go func() {
		defer wg.Done()
		ch.Sender(serverC)
	}()

	// Feed some messages through the Receiver path
	go func() {
		for i := 0; i < 5; i++ {
			WriteMsg(recvC1, &message.Ping{Ping: "x"})
		}
	}()

	// Consume on the Sender path
	go func() {
		for i := 0; i < 5; i++ {
			ReadMsg(clientC)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Close everything
	ch.Close()
	recvC1.Close()
	recvC2.Close()
	serverC.Close()
	clientC.Close()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Sender/Receiver did not exit after Close — deadlock")
	}
}

// TestChannel_CloseStatsCleared verifies that pending stats are properly
// cleared after Close, not leaked.
func TestChannel_CloseStatsCleared(t *testing.T) {
	ch := NewChan("test-stats-clear", 100) // large buffer to avoid blocking
	ch.Mod = Sender

	// Add some pending stats (within buffer capacity)
	for i := 0; i < 50; i++ {
		ch.Send(uint64(i%10), &message.Ping{Ping: "x"})
	}

	// Verify pending > 0
	if ch.stats.pendingCount.Count() == 0 {
		t.Fatal("should have pending count before close")
	}

	ch.Close()

	// After close, stats should be cleared
	if ch.stats.pendingCount.Count() != 0 {
		t.Fatalf("pendingCount should be 0 after Close, got %d", ch.stats.pendingCount.Count())
	}
	if ch.stats.pendingSize.Count() != 0 {
		t.Fatalf("pendingSize should be 0 after Close, got %d", ch.stats.pendingSize.Count())
	}
}

// TestChannel_RapidCreateCloseLoop verifies no goroutine leaks or hangs
// when rapidly creating and closing channels.
func TestChannel_RapidCreateCloseLoop(t *testing.T) {
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			ch := NewChan("rapid", 1)
			ch.Mod = Sender
			ch.Send(1, &message.Ping{Ping: "x"})
			ch.Close()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("rapid create/close loop hung")
	}
}

// TestChannel_SendAfterCloseReturnsErrorConsistently checks that all sends
// after close return error, never panic.
func TestChannel_SendAfterCloseReturnsErrorConsistently(t *testing.T) {
	ch := NewChan("test-post-close", 10)
	ch.Close()

	var errCount int64
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := ch.Send(1, &message.Ping{Ping: "x"})
			if err != nil {
				atomic.AddInt64(&errCount, 1)
			}
		}()
	}

	wg.Wait()

	if errCount != 100 {
		t.Fatalf("expected 100 errors, got %d — some sends didn't detect close", errCount)
	}
}

// TestChannel_SenderExitsOnChannelClose verifies that Sender exits its select
// loop when StopCh is closed, even if there's nothing in C.
func TestChannel_SenderExitsOnChannelClose(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()

	ch := NewChan("test-sender-exit", 10)
	// Don't put anything in C — Sender blocks on select

	done := make(chan error, 1)
	go func() {
		done <- ch.Sender(c1)
	}()

	time.Sleep(50 * time.Millisecond)
	ch.Close()
	c1.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Sender did not exit on empty channel + Close — deadlock")
	}
}
