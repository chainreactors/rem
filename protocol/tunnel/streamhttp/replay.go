//go:build !tinygo

package streamhttp

import (
	"io"
	"sync"
)

// replayBuffer is a ring buffer of events for reconnection replay.
// Events store raw binary data (no encoding).

type replayEvent struct {
	id   uint64
	data []byte
}

type replayBuffer struct {
	mu        sync.Mutex
	events    []replayEvent
	head      int    // index of oldest event
	count     int    // current number of events
	cap       int    // ring capacity
	nextID    uint64 // next sequence ID to assign
	closed    bool
	notify    chan struct{} // signaled on Append / Close
	closeOnce sync.Once
}

func newReplayBuffer(capacity int) *replayBuffer {
	return &replayBuffer{
		events: make([]replayEvent, capacity),
		cap:    capacity,
		nextID: 1,
		notify: make(chan struct{}, 1),
	}
}

// Append adds data to the ring buffer and returns the assigned sequence ID.
func (rb *replayBuffer) Append(data []byte) uint64 {
	rb.mu.Lock()
	if rb.closed {
		rb.mu.Unlock()
		return 0
	}
	id := rb.nextID
	rb.nextID++

	// Copy data to avoid retaining caller's slice.
	stored := make([]byte, len(data))
	copy(stored, data)

	idx := (rb.head + rb.count) % rb.cap
	rb.events[idx] = replayEvent{id: id, data: stored}

	if rb.count < rb.cap {
		rb.count++
	} else {
		rb.head = (rb.head + 1) % rb.cap
	}
	rb.mu.Unlock()

	// Non-blocking signal.
	select {
	case rb.notify <- struct{}{}:
	default:
	}
	return id
}

// Collect returns events with id > afterID without blocking.
// Returns remaining events even when closed; error only when no events left.
func (rb *replayBuffer) Collect(afterID uint64) ([]replayEvent, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	var out []replayEvent
	for i := 0; i < rb.count; i++ {
		idx := (rb.head + i) % rb.cap
		ev := rb.events[idx]
		if ev.id > afterID {
			out = append(out, ev)
		}
	}
	if len(out) == 0 && rb.closed {
		return nil, io.ErrClosedPipe
	}
	return out, nil
}

// Notify returns the notification channel for use in select.
func (rb *replayBuffer) Notify() <-chan struct{} {
	return rb.notify
}

// Close marks buffer closed and signals waiters.
func (rb *replayBuffer) Close() {
	rb.closeOnce.Do(func() {
		rb.mu.Lock()
		rb.closed = true
		rb.mu.Unlock()
		close(rb.notify)
	})
}
