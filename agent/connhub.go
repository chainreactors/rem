package agent

import (
	"fmt"
	"math/rand"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/yamux"
)

const (
	ConnBalanceRandom     = "random"
	ConnBalanceFallback   = "fallback"
	ConnBalanceRoundRobin = "round-robin"
)

func normalizeConnBalance(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ConnBalanceRandom:
		return ConnBalanceRandom
	case "rr", "roundrobin", ConnBalanceRoundRobin:
		return ConnBalanceRoundRobin
	case "fb", ConnBalanceFallback:
		return ConnBalanceFallback
	default:
		return ConnBalanceFallback
	}
}

type managedConn struct {
	id      string
	label   string
	conn    net.Conn
	session *yamux.Session
	control net.Conn
	ctrlMu  sync.Mutex

	active   atomic.Int64
	selected atomic.Uint64
	healthy  atomic.Bool
}

type ConnHub struct {
	mu sync.RWMutex

	conns       map[string]*managedConn
	preferredID string
	algo        string
	rrCursor    atomic.Uint64

	bridgeRoute sync.Map // map[bridgeID]connID

	rngMu sync.Mutex
	rng   *rand.Rand

	controlCursor atomic.Uint64
	controlInbox  chan message.Message
	controlErrs   chan error

	closed    chan struct{}
	closeOnce sync.Once
	ctrlDown  atomic.Bool
}

func NewConnHub(algo string) *ConnHub {
	return &ConnHub{
		conns:        make(map[string]*managedConn),
		algo:         normalizeConnBalance(algo),
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
		controlInbox: make(chan message.Message, 256),
		controlErrs:  make(chan error, 1),
		closed:       make(chan struct{}),
	}
}

func (h *ConnHub) SetAlgorithm(name string) {
	h.mu.Lock()
	h.algo = normalizeConnBalance(name)
	h.mu.Unlock()
}

func (h *ConnHub) Algorithm() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.algo
}

func (h *ConnHub) AddClientConn(id, label string, conn net.Conn, session *yamux.Session) (net.Conn, error) {
	return h.addConn(id, label, conn, session, session.Open)
}

func (h *ConnHub) AddServerConn(id, label string, conn net.Conn, session *yamux.Session) (net.Conn, error) {
	return h.addConn(id, label, conn, session, session.Accept)
}

func (h *ConnHub) addConn(id, label string, conn net.Conn, session *yamux.Session, openControl func() (net.Conn, error)) (net.Conn, error) {
	if id == "" {
		return nil, fmt.Errorf("empty conn id")
	}
	if conn == nil || session == nil {
		return nil, fmt.Errorf("conn/session is nil")
	}
	if h.isClosed() {
		return nil, fmt.Errorf("connhub closed")
	}

	control, err := openControl()
	if err != nil {
		return nil, fmt.Errorf("open control stream: %w", err)
	}

	h.mu.Lock()
	if h.isClosed() {
		h.mu.Unlock()
		_ = control.Close()
		return nil, fmt.Errorf("connhub closed")
	}
	if _, exists := h.conns[id]; exists {
		h.mu.Unlock()
		_ = control.Close()
		return nil, fmt.Errorf("conn %s already exists", id)
	}
	m := &managedConn{
		id:      id,
		label:   label,
		conn:    conn,
		session: session,
		control: control,
	}
	m.healthy.Store(true)
	h.conns[id] = m
	if h.preferredID == "" {
		h.preferredID = id
	}
	h.ctrlDown.Store(false)
	h.mu.Unlock()
	h.startControlReader(id, control)
	return control, nil
}

func (h *ConnHub) RemoveConn(id string) {
	var (
		m          *managedConn
		hadControl bool
		remaining  int
	)
	h.mu.Lock()
	if existing, ok := h.conns[id]; ok {
		m = existing
		hadControl = existing.control != nil
		delete(h.conns, id)
		if h.preferredID == id {
			h.preferredID = ""
		}
		remaining = h.controlCountLocked()
	}
	h.mu.Unlock()

	if m == nil {
		return
	}
	if m.control != nil {
		_ = m.control.Close()
	}
	if m.session != nil {
		_ = m.session.Close()
	}
	if m.conn != nil {
		_ = m.conn.Close()
	}
	h.removeBridgeRoutesForConn(id)
	if hadControl && remaining == 0 {
		h.notifyControlDown(fmt.Errorf("all control streams closed"))
	}
}

func (h *ConnHub) MarkUnhealthy(id string) {
	h.mu.RLock()
	conn := h.conns[id]
	h.mu.RUnlock()
	if conn != nil {
		conn.healthy.Store(false)
	}
}

func (h *ConnHub) OpenStream(bridgeID uint64) (net.Conn, string, error) {
	// Sticky route for existing bridge.
	if v, ok := h.bridgeRoute.Load(bridgeID); ok {
		connID := v.(string)
		if m := h.getConn(connID); m != nil && m.healthy.Load() {
			stream, err := m.session.Open()
			if err == nil {
				return stream, connID, nil
			}
			h.bridgeRoute.Delete(bridgeID)
			h.MarkUnhealthy(connID)
		}
		h.bridgeRoute.Delete(bridgeID)
	}

	excluded := map[string]struct{}{}
	for {
		m := h.pick(excluded)
		if m == nil {
			return nil, "", fmt.Errorf("no healthy conn available")
		}

		m.active.Add(1)
		m.selected.Add(1)
		h.bridgeRoute.Store(bridgeID, m.id)

		stream, err := m.session.Open()
		if err == nil {
			return stream, m.id, nil
		}

		h.bridgeRoute.Delete(bridgeID)
		if m.active.Add(-1) < 0 {
			m.active.Store(0)
		}
		m.healthy.Store(false)
		excluded[m.id] = struct{}{}
	}
}

func (h *ConnHub) ReleaseBridge(bridgeID uint64) {
	v, ok := h.bridgeRoute.LoadAndDelete(bridgeID)
	if !ok {
		return
	}
	connID := v.(string)
	m := h.getConn(connID)
	if m == nil {
		return
	}
	if m.active.Add(-1) < 0 {
		m.active.Store(0)
	}
}

type ConnHubConnStat struct {
	ID       string
	Label    string
	Active   int64
	Selected uint64
	Healthy  bool
}

func (h *ConnHub) Stats() []ConnHubConnStat {
	h.mu.RLock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	stats := make([]ConnHubConnStat, 0, len(ids))
	for _, id := range ids {
		m := h.conns[id]
		stats = append(stats, ConnHubConnStat{
			ID:       m.id,
			Label:    m.label,
			Active:   m.active.Load(),
			Selected: m.selected.Load(),
			Healthy:  m.healthy.Load(),
		})
	}
	h.mu.RUnlock()
	return stats
}

func (h *ConnHub) Count() int {
	h.mu.RLock()
	n := len(h.conns)
	h.mu.RUnlock()
	return n
}

func (h *ConnHub) ControlStreamCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.controlCountLocked()
}

func (h *ConnHub) SendControl(msg message.Message) error {
	ids := h.controlIDs()
	if len(ids) == 0 {
		return fmt.Errorf("no control stream available")
	}
	start := int(h.controlCursor.Add(1)-1) % len(ids)
	for i := 0; i < len(ids); i++ {
		id := ids[(start+i)%len(ids)]
		m := h.getConn(id)
		if m == nil {
			continue
		}
		stream := m.control
		if stream == nil {
			continue
		}
		m.ctrlMu.Lock()
		err := cio.WriteMsg(stream, msg)
		m.ctrlMu.Unlock()
		if err == nil {
			return nil
		}
		h.RemoveConn(id)
	}
	return fmt.Errorf("all control streams failed")
}

func (h *ConnHub) ControlInbox() <-chan message.Message {
	return h.controlInbox
}

func (h *ConnHub) ControlErrors() <-chan error {
	return h.controlErrs
}

func (h *ConnHub) Close() {
	h.closeOnce.Do(func() {
		close(h.closed)
		h.mu.Lock()
		conns := h.conns
		h.conns = map[string]*managedConn{}
		h.preferredID = ""
		h.mu.Unlock()
		for _, m := range conns {
			if m.control != nil {
				_ = m.control.Close()
			}
			if m.session != nil {
				_ = m.session.Close()
			}
			if m.conn != nil {
				_ = m.conn.Close()
			}
		}
	})
}

func (h *ConnHub) getConn(id string) *managedConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[id]
}

func (h *ConnHub) pick(excluded map[string]struct{}) *managedConn {
	candidates := h.candidates(excluded)
	if len(candidates) == 0 {
		return nil
	}

	h.mu.RLock()
	algo := h.algo
	preferredID := h.preferredID
	h.mu.RUnlock()

	switch algo {
	case ConnBalanceRandom:
		h.rngMu.Lock()
		idx := h.rng.Intn(len(candidates))
		h.rngMu.Unlock()
		return candidates[idx]
	case ConnBalanceRoundRobin:
		idx := int(h.rrCursor.Add(1)-1) % len(candidates)
		return candidates[idx]
	case ConnBalanceFallback:
		for _, c := range candidates {
			if c.id == preferredID {
				return c
			}
		}
		return candidates[0]
	default:
		return candidates[0]
	}
}

func (h *ConnHub) candidates(excluded map[string]struct{}) []*managedConn {
	h.mu.RLock()
	ids := make([]string, 0, len(h.conns))
	for id, m := range h.conns {
		if !m.healthy.Load() {
			continue
		}
		if _, skip := excluded[id]; skip {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]*managedConn, 0, len(ids))
	for _, id := range ids {
		result = append(result, h.conns[id])
	}
	h.mu.RUnlock()
	return result
}

func (h *ConnHub) startControlReader(connID string, stream net.Conn) {
	go func() {
		for {
			msg, err := cio.ReadMsg(stream)
			if err != nil {
				h.RemoveConn(connID)
				return
			}
			if !h.enqueueControl(msg) {
				return
			}
		}
	}()
}

func (h *ConnHub) enqueueControl(msg message.Message) bool {
	select {
	case <-h.closed:
		return false
	case h.controlInbox <- msg:
		return true
	}
}

func (h *ConnHub) removeBridgeRoutesForConn(connID string) {
	h.bridgeRoute.Range(func(key, value interface{}) bool {
		id, ok := value.(string)
		if ok && id == connID {
			h.bridgeRoute.Delete(key)
		}
		return true
	})
}

func (h *ConnHub) controlIDs() []string {
	h.mu.RLock()
	ids := make([]string, 0, len(h.conns))
	for id, c := range h.conns {
		if c.control != nil {
			ids = append(ids, id)
		}
	}
	h.mu.RUnlock()
	sort.Strings(ids)
	return ids
}

func (h *ConnHub) controlCountLocked() int {
	count := 0
	for _, c := range h.conns {
		if c.control != nil {
			count++
		}
	}
	return count
}

func (h *ConnHub) notifyControlDown(err error) {
	if err == nil {
		err = fmt.Errorf("all control streams closed")
	}
	if !h.ctrlDown.CompareAndSwap(false, true) {
		return
	}
	select {
	case <-h.closed:
	case h.controlErrs <- err:
	default:
	}
}

func (h *ConnHub) isClosed() bool {
	select {
	case <-h.closed:
		return true
	default:
		return false
	}
}
