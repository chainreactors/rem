package runner

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/utils"
)

// 编译时可覆盖的默认值
var (
	DefaultMod     = "reverse"
	DefaultConsole = "tcp://0.0.0.0:34996"
	DefaultLocal   = ""
	DefaultRemote  = ""
	DefaultQuiet   = "" // "true" 启用 quiet 模式
)

type MiscOptions struct {
	Key     string `short:"k" long:"key" description:"key for encrypt" default:""`
	Version bool   `long:"version" description:"show version"`
	Debug   bool   `long:"debug" description:"debug mode"`
	Detail  bool   `long:"detail" description:"show detail"`
	Quiet   bool   `short:"q" long:"quiet" description:"quiet mode"`
	Dump    bool   `long:"dump" description:"dump data"`
	List    bool   `long:"list" description:"list all registered tunnels, services and wrappers"`
}

type MainOptions struct {
	ServerAddr  []string `short:"s" long:"server" description:"server listen address"`
	ClientAddr  []string `short:"c" long:"client" description:"client connect address"`
	LocalAddr   []string `short:"l" long:"local" description:"local address (repeatable)"`
	RemoteAddr  []string `short:"r" long:"remote" description:"remote address (repeatable)"`
	Alias       string   `short:"a" long:"alias" description:"alias" default:""`
	Redirect    string   `short:"d" long:"destination" description:"destination agent id"`
	ProxyAddr   []string `short:"x" long:"proxy" description:"outbound proxy chain"`
	ForwardAddr []string `short:"f" long:"forward" description:"proxy chain for connect to console"`
	Mod         string   `short:"m" long:"mod" description:"rem mod, reverse/proxy/bind" default:""`
	ConnectOnly bool     `short:"n" long:"connect-only" description:"only connect to console"`
}

type ConfigOptions struct {
	IP               string `short:"i" long:"ip" description:"console external ip address"`
	Retry            int    `long:"retry" description:"max consecutive dial failures before giving up; 0 = infinite" default:"0"`
	RetryInterval    int    `long:"retry-interval" description:"retry interval" default:"10"`
	RetryMaxInterval int    `long:"retry-max-interval" description:"max retry interval for exponential backoff" default:"300"`
	LoadBalance      string `long:"lb" description:"connhub load balance algorithm: random/fallback/round-robin"`
	Subscribe        string `long:"sub" default:"http://0.0.0.0:29999" description:"subscribe address"`
	NoSubscribe      bool   `long:"no-sub" description:"disable subscribe"`
}

type Options struct {
	MainOptions   `group:"Main Options"`
	MiscOptions   `group:"Miscellaneous Options"`
	ConfigOptions `group:"Config Options"`
}

var ErrHelpRequested = errors.New("help requested")

const optionsUsage = `
OPTIONS:
  Main Options:
    -s, --server <addr>           server listen address (repeatable)
    -c, --client <addr>           client connect address (repeatable)
    -l, --local <addr>            local address (repeatable)
    -r, --remote <addr>           remote address (repeatable)
    -a, --alias <name>            alias
    -d, --destination <id>        destination agent id
    -x, --proxy <url>             outbound proxy chain (repeatable)
    -f, --forward <url>           proxy chain for connect to console (repeatable)
    -m, --mod <name>              rem mod, reverse/proxy/bind
    -n, --connect-only            only connect to console

  Miscellaneous Options:
    -k, --key <key>               key for encrypt
        --version                 show version
        --debug                   debug mode
        --detail                  show detail
    -q, --quiet                   quiet mode
        --dump                    dump data
        --list                    list all registered tunnels, services and wrappers

  Config Options:
    -i, --ip <ip>                 console external ip address
        --lb <name>               connhub load balance: random/fallback/round-robin
        --sub <url>               subscribe address (default: http://0.0.0.0:29999)
        --no-sub                  disable subscribe

  Common URL Query:
    retry=<num>                   reconnect attempts (default: 10)
    retry-interval=<num>          reconnect interval seconds (default: 10)
    retry-max-interval=<num>      max backoff interval seconds (default: 300)
    lb=<name>                     connhub load balance: random/fallback/round-robin
                                  can be set on --server/--client/--local/--remote URL

    -h, --help                    show help
`

func OptionsUsage() string {
	return optionsUsage
}

func (opt *Options) ParseArgs(args []string) error {
	parsed := *opt
	parsed.applyParseDefaults()
	normalizedArgs := normalizeFlagArgs(args)

	parser := flag.NewFlagSet("rem", flag.ContinueOnError)
	parser.SetOutput(io.Discard)

	bindString(parser, &parsed.Key, "k", "key", "key for encrypt")
	bindBool(parser, &parsed.Version, "", "version", "show version")
	bindBool(parser, &parsed.Debug, "", "debug", "debug mode")
	bindBool(parser, &parsed.Detail, "", "detail", "show detail")
	bindBool(parser, &parsed.Quiet, "q", "quiet", "quiet mode")
	bindBool(parser, &parsed.Dump, "", "dump", "dump data")
	bindBool(parser, &parsed.List, "", "list", "list all registered tunnels, services and wrappers")

	bindStringSlice(parser, &parsed.ServerAddr, "s", "server", "server listen address")
	bindStringSlice(parser, &parsed.ClientAddr, "c", "client", "client connect address")
	bindStringSlice(parser, &parsed.LocalAddr, "l", "local", "local address")
	bindStringSlice(parser, &parsed.RemoteAddr, "r", "remote", "remote address")
	bindString(parser, &parsed.Alias, "a", "alias", "alias")
	bindString(parser, &parsed.Redirect, "d", "destination", "destination agent id")
	bindStringSlice(parser, &parsed.ProxyAddr, "x", "proxy", "outbound proxy chain")
	bindStringSlice(parser, &parsed.ForwardAddr, "f", "forward", "proxy chain for connect to console")
	bindString(parser, &parsed.Mod, "m", "mod", "rem mod, reverse/proxy/bind")
	bindBool(parser, &parsed.ConnectOnly, "n", "connect-only", "only connect to console")

	bindString(parser, &parsed.IP, "i", "ip", "console external ip address")
	bindString(parser, &parsed.LoadBalance, "", "lb", "connhub load balance algorithm: random/fallback/round-robin")
	bindString(parser, &parsed.Subscribe, "", "sub", "subscribe address")
	bindBool(parser, &parsed.NoSubscribe, "", "no-sub", "disable subscribe")

	if err := parser.Parse(normalizedArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ErrHelpRequested
		}
		return err
	}

	if parser.NArg() > 0 {
		return fmt.Errorf("unknown arguments: %s", strings.Join(parser.Args(), " "))
	}

	*opt = parsed
	return nil
}

func (opt *Options) applyParseDefaults() {
	// Retry == 0 means infinite reconnect (default).
	// No override needed.
	if opt.RetryInterval == 0 {
		opt.RetryInterval = 10
	}
	if opt.RetryMaxInterval == 0 {
		opt.RetryMaxInterval = 300
	}
	if opt.Subscribe == "" {
		opt.Subscribe = "http://0.0.0.0:29999"
	}
	if opt.LoadBalance == "" {
		opt.LoadBalance = "fallback"
	}
}

func bindString(parser *flag.FlagSet, target *string, shortName, longName, usage string) {
	if shortName != "" {
		parser.StringVar(target, shortName, *target, usage)
	}
	if longName != "" {
		parser.StringVar(target, longName, *target, usage)
	}
}

func bindBool(parser *flag.FlagSet, target *bool, shortName, longName, usage string) {
	if shortName != "" {
		parser.BoolVar(target, shortName, *target, usage)
	}
	if longName != "" {
		parser.BoolVar(target, longName, *target, usage)
	}
}

func bindStringSlice(parser *flag.FlagSet, target *[]string, shortName, longName, usage string) {
	if shortName != "" {
		parser.Var(&stringSliceValue{target: target}, shortName, usage)
	}
	if longName != "" {
		parser.Var(&stringSliceValue{target: target}, longName, usage)
	}
}

type stringSliceValue struct {
	target *[]string
}

var shortFlagsWithValue = map[byte]struct{}{
	'k': {},
	's': {},
	'c': {},
	'l': {},
	'r': {},
	'a': {},
	'd': {},
	'x': {},
	'f': {},
	'm': {},
	'i': {},
}

func normalizeFlagArgs(args []string) []string {
	normalized := make([]string, 0, len(args))

	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			normalized = append(normalized, args[index:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) <= 2 || strings.Contains(arg, "=") {
			normalized = append(normalized, arg)
			continue
		}

		shortFlags := arg[1:]
		expanded := false
		for flagIndex := 0; flagIndex < len(shortFlags); flagIndex++ {
			name := shortFlags[flagIndex]
			normalized = append(normalized, "-"+string(name))
			if _, ok := shortFlagsWithValue[name]; ok {
				if flagIndex+1 < len(shortFlags) {
					normalized = append(normalized, shortFlags[flagIndex+1:])
				}
				expanded = true
				break
			}
		}
		if !expanded {
			continue
		}
	}

	return normalized
}

func (value *stringSliceValue) String() string {
	if value == nil || value.target == nil {
		return ""
	}
	return strings.Join(*value.target, ",")
}

func (value *stringSliceValue) Set(input string) error {
	*value.target = append(*value.target, input)
	return nil
}

func (opt *Options) Prepare() (*RunnerConfig, error) {
	// 检查是否同时设置了 ServerAddr 和 ClientAddr
	if len(opt.ServerAddr) > 0 && len(opt.ClientAddr) > 0 {
		return nil, fmt.Errorf("cannot specify both -s/--server and -c/--client")
	}

	// 如果命令行参数为空，使用编译时设置的默认值
	if opt.Mod == "" {
		opt.Mod = DefaultMod
	}

	if len(opt.LocalAddr) == 0 && DefaultLocal != "" {
		opt.LocalAddr = []string{DefaultLocal}
	}
	if len(opt.RemoteAddr) == 0 && DefaultRemote != "" {
		opt.RemoteAddr = []string{DefaultRemote}
	}

	// 如果命令行没有设置 Quiet，使用编译时默认值
	if !opt.Quiet && DefaultQuiet == "true" {
		opt.Quiet = true
	}

	if opt.Debug {
		utils.Log = logs.NewLogger(logs.DebugLevel)
		utils.Log.LogFileName = "maitai.log"
		utils.Log.Init()
	} else if opt.Detail {
		utils.Log = logs.NewLogger(utils.IOLog)
	} else if opt.Quiet {
		utils.Log = logs.NewLogger(100)
	} else {
		utils.Log = logs.NewLogger(logs.InfoLevel)
	}
	if opt.Key == "" {
		opt.Key = core.DefaultKey
	}

	r := &RunnerConfig{
		URLs:    &core.URLs{},
		Options: opt,
	}
	// 根据 ServerAddr 或 ClientAddr 设置 ConsoleAddr
	var consoleAddrs []string

	if len(opt.ServerAddr) > 0 {
		consoleAddrs = opt.ServerAddr
		r.IsServerMode = true
	} else if len(opt.ClientAddr) > 0 {
		consoleAddrs = opt.ClientAddr
		r.IsServerMode = false
	} else {
		// 如果都没有设置，使用默认值（向后兼容：根据hostname判断）
		consoleAddrs = []string{DefaultConsole}
	}

	var err error
	for _, u := range consoleAddrs {
		conURL, err := core.NewConsoleURL(u)
		if err != nil {
			return nil, err
		}
		if conURL.User == nil {
			conURL.User = url.UserPassword(opt.Key, "")
		}
		r.ConsoleURLs = append(r.ConsoleURLs, conURL)
	}

	r.URLs.RemoteURL, err = core.NewURL(firstOrEmpty(opt.RemoteAddr))
	if err != nil {
		return nil, err
	}
	r.URLs.LocalURL, err = core.NewURL(firstOrEmpty(opt.LocalAddr))
	if err != nil {
		return nil, err
	}

	// Parse extra serve pairs (index 1+)
	extraCount := len(opt.LocalAddr)
	if len(opt.RemoteAddr) > extraCount {
		extraCount = len(opt.RemoteAddr)
	}
	for i := 1; i < extraCount; i++ {
		localURL, err := core.NewURL(indexOrEmpty(opt.LocalAddr, i))
		if err != nil {
			return nil, fmt.Errorf("extra local[%d]: %w", i, err)
		}
		remoteURL, err := core.NewURL(indexOrEmpty(opt.RemoteAddr, i))
		if err != nil {
			return nil, fmt.Errorf("extra remote[%d]: %w", i, err)
		}
		r.ExtraServes = append(r.ExtraServes, ExtraServe{LocalURL: localURL, RemoteURL: remoteURL})
	}
	if err = opt.applyCommonURLParams(r); err != nil {
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
			r.URLs.RemoteURL.SetSchema(core.Socks5Serve)
		}
		if r.URLs.LocalURL.Scheme == core.DefaultScheme {
			r.URLs.LocalURL.SetSchema(core.RawServe)
		}
	} else if r.Mod == core.Proxy {
		if r.URLs.LocalURL.Port() == "0" {
			r.URLs.LocalURL.SetPort(utils.RandPort())
		}
		if r.URLs.RemoteURL.Scheme == core.DefaultScheme {
			r.URLs.RemoteURL.SetSchema(core.RawServe)
		}
		if r.URLs.LocalURL.Scheme == core.DefaultScheme {
			r.URLs.LocalURL.SetSchema(core.Socks5Serve)
		}
	}
	// Apply scheme defaults to extra serves
	for i := range r.ExtraServes {
		if r.Mod == core.Reverse {
			if r.ExtraServes[i].RemoteURL.Port() == "0" {
				r.ExtraServes[i].RemoteURL.SetPort(utils.RandPort())
			}
			if r.ExtraServes[i].RemoteURL.Scheme == core.DefaultScheme {
				r.ExtraServes[i].RemoteURL.SetSchema(core.Socks5Serve)
			}
			if r.ExtraServes[i].LocalURL.Scheme == core.DefaultScheme {
				r.ExtraServes[i].LocalURL.SetSchema(core.RawServe)
			}
		} else if r.Mod == core.Proxy {
			if r.ExtraServes[i].LocalURL.Port() == "0" {
				r.ExtraServes[i].LocalURL.SetPort(utils.RandPort())
			}
			if r.ExtraServes[i].RemoteURL.Scheme == core.DefaultScheme {
				r.ExtraServes[i].RemoteURL.SetSchema(core.RawServe)
			}
			if r.ExtraServes[i].LocalURL.Scheme == core.DefaultScheme {
				r.ExtraServes[i].LocalURL.SetSchema(core.Socks5Serve)
			}
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

	if r.IsServerMode && r.ConsoleURLs[0].Host == "0.0.0.0" && r.IP == "" {
		r.IP = detectExternalIP()
	}

	utils.Log.Importantf("mod: %s , remote: %s ,local %s", r.Mod, r.URLs.RemoteURL.String(), r.URLs.LocalURL.String())
	utils.Log.Importantf("console: %v", r.ConsoleURLs)
	return r, nil
}

func (opt *Options) preparePortForward(r *RunnerConfig) {
	if r.URLs.RemoteURL.Scheme == core.PortForwardServe {
		r.URLs.LocalURL.Scheme = core.RawServe
	}
	if r.URLs.LocalURL.Scheme == core.PortForwardServe {
		r.URLs.RemoteURL.Scheme = core.RawServe
	}
}

func (opt *Options) applyCommonURLParams(r *RunnerConfig) error {
	urls := make([]*core.URL, 0, len(r.ConsoleURLs)+2)
	urls = append(urls, r.ConsoleURLs...)
	urls = append(urls, r.URLs.LocalURL, r.URLs.RemoteURL)

	if value, found := popCommonQueryValue(urls, "retry"); found {
		retry, err := parseNonNegativeInt("retry", value)
		if err != nil {
			return err
		}
		opt.Retry = retry
	}

	if value, found := popCommonQueryValue(urls, "retry-interval"); found {
		retryInterval, err := parseNonNegativeInt("retry-interval", value)
		if err != nil {
			return err
		}
		opt.RetryInterval = retryInterval
	}

	if value, found := popCommonQueryValue(urls, "retry-max-interval"); found {
		retryMaxInterval, err := parseNonNegativeInt("retry-max-interval", value)
		if err != nil {
			return err
		}
		opt.RetryMaxInterval = retryMaxInterval
	}

	if value, found := popCommonQueryValue(urls, "lb"); found {
		opt.LoadBalance = value
	}

	return nil
}

func popCommonQueryValue(urls []*core.URL, key string) (string, bool) {
	var value string
	found := false

	for _, parsedURL := range urls {
		if parsedURL == nil {
			continue
		}

		query := parsedURL.Query()
		if !query.Has(key) {
			continue
		}

		currentValue := query.Get(key)
		query.Del(key)
		parsedURL.RawQuery = query.Encode()

		if !found {
			value = currentValue
			found = true
		}
	}

	return value, found
}

func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func indexOrEmpty(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func parseNonNegativeInt(name, value string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("invalid %s value: empty", name)
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: %w", name, value, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("invalid %s value %q: must be >= 0", name, value)
	}

	return parsed, nil
}
