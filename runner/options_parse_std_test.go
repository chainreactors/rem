//go:build !tinygo

package runner

import (
	"errors"
	"reflect"
	"testing"

	"github.com/kballard/go-shellquote"
)

func TestOptionsParseArgsShortLongAndDefaults(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-s", "tcp://0.0.0.0:34996",
		"--server", "ws://127.0.0.1:8080",
		"-c", "tcp://client-a:34996",
		"--client", "tcp://client-b:34996",
		"-l", "port://:8080",
		"--remote", "port://:9090",
		"-a", "demo",
		"--destination", "agent-1",
		"-x", "socks5://127.0.0.1:1080",
		"--proxy", "http://127.0.0.1:8081",
		"-f", "socks5://127.0.0.1:2080",
		"--forward", "http://127.0.0.1:2081",
		"-m", "proxy",
		"-n",
		"-k", "test-key",
		"--version",
		"--debug",
		"--detail",
		"-q",
		"--dump",
		"--list",
		"-i", "1.2.3.4",
		"--sub", "http://0.0.0.0:10080",
		"--lb", "round-robin",
		"--no-sub",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	if !reflect.DeepEqual(opt.ServerAddr, []string{"tcp://0.0.0.0:34996", "ws://127.0.0.1:8080"}) {
		t.Fatalf("unexpected server addresses: %#v", opt.ServerAddr)
	}
	if !reflect.DeepEqual(opt.ClientAddr, []string{"tcp://client-a:34996", "tcp://client-b:34996"}) {
		t.Fatalf("unexpected client addresses: %#v", opt.ClientAddr)
	}
	if !reflect.DeepEqual(opt.ProxyAddr, []string{"socks5://127.0.0.1:1080", "http://127.0.0.1:8081"}) {
		t.Fatalf("unexpected proxy addresses: %#v", opt.ProxyAddr)
	}
	if !reflect.DeepEqual(opt.ForwardAddr, []string{"socks5://127.0.0.1:2080", "http://127.0.0.1:2081"}) {
		t.Fatalf("unexpected forward addresses: %#v", opt.ForwardAddr)
	}

	if !reflect.DeepEqual(opt.LocalAddr, []string{"port://:8080"}) || !reflect.DeepEqual(opt.RemoteAddr, []string{"port://:9090"}) {
		t.Fatalf("unexpected local/remote: %#v %#v", opt.LocalAddr, opt.RemoteAddr)
	}
	if opt.Alias != "demo" || opt.Redirect != "agent-1" || opt.Mod != "proxy" {
		t.Fatalf("unexpected alias/redirect/mod: %s %s %s", opt.Alias, opt.Redirect, opt.Mod)
	}
	if opt.Key != "test-key" || opt.IP != "1.2.3.4" || opt.Subscribe != "http://0.0.0.0:10080" {
		t.Fatalf("unexpected key/ip/sub: %s %s %s", opt.Key, opt.IP, opt.Subscribe)
	}
	if opt.LoadBalance != "round-robin" {
		t.Fatalf("unexpected load balance: %s", opt.LoadBalance)
	}
	if opt.Retry != 10 || opt.RetryInterval != 10 {
		t.Fatalf("unexpected retry options: %d %d", opt.Retry, opt.RetryInterval)
	}

	if !opt.ConnectOnly || !opt.Version || !opt.Debug || !opt.Detail || !opt.Quiet || !opt.Dump || !opt.List || !opt.NoSubscribe {
		t.Fatalf("unexpected bool options: %+v", opt)
	}
}

func TestOptionsParseArgsDefaults(t *testing.T) {
	var opt Options
	err := opt.ParseArgs(nil)
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	if opt.Retry != 10 {
		t.Fatalf("unexpected default retry: %d", opt.Retry)
	}
	if opt.RetryInterval != 10 {
		t.Fatalf("unexpected default retry interval: %d", opt.RetryInterval)
	}
	if opt.Subscribe != "http://0.0.0.0:29999" {
		t.Fatalf("unexpected default subscribe: %s", opt.Subscribe)
	}
	if opt.LoadBalance != "fallback" {
		t.Fatalf("unexpected default load balance: %s", opt.LoadBalance)
	}
}

func TestOptionsParseArgsHelp(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{"--help"})
	if !errors.Is(err, ErrHelpRequested) {
		t.Fatalf("expected help error, got: %v", err)
	}
}

func TestOptionsParseArgsPositionalError(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{"-m", "proxy", "unexpected"})
	if err == nil {
		t.Fatalf("expected positional argument error")
	}
}

func TestRemDialCommandLineCompatibility(t *testing.T) {
	testCases := []struct {
		name    string
		cmdline string
	}{
		{
			name:    "short flags",
			cmdline: "-c tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw -n -a test --debug",
		},
		{
			name:    "long flags with equals",
			cmdline: "--client=tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw --connect-only --alias=test --detail",
		},
		{
			name:    "mixed flags and quoted url",
			cmdline: "-c 'tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw&compress=1&retry=3&retry-interval=2' -m proxy -l 'memory+socks5://:@pipe-name' -r 'raw://127.0.0.1:9001'",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			args, err := shellquote.Split(testCase.cmdline)
			if err != nil {
				t.Fatalf("shellquote split failed: %v", err)
			}

			var opt Options
			err = opt.ParseArgs(args)
			if err != nil {
				t.Fatalf("ParseArgs failed: %v", err)
			}

			prepared, err := opt.Prepare()
			if err != nil {
				t.Fatalf("Prepare failed: %v", err)
			}

			if len(prepared.ConsoleURLs) == 0 {
				t.Fatalf("expected at least one console URL")
			}
		})
	}
}

func TestRemDialCommandLineMissingValue(t *testing.T) {
	args, err := shellquote.Split("-c")
	if err != nil {
		t.Fatalf("shellquote split failed: %v", err)
	}

	var opt Options
	err = opt.ParseArgs(args)
	if err == nil {
		t.Fatalf("expected parse error for missing -c value")
	}
}

func TestOptionsParseArgsRetryFlagsRemoved(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{"--retry", "3"})
	if err == nil {
		t.Fatalf("expected parse error for removed --retry flag")
	}
}

func TestOptionsRetryFromURLCommonParams(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-c", "tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw&retry=3&retry-interval=2&lb=random",
		"-m", "proxy",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	prepared, err := opt.Prepare()
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if prepared.Retry != 3 || prepared.RetryInterval != 2 {
		t.Fatalf("unexpected retry values from url: %d %d", prepared.Retry, prepared.RetryInterval)
	}
	if prepared.LoadBalance != "random" {
		t.Fatalf("unexpected lb value from url: %s", prepared.LoadBalance)
	}
	if prepared.ConsoleURLs[0].GetQuery("retry") != "" || prepared.ConsoleURLs[0].GetQuery("retry-interval") != "" {
		t.Fatalf("common params should be consumed from console url query")
	}
}

func TestOptionsRetryFromLocalRemoteURLCommonParams(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-c", "tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw",
		"-l", "memory+socks5://:@pipe-name?retry=4",
		"-r", "raw://127.0.0.1:9001?retry-interval=6",
		"-m", "proxy",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	prepared, err := opt.Prepare()
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if prepared.Retry != 4 || prepared.RetryInterval != 6 {
		t.Fatalf("unexpected retry values from local/remote url: %d %d", prepared.Retry, prepared.RetryInterval)
	}
}

func TestOptionsRetryFromURLInvalid(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-c", "tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw&retry=abc",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	_, err = opt.Prepare()
	if err == nil {
		t.Fatalf("expected Prepare error for invalid retry url value")
	}
}

func TestOptionsParseArgsCombinedShortFlags(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{"-qn", "-mproxy", "-ctcp://127.0.0.1:34996"})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	if !opt.Quiet || !opt.ConnectOnly {
		t.Fatalf("expected combined bool flags to be true: quiet=%v connectOnly=%v", opt.Quiet, opt.ConnectOnly)
	}
	if opt.Mod != "proxy" {
		t.Fatalf("expected mod from attached short value, got: %s", opt.Mod)
	}
	if len(opt.ClientAddr) != 1 || opt.ClientAddr[0] != "tcp://127.0.0.1:34996" {
		t.Fatalf("expected client from attached short value, got: %#v", opt.ClientAddr)
	}
}

func TestOptionsParseArgsClientServerListFormsAndOrder(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-ctcp://client-1:1001",
		"--client=tcp://client-2:1002",
		"-c", "tcp://client-3:1003",
		"-stcp://0.0.0.0:2001",
		"--server=ws://0.0.0.0:2002",
		"-s", "tcp://0.0.0.0:2003",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	expectedClients := []string{"tcp://client-1:1001", "tcp://client-2:1002", "tcp://client-3:1003"}
	if !reflect.DeepEqual(opt.ClientAddr, expectedClients) {
		t.Fatalf("unexpected client addresses: %#v", opt.ClientAddr)
	}

	expectedServers := []string{"tcp://0.0.0.0:2001", "ws://0.0.0.0:2002", "tcp://0.0.0.0:2003"}
	if !reflect.DeepEqual(opt.ServerAddr, expectedServers) {
		t.Fatalf("unexpected server addresses: %#v", opt.ServerAddr)
	}
}

func TestOptionsParseArgsShortAndLongEqualsSyntax(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-m=proxy",
		"-k=test-key",
		"-i=1.2.3.4",
		"--alias=demo",
		"--destination=agent-1",
		"--sub=http://0.0.0.0:10080",
		"--proxy=socks5://127.0.0.1:1080",
		"--proxy=http://127.0.0.1:8081",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	if opt.Mod != "proxy" || opt.Key != "test-key" || opt.IP != "1.2.3.4" {
		t.Fatalf("unexpected short equals parse result: mod=%s key=%s ip=%s", opt.Mod, opt.Key, opt.IP)
	}
	if opt.Alias != "demo" || opt.Redirect != "agent-1" || opt.Subscribe != "http://0.0.0.0:10080" {
		t.Fatalf("unexpected long equals parse result: alias=%s redirect=%s sub=%s", opt.Alias, opt.Redirect, opt.Subscribe)
	}
	if !reflect.DeepEqual(opt.ProxyAddr, []string{"socks5://127.0.0.1:1080", "http://127.0.0.1:8081"}) {
		t.Fatalf("unexpected proxy list from equals syntax: %#v", opt.ProxyAddr)
	}
}

func TestOptionsRetryMaxIntervalDefault(t *testing.T) {
	var opt Options
	err := opt.ParseArgs(nil)
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if opt.RetryMaxInterval != 300 {
		t.Fatalf("unexpected default retry max interval: %d", opt.RetryMaxInterval)
	}
}

func TestOptionsRetryMaxIntervalFromURL(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-c", "tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw&retry-max-interval=600",
		"-m", "proxy",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	prepared, err := opt.Prepare()
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if prepared.RetryMaxInterval != 600 {
		t.Fatalf("unexpected retry max interval from url: %d", prepared.RetryMaxInterval)
	}
	if prepared.ConsoleURLs[0].GetQuery("retry-max-interval") != "" {
		t.Fatalf("retry-max-interval should be consumed from console url query")
	}
}

func TestOptionsRetryMaxIntervalFromURLInvalid(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-c", "tcp://nonenonenonenone:@127.0.0.1:34996/?wrapper=raw&retry-max-interval=abc",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	_, err = opt.Prepare()
	if err == nil {
		t.Fatalf("expected Prepare error for invalid retry-max-interval url value")
	}
}

func TestOptionsPrepareRejectsServerAndClientTogether(t *testing.T) {
	var opt Options
	err := opt.ParseArgs([]string{
		"-s", "tcp://0.0.0.0:34996",
		"-c", "tcp://127.0.0.1:34996",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}

	_, err = opt.Prepare()
	if err == nil {
		t.Fatalf("expected Prepare to reject both server and client settings")
	}
}
