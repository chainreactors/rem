// Copyright IBM Corp. 2014, 2025
// SPDX-License-Identifier: MPL-2.0

package yamux

// FlowControlConfig configures the two-level adaptive window mechanism.
// When enabled, the stream window alternates between LargeWindow (no flow
// control) and SmallWindow (active flow control) based on receive buffer
// pressure.
type FlowControlConfig struct {
	// LargeWindow is the window size when flow control is inactive.
	LargeWindow uint32
	// SmallWindow is the window size when flow control is active.
	SmallWindow uint32
	// Threshold is the buffer occupancy ratio (0~1.0) at which the
	// window downgrades from large to small.
	Threshold float64
	// WaitN is the multiplier applied to ConnectionWriteTimeout.
	// If the sender blocks for longer than ConnectionWriteTimeout*WaitN
	// without receiving a WindowUpdate, it auto-resets sendWindow to
	// LargeWindow.
	WaitN int
}
