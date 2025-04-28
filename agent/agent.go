package agent

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
	"google.golang.org/protobuf/proto"
)

var Agents = &agents{
	Map: &sync.Map{},
}

type agents struct {
	*sync.Map
}

func (as agents) Add(agent *Agent) {
	as.Store(agent.ID, agent)
}

func (as agents) Get(id string) (*Agent, bool) {
	if v, ok := as.Load(id); ok {
		return v.(*Agent), ok
	}
	return nil, false
}

func (as agents) Exist(id string) bool {
	_, ok := as.Load(id)
	return ok
}

func (as agents) Send(id string, msg proto.Message) error {
	if agent, ok := as.Get(id); ok {
		return agent.sendCh.Send(0, msg)
	}
	return ErrNotFoundAgent
}

func NewAgent(cfg *Config) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())
	agent := &Agent{
		Config:    cfg,
		ctx:       ctx,
		canceler:  cancel,
		sendCh:    cio.NewChan("SendCh", 1024),
		receiveCh: cio.NewChan("ReceiveCh", 1024),
		log:       utils.NewRingLogWriter(256),
	}
	if cfg.Alias != "" {
		agent.ID = cfg.Alias
	} else {
		agent.ID = utils.RandomString(8)
	}

	if Agents.Exist(agent.ID) {
		return nil, fmt.Errorf("agent %s already exist", agent.ID)
	}

	return agent, nil
}

type Agent struct {
	*Config
	ID       string
	Closed   bool
	Outbound core.Outbound
	Inbound  core.Inbound
	Conn     net.Conn
	Init     bool

	connCount int
	connIndex uint64
	receiveCh *cio.Channel // 接受msg的信道
	sendCh    *cio.Channel // 发送msg的信道
	ctx       context.Context
	canceler  context.CancelFunc
	client    proxyclient.Dial
	bridgeMap sync.Map
	listener  net.Listener
	log       *utils.RingLogWriter
}

func (agent *Agent) Name() string {
	return agent.ID
}

func (agent *Agent) Send(msg proto.Message) error {
	return agent.sendCh.Send(0, msg)
}

func (agent *Agent) Dial(remote, local *core.URL) (err error) {
	agent.URLs.RemoteURL = remote
	agent.URLs.LocalURL = local
	control := &message.Control{
		Mod:         agent.Mod,
		Remote:      agent.LocalURL.String(),
		Local:       agent.RemoteURL.String(),
		Source:      agent.ID,
		Destination: agent.ID,
		Options:     agent.Params,
	}
	if agent.Redirect != "" {
		control.Destination = agent.Redirect
	}
	ack, err := cio.WriteAndAssertMsg(agent.Conn, control)
	if err != nil {
		return err
	}
	agent.Controller = control
	if ack.Port != 0 {
		agent.RemoteURL.SetPort(int(ack.Port))
	}

	return
}

func (agent *Agent) Serve(control *message.Control) error {
	// 开始监听
	for !agent.Closed {
		remote, err := agent.Accept()
		if err != nil {
			return err
		}
		bridge, err := NewBridgeWithConn(agent, remote, control)
		if err != nil {
			agent.Log("bridge.failed", logs.ErrorLevel, err.Error())
			continue
		}
		agent.Log("bridge.new", logs.DebugLevel, "bridge %d created", bridge.id)
		agent.saveBridge(bridge)
	}
	return nil
}

func (agent *Agent) Listen(dst *core.URL) error {
	ctx := context.WithValue(context.Background(), "meta", make(core.Metas))

	tun, err := core.ListenerCreate(dst.Tunnel, ctx)
	if err != nil {
		return err
	}
	agent.listener, err = tun.Listen(dst.String())
	if err != nil {
		return err
	}
	return nil
}

func (agent *Agent) ListenAndServe(dst *core.URL, control *message.Control) error {
	err := agent.Listen(dst)
	if err != nil {
		return err
	}
	agent.Log("listen", logs.DebugLevel, "%s", dst.String())
	go func() {
		err := agent.Serve(control)
		if err != nil {
			agent.Close(fmt.Errorf("%s accept error, %w", agent.Type, err))
			return
		}
	}()
	return nil
}

func (agent *Agent) Accept() (net.Conn, error) {
	conn, err := agent.listener.Accept()
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (agent *Agent) Login(conn net.Conn) error {
	// 向console 请求登录
	token, err := utils.AesEncrypt(agent.AuthKey, utils.PKCS7Padding(agent.AuthKey, 16))
	if err != nil {
		return err
	}

	_, err = cio.WriteAndAssertMsg(conn, &message.Login{
		Agent:        agent.ID,
		ConsoleProto: agent.ConsoleURL.Scheme,
		ConsoleIP:    agent.ConsoleURL.Hostname(),
		ConsolePort:  agent.ConsoleURL.IntPort(),
		Mod:          agent.Mod,
		Token:        hex.EncodeToString(token),
		Interfaces:   agent.Interfaces,
		Hostname:     agent.Hostname,
		Username:     agent.Username,
	})
	if err != nil {
		return err
	}

	agent.Conn = conn
	return nil
}

func (agent *Agent) Handler() error {
	var err error
	agent.client, err = proxyclient.NewClientChain(agent.Proxies) // client <-[proxies]-> target
	if err != nil {
		return err
	}
	// 创建中转agent
	if !agent.isDestination(agent.Controller.Destination) && agent.Type == core.SERVER {
		agent.Type = core.Redirect
		des, ok := Agents.Get(agent.Redirect)
		if ok {
			des.sendCh.Send(0, agent.Controller)
		} else {
			agent.Log("redirect", logs.ErrorLevel, "%s not found", agent.Redirect)
		}
	}
	agent.Log("login", logs.DebugLevel, "login SUCCESSFULLY, %s connect ESTABLISHED, id: %s, des: %s", agent.ConsoleURL.Scheme, agent.Name(), agent.Redirect)
	// 挂起sender与receiver, 用来与两端交换数据. 任何一端报错都会退出并重建连接.
	go func() {
		err := agent.sendCh.Sender(agent.Conn)
		if err != nil {
			agent.Close(fmt.Errorf("sender error, %w", err))
		}
	}()
	go func() {
		err := agent.receiveCh.Receiver(agent.Conn)
		if err != nil {
			agent.Close(fmt.Errorf("receiver error, %w", err))
		}
	}()

	go agent.monitor()

	if agent.Type != core.Redirect {
		err = agent.handlerControl(agent.Controller)
	}
	if err != nil {
		return err
	}
	agent.Init = true
	return agent.handleMessage()
}

func (agent *Agent) Fork(ctrl *message.Control) (*Agent, error) {
	cfg := agent.Config.Clone(ctrl)
	ctx, cancel := context.WithCancel(agent.ctx)
	a := &Agent{
		Config:    cfg,
		ID:        cfg.Alias,
		ctx:       ctx,
		canceler:  cancel,
		Conn:      agent.Conn,
		client:    agent.client,
		sendCh:    agent.sendCh,
		receiveCh: cio.NewChan("ReceiveCh", 1024),
		log:       agent.log,
	}

	err := a.handlerControl(ctrl)
	if err != nil {
		return nil, err
	}

	go a.monitor()
	go func() {
		err := a.handleMessage()
		if err != nil {
			return
		}
	}()
	return a, nil
}

// 需要监听端口的那端
func (agent *Agent) handlerInbound(local, remote *core.URL, control *message.Control) error {
	err := agent.ListenAndServe(local, control)
	if err != nil {
		return err
	}
	if local.Scheme == "" {
		return fmt.Errorf("relay is nil")
	}

	options := utils.MergeMaps(control.Options, local.Options())
	options["server"] = agent.ExternalIP
	if local.Scheme == core.PortForwardServe {
		options["src"] = local.Host
		options["dest"] = remote.Host
	}

	if local.Tunnel == core.MemoryTunnel {
		options["type"] = "memory"
		options["port"] = ""
		options["server"] = "memory"
	}
	instant, err := core.InboundCreate(local.Scheme, options)
	if err != nil {
		agent.Log("inbound.create", logs.ErrorLevel, "%s", err.Error())
		return err
	}
	agent.Inbound = instant
	return nil
}

func (agent *Agent) handlerOutbound(local, remote *core.URL, control *message.Control) error {
	var instant core.Outbound
	options := utils.MergeMaps(control.Options, local.Options())
	switch local.Scheme {
	case core.PortForwardServe, core.CobaltStrikeServe:
		options["src"] = remote.Host
		options["dest"] = local.Host
	}

	instant, err := core.OutboundCreate(local.Scheme, options, core.NewProxyDialer(agent.client))
	if err != nil {
		agent.Log("outbound.create", logs.ErrorLevel, "%s", err.Error())
		return err
	}
	agent.Outbound = instant

	return nil
}

func (agent *Agent) handlerControl(control *message.Control) error {
	if control.Mod == core.Connect {
		return nil
	}
	isAccept := agent.isAccept(control)
	local := control.LocalURL()
	remote := control.RemoteURL()

	// 根据模式和Accept状态选择处理方式
	var err error
	if control.Mod == core.Reverse {
		if isAccept {
			err = agent.handlerInbound(local, remote, control)
		} else {
			err = agent.handlerOutbound(remote, local, control)
		}
	} else if control.Mod == core.Proxy {
		if isAccept {
			err = agent.handlerOutbound(local, remote, control)
		} else {
			err = agent.handlerInbound(remote, local, control)
		}
	} else {
		return fmt.Errorf("unsupported mod: %s", control.Mod)
	}

	if agent.Type == core.SERVER && agent.Mod == core.Proxy {
		inboundURL := remote.Copy()
		inboundURL.SetHostname(agent.Hostname)
		logs.Log.Importantf("[agent.inbound] remote inbound serbing: %s", inboundURL.String())
	}

	if err != nil {
		return fmt.Errorf("handler failed: %w", err)
	}
	return nil
}

func (agent *Agent) handleMessage() error {
	for {
		select {
		case msg, ok := <-agent.receiveCh.C:
			if !ok {
				return fmt.Errorf("receiver Closed")
			}
			switch m := msg.Message.(type) {
			case *message.Pong:
			case *message.Ping:
				pmsg := &message.Pong{
					Pong: "pong",
				}
				err := agent.sendCh.Send(0, pmsg)
				if err != nil {
					return err
				}
			case *message.ConnStart:
				bridge, err := NewBridgeWithMsg(agent, m)
				if err != nil {
					bridge.Log("start", logs.DebugLevel, "connect failed: %s", err.Error())
					return err
				}
				agent.saveBridge(bridge)
			case *message.ConnEnd:
				if bridge, err := agent.getBridge(m.ID); err == nil {
					go func() {
						if !bridge.closed {
							bridge.Log("end", logs.DebugLevel, "bridge %d closed by remote: %s", m.ID, m.Msg)
							bridge.Close()
						}
						agent.bridgeMap.Delete(bridge.id)
					}()
				} else {
					agent.Log("end", logs.DebugLevel, "bridge %d not found", m.ID)
				}
			case *message.Packet:
				if bridge, err := agent.getBridge(m.ID); err == nil {
					select {
					case <-agent.ctx.Done():
						break
					default:
						if _, err := bridge.buf.Write(m.Data); err != nil {
							bridge.Log("write", logs.DebugLevel, "bridge %d write failed: %v", m.ID, err)
						} else {
							bridge.recvSum += int64(len(m.Data))
						}
					}
				} else {
					agent.Log("write", logs.DebugLevel, "bridge %d not found", m.ID)
				}

			case *message.Redirect:
				if m.Destination == m.Source {
					if des, ok := Agents.Get(m.Destination); ok {
						des.receiveCh.Send(0, message.Unwrap(m))
					} else {
						agent.Log("redirect", logs.ErrorLevel, "%s not found", m.Destination)
					}
				} else if m.Destination == agent.ID {
					agent.receiveCh.C <- &cio.Message{Message: message.Unwrap(m)}
					continue
				} else {
					if des, ok := Agents.Get(m.Destination); ok {
						des.sendCh.Send(0, message.Unwrap(m))
					} else {
						agent.Log("redirect", logs.ErrorLevel, "%s not found", m.Destination)
					}
				}

			case *message.Control:
				if m.Fork {
					a, err := agent.Fork(m)
					if err != nil {
						agent.Log("failed", logs.ErrorLevel, "%s", err.Error())
					}
					Agents.Add(a)
				} else {
					err := agent.handlerControl(m)
					if err != nil {
						agent.Log("failed", logs.ErrorLevel, "%s", err.Error())
					}
				}

			default:
				agent.Log("message", logs.DebugLevel, "error msg type")
			}
		}
	}
}

func (agent *Agent) saveBridge(bridge *Bridge) {
	agent.connCount++
	agent.bridgeMap.Store(bridge.id, bridge)
	bridge.Handler(agent)
}

func (agent *Agent) getBridge(id uint64) (*Bridge, error) {
	if v, ok := agent.bridgeMap.Load(id); ok {
		return v.(*Bridge), nil
	} else {
		return nil, ErrNotFoundBridge
	}

}

// 定期输出agent状态
func (agent *Agent) monitor() {
	for !agent.Closed {
		select {
		case <-time.After(monitorInterval * time.Second):
			agent.Log("monitor", logs.DebugLevel, "connections: %d/%d, sender: %s, receiver: %s",
				agent.connCount, agent.connIndex,
				agent.sendCh.GetStats(),
				agent.receiveCh.GetStats(),
			)
		}
	}
}

func (agent *Agent) Close(err error) {
	if agent.Closed {
		return
	}
	agent.Closed = true
	agent.Log("exit", logs.ImportantLevel, "%s: %s", agent.ID, err.Error())
	agent.canceler()
	agent.sendCh.Close()
	agent.receiveCh.Close()
	if agent.listener != nil {
		agent.listener.Close()
	}
	if agent.Conn != nil {
		agent.Conn.Close()
	}
}

func (agent *Agent) Log(part string, level logs.Level, msg string, s ...interface{}) {
	utils.Log.FLogf(agent.log, level, "[%s.%s.%s] %s", agent.Type, agent.ID, part, fmt.Sprintf(msg, s...))
}

func (agent *Agent) Metrics() string {
	return fmt.Sprintf("connections: %d, sender: %s, receiver: %s",
		agent.connCount,
		agent.sendCh.GetStats(),
		agent.receiveCh.GetStats(),
	)
}

func (agent *Agent) HistoryLog() string {
	return agent.log.String()
}

func (agent *Agent) isDestination(redirect string) bool {
	if redirect == "" {
		return true
	}

	if redirect == agent.ID {
		return true
	}
	return false
}

func (agent *Agent) isAccept(control *message.Control) bool {
	if agent.Type == core.CLIENT {
		if agent.ID == control.Source {
			// 当source和id对应的时候, 才是主动发起方
			return false
		} else {
			return true
		}
	} else {
		return true
	}
}
