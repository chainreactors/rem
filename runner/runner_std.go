//go:build !tinygo

package runner

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/user"
	"strings"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/cryptor"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	proxyclient.RegisterScheme("REM", newRemProxyClient)
}

func newRemProxyClient(proxyURL *url.URL, _ proxyclient.Dial) (proxyclient.Dial, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		memoryPipe := utils.RandomString(8)
		console, err := NewConsoleWithCMD(fmt.Sprintf("-c %s -m proxy -l memory+socks5://:@%s", proxyURL.String(), memoryPipe))
		if err != nil {
			return nil, err
		}
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
			}
			time.Sleep(100 * time.Millisecond)
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

func resolveAgentUsername() string {
	current, err := user.Current()
	if err != nil {
		utils.Log.Warnf("[agent] %s", err.Error())
		return "unknown"
	}
	return current.Username
}

func resolveAgentInterfaces() []string {
	return utils.GetLocalSubnet()
}

func buildAgentName(hostname string) string {
	name := hostname + "-" + utils.GenerateMachineHash()
	if len(name) > 24 {
		name = name[:24]
	}
	return name
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

		data, err := utils.MarshalClashConfigYAML(&utils.ClashConfig{
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
				"IP-CIDR,10.0.0.0/8,10_NET",
				"IP-CIDR,172.16.0.0/12,172_NET",
				"IP-CIDR,192.168.0.0/16,192_NET",
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

func detectExternalIP() string {
	resp, err := http.Get("http://myip.ipip.net/s")
	if err == nil {
		content, _ := io.ReadAll(resp.Body)
		return strings.TrimSpace(string(content))
	}
	utils.Log.Warnf("auto get external ip failed, use 127.0.0.1 replace")
	return "127.0.0.1"
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
	if err != nil {
		return err
	}
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
			wrapped, err := plug.Handle(conn, conn)
			if err != nil {
				utils.Log.Error(err)
				return
			}
			cio.Join(wrapped, conn)
		}()
	}
}

func buildConsoleToken(key string) (string, error) {
	data := []byte(key)
	token, err := cryptor.AesEncrypt(data, cryptor.PKCS7Padding(data, 16))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(token), nil
}

func defaultWrapperOptions() core.WrapperOptions {
	return core.GenerateRandomWrapperOptions(2, 4)
}
