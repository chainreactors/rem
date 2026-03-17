package agent

import (
	"net/url"
	"time"

	"github.com/pkg/errors"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
)

const (
	monitorInterval = 30
)

var (
	KeepaliveInterval  = 60 * time.Second // client sends ping every interval
	KeepaliveMaxMissed = 3                // consecutive unanswered pings before declaring dead
)

var (
	ErrNotFoundBridge = errors.New("not found bridge")
	ErrNotFoundAgent  = errors.New("not found agent")
)

func SetKeepaliveConfig(interval time.Duration, maxMissed int) {
	if interval > 0 {
		KeepaliveInterval = interval
	}
	if maxMissed > 0 {
		KeepaliveMaxMissed = maxMissed
	}
}

type Config struct {
	*core.URLs
	ExternalIP  string
	Alias       string
	Redirect    string
	Type        string
	AuthKey     []byte
	Mod         string
	LoadBalance string
	Proxies     []*url.URL
	Params      map[string]string
	Interfaces  []string
	Username    string
	Hostname    string
	Controller  *message.Control
}

func (c *Config) Clone(ctrl *message.Control) *Config {
	return &Config{
		URLs: &core.URLs{
			ConsoleURL: c.ConsoleURL.Copy(),
			RemoteURL:  ctrl.RemoteURL(),
			LocalURL:   ctrl.LocalURL(),
		},
		ExternalIP:  c.ExternalIP,
		Alias:       ctrl.Source,
		Mod:         ctrl.Mod,
		Redirect:    ctrl.Destination,
		Type:        c.Type,
		AuthKey:     c.AuthKey,
		LoadBalance: c.LoadBalance,
		Proxies:     c.Proxies,
		Params:      c.Params,
		Controller:  c.Controller,
	}
}

func (c *Config) LocalAddr() string {
	return c.LocalURL.Host
}

func (c *Config) RemoteAddr() string {
	return c.RemoteURL.Host
}
