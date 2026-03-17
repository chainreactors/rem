package message

import (
	"bytes"
	"fmt"
	"testing"
)

func roundTrip[T Message](orig T, dest T) error {
	data, err := orig.MarshalVT()
	if err != nil {
		return err
	}
	if len(data) != orig.SizeVT() {
		return fmt.Errorf("SizeVT()=%d but MarshalVT produced %d bytes", orig.SizeVT(), len(data))
	}
	return dest.UnmarshalVT(data)
}

// ===== Basic message round-trips =====

func TestLoginRoundTrip(t *testing.T) {
	orig := &Login{
		ConsoleIP:    "192.168.1.100",
		ConsolePort:  8443,
		ConsoleProto: "tcp",
		Mod:          "socks5",
		Token:        "secret-token-xyz",
		Agent:        "agent-001",
		Interfaces:   []string{"eth0", "wlan0", "lo"},
		Hostname:     "workstation",
		Username:     "admin",
		Wrapper:      "tls",
	}
	got := &Login{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if got.ConsoleIP != orig.ConsoleIP || got.ConsolePort != orig.ConsolePort ||
		got.ConsoleProto != orig.ConsoleProto || got.Mod != orig.Mod ||
		got.Token != orig.Token || got.Agent != orig.Agent ||
		got.Hostname != orig.Hostname || got.Username != orig.Username ||
		got.Wrapper != orig.Wrapper {
		t.Errorf("scalar fields mismatch:\n  got  %+v\n  want %+v", got, orig)
	}
	if len(got.Interfaces) != len(orig.Interfaces) {
		t.Fatalf("Interfaces len=%d, want %d", len(got.Interfaces), len(orig.Interfaces))
	}
	for i := range orig.Interfaces {
		if got.Interfaces[i] != orig.Interfaces[i] {
			t.Errorf("Interfaces[%d]=%q, want %q", i, got.Interfaces[i], orig.Interfaces[i])
		}
	}
}

func TestLoginEmpty(t *testing.T) {
	orig := &Login{}
	data, _ := orig.MarshalVT()
	if len(data) != 0 {
		t.Errorf("empty Login should serialize to 0 bytes, got %d", len(data))
	}
	got := &Login{}
	if err := got.UnmarshalVT(data); err != nil {
		t.Fatal(err)
	}
}

func TestControlRoundTrip(t *testing.T) {
	orig := &Control{
		Source:      "node-a",
		Destination: "node-b",
		Mod:         "forward",
		Remote:      "10.0.0.1:80",
		Local:       "127.0.0.1:8080",
		Fork:        true,
		Options: map[string]string{
			"timeout": "30s",
			"retry":   "3",
			"mode":    "fast",
		},
	}
	got := &Control{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if got.Source != orig.Source || got.Destination != orig.Destination ||
		got.Mod != orig.Mod || got.Remote != orig.Remote ||
		got.Local != orig.Local || got.Fork != orig.Fork {
		t.Errorf("scalar fields mismatch:\n  got  %+v\n  want %+v", got, orig)
	}
	if len(got.Options) != len(orig.Options) {
		t.Fatalf("Options len=%d, want %d", len(got.Options), len(orig.Options))
	}
	for k, v := range orig.Options {
		if got.Options[k] != v {
			t.Errorf("Options[%q]=%q, want %q", k, got.Options[k], v)
		}
	}
}

func TestControlEmptyMap(t *testing.T) {
	orig := &Control{Mod: "test"}
	got := &Control{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if got.Options != nil {
		t.Errorf("nil map should stay nil, got %v", got.Options)
	}
}

func TestAckRoundTrip(t *testing.T) {
	orig := &Ack{Status: 200, Error: "something went wrong", Port: 9090}
	got := &Ack{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if *got != *orig {
		t.Errorf("got %+v, want %+v", got, orig)
	}
}

func TestPingPongRoundTrip(t *testing.T) {
	ping := &Ping{Ping: "hello"}
	gotPing := &Ping{}
	if err := roundTrip(ping, gotPing); err != nil {
		t.Fatal(err)
	}
	if gotPing.Ping != ping.Ping {
		t.Errorf("Ping=%q, want %q", gotPing.Ping, ping.Ping)
	}

	pong := &Pong{Pong: "world"}
	gotPong := &Pong{}
	if err := roundTrip(pong, gotPong); err != nil {
		t.Fatal(err)
	}
	if gotPong.Pong != pong.Pong {
		t.Errorf("Pong=%q, want %q", gotPong.Pong, pong.Pong)
	}
}

func TestConnStartRoundTrip(t *testing.T) {
	orig := &ConnStart{ID: 0xDEADBEEF, Destination: "10.0.0.1:443", Source: "192.168.1.1:12345"}
	got := &ConnStart{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if *got != *orig {
		t.Errorf("got %+v, want %+v", got, orig)
	}
}

func TestConnEndRoundTrip(t *testing.T) {
	orig := &ConnEnd{ID: 42, Msg: "connection reset"}
	got := &ConnEnd{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if *got != *orig {
		t.Errorf("got %+v, want %+v", got, orig)
	}
}

func TestCWNDRoundTrip(t *testing.T) {
	orig := &CWND{Bridge: 999, CWND: true, Token: -12345}
	got := &CWND{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if got.Bridge != orig.Bridge || got.CWND != orig.CWND || got.Token != orig.Token {
		t.Errorf("got %+v, want %+v", got, orig)
	}
}

// ===== Packet with binary data =====

func TestPacketBinaryData(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"simple", []byte("hello world")},
		{"binary_all_zeros", make([]byte, 256)},
		{"binary_all_0xff", bytes.Repeat([]byte{0xff}, 256)},
		{"mixed_binary", func() []byte {
			b := make([]byte, 512)
			for i := range b {
				b[i] = byte(i % 256)
			}
			return b
		}()},
		{"null_bytes_embedded", []byte{0x00, 0x01, 0x00, 0x02, 0x00, 0x00, 0x03}},
		{"empty_data", []byte{}},
		{"single_byte", []byte{0x42}},
		{"large_payload", make([]byte, 64*1024)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := &Packet{ID: 7, Index: 3, Data: tt.data}
			got := &Packet{}
			if err := roundTrip(orig, got); err != nil {
				t.Fatal(err)
			}
			if got.ID != orig.ID || got.Index != orig.Index {
				t.Errorf("ID/Index mismatch: got (%d,%d), want (%d,%d)",
					got.ID, got.Index, orig.ID, orig.Index)
			}
			if !bytes.Equal(got.Data, orig.Data) {
				t.Errorf("Data mismatch: got len=%d, want len=%d", len(got.Data), len(orig.Data))
			}
		})
	}
}

func TestPacketNilData(t *testing.T) {
	orig := &Packet{ID: 1, Index: 0, Data: nil}
	got := &Packet{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if got.Data != nil {
		t.Errorf("nil Data should stay nil, got len=%d", len(got.Data))
	}
}

// ===== Redirect nested oneof =====

func TestRedirectWithConnStart(t *testing.T) {
	orig := &Redirect{
		Source:      "src-node",
		Destination: "dst-node",
		Route:       "route-1",
		Msg: &Redirect_Start{Start: &ConnStart{
			ID:          100,
			Destination: "10.0.0.5:443",
			Source:      "192.168.1.10:54321",
		}},
	}
	got := &Redirect{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if got.Source != orig.Source || got.Destination != orig.Destination || got.Route != orig.Route {
		t.Errorf("outer fields mismatch: got (%q,%q,%q), want (%q,%q,%q)",
			got.Source, got.Destination, got.Route,
			orig.Source, orig.Destination, orig.Route)
	}
	start := got.GetStart()
	if start == nil {
		t.Fatal("expected Redirect_Start, got nil")
	}
	origStart := orig.GetStart()
	if start.ID != origStart.ID || start.Destination != origStart.Destination || start.Source != origStart.Source {
		t.Errorf("nested ConnStart mismatch: got %+v, want %+v", start, origStart)
	}
}

func TestRedirectWithPacket(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x01, 0x02, 0x03}
	orig := &Redirect{
		Source:      "a",
		Destination: "b",
		Msg: &Redirect_Packet{Packet: &Packet{
			ID:    200,
			Index: 5,
			Data:  payload,
		}},
	}
	got := &Redirect{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	pkt := got.GetPacket()
	if pkt == nil {
		t.Fatal("expected Redirect_Packet, got nil")
	}
	if pkt.ID != 200 || pkt.Index != 5 {
		t.Errorf("nested Packet ID/Index mismatch: got (%d,%d), want (200,5)", pkt.ID, pkt.Index)
	}
	if !bytes.Equal(pkt.Data, payload) {
		t.Errorf("nested Packet Data mismatch: got %x, want %x", pkt.Data, payload)
	}
}

func TestRedirectWithConnEnd(t *testing.T) {
	orig := &Redirect{
		Source: "x",
		Route:  "route-2",
		Msg:    &Redirect_End{End: &ConnEnd{ID: 300, Msg: "EOF"}},
	}
	got := &Redirect{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	end := got.GetEnd()
	if end == nil {
		t.Fatal("expected Redirect_End, got nil")
	}
	if end.ID != 300 || end.Msg != "EOF" {
		t.Errorf("nested ConnEnd mismatch: got %+v, want {ID:300 Msg:EOF}", end)
	}
}

func TestRedirectWithCWND(t *testing.T) {
	orig := &Redirect{
		Destination: "target",
		Msg:         &Redirect_Cwnd{Cwnd: &CWND{Bridge: 50, CWND: true, Token: -999}},
	}
	got := &Redirect{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	cwnd := got.GetCwnd()
	if cwnd == nil {
		t.Fatal("expected Redirect_Cwnd, got nil")
	}
	if cwnd.Bridge != 50 || !cwnd.CWND || cwnd.Token != -999 {
		t.Errorf("nested CWND mismatch: got %+v", cwnd)
	}
}

func TestRedirectNoMsg(t *testing.T) {
	orig := &Redirect{Source: "s", Destination: "d", Route: "r"}
	got := &Redirect{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	if got.Msg != nil {
		t.Errorf("expected nil Msg, got %T", got.Msg)
	}
	if got.Source != "s" || got.Destination != "d" || got.Route != "r" {
		t.Errorf("outer fields mismatch: %+v", got)
	}
}

func TestRedirectPacketLargeBinary(t *testing.T) {
	payload := make([]byte, 16*1024)
	for i := range payload {
		payload[i] = byte((i*7 + 13) % 256)
	}
	orig := &Redirect{
		Source:      "relay-1",
		Destination: "relay-2",
		Route:       "chain",
		Msg: &Redirect_Packet{Packet: &Packet{
			ID:    ^uint64(0),
			Index: -1,
			Data:  payload,
		}},
	}
	got := &Redirect{}
	if err := roundTrip(orig, got); err != nil {
		t.Fatal(err)
	}
	pkt := got.GetPacket()
	if pkt == nil {
		t.Fatal("expected Redirect_Packet")
	}
	if pkt.ID != ^uint64(0) {
		t.Errorf("ID=%d, want max uint64", pkt.ID)
	}
	if pkt.Index != -1 {
		t.Errorf("Index=%d, want -1", pkt.Index)
	}
	if !bytes.Equal(pkt.Data, payload) {
		t.Errorf("payload mismatch: got len=%d, want len=%d", len(pkt.Data), len(payload))
	}
}

// ===== SizeVT consistency across all message types =====

func TestAllMessagesSizeConsistency(t *testing.T) {
	messages := []struct {
		name string
		msg  Message
	}{
		{"Login_full", &Login{
			ConsoleIP: "1.2.3.4", ConsolePort: 443, ConsoleProto: "tcp",
			Mod: "m", Token: "t", Agent: "a", Interfaces: []string{"i1", "i2"},
			Hostname: "h", Username: "u", Wrapper: "w",
		}},
		{"Control_with_map", &Control{
			Source: "s", Destination: "d", Mod: "m", Remote: "r", Local: "l",
			Fork: true, Options: map[string]string{"k1": "v1", "k2": "v2"},
		}},
		{"Ack", &Ack{Status: 1, Error: "err", Port: 80}},
		{"Ping", &Ping{Ping: "p"}},
		{"Pong", &Pong{Pong: "p"}},
		{"ConnStart", &ConnStart{ID: 1, Destination: "d", Source: "s"}},
		{"ConnEnd", &ConnEnd{ID: 1, Msg: "m"}},
		{"Packet_binary", &Packet{ID: 1, Index: 2, Data: []byte{0, 1, 2, 3, 4, 5}}},
		{"CWND", &CWND{Bridge: 1, CWND: true, Token: 100}},
		{"Redirect_start", &Redirect{
			Source: "s", Destination: "d", Route: "r",
			Msg: &Redirect_Start{Start: &ConnStart{ID: 1, Destination: "d"}},
		}},
		{"Redirect_packet", &Redirect{
			Msg: &Redirect_Packet{Packet: &Packet{ID: 1, Data: []byte{0xff}}},
		}},
		{"Redirect_end", &Redirect{
			Msg: &Redirect_End{End: &ConnEnd{ID: 1, Msg: "done"}},
		}},
		{"Redirect_cwnd", &Redirect{
			Msg: &Redirect_Cwnd{Cwnd: &CWND{Bridge: 1, CWND: true}},
		}},
	}
	for _, tt := range messages {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.msg.MarshalVT()
			if err != nil {
				t.Fatal(err)
			}
			if len(data) != tt.msg.SizeVT() {
				t.Errorf("SizeVT()=%d, actual marshal len=%d", tt.msg.SizeVT(), len(data))
			}
		})
	}
}

func TestReconfigureRoundTrip(t *testing.T) {
	orig := &Reconfigure{
		Options: map[string]string{
			"interval":    "5000",
			"maxBodySize": "200000",
			"custom":      "value",
		},
	}
	dest := &Reconfigure{}
	if err := roundTrip(orig, dest); err != nil {
		t.Fatal(err)
	}
	if len(dest.Options) != len(orig.Options) {
		t.Fatalf("Options length mismatch: got %d, want %d", len(dest.Options), len(orig.Options))
	}
	for k, v := range orig.Options {
		if dest.Options[k] != v {
			t.Fatalf("Options[%q] = %q, want %q", k, dest.Options[k], v)
		}
	}
}

func TestReconfigureEmpty(t *testing.T) {
	orig := &Reconfigure{}
	dest := &Reconfigure{}
	if err := roundTrip(orig, dest); err != nil {
		t.Fatal(err)
	}
	if dest.Options != nil && len(dest.Options) != 0 {
		t.Fatalf("expected nil or empty Options, got %v", dest.Options)
	}
}

func TestReconfigureMessageType(t *testing.T) {
	msg := &Reconfigure{Options: map[string]string{"k": "v"}}
	if GetMessageType(msg) != ReconfigureMsg {
		t.Fatalf("GetMessageType = %d, want %d", GetMessageType(msg), ReconfigureMsg)
	}
	created := NewMessage(ReconfigureMsg)
	if created == nil {
		t.Fatal("NewMessage(ReconfigureMsg) returned nil")
	}
	if _, ok := created.(*Reconfigure); !ok {
		t.Fatalf("NewMessage(ReconfigureMsg) returned %T, want *Reconfigure", created)
	}
}
