package runner

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

type Vars map[string]string

func (v Vars) Clone() Vars {
	if len(v) == 0 {
		return Vars{}
	}
	cp := make(Vars, len(v))
	for key, value := range v {
		cp[key] = value
	}
	return cp
}

func (v Vars) Expand(raw string) string {
	if raw == "" || len(v) == 0 {
		return raw
	}
	replacements := make([]string, 0, len(v)*2)
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		replacements = append(replacements, "{{"+key+"}}", v[key])
	}
	return strings.NewReplacer(replacements...).Replace(raw)
}

type CapabilityCheck func(*testing.T) string

type ReadyKind string

const (
	ReadyTCPListen  ReadyKind = "tcp_listen"
	ReadySocksHTTP  ReadyKind = "socks_http"
	ReadyHTTPProxy  ReadyKind = "http_proxy"
	ReadyUnixSocket ReadyKind = "unix_socket"
	ReadyDelayOnly  ReadyKind = "delay_only"
	ReadyReportFile ReadyKind = "report_file"
)

type ReadySpec struct {
	Kind     ReadyKind
	Target   string
	Timeout  time.Duration
	User     string
	Password string
}

type ProcessSpec struct {
	Name        string
	Mode        string
	Command     string
	Env         Vars
	Wait        []ReadySpec
	TestTimeout time.Duration
}

type FixtureSpec struct {
	HelperTest string
	Vars       Vars
	Env        Vars
	Processes  []ProcessSpec
}

type Scenario struct {
	Name string
	Run  func(t *testing.T, fx *Fixture)
}

type CaseSpec struct {
	Name      string
	Check     CapabilityCheck
	Build     func(t *testing.T) FixtureSpec
	Scenarios []Scenario
}

type Fixture struct {
	t            *testing.T
	helperTest   string
	vars         Vars
	env          Vars
	processes    map[string]*processHandle
	processSpecs map[string]ProcessSpec
}

type processHandle struct {
	spec ProcessSpec
	cmd  *exec.Cmd
}

func RunTunnelSuite(t *testing.T, cases []CaseSpec) {
	runSuite(t, cases)
}

func RunServeSuite(t *testing.T, cases []CaseSpec) {
	runSuite(t, cases)
}

func RunConsoleSuite(t *testing.T, cases []CaseSpec) {
	runSuite(t, cases)
}

func runSuite(t *testing.T, cases []CaseSpec) {
	for _, tc := range cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Check != nil {
				if reason := tc.Check(t); reason != "" {
					t.Skip(reason)
				}
			}
			for _, scenario := range tc.Scenarios {
				scenario := scenario
				t.Run(scenario.Name, func(t *testing.T) {
					spec := tc.Build(t)
					fx := StartFixture(t, spec)
					scenario.Run(t, fx)
				})
			}
		})
	}
}

func StartFixture(t *testing.T, spec FixtureSpec) *Fixture {
	t.Helper()

	helperTest := spec.HelperTest
	if helperTest == "" {
		helperTest = "^TestE2EHelperProcess$"
	}

	fx := &Fixture{
		t:            t,
		helperTest:   helperTest,
		vars:         spec.Vars.Clone(),
		env:          spec.Env.Clone(),
		processes:    make(map[string]*processHandle, len(spec.Processes)),
		processSpecs: make(map[string]ProcessSpec, len(spec.Processes)),
	}
	for _, procSpec := range spec.Processes {
		fx.processSpecs[procSpec.Name] = procSpec
	}

	t.Cleanup(func() {
		for i := len(spec.Processes) - 1; i >= 0; i-- {
			fx.stopProcess(spec.Processes[i].Name)
		}
	})

	for _, procSpec := range spec.Processes {
		fx.startProcess(procSpec)
	}

	return fx
}

func (f *Fixture) Vars() Vars {
	return f.vars.Clone()
}

func (f *Fixture) Var(name string) string {
	return f.vars[name]
}

func (f *Fixture) Endpoint(name string) string {
	return f.vars[name]
}

func (f *Fixture) SetVar(name, value string) {
	f.vars[name] = value
}

func (f *Fixture) Process(name string) *exec.Cmd {
	handle := f.processes[name]
	if handle == nil {
		return nil
	}
	return handle.cmd
}

func (f *Fixture) Kill(name string) {
	f.t.Helper()
	f.stopProcess(name)
}

func (f *Fixture) Restart(name string) {
	f.t.Helper()
	spec, ok := f.processSpecs[name]
	if !ok {
		f.t.Fatalf("restart unknown process %q", name)
	}
	f.stopProcess(name)
	f.startProcess(spec)
}

func (f *Fixture) Wait(spec ReadySpec) {
	f.t.Helper()

	target := f.vars.Expand(spec.Target)
	switch spec.Kind {
	case ReadyTCPListen:
		WaitForTCP(f.t, target, spec.Timeout)
	case ReadySocksHTTP:
		WaitForSOCKSHTTP(f.t, target, spec.User, spec.Password, spec.Timeout)
	case ReadyHTTPProxy:
		WaitForHTTPProxyHTTPS(f.t, target, spec.User, spec.Password, spec.Timeout)
	case ReadyUnixSocket, ReadyReportFile:
		WaitForFile(f.t, target, spec.Timeout)
	case ReadyDelayOnly:
		time.Sleep(spec.Timeout)
	default:
		f.t.Fatalf("unknown ready kind %q", spec.Kind)
	}
}

func (f *Fixture) startProcess(spec ProcessSpec) {
	f.t.Helper()

	if spec.Name == "" {
		f.t.Fatal("process name must not be empty")
	}
	if spec.Mode == "" {
		f.t.Fatalf("process %q mode must not be empty", spec.Name)
	}

	args := []string{"-test.run=" + f.helperTest, "-test.v"}
	if spec.TestTimeout > 0 {
		args = append(args, "-test.timeout="+spec.TestTimeout.String())
	}

	cmd := exec.Command(os.Args[0], args...)
	env := append(os.Environ(), "REM_E2E_MODE="+f.vars.Expand(spec.Mode))
	expandedCommand := f.vars.Expand(spec.Command)
	if expandedCommand != "" {
		env = append(env, "REM_HELPER_CMD="+expandedCommand)
	}
	for key, value := range f.env {
		env = append(env, key+"="+f.vars.Expand(value))
	}
	for key, value := range spec.Env {
		env = append(env, key+"="+f.vars.Expand(value))
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		f.t.Fatalf("start process %q: %v", spec.Name, err)
	}
	f.processes[spec.Name] = &processHandle{spec: spec, cmd: cmd}

	for _, waitSpec := range spec.Wait {
		f.Wait(waitSpec)
	}
}

func (f *Fixture) stopProcess(name string) {
	handle := f.processes[name]
	if handle == nil || handle.cmd == nil || handle.cmd.Process == nil {
		return
	}
	if handle.cmd.ProcessState == nil {
		_ = handle.cmd.Process.Kill()
		_ = handle.cmd.Wait()
	}
	delete(f.processes, name)
}

func WaitForTCP(t testing.TB, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("tcp %s not ready within %v: %v", addr, timeout, lastErr)
}

func WaitForFile(t testing.TB, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("file %s not ready within %v", path, timeout)
}

func NewSOCKS5Client(t testing.TB, socksAddr, user, password string, timeout time.Duration) *http.Client {
	t.Helper()
	var auth *proxy.Auth
	if user != "" || password != "" {
		auth = &proxy.Auth{User: user, Password: password}
	}
	dialer, err := proxy.SOCKS5("tcp", socksAddr, auth, proxy.Direct)
	if err != nil {
		t.Fatalf("new socks5 client for %s: %v", socksAddr, err)
	}
	return &http.Client{
		Transport: &http.Transport{Dial: dialer.Dial},
		Timeout:   timeout,
	}
}

func WaitForSOCKSHTTP(t testing.TB, socksAddr, user, password string, timeout time.Duration) {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()

	client := NewSOCKS5Client(t, socksAddr, user, password, 5*time.Second)
	_, body := HTTPGetEventually(t, client, ts.URL, timeout)
	if string(body) != "ok" {
		t.Fatalf("unexpected socks http probe body from %s: %q", socksAddr, body)
	}
	client.CloseIdleConnections()
}

func NewHTTPProxyClient(t testing.TB, proxyAddr, user, password string, tlsConfig *tls.Config, timeout time.Duration) *http.Client {
	t.Helper()
	proxyURL := &url.URL{
		Scheme: "http",
		Host:   proxyAddr,
	}
	if user != "" || password != "" {
		proxyURL.User = url.UserPassword(user, password)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: tlsConfig,
		},
		Timeout: timeout,
	}
}

func WaitForHTTPProxyHTTPS(t testing.TB, proxyAddr, user, password string, timeout time.Duration) {
	t.Helper()

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer ts.Close()

	baseTransport, ok := ts.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected tls transport type %T", ts.Client().Transport)
	}
	client := NewHTTPProxyClient(t, proxyAddr, user, password, baseTransport.TLSClientConfig, 5*time.Second)
	_, body := HTTPGetEventually(t, client, ts.URL, timeout)
	if string(body) != "ok" {
		t.Fatalf("unexpected http proxy probe body from %s: %q", proxyAddr, body)
	}
	client.CloseIdleConnections()
}

func HTTPGetEventually(t testing.TB, client *http.Client, targetURL string, timeout time.Duration) (*http.Response, []byte) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		resp, err := client.Get(targetURL)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil {
				return resp, body
			}
			lastErr = readErr
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("http get %s did not succeed within %v: %v", targetURL, timeout, lastErr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func JoinTempPath(t testing.TB, dir, name string) string {
	t.Helper()
	return filepath.Join(dir, name)
}
