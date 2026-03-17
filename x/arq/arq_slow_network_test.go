package arq

import (
	"encoding/binary"
	"fmt"
	"sync"
	"testing"
	"time"
)

// slowNetwork 模拟低频通信的网络环境
// 连接两个 ARQ 实例，支持延迟、丢包、单向控制
type slowNetwork struct {
	mu sync.Mutex

	senderARQ   *ARQ
	receiverARQ *ARQ

	// 发送端 -> 接收端 的数据包队列
	forwardQueue []pendingPacket
	// 接收端 -> 发送端 的数据包队列 (NACK)
	reverseQueue []pendingPacket

	// 网络配置
	packetInterval time.Duration // 发包间隔
	oneWayDelay    time.Duration // 单程传输延迟

	// 丢包控制: 指定哪些包被丢弃
	forwardDropSet map[uint32]bool // 正向丢弃的 SN 集合
	reverseDropAll bool            // 丢弃所有反向包 (NACK)

	// 统计
	forwardSent    int
	forwardDropped int
	reverseSent    int
	reverseDropped int
	nacksSent      int // 接收端发出的 NACK 数量
}

type pendingPacket struct {
	data      []byte
	deliverAt time.Time
}

func newSlowNetwork(packetInterval, oneWayDelay time.Duration) *slowNetwork {
	net := &slowNetwork{
		packetInterval: packetInterval,
		oneWayDelay:    oneWayDelay,
		forwardDropSet: make(map[uint32]bool),
	}

	// 创建 sender ARQ, 输出到 forwardQueue
	net.senderARQ = NewSimpleARQ(1, func(data []byte) {
		net.mu.Lock()
		defer net.mu.Unlock()

		d := make([]byte, len(data))
		copy(d, data)

		// 解析 SN 判断是否丢弃 (11字节头: cmd+sn+ack+len)
		if len(data) >= ARQ_OVERHEAD {
			sn := binary.BigEndian.Uint32(data[1:5])
			cmd := data[0]
			if cmd == CMD_DATA {
				net.forwardSent++
				if net.forwardDropSet[sn] {
					net.forwardDropped++
					return // 模拟丢包
				}
			}
		}

		net.forwardQueue = append(net.forwardQueue, pendingPacket{
			data:      d,
			deliverAt: time.Now().Add(oneWayDelay),
		})
	})

	// 创建 receiver ARQ, 输出到 reverseQueue (NACK 通道)
	net.receiverARQ = NewSimpleARQ(1, func(data []byte) {
		net.mu.Lock()
		defer net.mu.Unlock()

		d := make([]byte, len(data))
		copy(d, data)

		if len(data) >= ARQ_OVERHEAD && data[0] == CMD_NACK {
			net.nacksSent++
		}

		if net.reverseDropAll {
			net.reverseDropped++
			return
		}

		net.reverseSent++
		net.reverseQueue = append(net.reverseQueue, pendingPacket{
			data:      d,
			deliverAt: time.Now().Add(oneWayDelay),
		})
	})

	return net
}

// deliver 检查队列中到期的包并投递
func (n *slowNetwork) deliver() {
	n.mu.Lock()
	now := time.Now()

	// 正向投递: sender -> receiver
	var remainFwd []pendingPacket
	var toDeliverFwd [][]byte
	for _, p := range n.forwardQueue {
		if now.After(p.deliverAt) {
			toDeliverFwd = append(toDeliverFwd, p.data)
		} else {
			remainFwd = append(remainFwd, p)
		}
	}
	n.forwardQueue = remainFwd

	// 反向投递: receiver -> sender
	var remainRev []pendingPacket
	var toDeliverRev [][]byte
	for _, p := range n.reverseQueue {
		if now.After(p.deliverAt) {
			toDeliverRev = append(toDeliverRev, p.data)
		} else {
			remainRev = append(remainRev, p)
		}
	}
	n.reverseQueue = remainRev
	n.mu.Unlock()

	// 在锁外调用 Input，避免死锁
	for _, d := range toDeliverFwd {
		n.receiverARQ.Input(d)
	}
	for _, d := range toDeliverRev {
		n.senderARQ.Input(d)
	}
}

// tick 模拟一个时间片: 投递 + 更新两端 ARQ
func (n *slowNetwork) tick() {
	n.deliver()
	n.senderARQ.Update()
	n.receiverARQ.Update()
}

// stats 返回网络统计摘要
func (n *slowNetwork) stats() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return fmt.Sprintf("fwd_sent=%d fwd_dropped=%d rev_sent=%d rev_dropped=%d nacks=%d",
		n.forwardSent, n.forwardDropped, n.reverseSent, n.reverseDropped, n.nacksSent)
}

// ============================================================================
// 问题 2: NACK 每次只报告一个缺失 SN (P1)
// 丢弃 SN=1,2,3 三个连续包，批量 NACK 应 1 轮恢复。
// ============================================================================

func TestSlowNet_SingleNACKPerCycle(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 30*time.Millisecond)

	// 丢弃 SN=1,2,3
	droppedOnce := map[uint32]bool{1: true, 2: true, 3: true}
	net.forwardDropSet = droppedOnce

	// 发送 8 个包: SN 0-7
	for i := 0; i < 8; i++ {
		net.senderARQ.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 取消丢包
	time.Sleep(100 * time.Millisecond)
	net.mu.Lock()
	net.forwardDropSet = make(map[uint32]bool)
	net.mu.Unlock()

	var received string
	nackRounds := 0
	lastNackCount := 0

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.receiverARQ.Recv(); len(data) > 0 {
			received += string(data)
		}

		net.mu.Lock()
		currentNacks := net.nacksSent
		net.mu.Unlock()
		if currentNacks > lastNackCount {
			nackRounds++
			lastNackCount = currentNacks
		}

		expected := "pkt-0|pkt-1|pkt-2|pkt-3|pkt-4|pkt-5|pkt-6|pkt-7|"
		if received == expected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("网络统计: %s", net.stats())
	t.Logf("接收到的数据: %q", received)
	t.Logf("NACK 轮次: %d (丢失了 3 个包)", nackRounds)

	expected := "pkt-0|pkt-1|pkt-2|pkt-3|pkt-4|pkt-5|pkt-6|pkt-7|"
	if received != expected {
		t.Logf("FAIL: 数据未完全恢复")
		t.Logf("  期望: %q", expected)
		t.Logf("  实际: %q", received)
		t.Fail()
		return
	}

	if nackRounds > 3 {
		t.Logf("ISSUE: 恢复 3 个丢包需要 %d 轮 NACK 交互", nackRounds)
	} else if nackRounds > 0 {
		t.Logf("OK: %d 轮 NACK 恢复了 3 个丢包", nackRounds)
	}
}

// ============================================================================
// 问题 3: 无 ACK，snd_buf 靠超时猜测清理 (P0)
// Piggyback ACK 应及时清理 snd_buf。
// ============================================================================

func TestSlowNet_SndBufNeverCleared(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 20*time.Millisecond)

	// 正常发送，不丢包
	for i := 0; i < 10; i++ {
		net.senderARQ.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 确保接收端收到所有数据
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 20; i++ {
		net.tick()
		time.Sleep(30 * time.Millisecond)
	}

	var received string
	if data := net.receiverARQ.Recv(); len(data) > 0 {
		received = string(data)
	}

	net.senderARQ.mu.Lock()
	sndBufLen := len(net.senderARQ.snd_buf)
	sndQueueLen := len(net.senderARQ.snd_queue)
	net.senderARQ.mu.Unlock()

	t.Logf("接收端收到: %q", received)
	t.Logf("发送端状态: snd_buf=%d 段, snd_queue=%d 段", sndBufLen, sndQueueLen)
	t.Logf("网络统计: %s", net.stats())

	if sndBufLen > 0 {
		t.Logf("ISSUE: 接收端已收到所有数据，但发送端 snd_buf 仍有 %d 个段", sndBufLen)
		t.Fail()
	}
}

// ============================================================================
// 问题 4: 窗口耗尽导致发送阻塞 (P0 的后果)
// ACK 及时清理 snd_buf，窗口不会耗尽。
// ============================================================================

func TestSlowNet_WindowExhaustion(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 20*time.Millisecond)

	// 发送超过窗口大小的数据
	totalPackets := ARQ_WND_SIZE + 10 // 42 个包
	for i := 0; i < totalPackets; i++ {
		net.senderARQ.Send([]byte(fmt.Sprintf("p%02d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 持续 tick 给足够时间
	for i := 0; i < 30; i++ {
		net.tick()
		time.Sleep(50 * time.Millisecond)
	}

	net.senderARQ.mu.Lock()
	sndBufLen := len(net.senderARQ.snd_buf)
	sndQueueLen := len(net.senderARQ.snd_queue)
	net.senderARQ.mu.Unlock()

	var received string
	if data := net.receiverARQ.Recv(); len(data) > 0 {
		received = string(data)
	}

	t.Logf("发送了 %d 个包", totalPackets)
	t.Logf("发送端: snd_buf=%d, snd_queue=%d (被阻塞的包)", sndBufLen, sndQueueLen)
	t.Logf("接收端收到: %d 字节", len(received))
	t.Logf("网络统计: %s", net.stats())

	if sndQueueLen > 0 {
		t.Logf("ISSUE: %d 个包被阻塞在 snd_queue 中，因为 snd_buf 满 (%d/%d)", sndQueueLen, sndBufLen, ARQ_WND_SIZE)
		t.Fail()
	}
}

// ============================================================================
// 问题 5: NACK 阈值过高，低频场景下 gap 难以积累 (P1)
// 阈值降为 1，单个丢包即可触发 NACK。
// ============================================================================

func TestSlowNet_NACKThresholdTooHigh(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 30*time.Millisecond)

	// 丢弃 SN=2
	net.forwardDropSet[2] = true

	// 发送 5 个包
	for i := 0; i < 5; i++ {
		net.senderARQ.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 取消丢包
	net.mu.Lock()
	net.forwardDropSet = make(map[uint32]bool)
	net.mu.Unlock()

	deadline := time.Now().Add(8 * time.Second)
	var received string
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.receiverARQ.Recv(); len(data) > 0 {
			received += string(data)
		}
		if received == "pkt-0|pkt-1|pkt-2|pkt-3|pkt-4|" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	net.mu.Lock()
	nacks := net.nacksSent
	net.mu.Unlock()

	t.Logf("接收到的数据: %q", received)
	t.Logf("NACK 发送次数: %d", nacks)
	t.Logf("网络统计: %s", net.stats())

	expected := "pkt-0|pkt-1|pkt-2|pkt-3|pkt-4|"
	if received != expected {
		t.Logf("FAIL: 数据不完整")
		t.Logf("  期望: %q", expected)
		t.Logf("  实际: %q", received)

		net.receiverARQ.mu.Lock()
		rcvNxt := net.receiverARQ.rcv_nxt
		bufKeys := make([]uint32, 0)
		for k := range net.receiverARQ.rcv_buf {
			bufKeys = append(bufKeys, k)
		}
		net.receiverARQ.mu.Unlock()

		t.Logf("  接收端 rcv_nxt=%d, rcv_buf 中缓存的 SN: %v", rcvNxt, bufKeys)
		t.Fail()
	}
}

// ============================================================================
// 问题 6: NACK 丢失后无法恢复 (P1)
// RTO 自动重传作为兜底，即使 NACK 丢失也能恢复。
// ============================================================================

// ============================================================================
// 综合场景: 模拟低频通信
// 发送 10 个包, 20% 丢包率, 观察端到端恢复情况
// ============================================================================

func TestSlowNet_Realistic3sScenario(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 50*time.Millisecond)

	// 模拟 20% 丢包: 丢弃 SN=2, SN=7
	net.forwardDropSet[2] = true
	net.forwardDropSet[7] = true

	totalPackets := 10

	for i := 0; i < totalPackets; i++ {
		net.senderARQ.Send([]byte(fmt.Sprintf("pkt-%02d|", i)))
		time.Sleep(150 * time.Millisecond)
		net.tick()
	}

	// 取消丢包
	time.Sleep(100 * time.Millisecond)
	net.mu.Lock()
	net.forwardDropSet = make(map[uint32]bool)
	net.mu.Unlock()

	var received string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.receiverARQ.Recv(); len(data) > 0 {
			received += string(data)
		}
		var expected string
		for i := 0; i < totalPackets; i++ {
			expected += fmt.Sprintf("pkt-%02d|", i)
		}
		if received == expected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	net.mu.Lock()
	nacks := net.nacksSent
	net.mu.Unlock()

	var expected string
	for i := 0; i < totalPackets; i++ {
		expected += fmt.Sprintf("pkt-%02d|", i)
	}

	t.Logf("=== 低频通信综合测试 ===")
	t.Logf("发送: %d 包, 丢包: SN=2,7 (20%%)", totalPackets)
	t.Logf("接收: %q", received)
	t.Logf("NACK 次数: %d", nacks)
	t.Logf("网络统计: %s", net.stats())

	net.senderARQ.mu.Lock()
	sndBuf := len(net.senderARQ.snd_buf)
	net.senderARQ.mu.Unlock()

	net.receiverARQ.mu.Lock()
	rcvNxt := net.receiverARQ.rcv_nxt
	rcvBufLen := len(net.receiverARQ.rcv_buf)
	var bufferedSNs []uint32
	for k := range net.receiverARQ.rcv_buf {
		bufferedSNs = append(bufferedSNs, k)
	}
	net.receiverARQ.mu.Unlock()

	t.Logf("发送端 snd_buf 残留: %d 段", sndBuf)
	t.Logf("接收端 rcv_nxt=%d, rcv_buf 缓存: %v", rcvNxt, bufferedSNs)

	if received == expected {
		t.Log("PASS: 所有数据恢复完整")
	} else {
		t.Log("FAIL: 数据恢复不完整")
		if rcvBufLen > 0 {
			t.Logf("  - 接收端有 %d 个乱序包卡在 rcv_buf (等待 SN=%d)", rcvBufLen, rcvNxt)
		}
		if nacks == 0 {
			t.Logf("  - 未发送任何 NACK")
		}
		if sndBuf > 0 {
			t.Logf("  - 发送端有 %d 段在 snd_buf 中等待超时清理", sndBuf)
		}
		t.Fail()
	}
}

// ============================================================================
// 问题量化: snd_buf 超时清理前的内存占用
// ============================================================================

func TestSlowNet_SndBufMemoryAccumulation(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 20*time.Millisecond)

	for i := 0; i < 10; i++ {
		payload := make([]byte, 1000) // 1KB per packet
		copy(payload, []byte(fmt.Sprintf("pkt-%d", i)))
		net.senderARQ.Send(payload)
		time.Sleep(150 * time.Millisecond)
		net.tick()
	}

	// 确保接收端全部收到
	for i := 0; i < 20; i++ {
		net.tick()
		net.receiverARQ.Recv()
		time.Sleep(30 * time.Millisecond)
	}

	net.senderARQ.mu.Lock()
	sndBufLen := len(net.senderARQ.snd_buf)
	totalBytes := 0
	for _, seg := range net.senderARQ.snd_buf {
		totalBytes += len(seg.data) + ARQ_OVERHEAD + 20
	}
	net.senderARQ.mu.Unlock()

	t.Logf("发送 10 个 1KB 包后:")
	t.Logf("  snd_buf 段数: %d", sndBufLen)
	t.Logf("  snd_buf 内存: ~%d bytes", totalBytes)

	if sndBufLen > 0 {
		t.Logf("  snd_buf 未被 ACK 清理")
		t.Fail()
	}
}
