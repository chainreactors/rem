package core

import (
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/chainreactors/rem/x/utils"
)

type Outbound interface {
	Name() string

	Handle(conn io.ReadWriteCloser, realConn net.Conn) (net.Conn, error)

	ToClash() *utils.Proxies
}

// Use sync.Map to survive TinyGo WASM package-level variable corruption.
var outboundCreators sync.Map // map[string]OutboundCreatorFn

// params has prefix "plugin_"
type OutboundCreatorFn func(options map[string]string, dial ContextDialer) (Outbound, error)

func OutboundRegister(name string, fn OutboundCreatorFn) {
	if _, loaded := outboundCreators.LoadOrStore(name, fn); loaded {
		outboundCreators.Store(name, fn)
	}
}

func OutboundCreate(name string, options map[string]string, dialer ContextDialer) (p Outbound, err error) {
	if fn, ok := outboundCreators.Load(name); ok {
		return fn.(OutboundCreatorFn)(options, dialer)
	}
	available := GetRegisteredOutbounds()
	err = fmt.Errorf("outbound [%s] is not registered. Available outbounds: %v", name, available)
	return
}

// GetRegisteredOutbounds 获取所有已注册的 outbound 名称
func GetRegisteredOutbounds() []string {
	var names []string
	outboundCreators.Range(func(key, value interface{}) bool {
		names = append(names, key.(string))
		return true
	})
	return names
}
