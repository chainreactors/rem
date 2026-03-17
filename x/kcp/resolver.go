package kcp

import (
	"github.com/chainreactors/rem/x/simplex"
	"net"
	"strings"

	"github.com/pkg/errors"
)

// Resolver 定义了地址解析的接口
type Resolver interface {
	// ResolveAddr 解析地址为net.Addr
	ResolveAddr(network, address string) (net.Addr, error)
}

// resolver工厂函数，根据网络类型返回对应的resolver
func newResolver(network string) Resolver {
	switch {
	case strings.HasPrefix(network, "udp"):
		return &udpResolver{}
	case strings.HasPrefix(network, "ip"):
		return &icmpResolver{}
	default:
		return &simplexResolver{} // 默认使用UDP
	}
}

// UDP resolver实现
type udpResolver struct{}

func (r *udpResolver) ResolveAddr(network, address string) (net.Addr, error) {
	return net.ResolveUDPAddr(network, address)
}

// ICMP resolver实现
type icmpResolver struct{}

func (r *icmpResolver) ResolveAddr(network, address string) (net.Addr, error) {
	host, err := splitHostOnly(address)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	// ICMP只需要IP地址
	return net.ResolveIPAddr("ip", host)
}

// HTTP resolver实现
type simplexResolver struct{}

func (r *simplexResolver) ResolveAddr(network, address string) (net.Addr, error) {
	return simplex.ResolveSimplexAddr(network, address)
}

// 辅助函数:只返回host部分
func splitHostOnly(address string) (string, error) {
	host, _, err := net.SplitHostPort(address)
	return host, err
}
