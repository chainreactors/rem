package message

import (
	"fmt"
	"github.com/chainreactors/rem/x/utils"
)

// MsgType 定义消息类型
type MsgType int8

// 定义消息状态常量
const (
	StatusFailed  = 0
	StatusSuccess = 1
)

// 定义消息类型常量
const (
	LoginMsg MsgType = iota + 1
	AckMsg
	ControlMsg
	PingMsg
	PongMsg
	PacketMsg
	ConnStartMsg
	ConnEndMsg
	RedirectMsg
	CWNDMsg
	BridgeOpenMsg
	BridgeCloseMsg
	ReconfigureMsg
	End
)

// 定义标准错误类型
var (
	ErrEmptyMessage    = fmt.Errorf("empty message")
	ErrInvalidType     = fmt.Errorf("invalid message type")
	ErrUnknownType     = fmt.Errorf("unknown message type")
	ErrMarshal         = fmt.Errorf("marshal error")
	ErrUnmarshal       = fmt.Errorf("unmarshal error")
	ErrTypeMismatch    = fmt.Errorf("message type mismatch")
	ErrMessageLength   = fmt.Errorf("message length error")
	ErrInvalidStatus   = fmt.Errorf("invalid message status")
	ErrConnectionError = fmt.Errorf("connection error")
)

// NewMessage 根据消息类型创建新的消息实例
// 使用 switch 而非 map，避免 TinyGo WASM 中 map key 损坏问题
func NewMessage(msgType MsgType) Message {
	switch msgType {
	case LoginMsg:
		return &Login{}
	case AckMsg:
		return &Ack{}
	case ControlMsg:
		return &Control{}
	case PingMsg:
		return &Ping{}
	case PongMsg:
		return &Pong{}
	case PacketMsg:
		return &Packet{}
	case ConnStartMsg:
		return &ConnStart{}
	case ConnEndMsg:
		return &ConnEnd{}
	case RedirectMsg:
		return &Redirect{}
	case CWNDMsg:
		return &CWND{}
	case BridgeOpenMsg:
		return &BridgeOpen{}
	case BridgeCloseMsg:
		return &BridgeClose{}
	case ReconfigureMsg:
		return &Reconfigure{}
	default:
		return nil
	}
}

// ValidateMessageType 验证消息类型是否有效
func ValidateMessageType(msgType MsgType) bool {
	return msgType > 0 && msgType < End
}

// WrapError 包装错误信息
func WrapError(err error, format string, args ...interface{}) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: "+format, append([]interface{}{err}, args...)...)
}

// GetMessageType 根据消息实例获取消息类型
func GetMessageType(msg Message) MsgType {
	if msg == nil {
		return 0
	}

	switch msg.(type) {
	case *Login:
		return LoginMsg
	case *Ack:
		return AckMsg
	case *Control:
		return ControlMsg
	case *Ping:
		return PingMsg
	case *Pong:
		return PongMsg
	case *Packet:
		return PacketMsg
	case *ConnStart:
		return ConnStartMsg
	case *ConnEnd:
		return ConnEndMsg
	case *Redirect:
		return RedirectMsg
	case *CWND:
		return CWNDMsg
	case *BridgeOpen:
		return BridgeOpenMsg
	case *BridgeClose:
		return BridgeCloseMsg
	case *Reconfigure:
		return ReconfigureMsg
	default:
		return 0
	}
}

func Wrap(src, dst string, m Message) *Redirect {
	msg := &Redirect{
		Source:      src,
		Destination: dst,
	}
	switch m.(type) {
	case *Packet:
		msg.Msg = &Redirect_Packet{Packet: m.(*Packet)}
	case *ConnStart:
		msg.Msg = &Redirect_Start{Start: m.(*ConnStart)}
	case *ConnEnd:
		msg.Msg = &Redirect_End{End: m.(*ConnEnd)}
	case *CWND:
		msg.Msg = &Redirect_Cwnd{Cwnd: m.(*CWND)}

	default:
		utils.Log.Error(ErrInvalidType)
	}
	return msg
}

func Unwrap(m *Redirect) Message {
	var msg Message
	switch m.GetMsg().(type) {
	case *Redirect_Packet:
		msg = m.GetPacket()
	case *Redirect_Start:
		msg = m.GetStart()
	case *Redirect_End:
		msg = m.GetEnd()
	case *Redirect_Cwnd:
		msg = m.GetCwnd()
	default:
		utils.Log.Error(ErrInvalidType)
	}
	return msg
}
