package trojan

import (
	"crypto/tls"
	log "github.com/sirupsen/logrus"
	"io"
	"net"
	"os"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/trojanx"
	"github.com/chainreactors/rem/x/trojanx/protocol"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	core.InboundRegister(core.TrojanServe, NewTrojanInbound)
	core.OutboundRegister(core.TrojanServe, NewTrojanOutbound)
}

func NewTrojanOutbound(options map[string]string, dial core.ContextDialer) (core.Outbound, error) {
	pwd := options["password"]
	config := &trojanx.TrojanConfig{
		Password: pwd,
	}
	server := trojanx.NewServer(
		trojanx.WithConfig(config),
		trojanx.WithLogger(&log.Logger{
			Level:        log.WarnLevel,
			ReportCaller: true,
			Out:          os.Stdout,
		}),
		trojanx.WithDial(dial.DialContext),
	)
	base := core.NewPluginOption(options, core.OutboundPlugin, core.TrojanServe)
	utils.Log.Importantf("[agent.outbound] trojan serving: %s, %s", base.String(), base.URL())
	return &TrojanPlugin{Server: server, PluginOption: base}, nil
}

func NewTrojanInbound(options map[string]string) (core.Inbound, error) {
	pwd := options["password"]
	config := &trojanx.TrojanConfig{
		Password: pwd,
	}
	server := trojanx.NewServer(
		trojanx.WithConfig(config),
		trojanx.WithLogger(&log.Logger{
			Level:        log.WarnLevel,
			ReportCaller: true,
			Out:          os.Stdout,
		}),
	)
	base := core.NewPluginOption(options, core.InboundPlugin, core.TrojanServe)
	utils.Log.Importantf("[agent.inbound] trojan serving: %s , %s", base.String(), base.URL())
	return &TrojanPlugin{Server: server, PluginOption: base}, nil
}

type TrojanPlugin struct {
	Server *trojanx.Server
	*core.PluginOption
}

func (plug *TrojanPlugin) genSelfSign() []tls.Certificate {
	signed, _ := generateSelfSigned()
	var tlsCertificates []tls.Certificate
	tlsCertificates = append(tlsCertificates, signed)
	return tlsCertificates
}

func (plug *TrojanPlugin) Handle(conn io.ReadWriteCloser, realConn net.Conn) (net.Conn, error) {
	defer conn.Close()
	wrapConn := cio.WrapConn(realConn, conn)
	server := tls.Server(wrapConn, &tls.Config{
		Certificates: plug.genSelfSign(),
	})
	plug.Server.ServeConn(server)
	return nil, nil
}

func (plug *TrojanPlugin) Relay(conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error) {
	signed, _ := generateSelfSigned()
	var tlsCertificates []tls.Certificate
	tlsCertificates = append(tlsCertificates, signed)
	tlsConn := tls.Server(conn, &tls.Config{
		Certificates: tlsCertificates,
	})
	req, err := plug.Server.ParseRequest(tlsConn)
	if err != nil {
		return nil, err
	}

	dest := &socks5.AddrSpec{Port: req.DescriptionPort}
	switch req.AddressType {
	case protocol.AddressTypeIPv6, protocol.AddressTypeIPv4:
		dest.IP = net.ParseIP(req.DescriptionAddress)
	case protocol.AddressTypeDomain:
		dest.FQDN = req.DescriptionAddress
	}
	relayReq := &socks5.RelayRequest{
		DestAddr: dest,
	}

	_, err = bridge.Write(relayReq.BuildRelay())
	if err != nil {
		return nil, err
	}

	_, _, err = socks5.ReadReply(bridge)
	if err != nil {
		return nil, err
	}

	return tlsConn, nil
}

func (plug *TrojanPlugin) Name() string {
	return core.TrojanServe
}
