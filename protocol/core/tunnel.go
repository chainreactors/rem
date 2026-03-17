package core

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
)

type TunnelDialer interface {
	Dial(dst string) (net.Conn, error)
}

type WrappedDialer struct {
	TunnelDialer
	Dialer func(string) (net.Conn, error)
}

func (w *WrappedDialer) Dial(dst string) (net.Conn, error) {
	if w.Dialer != nil {
		return w.Dialer(dst)
	}
	return w.TunnelDialer.Dial(dst)
}

type TunnelListener interface {
	net.Listener
	Listen(dst string) (net.Listener, error)
}

type WrappedListener struct {
	TunnelListener
	Lns net.Listener
}

func (w *WrappedListener) Accept() (net.Conn, error) {
	if w.Lns != nil {
		return w.Lns.Accept()
	}
	return w.TunnelListener.Accept()
}

// DialerCreatorFn 是创建 dialer 的工厂函数
type DialerCreatorFn func(ctx context.Context) (TunnelDialer, error)

// ListenerCreatorFn 是创建 listener 的工厂函数
type ListenerCreatorFn func(ctx context.Context) (TunnelListener, error)

var (
	dialerCreators   sync.Map // map[string]DialerCreatorFn
	listenerCreators sync.Map // map[string]ListenerCreatorFn
	// 别名映射表: alias -> "tunnel+serve" 格式
	dialerAliases   sync.Map // map[string]string
	listenerAliases sync.Map // map[string]string
)

// DialerRegister 注册一个 dialer 类型，可选地注册别名
// 例如: DialerRegister("tcp+tls", fn, "tls")
func DialerRegister(name string, fn DialerCreatorFn, aliases ...string) {
	if _, loaded := dialerCreators.LoadOrStore(name, fn); loaded {
		// Already registered — overwrite silently for ForceRegister compatibility
		dialerCreators.Store(name, fn)
	}
	// 注册所有别名
	for _, alias := range aliases {
		dialerAliases.Store(alias, name)
	}
}

// ListenerRegister 注册一个 listener 类型，可选地注册别名
// 例如: ListenerRegister("tcp+tls", fn, "tls")
func ListenerRegister(name string, fn ListenerCreatorFn, aliases ...string) {
	if _, loaded := listenerCreators.LoadOrStore(name, fn); loaded {
		listenerCreators.Store(name, fn)
	}
	// 注册所有别名
	for _, alias := range aliases {
		listenerAliases.Store(alias, name)
	}
}

// DialerCreate 创建一个指定类型的 dialer
// 支持别名解析，如果 name 是别名，会自动解析为实际名称
func DialerCreate(name string, ctx context.Context) (TunnelDialer, error) {
	// 先检查是否是别名
	if target, ok := dialerAliases.Load(name); ok {
		name = target.(string)
	}

	if fn, ok := dialerCreators.Load(name); ok {
		return fn.(DialerCreatorFn)(ctx)
	}
	available := GetRegisteredDialers()
	return nil, fmt.Errorf("dialer [%s] is not registered. Available dialers: %v", name, available)
}

// ListenerCreate 创建一个指定类型的 listener
// 支持别名解析，如果 name 是别名，会自动解析为实际名称
func ListenerCreate(name string, ctx context.Context) (TunnelListener, error) {
	// 先检查是否是别名
	if target, ok := listenerAliases.Load(name); ok {
		name = target.(string)
	}

	if fn, ok := listenerCreators.Load(name); ok {
		return fn.(ListenerCreatorFn)(ctx)
	}
	available := GetRegisteredListeners()
	return nil, fmt.Errorf("listener [%s] is not registered. Available listeners: %v", name, available)
}

// GetRegisteredDialers 获取所有已注册的 dialer 名称，包括别名
// 格式: "dns [dot, doh]" 或 "tcp"
func GetRegisteredDialers() []string {
	var names []string

	// 构建反向映射: target -> []aliases
	reverseMap := make(map[string][]string)
	dialerAliases.Range(func(key, value interface{}) bool {
		alias := key.(string)
		target := value.(string)
		reverseMap[target] = append(reverseMap[target], alias)
		return true
	})

	// 添加实际注册的 dialers，如果有别名则附加
	dialerCreators.Range(func(key, value interface{}) bool {
		name := key.(string)
		if aliases, ok := reverseMap[name]; ok {
			names = append(names, fmt.Sprintf("%s [%s]", name, strings.Join(aliases, ", ")))
		} else {
			names = append(names, name)
		}
		return true
	})

	return names
}

// GetRegisteredListeners 获取所有已注册的 listener 名称，包括别名
// 格式: "dns [dot, doh]" 或 "tcp"
func GetRegisteredListeners() []string {
	var names []string

	// 构建反向映射: target -> []aliases
	reverseMap := make(map[string][]string)
	listenerAliases.Range(func(key, value interface{}) bool {
		alias := key.(string)
		target := value.(string)
		reverseMap[target] = append(reverseMap[target], alias)
		return true
	})

	// 添加实际注册的 listeners，如果有别名则附加
	listenerCreators.Range(func(key, value interface{}) bool {
		name := key.(string)
		if aliases, ok := reverseMap[name]; ok {
			names = append(names, fmt.Sprintf("%s [%s]", name, strings.Join(aliases, ", ")))
		} else {
			names = append(names, name)
		}
		return true
	})

	return names
}

func GetMetas(ctx context.Context) Metas {
	if m, ok := ctx.Value("meta").(Metas); ok {
		return m
	}
	return nil
}

type Metas map[string]interface{}

func (m Metas) Value(key string) interface{} {
	return m[key]
}

func (m Metas) GetString(key string) string {
	v, ok := m[key]
	if ok {
		s, ok := v.(string)
		if ok {
			return s
		}
		return ""
	}
	return ""
}
func (m Metas) URL() *URL {
	return m["url"].(*URL)
}

type BeforeHook struct {
	DialHook   func(ctx context.Context, addr string) context.Context
	AcceptHook func(ctx context.Context) context.Context
	ListenHook func(ctx context.Context, addr string) context.Context
}

type AfterHookFunc func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error)

type AfterHook struct {
	Priority   uint
	DialerHook func(ctx context.Context, c net.Conn, addr string) (context.Context, net.Conn, error)
	AcceptHook func(ctx context.Context, c net.Conn) (context.Context, net.Conn, error)
	ListenHook func(ctx context.Context, listener net.Listener) (context.Context, net.Listener, error)
}
