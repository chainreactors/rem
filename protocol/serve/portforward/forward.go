package portforward

import (
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	core.InboundRegister(core.PortForwardServe, NewForwardInbound)
	core.OutboundRegister(core.PortForwardServe, NewForwardOutbound)
}

func NewForwardInbound(params map[string]string) (core.Inbound, error) {
	dest, ok := params["dest"]
	if !ok {
		return nil, fmt.Errorf("dest not found")
	}
	relay, err := newRelayRequest(dest)
	if err != nil {
		return nil, err
	}
	utils.Log.Importantf("[agent.inbound] portforward serving: %s -> %s", params["src"], dest)
	return &ForwardPlugin{
		Dest: relay,
	}, nil
}

func NewForwardOutbound(params map[string]string, dial core.ContextDialer) (core.Outbound, error) {
	dest, ok := params["dest"]
	if !ok {
		return nil, fmt.Errorf("dest not found")
	}
	relay, err := newRelayRequest(dest)
	if err != nil {
		return nil, err
	}
	utils.Log.Importantf("[agent.outbound] portforward serving: %s -> %s", dest, params["src"])
	return &ForwardPlugin{
		Dest: relay,
		dial: dial,
	}, nil
}

func newRelayRequest(dest string) (*socks5.RelayRequest, error) {
	host, portText, err := net.SplitHostPort(dest)
	if err != nil {
		return nil, fmt.Errorf("invalid dest %q: %w", dest, err)
	}

	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, fmt.Errorf("invalid dest port %q: %w", dest, err)
	}

	addr := &socks5.AddrSpec{Port: port}
	if ip := net.ParseIP(host); ip != nil {
		addr.IP = ip
	} else {
		addr.FQDN = host
	}

	return &socks5.RelayRequest{DestAddr: addr}, nil
}

type ForwardPlugin struct {
	Dest *socks5.RelayRequest
	dial core.ContextDialer
}

func (plug *ForwardPlugin) Handle(conn io.ReadWriteCloser, realConn net.Conn) (net.Conn, error) {
	if plug.dial == nil {
		return nil, fmt.Errorf("dialer not configured")
	}
	remote, err := plug.dial.Dial("tcp", plug.Dest.DestAddr.Address())
	if err != nil {
		return nil, err
	}
	return remote, nil
}

func (plug *ForwardPlugin) Relay(conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error) {
	_, err := bridge.Write(plug.Dest.BuildRelay())
	if err != nil {
		return nil, err
	}

	_, _, err = socks5.ReadReply(bridge)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func (plug *ForwardPlugin) Name() string {
	return core.PortForwardServe
}

func (plug *ForwardPlugin) ToClash() *utils.Proxies {
	return nil
}
