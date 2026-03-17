package socks5

import (
	"bytes"
	"io"
	"log"
	"net"
	"os"
	"testing"
	"time"
)

func TestSOCKS5_Connect(t *testing.T) {
	// Create a local listener (target)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4)
		if _, err := io.ReadAtLeast(conn, buf, 4); err != nil {
			t.Errorf("err: %v", err)
			return
		}

		if !bytes.Equal(buf, []byte("ping")) {
			t.Errorf("bad: %v", buf)
			return
		}
		conn.Write([]byte("pong"))
	}()
	lAddr := l.Addr().(*net.TCPAddr)

	// Create a socks server with user/pass auth
	creds := StaticCredentials{
		"foo": "bar",
	}
	cator := UserPassAuthenticator{Credentials: creds}
	conf := &Config{
		AuthMethods: []Authenticator{cator},
		Logger:      log.New(os.Stdout, "", log.LstdFlags),
	}
	serv, err := New(conf)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Start listening on dynamic port
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer sl.Close()
	go serv.Serve(sl)
	time.Sleep(10 * time.Millisecond)

	// Connect to SOCKS5 server
	conn, err := net.Dial("tcp", sl.Addr().String())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	defer conn.Close()

	// Use ClientConnect for proper step-by-step handshake
	if err := ClientConnect(conn, lAddr.String(), "foo", "bar"); err != nil {
		t.Fatalf("ClientConnect: %v", err)
	}

	// Send ping after handshake completes
	conn.Write([]byte("ping"))

	// Read pong
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	out := make([]byte, 4)
	if _, err := io.ReadAtLeast(conn, out, 4); err != nil {
		t.Fatalf("err: %v", err)
	}

	if !bytes.Equal(out, []byte("pong")) {
		t.Fatalf("expected pong, got %v", out)
	}
}
