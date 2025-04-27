package socks

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/socks5"
	"github.com/chainreactors/rem/x/utils"
)

func init() {
	core.OutboundRegister(core.Socks5Serve, NewSocks5Outbound)
	core.InboundRegister(core.Socks5Serve, NewSocksInbound)
	core.OutboundRegister(core.RawServe, NewRelaySocks5Outbound)
}

func NewRelaySocks5Outbound(params map[string]string, dial core.ContextDialer) (core.Outbound, error) {
	server, err := NewRelaySocksServer()
	if err != nil {
		return nil, err
	}
	if dial != nil {
		server.Config.Dial = dial.DialContext
	}
	return &Socks5Plugin{
		Server: server,
	}, nil
}

func NewSocks5Server(username, password string, dial core.ContextDialer) (*socks5.Server, error) {
	var auth []socks5.Authenticator
	if username != "" {
		cred := socks5.StaticCredentials{username: password}
		auth = []socks5.Authenticator{socks5.UserPassAuthenticator{Credentials: cred}}
	} else {
		auth = []socks5.Authenticator{socks5.NoAuthAuthenticator{}}
	}

	conf := &socks5.Config{
		AuthMethods: auth,
		Logger:      log.New(ioutil.Discard, "", log.LstdFlags),
	}
	if dial != nil {
		conf.Dial = dial.DialContext
	}
	return socks5.New(conf)
}

func NewRelaySocksServer() (*socks5.Server, error) {
	conf := &socks5.Config{
		AuthMethods: []socks5.Authenticator{socks5.RelayAuthenticator{}},
		Logger:      log.New(ioutil.Discard, "", log.LstdFlags),
	}
	utils.Log.Importantf("[agent.outbound] relay serving")
	return socks5.New(conf)
}

func NewSocksInbound(params map[string]string) (core.Inbound, error) {
	var user, pwd string
	if params != nil {
		user = params["username"]
		pwd = params["password"]
	}
	server, err := NewSocks5Server(user, pwd, nil)
	if err != nil {
		return nil, err
	}
	base := core.NewPluginOption(params, core.InboundPlugin, core.Socks5Serve)
	utils.Log.Importantf("[agent.inbound] Socks5 serving: %s , %s", base.String(), base.URL())
	return &Socks5Plugin{Server: server, PluginOption: base}, nil
}

type Socks5Plugin struct {
	Server *socks5.Server
	*core.PluginOption
}

func NewSocks5Outbound(params map[string]string, dialer core.ContextDialer) (core.Outbound, error) {
	var user, passwd string
	if params != nil {
		user = params["username"]
		passwd = params["password"]
	}

	server, err := NewSocks5Server(user, passwd, dialer)
	if err != nil {
		return nil, err
	}

	base := core.NewPluginOption(params, core.OutboundPlugin, core.Socks5Serve)
	utils.Log.Importantf("[agent.outbound] Socks5 serving: %s , %s", base.String(), base.URL())
	return &Socks5Plugin{
		Server:       server,
		PluginOption: base,
	}, nil
}

func (sp *Socks5Plugin) Handle(conn io.ReadWriteCloser, realConn net.Conn) (net.Conn, error) {
	wrapConn := cio.WrapConn(realConn, conn)

	ctx := context.Background()
	req, err := sp.Server.ParseConn(ctx, wrapConn)
	if err != nil {
		return nil, err
	}

	// Switch on the command
	switch req.Command {
	case socks5.ConnectCommand:
		target, err := sp.Server.Dial(ctx, req, wrapConn)
		if err != nil {
			return nil, err
		}
		local := target.LocalAddr().(*net.TCPAddr)
		bind := socks5.AddrSpec{IP: local.IP, Port: local.Port}

		if err := sp.Server.SendReply(conn, socks5.SuccessReply, &bind); err != nil {
			return nil, fmt.Errorf("Failed to send reply: %v", err)
		}

		return target, nil
	//case BindCommand:
	//	return s.handleBind(ctx, conn, req)
	//case AssociateCommand:
	//	return s.handleAssociate(ctx, conn, req)
	default:
		if err := sp.Server.SendReply(conn, socks5.CommandNotSupported, nil); err != nil {
			return nil, fmt.Errorf("Failed to send reply: %v", err)
		}
		return nil, fmt.Errorf("Unsupported command: %v", req.Command)
	}
}

func (sp *Socks5Plugin) Relay(conn net.Conn, bridge io.ReadWriteCloser) (net.Conn, error) {
	req, err := sp.Server.ParseConn(context.Background(), conn)
	if err != nil {
		return nil, err
	}
	_, err = bridge.Write(req.BuildRelay())
	if err != nil {
		return nil, err
	}

	rep, addr, _ := socks5.ReadReply(bridge)
	replyData, err := socks5.BuildReply(rep, addr)
	if err != nil {
		return nil, err
	}
	_, err = conn.Write(replyData)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

func (sp *Socks5Plugin) Name() string {
	return core.Socks5Serve
}
