package simplex

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/logs"
)

// findPacketBoundary walks TLV packet headers in data and returns the largest
// offset ≤ maxSize that falls on a packet boundary (i.e. no partial packet).
// If a single packet exceeds maxSize, it is included whole to avoid deadlock.
func findPacketBoundary(data []byte, maxSize int) int {
	offset := 0
	for offset < len(data) {
		if offset+5 > len(data) {
			break
		}
		pktDataLen := int(binary.LittleEndian.Uint32(data[offset+1 : offset+5]))
		next := offset + 5 + pktDataLen
		if next > maxSize && offset > 0 {
			break
		}
		offset = next
		if offset >= maxSize {
			break
		}
	}
	if offset == 0 && len(data) > 0 {
		offset = len(data)
	}
	return offset
}

const (
	// fileHandlerIdleMultiplier × polling interval = idle timeout for handleClient.
	// Production (2s interval): 150 × 2s = 5min; Test (50ms): 150 × 50ms = 7.5s
	fileHandlerIdleMultiplier = 150

	fileDefaultMaxFailures = 10
)

// FileStorageOps abstracts cloud file storage operations (cloud storage / OSS / etc).
type FileStorageOps interface {
	ReadFile(path string) ([]byte, error)      // Read file; 404 → os.ErrNotExist
	WriteFile(path string, data []byte) error  // Create/overwrite file
	DeleteFile(path string) error              // Delete file
	ListFiles(prefix string) ([]string, error) // List file names under prefix
	FileExists(path string) (bool, error)      // Lightweight existence check
}

// seqFilename generates a deterministic filename from seed + direction + sequence number.
// Both client and server compute the same name for the same (seed, seq) pair.
func seqFilename(prefix, clientID, seed, direction string, seq int) string {
	return fmt.Sprintf("%s%s.%s.%s.%016x", prefix, clientID, direction, seqFileHash(seed, direction, seq), uint64(seq))
}

const seqMaxPendingFiles = 50
const seqMaxOpsPerTick = 5

// FileTransportConfig parameterizes file transport behavior.
type FileTransportConfig struct {
	SendSuffix              string // e.g. "_send.txt" or "_send"
	RecvSuffix              string // e.g. "_recv.txt" or "_recv"
	Interval                time.Duration
	MaxBodySize             int
	IdleMultiplier          int // handler idle exit multiplier; 0 = use default (fileHandlerIdleMultiplier)
	IdleTimeout             time.Duration
	MaxFailures             int    // consecutive failure limit; 0 = use default (fileDefaultMaxFailures)
	LogPrefix               string // e.g. "[cloud storage]" or "[OSS-File]"
	SequenceMode            bool   // true = seed-based sequenced filenames (non-blocking pipeline)
	SequenceMaxPendingFiles int    // seq mode upload backlog guard; 0 = seqMaxPendingFiles
	SkipBaseline            bool   // true when an outer dispatcher already owns stale-session filtering
}

func (c *FileTransportConfig) idleMultiplier() int {
	if c.IdleMultiplier > 0 {
		return c.IdleMultiplier
	}
	return fileHandlerIdleMultiplier
}

func (c *FileTransportConfig) idleTimeout() time.Duration {
	if c.IdleTimeout > 0 {
		return c.IdleTimeout
	}
	if c.Interval <= 0 {
		return 0
	}
	return c.Interval * time.Duration(c.idleMultiplier())
}

func (c *FileTransportConfig) maxFailures() int {
	if c.MaxFailures > 0 {
		return c.MaxFailures
	}
	return fileDefaultMaxFailures
}

func (c *FileTransportConfig) seqMaxPendingFiles() int {
	if c.SequenceMaxPendingFiles > 0 {
		return c.SequenceMaxPendingFiles
	}
	return seqMaxPendingFiles
}

// fileClientState holds per-client state on the server side.
type fileClientState struct {
	inBuffer      *SimplexBuffer
	outBuffer     *SimplexBuffer
	addr          *SimplexAddr
	cancel        context.CancelFunc
	activityMu    sync.Mutex
	lastActivity  time.Time
	readSeq       int              // sequence mode: next expected send-file seq
	writeSeq      int              // sequence mode: next recv-file seq to assign
	seed          string           // sequence mode: shared seed for filename generation
	pendingWrites []seqPendingFile // sequence mode: server-to-client files waiting for successful upload
	readSeen      map[int]bool     // sequence mode: out-of-order CTRL files already consumed
	readyMu       sync.Mutex       // protects readySeqFiles
	readySeqFiles map[int]string   // sequence mode: files found by the server directory scan
}

func (s *fileClientState) touchActivity() {
	s.activityMu.Lock()
	s.lastActivity = time.Now()
	s.activityMu.Unlock()
}

func (s *fileClientState) activity() time.Time {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	return s.lastActivity
}

// seqProgress stores sequence counters so a restarted handler can resume.
type seqProgress struct {
	readSeq  int
	writeSeq int
}

type seqPendingFile struct {
	seq     int
	data    []byte
	control bool
}

type seqFileRef struct {
	clientID  string
	direction string
	seq       int
	name      string
}

func seqFileHash(seed, direction string, seq int) string {
	h := fnv.New32a()
	io.WriteString(h, seed)
	io.WriteString(h, direction)
	binary.Write(h, binary.LittleEndian, int64(seq))
	return fmt.Sprintf("%08x", h.Sum32())
}

func parseSeqFilename(name, seed string) (seqFileRef, bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 4 {
		return seqFileRef{}, false
	}
	seqPart := parts[len(parts)-1]
	hashPart := parts[len(parts)-2]
	direction := parts[len(parts)-3]
	clientID := strings.Join(parts[:len(parts)-3], ".")
	if clientID == "" || (direction != "s" && direction != "r") || len(hashPart) != 8 {
		return seqFileRef{}, false
	}
	seq64, err := strconv.ParseUint(seqPart, 16, 63)
	if err != nil {
		return seqFileRef{}, false
	}
	seq := int(seq64)
	if hashPart != seqFileHash(seed, direction, seq) {
		return seqFileRef{}, false
	}
	return seqFileRef{
		clientID:  clientID,
		direction: direction,
		seq:       seq,
		name:      name,
	}, true
}

func markSeqConsumed(base *int, seen map[int]bool, seq int) {
	if seq == *base {
		*base = *base + 1
		for seen != nil && seen[*base] {
			delete(seen, *base)
			*base = *base + 1
		}
		return
	}
	if seen != nil {
		seen[seq] = true
	}
}

func simplexPacketsAllControl(packets *SimplexPackets) bool {
	if packets == nil || len(packets.Packets) == 0 {
		return true
	}
	for _, pkt := range packets.Packets {
		if pkt == nil {
			continue
		}
		if pkt.PacketType != SimplexPacketTypeCTRL {
			return false
		}
	}
	return true
}

type seqReadResult struct {
	seq  int
	name string
	data []byte
	err  error
}

type seqReadCandidate struct {
	seq  int
	name string
}

func appendPendingPackets(ctrl, data *[]byte, pkts *SimplexPackets) int {
	total := 0
	if pkts == nil {
		return 0
	}
	for _, pkt := range pkts.Packets {
		if pkt == nil {
			continue
		}
		raw := pkt.Marshal()
		total += len(raw)
		if pkt.PacketType == SimplexPacketTypeCTRL {
			*ctrl = append(*ctrl, raw...)
		} else {
			*data = append(*data, raw...)
		}
	}
	return total
}

func popPendingBytes(queue *[]byte, maxSize int) []byte {
	if len(*queue) == 0 {
		return nil
	}
	cutoff := len(*queue)
	if len(*queue) > maxSize {
		cutoff = findPacketBoundary(*queue, maxSize)
	}
	chunk := append([]byte(nil), (*queue)[:cutoff]...)
	*queue = (*queue)[cutoff:]
	return chunk
}

func pushPendingBytes(queue *[]byte, data []byte) {
	if len(data) == 0 {
		return
	}
	next := make([]byte, 0, len(data)+len(*queue))
	next = append(next, data...)
	next = append(next, (*queue)...)
	*queue = next
}

func selectSeqWriteBatch(files []seqPendingFile, limit int) []seqPendingFile {
	if limit <= 0 || len(files) == 0 {
		return nil
	}
	batch := make([]seqPendingFile, 0, limit)
	for _, file := range files {
		if !file.control {
			continue
		}
		batch = append(batch, file)
		if len(batch) == limit {
			return batch
		}
	}
	for _, file := range files {
		if file.control {
			continue
		}
		batch = append(batch, file)
		if len(batch) == limit {
			return batch
		}
	}
	return batch
}

func deleteSeqFileAsync(ops FileStorageOps, logPrefix, name string) {
	go func() {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if err = ops.DeleteFile(name); err == nil {
				return
			}
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
		logs.Log.Debugf("%s delete %s failed: %v", logPrefix, name, err)
	}()
}

// readSeqFilesParallel uses LIST to discover available seq files, then reads
// them in parallel. LIST acts as a readiness check — only files that exist
// are read, avoiding 404 probes on high-latency cloud APIs.
// In single-file mode or when the caller already knows filenames, use
// readSeqCandidatesParallel directly.
func readSeqFilesParallel(
	ops FileStorageOps, logPrefix string,
	prefix, clientID, seed, direction string,
	baseSeq *int, seen map[int]bool,
	buf *SimplexBuffer,
) int {
	folderPath := strings.TrimRight(prefix, "/")
	files, err := ops.ListFiles(folderPath)
	if err != nil {
		logs.Log.Debugf("%s failed to list seq files in %s: %v", logPrefix, prefix, err)
		return 0
	}

	available := make([]seqReadCandidate, 0, len(files))
	for _, name := range files {
		ref, ok := parseSeqFilename(name, seed)
		if !ok || ref.clientID != clientID || ref.direction != direction {
			continue
		}
		fullName := prefix + name
		if ref.seq < *baseSeq || (seen != nil && seen[ref.seq]) {
			deleteSeqFileAsync(ops, logPrefix, fullName)
			continue
		}
		available = append(available, seqReadCandidate{seq: ref.seq, name: fullName})
	}
	processed, _ := readSeqCandidatesParallel(ops, logPrefix, available, baseSeq, seen, buf)
	return processed
}

func readSeqCandidatesParallel(
	ops FileStorageOps, logPrefix string,
	available []seqReadCandidate,
	baseSeq *int, seen map[int]bool,
	buf *SimplexBuffer,
) (int, map[int]bool) {
	sort.Slice(available, func(i, j int) bool {
		return available[i].seq < available[j].seq
	})
	if len(available) > seqMaxOpsPerTick {
		available = available[:seqMaxOpsPerTick]
	}
	if len(available) == 0 {
		return 0, nil
	}

	results := make([]seqReadResult, len(available))
	var wg sync.WaitGroup
	for i, ref := range available {
		results[i] = seqReadResult{seq: ref.seq, name: ref.name}
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			results[idx].data, results[idx].err = ops.ReadFile(name)
		}(i, ref.name)
	}
	wg.Wait()

	processed := 0
	remove := make(map[int]bool)
	for i := range available {
		r := results[i]
		if r.err != nil {
			continue
		}
		if packets, err := ParseSimplexPackets(r.data); err == nil {
			if r.seq != *baseSeq && !simplexPacketsAllControl(packets) {
				continue
			}
			for _, pkt := range packets.Packets {
				buf.PutPacket(pkt)
			}
		} else {
			logs.Log.Errorf("%s failed to parse seq file %s: %v", logPrefix, r.name, err)
		}
		deleteSeqFileAsync(ops, logPrefix, r.name)
		remove[r.seq] = true
		markSeqConsumed(baseSeq, seen, r.seq)
		processed++
	}
	return processed, remove
}

// FileTransportServer implements file-based polling server logic.
type FileTransportServer struct {
	ops      FileStorageOps
	prefix   string // directory/key prefix
	cfg      FileTransportConfig
	clients  sync.Map        // clientID → *fileClientState
	baseline map[string]bool // client IDs that existed at startup (ignored)
	seqState sync.Map        // seq mode: clientID → *seqProgress (survives handler restarts)
	addr     *SimplexAddr
	ctx      context.Context
	cancel   context.CancelFunc

	onClientClosedMu sync.Mutex
	onClientClosed   func(*SimplexAddr)
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
	if !cfg.SkipBaseline {
		s.baseline = s.snapshotExistingClients()
		if len(s.baseline) > 0 {
			logs.Log.Infof("%s baseline: ignoring %d pre-existing client(s)", cfg.LogPrefix, len(s.baseline))
		}
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
	seed := s.deriveSeed()
	for _, name := range files {
		var clientID string
		if s.cfg.SequenceMode {
			if ref, ok := parseSeqFilename(name, seed); ok {
				clientID = ref.clientID
			}
		} else {
			if strings.HasSuffix(name, s.cfg.SendSuffix) {
				clientID = strings.TrimSuffix(name, s.cfg.SendSuffix)
			}
		}
		if clientID != "" {
			existing[clientID] = true
		}
	}
	return existing
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
	folderPath := strings.TrimRight(s.prefix, "/")
	files, err := s.ops.ListFiles(folderPath)
	if err != nil {
		logs.Log.Debugf("%s Failed to list files in %s: %v", s.cfg.LogPrefix, s.prefix, err)
		return
	}

	seen := make(map[string]bool)
	seed := s.deriveSeed()

	for _, name := range files {
		var clientID string
		var ref seqFileRef

		if s.cfg.SequenceMode {
			// Sequence mode: filenames look like
			// "clientID.direction.hash8.seq16"; only client uploads ("s")
			// create new server-side sessions.
			parsed, ok := parseSeqFilename(name, seed)
			if !ok || parsed.direction != "s" {
				continue
			}
			ref = parsed
			clientID = ref.clientID
		} else {
			// Single mode: filenames look like "clientID_send"
			if !strings.HasSuffix(name, s.cfg.SendSuffix) {
				continue
			}
			clientID = strings.TrimSuffix(name, s.cfg.SendSuffix)
		}

		if clientID == "" {
			continue
		}
		if !s.cfg.SequenceMode {
			if seen[clientID] {
				continue
			}
			seen[clientID] = true
		}

		if s.baseline[clientID] {
			continue
		}
		if stateInterface, exists := s.clients.Load(clientID); exists {
			if s.cfg.SequenceMode {
				state := stateInterface.(*fileClientState)
				state.readyMu.Lock()
				if state.readySeqFiles == nil {
					state.readySeqFiles = make(map[int]string)
				}
				state.readySeqFiles[ref.seq] = s.prefix + ref.name
				state.readyMu.Unlock()
			}
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

		if s.cfg.SequenceMode {
			state.seed = s.deriveSeed()
			state.readSeen = make(map[int]bool)
			state.readySeqFiles = make(map[int]string)
			state.readySeqFiles[ref.seq] = s.prefix + ref.name
			if prev, ok := s.seqState.Load(clientID); ok {
				p := prev.(*seqProgress)
				state.readSeq = p.readSeq
				state.writeSeq = p.writeSeq
			}
		}

		s.clients.Store(clientID, state)
		logs.Log.Infof("%s New client connected: clientID=%s", s.cfg.LogPrefix, clientID)
		go s.handleClient(clientCtx, clientID, state)
	}
}

func (s *FileTransportServer) deriveSeed() string {
	return s.prefix
}

func (s *FileTransportServer) handleClient(ctx context.Context, clientID string, state *fileClientState) {
	if s.cfg.SequenceMode {
		s.handleClientSequence(ctx, clientID, state)
	} else {
		s.handleClientSingle(ctx, clientID, state)
	}
}

func (s *FileTransportServer) handleClientSingle(ctx context.Context, clientID string, state *fileClientState) {
	sendFile := s.prefix + clientID + s.cfg.SendSuffix
	recvFile := s.prefix + clientID + s.cfg.RecvSuffix
	idleTimeout := s.cfg.idleTimeout()

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	defer s.cleanupClient(clientID)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if idleTimeout > 0 && time.Since(state.activity()) > idleTimeout {
				logs.Log.Infof("%s Client %s idle for %v, cleaning up", s.cfg.LogPrefix, clientID, idleTimeout)
				return
			}

			if fileContent, err := s.ops.ReadFile(sendFile); err == nil {
				state.touchActivity()
				if packets, err := ParseSimplexPackets(fileContent); err == nil {
					for _, pkt := range packets.Packets {
						state.inBuffer.PutPacket(pkt)
					}
				} else {
					logs.Log.Errorf("%s failed to parse simplex packets: %v", s.cfg.LogPrefix, err)
				}
				s.ops.DeleteFile(sendFile)
			}

			exists, err := s.ops.FileExists(recvFile)
			if err != nil {
				continue
			}
			if exists {
				continue
			}

			allPkts := s.drainOutBuffer(state)
			if len(allPkts) > 0 {
				data := (&SimplexPackets{Packets: allPkts}).Marshal()
				if err := s.ops.WriteFile(recvFile, data); err != nil {
					logs.Log.Errorf("%s Failed to write to recv file %s: %v", s.cfg.LogPrefix, recvFile, err)
					for _, pkt := range allPkts {
						state.outBuffer.PutPacket(pkt)
					}
				}
			}
		}
	}
}

func (s *FileTransportServer) handleClientSequence(ctx context.Context, clientID string, state *fileClientState) {
	idleTimeout := s.cfg.idleTimeout()
	defer s.cleanupClient(clientID)

	go func() {
		readTicker := time.NewTicker(s.cfg.Interval)
		defer readTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-readTicker.C:
				if s.readSequenceFiles(clientID, state) > 0 {
					state.touchActivity()
				}
			}
		}
	}()

	writeTicker := time.NewTicker(s.cfg.Interval)
	defer writeTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-writeTicker.C:
			if idleTimeout > 0 && time.Since(state.activity()) > idleTimeout {
				logs.Log.Infof("%s Client %s idle for %v, cleaning up", s.cfg.LogPrefix, clientID, idleTimeout)
				return
			}
			s.writeSequenceFiles(clientID, state)
		}
	}
}

func (s *FileTransportServer) readSequenceFiles(clientID string, state *fileClientState) int {
	if state.readySeqFiles == nil {
		return readSeqFilesParallel(
			s.ops, s.cfg.LogPrefix,
			s.prefix, clientID, state.seed, "s",
			&state.readSeq, state.readSeen,
			state.inBuffer,
		)
	}

	state.readyMu.Lock()
	available := make([]seqReadCandidate, 0, len(state.readySeqFiles))
	for seq, name := range state.readySeqFiles {
		if seq < state.readSeq || state.readSeen[seq] {
			deleteSeqFileAsync(s.ops, s.cfg.LogPrefix, name)
			delete(state.readySeqFiles, seq)
			continue
		}
		available = append(available, seqReadCandidate{seq: seq, name: name})
	}
	state.readyMu.Unlock()

	processed, remove := readSeqCandidatesParallel(s.ops, s.cfg.LogPrefix, available, &state.readSeq, state.readSeen, state.inBuffer)
	if len(remove) > 0 {
		state.readyMu.Lock()
		for seq := range remove {
			delete(state.readySeqFiles, seq)
		}
		state.readyMu.Unlock()
	}
	return processed
}

func (s *FileTransportServer) writeSequenceFiles(clientID string, state *fileClientState) int {
	opsPerTick := seqMaxOpsPerTick
	if ctrl, _ := state.outBuffer.GetControlPackets(); ctrl != nil && ctrl.Size() > 0 {
		state.pendingWrites = append(state.pendingWrites, seqPendingFile{
			seq:     state.writeSeq,
			data:    ctrl.Marshal(),
			control: true,
		})
		state.writeSeq++
	}
	for len(state.pendingWrites) < opsPerTick {
		allPkts := s.drainOutBuffer(state)
		if len(allPkts) == 0 {
			break
		}
		data := (&SimplexPackets{Packets: allPkts}).Marshal()
		state.pendingWrites = append(state.pendingWrites, seqPendingFile{seq: state.writeSeq, data: data})
		state.writeSeq++
	}
	if len(state.pendingWrites) == 0 {
		return 0
	}

	batch := selectSeqWriteBatch(state.pendingWrites, opsPerTick)
	success := s.writeSequenceBatch(clientID, state.seed, "r", batch)
	if len(success) == 0 {
		return 0
	}

	state.pendingWrites = compactSuccessfulSeqFiles(state.pendingWrites, success)
	return len(success)
}

func (s *FileTransportServer) writeSequenceBatch(clientID, seed, direction string, batch []seqPendingFile) map[int]bool {
	success := make(map[int]bool, len(batch))
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, item := range batch {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			fname := seqFilename(s.prefix, clientID, seed, direction, item.seq)
			if err := s.ops.WriteFile(fname, item.data); err != nil {
				logs.Log.Errorf("%s failed to write seq %s %d: %v", s.cfg.LogPrefix, direction, item.seq, err)
				return
			}
			mu.Lock()
			success[item.seq] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	return success
}

func (s *FileTransportServer) drainOutBuffer(state *fileClientState) []*SimplexPacket {
	var allPkts []*SimplexPacket
	totalSize := 0
	for {
		pkt, err := state.outBuffer.GetPacket()
		if err != nil || pkt == nil {
			break
		}
		pktSize := pkt.Size()
		if totalSize+pktSize > s.cfg.MaxBodySize && len(allPkts) > 0 {
			state.outBuffer.PutPacket(pkt)
			break
		}
		allPkts = append(allPkts, pkt)
		totalSize += pktSize
	}
	return allPkts
}

// cleanupClient removes a client handler from the clients map using LoadAndDelete,
// ensuring scanForNewClients can create a new handler for the same clientID.
// In sequence mode, it also deletes all residual Blob files for the client
// to avoid phantom reconnections on the next directory scan.
func (s *FileTransportServer) cleanupClient(clientID string) {
	if stateInterface, loaded := s.clients.LoadAndDelete(clientID); loaded {
		if state, ok := stateInterface.(*fileClientState); ok {
			if s.cfg.SequenceMode {
				s.seqState.Store(clientID, &seqProgress{
					readSeq:  state.readSeq,
					writeSeq: state.writeSeq,
				})
				go s.deleteClientFiles(clientID)
			}
			state.inBuffer.Close()
			state.outBuffer.Close()
			if state.cancel != nil {
				state.cancel()
			}
			s.notifyClientClosed(state.addr)
		}
		logs.Log.Infof("%s Cleaned up handler for client %s", s.cfg.LogPrefix, clientID)
	}
}

// deleteClientFiles removes all residual files for a dead client by listing
// everything under the client's prefix and deleting in bulk. In Azure Blob
// (and similar object stores) a "directory" is just a shared key prefix, so
// deleting all objects with that prefix is equivalent to removing the directory.
func (s *FileTransportServer) deleteClientFiles(clientID string) {
	clientPrefix := s.prefix + clientID
	files, err := s.ops.ListFiles(clientPrefix)
	if err != nil {
		return
	}
	for _, name := range files {
		deleteSeqFileAsync(s.ops, s.cfg.LogPrefix, clientPrefix+"/"+name)
	}
	if len(files) > 0 {
		logs.Log.Infof("%s deleted %d residual files for client %s", s.cfg.LogPrefix, len(files), clientID)
	}
}

func (s *FileTransportServer) SetOnClientClosed(fn func(*SimplexAddr)) {
	s.onClientClosedMu.Lock()
	s.onClientClosed = fn
	s.onClientClosedMu.Unlock()
}

func (s *FileTransportServer) notifyClientClosed(addr *SimplexAddr) {
	s.onClientClosedMu.Lock()
	fn := s.onClientClosed
	s.onClientClosedMu.Unlock()
	if fn != nil && addr != nil {
		fn(addr)
	}
}

func (s *FileTransportServer) ClientCount() int {
	count := 0
	s.clients.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

func (s *FileTransportServer) LastActivity() time.Time {
	var last time.Time
	s.clients.Range(func(_, value interface{}) bool {
		if state, ok := value.(*fileClientState); ok {
			if t := state.activity(); t.After(last) {
				last = t
			}
		}
		return true
	})
	return last
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
// If the peer handler no longer exists, the transport treats that peer as
// closed so the upper Simplex wrapper can stop retrying stale sends.
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
		state.touchActivity()
		totalSize := 0
		for _, pkt := range pkts.Packets {
			state.outBuffer.PutPacket(pkt)
			totalSize += len(pkt.Data)
		}
		return totalSize, nil
	}

	return 0, io.ErrClosedPipe
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
// Send() and Receive() do inline I/O (blocking), following the same pattern
// as cloud storage/HTTP transports. SimplexClient.polling() drives all timing.
type FileTransportClient struct {
	ops      FileStorageOps
	cfg      FileTransportConfig
	buffer   *SimplexBuffer
	sendFile string // single mode: fixed send path
	recvFile string // single mode: fixed recv path
	addr     *SimplexAddr
	ctx      context.Context
	cancel   context.CancelFunc
	// sequence mode fields
	prefix             string
	clientID           string
	seed               string
	sendSeq            int
	recvSeq            int
	highestUploadedSeq int
	recvSeen           map[int]bool
	closeOnce          sync.Once
}

// NewFileTransportClient creates a new file transport client.
func NewFileTransportClient(ops FileStorageOps, cfg FileTransportConfig,
	buffer *SimplexBuffer, sendFile, recvFile string,
	addr *SimplexAddr, ctx context.Context, cancel context.CancelFunc) *FileTransportClient {
	return &FileTransportClient{
		ops:                ops,
		cfg:                cfg,
		buffer:             buffer,
		sendFile:           sendFile,
		recvFile:           recvFile,
		addr:               addr,
		ctx:                ctx,
		cancel:             cancel,
		highestUploadedSeq: -1,
		recvSeen:           make(map[int]bool),
	}
}

// StartMonitoring is a no-op. Send/Receive do inline I/O driven by SimplexClient.polling().
func (c *FileTransportClient) StartMonitoring() {}

func (c *FileTransportClient) Close() error {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	return nil
}

func compactSuccessfulSeqFiles(files []seqPendingFile, success map[int]bool) []seqPendingFile {
	if len(success) == 0 {
		return files
	}
	next := files[:0]
	for _, file := range files {
		if !success[file.seq] {
			next = append(next, file)
		}
	}
	return next
}

// Receive reads packets from cloud storage inline (blocking).
// Buffer is checked first for leftover packets from a previous batch read.
func (c *FileTransportClient) Receive() (*SimplexPacket, *SimplexAddr, error) {
	select {
	case <-c.ctx.Done():
		return nil, nil, io.ErrClosedPipe
	default:
	}

	pkt, err := c.buffer.GetPacket()
	if err != nil {
		return nil, c.addr, err
	}
	if pkt != nil {
		return pkt, c.addr, nil
	}

	if c.cfg.SequenceMode {
		readSeqFilesParallel(
			c.ops, c.cfg.LogPrefix,
			c.prefix, c.clientID, c.seed, "r",
			&c.recvSeq, c.recvSeen,
			c.buffer,
		)
	} else {
		if fileContent, err := c.ops.ReadFile(c.recvFile); err == nil {
			if packets, err := ParseSimplexPackets(fileContent); err == nil {
				for _, p := range packets.Packets {
					c.buffer.PutPacket(p)
				}
			}
			c.ops.DeleteFile(c.recvFile)
		}
	}

	pkt, err = c.buffer.GetPacket()
	return pkt, c.addr, err
}

// Send writes packets to cloud storage inline (blocking).
func (c *FileTransportClient) Send(pkts *SimplexPackets, addr *SimplexAddr) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, io.ErrClosedPipe
	default:
	}

	if pkts == nil || pkts.Size() == 0 {
		return 0, nil
	}

	if c.cfg.SequenceMode {
		return c.sendSequence(pkts)
	}
	return c.sendSingle(pkts)
}

func (c *FileTransportClient) sendSingle(pkts *SimplexPackets) (int, error) {
	exists, err := c.ops.FileExists(c.sendFile)
	if err != nil {
		return 0, err
	}
	if exists {
		return 0, fmt.Errorf("sendFile stale, server not consuming")
	}
	data := pkts.Marshal()
	if err := c.ops.WriteFile(c.sendFile, data); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (c *FileTransportClient) sendSequence(pkts *SimplexPackets) (int, error) {
	var ctrlBytes, dataBytes []byte
	appendPendingPackets(&ctrlBytes, &dataBytes, pkts)

	baseSeq := c.sendSeq
	var files []seqPendingFile
	seq := baseSeq
	for len(ctrlBytes) > 0 {
		chunk := popPendingBytes(&ctrlBytes, c.cfg.MaxBodySize)
		files = append(files, seqPendingFile{seq: seq, data: chunk, control: true})
		seq++
	}
	for len(dataBytes) > 0 {
		chunk := popPendingBytes(&dataBytes, c.cfg.MaxBodySize)
		files = append(files, seqPendingFile{seq: seq, data: chunk})
		seq++
	}

	if len(files) == 0 {
		return 0, nil
	}

	maxPending := c.cfg.seqMaxPendingFiles()
	if c.highestUploadedSeq >= maxPending {
		oldSeq := c.highestUploadedSeq - maxPending + 1
		oldFile := seqFilename(c.prefix, c.clientID, c.seed, "s", oldSeq)
		if exists, _ := c.ops.FileExists(oldFile); exists {
			return 0, fmt.Errorf("server not consuming: %d uploaded files pending", maxPending)
		}
	}

	var wg sync.WaitGroup
	type writeResult struct {
		seq int
		err error
	}
	results := make([]writeResult, len(files))
	for i, item := range files {
		i, item := i, item
		results[i].seq = item.seq
		wg.Add(1)
		go func() {
			defer wg.Done()
			fname := seqFilename(c.prefix, c.clientID, c.seed, "s", item.seq)
			results[i].err = c.ops.WriteFile(fname, item.data)
		}()
	}
	wg.Wait()

	totalSent := 0
	for i, r := range results {
		if r.err != nil {
			// rollback: don't advance sendSeq so retry uses same seq numbers
			return totalSent, fmt.Errorf("seq write %d failed: %w", r.seq, r.err)
		}
		totalSent += len(files[i].data)
		if r.seq > c.highestUploadedSeq {
			c.highestUploadedSeq = r.seq
		}
	}
	// only advance sendSeq after all writes succeed
	c.sendSeq = seq
	return totalSent, nil
}
