//go:build !tinygo

package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chainreactors/rem/agent"
	"github.com/kballard/go-shellquote"
	"golang.org/x/net/proxy"
)

type connHubPerfReport struct {
	LoadBalance string                  `json:"load_balance"`
	Requests    int                     `json:"requests"`
	DurationMS  int64                   `json:"duration_ms"`
	Stats       []agent.ConnHubConnStat `json:"stats"`
}

func statsByLabel(stats []agent.ConnHubConnStat) map[string]uint64 {
	selectedByLabel := map[string]uint64{}
	for _, stat := range stats {
		selectedByLabel[stat.Label] += stat.Selected
	}
	return selectedByLabel
}

func TestHelperRunnerProcess(t *testing.T) {
	if os.Getenv("REM_HELPER_RUNNER") == "" {
		t.Skip("subprocess helper")
	}
	cmdline := os.Getenv("REM_HELPER_CMD")
	if cmdline == "" {
		t.Fatalf("REM_HELPER_CMD not set")
	}
	args, err := shellquote.Split(cmdline)
	if err != nil {
		t.Fatalf("shellquote split: %v", err)
	}
	var opt Options
	if err := opt.ParseArgs(args); err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	r, err := opt.Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := r.Run(); err != nil {
		t.Fatalf("Runner.Run: %v", err)
	}
}

// TestHelperConnHubClient is a subprocess helper that:
//  1. Dials tcp+udp+ws into one ConnHub client
//  2. Sends load through local SOCKS5
//  3. Writes per-conn selected counters to a JSON report file
func TestHelperConnHubClient(t *testing.T) {
	if os.Getenv("REM_CONNHUB_CLIENT") == "" {
		t.Skip("subprocess helper")
	}

	tcpAddr := os.Getenv("REM_TCP_ADDR")
	udpAddr := os.Getenv("REM_UDP_ADDR")
	wsAddr := os.Getenv("REM_WS_ADDR")
	socksPort := os.Getenv("REM_SOCKS_PORT")
	alias := os.Getenv("REM_ALIAS")
	targetURL := os.Getenv("REM_TARGET_URL")
	reportPath := os.Getenv("REM_REPORT_PATH")
	loadBalance := os.Getenv("REM_LB")
	if loadBalance == "" {
		loadBalance = "round-robin"
	}

	reqCount := 60
	if raw := os.Getenv("REM_REQ_COUNT"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			reqCount = parsed
		}
	}

	var opt Options
	err := opt.ParseArgs([]string{
		"-c", fmt.Sprintf("tcp://%s/?wrapper=raw", tcpAddr),
		"-c", fmt.Sprintf("udp://%s/?wrapper=raw", udpAddr),
		"-c", fmt.Sprintf("ws://%s/hub?wrapper=raw", wsAddr),
		"-m", "proxy",
		"-l", fmt.Sprintf("socks5://127.0.0.1:%s", socksPort),
		"-a", alias,
		"--lb", loadBalance,
	})
	if err != nil {
		t.Fatalf("ParseArgs: %v", err)
	}
	r, err := opt.Prepare()
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	fullURLs, upURLs, downURLs, err := splitConsoleChannels(r.ConsoleURLs)
	if err != nil {
		t.Fatalf("splitConsoleChannels: %v", err)
	}
	if len(fullURLs) == 0 {
		t.Fatalf("helper expects full channel")
	}

	console, err := NewConsole(r, r.NewURLs(fullURLs[0]))
	if err != nil {
		t.Fatalf("NewConsole: %v", err)
	}
	a, err := console.Dial(fullURLs[0])
	if err != nil {
		t.Fatalf("Dial channel: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- a.Handler()
	}()
	defer func() {
		a.Close(nil)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for !a.Init && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !a.Init {
		t.Fatalf("agent not initialized")
	}

	r.attachChannelConns(a, fullURLs, upURLs, downURLs, 0)

	socksAddr := fmt.Sprintf("127.0.0.1:%s", socksPort)
	waitForTCP(t, socksAddr, 15*time.Second)
	time.Sleep(300 * time.Millisecond)

	dialer, err := proxy.SOCKS5("tcp", socksAddr, socksAuth(), proxy.Direct)
	if err != nil {
		t.Fatalf("SOCKS5 dialer: %v", err)
	}
	client := &http.Client{
		Transport: &http.Transport{
			Dial:              dialer.Dial,
			DisableKeepAlives: true,
		},
		Timeout: 15 * time.Second,
	}

	start := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan error, reqCount)
	sem := make(chan struct{}, 10)
	for i := 0; i < reqCount; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()
			for attempt := 1; attempt <= 3; attempt++ {
				resp, err := client.Get(targetURL)
				if err != nil {
					if attempt == 3 {
						errCh <- fmt.Errorf("request %d failed: %w", index, err)
						return
					}
					time.Sleep(100 * time.Millisecond)
					continue
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					if attempt == 3 {
						errCh <- fmt.Errorf("request %d read failed: %w", index, err)
						return
					}
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if string(body) != "hub-ok" {
					errCh <- fmt.Errorf("request %d unexpected body: %q", index, body)
				}
				return
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	duration := time.Since(start)

	report := connHubPerfReport{
		LoadBalance: loadBalance,
		Requests:    reqCount,
		DurationMS:  duration.Milliseconds(),
		Stats:       a.ConnHubStats(),
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(reportPath, raw, 0644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}

func TestConnHub_TCPUDPWS_Performance(t *testing.T) {
	tcpPort := freePort(t)
	udpPort := freeUDPPort(t)
	wsPort := freePort(t)
	alias := fmt.Sprintf("hub%d", tcpPort)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hub-ok"))
	}))
	defer ts.Close()

	serverProc := exec.Command(os.Args[0], "-test.run=^TestHelperRunnerProcess$", "-test.v")
	serverProc.Env = append(os.Environ(),
		"REM_HELPER_RUNNER=1",
		fmt.Sprintf("REM_HELPER_CMD=-s tcp://0.0.0.0:%d/?wrapper=raw -s udp://0.0.0.0:%d/?wrapper=raw -s ws://0.0.0.0:%d/hub?wrapper=raw -i 127.0.0.1 --no-sub",
			tcpPort, udpPort, wsPort),
	)
	serverProc.Stdout = os.Stdout
	serverProc.Stderr = os.Stderr
	if err := serverProc.Start(); err != nil {
		t.Fatalf("start server helper: %v", err)
	}
	t.Cleanup(func() {
		serverProc.Process.Kill()
		serverProc.Wait()
	})

	waitForTCP(t, fmt.Sprintf("127.0.0.1:%d", tcpPort), 10*time.Second)
	time.Sleep(1500 * time.Millisecond)

	tests := []struct {
		name string
		lb   string
	}{
		{name: "RoundRobin", lb: "round-robin"},
		{name: "Random", lb: "random"},
		{name: "Fallback", lb: "fallback"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			socksPort := freePort(t)
			reportPath := filepath.Join(t.TempDir(), "connhub_report.json")

			clientProc := exec.Command(os.Args[0], "-test.run=^TestHelperConnHubClient$", "-test.v")
			clientProc.Env = append(os.Environ(),
				"REM_CONNHUB_CLIENT=1",
				fmt.Sprintf("REM_TCP_ADDR=127.0.0.1:%d", tcpPort),
				fmt.Sprintf("REM_UDP_ADDR=127.0.0.1:%d", udpPort),
				fmt.Sprintf("REM_WS_ADDR=127.0.0.1:%d", wsPort),
				fmt.Sprintf("REM_SOCKS_PORT=%d", socksPort),
				fmt.Sprintf("REM_ALIAS=%s", alias),
				fmt.Sprintf("REM_TARGET_URL=%s", ts.URL),
				fmt.Sprintf("REM_REPORT_PATH=%s", reportPath),
				fmt.Sprintf("REM_LB=%s", tc.lb),
				"REM_REQ_COUNT=45",
			)
			clientProc.Stdout = os.Stdout
			clientProc.Stderr = os.Stderr
			if err := clientProc.Start(); err != nil {
				t.Fatalf("start client helper: %v", err)
			}

			done := make(chan error, 1)
			go func() {
				done <- clientProc.Wait()
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("client helper failed: %v", err)
				}
			case <-time.After(120 * time.Second):
				_ = clientProc.Process.Kill()
				t.Fatalf("client helper timeout")
			}

			reportRaw, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatalf("read report: %v", err)
			}

			var report connHubPerfReport
			if err := json.Unmarshal(reportRaw, &report); err != nil {
				t.Fatalf("unmarshal report: %v", err)
			}
			if report.Requests == 0 || len(report.Stats) == 0 {
				t.Fatalf("empty report: %+v", report)
			}

			selectedByLabel := statsByLabel(report.Stats)
			if selectedByLabel["tcp"] == 0 {
				t.Fatalf("tcp channel selected count is zero: %+v", selectedByLabel)
			}

			switch tc.lb {
			case "round-robin", "random":
				if selectedByLabel["udp-1"] == 0 || selectedByLabel["ws-2"] == 0 {
					t.Fatalf("%s requires all channels to be used: %+v", tc.lb, selectedByLabel)
				}
			case "fallback":
				if selectedByLabel["tcp"] < selectedByLabel["udp-1"] || selectedByLabel["tcp"] < selectedByLabel["ws-2"] {
					t.Fatalf("fallback should prioritize preferred tcp: %+v", selectedByLabel)
				}
			}

			seconds := float64(report.DurationMS) / 1000.0
			if seconds <= 0 {
				seconds = 0.001
			}
			rps := float64(report.Requests) / seconds
			t.Logf("ConnHub perf: lb=%s req=%d duration=%dms rps=%.1f stats=%+v",
				report.LoadBalance, report.Requests, report.DurationMS, rps, selectedByLabel)
		})
	}
}
