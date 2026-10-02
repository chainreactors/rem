package agent

import (
	"testing"

	"github.com/chainreactors/rem/protocol/core"
)

func TestYamuxConfigUsesDefaultWindowForTCP(t *testing.T) {
	consoleURL, err := core.NewConsoleURL("tcp://127.0.0.1:0/?wrapper=raw")
	if err != nil {
		t.Fatalf("NewConsoleURL: %v", err)
	}
	a := &Agent{Config: &Config{URLs: &core.URLs{ConsoleURL: consoleURL}}}

	cfg := a.yamuxConfig()
	if cfg.MaxStreamWindowSize != 256*1024 {
		t.Fatalf("tcp window = %d, want default 256KiB", cfg.MaxStreamWindowSize)
	}
}

func TestYamuxConfigUsesLargeWindowForSimplex(t *testing.T) {
	consoleURL, err := core.NewConsoleURL("simplex+oss://bucket.example.com/rem/?wrapper=raw")
	if err != nil {
		t.Fatalf("NewConsoleURL: %v", err)
	}
	a := &Agent{Config: &Config{URLs: &core.URLs{ConsoleURL: consoleURL}}}

	cfg := a.yamuxConfig()
	if cfg.MaxStreamWindowSize != simplexYamuxStreamWindow {
		t.Fatalf("simplex window = %d, want %d", cfg.MaxStreamWindowSize, simplexYamuxStreamWindow)
	}
}
