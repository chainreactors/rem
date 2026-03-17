package servetest

import (
	"bytes"
	"context"
	"io"
	"net"
	"time"
)

type Addr struct {
	network string
	value   string
}

func NewAddr(network, value string) Addr {
	if network == "" {
		network = "tcp"
	}
	return Addr{
		network: network,
		value:   value,
	}
}

func (a Addr) Network() string { return a.network }

func (a Addr) String() string { return a.value }

type ReadWriteCloser struct {
	Reader io.Reader
	Buffer bytes.Buffer

	WriteErr error
	CloseErr error

	CloseCount int
}

func NewReadWriteCloser(data []byte) *ReadWriteCloser {
	var reader io.Reader
	if data != nil {
		reader = bytes.NewReader(data)
	}
	return &ReadWriteCloser{Reader: reader}
}

func NewReadWriteCloserString(data string) *ReadWriteCloser {
	return NewReadWriteCloser([]byte(data))
}

func (rwc *ReadWriteCloser) Read(p []byte) (int, error) {
	if rwc.Reader == nil {
		return 0, io.EOF
	}
	return rwc.Reader.Read(p)
}

func (rwc *ReadWriteCloser) Write(p []byte) (int, error) {
	if rwc.WriteErr != nil {
		return 0, rwc.WriteErr
	}
	return rwc.Buffer.Write(p)
}

func (rwc *ReadWriteCloser) Close() error {
	rwc.CloseCount++
	return rwc.CloseErr
}

func (rwc *ReadWriteCloser) Bytes() []byte {
	return rwc.Buffer.Bytes()
}

func (rwc *ReadWriteCloser) String() string {
	return rwc.Buffer.String()
}

type Conn struct {
	ReadWriteCloser
	Local  net.Addr
	Remote net.Addr
}

func NewConn(data []byte) *Conn {
	return &Conn{
		ReadWriteCloser: *NewReadWriteCloser(data),
		Local:           NewAddr("tcp", "127.0.0.1:1000"),
		Remote:          NewAddr("tcp", "127.0.0.1:2000"),
	}
}

func NewConnString(data string) *Conn {
	return NewConn([]byte(data))
}

func (c *Conn) LocalAddr() net.Addr {
	if c.Local != nil {
		return c.Local
	}
	return NewAddr("tcp", "127.0.0.1:1000")
}

func (c *Conn) RemoteAddr() net.Addr {
	if c.Remote != nil {
		return c.Remote
	}
	return NewAddr("tcp", "127.0.0.1:2000")
}

func (c *Conn) SetDeadline(time.Time) error      { return nil }
func (c *Conn) SetReadDeadline(time.Time) error  { return nil }
func (c *Conn) SetWriteDeadline(time.Time) error { return nil }

type Dialer struct {
	Conn net.Conn
	Err  error

	Calls       int
	LastNetwork string
	LastAddress string
}

func (d *Dialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *Dialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.Calls++
	d.LastNetwork = network
	d.LastAddress = address
	if d.Err != nil {
		return nil, d.Err
	}
	return d.Conn, nil
}

func SOCKS5SuccessReply() []byte {
	return []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
}

func SOCKS5Reply(status byte) []byte {
	return []byte{0x05, status, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
}
