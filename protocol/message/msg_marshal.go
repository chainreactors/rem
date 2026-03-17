package message

// ===== Login =====

func (x *Login) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendString(b, 1, x.ConsoleIP)
	b = appendInt32(b, 2, x.ConsolePort)
	b = appendString(b, 3, x.ConsoleProto)
	b = appendString(b, 4, x.Mod)
	b = appendString(b, 5, x.Token)
	b = appendString(b, 8, x.Agent)
	for _, s := range x.Interfaces {
		b = appendString(b, 9, s)
	}
	b = appendString(b, 10, x.Hostname)
	b = appendString(b, 11, x.Username)
	b = appendString(b, 13, x.Wrapper)
	b = appendString(b, 14, x.ChannelRole)
	return b, nil
}

func (x *Login) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.ConsoleIP = string(v)
			b = b[n:]
		case 2:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.ConsolePort = int32(v)
			b = b[n:]
		case 3:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.ConsoleProto = string(v)
			b = b[n:]
		case 4:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Mod = string(v)
			b = b[n:]
		case 5:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Token = string(v)
			b = b[n:]
		case 8:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Agent = string(v)
			b = b[n:]
		case 9:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Interfaces = append(x.Interfaces, string(v))
			b = b[n:]
		case 10:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Hostname = string(v)
			b = b[n:]
		case 11:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Username = string(v)
			b = b[n:]
		case 13:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Wrapper = string(v)
			b = b[n:]
		case 14:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.ChannelRole = string(v)
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Login) SizeVT() int {
	n := 0
	n += sizeString(1, x.ConsoleIP)
	n += sizeInt32(2, x.ConsolePort)
	n += sizeString(3, x.ConsoleProto)
	n += sizeString(4, x.Mod)
	n += sizeString(5, x.Token)
	n += sizeString(8, x.Agent)
	for _, s := range x.Interfaces {
		n += sizeString(9, s)
	}
	n += sizeString(10, x.Hostname)
	n += sizeString(11, x.Username)
	n += sizeString(13, x.Wrapper)
	n += sizeString(14, x.ChannelRole)
	return n
}

// ===== Control =====

func (x *Control) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendString(b, 2, x.Source)
	b = appendString(b, 3, x.Destination)
	b = appendString(b, 5, x.Mod)
	b = appendString(b, 6, x.Remote)
	b = appendString(b, 7, x.Local)
	b = appendBool(b, 8, x.Fork)
	for k, v := range x.Options {
		entrySize := sizeString(1, k) + sizeString(2, v)
		b = appendTag(b, 12, wireBytes)
		b = appendVarint(b, uint64(entrySize))
		b = appendString(b, 1, k)
		b = appendString(b, 2, v)
	}
	return b, nil
}

func (x *Control) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 2:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Source = string(v)
			b = b[n:]
		case 3:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Destination = string(v)
			b = b[n:]
		case 5:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Mod = string(v)
			b = b[n:]
		case 6:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Remote = string(v)
			b = b[n:]
		case 7:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Local = string(v)
			b = b[n:]
		case 8:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.Fork = v != 0
			b = b[n:]
		case 12:
			entryData, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
			if x.Options == nil {
				x.Options = make(map[string]string)
			}
			var key, val string
			eb := entryData
			for len(eb) > 0 {
				fn, _, en := consumeTag(eb)
				if en < 0 {
					return errInvalidData
				}
				eb = eb[en:]
				v, en := consumeBytes(eb)
				if en < 0 {
					return errInvalidData
				}
				if fn == 1 {
					key = string(v)
				} else if fn == 2 {
					val = string(v)
				}
				eb = eb[en:]
			}
			x.Options[key] = val
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Control) SizeVT() int {
	n := 0
	n += sizeString(2, x.Source)
	n += sizeString(3, x.Destination)
	n += sizeString(5, x.Mod)
	n += sizeString(6, x.Remote)
	n += sizeString(7, x.Local)
	n += sizeBool(8, x.Fork)
	for k, v := range x.Options {
		entrySize := sizeString(1, k) + sizeString(2, v)
		n += sizeTag(12) + sizeVarint(uint64(entrySize)) + entrySize
	}
	return n
}

// ===== Ack =====

func (x *Ack) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendInt32(b, 1, x.Status)
	b = appendString(b, 2, x.Error)
	b = appendInt32(b, 3, x.Port)
	return b, nil
}

func (x *Ack) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.Status = int32(v)
			b = b[n:]
		case 2:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Error = string(v)
			b = b[n:]
		case 3:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.Port = int32(v)
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Ack) SizeVT() int {
	return sizeInt32(1, x.Status) + sizeString(2, x.Error) + sizeInt32(3, x.Port)
}

// ===== Ping =====

func (x *Ping) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendString(b, 1, x.Ping)
	return b, nil
}

func (x *Ping) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		if fieldNum == 1 {
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Ping = string(v)
			b = b[n:]
		} else {
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Ping) SizeVT() int { return sizeString(1, x.Ping) }

// ===== Pong =====

func (x *Pong) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendString(b, 1, x.Pong)
	return b, nil
}

func (x *Pong) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		if fieldNum == 1 {
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Pong = string(v)
			b = b[n:]
		} else {
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Pong) SizeVT() int { return sizeString(1, x.Pong) }

// ===== ConnStart =====

func (x *ConnStart) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendUint64Field(b, 1, x.ID)
	b = appendString(b, 3, x.Destination)
	b = appendString(b, 4, x.Source)
	return b, nil
}

func (x *ConnStart) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.ID = v
			b = b[n:]
		case 3:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Destination = string(v)
			b = b[n:]
		case 4:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Source = string(v)
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *ConnStart) SizeVT() int {
	return sizeUint64Field(1, x.ID) + sizeString(3, x.Destination) + sizeString(4, x.Source)
}

// ===== ConnEnd =====

func (x *ConnEnd) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendUint64Field(b, 1, x.ID)
	b = appendString(b, 2, x.Msg)
	return b, nil
}

func (x *ConnEnd) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.ID = v
			b = b[n:]
		case 2:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Msg = string(v)
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *ConnEnd) SizeVT() int {
	return sizeUint64Field(1, x.ID) + sizeString(2, x.Msg)
}

// ===== Packet =====

func (x *Packet) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendUint64Field(b, 1, x.ID)
	b = appendInt32(b, 2, x.Index)
	b = appendRawBytes(b, 5, x.Data)
	return b, nil
}

func (x *Packet) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.ID = v
			b = b[n:]
		case 2:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.Index = int32(v)
			b = b[n:]
		case 5:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Data = append([]byte(nil), v...)
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Packet) SizeVT() int {
	return sizeUint64Field(1, x.ID) + sizeInt32(2, x.Index) + sizeRawBytes(5, x.Data)
}

// ===== CWND =====

func (x *CWND) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendUint64Field(b, 1, x.Bridge)
	b = appendBool(b, 2, x.CWND)
	b = appendInt64Field(b, 3, x.Token)
	return b, nil
}

func (x *CWND) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.Bridge = v
			b = b[n:]
		case 2:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.CWND = v != 0
			b = b[n:]
		case 3:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.Token = int64(v)
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *CWND) SizeVT() int {
	return sizeUint64Field(1, x.Bridge) + sizeBool(2, x.CWND) + sizeInt64Field(3, x.Token)
}

// ===== BridgeOpen =====

func (x *BridgeOpen) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendUint64Field(b, 1, x.ID)
	b = appendString(b, 2, x.Source)
	b = appendString(b, 3, x.Destination)
	return b, nil
}

func (x *BridgeOpen) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.ID = v
			b = b[n:]
		case 2:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Source = string(v)
			b = b[n:]
		case 3:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Destination = string(v)
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *BridgeOpen) SizeVT() int {
	return sizeUint64Field(1, x.ID) + sizeString(2, x.Source) + sizeString(3, x.Destination)
}

// ===== BridgeClose =====

func (x *BridgeClose) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendUint64Field(b, 1, x.ID)
	return b, nil
}

func (x *BridgeClose) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeVarint(b)
			if n < 0 {
				return errInvalidData
			}
			x.ID = v
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *BridgeClose) SizeVT() int {
	return sizeUint64Field(1, x.ID)
}

// ===== Redirect =====

func (x *Redirect) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	b = appendString(b, 1, x.Source)
	b = appendString(b, 2, x.Destination)
	b = appendString(b, 3, x.Route)
	if x.Msg != nil {
		b = x.Msg.marshalVT(b)
	}
	return b, nil
}

func (x *Redirect) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		switch fieldNum {
		case 1:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Source = string(v)
			b = b[n:]
		case 2:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Destination = string(v)
			b = b[n:]
		case 3:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			x.Route = string(v)
			b = b[n:]
		case 10:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			msg := &ConnStart{}
			if err := msg.UnmarshalVT(v); err != nil {
				return err
			}
			x.Msg = &Redirect_Start{Start: msg}
			b = b[n:]
		case 11:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			msg := &Packet{}
			if err := msg.UnmarshalVT(v); err != nil {
				return err
			}
			x.Msg = &Redirect_Packet{Packet: msg}
			b = b[n:]
		case 12:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			msg := &ConnEnd{}
			if err := msg.UnmarshalVT(v); err != nil {
				return err
			}
			x.Msg = &Redirect_End{End: msg}
			b = b[n:]
		case 13:
			v, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			msg := &CWND{}
			if err := msg.UnmarshalVT(v); err != nil {
				return err
			}
			x.Msg = &Redirect_Cwnd{Cwnd: msg}
			b = b[n:]
		default:
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Redirect) SizeVT() int {
	n := sizeString(1, x.Source) + sizeString(2, x.Destination) + sizeString(3, x.Route)
	if x.Msg != nil {
		n += x.Msg.sizeVT()
	}
	return n
}

// --- oneof marshalVT / sizeVT ---

func (o *Redirect_Start) marshalVT(b []byte) []byte {
	return appendMessageField(b, 10, o.Start)
}
func (o *Redirect_Start) sizeVT() int { return sizeMessageField(10, o.Start) }

func (o *Redirect_Packet) marshalVT(b []byte) []byte {
	return appendMessageField(b, 11, o.Packet)
}
func (o *Redirect_Packet) sizeVT() int { return sizeMessageField(11, o.Packet) }

func (o *Redirect_End) marshalVT(b []byte) []byte {
	return appendMessageField(b, 12, o.End)
}
func (o *Redirect_End) sizeVT() int { return sizeMessageField(12, o.End) }

func (o *Redirect_Cwnd) marshalVT(b []byte) []byte {
	return appendMessageField(b, 13, o.Cwnd)
}
func (o *Redirect_Cwnd) sizeVT() int { return sizeMessageField(13, o.Cwnd) }

// ===== Reconfigure =====

func (x *Reconfigure) MarshalVT() ([]byte, error) {
	b := make([]byte, 0, x.SizeVT())
	for k, v := range x.Options {
		entrySize := sizeString(1, k) + sizeString(2, v)
		b = appendTag(b, 1, wireBytes)
		b = appendVarint(b, uint64(entrySize))
		b = appendString(b, 1, k)
		b = appendString(b, 2, v)
	}
	return b, nil
}

func (x *Reconfigure) UnmarshalVT(data []byte) error {
	b := data
	for len(b) > 0 {
		fieldNum, wt, n := consumeTag(b)
		if n < 0 {
			return errInvalidData
		}
		b = b[n:]
		if fieldNum == 1 {
			entryData, n := consumeBytes(b)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
			if x.Options == nil {
				x.Options = make(map[string]string)
			}
			var key, val string
			eb := entryData
			for len(eb) > 0 {
				fn, _, en := consumeTag(eb)
				if en < 0 {
					return errInvalidData
				}
				eb = eb[en:]
				v, en := consumeBytes(eb)
				if en < 0 {
					return errInvalidData
				}
				if fn == 1 {
					key = string(v)
				} else if fn == 2 {
					val = string(v)
				}
				eb = eb[en:]
			}
			x.Options[key] = val
		} else {
			n := skipField(b, wt)
			if n < 0 {
				return errInvalidData
			}
			b = b[n:]
		}
	}
	return nil
}

func (x *Reconfigure) SizeVT() int {
	n := 0
	for k, v := range x.Options {
		entrySize := sizeString(1, k) + sizeString(2, v)
		n += sizeTag(1) + sizeVarint(uint64(entrySize)) + entrySize
	}
	return n
}
