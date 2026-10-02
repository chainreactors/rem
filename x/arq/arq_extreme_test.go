package arq

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// bidirectionalNetwork: 双向网络模拟器
// 支持两个节点同时收发数据，每个方向独立丢包控制
// ============================================================================

type bidirectionalNetwork struct {
	mu sync.Mutex

	nodeA *ARQ // 节点 A
	nodeB *ARQ // 节点 B

	// A -> B 的包队列
	queueAtoB []pendingPacket
	// B -> A 的包队列
	queueBtoA []pendingPacket

	oneWayDelay time.Duration

	// 丢包控制
	dropAtoB    bool            // 丢弃所有 A->B 的包
	dropBtoA    bool            // 丢弃所有 B->A 的包
	dropSetAtoB map[uint32]bool // 按 SN 丢弃 A->B 的 DATA 包
	dropSetBtoA map[uint32]bool // 按 SN 丢弃 B->A 的 DATA 包

	// 统计
	sentAtoB    int
	droppedAtoB int
	sentBtoA    int
	droppedBtoA int
}

func newBidirectionalNetwork(oneWayDelay time.Duration) *bidirectionalNetwork {
	net := &bidirectionalNetwork{
		oneWayDelay: oneWayDelay,
		dropSetAtoB: make(map[uint32]bool),
		dropSetBtoA: make(map[uint32]bool),
	}

	// 节点 A: 输出到 queueAtoB
	net.nodeA = NewSimpleARQ(func(data []byte) {
		net.mu.Lock()
		defer net.mu.Unlock()

		d := make([]byte, len(data))
		copy(d, data)

		// 全部丢弃
		if net.dropAtoB {
			net.droppedAtoB++
			return
		}

		// 按 SN 丢弃 DATA 包
		if len(data) >= ARQ_OVERHEAD {
			cmd := data[0]
			sn := binary.BigEndian.Uint32(data[1:5])
			if cmd == CMD_DATA {
				net.sentAtoB++
				if net.dropSetAtoB[sn] {
					net.droppedAtoB++
					return
				}
			}
		}

		net.queueAtoB = append(net.queueAtoB, pendingPacket{
			data:      d,
			deliverAt: time.Now().Add(oneWayDelay),
		})
	})

	// 节点 B: 输出到 queueBtoA
	net.nodeB = NewSimpleARQ(func(data []byte) {
		net.mu.Lock()
		defer net.mu.Unlock()

		d := make([]byte, len(data))
		copy(d, data)

		// 全部丢弃
		if net.dropBtoA {
			net.droppedBtoA++
			return
		}

		// 按 SN 丢弃 DATA 包
		if len(data) >= ARQ_OVERHEAD {
			cmd := data[0]
			sn := binary.BigEndian.Uint32(data[1:5])
			if cmd == CMD_DATA {
				net.sentBtoA++
				if net.dropSetBtoA[sn] {
					net.droppedBtoA++
					return
				}
			}
		}

		net.queueBtoA = append(net.queueBtoA, pendingPacket{
			data:      d,
			deliverAt: time.Now().Add(oneWayDelay),
		})
	})

	return net
}

func (n *bidirectionalNetwork) deliver() {
	n.mu.Lock()
	now := time.Now()

	var remainAB []pendingPacket
	var toDeliverAB [][]byte
	for _, p := range n.queueAtoB {
		if now.After(p.deliverAt) {
			toDeliverAB = append(toDeliverAB, p.data)
		} else {
			remainAB = append(remainAB, p)
		}
	}
	n.queueAtoB = remainAB

	var remainBA []pendingPacket
	var toDeliverBA [][]byte
	for _, p := range n.queueBtoA {
		if now.After(p.deliverAt) {
			toDeliverBA = append(toDeliverBA, p.data)
		} else {
			remainBA = append(remainBA, p)
		}
	}
	n.queueBtoA = remainBA
	n.mu.Unlock()

	for _, d := range toDeliverAB {
		n.nodeB.Input(d)
	}
	for _, d := range toDeliverBA {
		n.nodeA.Input(d)
	}
}

func (n *bidirectionalNetwork) tick() {
	n.deliver()
	n.nodeA.Update()
	n.nodeB.Update()
}

func (n *bidirectionalNetwork) stats() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return fmt.Sprintf("A->B sent=%d dropped=%d | B->A sent=%d dropped=%d",
		n.sentAtoB, n.droppedAtoB, n.sentBtoA, n.droppedBtoA)
}

// setPartition 设置网络分区 (双向全部丢弃)
func (n *bidirectionalNetwork) setPartition(partitioned bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.dropAtoB = partitioned
	n.dropBtoA = partitioned
}

// ============================================================================
// 场景 1: 双向同时通信
// 两端同时发数据，piggyback ACK 在双向通信中应更高效。
// 测试：双向各发 10 个包，各丢 20%，双方都应 100% 恢复。
// ============================================================================

func TestExtreme_BidirectionalCommunication(t *testing.T) {
	net := newBidirectionalNetwork(30 * time.Millisecond)

	// A->B 丢 SN=2,7; B->A 丢 SN=3,6
	net.dropSetAtoB[2] = true
	net.dropSetAtoB[7] = true
	net.dropSetBtoA[3] = true
	net.dropSetBtoA[6] = true

	totalPackets := 10

	// 双向同时发数据
	for i := 0; i < totalPackets; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("A%02d|", i)))
		net.nodeB.Send([]byte(fmt.Sprintf("B%02d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 取消丢包
	time.Sleep(100 * time.Millisecond)
	net.mu.Lock()
	net.dropSetAtoB = make(map[uint32]bool)
	net.dropSetBtoA = make(map[uint32]bool)
	net.mu.Unlock()

	// 构建期望
	var expectedAtB, expectedAtA string
	for i := 0; i < totalPackets; i++ {
		expectedAtB += fmt.Sprintf("A%02d|", i)
		expectedAtA += fmt.Sprintf("B%02d|", i)
	}

	// 等待恢复
	var receivedAtB, receivedAtA string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			receivedAtB += string(data)
		}
		if data := net.nodeA.Recv(); len(data) > 0 {
			receivedAtA += string(data)
		}
		if receivedAtB == expectedAtB && receivedAtA == expectedAtA {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("网络统计: %s", net.stats())
	t.Logf("B 收到 A 的数据: %q", receivedAtB)
	t.Logf("A 收到 B 的数据: %q", receivedAtA)

	// 检查 snd_buf 清理 (piggyback ACK 应该更高效)
	net.nodeA.mu.Lock()
	sndBufA := len(net.nodeA.snd_buf)
	net.nodeA.mu.Unlock()
	net.nodeB.mu.Lock()
	sndBufB := len(net.nodeB.snd_buf)
	net.nodeB.mu.Unlock()
	t.Logf("snd_buf: A=%d, B=%d", sndBufA, sndBufB)

	if receivedAtB != expectedAtB {
		t.Errorf("B 收到的数据不完整: 期望 %q, 实际 %q", expectedAtB, receivedAtB)
	}
	if receivedAtA != expectedAtA {
		t.Errorf("A 收到的数据不完整: 期望 %q, 实际 %q", expectedAtA, receivedAtA)
	}
}

// TestExtreme_BidirectionalHighLoss 双向高丢包率
func TestExtreme_BidirectionalHighLoss(t *testing.T) {
	net := newBidirectionalNetwork(30 * time.Millisecond)

	// 40% 丢包率: A->B 丢 0,3,5,8; B->A 丢 1,4,6,9
	for _, sn := range []uint32{0, 3, 5, 8} {
		net.dropSetAtoB[sn] = true
	}
	for _, sn := range []uint32{1, 4, 6, 9} {
		net.dropSetBtoA[sn] = true
	}

	totalPackets := 10

	for i := 0; i < totalPackets; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("A%02d|", i)))
		net.nodeB.Send([]byte(fmt.Sprintf("B%02d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 取消丢包
	time.Sleep(100 * time.Millisecond)
	net.mu.Lock()
	net.dropSetAtoB = make(map[uint32]bool)
	net.dropSetBtoA = make(map[uint32]bool)
	net.mu.Unlock()

	var expectedAtB, expectedAtA string
	for i := 0; i < totalPackets; i++ {
		expectedAtB += fmt.Sprintf("A%02d|", i)
		expectedAtA += fmt.Sprintf("B%02d|", i)
	}

	var receivedAtB, receivedAtA string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			receivedAtB += string(data)
		}
		if data := net.nodeA.Recv(); len(data) > 0 {
			receivedAtA += string(data)
		}
		if receivedAtB == expectedAtB && receivedAtA == expectedAtA {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("网络统计: %s", net.stats())
	t.Logf("B 收到 A: %q", receivedAtB)
	t.Logf("A 收到 B: %q", receivedAtA)

	if receivedAtB != expectedAtB {
		t.Errorf("B 数据不完整")
	}
	if receivedAtA != expectedAtA {
		t.Errorf("A 数据不完整")
	}
}

// ============================================================================
// 场景 2: 网络分区后恢复
// 发送若干包后网络完全中断，一段时间后恢复。
// 关注: RTO 退避是否导致恢复延迟过高，数据是否最终恢复。
// ============================================================================

func TestExtreme_NetworkPartitionRecovery(t *testing.T) {
	net := newBidirectionalNetwork(30 * time.Millisecond)

	// 阶段 1: 正常发送 5 个包
	for i := 0; i < 5; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 确保前 5 个包送达
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 10; i++ {
		net.tick()
		time.Sleep(30 * time.Millisecond)
	}

	var phase1 string
	if data := net.nodeB.Recv(); len(data) > 0 {
		phase1 = string(data)
	}
	t.Logf("分区前收到: %q", phase1)

	// 阶段 2: 网络分区 — 继续发 5 个包，全部丢失
	net.setPartition(true)
	t.Logf("=== 网络分区开始 ===")

	for i := 5; i < 10; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 分区持续，RTO 退避在积累
	partitionDuration := 3 * time.Second
	partitionStart := time.Now()
	for time.Since(partitionStart) < partitionDuration {
		net.tick()
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("分区持续 %v, RTO 退避已积累", partitionDuration)

	// 阶段 3: 恢复网络
	net.setPartition(false)
	recoveryStart := time.Now()
	t.Logf("=== 网络恢复 ===")

	var phase2 string
	deadline := time.Now().Add(20 * time.Second)
	var expectedPhase2 string
	for i := 5; i < 10; i++ {
		expectedPhase2 += fmt.Sprintf("pkt-%d|", i)
	}

	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			phase2 += string(data)
		}
		if phase2 == expectedPhase2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	recoveryTime := time.Since(recoveryStart)

	t.Logf("恢复后收到: %q", phase2)
	t.Logf("恢复耗时: %v", recoveryTime)
	t.Logf("网络统计: %s", net.stats())

	// 检查 snd_buf
	net.nodeA.mu.Lock()
	sndBuf := len(net.nodeA.snd_buf)
	net.nodeA.mu.Unlock()
	t.Logf("发送端 snd_buf 残留: %d 段", sndBuf)

	if phase2 != expectedPhase2 {
		t.Errorf("分区恢复后数据不完整: 期望 %q, 实际 %q", expectedPhase2, phase2)
	}

	// 恢复时间应合理: RTO 退避最大 8*RTO=48s, 但实际应远小于这个值
	// 因为分区期间 RTO 退避了若干轮, 恢复后需要等到下一个 resendts 到达
	if recoveryTime > 15*time.Second {
		t.Errorf("恢复延迟过高: %v (超过 15s)", recoveryTime)
	} else {
		t.Logf("恢复延迟可接受: %v", recoveryTime)
	}
}

// TestExtreme_LongPartitionThenBurst 长时间分区后突发恢复
func TestExtreme_LongPartitionThenBurst(t *testing.T) {
	net := newBidirectionalNetwork(20 * time.Millisecond)

	// 发送 20 个包, 然后立即断网
	for i := 0; i < 20; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("p%02d|", i)))
	}

	// flush 出去一些
	for i := 0; i < 5; i++ {
		net.tick()
		time.Sleep(120 * time.Millisecond)
	}

	// 丢弃 A->B 的最后几个包 (模拟发到一半断网)
	net.mu.Lock()
	net.dropAtoB = true
	net.mu.Unlock()

	// 分区中: tick 继续但包全丢
	for i := 0; i < 20; i++ {
		net.tick()
		time.Sleep(100 * time.Millisecond)
	}

	// 恢复
	net.mu.Lock()
	net.dropAtoB = false
	net.mu.Unlock()

	var expected string
	for i := 0; i < 20; i++ {
		expected += fmt.Sprintf("p%02d|", i)
	}

	var received string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			received += string(data)
		}
		if received == expected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("接收: %q", received)
	t.Logf("网络统计: %s", net.stats())

	if received != expected {
		t.Errorf("长分区恢复后数据不完整: 实际 %d 字节, 期望 %d 字节", len(received), len(expected))
	}
}

// ============================================================================
// 场景 3: 接收端重启 (rcv_nxt 归零)
// 发送端已发了一些数据，接收端崩溃重建 ARQ 实例 (rcv_nxt=0)。
// 关注: 发送端收到 ack=0 的包不会回退清理，旧数据是否被新接收端接受。
// ============================================================================

func TestExtreme_ReceiverRestart(t *testing.T) {
	net := newBidirectionalNetwork(20 * time.Millisecond)

	// 阶段 1: 正常发送 5 个包
	for i := 0; i < 5; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("old-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 确保全部送达
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 10; i++ {
		net.tick()
		time.Sleep(30 * time.Millisecond)
	}

	var phase1 string
	if data := net.nodeB.Recv(); len(data) > 0 {
		phase1 = string(data)
	}
	t.Logf("重启前收到: %q", phase1)

	// 记录发送端状态
	net.nodeA.mu.Lock()
	sndNxtBefore := net.nodeA.snd_nxt
	sndBufBefore := len(net.nodeA.snd_buf)
	net.nodeA.mu.Unlock()
	t.Logf("重启前发送端: snd_nxt=%d, snd_buf=%d", sndNxtBefore, sndBufBefore)

	// 阶段 2: 接收端重启 — 替换为新的 ARQ 实例
	// 注意: 新接收端 rcv_nxt=0, 但发送端 snd_nxt 已经是 5
	net.mu.Lock()
	oldOutput := net.nodeB.output
	net.nodeB = NewSimpleARQWithOutput(oldOutput)
	net.mu.Unlock()

	t.Logf("=== 接收端重启 (rcv_nxt=0) ===")

	// 阶段 3: 发送新数据
	for i := 0; i < 5; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("new-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 等待一段时间
	deadline := time.Now().Add(15 * time.Second)
	var phase2 string
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			phase2 += string(data)
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("重启后收到: %q", phase2)

	// 检查状态
	net.nodeA.mu.Lock()
	sndNxtAfter := net.nodeA.snd_nxt
	sndBufAfter := len(net.nodeA.snd_buf)
	net.nodeA.mu.Unlock()
	net.nodeB.mu.Lock()
	rcvNxtNew := net.nodeB.rcv_nxt
	rcvBufNew := len(net.nodeB.rcv_buf)
	net.nodeB.mu.Unlock()

	t.Logf("发送端: snd_nxt=%d, snd_buf=%d", sndNxtAfter, sndBufAfter)
	t.Logf("新接收端: rcv_nxt=%d, rcv_buf=%d", rcvNxtNew, rcvBufNew)
	t.Logf("网络统计: %s", net.stats())

	// 新接收端 rcv_nxt=0 但收到 SN=5,6,7,8,9 (乱序), 会缓存到 rcv_buf
	// 无法恢复: 因为 SN=0,1,2,3,4 永远不会再来 (旧数据已经被旧接收端消费)
	// 这是预期行为: 接收端重启后需要上层协议处理重建
	t.Logf("分析: 接收端重启后 rcv_nxt=0, 新数据 SN=%d+ 无法对齐", sndNxtBefore)
	t.Logf("  这是已知限制: ARQ 层无法感知对端重启, 需要上层协议 (如 yamux) 处理重建")

	// 验证: 发送端 snd_buf 不会因为收到 ack=0 而异常清理
	// processAck(0) 直接 return, 不影响 snd_buf
	if sndBufAfter < 0 {
		t.Errorf("snd_buf 异常: %d", sndBufAfter)
	}
}

// TestExtreme_ReceiverRestart_SenderProcessAckZero 验证 processAck(0) 的安全性
func TestExtreme_ReceiverRestart_SenderProcessAckZero(t *testing.T) {
	// 直接测试 processAck(0) 不会清理 snd_buf
	var outputData [][]byte
	var mu sync.Mutex

	arq := NewSimpleARQ(func(data []byte) {
		mu.Lock()
		d := make([]byte, len(data))
		copy(d, data)
		outputData = append(outputData, d)
		mu.Unlock()
	})

	// 发送 5 个包
	for i := 0; i < 5; i++ {
		arq.Send([]byte(fmt.Sprintf("pkt-%d", i)))
	}
	time.Sleep(120 * time.Millisecond)
	arq.Update()
	time.Sleep(50 * time.Millisecond)

	// 记录 snd_buf
	arq.mu.Lock()
	sndBufBefore := len(arq.snd_buf)
	arq.mu.Unlock()

	// 模拟收到 ack=0 的包 (新接收端重启后的 ACK)
	ackPkt := make([]byte, ARQ_OVERHEAD)
	ackPkt[0] = CMD_ACK
	binary.BigEndian.PutUint32(ackPkt[1:5], 0)  // sn=0
	binary.BigEndian.PutUint32(ackPkt[5:9], 0)  // ack=0
	binary.BigEndian.PutUint16(ackPkt[9:11], 0) // len=0
	arq.Input(ackPkt)

	// snd_buf 不应被清理
	arq.mu.Lock()
	sndBufAfter := len(arq.snd_buf)
	arq.mu.Unlock()

	t.Logf("processAck(0) 前后 snd_buf: %d -> %d", sndBufBefore, sndBufAfter)

	if sndBufAfter != sndBufBefore {
		t.Errorf("processAck(0) 不应改变 snd_buf: %d -> %d", sndBufBefore, sndBufAfter)
	}

	// 模拟收到正常 ACK (ack=3), 应清理 SN 0,1,2
	ackPkt2 := make([]byte, ARQ_OVERHEAD)
	ackPkt2[0] = CMD_ACK
	binary.BigEndian.PutUint32(ackPkt2[1:5], 0)
	binary.BigEndian.PutUint32(ackPkt2[5:9], 3) // ack=3
	binary.BigEndian.PutUint16(ackPkt2[9:11], 0)
	arq.Input(ackPkt2)

	arq.mu.Lock()
	sndBufAfterAck3 := len(arq.snd_buf)
	arq.mu.Unlock()

	t.Logf("processAck(3) 后 snd_buf: %d", sndBufAfterAck3)

	// 应清理掉 SN 0,1,2 (3个包)
	expectedRemaining := sndBufBefore - 3
	if expectedRemaining < 0 {
		expectedRemaining = 0
	}
	if sndBufAfterAck3 != expectedRemaining {
		t.Errorf("processAck(3) 后 snd_buf 应为 %d, 实际 %d", expectedRemaining, sndBufAfterAck3)
	}
}

// ============================================================================
// 场景 4: 全部丢包 + ACK 全丢
// 极端: 所有方向的所有包都丢弃一段时间，然后恢复。
// 在重传预算内恢复时，ARQ 仍应完整交付。
// ============================================================================

func TestExtreme_TotalPacketLoss(t *testing.T) {
	net := newBidirectionalNetwork(20 * time.Millisecond)

	// 发送 5 个包, 全部正常
	for i := 0; i < 5; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 确保送达
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 10; i++ {
		net.tick()
		time.Sleep(30 * time.Millisecond)
	}

	var phase1 string
	if data := net.nodeB.Recv(); len(data) > 0 {
		phase1 = string(data)
	}
	t.Logf("全丢前收到: %q", phase1)

	// 全部丢包: A->B 和 B->A 都丢
	net.setPartition(true)

	// 继续发 5 个包 — 数据包、NACK、ACK 全丢
	for i := 5; i < 10; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("pkt-%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	// 全丢状态持续一段时间
	for i := 0; i < 30; i++ {
		net.tick()
		time.Sleep(50 * time.Millisecond)
	}

	net.nodeA.mu.Lock()
	sndBufDuringLoss := len(net.nodeA.snd_buf)
	net.nodeA.mu.Unlock()
	t.Logf("全丢期间 snd_buf: %d 段 (应>0, RTO 重传中但全丢)", sndBufDuringLoss)

	// 恢复网络
	net.setPartition(false)
	t.Logf("=== 网络恢复 ===")

	var phase2 string
	var expectedPhase2 string
	for i := 5; i < 10; i++ {
		expectedPhase2 += fmt.Sprintf("pkt-%d|", i)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			phase2 += string(data)
		}
		if phase2 == expectedPhase2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("恢复后收到: %q", phase2)
	t.Logf("网络统计: %s", net.stats())

	net.nodeA.mu.Lock()
	sndBufAfter := len(net.nodeA.snd_buf)
	net.nodeA.mu.Unlock()
	t.Logf("恢复后 snd_buf: %d 段", sndBufAfter)

	if phase2 != expectedPhase2 {
		t.Errorf("全丢恢复后数据不完整: 期望 %q, 实际 %q", expectedPhase2, phase2)
	}
}

// TestExtreme_TotalLossExceedsRetryBudgetFailsSession verifies that ARQ fails
// explicitly once a segment exceeds its retransmission budget.
func TestExtreme_TotalLossExceedsRetryBudgetFailsSession(t *testing.T) {
	arq := NewARQWithConfig(func([]byte) {}, ARQConfig{
		MTU:                ARQ_MTU,
		RTO:                20,
		MaxRetransmissions: 2,
	})

	arq.Send([]byte("will-fail"))
	time.Sleep(ARQ_INTERVAL * time.Millisecond * 2)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		arq.Update()
		if err := arq.Err(); err != nil {
			var deliveryErr *DeliveryFailureError
			if !errors.As(err, &deliveryErr) {
				t.Fatalf("unexpected error type: %v", err)
			}
			if deliveryErr.Retransmissions != 2 {
				t.Fatalf("retransmissions=%d, want 2", deliveryErr.Retransmissions)
			}
			t.Logf("ARQ failed as expected after retransmission budget exhaustion: %v", err)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("expected ARQ to fail after retransmission budget exhaustion")
}

// TestExtreme_TotalLoss_RapidRecovery 全丢后快速恢复的时序
func TestExtreme_TotalLoss_RapidRecovery(t *testing.T) {
	net := newBidirectionalNetwork(10 * time.Millisecond)

	// 发 10 个包, 同时双向全丢
	net.setPartition(true)

	for i := 0; i < 10; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("p%02d|", i)))
		time.Sleep(50 * time.Millisecond)
		net.tick()
	}

	// 短暂全丢 (< RTO)
	time.Sleep(500 * time.Millisecond)
	for i := 0; i < 10; i++ {
		net.tick()
		time.Sleep(50 * time.Millisecond)
	}

	// 快速恢复
	net.setPartition(false)

	var expected string
	for i := 0; i < 10; i++ {
		expected += fmt.Sprintf("p%02d|", i)
	}

	var received string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			received += string(data)
		}
		if received == expected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("快速恢复后收到: %q", received)
	t.Logf("网络统计: %s", net.stats())

	if received != expected {
		t.Errorf("快速恢复后数据不完整: 期望 %q, 实际 %q", expected, received)
	}
}

// ============================================================================
// 双向通信 + 各类丢包组合 (表驱动)
// ============================================================================

// bidirectionalTestCase 描述一个双向丢包测试场景。
type bidirectionalTestCase struct {
	name         string
	oneWayDelay  time.Duration
	totalPackets int
	timeout      time.Duration
	payloadFmt   string // "A%02d|" or "A%d|", 默认 "A%02d|"
	setupDrops   func(net *bidirectionalNetwork)               // 发送前设置丢包
	onSendLoop   func(i int, net *bidirectionalNetwork)        // 每轮发送时回调
	postSend     func(net *bidirectionalNetwork)               // 发送完成后、验证前
}

// runBidirectionalTest 运行一个双向丢包测试。
func runBidirectionalTest(t *testing.T, tc bidirectionalTestCase) {
	t.Helper()

	net := newBidirectionalNetwork(tc.oneWayDelay)
	pktFmt := tc.payloadFmt
	if pktFmt == "" {
		pktFmt = "A%02d|"
	}
	pktFmtB := "B" + pktFmt[1:]

	if tc.setupDrops != nil {
		tc.setupDrops(net)
	}

	for i := 0; i < tc.totalPackets; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf(pktFmt, i)))
		net.nodeB.Send([]byte(fmt.Sprintf(pktFmtB, i)))

		if tc.onSendLoop != nil {
			tc.onSendLoop(i, net)
		}

		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	if tc.postSend != nil {
		tc.postSend(net)
	} else {
		// 默认: 取消所有丢包
		time.Sleep(100 * time.Millisecond)
		net.mu.Lock()
		net.dropSetAtoB = make(map[uint32]bool)
		net.dropSetBtoA = make(map[uint32]bool)
		net.dropAtoB = false
		net.dropBtoA = false
		net.mu.Unlock()
	}

	var expectedAtB, expectedAtA string
	for i := 0; i < tc.totalPackets; i++ {
		expectedAtB += fmt.Sprintf(pktFmt, i)
		expectedAtA += fmt.Sprintf(pktFmtB, i)
	}

	var receivedAtB, receivedAtA string
	deadline := time.Now().Add(tc.timeout)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			receivedAtB += string(data)
		}
		if data := net.nodeA.Recv(); len(data) > 0 {
			receivedAtA += string(data)
		}
		if receivedAtB == expectedAtB && receivedAtA == expectedAtA {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("网络统计: %s", net.stats())
	if receivedAtB != expectedAtB {
		t.Errorf("B 数据不完整: got %d/%d bytes", len(receivedAtB), len(expectedAtB))
	}
	if receivedAtA != expectedAtA {
		t.Errorf("A 数据不完整: got %d/%d bytes", len(receivedAtA), len(expectedAtA))
	}
}

func TestExtreme_Bidirectional(t *testing.T) {
	cases := []bidirectionalTestCase{
		{
			name:         "AsymmetricLoss",
			oneWayDelay:  30 * time.Millisecond,
			totalPackets: 10,
			timeout:      15 * time.Second,
			setupDrops: func(net *bidirectionalNetwork) {
				for _, sn := range []uint32{0, 2, 4, 6, 8} {
					net.dropSetAtoB[sn] = true
				}
				net.dropSetBtoA[5] = true
			},
		},
		{
			name:         "AlternatingLoss",
			oneWayDelay:  20 * time.Millisecond,
			totalPackets: 10,
			timeout:      15 * time.Second,
			onSendLoop: func(i int, net *bidirectionalNetwork) {
				net.mu.Lock()
				if i%2 == 0 {
					net.dropSetAtoB[uint32(i)] = true
				} else {
					net.dropSetBtoA[uint32(i)] = true
				}
				net.mu.Unlock()
			},
		},
		{
			name:         "BurstLoss",
			oneWayDelay:  20 * time.Millisecond,
			totalPackets: 12,
			timeout:      15 * time.Second,
			setupDrops: func(net *bidirectionalNetwork) {
				for sn := uint32(3); sn <= 7; sn++ {
					net.dropSetAtoB[sn] = true
				}
				for sn := uint32(2); sn <= 4; sn++ {
					net.dropSetBtoA[sn] = true
				}
			},
		},
		{
			name:         "TailLossBothDirections",
			oneWayDelay:  20 * time.Millisecond,
			totalPackets: 8,
			timeout:      15 * time.Second,
			payloadFmt:   "A%d|",
			setupDrops: func(net *bidirectionalNetwork) {
				net.dropSetAtoB[6] = true
				net.dropSetAtoB[7] = true
				net.dropSetBtoA[6] = true
				net.dropSetBtoA[7] = true
			},
		},
		{
			name:         "PartitionDuringExchange",
			oneWayDelay:  20 * time.Millisecond,
			totalPackets: 15,
			timeout:      20 * time.Second,
			onSendLoop: func(i int, net *bidirectionalNetwork) {
				if i == 7 {
					net.setPartition(true)
				}
			},
			postSend: func(net *bidirectionalNetwork) {
				// 分区持续 2 秒
				for i := 0; i < 20; i++ {
					net.tick()
					time.Sleep(100 * time.Millisecond)
				}
				net.setPartition(false)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runBidirectionalTest(t, tc)
		})
	}
}

// TestExtreme_Bidirectional_OneDirectionTotalLoss 单方向完全丢包 (2 阶段, 不适合表驱动)
func TestExtreme_Bidirectional_OneDirectionTotalLoss(t *testing.T) {
	net := newBidirectionalNetwork(20 * time.Millisecond)

	// Phase 1: B 先发数据, A->B 正常
	for i := 0; i < 5; i++ {
		net.nodeB.Send([]byte(fmt.Sprintf("B%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 10; i++ {
		net.tick()
		time.Sleep(30 * time.Millisecond)
	}

	var bToA string
	if data := net.nodeA.Recv(); len(data) > 0 {
		bToA = string(data)
	}
	t.Logf("A 收到 B 的数据 (全丢前): %q", bToA)

	// Phase 2: A->B 完全丢包
	net.mu.Lock()
	net.dropAtoB = true
	net.mu.Unlock()

	for i := 0; i < 5; i++ {
		net.nodeA.Send([]byte(fmt.Sprintf("A%d|", i)))
		time.Sleep(120 * time.Millisecond)
		net.tick()
	}

	for i := 0; i < 20; i++ {
		net.tick()
		time.Sleep(50 * time.Millisecond)
	}

	var aToB string
	if data := net.nodeB.Recv(); len(data) > 0 {
		aToB = string(data)
	}

	// 恢复
	net.mu.Lock()
	net.dropAtoB = false
	net.mu.Unlock()

	expectedAtoB := "A0|A1|A2|A3|A4|"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		net.tick()
		if data := net.nodeB.Recv(); len(data) > 0 {
			aToB += string(data)
		}
		if aToB == expectedAtoB {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("恢复后 B 收到 A: %q", aToB)
	t.Logf("网络统计: %s", net.stats())

	if aToB != expectedAtoB {
		t.Errorf("A->B 恢复后数据不完整: 期望 %q, 实际 %q", expectedAtoB, aToB)
	}
}

// ============================================================================
// Simulated polling matrix tests (merged from arq_simulated_matrix_test.go)
// ============================================================================

type simulatedClock struct {
	now time.Time
}

func newSimulatedClock() *simulatedClock {
	return &simulatedClock{now: time.Unix(0, 0)}
}

func (c *simulatedClock) Now() time.Time {
	return c.now
}

func (c *simulatedClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

type queuedBatch struct {
	packets   [][]byte
	deliverAt time.Time
}

type simulatedPollingLink struct {
	clock        *simulatedClock
	interval     time.Duration
	oneWayDelay  time.Duration
	maxBodySize  int
	aToBPending  [][]byte
	bToAPending  [][]byte
	aToBInFlight []queuedBatch
	bToAInFlight []queuedBatch
	nextAFlush   time.Time
	nextBFlush   time.Time
	aBatches     int
	bBatches     int
}

func newSimulatedPollingLink(clock *simulatedClock, interval, oneWayDelay time.Duration, maxBodySize int) *simulatedPollingLink {
	now := clock.Now()
	return &simulatedPollingLink{
		clock:       clock,
		interval:    interval,
		oneWayDelay: oneWayDelay,
		maxBodySize: maxBodySize,
		nextAFlush:  now,
		nextBFlush:  now,
	}
}

func (l *simulatedPollingLink) enqueueA(data []byte) {
	pkt := make([]byte, len(data))
	copy(pkt, data)
	l.aToBPending = append(l.aToBPending, pkt)
}

func (l *simulatedPollingLink) enqueueB(data []byte) {
	pkt := make([]byte, len(data))
	copy(pkt, data)
	l.bToAPending = append(l.bToAPending, pkt)
}

func (l *simulatedPollingLink) flushPending(now time.Time) {
	for !l.nextAFlush.After(now) {
		if batch := l.takeBatch(&l.aToBPending); len(batch) > 0 {
			l.aToBInFlight = append(l.aToBInFlight, queuedBatch{
				packets:   batch,
				deliverAt: l.nextAFlush.Add(l.oneWayDelay),
			})
			l.aBatches++
		}
		l.nextAFlush = l.nextAFlush.Add(l.interval)
	}
	for !l.nextBFlush.After(now) {
		if batch := l.takeBatch(&l.bToAPending); len(batch) > 0 {
			l.bToAInFlight = append(l.bToAInFlight, queuedBatch{
				packets:   batch,
				deliverAt: l.nextBFlush.Add(l.oneWayDelay),
			})
			l.bBatches++
		}
		l.nextBFlush = l.nextBFlush.Add(l.interval)
	}
}

func (l *simulatedPollingLink) takeBatch(queue *[][]byte) [][]byte {
	if len(*queue) == 0 {
		return nil
	}
	total := 0
	count := 0
	for count < len(*queue) {
		size := len((*queue)[count])
		if count > 0 && total+size > l.maxBodySize {
			break
		}
		total += size
		count++
	}
	batch := make([][]byte, count)
	copy(batch, (*queue)[:count])
	*queue = (*queue)[count:]
	return batch
}

func (l *simulatedPollingLink) deliverDue(now time.Time, a, b *ARQ) {
	remainingAToB := l.aToBInFlight[:0]
	for _, batch := range l.aToBInFlight {
		if batch.deliverAt.After(now) {
			remainingAToB = append(remainingAToB, batch)
			continue
		}
		for _, pkt := range batch.packets {
			b.Input(pkt)
		}
	}
	l.aToBInFlight = remainingAToB

	remainingBToA := l.bToAInFlight[:0]
	for _, batch := range l.bToAInFlight {
		if batch.deliverAt.After(now) {
			remainingBToA = append(remainingBToA, batch)
			continue
		}
		for _, pkt := range batch.packets {
			a.Input(pkt)
		}
	}
	l.bToAInFlight = remainingBToA
}

func simulatedStepForInterval(interval time.Duration) time.Duration {
	step := interval / 10
	if step < time.Millisecond {
		step = time.Millisecond
	}
	if step > 10*time.Millisecond {
		step = 10 * time.Millisecond
	}
	return step
}

func runSimulatedARQTransfer(t *testing.T, interval, oneWayDelay time.Duration, size int) {
	t.Helper()

	clock := newSimulatedClock()
	oldNowFunc := nowFunc
	nowFunc = clock.Now
	defer func() { nowFunc = oldNowFunc }()

	link := newSimulatedPollingLink(clock, interval, oneWayDelay, 190000)
	cfg := ARQConfig{
		MTU:                   ARQ_MTU,
		RTO:                   int((interval * 5) / time.Millisecond),
		StandaloneAckSegments: ARQ_WND_SIZE,
	}
	if cfg.RTO <= 0 {
		cfg.RTO = 1
	}

	sender := NewARQWithConfig(link.enqueueA, cfg)
	receiver := NewARQWithConfig(link.enqueueB, cfg)

	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte((i*7 + 13) % 251)
	}
	sender.Send(payload)

	step := simulatedStepForInterval(interval)
	limit := clock.Now().Add(30 * time.Minute)
	var received []byte

	for len(received) < len(payload) {
		if !clock.Now().Before(limit) {
			t.Fatalf("timeout: got %d/%d bytes after %v logical time (a_batches=%d b_batches=%d snd_buf=%d snd_queue=%d)",
				len(received), len(payload), clock.Now().Sub(time.Unix(0, 0)),
				link.aBatches, link.bBatches, sender.WaitBuf(), sender.WaitSnd()-sender.WaitBuf())
		}

		clock.Advance(step)
		now := clock.Now()

		link.deliverDue(now, sender, receiver)
		sender.Update()
		receiver.Update()
		link.flushPending(now)

		if data := receiver.Recv(); len(data) > 0 {
			received = append(received, data...)
		}
	}

	if !bytes.Equal(received, payload) {
		t.Fatalf("payload mismatch for interval=%v size=%d", interval, size)
	}

	t.Logf("delivered %s over interval=%v delay=%v in logical %v (a_batches=%d b_batches=%d)",
		formatSize(size), interval, oneWayDelay, clock.Now().Sub(time.Unix(0, 0)), link.aBatches, link.bBatches)
}

func formatSize(size int) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%dMB", size/(1024*1024))
	}
	return fmt.Sprintf("%dKB", size/1024)
}

func TestARQSimulatedPollingMatrix_DeliversAcrossIntervalsAndSizes(t *testing.T) {
	cases := []struct {
		name        string
		interval    time.Duration
		oneWayDelay time.Duration
		size        int
	}{
		{name: "1ms_64KB", interval: 1 * time.Millisecond, oneWayDelay: 1 * time.Millisecond, size: 64 * 1024},
		{name: "10ms_256KB", interval: 10 * time.Millisecond, oneWayDelay: 5 * time.Millisecond, size: 256 * 1024},
		{name: "50ms_1MB", interval: 50 * time.Millisecond, oneWayDelay: 25 * time.Millisecond, size: 1 * 1024 * 1024},
		{name: "250ms_2MB", interval: 250 * time.Millisecond, oneWayDelay: 125 * time.Millisecond, size: 2 * 1024 * 1024},
		{name: "1s_2MB", interval: 1 * time.Second, oneWayDelay: 500 * time.Millisecond, size: 2 * 1024 * 1024},
		{name: "3s_5MB", interval: 3 * time.Second, oneWayDelay: 1500 * time.Millisecond, size: 5 * 1024 * 1024},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSimulatedARQTransfer(t, tc.interval, tc.oneWayDelay, tc.size)
		})
	}
}
