package raw

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
	core.InboundRegister(core.RawServe, NewRawInbound)
}

func NewRawInbound(params map[string]string) (core.Inbound, error) {
	dest, ok := params["dest"]
	if !ok {
		return nil, fmt.Errorf("dest not found")
	}
	relay, err := newRelayRequest(dest)
	if err != nil {
		return nil, err
	}
	base := core.NewPluginOption(params, core.InboundPlugin, core.RawServe)
	utils.Log.Importantf("[agent.inbound] raw serving: %s -> %s", base.String(), dest)
	return &RawPlugin{
		PluginOption: base,
		Dest:         relay,
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

type RawPlugin struct {
	*core.PluginOption
	Dest *socks5.RelayRequest
}

func (r *RawPlugin) Name() string {
	return core.RawServe
}

func (r *RawPlugin) ToClash() *utils.Proxies {
	return nil
}

func (r *RawPlugin) Relay(conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error) {
	_, err := bridge.Write(r.Dest.BuildRelay())
	if err != nil {
		return nil, err
	}

	_, _, err = socks5.ReadReply(bridge)
	if err != nil {
		return nil, err
	}

	return conn, nil
}
