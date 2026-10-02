package arq

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestARQReordering tests ARQ with severe packet reordering
func TestARQReordering(t *testing.T) {
	arq := NewSimpleARQ(func([]byte) {})

	// Create packets 0-9
	packets := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		packets[i] = makeARQDataPacket(uint32(i), []byte{byte(i)})
	}

	// Shuffle packets randomly
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	rng.Shuffle(len(packets), func(i, j int) {
		packets[i], packets[j] = packets[j], packets[i]
	})

	// Input shuffled packets
	for _, pkt := range packets {
		arq.Input(pkt)
	}

	// Should eventually receive all data in order
	data := arq.Recv()
	if len(data) != 10 {
		t.Fatalf("Expected 10 bytes, got %d", len(data))
	}

	// Verify data is in correct order
	for i := 0; i < 10; i++ {
		if data[i] != byte(i) {
			t.Fatalf("Data out of order at position %d: got %d, expected %d", i, data[i], i)
		}
	}
}

// TestARQBurstySend tests ARQ with bursty send patterns
func TestARQBurstySend(t *testing.T) {
	var outputCount int32

	output := func(data []byte) {
		atomic.AddInt32(&outputCount, 1)
	}

	arq := NewSimpleARQ(output)

	// Send in bursts
	for burst := 0; burst < 5; burst++ {
		// Burst: send many packets quickly
		for i := 0; i < 50; i++ {
			arq.Send([]byte{byte(i)})
		}

		// Update to flush
		time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
		arq.Update()

		// Pause between bursts
		time.Sleep(100 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)

	if outputCount == 0 {
		t.Error("No packets sent in bursty test")
	}

	t.Logf("Sent %d packets in 5 bursts", outputCount)
}

// TestARQDuplicateNACK tests handling of duplicate NACK packets
func TestARQDuplicateNACK(t *testing.T) {
	var outputPackets [][]byte
	var mu sync.Mutex

	output := func(data []byte) {
		mu.Lock()
		pkt := make([]byte, len(data))
		copy(pkt, data)
		outputPackets = append(outputPackets, pkt)
		mu.Unlock()
	}

	arq := NewSimpleARQ(output)

	// Send data
	arq.Send([]byte("test"))
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	arq.Update()
	time.Sleep(50 * time.Millisecond)

	// Clear output
	mu.Lock()
	outputPackets = nil
	mu.Unlock()

	// Send same NACK multiple times (11-byte header)
	nackPkt := make([]byte, ARQ_OVERHEAD)
	nackPkt[0] = CMD_NACK
	binary.BigEndian.PutUint32(nackPkt[1:5], 0)
	binary.BigEndian.PutUint32(nackPkt[5:9], 0) // ack = 0
	binary.BigEndian.PutUint16(nackPkt[9:11], 0)

	for i := 0; i < 5; i++ {
		arq.Input(nackPkt)
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)

	// Should have retransmitted (possibly multiple times)
	mu.Lock()
	retransmitCount := len(outputPackets)
	mu.Unlock()

	if retransmitCount == 0 {
		t.Error("No retransmission after NACK")
	}

	t.Logf("Retransmitted %d times for 5 duplicate NACKs", retransmitCount)
}

// TestARQMalformedPackets tests handling of malformed packets
func TestARQMalformedPackets(t *testing.T) {
	arq := NewSimpleARQ(func([]byte) {})

	testCases := []struct {
		name string
		data []byte
	}{
		{"Empty", []byte{}},
		{"Too short", []byte{1, 2}},
		{"Invalid length", []byte{CMD_DATA, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xFF}},     // Claims 65535 bytes
		{"Truncated data", []byte{CMD_DATA, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 1, 2, 3}}, // Claims 10 bytes, has 3
		{"Invalid command", []byte{0xFF, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not crash
			arq.Input(tc.data)

			// Should not have received any data
			data := arq.Recv()
			if len(data) > 0 {
				t.Errorf("Received data from malformed packet: %v", data)
			}
		})
	}
}

// TestARQRapidUpdateCalls tests rapid Update() calls
func TestARQRapidUpdateCalls(t *testing.T) {
	var outputCount int32

	output := func(data []byte) {
		atomic.AddInt32(&outputCount, 1)
	}

	arq := NewSimpleARQ(output)

	// Send some data
	for i := 0; i < 10; i++ {
		arq.Send([]byte{byte(i)})
	}

	// Call Update rapidly
	for i := 0; i < 100; i++ {
		arq.Update()
		time.Sleep(time.Millisecond)
	}

	// Should have sent packets
	if outputCount == 0 {
		t.Error("No packets sent after rapid updates")
	}

	t.Logf("Sent %d packets with 100 rapid updates", outputCount)
}

// TestARQConcurrentInputUpdate tests concurrent Input and Update calls
func TestARQConcurrentInputUpdate(t *testing.T) {
	arq := NewSimpleARQ(func([]byte) {})

	var wg sync.WaitGroup
	duration := 1 * time.Second
	stopCh := make(chan struct{})
	time.AfterFunc(duration, func() { close(stopCh) })

	// Continuous Input
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := uint32(0)
		for {
			select {
			case <-stopCh:
				return
			default:
				pkt := makeARQDataPacket(counter, []byte{byte(counter)})
				arq.Input(pkt)
				counter++
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	// Continuous Update
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				arq.Update()
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Continuous Recv
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				arq.Recv()
				time.Sleep(15 * time.Millisecond)
			}
		}
	}()

	wg.Wait()

	// Should not crash
	t.Log("Concurrent Input/Update/Recv completed without crash")
}

// TestARQMemoryLeak tests for potential memory leaks in send buffer
func TestARQMemoryLeak(t *testing.T) {
	arq := NewSimpleARQ(func([]byte) {})

	// Send many packets
	for i := 0; i < 1000; i++ {
		arq.Send(make([]byte, 100))
	}

	// Update to move to send buffer
	for i := 0; i < 10; i++ {
		time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
		arq.Update()
	}

	initialWait := arq.WaitSnd()

	// Wait for several update cycles. ARQ should either remain bounded within its
	// retransmission budget or fail the session explicitly instead of silently dropping.
	t.Logf("Send buffer has %d packets waiting", initialWait)

	if initialWait > ARQ_WND_SIZE*2 {
		t.Logf("Warning: send buffer growing beyond expected size")
	}
}

// TestARQZeroMTU tests ARQ with invalid MTU
func TestARQZeroMTU(t *testing.T) {
	// Should use default MTU
	arq := NewSimpleARQWithMTU(func([]byte) {}, 0)

	if arq.mtu != ARQ_MTU {
		t.Errorf("Expected default MTU %d, got %d", ARQ_MTU, arq.mtu)
	}

	// Should still work
	arq.Send([]byte("test"))
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)
	arq.Update()
}

// TestARQNegativeMTU tests ARQ with negative MTU
func TestARQNegativeMTU(t *testing.T) {
	// Should use default MTU
	arq := NewSimpleARQWithMTU(func([]byte) {}, -100)

	if arq.mtu != ARQ_MTU {
		t.Errorf("Expected default MTU %d, got %d", ARQ_MTU, arq.mtu)
	}
}

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
	net.senderARQ = NewSimpleARQ(func(data []byte) {
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
	net.receiverARQ = NewSimpleARQ(func(data []byte) {
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
// snd_buf 在低包量场景下会保留 in-flight 数据，直到 standalone ACK timer 触发。
// 验证其最终能被 ACK 正常释放，而不是永久滞留。
// ============================================================================

func TestSlowNet_SndBufClearsAfterStandaloneAckTimer(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 20*time.Millisecond)
	net.senderARQ.rto = 100
	net.receiverARQ.rto = 100

	// 正常发送，不丢包
	for i := 0; i < 10; i++ {
		net.senderARQ.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	expected := "pkt-0|pkt-1|pkt-2|pkt-3|pkt-4|pkt-5|pkt-6|pkt-7|pkt-8|pkt-9|"
	var received string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.receiverARQ.Recv(); len(data) > 0 {
			received += string(data)
		}
		net.senderARQ.mu.Lock()
		sndBufLen := len(net.senderARQ.snd_buf)
		sndQueueLen := len(net.senderARQ.snd_queue)
		net.senderARQ.mu.Unlock()
		if received == expected && sndBufLen == 0 && sndQueueLen == 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	net.senderARQ.mu.Lock()
	sndBufLen := len(net.senderARQ.snd_buf)
	sndQueueLen := len(net.senderARQ.snd_queue)
	net.senderARQ.mu.Unlock()

	t.Logf("接收端收到: %q", received)
	t.Logf("发送端状态: snd_buf=%d 段, snd_queue=%d 段", sndBufLen, sndQueueLen)
	t.Logf("网络统计: %s", net.stats())

	if received != expected {
		t.Fatalf("接收端数据不完整: got %q", received)
	}
	if sndBufLen > 0 || sndQueueLen > 0 {
		t.Fatalf("standalone ACK timer 未释放发送窗口: snd_buf=%d snd_queue=%d", sndBufLen, sndQueueLen)
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
// 验证 snd_buf 占用在 ACK 到达后会释放，而不是持续累积。
// ============================================================================

func TestSlowNet_SndBufMemoryReleasesAfterAckTimer(t *testing.T) {
	net := newSlowNetwork(150*time.Millisecond, 20*time.Millisecond)
	net.senderARQ.rto = 100
	net.receiverARQ.rto = 100

	for i := 0; i < 10; i++ {
		payload := make([]byte, 1000) // 1KB per packet
		copy(payload, []byte(fmt.Sprintf("pkt-%d", i)))
		net.senderARQ.Send(payload)
		time.Sleep(150 * time.Millisecond)
		net.tick()
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		net.receiverARQ.Recv()
		net.senderARQ.mu.Lock()
		sndBufLen := len(net.senderARQ.snd_buf)
		net.senderARQ.mu.Unlock()
		if sndBufLen == 0 {
			break
		}
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
		t.Fatalf("snd_buf 未在 ACK 后释放: %d 段仍在飞行", sndBufLen)
	}
}
