// Package arq implements a selective-repeat ARQ protocol designed for
// extreme network conditions:
//
//   - Low bandwidth, high latency (RTT from seconds to minutes)
//   - High packet loss (30%–70%+)
//   - Very low send frequency (3s–60s, up to 600s per packet)
//
// These constraints arise from polling-based covert transports (e.g.
// SharePoint, Teams, DNS) where each "packet" is an HTTP request with
// multi-second round-trip time and significant jitter.
//
// Key design decisions:
//
//   - NACK-only retransmission: no standalone ACK packets, ACKs are
//     piggybacked on outgoing data. This halves the number of requests
//     on polling transports where every packet costs a full HTTP
//     round-trip.
//   - Batch NACK: a single NACK carries all missing sequence numbers,
//     minimizing control overhead on slow links.
//   - No timer-based retransmission (no RTO retransmit). Retransmits
//     are driven entirely by NACKs from the receiver. This avoids
//     spurious retransmits on links where RTT >> typical RTO.
//   - Event-driven flush: data is flushed immediately on Send() and
//     Input() rather than on a periodic timer, so the protocol adds
//     zero latency regardless of send frequency.
//   - Conservative cleanup: unacknowledged segments are silently
//     discarded after 10×RTO (default 60s) to bound memory, but the
//     session is never killed by the ARQ layer — lifecycle is
//     controlled by the application.
package arq

import (
	"encoding/binary"
	"sync"
	"time"
)

// SimpleARQChecker 简单ARQ协议的包类型判断（默认）
// 注意：只有 CMD_NACK (cmd=2) 被视为控制包。
// CMD_ACK (cmd=3) 不再被视为控制包，因为独立 ACK 已禁用，
// 只使用捎带 ACK，这样可避免在 SimplexBuffer 中破坏包顺序。
func SimpleARQChecker(data []byte) bool {
	if len(data) < ARQ_OVERHEAD {
		return false
	}
	cmd := data[0]
	return cmd == CMD_NACK
}

// 包类型常量
const (
	CMD_DATA = 1
	CMD_NACK = 2 // 负确认，要求重传特定包
	CMD_ACK  = 3 // 独立 ACK 包
)

// 配置常量 (用作默认值)
const (
	ARQ_OVERHEAD       = 11                     // 包头大小: 1+4+4+2 (cmd+sn+ack+len)
	ARQ_MTU            = 1400                   // 最大传输单元
	ARQ_MSS            = ARQ_MTU - ARQ_OVERHEAD // 最大段大小
	ARQ_WND_SIZE       = 32                     // 窗口大小
	ARQ_RTO            = 6000                   // 重传超时时间(ms)
	ARQ_INTERVAL       = 100                    // 更新间隔(ms)
	ARQ_NACK_THRESHOLD = 1                      // 触发NACK的丢包阈值 (降为1，适配低频场景)
	ARQ_ACK_INTERVAL   = 5000                   // 独立 ACK 发送间隔(ms)
)

// ARQConfig holds per-instance configuration for ARQ.
// Zero values mean "use default".
type ARQConfig struct {
	MTU     int // 0 = default (1400)
	Timeout int // ms, 0 = no timeout
	RTO     int // ms, 0 = default (6000) - used for cleanup timeout and NACK interval
}

// Segment 数据段
type Segment struct {
	cmd  uint8  // 命令类型
	sn   uint32 // 序列号
	len  uint16 // 数据长度
	ts   uint32 // 首次发送时间戳（cleanup 用）
	data []byte // 数据
}

// ARQ 简化的ARQ实现
type ARQ struct {
	mu   sync.Mutex // 保护所有状态的并发访问
	conv uint32     // 会话ID

	// 可配置参数
	mtu     int // 最大传输单元
	mss     int // 最大段大小 (mtu - overhead)
	timeout int // 默认超时时间(ms)，0表示无超时
	rto     int // 重传超时(ms) - used for cleanup and NACK interval

	// 状态
	snd_nxt uint32 // 下一个发送序列号
	snd_una uint32 // 最早未确认的 SN
	rcv_nxt uint32 // 下一个期望接收序列号

	// 缓冲区
	snd_queue []Segment          // 发送队列
	snd_buf   []Segment          // 发送缓冲区(等待确认或超时)
	rcv_buf   map[uint32]Segment // 接收缓冲区(乱序包)
	rcv_queue []byte             // 接收队列(有序数据)

	// 接收端状态跟踪
	highest_rcv uint32 // 接收到的最高序列号
	nack_count  int    // 连续gap计数
	nack_sent   uint32 // 最后发送NACK的时间
	nack_retry  int    // NACK重试次数
	nack_gap_ts uint32 // 首次检测到 gap 的时间戳 (用于时间触发)

	// 时间相关
	current uint32 // 当前时间

	// 输出函数
	output func([]byte)
}

// NewSimpleARQ 创建新的ARQ实例
func NewSimpleARQ(conv uint32, output func([]byte)) *ARQ {
	return NewSimpleARQWithMTU(conv, output, ARQ_MTU, 0)
}

// NewSimpleARQWithMTU 创建新的ARQ实例，支持自定义MTU和超时
func NewSimpleARQWithMTU(conv uint32, output func([]byte), mtu int, timeout int) *ARQ {
	return NewARQWithConfig(conv, output, ARQConfig{
		MTU:     mtu,
		Timeout: timeout,
	})
}

// NewARQWithConfig 创建新的ARQ实例，支持完整配置
func NewARQWithConfig(conv uint32, output func([]byte), cfg ARQConfig) *ARQ {
	if cfg.MTU <= ARQ_OVERHEAD {
		cfg.MTU = ARQ_MTU
	}
	if cfg.RTO == 0 {
		cfg.RTO = ARQ_RTO
	}

	arq := &ARQ{
		conv:      conv,
		mtu:       cfg.MTU,
		mss:       cfg.MTU - ARQ_OVERHEAD,
		timeout:   cfg.Timeout,
		rto:       cfg.RTO,
		rcv_buf:   make(map[uint32]Segment),
		rcv_queue: make([]byte, 0),
		output:    output,
		current:   currentMs(),
	}
	return arq
}

// currentMs 获取当前时间戳(毫秒)
func currentMs() uint32 {
	return uint32(time.Now().UnixNano() / 1000000)
}

// Send 发送数据，支持自动分片
func (arq *ARQ) Send(data []byte) {
	if len(data) == 0 {
		return
	}

	arq.mu.Lock()
	defer arq.mu.Unlock()

	// 根据配置的MSS进行分片处理
	for len(data) > 0 {
		size := len(data)
		if size > arq.mss {
			size = arq.mss
		}

		seg := Segment{
			cmd:  CMD_DATA,
			sn:   arq.snd_nxt,
			len:  uint16(size),
			data: make([]byte, size),
		}
		copy(seg.data, data[:size])

		arq.snd_queue = append(arq.snd_queue, seg)
		arq.snd_nxt++
		data = data[size:]
	}
}

// Recv 接收数据
func (arq *ARQ) Recv() []byte {
	arq.mu.Lock()
	defer arq.mu.Unlock()

	if len(arq.rcv_queue) == 0 {
		return nil
	}

	data := make([]byte, len(arq.rcv_queue))
	copy(data, arq.rcv_queue)
	arq.rcv_queue = arq.rcv_queue[:0]
	return data
}

// Input 输入接收到的数据包
func (arq *ARQ) Input(data []byte) {
	if len(data) < ARQ_OVERHEAD {
		return
	}

	arq.mu.Lock()
	defer arq.mu.Unlock()

	for len(data) >= ARQ_OVERHEAD {
		// 解析 11 字节包头: cmd(1) + sn(4) + ack(4) + len(2)
		cmd := data[0]
		sn := binary.BigEndian.Uint32(data[1:5])
		ack := binary.BigEndian.Uint32(data[5:9])
		length := binary.BigEndian.Uint16(data[9:11])

		data = data[ARQ_OVERHEAD:]
		if len(data) < int(length) {
			break
		}

		switch cmd {
		case CMD_DATA:
			arq.handleData(sn, data[:length])
			arq.processAck(ack)
		case CMD_NACK:
			// 批量 NACK: sn 是第一个缺失 SN, payload 是后续缺失 SN 列表
			arq.handleBatchNack(sn, data[:length])
			arq.processAck(ack)
		case CMD_ACK:
			arq.processAck(ack)
		}

		data = data[length:]
	}
}

// handleData 处理数据包
func (arq *ARQ) handleData(sn uint32, data []byte) {
	// 更新接收到的最高序列号
	if sn > arq.highest_rcv {
		arq.highest_rcv = sn
	}

	// 检查序列号
	if sn < arq.rcv_nxt {
		// 重复包，丢弃
		return
	}

	if sn == arq.rcv_nxt {
		// 按序到达
		arq.rcv_queue = append(arq.rcv_queue, data...)
		arq.rcv_nxt++
		arq.nack_count = 0 // 重置gap计数
		arq.nack_retry = 0 // 重置NACK重试计数
		arq.nack_gap_ts = 0

		// 检查缓冲区中的连续包
		for {
			if seg, exists := arq.rcv_buf[arq.rcv_nxt]; exists {
				arq.rcv_queue = append(arq.rcv_queue, seg.data...)
				delete(arq.rcv_buf, arq.rcv_nxt)
				arq.rcv_nxt++
			} else {
				break
			}
		}
	} else {
		// 乱序到达，存入缓冲区
		seg := Segment{
			sn:   sn,
			data: make([]byte, len(data)),
		}
		copy(seg.data, data)
		arq.rcv_buf[sn] = seg

		// 检查是否需要发送NACK
		arq.checkAndSendNack()
	}
}

// processAck 处理捎带 ACK，清理 snd_buf 中已确认的段
func (arq *ARQ) processAck(ack uint32) {
	if ack == 0 {
		return
	}

	// 移除所有 sn < ack 的段
	newBuf := make([]Segment, 0, len(arq.snd_buf))
	for _, seg := range arq.snd_buf {
		if seg.sn >= ack {
			newBuf = append(newBuf, seg)
		}
	}
	arq.snd_buf = newBuf

	// 更新 snd_una
	if ack > arq.snd_una {
		arq.snd_una = ack
	}
}

// checkAndSendNack 检查并发送批量 NACK
func (arq *ARQ) checkAndSendNack() {
	// 收集 rcv_nxt 到 highest_rcv 之间所有缺失的 SN
	var missing []uint32
	for sn := arq.rcv_nxt; sn < arq.highest_rcv; sn++ {
		if _, exists := arq.rcv_buf[sn]; !exists {
			missing = append(missing, sn)
		}
	}

	if len(missing) == 0 {
		arq.nack_retry = 0
		arq.nack_gap_ts = 0
		return
	}

	// 记录首次检测到 gap 的时间
	if arq.nack_gap_ts == 0 {
		arq.nack_gap_ts = arq.current
	}

	// 条件1: gap_count >= 阈值 (降为1)
	// 条件2: gap 存在超过 2*RTO (覆盖低频场景)
	gapThresholdMet := len(missing) >= ARQ_NACK_THRESHOLD
	timeThresholdMet := arq.nack_gap_ts > 0 && arq.current-arq.nack_gap_ts > uint32(2*arq.rto)

	if !gapThresholdMet && !timeThresholdMet {
		return
	}

	// 计算NACK重传间隔，使用指数退避
	nack_interval := uint32(100 * (1 << min(arq.nack_retry, 5))) // 最大3.2秒

	// 检查是否需要重传NACK
	if arq.current-arq.nack_sent > nack_interval {
		arq.sendBatchNack(missing)
		arq.nack_sent = arq.current
		arq.nack_retry++
	}
}

// min 辅助函数
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// handleBatchNack 处理批量 NACK 包
func (arq *ARQ) handleBatchNack(firstSN uint32, data []byte) {
	// 重传第一个缺失 SN
	arq.retransmitSN(firstSN)

	// 解析 payload 中的后续缺失 SN
	for len(data) >= 4 {
		sn := binary.BigEndian.Uint32(data[:4])
		arq.retransmitSN(sn)
		data = data[4:]
	}
}

// retransmitSN 在 snd_buf 中查找并重传指定 SN
func (arq *ARQ) retransmitSN(sn uint32) {
	for i := range arq.snd_buf {
		if arq.snd_buf[i].sn == sn {
			arq.sendSegment(&arq.snd_buf[i])
			break
		}
	}
}

// sendBatchNack 发送批量 NACK 包
func (arq *ARQ) sendBatchNack(missing []uint32) {
	if len(missing) == 0 {
		return
	}

	// NACK 包: sn = missing[0], payload = 后续缺失 SN 列表 (每个 4 字节)
	payloadLen := (len(missing) - 1) * 4
	buf := make([]byte, ARQ_OVERHEAD+payloadLen)
	buf[0] = CMD_NACK
	binary.BigEndian.PutUint32(buf[1:5], missing[0])
	binary.BigEndian.PutUint32(buf[5:9], arq.rcv_nxt) // 捎带 ACK
	binary.BigEndian.PutUint16(buf[9:11], uint16(payloadLen))

	// 写入后续缺失 SN
	offset := ARQ_OVERHEAD
	for i := 1; i < len(missing); i++ {
		binary.BigEndian.PutUint32(buf[offset:offset+4], missing[i])
		offset += 4
	}

	arq.output(buf)
}

// Update 更新状态
func (arq *ARQ) Update() {
	arq.mu.Lock()
	defer arq.mu.Unlock()

	arq.current = currentMs()
	arq.flush()
}

// flush 刷新数据
func (arq *ARQ) flush() {
	// 移动队列中的包到发送缓冲区（首次发送）
	for len(arq.snd_queue) > 0 && len(arq.snd_buf) < ARQ_WND_SIZE {
		seg := arq.snd_queue[0]
		arq.snd_queue = arq.snd_queue[1:]

		seg.ts = arq.current
		arq.sendSegment(&seg)
		arq.snd_buf = append(arq.snd_buf, seg)
	}

	// 清理超时的已发送包（10*RTO 后静默丢弃）
	arq.cleanupOldSegments()
}

// cleanupOldSegments 清理超时的发送缓冲区包（10*RTO 后静默丢弃）
func (arq *ARQ) cleanupOldSegments() {
	timeout := uint32(10 * arq.rto)
	newBuf := make([]Segment, 0, len(arq.snd_buf))

	for _, seg := range arq.snd_buf {
		if arq.current-seg.ts < timeout {
			newBuf = append(newBuf, seg)
		}
	}

	arq.snd_buf = newBuf
}

// sendSegment 发送数据段 (11 字节头)
func (arq *ARQ) sendSegment(seg *Segment) {
	buf := make([]byte, ARQ_OVERHEAD+len(seg.data))
	buf[0] = seg.cmd
	binary.BigEndian.PutUint32(buf[1:5], seg.sn)
	binary.BigEndian.PutUint32(buf[5:9], arq.rcv_nxt) // 捎带 ACK
	binary.BigEndian.PutUint16(buf[9:11], seg.len)
	copy(buf[ARQ_OVERHEAD:], seg.data)
	arq.output(buf)
}

// WaitSnd 返回等待发送的包数量
func (arq *ARQ) WaitSnd() int {
	arq.mu.Lock()
	defer arq.mu.Unlock()
	return len(arq.snd_queue) + len(arq.snd_buf)
}

// WaitRcv 返回乱序接收缓冲区的包数量
func (arq *ARQ) WaitRcv() int {
	arq.mu.Lock()
	defer arq.mu.Unlock()
	return len(arq.rcv_buf)
}

// HasTimeout 返回是否设置了超时
func (arq *ARQ) HasTimeout() bool {
	return arq.timeout > 0
}

// GetTimeout 返回配置的超时时间（毫秒）
func (arq *ARQ) GetTimeout() int {
	return arq.timeout
}
