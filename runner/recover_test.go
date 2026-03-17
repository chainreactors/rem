package runner

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/core"
)

// ---------------------------------------------------------------------------
// RunnerConfig.Run — panic recovery returns error, never hangs
// ---------------------------------------------------------------------------

// TestRun_PanicReturnsError verifies that a panic inside Run() is captured
// as a returned error, rather than being silently swallowed.
func TestRun_PanicReturnsError(t *testing.T) {
	// Provide a ConsoleURL so the for loop executes, but leave Options/URLs nil
	// to trigger a nil-pointer panic on r.URLs.Copy() access.
	r := &RunnerConfig{
		ConsoleURLs: []*core.URL{{}},
	}

	err := r.Run()
	if err == nil {
		t.Fatal("Run() should return an error when it panics, not nil")
	}
	if !strings.Contains(err.Error(), "panic in Run") {
		t.Fatalf("error should contain 'panic in Run', got: %v", err)
	}
}

// TestRun_PanicErrorContainsStackTrace verifies the error includes a stack
// trace so the root cause can be diagnosed.
func TestRun_PanicErrorContainsStackTrace(t *testing.T) {
	r := &RunnerConfig{
		ConsoleURLs: []*core.URL{{}},
	}

	err := r.Run()
	if err == nil {
		t.Fatal("expected error")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "goroutine") {
		t.Fatalf("error should contain stack trace with 'goroutine', got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "runner.go") {
		t.Fatalf("error should reference runner.go in stack, got: %v", errMsg)
	}
}

// TestRun_PanicDoesNotHang ensures Run() returns promptly on panic,
// not blocking forever.
func TestRun_PanicDoesNotHang(t *testing.T) {
	r := &RunnerConfig{
		ConsoleURLs: []*core.URL{{}},
	}

	done := make(chan error, 1)
	go func() {
		done <- r.Run()
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from panic")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() hung after panic — this would deadlock the system")
	}
}

// TestRun_ConcurrentPanics ensures multiple goroutines calling Run() with
// panicking configs all return errors without interfering with each other.
func TestRun_ConcurrentPanics(t *testing.T) {
	var wg sync.WaitGroup
	const n = 20

	errors := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &RunnerConfig{
				ConsoleURLs: []*core.URL{{}},
			}
			errors <- r.Run()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(errors)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent panicking Run() calls hung")
	}

	var count int
	for err := range errors {
		if err == nil {
			t.Fatal("expected error from panicking Run()")
		}
		count++
	}
	if count != n {
		t.Fatalf("expected %d errors, got %d", n, count)
	}
}

// TestRun_EmptyConsoleURLsNoHang ensures Run() with no ConsoleURLs returns
// immediately without blocking.
func TestRun_EmptyConsoleURLsNoHang(t *testing.T) {
	r := &RunnerConfig{
		Options:     &Options{},
		ConsoleURLs: nil,
	}

	done := make(chan error, 1)
	go func() {
		done <- r.Run()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error with empty ConsoleURLs: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() with empty ConsoleURLs hung — should return immediately")
	}
}

// TestRun_ConnHubPathPanicCaptured verifies that a panic in the ConnHub code
// path is also captured by the named-return recover.
func TestRun_ConnHubPathPanicCaptured(t *testing.T) {
	// Multiple ConsoleURLs triggers the ConnHub path (len > 1).
	// Nil Options causes panic inside runConnHubClient.
	r := &RunnerConfig{
		ConsoleURLs: []*core.URL{{}, {}},
	}

	done := make(chan error, 1)
	go func() {
		done <- r.Run()
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from ConnHub path panic")
		}
		if !strings.Contains(err.Error(), "panic in Run") {
			t.Fatalf("error should contain 'panic in Run', got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() hung on ConnHub path panic")
	}
}

// TestRun_ServerModePanicCaptured verifies that panics in the server-mode
// path are captured and returned as errors.
func TestRun_ServerModePanicCaptured(t *testing.T) {
	r := &RunnerConfig{
		IsServerMode: true,
		ConsoleURLs:  []*core.URL{{}},
		// nil Options/URLs will cause panic
	}

	done := make(chan error, 1)
	go func() {
		done <- r.Run()
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from server-mode panic")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() hung in server mode")
	}
}
