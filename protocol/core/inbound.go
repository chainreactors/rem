package core

import (
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/chainreactors/rem/x/utils"
)

type Inbound interface {
	Name() string
	Relay(conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error)
	ToClash() *utils.Proxies
}

// Use sync.Map to survive TinyGo WASM package-level variable corruption.
var inboundCreators sync.Map // map[string]InboundCreatorFn

// params has prefix "relay_"
type InboundCreatorFn func(options map[string]string) (Inbound, error)

func InboundRegister(name string, fn InboundCreatorFn) {
	if _, loaded := inboundCreators.LoadOrStore(name, fn); loaded {
		inboundCreators.Store(name, fn)
	}
}

func InboundCreate(name string, options map[string]string) (p Inbound, err error) {
	if fn, ok := inboundCreators.Load(name); ok {
		return fn.(InboundCreatorFn)(options)
	}
	available := GetRegisteredInbounds()
	err = fmt.Errorf("inbound [%s] is not registered. Available inbounds: %v", name, available)
	return
}

// GetRegisteredInbounds 获取所有已注册的 inbound 名称
func GetRegisteredInbounds() []string {
	var names []string
	inboundCreators.Range(func(key, value interface{}) bool {
		names = append(names, key.(string))
		return true
	})
	return names
}

func NewPluginOption(options map[string]string, mod, typ string) *PluginOption {
	if options == nil {
		options = make(map[string]string)
	}
	options["type"] = typ
	return &PluginOption{
		options: options,
		Mod:     mod,
		Proxy:   utils.NewProxies(options),
	}
}

type PluginOption struct {
	Proxy   *utils.Proxies
	Mod     string
	options map[string]string
}

func (relay *PluginOption) String() string {
	return fmt.Sprintf("%s %s %d %s %s",
		relay.Proxy.Type,
		relay.Proxy.Server,
		relay.Proxy.Port,
		relay.Proxy.Username,
		relay.Proxy.Password)
}

func (relay *PluginOption) URL() string {
	if relay.Proxy.Username != "" {
		return fmt.Sprintf("%s://%s:%s@%s:%d",
			relay.Proxy.Type,
			relay.Proxy.Username,
			relay.Proxy.Password,
			relay.Proxy.Server,
			relay.Proxy.Port,
		)
	} else {
		return fmt.Sprintf("%s://%s:%d",
			relay.Proxy.Type,
			relay.Proxy.Server,
			relay.Proxy.Port,
		)
	}
}

func (relay *PluginOption) ToClash() *utils.Proxies {
	return relay.Proxy
}
