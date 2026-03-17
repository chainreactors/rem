package message

// Message is the interface for all protobuf messages (wire-format compatible).
type Message interface {
	MarshalVT() ([]byte, error)
	UnmarshalVT([]byte) error
	SizeVT() int
}

// --- oneof interface for Redirect.Msg ---

// Marshal serializes a Message to protobuf wire format.
func Marshal(msg Message) ([]byte, error) {
	return msg.MarshalVT()
}

// Unmarshal deserializes protobuf wire format into a Message.
func Unmarshal(data []byte, msg Message) error {
	return msg.UnmarshalVT(data)
}

// Size returns the serialized size of a Message.
func Size(msg Message) int {
	return msg.SizeVT()
}

type isRedirect_Msg interface {
	isRedirect_Msg()
	sizeVT() int
	marshalVT([]byte) []byte
}

// --- Message structs ---

type Login struct {
	ConsoleIP    string   `json:"ConsoleIP,omitempty"`
	ConsolePort  int32    `json:"ConsolePort,omitempty"`
	ConsoleProto string   `json:"ConsoleProto,omitempty"`
	Mod          string   `json:"Mod,omitempty"`
	Token        string   `json:"Token,omitempty"`
	Agent        string   `json:"Agent,omitempty"`
	Interfaces   []string `json:"Interfaces,omitempty"`
	Hostname     string   `json:"Hostname,omitempty"`
	Username     string   `json:"Username,omitempty"`
	Wrapper      string   `json:"Wrapper,omitempty"`
	ChannelRole  string   `json:"ChannelRole,omitempty"`
}

type Control struct {
	Source      string            `json:"Source,omitempty"`
	Destination string            `json:"Destination,omitempty"`
	Mod         string            `json:"Mod,omitempty"`
	Remote      string            `json:"Remote,omitempty"`
	Local       string            `json:"Local,omitempty"`
	Fork        bool              `json:"Fork,omitempty"`
	Options     map[string]string `json:"options,omitempty"`
}

type Ack struct {
	Status int32  `json:"Status,omitempty"`
	Error  string `json:"Error,omitempty"`
	Port   int32  `json:"Port,omitempty"`
}

type Ping struct {
	Ping string `json:"Ping,omitempty"`
}

type Pong struct {
	Pong string `json:"Pong,omitempty"`
}

type ConnStart struct {
	ID          uint64 `json:"ID,omitempty"`
	Destination string `json:"Destination,omitempty"`
	Source      string `json:"Source,omitempty"`
}

type ConnEnd struct {
	ID  uint64 `json:"ID,omitempty"`
	Msg string `json:"Msg,omitempty"`
}

type Packet struct {
	ID    uint64 `json:"ID,omitempty"`
	Index int32  `json:"Index,omitempty"`
	Data  []byte `json:"Data,omitempty"`
}

type Redirect struct {
	Source      string         `json:"Source,omitempty"`
	Destination string         `json:"Destination,omitempty"`
	Route       string         `json:"Route,omitempty"`
	Msg         isRedirect_Msg `json:"-"`
}

type CWND struct {
	Bridge uint64 `json:"Bridge,omitempty"`
	CWND   bool   `json:"CWND,omitempty"`
	Token  int64  `json:"Token,omitempty"`
}

type BridgeOpen struct {
	ID          uint64 `json:"ID,omitempty"`
	Source      string `json:"Source,omitempty"`
	Destination string `json:"Destination,omitempty"`
}

type BridgeClose struct {
	ID uint64 `json:"ID,omitempty"`
}

type Reconfigure struct {
	Options map[string]string `json:"options,omitempty"`
}

// --- oneof wrapper types ---

type Redirect_Start struct{ Start *ConnStart }
type Redirect_Packet struct{ Packet *Packet }
type Redirect_End struct{ End *ConnEnd }
type Redirect_Cwnd struct{ Cwnd *CWND }

func (*Redirect_Start) isRedirect_Msg()  {}
func (*Redirect_Packet) isRedirect_Msg() {}
func (*Redirect_End) isRedirect_Msg()    {}
func (*Redirect_Cwnd) isRedirect_Msg()   {}

// --- Getters for Redirect oneof ---

func (x *Redirect) GetMsg() isRedirect_Msg {
	if x != nil {
		return x.Msg
	}
	return nil
}

func (x *Redirect) GetStart() *ConnStart {
	if v, ok := x.GetMsg().(*Redirect_Start); ok {
		return v.Start
	}
	return nil
}

func (x *Redirect) GetPacket() *Packet {
	if v, ok := x.GetMsg().(*Redirect_Packet); ok {
		return v.Packet
	}
	return nil
}

func (x *Redirect) GetEnd() *ConnEnd {
	if v, ok := x.GetMsg().(*Redirect_End); ok {
		return v.End
	}
	return nil
}

func (x *Redirect) GetCwnd() *CWND {
	if v, ok := x.GetMsg().(*Redirect_Cwnd); ok {
		return v.Cwnd
	}
	return nil
}
