package runner

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
	"github.com/jessevdk/go-flags"
)

// 编译时可覆盖的默认值
var (
	DefaultMod     = "reverse"
	DefaultConsole = "tcp://0.0.0.0:34996"
	DefaultLocal   = ""
	DefaultRemote  = ""
)

type MiscOptions struct {
	Key     string `short:"k" long:"key" description:"key for encrypt" default:""`
	Alias   string `short:"a" long:"alias" description:"alias" default:""`
	Version bool   `long:"version" description:"show version"`
	Debug   bool   `long:"debug" description:"debug mode"`
	Detail  bool   `long:"detail" description:"show detail"`
	Quiet   bool   `long:"quiet" description:"quiet mode"`
	Dump    bool   `long:"dump" description:"dump data"`
}

type MainOptions struct {
	ConsoleAddr []string `short:"c" long:"console" description:"console address"`
	LocalAddr   string   `short:"l" long:"local" default:"" description:"local address"`
	RemoteAddr  string   `short:"r" long:"remote" default:""  description:"remote address"`
	Redirect    string   `short:"d" long:"destination" description:"destination agent id"`
	ProxyAddr   []string `short:"x" long:"proxy" description:"outbound proxy chain"`
	ForwardAddr []string `short:"f" long:"forward" description:"proxy chain for connect to console"`
	Mod         string   `short:"m" long:"mod" description:"rem mod, reverse/proxy/bind" default:""`
	ConnectOnly bool     `short:"n" long:"connect-only" description:"only connect to console"`
}

type ConfigOptions struct {
	IP            string `short:"i" long:"ip" description:"console external ip address"`
	Retry         int    `long:"retry" description:"retry times" default:"10"`
	RetryInterval int    `long:"retry-interval" description:"retry interval" default:"10"`
	Subscribe     string `long:"sub" default:"http://0.0.0.0:29999" description:"subscribe address"`
	NoSubscribe   bool   `long:"no-sub" description:"disable subscribe"`
	//Templates string `short:"t" long:"templates" description:"templates path" default:""`
}

type Options struct {
	MainOptions   `group:"Main Options"`
	MiscOptions   `group:"Miscellaneous Options"`
	ConfigOptions `group:"Config Options"`
}

func (opt *Options) ParseArgs(args []string) error {
	parser := flags.NewParser(opt, flags.Default)
	_, err := parser.ParseArgs(args)
	if err != nil {
		return err
	}

	return nil
}

func (opt *Options) Prepare() (*RunnerConfig, error) {
	if opt.Key == "" {
		opt.Key = core.DefaultKey
	}

	r := &RunnerConfig{
		URLs:    &core.URLs{},
		Options: opt,
	}

	var err error
	for _, u := range opt.ConsoleAddr {
		conURL, err := core.NewConsoleURL(u)
		if err != nil {
			return nil, err
		}
		if conURL.User == nil {
			conURL.User = url.UserPassword(opt.Key, "")
		}
		r.ConsoleURLs = append(r.ConsoleURLs, conURL)
	}

	r.URLs.RemoteURL, err = core.NewURL(opt.RemoteAddr)
	if err != nil {
		return nil, err
	}
	r.URLs.LocalURL, err = core.NewURL(opt.LocalAddr)
	if err != nil {
		return nil, err
	}

	if r.ConnectOnly {
		r.Mod = core.Connect
	}
	// 随机端口处理
	if r.Mod == core.Reverse {
		if r.URLs.RemoteURL.Port() == "0" {
			r.URLs.RemoteURL.SetPort(utils.RandPort())
		}
		if r.URLs.RemoteURL.Scheme == core.DefaultScheme {
			r.URLs.RemoteURL.Scheme = core.Socks5Serve
		}
		if r.URLs.LocalURL.Scheme == core.DefaultScheme {
			r.URLs.LocalURL.Scheme = core.RawServe
		}
	} else if r.Mod == core.Proxy {
		if r.URLs.LocalURL.Port() == "0" {
			r.URLs.LocalURL.SetPort(utils.RandPort())
		}
		if r.URLs.RemoteURL.Scheme == core.DefaultScheme {
			r.URLs.RemoteURL.Scheme = core.RawServe
		}
		if r.URLs.LocalURL.Scheme == core.DefaultScheme {
			r.URLs.LocalURL.Scheme = core.Socks5Serve
		}
	}
	opt.preparePortForward(r)

	for _, proxyUrl := range r.ProxyAddr {
		u, err := url.Parse(proxyUrl)
		if err != nil {
			return nil, err
		}
		r.Proxies = append(r.Proxies, u)
	}

	if r.IP == "" {
		resp, err := http.Get("http://myip.ipip.net/s")
		if err == nil {
			content, _ := io.ReadAll(resp.Body)
			r.IP = strings.TrimSpace(string(content))
		} else {
			r.IP = "127.0.0.1"
			utils.Log.Warnf("auto get external ip failed, use 127.0.0.1 replace")
		}
	}

	return r, nil
}

func (opt *Options) preparePortForward(r *RunnerConfig) {
	if r.URLs.RemoteURL.Scheme == core.PortForwardServe {
		if r.URLs.RemoteURL.Hostname() == "0.0.0.0" {
			r.URLs.RemoteURL.SetHostname("127.0.0.1")
		}
		r.URLs.LocalURL.Scheme = core.RawServe
	}
	if r.URLs.LocalURL.Scheme == core.PortForwardServe {
		if r.URLs.LocalURL.Hostname() == "0.0.0.0" {
			r.URLs.LocalURL.SetHostname("127.0.0.1")
		}
		r.URLs.RemoteURL.Scheme = core.RawServe
	}
}
