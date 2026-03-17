package runner

import (
	"fmt"
	"net/url"
	"os"
	"runtime/debug"
	"sync"

	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/tunnel"
	"github.com/chainreactors/rem/x/utils"
	"github.com/kballard/go-shellquote"
)

func NewConsoleWithCMD(cmdline string) (*Console, error) {
	var option Options
	args, err := shellquote.Split(cmdline)
	if err != nil {
		return nil, err
	}
	err = option.ParseArgs(args)
	if err != nil {
		return nil, err
	}
	runner, err := option.Prepare()
	if err != nil {
		return nil, err
	}

	runner.URLs.ConsoleURL = runner.ConsoleURLs[0]
	console, err := NewConsole(runner, runner.URLs)
	if err != nil {
		return nil, err
	}
	return console, nil
}

func (c *Console) newAgent(urls *core.URLs, r *RunnerConfig) (*agent.Agent, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		utils.Log.Warnf("[agent] %s", err.Error())
	}

	a, err := agent.NewAgent(&agent.Config{
		Type: core.CLIENT,
		Mod:  r.Mod,
		URLs: &core.URLs{
			ConsoleURL: urls.ConsoleURL.Copy(),
		},
		ExternalIP:  c.Config.IP,
		Alias:       c.Config.Alias,
		AuthKey:     []byte(r.Key),
		LoadBalance: r.LoadBalance,
		Proxies:     r.Proxies,
		Redirect:    r.Redirect,
		Interfaces:  resolveAgentInterfaces(),
		Params: map[string]string{
			"ip":   r.IP,
			"name": buildAgentName(hostname),
			"lb":   r.LoadBalance,
		},
		Username: resolveAgentUsername(),
		Hostname: hostname,
	})

	a.Recover = func() error {
		return c.Run()
	}

	return a, nil
}

func buildAdvancedTunnelOptions(console *Console, runner *RunnerConfig) ([]tunnel.TunnelOption, error) {
	var tunOpts []tunnel.TunnelOption
	if len(runner.ForwardAddr) > 0 {
		tunOpts = append(tunOpts, tunnel.WithProxyClient(runner.ForwardAddr))
	}
	if console.ConsoleURL.GetQuery("compress") != "" {
		tunOpts = append(tunOpts, tunnel.WithCompression())
	}
	return appendAdvanceTunnelOptions(console, runner, tunOpts)
}

type ExtraServe struct {
	LocalURL  *core.URL
	RemoteURL *core.URL
}

type RunnerConfig struct {
	*Options
	URLs         *core.URLs
	ConsoleURLs  []*core.URL
	Proxies      []*url.URL
	ExtraServes  []ExtraServe
	IsServerMode bool // true if using -s/--server, false if using -c/--client
}

func (r *RunnerConfig) NewURLs(con *core.URL) *core.URLs {
	urls := r.URLs.Copy()
	urls.ConsoleURL = con
	return urls
}

func (r *RunnerConfig) Run() (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic in Run: %v\n%s", r, debug.Stack())
		}
	}()

	// Client ConnHub mode: multi-channel or directional channels.
	if !r.IsServerMode && (hasDirectionalChannels(r.ConsoleURLs) || len(r.ConsoleURLs) > 1) {
		return r.runConnHubClient()
	}

	var wg sync.WaitGroup
	for _, cURL := range r.ConsoleURLs {
		wg.Add(1)
		console, err := NewConsole(r, r.NewURLs(cURL))
		if err != nil {
			utils.Log.Error("[console] " + err.Error())
			return err
		}

		if r.Mod == "bind" {
			go func(console *Console) {
				err := console.Bind()
				if err != nil {
					utils.Log.Error(err.Error())
				}
				wg.Done()
			}(console)
		} else {
			go func(console *Console) {
				err := console.Run()
				if err != nil {
					utils.Log.Error(err)
				}
				wg.Done()
			}(console)
		}

	}

	wg.Wait()
	return nil
}
