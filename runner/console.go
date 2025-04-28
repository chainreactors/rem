package runner

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/protocol/tunnel"
	_ "github.com/chainreactors/rem/protocol/tunnel/memory"
	"github.com/chainreactors/rem/x/utils"
	"github.com/kballard/go-shellquote"
	"gopkg.in/yaml.v3"
)

func init() {
	proxyclient.RegisterScheme("REM", newRemProxyClient)
}

// 实现 DialFactory 函数
func newRemProxyClient(proxyURL *url.URL, upstreamDial proxyclient.Dial) (proxyclient.Dial, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		memoryPipe := utils.RandomString(8)
		console, err := NewConsoleWithCMD(fmt.Sprintf("-c %s -m proxy -l memory+socks5://:@%s", proxyURL.String(), memoryPipe))
		a, err := console.Dial(&core.URL{URL: proxyURL})
		if err != nil {
			return nil, err
		}
		go func() {
			err := a.Handler()
			if err != nil {
				a.Log("handler", logs.ErrorLevel, "%s", err.Error())
				return
			}
		}()

		for {
			if a.Init {
				break
			} else {
				time.Sleep(100 * time.Millisecond)
			}
		}
		memURL := &url.URL{
			Scheme: "memory",
			Host:   memoryPipe,
		}
		memClient, err := proxyclient.NewClient(memURL)
		if err != nil {
			return nil, err
		}
		return memClient(ctx, network, address)
	}, nil
}

func NewConsoleWithCMD(cmdline string) (*Console, error) {
	var option Options
	args, err := shellquote.Split(cmdline)
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

func NewConsole(runner *RunnerConfig, urls *core.URLs) (*Console, error) {
	var err error
	console := &Console{
		URLs:   urls,
		Config: runner,
	}

	token, err := utils.AesEncrypt([]byte(runner.Key), utils.PKCS7Padding([]byte(runner.Key), 16))
	if err != nil {
		return nil, err
	}
	console.token = hex.EncodeToString(token)
	isServer := console.IsServer()
	tunOpts := []tunnel.TunnelOption{}
	if console.ConsoleURL.GetQuery("tls") != "" {
		tunOpts = append(tunOpts, tunnel.WithTLS())
	}
	if console.ConsoleURL.GetQuery("tlsintls") != "" {
		tunOpts = append(tunOpts, tunnel.WithTLSInTLS())
	}
	if len(runner.ForwardAddr) > 0 {
		tunOpts = append(tunOpts, tunnel.WithProxyClient(runner.ForwardAddr))
	}
	if console.ConsoleURL.GetQuery("compress") != "" {
		tunOpts = append(tunOpts, tunnel.WithCompression())
	}
	var wrapperOpts core.WrapperOptions
	if wrapperStr := console.ConsoleURL.GetQuery("wrapper"); wrapperStr != "" {
		if wrapperStr != "raw" { //debug
			wrapperOpts, err = core.ParseWrapperOptions(wrapperStr, runner.Key)
			if err != nil {
				return nil, err
			}
		}
	} else {
		wrapperOpts = core.GenerateRandomWrapperOptions(2, 4)
	}

	if wrapperOpts != nil {
		console.ConsoleURL.SetQuery("wrapper", wrapperOpts.String(runner.Key))
		tunOpts = append(tunOpts, tunnel.WithWrappers(isServer, wrapperOpts))
	}

	console.tunnel, err = tunnel.NewTunnel(context.Background(), console.ConsoleURL.Scheme, isServer, tunOpts...)
	if err != nil {
		return nil, err
	}
	return console, nil
}

type Console struct {
	Config *RunnerConfig
	token  string
	*core.URLs
	sub    *core.URL
	tunnel *tunnel.TunnelService
	closed bool
}

func (c *Console) IsServer() bool {
	return c.ConsoleURL.Hostname() == "0.0.0.0"
}

func (c *Console) Run() error {
	if c.IsServer() {
		err := c.Listen(c.ConsoleURL)
		if err != nil {
			return err
		}

		utils.Log.Importantf("%s channel starting with %s", c.ConsoleURL.Scheme, c.Config.IP)
		utils.Log.Important(c.Link())
		for {
			age, err := c.Accept()
			if err != nil {
				utils.Log.Error(err.Error())
				continue
			}

			go c.Handler(age)
		}
	} else {
		for i := 0; i <= c.Config.Retry; i++ {
			age, err := c.Dial(c.ConsoleURL)
			if err != nil {
				utils.Log.Error(err)
				time.Sleep(time.Duration(c.Config.RetryInterval) * time.Second)
				continue
			}
			c.Handler(age)
			utils.Log.Infof("Reconnect to server, %d ", i)
		}
	}
	return nil
}

func (c *Console) Bind() error {
	client, err := proxyclient.NewClientChain(c.Config.Proxies)
	if err != nil {
		return nil
	}
	if c.LocalURL.Port() == "0" {
		c.LocalURL.SetPort(utils.RandPort())
	}
	plug, err := core.OutboundCreate(c.LocalURL.Scheme, map[string]string{
		"username": c.LocalURL.Username(),
		"password": c.LocalURL.Password(),
		"server":   c.Config.IP,
		"port":     c.LocalURL.Port(),
	}, core.NewProxyDialer(client))
	listen, err := net.Listen("tcp", c.LocalURL.Host)
	if err != nil {
		return err
	}
	for {
		conn, err := listen.Accept()
		if err != nil {
			return err
		}
		go func() {
			_, err := plug.Handle(conn, conn)
			if err != nil {
				utils.Log.Error(err)
				return
			}
		}()
	}
	return nil
}

func (c *Console) newAgent(urls *core.URLs, r *RunnerConfig) (*agent.Agent, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}

	username, err := user.Current()
	if err != nil {
		return nil, err
	}

	return agent.NewAgent(&agent.Config{
		Type: core.CLIENT,
		Mod:  r.Mod,
		URLs: &core.URLs{
			ConsoleURL: urls.ConsoleURL.Copy(),
		},
		ExternalIP: c.Config.IP,
		Alias:      c.Config.Alias,
		AuthKey:    []byte(r.Key),
		Proxies:    r.Proxies,
		Redirect:   r.Redirect,
		Interfaces: utils.GetLocalSubnet(),
		Params: map[string]string{
			"ip":   r.IP,
			"name": (hostname + "-" + utils.GenerateMachineHash())[:24],
		},
		Username: username.Username,
		Hostname: hostname,
	})
}

func (c *Console) Dial(address *core.URL) (*agent.Agent, error) {
	conn, err := c.tunnel.Dial(address.String())
	if err != nil {
		return nil, err
	}
	a, err := c.newAgent(c.URLs, c.Config)
	if err != nil {
		return nil, err
	}

	err = a.Login(conn)
	if err != nil {
		a.Close(err)
		return nil, err
	}
	err = a.Dial(c.RemoteURL, c.LocalURL)
	if err != nil {
		return nil, err
	}
	agent.Agents.Add(a)
	return a, nil
}

func (c *Console) Fork(raw string, args []string) (*agent.Agent, error) {
	var opt Options
	err := opt.ParseArgs(args)
	if err != nil {
		return nil, err
	}
	r, err := opt.Prepare()
	if err != nil {
		return nil, err
	}
	if r.Alias == "" {
		r.Alias = utils.RandomString(8)
	}

	a, ok := agent.Agents.Get(raw)
	if !ok {
		return nil, fmt.Errorf("not found agent")
	}

	forked, err := a.Fork(&message.Control{
		Mod:         r.Mod,
		Source:      r.Alias,
		Destination: r.Alias,
		Local:       r.URLs.RemoteURL.String(),
		Remote:      r.URLs.LocalURL.String(),
	})
	if err != nil {
		return nil, err
	}
	a.Send(&message.Control{
		Mod:         r.Mod,
		Source:      r.Alias,
		Destination: r.Alias,
		Local:       r.URLs.RemoteURL.String(),
		Remote:      r.URLs.LocalURL.String(),
		Fork:        true,
	})
	agent.Agents.Add(forked)
	return forked, nil
}

func (c *Console) Listen(dst *core.URL) error {
	var err error
	_, err = c.tunnel.Listen(dst.String())
	if err != nil {
		if strings.Contains(err.Error(), "bind: An attempt was made to access a socket in a way forbidden by its access permissions.") {
			dst.SetPort(utils.RandPort())
			utils.Log.Importantf("port is already in use, get a new port **%s** at random", c.ConsoleURL.Port())
			return c.Listen(dst)
		}
		return err
	}
	if !c.Config.NoSubscribe {
		if c.Config.Subscribe != "" {
			subURL, err := core.NewURL(c.Config.Subscribe)
			if err != nil {
				utils.Log.Warnf("subscribe url error: %s", err.Error())
			}
			c.sub = subURL
		}
		c.handlerSubscribe()
	}

	return nil
}

func (c *Console) Accept() (*agent.Agent, error) {
	conn, err := c.tunnel.Accept()
	if err != nil {
		return nil, err
	}

	loginMsg, err := cio.ReadAndAssertMsg(conn, message.LoginMsg)
	if err != nil {
		return nil, err
	}

	login := loginMsg.(*message.Login)
	if c.token != login.Token {
		utils.Log.Error("error token , please check")
		cio.WriteMsg(conn, &message.Ack{Status: message.StatusFailed})
		conn.Close()
		return nil, fmt.Errorf("invalid token")
	} else {
		cio.WriteMsg(conn, &message.Ack{Status: message.StatusSuccess})
	}

	controlMsg, err := cio.ReadAndAssertMsg(conn, message.ControlMsg)
	if err != nil {
		return nil, err
	} else {
		cio.WriteMsg(conn, &message.Ack{Status: message.StatusSuccess})
	}
	control := controlMsg.(*message.Control)
	server, err := agent.NewAgent(&agent.Config{
		Alias:      login.Agent,
		Type:       core.SERVER,
		Mod:        control.Mod,
		Redirect:   control.Destination,
		ExternalIP: c.Config.IP,
		Interfaces: login.Interfaces,
		Hostname:   login.Hostname,
		Username:   login.Username,
		Controller: control,
		URLs: &core.URLs{
			ConsoleURL: login.ConsoleURL(),
			RemoteURL:  control.LocalURL(),
			LocalURL:   control.RemoteURL(),
		},
		Params: control.Options,
	})
	server.Conn = conn
	utils.Log.Importantf("%s:%s %s connected from %s, iface: %v", server.Hostname, server.Username, server.Name(), conn.RemoteAddr().String(), server.Interfaces)
	agent.Agents.Add(server)
	return server, nil
}

func (c *Console) Handler(server *agent.Agent) {
	defer func() {
		agent.Agents.Delete(server.ID)
	}()
	err := server.Handler()
	if err != nil {
		server.Log("handler", logs.DebugLevel, "%s", err.Error())
	}
}

func (c *Console) handlerSubscribe() {
	var subURL *core.URL
	if c.sub != nil {
		subURL = c.sub
	} else {
		subURL, _ = core.NewURL(fmt.Sprintf("http://0.0.0.0:%d", utils.RandPort()))
	}
	if subURL.Path == "" {
		subURL.Path = "/" + utils.GenerateMachineHash()
	}
	mux := http.NewServeMux()
	mux.HandleFunc(subURL.Path, func(w http.ResponseWriter, request *http.Request) {
		var proxies []*utils.Proxies
		agent.Agents.Range(func(key, value interface{}) bool {
			a := value.(*agent.Agent)
			if a.Inbound != nil {
				if proxy := a.Inbound.ToClash(); proxy != nil {
					proxies = append(proxies, proxy)
				}
			}

			return true
		})

		var proxyNames []string
		for _, proxy := range proxies {
			proxyNames = append(proxyNames, proxy.Name)
		}

		data, err := yaml.Marshal(&utils.ClashConfig{
			Proxies: proxies,
			Mode:    "rule",
			ProxyGroups: []*utils.ProxyGroup{
				{
					Name:    "10_NET",
					Type:    "select",
					Proxies: append(proxyNames, "DIRECT"),
				},
				{
					Name:    "172_NET",
					Type:    "select",
					Proxies: append(proxyNames, "DIRECT"),
				},
				{
					Name:    "192_NET",
					Type:    "select",
					Proxies: append(proxyNames, "DIRECT"),
				},
			},
			Rules: []string{
				// 10段内网
				"IP-CIDR,10.0.0.0/8,10_NET",
				// 172段内网
				"IP-CIDR,172.16.0.0/12,172_NET",
				// 192段内网
				"IP-CIDR,192.168.0.0/16,192_NET",
				// 其他流量直连
				"MATCH,DIRECT",
			},
		})

		if err != nil {
			utils.Log.Error(err)
			return
		}
		w.Header().Set("Content-Disposition", "attachment;filename*=UTF-8''clash_proxies")
		w.WriteHeader(200)
		w.Write(data)
	})

	go func() {
		server := &http.Server{
			Addr:    fmt.Sprintf("0.0.0.0:%s", subURL.Port()),
			Handler: mux,
		}
		err := server.ListenAndServe()
		if err != nil {
			utils.Log.Error(err)
		}
	}()

	utils.Log.Importantf("subscribe server started at http://%s:%s%s", c.Config.IP, subURL.Port(), subURL.Path)
	return
}

func (c *Console) Close() error {
	c.closed = true
	agent.Agents.Range(func(key, value interface{}) bool {
		value.(*agent.Agent).Close(nil)
		return true
	})
	return c.tunnel.Close()
}

func (c *Console) Link() string {
	u := c.tunnel.Addr().(*core.URL)
	if u.Scheme != core.UNIXTunnel {
		u.SetHostname(c.Config.IP)
	}
	return u.String()
}

func (c *Console) Subscribe() string {
	return c.sub.String()
}
