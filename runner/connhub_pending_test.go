//go:build !tinygo

package runner

import (
	"net"
	"testing"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
)

func closePair(c1, c2 net.Conn) {
	if c1 != nil {
		_ = c1.Close()
	}
	if c2 != nil {
		_ = c2.Close()
	}
}

func newPendingTestConsole() *Console {
	return &Console{pending: make(map[string]*pendingPair)}
}

func TestConsoleMatchPendingConnFull(t *testing.T) {
	console := newPendingTestConsole()
	c1, c2 := net.Pipe()
	defer closePair(c1, c2)

	role, err := parseChannelRole(makeChannelRole(channelRoleFull, "tcp-0"))
	if err != nil {
		t.Fatalf("parseChannelRole failed: %v", err)
	}

	conn, login, ready, err := console.matchPendingConn("agent-a", role, pendingConn{
		conn: c1,
		login: &message.Login{
			ChannelRole: makeChannelRole(channelRoleFull, "tcp-0"),
		},
	})
	if err != nil {
		t.Fatalf("matchPendingConn full returned error: %v", err)
	}
	if !ready {
		t.Fatalf("matchPendingConn full should be ready")
	}
	if conn == nil || login == nil {
		t.Fatalf("matchPendingConn full should return conn/login")
	}
}

func TestConsoleMatchPendingConnPairsUpDown(t *testing.T) {
	console := newPendingTestConsole()

	upLocal, upRemote := net.Pipe()
	defer closePair(upLocal, upRemote)
	downLocal, downRemote := net.Pipe()
	defer closePair(downLocal, downRemote)

	upRole, _ := parseChannelRole(core.DirUp)
	_, _, ready, err := console.matchPendingConn("agent-a", upRole, pendingConn{
		conn: upLocal,
		login: &message.Login{
			ChannelRole: core.DirUp,
		},
	})
	if err != nil {
		t.Fatalf("matchPendingConn up returned error: %v", err)
	}
	if ready {
		t.Fatalf("matchPendingConn up should wait for down")
	}

	downRole, _ := parseChannelRole(core.DirDown)
	conn, login, ready, err := console.matchPendingConn("agent-a", downRole, pendingConn{
		conn: downLocal,
		login: &message.Login{
			ChannelRole: core.DirDown,
		},
	})
	if err != nil {
		t.Fatalf("matchPendingConn down returned error: %v", err)
	}
	if !ready {
		t.Fatalf("matchPendingConn down should complete pair")
	}
	if conn == nil || login == nil {
		t.Fatalf("matchPendingConn pair should return conn/login")
	}
}

func TestConsoleMatchPendingConnPairsByID(t *testing.T) {
	console := newPendingTestConsole()

	up0Local, up0Remote := net.Pipe()
	defer closePair(up0Local, up0Remote)
	down0Local, down0Remote := net.Pipe()
	defer closePair(down0Local, down0Remote)

	up1Local, up1Remote := net.Pipe()
	defer closePair(up1Local, up1Remote)
	down1Local, down1Remote := net.Pipe()
	defer closePair(down1Local, down1Remote)

	up0Role, _ := parseChannelRole(makeChannelRole(core.DirUp, "pair-0"))
	_, _, _, _ = console.matchPendingConn("agent-a", up0Role, pendingConn{
		conn: up0Local,
		login: &message.Login{
			ChannelRole: makeChannelRole(core.DirUp, "pair-0"),
		},
	})
	up1Role, _ := parseChannelRole(makeChannelRole(core.DirUp, "pair-1"))
	_, _, _, _ = console.matchPendingConn("agent-a", up1Role, pendingConn{
		conn: up1Local,
		login: &message.Login{
			ChannelRole: makeChannelRole(core.DirUp, "pair-1"),
		},
	})

	down1Role, _ := parseChannelRole(makeChannelRole(core.DirDown, "pair-1"))
	conn1, login1, ready1, err := console.matchPendingConn("agent-a", down1Role, pendingConn{
		conn: down1Local,
		login: &message.Login{
			ChannelRole: makeChannelRole(core.DirDown, "pair-1"),
		},
	})
	if err != nil || !ready1 || conn1 == nil || login1 == nil {
		t.Fatalf("pair-1 should be ready, err=%v ready=%v", err, ready1)
	}

	down0Role, _ := parseChannelRole(makeChannelRole(core.DirDown, "pair-0"))
	conn0, login0, ready0, err := console.matchPendingConn("agent-a", down0Role, pendingConn{
		conn: down0Local,
		login: &message.Login{
			ChannelRole: makeChannelRole(core.DirDown, "pair-0"),
		},
	})
	if err != nil || !ready0 || conn0 == nil || login0 == nil {
		t.Fatalf("pair-0 should be ready, err=%v ready=%v", err, ready0)
	}
}

func TestConsoleMatchPendingConnRejectsDuplicateRole(t *testing.T) {
	console := newPendingTestConsole()
	up1Local, up1Remote := net.Pipe()
	defer closePair(up1Local, up1Remote)
	up2Local, up2Remote := net.Pipe()
	defer closePair(up2Local, up2Remote)

	upRole, _ := parseChannelRole(makeChannelRole(core.DirUp, "pair-0"))
	_, _, _, err := console.matchPendingConn("agent-a", upRole, pendingConn{
		conn: up1Local,
		login: &message.Login{
			ChannelRole: makeChannelRole(core.DirUp, "pair-0"),
		},
	})
	if err != nil {
		t.Fatalf("first up should not fail: %v", err)
	}

	_, _, _, err = console.matchPendingConn("agent-a", upRole, pendingConn{
		conn: up2Local,
		login: &message.Login{
			ChannelRole: makeChannelRole(core.DirUp, "pair-0"),
		},
	})
	if err == nil {
		t.Fatalf("duplicate up should fail")
	}
}

func TestParseChannelRole(t *testing.T) {
	tests := []struct {
		raw  string
		mode string
		id   string
		ok   bool
	}{
		{raw: "", mode: "", id: "", ok: true},
		{raw: core.DirUp, mode: core.DirUp, id: "", ok: true},
		{raw: makeChannelRole(core.DirDown, "p0"), mode: core.DirDown, id: "p0", ok: true},
		{raw: makeChannelRole(channelRoleFull, "tcp-0"), mode: channelRoleFull, id: "tcp-0", ok: true},
		{raw: "sideways", ok: false},
	}
	for _, tc := range tests {
		got, err := parseChannelRole(tc.raw)
		if tc.ok && err != nil {
			t.Fatalf("%q should parse: %v", tc.raw, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%q should fail parse", tc.raw)
		}
		if !tc.ok {
			continue
		}
		if got.Mode != tc.mode || got.ID != tc.id {
			t.Fatalf("%q parsed as %+v, want mode=%s id=%s", tc.raw, got, tc.mode, tc.id)
		}
	}
}
