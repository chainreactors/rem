package cio

import (
	"encoding/binary"
	"net"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/protocol/message"
	"github.com/chainreactors/rem/x/utils"
)

func unpack(bs []byte, mType message.MsgType) (message.Message, error) {
	if len(bs) == 0 {
		return nil, message.ErrEmptyMessage
	}

	if !message.ValidateMessageType(mType) {
		return nil, message.WrapError(message.ErrInvalidType, "type: %d", mType)
	}

	msg := message.NewMessage(mType)
	if msg == nil {
		return nil, message.WrapError(message.ErrUnknownType, "type: %d", mType)
	}

	if err := msg.UnmarshalVT(bs); err != nil {
		return nil, message.WrapError(message.ErrUnmarshal, err.Error())
	}
	return msg, nil
}

func pack(msg message.Message) ([]byte, error) {
	msgType := message.GetMessageType(msg)
	if msgType == 0 {
		return nil, message.ErrInvalidType
	}

	content, err := msg.MarshalVT()
	if err != nil {
		return nil, message.WrapError(message.ErrMarshal, err.Error())
	}

	buf := GetBuf(1 + 4 + len(content))
	buf[0] = byte(msgType)
	binary.LittleEndian.PutUint32(buf[1:5], uint32(len(content)))
	copy(buf[5:], content)
	return buf, nil
}

func WriteMsg(conn net.Conn, msg message.Message) error {
	packedmsg, err := pack(msg)
	if err != nil {
		return err
	}
	defer PutBuf(packedmsg)
	utils.Log.Logf(utils.IOLog, "[write] %s to %s, %d bytes\n",
		conn.LocalAddr().String(), conn.RemoteAddr().String(), len(packedmsg))
	utils.Log.Logf(utils.DUMPLog, "[write] %v", packedmsg)
	n, err := conn.Write(packedmsg)
	if n != len(packedmsg) {
		utils.Log.Debugf("write error, %s", err.Error())
		return message.WrapError(message.ErrConnectionError,
			"write %d bytes, expected %d bytes", n, len(packedmsg))
	}
	return err
}

// readFull reads exactly len(buf) bytes from conn without using io.ReadFull.
// This avoids TinyGo WASM issues with io.Reader interface conversion.
func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func ReadMsg(conn net.Conn) (message.Message, error) {
	header := GetBuf(5)
	defer PutBuf(header)

	_, err := readFull(conn, header)
	if err != nil {
		utils.Log.Logf(utils.IOLog, "[read] %s from %s: read greet error, %s\n",
			conn.RemoteAddr().String(), conn.LocalAddr().String(), err.Error())
		return nil, message.WrapError(message.ErrConnectionError, err.Error())
	}

	mtype := message.MsgType(header[0])
	if int(mtype) >= int(message.End) {
		return nil, message.WrapError(message.ErrInvalidType,
			"invalid message type %d", header[0])
	}

	length := binary.LittleEndian.Uint32(header[1:5])
	if int(length) > core.MaxPacketSize {
		return nil, message.WrapError(message.ErrMessageLength,
			"message length %d exceeds max size %d", length, core.MaxPacketSize)
	}

	utils.Log.Logf(utils.IOLog, "[read] %s from %s, %d bytes \n",
		conn.RemoteAddr().String(), conn.LocalAddr().String(), length)

	bs := GetBuf(int(length))
	defer PutBuf(bs)

	n, err := readFull(conn, bs)
	if err != nil {
		return nil, message.WrapError(message.ErrConnectionError, err.Error())
	}
	if n != int(length) {
		return nil, message.WrapError(message.ErrMessageLength,
			"expected %d, got %d", length, n)
	}

	msg, err := unpack(bs, mtype)

	if err != nil {
		return nil, err
	}

	utils.Log.Logf(utils.DUMPLog, "[read] %v", bs)
	return msg, nil
}

func ReadAndAssertMsg(conn net.Conn, expect message.MsgType) (message.Message, error) {
	msg, err := ReadMsg(conn)
	if err != nil {
		return nil, err
	}

	actualType := message.GetMessageType(msg)
	if actualType != expect {
		return nil, message.WrapError(message.ErrTypeMismatch,
			"expected type %d, got %d", expect, actualType)
	}
	return msg, nil
}

func WriteAndAssertMsg(conn net.Conn, msg message.Message) (*message.Ack, error) {
	err := WriteMsg(conn, msg)
	if err != nil {
		return nil, err
	}

	ackMsg, err := ReadAndAssertMsg(conn, message.AckMsg)
	if err != nil {
		return nil, err
	}

	ack := ackMsg.(*message.Ack)
	if ack.Status != message.StatusSuccess {
		return nil, message.WrapError(message.ErrInvalidStatus, ack.Error)
	}
	return ack, nil
}
