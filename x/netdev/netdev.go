//go:build tinygo || wasip1

package netdev

import (
	"net/netip"
	"time"
	_ "unsafe"
)

// Netdever mirrors tinygo's net.netdever interface.
// Method signatures must match exactly for go:linkname registration to work.
type Netdever interface {
	GetHostByName(name string) (netip.Addr, error)
	Addr() (netip.Addr, error)
	Socket(domain int, stype int, protocol int) (int, error)
	Bind(sockfd int, ip netip.AddrPort) error
	Connect(sockfd int, host string, ip netip.AddrPort) error
	Listen(sockfd int, backlog int) error
	Accept(sockfd int) (int, netip.AddrPort, error)
	Send(sockfd int, buf []byte, flags int, deadline time.Time) (int, error)
	Recv(sockfd int, buf []byte, flags int, deadline time.Time) (int, error)
	Close(sockfd int) error
	SetSockOpt(sockfd int, level int, opt int, value interface{}) error
}

//go:linkname useNetdev net.useNetdev
func useNetdev(dev Netdever)

// UseNetdev registers a Netdever with TinyGo's net package.
func UseNetdev(dev Netdever) {
	useNetdev(dev)
}
