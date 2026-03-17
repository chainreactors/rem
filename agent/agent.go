package agent

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
	"github.com/chainreactors/rem/x/yamux"
)

var Agents = &agents{
	Map: &sync.Map{},
}

type agents struct {
	*sync.Map
}

func (agent *Agent) SafeGoWithRestart(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				agent.Log("panic", logs.ErrorLevel, "critical goroutine %s panic: %v\n%s", name, r, debug.Stack())
				agent.Close(fmt.Errorf("panic in %s: %v", name, r))
				if agent.Recover != nil {
					agent.Log("restart", logs.ImportantLevel, "triggering agent restart due to panic in %s", name)
					if err := agent.Recover(); err != nil {
						agent.Log("recover", logs.ErrorLevel, "failed to recover: %v", err)
					}
				}
			}
		}()
		fn()
	}()
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

func (as agents) Send(id string, msg message.Message) error {
	if agent, ok := as.Get(id); ok {
		return agent.Send(msg)
	}
	return ErrNotFoundAgent
}

func NewAgent(cfg *Config) (*Agent, error) {
	ctx, cancel := context.WithCancel(context.Background())
	agent := &Agent{
		Config:   cfg,
		ctx:      ctx,
		canceler: cancel,
		log:      utils.NewRingLogWriter(256),
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
	ID            string
	Closed        bool
	Outbound      core.Outbound
	Inbound       core.Inbound
	Conn          net.Conn
	Init          bool
	Recover       func() error
	TransportAddr net.Addr // underlying transport address (e.g. *SimplexAddr), used for dynamic reconfiguration

	connCount      int64
	connIndex      uint64
	sharedBridgeID *uint64 // non-nil for forked agents; points to parent's connIndex
	connIDSeq      atomic.Uint64
	sessionConnID  string
	session        *yamux.Session // yamux multiplexer over Conn
	stream0        net.Conn       // tinygo poll mode uses stream0 directly
	pendingStreams sync.Map       // map[uint64]net.Conn — data streams waiting for BridgeOpen
	ctx            context.Context
	canceler       context.CancelFunc
	client         core.ContextDialer
	bridgeMap      sync.Map
	listener       net.Listener
	log            *utils.RingLogWriter

	connHub  *ConnHub
	bgOnce   sync.Once
	parent   *Agent   // non-nil for forked agents; used to check parent's pendingStreams
	children sync.Map // map[string]*Agent — forked child agents (for message dispatch)
}

func (agent *Agent) Name() string {
	return agent.ID
}

func (agent *Agent) nextConnID(label string) string {
	if label == "" {
		label = "conn"
	}
	index := agent.connIDSeq.Add(1) - 1
	return fmt.Sprintf("%s-%d", label, index)
}

func (agent *Agent) Send(msg message.Message) error {
	if agent.connHub == nil {
		return fmt.Errorf("connhub not initialized")
	}
	return agent.connHub.SendControl(msg)
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
	agent.Conn.SetReadDeadline(time.Now().Add(LoginTimeout))
	ack, err := cio.WriteAndAssertMsg(agent.Conn, control)
	agent.Conn.SetReadDeadline(time.Time{})
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
	agent.SafeGoWithRestart("serve", func() {
		err := agent.Serve(control)
		if err != nil {
			agent.Close(fmt.Errorf("%s accept error, %w", agent.Type, err))
			return
		}
	})
	return nil
}

func (agent *Agent) Accept() (net.Conn, error) {
	conn, err := agent.listener.Accept()
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// LoginTimeout limits how long Login() / Dial() waits for the server's Ack.
// On polling-based transports (OneDrive, SharePoint) the server may need
// several poll cycles to discover the new client.  Keep this short-ish so
// the caller's retry loop re-dials quickly with a fresh transport instance.
var LoginTimeout = 60 * time.Second

func (agent *Agent) Login(conn net.Conn) error {
	// 向console 请求登录
	token, err := buildAuthToken(agent.AuthKey)
	if err != nil {
		return err
	}

	// Set a read deadline so Login doesn't block forever when the server
	// hasn't discovered this client yet (common on polling transports).
	conn.SetReadDeadline(time.Now().Add(LoginTimeout))

	_, err = cio.WriteAndAssertMsg(conn, &message.Login{
		Agent:        agent.ID,
		ConsoleProto: agent.ConsoleURL.Scheme,
		ConsoleIP:    agent.ConsoleURL.Hostname(),
		ConsolePort:  agent.ConsoleURL.IntPort(),
		Mod:          agent.Mod,
		Token:        token,
		Interfaces:   agent.Interfaces,
		Hostname:     agent.Hostname,
		Username:     agent.Username,
	})
	if err != nil {
		conn.SetReadDeadline(time.Time{}) // clear deadline on error path too
		return err
	}

	// Clear deadline for subsequent I/O (yamux, keepalive, etc.)
	conn.SetReadDeadline(time.Time{})
	agent.Conn = conn
	return nil
}

func (agent *Agent) Handler() error {
	if err := agent.HandlerInit(); err != nil {
		return err
	}
	agent.StartBackgroundLoops()
	return agent.handleMessage()
}

// StartBackgroundLoops starts the monitor and stream acceptor goroutines.
// Safe to call multiple times (uses sync.Once internally).
func (agent *Agent) StartBackgroundLoops() {
	agent.bgOnce.Do(func() {
		go agent.monitor()

		// Accept incoming yamux streams (bridge data streams from peer)
		agent.SafeGoWithRestart("streamAccept", func() {
			agent.acceptStreams()
		})
	})
}

// HandlerInit performs synchronous initialization without entering the blocking
// message loop.
func (agent *Agent) HandlerInit() error {
	if agent.Init {
		return nil
	}
	err := agent.initProxyClient()
	if err != nil {
		return err
	}

	// Create yamux session over the already-encrypted Conn
	if err := agent.initYamux(); err != nil {
		return err
	}

	agent.routeControl(agent.Controller)
	agent.Log("login", logs.DebugLevel, "login SUCCESSFULLY, %s connect ESTABLISHED, id: %s, des: %s", agent.ConsoleURL.Scheme, agent.Name(), agent.Redirect)

	if agent.Type != core.Redirect {
		err = agent.handlerControl(agent.Controller)
	}
	if err != nil {
		agent.Close(err)
		return err
	}
	agent.Init = true
	return nil
}

// HandleMessages runs the blocking message loop. Call after HandlerInit().
func (agent *Agent) HandleMessages() error {
	return agent.handleMessage()
}

func (agent *Agent) forwardControl(control *message.Control, destination string) {
	if destination == "" {
		agent.Log("redirect", logs.ErrorLevel, "empty destination for control source=%s", control.Source)
		return
	}
	if destination == agent.ID {
		agent.Log("redirect", logs.ErrorLevel, "reject self-forward loop for %s", destination)
		return
	}
	if des, ok := Agents.Get(destination); ok {
		if err := des.Send(control); err == nil {
			agent.Log("redirect", logs.DebugLevel, "forward control source=%s to %s", control.Source, destination)
			return
		} else {
			agent.Log("redirect", logs.ErrorLevel, "forward control source=%s to %s failed: %v", control.Source, destination, err)
		}
		return
	}
	agent.Log("redirect", logs.ErrorLevel, "%s not found", destination)
}

func (agent *Agent) routeControl(control *message.Control) bool {
	if control == nil {
		return false
	}
	if agent.Type == core.CLIENT {
		return false
	}
	if agent.isDestination(control.Destination) {
		return false
	}
	agent.Type = core.Redirect
	agent.forwardControl(control, control.Destination)
	return true
}

func (agent *Agent) Fork(ctrl *message.Control) (*Agent, error) {
	cfg := agent.Config.Clone(ctrl)
	ctx, cancel := context.WithCancel(agent.ctx)
	a := &Agent{
		Config:  cfg,
		ID:      cfg.Alias,
		Conn:    agent.Conn,
		Recover: agent.Recover,

		ctx:            ctx,
		canceler:       cancel,
		client:         agent.client,
		session:        agent.session, // share parent's yamux session
		log:            agent.log,
		connHub:        agent.connHub,
		parent:         agent,
		sharedBridgeID: &agent.connIndex, // share parent's bridge ID counter
	}

	err := a.handlerControl(ctrl)
	if err != nil {
		return nil, err
	}

	go a.monitor()
	a.Init = true

	// Register child in parent for message dispatch.
	// The parent's handleMessage() routes BridgeOpen/BridgeClose to children
	// instead of each child running its own handleMessage() on the shared controlInbox.
	agent.children.Store(a.ID, a)

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
	if local.Scheme == core.PortForwardServe || local.Scheme == core.RawServe {
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

	instant, err := core.OutboundCreate(local.Scheme, options, agent.client)
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
	if agent.connHub == nil {
		return fmt.Errorf("connhub not initialized")
	}
	inbox := agent.connHub.ControlInbox()
	errs := agent.connHub.ControlErrors()

	keepaliveTicker := time.NewTicker(KeepaliveInterval)
	defer keepaliveTicker.Stop()
	missedPongs := 0 // consecutive pings without pong reply

	for {
		var msg message.Message
		select {
		case <-agent.ctx.Done():
			if agent.Closed {
				return nil
			}
			return fmt.Errorf("agent stopped")
		case err := <-errs:
			if err == nil {
				err = fmt.Errorf("all control streams closed")
			}
			if agent.Closed {
				return nil
			}
			return fmt.Errorf("all control streams closed: %w", err)
		case <-keepaliveTicker.C:
			// Only CLIENT performs keepalive checking; server only responds to pings.
			if agent.Type != core.CLIENT {
				continue
			}
			missedPongs++
			if missedPongs >= KeepaliveMaxMissed {
				return fmt.Errorf("keepalive timeout: %d consecutive pings unanswered", missedPongs)
			}
			// Send ping in a goroutine so a blocked yamux write cannot
			// stall the select loop — the counter keeps incrementing
			// and will eventually kill the agent even if Send hangs.
			go func() {
				if err := agent.Send(&message.Ping{Ping: "keepalive"}); err != nil {
					agent.Log("keepalive", logs.ErrorLevel, "failed to send ping: %v", err)
				}
			}()
		case msg = <-inbox:
		}
		if msg == nil {
			continue
		}
		switch m := msg.(type) {
		case *message.Pong:
			missedPongs = 0
			agent.Log("pong", logs.DebugLevel, "pong %s", m.Pong)
		case *message.Ping:
			agent.Log("ping", logs.DebugLevel, "ping %s", m.Ping)
			if err := agent.Send(&message.Pong{Pong: "pong"}); err != nil {
				return err
			}
		case *message.BridgeOpen:
			// Route to child agent if the source matches a forked child.
			if child := agent.findChildForBridge(m.Source); child != nil {
				child.handleBridgeOpen(m)
			} else {
				agent.handleBridgeOpen(m)
			}
		case *message.BridgeClose:
			// Try parent first, then children.
			target := agent
			if _, err := agent.getBridge(m.ID); err != nil {
				agent.children.Range(func(_, v interface{}) bool {
					child := v.(*Agent)
					if _, err := child.getBridge(m.ID); err == nil {
						target = child
						return false
					}
					return true
				})
			}
			if bridge, err := target.getBridge(m.ID); err == nil {
				go func() {
					if !bridge.closed.Load() {
						bridge.Log("end", logs.DebugLevel, "bridge %d closed by remote", m.ID)
						bridge.Close()
					}
					target.bridgeMap.Delete(bridge.id)
				}()
			} else {
				agent.releaseBridgeRoute(m.ID)
			}
		case *message.Control:
			if agent.Type != core.CLIENT && agent.routeControl(m) {
				continue
			}
			if m.Fork {
				a, err := agent.Fork(m)
				if err != nil {
					agent.Log("failed", logs.ErrorLevel, "%s", err.Error())
				} else {
					Agents.Add(a)
				}
			} else {
				if err := agent.handlerControl(m); err != nil {
					agent.Log("failed", logs.ErrorLevel, "%s", err.Error())
				}
			}
		case *message.Reconfigure:
			agent.handleReconfigure(m)
		default:
			agent.Log("message", logs.DebugLevel, "unknown msg type on control stream")
		}
	}
}

// configurable is satisfied by types that support dynamic option updates (e.g. *simplex.SimplexAddr).
// Defined locally to avoid importing x/simplex.
type configurable interface {
	SetOption(key, value string)
}

func (agent *Agent) handleReconfigure(m *message.Reconfigure) {
	cfg, ok := agent.TransportAddr.(configurable)
	if !ok {
		agent.Log("reconfigure", logs.ErrorLevel, "transport addr %T does not support SetOption", agent.TransportAddr)
		return
	}
	for k, v := range m.Options {
		cfg.SetOption(k, v)
		agent.Log("reconfigure", logs.ImportantLevel, "set %s=%s", k, v)
	}
}

func (agent *Agent) saveBridge(bridge *Bridge) {
	atomic.AddInt64(&agent.connCount, 1)
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
		case <-utils.After(monitorInterval * time.Second):
			agent.Log("monitor", logs.DebugLevel, "connections: %d/%d",
				atomic.LoadInt64(&agent.connCount), atomic.LoadUint64(&agent.connIndex))
		}
	}
}

func (agent *Agent) Close(err error) {
	if agent.Closed {
		return
	}
	agent.Closed = true
	if err != nil {
		agent.Log("exit", logs.ImportantLevel, "%s: %s", agent.ID, err.Error())
	} else {
		agent.Log("exit", logs.ImportantLevel, "%s: closed normally", agent.ID)
	}
	agent.canceler()
	if agent.connHub != nil {
		agent.connHub.Close()
	}
	if agent.session != nil {
		agent.session.Close()
	}
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
	return fmt.Sprintf("connections: %d/%d", atomic.LoadInt64(&agent.connCount), atomic.LoadUint64(&agent.connIndex))
}

func (agent *Agent) HistoryLog() string {
	return agent.log.String()
}

// initYamux creates a yamux session over agent.Conn and opens stream 0 (control).
// CLIENT is the yamux client (opens streams), SERVER is the yamux server (accepts streams).
func (agent *Agent) initYamux() error {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = false          // application-level keepalive is the sole liveness mechanism
	cfg.ConnectionWriteTimeout = 24 * time.Hour // no yamux-level write timeout; keepalive governs liveness
	cfg.StreamOpenTimeout = 24 * time.Hour      // no yamux-level stream timeout; keepalive governs liveness

	var err error
	if agent.Type == core.CLIENT {
		agent.session, err = yamux.Client(agent.Conn, cfg)
		if err != nil {
			return fmt.Errorf("yamux client: %w", err)
		}
	} else {
		agent.session, err = yamux.Server(agent.Conn, cfg)
		if err != nil {
			return fmt.Errorf("yamux server: %w", err)
		}
	}

	algo := agent.LoadBalance
	if algo == "" && agent.Params != nil {
		algo = agent.Params["lb"]
	}
	if agent.connHub == nil {
		agent.connHub = NewConnHub(algo)
	} else if algo != "" {
		agent.connHub.SetAlgorithm(algo)
	}
	connID := agent.nextConnID(agent.ConsoleURL.Scheme)
	agent.sessionConnID = connID
	if agent.Type == core.CLIENT {
		controlStream, addErr := agent.connHub.AddClientConn(connID, agent.ConsoleURL.Scheme, agent.Conn, agent.session)
		if addErr != nil {
			return addErr
		}
		agent.setControlStream(controlStream)
		return nil
	}
	controlStream, addErr := agent.connHub.AddServerConn(connID, agent.ConsoleURL.Scheme, agent.Conn, agent.session)
	if addErr != nil {
		return addErr
	}
	agent.setControlStream(controlStream)
	return nil
}

// acceptStreams accepts incoming yamux data streams and associates them with bridges.
func (agent *Agent) acceptStreams() {
	connID := agent.sessionConnID
	if connID == "" {
		connID = agent.nextConnID(agent.ConsoleURL.Scheme)
		agent.sessionConnID = connID
	}
	agent.acceptStreamsForSession(connID, agent.session)
}

func (agent *Agent) acceptStreamsForSession(connID string, session *yamux.Session) {
	for {
		stream, err := session.Accept()
		if err != nil {
			if agent.connHub != nil {
				agent.connHub.MarkUnhealthy(connID)
				agent.connHub.RemoveConn(connID)
			}
			if !agent.Closed {
				// Don't call agent.Close() here — RemoveConn already notifies
				// handleMessage via controlErrs channel.
				agent.Log("stream", logs.DebugLevel, "channel %s closed: %v", connID, err)
			}
			return
		}
		go agent.handleDataStream(stream)
	}
}

func (agent *Agent) AttachConn(conn net.Conn, label string) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if agent.connHub == nil || agent.session == nil {
		return fmt.Errorf("agent not initialized")
	}
	return agent.attachConnNow(conn, label)
}

func (agent *Agent) attachConnNow(conn net.Conn, label string) error {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = false          // application-level keepalive is the sole liveness mechanism
	cfg.ConnectionWriteTimeout = 24 * time.Hour // no yamux-level write timeout; keepalive governs liveness
	cfg.StreamOpenTimeout = 24 * time.Hour      // no yamux-level stream timeout; keepalive governs liveness

	var (
		session *yamux.Session
		err     error
	)
	if agent.Type == core.CLIENT {
		session, err = yamux.Client(conn, cfg)
	} else {
		session, err = yamux.Server(conn, cfg)
	}
	if err != nil {
		_ = conn.Close()
		return err
	}

	connID := agent.nextConnID(label)
	var (
		addErr        error
		controlStream net.Conn
	)
	if agent.Type == core.CLIENT {
		controlStream, addErr = agent.connHub.AddClientConn(connID, label, conn, session)
	} else {
		controlStream, addErr = agent.connHub.AddServerConn(connID, label, conn, session)
	}
	if addErr != nil {
		_ = session.Close()
		_ = conn.Close()
		return addErr
	}
	agent.setControlStream(controlStream)

	agent.SafeGoWithRestart("streamAccept."+connID, func() {
		agent.acceptStreamsForSession(connID, session)
	})
	return nil
}

func (agent *Agent) openBridgeStream(bridgeID uint64) (net.Conn, string, error) {
	if agent.connHub != nil {
		return agent.connHub.OpenStream(bridgeID)
	}
	stream, err := agent.session.Open()
	if err != nil {
		return nil, "", err
	}
	return stream, "", nil
}

func (agent *Agent) releaseBridgeRoute(bridgeID uint64) {
	if agent.connHub != nil {
		agent.connHub.ReleaseBridge(bridgeID)
	}
}

func (agent *Agent) ConnHubStats() []ConnHubConnStat {
	if agent.connHub == nil {
		return nil
	}
	return agent.connHub.Stats()
}

// readStreamID reads the 8-byte little-endian bridge ID header from a yamux data stream.
func readStreamID(stream net.Conn) (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(stream, buf[:]); err != nil {
		return 0, err
	}
	return uint64(buf[0]) | uint64(buf[1])<<8 | uint64(buf[2])<<16 | uint64(buf[3])<<24 |
		uint64(buf[4])<<32 | uint64(buf[5])<<40 | uint64(buf[6])<<48 | uint64(buf[7])<<56, nil
}

// writeStreamID writes the 8-byte little-endian bridge ID header to a yamux data stream.
func writeStreamID(stream net.Conn, id uint64) error {
	var buf [8]byte
	buf[0] = byte(id)
	buf[1] = byte(id >> 8)
	buf[2] = byte(id >> 16)
	buf[3] = byte(id >> 24)
	buf[4] = byte(id >> 32)
	buf[5] = byte(id >> 40)
	buf[6] = byte(id >> 48)
	buf[7] = byte(id >> 56)
	_, err := stream.Write(buf[:])
	return err
}

// handleDataStream reads the bridge ID from an accepted yamux data stream.
// If the bridge already exists (BridgeOpen arrived first), attach and start.
// Otherwise, park the stream in pendingStreams for handleBridgeOpen to pick up.
// Also checks forked children's bridgeMaps since they share the same yamux session.
func (agent *Agent) handleDataStream(stream net.Conn) {
	id, err := readStreamID(stream)
	if err != nil {
		agent.Log("stream", logs.DebugLevel, "read bridge ID: %v", err)
		stream.Close()
		return
	}

	// Check parent's bridgeMap first.
	if bridge, err := agent.getBridge(id); err == nil {
		bridge.stream = stream
		bridge.Handler(agent)
		return
	}

	// Check children's bridgeMaps.
	var found bool
	agent.children.Range(func(_, v interface{}) bool {
		child := v.(*Agent)
		if bridge, err := child.getBridge(id); err == nil {
			bridge.stream = stream
			bridge.Handler(child)
			found = true
			return false
		}
		return true
	})
	if found {
		return
	}

	// BridgeOpen hasn't arrived yet — park the stream in parent's pendingStreams.
	// handleBridgeOpen (dispatched by parent) will check here.
	agent.pendingStreams.Store(id, stream)
}

// handleBridgeOpen processes a BridgeOpen received on stream 0.
// Creates the bridge, then checks if a data stream already arrived (race).
func (agent *Agent) handleBridgeOpen(m *message.BridgeOpen) {
	ctx, cancel := context.WithCancel(agent.ctx)
	bridge := &Bridge{
		id:          m.ID,
		source:      agent.ID,
		destination: m.Source,
		ctx:         ctx,
		cancel:      cancel,
		agent:       agent,
	}

	go func() {
		select {
		case <-bridge.ctx.Done():
			atomic.AddInt64(&agent.connCount, -1)
		case <-agent.ctx.Done():
			bridge.Close()
		}
	}()

	atomic.AddInt64(&agent.connCount, 1)
	agent.bridgeMap.Store(bridge.id, bridge)

	// Check if the data stream already arrived before this BridgeOpen.
	// Data streams are parked in the parent's pendingStreams (since acceptStreams
	// runs on the parent), so check there for forked children.
	if v, ok := agent.pendingStreams.LoadAndDelete(m.ID); ok {
		bridge.stream = v.(net.Conn)
		bridge.Handler(agent)
	} else if agent.parent != nil {
		if v, ok := agent.parent.pendingStreams.LoadAndDelete(m.ID); ok {
			bridge.stream = v.(net.Conn)
			bridge.Handler(agent)
		}
	}
	// Otherwise, handleDataStream will find the bridge when the stream arrives
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

// getChild returns a forked child agent by ID, or nil if not found.
func (agent *Agent) getChild(id string) *Agent {
	if v, ok := agent.children.Load(id); ok {
		return v.(*Agent)
	}
	return nil
}

// findChildForBridge checks if a BridgeOpen/BridgeClose message should be
// dispatched to a child agent. Returns the child agent or nil.
func (agent *Agent) findChildForBridge(source string) *Agent {
	if child := agent.getChild(source); child != nil {
		return child
	}
	return nil
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
