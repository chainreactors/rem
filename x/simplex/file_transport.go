package simplex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/logs"
)

const (
	// fileHandlerIdleMultiplier × polling interval = idle timeout for handleClient.
	// Production (2s interval): 150 × 2s = 5min; Test (50ms): 150 × 50ms = 7.5s
	fileHandlerIdleMultiplier = 150

	fileDefaultMaxFailures = 10
)

// FileStorageOps abstracts cloud file storage operations (OneDrive / OSS / etc).
type FileStorageOps interface {
	ReadFile(path string) ([]byte, error)     // Read file; 404 → os.ErrNotExist
	WriteFile(path string, data []byte) error // Create/overwrite file
	DeleteFile(path string) error             // Delete file
	ListFiles(prefix string) ([]string, error) // List file names under prefix
	FileExists(path string) (bool, error)     // Lightweight existence check
}

// FileTransportConfig parameterizes file transport behavior.
type FileTransportConfig struct {
	SendSuffix     string        // e.g. "_send.txt" or "_send"
	RecvSuffix     string        // e.g. "_recv.txt" or "_recv"
	Interval       time.Duration
	MaxBodySize    int
	IdleMultiplier int    // handler idle exit multiplier; 0 = use default (fileHandlerIdleMultiplier)
	MaxFailures    int    // consecutive failure limit; 0 = use default (fileDefaultMaxFailures)
	LogPrefix      string // e.g. "[OneDrive]" or "[OSS-File]"
}

func (c *FileTransportConfig) idleMultiplier() int {
	if c.IdleMultiplier > 0 {
		return c.IdleMultiplier
	}
	return fileHandlerIdleMultiplier
}

func (c *FileTransportConfig) maxFailures() int {
	if c.MaxFailures > 0 {
		return c.MaxFailures
	}
	return fileDefaultMaxFailures
}

// fileClientState holds per-client state on the server side.
type fileClientState struct {
	inBuffer     *SimplexBuffer
	outBuffer    *SimplexBuffer
	addr         *SimplexAddr
	cancel       context.CancelFunc
	lastActivity time.Time
}

// FileTransportServer implements file-based polling server logic.
type FileTransportServer struct {
	ops      FileStorageOps
	prefix   string // directory/key prefix
	cfg      FileTransportConfig
	clients  sync.Map // clientID → *fileClientState
	baseline map[string]bool // client IDs that existed at startup (ignored)
	addr     *SimplexAddr
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewFileTransportServer creates a new file transport server (does NOT start polling).
func NewFileTransportServer(ops FileStorageOps, prefix string, cfg FileTransportConfig,
	addr *SimplexAddr, ctx context.Context, cancel context.CancelFunc) *FileTransportServer {
	s := &FileTransportServer{
		ops:    ops,
		prefix: prefix,
		cfg:    cfg,
		addr:   addr,
		ctx:    ctx,
		cancel: cancel,
	}

	// Snapshot existing client files at startup.  These are leftovers from
	// the previous server instance — processing them would create ghost
	// sessions because the new server has no ARQ state for old clients.
	s.baseline = s.snapshotExistingClients()
	if len(s.baseline) > 0 {
		logs.Log.Infof("%s baseline: ignoring %d pre-existing client(s)", cfg.LogPrefix, len(s.baseline))
		// Notify old clients to reconnect immediately via RST.
		go s.notifyOldClientsOfRestart()
	}

	return s
}

// snapshotExistingClients lists the directory and returns a set of clientIDs
// whose _send.txt files already exist.
func (s *FileTransportServer) snapshotExistingClients() map[string]bool {
	if s.ops == nil {
		return nil
	}
	folderPath := strings.TrimRight(s.prefix, "/")
	files, err := s.ops.ListFiles(folderPath)
	if err != nil {
		return nil
	}
	existing := make(map[string]bool)
	for _, name := range files {
		if strings.HasSuffix(name, s.cfg.SendSuffix) {
			clientID := strings.TrimSuffix(name, s.cfg.SendSuffix)
			if clientID != "" {
				existing[clientID] = true
			}
		}
	}
	return existing
}

// notifyOldClientsOfRestart writes an RST packet to each baseline client's
// recv file, triggering the client to close its session and reconnect with a
// fresh identity.  Best-effort: errors are logged and skipped.
func (s *FileTransportServer) notifyOldClientsOfRestart() {
	rstPkt := NewSimplexPacket(SimplexPacketTypeRST, nil)
	pkts := &SimplexPackets{Packets: []*SimplexPacket{rstPkt}}
	data := pkts.Marshal()

	for clientID := range s.baseline {
		recvFile := s.prefix + clientID + s.cfg.RecvSuffix
		if err := s.ops.WriteFile(recvFile, data); err != nil {
			logs.Log.Debugf("%s RST to %s failed: %v", s.cfg.LogPrefix, clientID, err)
			continue
		}
		logs.Log.Infof("%s Sent RST to old client %s", s.cfg.LogPrefix, clientID)
	}
}

// StartPolling starts the background polling goroutine.
func (s *FileTransportServer) StartPolling() {
	go s.polling()
}

func (s *FileTransportServer) polling() {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.scanForNewClients()
		}
	}
}

func (s *FileTransportServer) scanForNewClients() {
	// ListFiles expects a folder path (no trailing separator).
	// s.prefix includes trailing "/" for file path construction, so trim it here.
	folderPath := strings.TrimRight(s.prefix, "/")
	files, err := s.ops.ListFiles(folderPath)
	if err != nil {
		logs.Log.Debugf("%s Failed to list files in %s: %v", s.cfg.LogPrefix, s.prefix, err)
		return
	}

	for _, name := range files {
		if !strings.HasSuffix(name, s.cfg.SendSuffix) {
			continue
		}

		clientID := strings.TrimSuffix(name, s.cfg.SendSuffix)
		if clientID == "" {
			continue
		}

		// Skip clients that existed before this server started.
		// Their send files are leftovers — we already sent them RST.
		if s.baseline[clientID] {
			continue
		}

		if _, exists := s.clients.Load(clientID); exists {
			continue
		}

		clientCtx, clientCancel := context.WithCancel(s.ctx)
		clientAddr := generateAddrFromPath(clientID, s.addr)
		state := &fileClientState{
			inBuffer:     NewSimplexBuffer(clientAddr),
			outBuffer:    NewSimplexBuffer(clientAddr),
			addr:         clientAddr,
			cancel:       clientCancel,
			lastActivity: time.Now(),
		}
		s.clients.Store(clientID, state)

		logs.Log.Infof("%s New client connected: clientID=%s", s.cfg.LogPrefix, clientID)

		go s.handleClient(clientCtx, clientID, state)
	}
}

func (s *FileTransportServer) handleClient(ctx context.Context, clientID string, state *fileClientState) {
	sendFile := s.prefix + clientID + s.cfg.SendSuffix
	recvFile := s.prefix + clientID + s.cfg.RecvSuffix
	idleTimeout := s.cfg.Interval * time.Duration(s.cfg.idleMultiplier())

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	defer s.cleanupClient(clientID)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Idle timeout check
			if idleTimeout > 0 && time.Since(state.lastActivity) > idleTimeout {
				logs.Log.Infof("%s Client %s idle for %v, cleaning up", s.cfg.LogPrefix, clientID, idleTimeout)
				return
			}

			// Read: check if client uploaded send file
			if fileContent, err := s.ops.ReadFile(sendFile); err == nil {
				state.lastActivity = time.Now()
				if packets, err := ParseSimplexPackets(fileContent); err == nil {
					for _, pkt := range packets.Packets {
						state.inBuffer.PutPacket(pkt)
					}
				} else {
					logs.Log.Errorf("%s failed to parse simplex packets: %v", s.cfg.LogPrefix, err)
				}
				s.ops.DeleteFile(sendFile)
			}

			// Write: check if recv file has been consumed, then send pending data
			exists, err := s.ops.FileExists(recvFile)
			if err != nil {
				continue // FileExists error, skip this tick
			}
			if exists {
				continue // recv file still exists, wait for client to consume
			}

			// Drain outBuffer, respect MaxBodySize
			var allPkts []*SimplexPacket
			totalSize := 0
			for {
				pkt, err := state.outBuffer.GetPacket()
				if err != nil || pkt == nil {
					break
				}
				pktSize := pkt.Size()
				if totalSize+pktSize > s.cfg.MaxBodySize && len(allPkts) > 0 {
					// Over limit, put back
					state.outBuffer.PutPacket(pkt)
					break
				}
				allPkts = append(allPkts, pkt)
				totalSize += pktSize
			}
			if len(allPkts) > 0 {
				state.lastActivity = time.Now()
				packets := &SimplexPackets{Packets: allPkts}
				data := packets.Marshal()
				if err := s.ops.WriteFile(recvFile, data); err != nil {
					logs.Log.Errorf("%s Failed to write to recv file %s: %v", s.cfg.LogPrefix, recvFile, err)
					// Put-back all packets on failure (Fix #2)
					for _, pkt := range allPkts {
						state.outBuffer.PutPacket(pkt)
					}
				}
			}
		}
	}
}

// cleanupClient removes a client handler from the clients map using LoadAndDelete,
// ensuring scanForNewClients can create a new handler for the same clientID.
func (s *FileTransportServer) cleanupClient(clientID string) {
	if stateInterface, loaded := s.clients.LoadAndDelete(clientID); loaded {
		if state, ok := stateInterface.(*fileClientState); ok && state.cancel != nil {
			state.cancel()
		}
		logs.Log.Infof("%s Cleaned up handler for client %s", s.cfg.LogPrefix, clientID)
	}
}

// Receive returns the first available packet from any connected client's inBuffer.
func (s *FileTransportServer) Receive() (*SimplexPacket, *SimplexAddr, error) {
	select {
	case <-s.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
	}

	var pkt *SimplexPacket
	var addr *SimplexAddr

	s.clients.Range(func(_, value interface{}) bool {
		state := value.(*fileClientState)
		if p, err := state.inBuffer.GetPacket(); err == nil && p != nil {
			pkt = p
			addr = state.addr
			return false
		}
		return true
	})

	return pkt, addr, nil
}

// Send routes packets to the specified client's outBuffer.
func (s *FileTransportServer) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	if pkts == nil || pkts.Size() == 0 {
		return 0, nil
	}

	clientID := addr.Path
	if clientID == "" {
		return 0, fmt.Errorf("client ID not found in addr")
	}

	if stateInterface, exists := s.clients.Load(clientID); exists {
		state := stateInterface.(*fileClientState)
		totalSize := 0
		for _, pkt := range pkts.Packets {
			state.outBuffer.PutPacket(pkt)
			totalSize += len(pkt.Data)
		}
		return totalSize, nil
	}

	return 0, fmt.Errorf("client state not found for clientID %s", clientID)
}

// CloseAll cancels all client handler goroutines.
func (s *FileTransportServer) CloseAll() {
	s.clients.Range(func(key, value interface{}) bool {
		if state, ok := value.(*fileClientState); ok && state.cancel != nil {
			state.cancel()
		}
		return true
	})
}

// FileTransportClient implements file-based polling client logic.
type FileTransportClient struct {
	ops         FileStorageOps
	cfg         FileTransportConfig
	buffer      *SimplexBuffer
	sendFile    string
	recvFile    string
	pendingMu   sync.Mutex
	pendingData []byte
	addr        *SimplexAddr
	ctx         context.Context
	cancel      context.CancelFunc
}

// NewFileTransportClient creates a new file transport client (does NOT start monitoring).
func NewFileTransportClient(ops FileStorageOps, cfg FileTransportConfig,
	buffer *SimplexBuffer, sendFile, recvFile string,
	addr *SimplexAddr, ctx context.Context, cancel context.CancelFunc) *FileTransportClient {
	return &FileTransportClient{
		ops:      ops,
		cfg:      cfg,
		buffer:   buffer,
		sendFile: sendFile,
		recvFile: recvFile,
		addr:     addr,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// StartMonitoring starts the background monitoring goroutine.
func (c *FileTransportClient) StartMonitoring() {
	go c.monitoring()
}

func (c *FileTransportClient) monitoring() {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	consecutiveFailures := 0
	sendStallCount := 0
	recvFailures := 0
	maxFailures := c.cfg.maxFailures()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// Send direction
			c.pendingMu.Lock()
			hasPending := len(c.pendingData) > 0
			var toSend []byte
			if hasPending {
				if len(c.pendingData) > c.cfg.MaxBodySize {
					toSend = c.pendingData[:c.cfg.MaxBodySize]
					c.pendingData = c.pendingData[c.cfg.MaxBodySize:]
				} else {
					toSend = c.pendingData
					c.pendingData = nil
				}
			}
			c.pendingMu.Unlock()

			if hasPending {
				exists, err := c.ops.FileExists(c.sendFile)
				if err != nil {
					// FileExists API error, defer send
					logs.Log.Debugf("%s FileExists failed for %s: %v", c.cfg.LogPrefix, c.sendFile, err)
					c.pendingMu.Lock()
					c.pendingData = append(toSend, c.pendingData...)
					c.pendingMu.Unlock()
				} else if !exists {
					// Send file doesn't exist (server consumed), safe to upload
					sendStallCount = 0
					if err := c.ops.WriteFile(c.sendFile, toSend); err != nil {
						logs.Log.Warnf("%s WriteFile failed (%d bytes): %v", c.cfg.LogPrefix, len(toSend), err)
						consecutiveFailures++
						if consecutiveFailures >= maxFailures {
							logs.Log.Errorf("%s %d consecutive send failures, closing client", c.cfg.LogPrefix, consecutiveFailures)
							c.cancel()
							return
						}
						c.pendingMu.Lock()
						c.pendingData = append(toSend, c.pendingData...)
						c.pendingMu.Unlock()
					} else {
						consecutiveFailures = 0
						logs.Log.Debugf("%s Uploaded %d bytes to %s", c.cfg.LogPrefix, len(toSend), c.sendFile)
					}
				} else {
					// Send file still exists (server hasn't read), defer
					sendStallCount++
					if sendStallCount >= maxFailures {
						logs.Log.Errorf("%s sendFile stale for %d ticks, server unreachable, closing", c.cfg.LogPrefix, sendStallCount)
						c.cancel()
						return
					}
					logs.Log.Debugf("%s sendFile still exists, deferring %d bytes (%d/%d)", c.cfg.LogPrefix, len(toSend), sendStallCount, maxFailures)
					c.pendingMu.Lock()
					c.pendingData = append(toSend, c.pendingData...)
					c.pendingMu.Unlock()
				}
			}

			// Recv direction
			if fileContent, err := c.ops.ReadFile(c.recvFile); err == nil {
				recvFailures = 0
				if packets, err := ParseSimplexPackets(fileContent); err == nil {
					for _, pkt := range packets.Packets {
						c.buffer.PutPacket(pkt)
					}
				} else {
					logs.Log.Errorf("%s failed to parse simplex packets: %v", c.cfg.LogPrefix, err)
				}
				c.ops.DeleteFile(c.recvFile)
			} else if !errors.Is(err, os.ErrNotExist) {
				// Real API error (not 404), count and close if over threshold (Fix #3)
				recvFailures++
				logs.Log.Warnf("%s recv ReadFile error (%d/%d): %v", c.cfg.LogPrefix, recvFailures, maxFailures, err)
				if recvFailures >= maxFailures {
					logs.Log.Errorf("%s %d consecutive recv failures, closing client", c.cfg.LogPrefix, recvFailures)
					c.cancel()
					return
				}
			}
		}
	}
}

// Receive returns a packet from the client's receive buffer.
func (c *FileTransportClient) Receive() (*SimplexPacket, *SimplexAddr, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
		pkt, err := c.buffer.GetPacket()
		return pkt, c.addr, err
	}
}

// Send appends serialized packets to the pending data queue.
func (c *FileTransportClient) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
	}

	if pkts == nil || pkts.Size() == 0 {
		return 0, nil
	}

	data := pkts.Marshal()

	c.pendingMu.Lock()
	c.pendingData = append(c.pendingData, data...)
	c.pendingMu.Unlock()

	return len(data), nil
}
