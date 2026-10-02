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
	Via         string // upstream relay ID (empty = direct connection to server)
	Type        string
	AuthKey     []byte
	InboundSide string // "local", "remote", or ""
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
			LocalURL:   ctrl.LocalURL(),
			RemoteURL:  ctrl.RemoteURL(),
		},
		ExternalIP:  c.ExternalIP,
		Alias:       ctrl.Source,
		InboundSide: ctrl.InboundSide,
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
