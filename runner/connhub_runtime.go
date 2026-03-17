package runner

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
)

const channelRoleFull = "full"

// mergeHalfConn combines two net.Conn: reads from readConn, writes to writeConn.
func mergeHalfConn(readConn, writeConn net.Conn) net.Conn {
	return &halfConn{
		readConn:  readConn,
		writeConn: writeConn,
	}
}

type halfConn struct {
	readConn  net.Conn
	writeConn net.Conn
	closeOnce sync.Once
	closeErr  error
}

func (c *halfConn) Read(p []byte) (int, error) {
	return c.readConn.Read(p)
}

func (c *halfConn) Write(p []byte) (int, error) {
	return c.writeConn.Write(p)
}

func (c *halfConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = closeHalfConn(c.readConn, c.writeConn)
	})
	return c.closeErr
}

func (c *halfConn) LocalAddr() net.Addr {
	return c.writeConn.LocalAddr()
}

func (c *halfConn) RemoteAddr() net.Addr {
	return c.writeConn.RemoteAddr()
}

func (c *halfConn) SetDeadline(t time.Time) error {
	return errors.Join(
		c.readConn.SetReadDeadline(t),
		c.writeConn.SetWriteDeadline(t),
	)
}

func (c *halfConn) SetReadDeadline(t time.Time) error {
	return c.readConn.SetReadDeadline(t)
}

func (c *halfConn) SetWriteDeadline(t time.Time) error {
	return c.writeConn.SetWriteDeadline(t)
}

func closeHalfConn(conns ...net.Conn) error {
	var errs []error
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type channelRole struct {
	Mode string
	ID   string
}

func parseChannelRole(raw string) (channelRole, error) {
	if raw == "" {
		return channelRole{}, nil
	}
	parts := strings.SplitN(raw, ":", 2)
	role := channelRole{Mode: parts[0]}
	if len(parts) == 2 {
		role.ID = parts[1]
	}
	switch role.Mode {
	case core.DirUp, core.DirDown:
		return role, nil
	default:
		if role.ID != "" {
			// Non-directional modes with id are treated as generic attach channels.
			return role, nil
		}
		if role.Mode == "" {
			return role, nil
		}
		return channelRole{}, fmt.Errorf("invalid channel role: %q", raw)
	}
}

func makeChannelRole(mode, id string) string {
	if id == "" {
		return mode
	}
	return mode + ":" + id
}

func hasDirectionalChannels(urls []*core.URL) bool {
	for _, u := range urls {
		if u.Direction != "" {
			return true
		}
	}
	return false
}

func loginWithChannelRole(conn net.Conn, a *agent.Agent, token, role string, consoleURL *core.URL) error {
	_, err := cio.WriteAndAssertMsg(conn, &message.Login{
		Agent:        a.ID,
		ConsoleProto: consoleURL.Scheme,
		ConsoleIP:    consoleURL.Hostname(),
		ConsolePort:  consoleURL.IntPort(),
		Mod:          a.Mod,
		Token:        token,
		Interfaces:   a.Interfaces,
		Hostname:     a.Hostname,
		Username:     a.Username,
		ChannelRole:  role,
	})
	return err
}

func splitConsoleChannels(urls []*core.URL) (fullURLs, upURLs, downURLs []*core.URL, err error) {
	for _, u := range urls {
		switch u.Direction {
		case core.DirUp:
			upURLs = append(upURLs, u)
		case core.DirDown:
			downURLs = append(downURLs, u)
		default:
			fullURLs = append(fullURLs, u)
		}
	}
	if len(upURLs) != len(downURLs) {
		return nil, nil, nil, fmt.Errorf("up/down channel count mismatch: up=%d down=%d", len(upURLs), len(downURLs))
	}
	if len(fullURLs) == 0 && len(upURLs) == 0 {
		return nil, nil, nil, fmt.Errorf("no console channels configured")
	}
	return fullURLs, upURLs, downURLs, nil
}

func (r *RunnerConfig) dialChannelConn(a *agent.Agent, url *core.URL, role string) (net.Conn, error) {
	console, err := NewConsole(r, r.NewURLs(url))
	if err != nil {
		return nil, err
	}
	conn, err := console.tunnel.Dial(url.String())
	if err != nil {
		return nil, err
	}
	if err := loginWithChannelRole(conn, a, console.token, role, url); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func (r *RunnerConfig) dialDirectionalConn(a *agent.Agent, upURL, downURL *core.URL, pairID string) (net.Conn, error) {
	upConn, err := r.dialChannelConn(a, upURL, makeChannelRole(core.DirUp, pairID))
	if err != nil {
		return nil, err
	}
	downConn, err := r.dialChannelConn(a, downURL, makeChannelRole(core.DirDown, pairID))
	if err != nil {
		_ = upConn.Close()
		return nil, err
	}
	// Client direction: write to up, read from down.
	return mergeHalfConn(downConn, upConn), nil
}

func (r *RunnerConfig) attachChannelConns(a *agent.Agent, fullURLs, upURLs, downURLs []*core.URL, pairStart int) {
	for index := 1; index < len(fullURLs); index++ {
		label := fmt.Sprintf("%s-%d", fullURLs[index].Scheme, index)
		conn, err := r.dialChannelConn(a, fullURLs[index], makeChannelRole(channelRoleFull, label))
		if err != nil {
			utils.Log.Warnf("[connhub] dial channel full %s failed: %v", label, err)
			continue
		}
		if err := a.AttachConn(conn, label); err != nil {
			utils.Log.Warnf("[connhub] attach channel full %s failed: %v", label, err)
			_ = conn.Close()
			continue
		}
		utils.Log.Infof("[connhub] attached channel full %s", label)
	}

	for index := pairStart; index < len(upURLs); index++ {
		pairID := fmt.Sprintf("pair-%d", index)
		conn, err := r.dialDirectionalConn(a, upURLs[index], downURLs[index], pairID)
		if err != nil {
			utils.Log.Warnf("[connhub] dial channel pair %s failed: %v", pairID, err)
			continue
		}
		if err := a.AttachConn(conn, pairID); err != nil {
			utils.Log.Warnf("[connhub] attach channel pair %s failed: %v", pairID, err)
			_ = conn.Close()
			continue
		}
		utils.Log.Infof("[connhub] attached channel pair %s", pairID)
	}
}

// runConnHubClient handles client-side multi-channel mode:
// any number of full channels and/or directional pairs.
func (r *RunnerConfig) runConnHubClient() error {
	fullURLs, upURLs, downURLs, err := splitConsoleChannels(r.ConsoleURLs)
	if err != nil {
		return err
	}

	directionalOnly := len(fullURLs) == 0
	connectURL := fullURLs[0]
	if directionalOnly {
		connectURL = upURLs[0]
	}

	urls := r.NewURLs(connectURL)
	console, err := NewConsole(r, urls)
	if err != nil {
		return err
	}

	consecutiveDialFailures := 0
	for {
		var a *agent.Agent
		if directionalOnly {
			a, err = console.DialDirectionalPair(upURLs[0], downURLs[0])
		} else {
			a, err = console.Dial(fullURLs[0])
		}
		if err != nil {
			consecutiveDialFailures++
			if r.Retry > 0 && consecutiveDialFailures > r.Retry {
				utils.Log.Errorf("[connhub-dial] %d consecutive failures, giving up", consecutiveDialFailures)
				break
			}
			utils.Log.Errorf("[connhub-dial] %s (%d failures)", err.Error(), consecutiveDialFailures)
			time.Sleep(time.Duration(r.RetryInterval) * time.Second)
			continue
		}

		if err := a.HandlerInit(); err != nil {
			a.Close(err)
			agent.Agents.Delete(a.ID)
			consecutiveDialFailures++
			if r.Retry > 0 && consecutiveDialFailures > r.Retry {
				utils.Log.Errorf("[connhub] %d consecutive failures, giving up", consecutiveDialFailures)
				break
			}
			utils.Log.Warnf("[connhub] agent init failed: %v (%d failures)", err, consecutiveDialFailures)
			time.Sleep(time.Duration(r.RetryInterval) * time.Second)
			continue
		}

		consecutiveDialFailures = 0
		pairStart := 0
		if directionalOnly {
			pairStart = 1
		}
		r.attachChannelConns(a, fullURLs, upURLs, downURLs, pairStart)
		console.Handler(a)
		utils.Log.Infof("handler exited, reconnecting...")
	}
	return nil
}
