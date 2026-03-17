package runner

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/protocol/tunnel"
	"github.com/chainreactors/rem/x/utils"
)

func NewConsole(runner *RunnerConfig, urls *core.URLs) (*Console, error) {
	var err error
	console := &Console{
		URLs:    urls,
		Config:  runner,
		pending: make(map[string]*pendingPair),
	}

	token, err := buildConsoleToken(runner.Key)
	if err != nil {
		return nil, err
	}
	console.token = token
	isServer := console.Config.IsServerMode
	tunOpts, err := buildAdvancedTunnelOptions(console, runner)
	if err != nil {
		return nil, err
	}
	var wrapperOpts core.WrapperOptions
	if wrapperStr := console.ConsoleURL.GetQuery("wrapper"); wrapperStr != "" {
		if wrapperStr != "raw" { //debug
			wrapperOpts, err = core.ParseWrapperOptions(wrapperStr, runner.Key)
			if err != nil {
				return nil, err
			}
		}
	} else {
		wrapperOpts = defaultWrapperOptions()
	}

	if wrapperOpts != nil {
		console.ConsoleURL.SetQuery("wrapper", wrapperOpts.String(runner.Key))
		tunOpts = append(tunOpts, tunnel.WithWrappers(isServer, wrapperOpts))
	}

	console.tunnel, err = tunnel.NewTunnel(context.Background(), console.ConsoleURL.Tunnel, isServer, tunOpts...)
	if err != nil {
		return nil, err
	}
	return console, nil
}

type Console struct {
	Config *RunnerConfig
	token  string
	*core.URLs
	sub    *core.URL
	tunnel *tunnel.TunnelService
	closed bool

	pendingMu sync.Mutex
	pending   map[string]*pendingPair

	pendingReaperOnce sync.Once
	pendingStopOnce   sync.Once
	pendingStop       chan struct{}
	pendingDone       chan struct{}
}

type pendingConn struct {
	conn  net.Conn
	login *message.Login
}

type pendingPair struct {
	createdAt time.Time
	up        pendingConn
	down      pendingConn
}

const (
	pendingPairTTL      = 30 * time.Second
	pendingReapInterval = 2 * time.Second
)

func (c *Console) Run() error {
	if c.Config.IsServerMode {
		err := c.Listen(c.ConsoleURL)
		if err != nil {
			return err
		}
		c.startPendingReaper()

		utils.Log.Importantf("%s channel starting with %s", c.ConsoleURL.Scheme, c.Config.IP)
		utils.Log.Important(c.Link())
		for {
			age, err := c.Accept()
			if err != nil {
				utils.Log.Error(err.Error())
				continue
			}
			if age == nil {
				continue // half-channel waiting for pair
			}

			go c.Handler(age)
		}
	} else {
		backoff := time.Duration(c.Config.RetryInterval) * time.Second
		maxBackoff := time.Duration(c.Config.RetryMaxInterval) * time.Second
		consecutiveDialFailures := 0

		for {
			age, err := c.Dial(c.ConsoleURL)
			if err != nil {
				consecutiveDialFailures++
				// Retry > 0: give up after N consecutive dial failures.
				// Retry <= 0 (default): never give up.
				if c.Config.Retry > 0 && consecutiveDialFailures > c.Config.Retry {
					utils.Log.Errorf("[dial] %d consecutive failures, giving up", consecutiveDialFailures)
					break
				}
				// jitter: ±25%
				jitter := time.Duration(rand.Int63n(int64(backoff/2))) - backoff/4
				sleep := backoff + jitter
				utils.Log.Errorf("[dial] %s, retry after %v (%d failures)", err.Error(), sleep, consecutiveDialFailures)
				time.Sleep(sleep)
				// exponential backoff, double but cap at max
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			// connected successfully — reset backoff and failure counter
			backoff = time.Duration(c.Config.RetryInterval) * time.Second
			consecutiveDialFailures = 0
			c.Handler(age)
			utils.Log.Infof("handler exited, reconnecting...")
			// Give the old agent time to fully clean up before creating a new one
			time.Sleep(1 * time.Second)
		}
	}
	return nil
}

func (c *Console) Dial(address *core.URL) (*agent.Agent, error) {
	conn, err := c.tunnel.Dial(address.String())
	if err != nil {
		return nil, err
	}
	a, err := c.newAgent(c.URLs, c.Config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Pass transport addr for dynamic reconfiguration (e.g. *SimplexAddr)
	if addr := c.tunnel.Meta("simplex_addr"); addr != nil {
		if na, ok := addr.(net.Addr); ok {
			a.TransportAddr = na
		}
	}
	err = a.Login(conn)
	if err != nil {
		conn.Close() // close transport first — stops polling goroutines
		a.Close(err)
		return nil, err
	}
	err = a.Dial(c.RemoteURL, c.LocalURL)
	if err != nil {
		conn.Close()
		a.Close(err)
		return nil, err
	}
	agent.Agents.Add(a)

	// Initialize yamux session and start background loops before forking.
	// Both are idempotent (HandlerInit checks Init flag, StartBackgroundLoops uses sync.Once).
	if len(c.Config.ExtraServes) > 0 {
		if err := a.HandlerInit(); err != nil {
			a.Close(err)
			return nil, err
		}
		a.StartBackgroundLoops()
	}

	// Auto-fork extra serves
	for _, extra := range c.Config.ExtraServes {
		alias := utils.RandomString(8)
		ctrl := &message.Control{
			Mod:         c.Config.Mod,
			Source:      alias,
			Destination: a.ID, // match primary agent so server processes locally
			Local:       extra.RemoteURL.String(),
			Remote:      extra.LocalURL.String(),
		}
		forked, err := a.Fork(ctrl)
		if err != nil {
			utils.Log.Errorf("[fork] extra serve failed: %s", err.Error())
			continue
		}
		a.Send(&message.Control{
			Mod:         ctrl.Mod,
			Source:      ctrl.Source,
			Destination: ctrl.Destination,
			Local:       ctrl.Local,
			Remote:      ctrl.Remote,
			Fork:        true,
		})
		agent.Agents.Add(forked)
	}

	return a, nil
}

// DialDirectionalPair dials up/down channels, then merges them into one logical Conn.
func (c *Console) DialDirectionalPair(upURL, downURL *core.URL) (*agent.Agent, error) {
	a, err := c.newAgent(c.URLs, c.Config)
	if err != nil {
		return nil, err
	}

	// Dial up channel (uses Console's own tunnel, which matches upURL's protocol)
	upConn, err := c.tunnel.Dial(upURL.String())
	if err != nil {
		return nil, fmt.Errorf("dial up: %w", err)
	}
	if err := loginWithChannelRole(upConn, a, c.token, core.DirUp, upURL); err != nil {
		upConn.Close()
		return nil, fmt.Errorf("login up: %w", err)
	}
	utils.Log.Infof("[connhub] up channel ready")

	// Create a second tunnel for the down channel
	downTun, err := tunnel.NewTunnel(context.Background(), downURL.Tunnel, false)
	if err != nil {
		upConn.Close()
		return nil, fmt.Errorf("new down tunnel: %w", err)
	}
	downConn, err := downTun.Dial(downURL.String())
	if err != nil {
		upConn.Close()
		return nil, fmt.Errorf("dial down: %w", err)
	}
	if err := loginWithChannelRole(downConn, a, c.token, core.DirDown, downURL); err != nil {
		upConn.Close()
		downConn.Close()
		return nil, fmt.Errorf("login down: %w", err)
	}
	utils.Log.Infof("[connhub] down channel ready")

	// Merge: client writes to upConn, reads from downConn
	dc := mergeHalfConn(downConn, upConn)
	a.Conn = dc

	// Send ControlMsg over the merged conn
	err = a.Dial(c.RemoteURL, c.LocalURL)
	if err != nil {
		dc.Close()
		return nil, err
	}
	agent.Agents.Add(a)
	return a, nil
}

func (c *Console) Fork(raw string, args []string) (*agent.Agent, error) {
	var opt Options
	err := opt.ParseArgs(args)
	if err != nil {
		return nil, err
	}
	r, err := opt.Prepare()
	if err != nil {
		return nil, err
	}
	if r.Alias == "" {
		r.Alias = utils.RandomString(8)
	}

	a, ok := agent.Agents.Get(raw)
	if !ok {
		return nil, fmt.Errorf("not found agent")
	}

	forked, err := a.Fork(&message.Control{
		Mod:         r.Mod,
		Source:      r.Alias,
		Destination: r.Alias,
		Local:       r.URLs.RemoteURL.String(),
		Remote:      r.URLs.LocalURL.String(),
	})
	if err != nil {
		return nil, err
	}
	a.Send(&message.Control{
		Mod:         r.Mod,
		Source:      r.Alias,
		Destination: r.Alias,
		Local:       r.URLs.RemoteURL.String(),
		Remote:      r.URLs.LocalURL.String(),
		Fork:        true,
	})
	agent.Agents.Add(forked)
	return forked, nil
}

func (c *Console) Listen(dst *core.URL) error {
	var err error
	_, err = c.tunnel.Listen(dst.String())
	if err != nil {
		if strings.Contains(err.Error(), "bind: An attempt was made to access a socket in a way forbidden by its access permissions.") {
			dst.SetPort(utils.RandPort())
			utils.Log.Importantf("port is already in use, get a new port **%s** at random", c.ConsoleURL.Port())
			return c.Listen(dst)
		}
		return err
	}
	if !c.Config.NoSubscribe {
		if c.Config.Subscribe != "" {
			subURL, err := core.NewURL(c.Config.Subscribe)
			if err != nil {
				utils.Log.Warnf("subscribe url error: %s", err.Error())
			}
			c.sub = subURL
		}
		c.handlerSubscribe()
	}

	return nil
}

func (c *Console) Accept() (*agent.Agent, error) {
	conn, err := c.tunnel.Accept()
	if err != nil {
		return nil, err
	}

	// 设置 login 读取超时，防止残留连接阻塞 accept 循环
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	loginMsg, err := cio.ReadAndAssertMsg(conn, message.LoginMsg)
	conn.SetReadDeadline(time.Time{}) // 清除超时
	if err != nil {
		conn.Close()
		return nil, err
	}

	login := loginMsg.(*message.Login)
	if c.token != login.Token {
		utils.Log.Error("error token , please check")
		cio.WriteMsg(conn, &message.Ack{Status: message.StatusFailed})
		conn.Close()
		return nil, fmt.Errorf("invalid token")
	} else {
		cio.WriteMsg(conn, &message.Ack{Status: message.StatusSuccess})
	}

	if login.ChannelRole != "" {
		return c.handleConnHubAccept(conn, login)
	}

	return c.finishAccept(conn, login)
}

// finishAccept completes the normal (non-duplex) accept flow: read Control, create Agent.
func (c *Console) finishAccept(conn net.Conn, login *message.Login) (*agent.Agent, error) {
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	controlMsg, err := cio.ReadAndAssertMsg(conn, message.ControlMsg)
	conn.SetReadDeadline(time.Time{}) // 清除超时
	if err != nil {
		conn.Close()
		return nil, err
	}
	cio.WriteMsg(conn, &message.Ack{Status: message.StatusSuccess})
	control := controlMsg.(*message.Control)
	if old, ok := agent.Agents.Get(login.Agent); ok {
		utils.Log.Warnf("[connhub] id=%s replacing existing session by new login/control", login.Agent)
		old.Close(fmt.Errorf("replaced by new login/control"))
		agent.Agents.Delete(old.ID)
	}

	server, err := agent.NewAgent(&agent.Config{
		Alias:       login.Agent,
		Type:        core.SERVER,
		Mod:         control.Mod,
		Redirect:    control.Destination,
		ExternalIP:  c.Config.IP,
		Interfaces:  login.Interfaces,
		Hostname:    login.Hostname,
		Username:    login.Username,
		Controller:  control,
		LoadBalance: control.Options["lb"],
		URLs: &core.URLs{
			ConsoleURL: login.ConsoleURL(),
			RemoteURL:  control.LocalURL(),
			LocalURL:   control.RemoteURL(),
		},
		Params: control.Options,
	})
	if err != nil {
		return nil, err
	}
	server.Recover = func() error { return c.Run() }
	server.Conn = conn
	// Pass transport addr for dynamic reconfiguration (e.g. *SimplexAddr)
	if addr := c.tunnel.Meta("simplex_addr"); addr != nil {
		if na, ok := addr.(net.Addr); ok {
			server.TransportAddr = na
		}
	}
	if err := server.HandlerInit(); err != nil {
		server.Close(err)
		return nil, err
	}
	utils.Log.Importantf("%s:%s %s connected from %s, iface: %v",
		server.Hostname, server.Username, server.Name(), conn.RemoteAddr().String(), server.Interfaces)
	agent.Agents.Add(server)
	return server, nil
}

func (c *Console) attachConnToAgent(agentID, label string, conn net.Conn) error {
	if label == "" {
		return fmt.Errorf("attach requires non-empty role id")
	}
	a, ok := agent.Agents.Get(agentID)
	if !ok {
		return fmt.Errorf("agent %s not found for channel attach", agentID)
	}
	if a.Closed {
		return fmt.Errorf("agent %s closed for channel attach", agentID)
	}
	return a.AttachConn(conn, label)
}

// handleConnHubAccept resolves role-based conn (full/up/down) and either:
//   - waits for pair (return nil,nil),
//   - attaches to an existing agent channel set,
//   - or treats it as initial conn and proceeds to finishAccept.
func pendingConnKey(agentID, channelID string) string {
	return agentID + "|" + channelID
}

func (c *Console) matchPendingConn(agentID string, role channelRole, current pendingConn) (readyConn net.Conn, readyLogin *message.Login, ready bool, err error) {
	if role.Mode != core.DirUp && role.Mode != core.DirDown {
		if role.ID == "" {
			// No role id is a session entry connection.
			return current.conn, current.login, true, nil
		}
		// Non directional role ids are direct attach channels.
		return current.conn, current.login, true, nil
	}

	key := pendingConnKey(agentID, role.ID)
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()

	pair := c.pending[key]
	if pair == nil {
		pair = &pendingPair{createdAt: time.Now()}
		c.pending[key] = pair
	}

	switch role.Mode {
	case core.DirUp:
		if pair.up.conn != nil {
			return nil, nil, false, fmt.Errorf("duplicate up role for %s", key)
		}
		pair.up = current
	case core.DirDown:
		if pair.down.conn != nil {
			return nil, nil, false, fmt.Errorf("duplicate down role for %s", key)
		}
		pair.down = current
	}

	if pair.up.conn == nil || pair.down.conn == nil {
		return nil, nil, false, nil
	}

	delete(c.pending, key)
	// Server direction: read from up, write to down.
	return mergeHalfConn(pair.up.conn, pair.down.conn), pair.up.login, true, nil
}

func (c *Console) handleConnHubAccept(conn net.Conn, login *message.Login) (*agent.Agent, error) {
	role, err := parseChannelRole(login.ChannelRole)
	if err != nil {
		conn.Close()
		return nil, err
	}

	resolvedConn, resolvedLogin, ready, err := c.matchPendingConn(login.Agent, role, pendingConn{
		conn:  conn,
		login: login,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !ready {
		utils.Log.Infof("[connhub] id=%s role=%s waiting pair", login.Agent, login.ChannelRole)
		return nil, nil
	}

	// Attach is only allowed for connections with an explicit role.ID.
	// No role.ID is always treated as a new session entry channel.
	if role.ID != "" {
		if err := c.attachConnToAgent(resolvedLogin.Agent, role.ID, resolvedConn); err != nil {
			_ = resolvedConn.Close()
			return nil, err
		}
		utils.Log.Infof("[connhub] id=%s channel %s attached", resolvedLogin.Agent, role.ID)
		return nil, nil
	}

	utils.Log.Infof("[connhub] id=%s channel ready", resolvedLogin.Agent)
	return c.finishAccept(resolvedConn, resolvedLogin)
}

func (c *Console) Handler(server *agent.Agent) {
	err := server.Handler()
	if err != nil {
		server.Log("handler", logs.ImportantLevel, "Handler error: %s", err.Error())
	}
	server.Close(err)
	// Delete agent immediately after Handler returns, before defer cleanup
	agent.Agents.Delete(server.ID)
}

func (c *Console) Close() error {
	c.closed = true
	c.stopPendingReaper()
	c.pendingMu.Lock()
	for _, pair := range c.pending {
		if pair.up.conn != nil {
			_ = pair.up.conn.Close()
		}
		if pair.down.conn != nil {
			_ = pair.down.conn.Close()
		}
	}
	c.pending = map[string]*pendingPair{}
	c.pendingMu.Unlock()
	agent.Agents.Range(func(key, value interface{}) bool {
		value.(*agent.Agent).Close(nil)
		return true
	})
	return c.tunnel.Close()
}

func (c *Console) Link() string {
	u := c.tunnel.Addr().(*core.URL)
	if u.Hostname() == "0.0.0.0" {
		u.SetHostname(c.Config.IP)
	}
	return u.String()
}

func (c *Console) Subscribe() string {
	return c.sub.String()
}

func (c *Console) startPendingReaper() {
	c.pendingReaperOnce.Do(func() {
		c.pendingStop = make(chan struct{})
		c.pendingDone = make(chan struct{})
		go func() {
			ticker := time.NewTicker(pendingReapInterval)
			defer ticker.Stop()
			defer close(c.pendingDone)
			for {
				select {
				case <-ticker.C:
					c.reapExpiredPending(time.Now())
				case <-c.pendingStop:
					return
				}
			}
		}()
	})
}

func (c *Console) stopPendingReaper() {
	c.pendingStopOnce.Do(func() {
		if c.pendingStop != nil {
			close(c.pendingStop)
		}
		if c.pendingDone != nil {
			<-c.pendingDone
		}
	})
}

func closePendingConn(conn net.Conn) {
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *Console) cleanupExpiredPendingLocked(now time.Time) {
	for key, pair := range c.pending {
		if pair == nil {
			delete(c.pending, key)
			continue
		}
		if pair.createdAt.IsZero() {
			pair.createdAt = now
		}
		if now.Sub(pair.createdAt) < pendingPairTTL {
			continue
		}
		closePendingConn(pair.up.conn)
		closePendingConn(pair.down.conn)
		delete(c.pending, key)
		utils.Log.Warnf("[connhub] pending pair timeout, dropped: %s", key)
	}
}

func (c *Console) reapExpiredPending(now time.Time) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.cleanupExpiredPendingLocked(now)
}
