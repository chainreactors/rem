package socks5

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/rem/x/utils"
)

func TestMain(m *testing.M) {
	utils.Log = logs.NewLogger(logs.WarnLevel)
	os.Exit(m.Run())
}

func TestClientConnect_NoAuth(t *testing.T) {
	// Backend target server
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetAddr := target.Addr().(*net.TCPAddr)

	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		io.ReadFull(conn, buf)
		conn.Write([]byte("pong"))
	}()

	// SOCKS5 server (no auth)
	conf := &Config{
		Logger: log.New(os.Stdout, "", log.LstdFlags),
	}
	serv, err := New(conf)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	go serv.Serve(sl)
	time.Sleep(10 * time.Millisecond)

	// Use ClientConnect
	conn, err := net.Dial("tcp", sl.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	addr := targetAddr.String()
	if err := ClientConnect(conn, addr, "", ""); err != nil {
		t.Fatalf("ClientConnect failed: %v", err)
	}

	// Send ping, expect pong
	conn.Write([]byte("ping"))
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("expected pong, got %q", buf)
	}
}

func TestClientConnect_UserPass(t *testing.T) {
	// Backend target server
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetAddr := target.Addr().(*net.TCPAddr)

	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 5)
		io.ReadFull(conn, buf)
		conn.Write([]byte("hello"))
	}()

	// SOCKS5 server with user/pass auth
	creds := StaticCredentials{"user1": "pass1"}
	conf := &Config{
		AuthMethods: []Authenticator{&UserPassAuthenticator{Credentials: creds}},
		Logger:      log.New(os.Stdout, "", log.LstdFlags),
	}
	serv, err := New(conf)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	go serv.Serve(sl)
	time.Sleep(10 * time.Millisecond)

	// Use ClientConnect with credentials
	conn, err := net.Dial("tcp", sl.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	addr := targetAddr.String()
	if err := ClientConnect(conn, addr, "user1", "pass1"); err != nil {
		t.Fatalf("ClientConnect with auth failed: %v", err)
	}

	conn.Write([]byte("world"))
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("expected hello, got %q", buf)
	}
}

func TestClientConnect_BadAuth(t *testing.T) {
	creds := StaticCredentials{"user1": "pass1"}
	conf := &Config{
		AuthMethods: []Authenticator{&UserPassAuthenticator{Credentials: creds}},
		Logger:      log.New(os.Stdout, "", log.LstdFlags),
	}
	serv, err := New(conf)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	go serv.Serve(sl)
	time.Sleep(10 * time.Millisecond)

	conn, err := net.Dial("tcp", sl.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	err = ClientConnect(conn, "127.0.0.1:9999", "wrong", "creds")
	if err == nil {
		t.Fatal("expected auth failure, got nil")
	}
	t.Logf("got expected error: %v", err)
}

func TestClientConnect_FQDN(t *testing.T) {
	// Backend target
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetPort := target.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		io.ReadFull(conn, buf)
		conn.Write(buf)
	}()

	// SOCKS5 server
	conf := &Config{Logger: log.New(os.Stdout, "", log.LstdFlags)}
	serv, err := New(conf)
	if err != nil {
		t.Fatal(err)
	}
	sl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sl.Close()
	go serv.Serve(sl)
	time.Sleep(10 * time.Millisecond)

	conn, err := net.Dial("tcp", sl.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	addr := net.JoinHostPort("localhost", fmt.Sprintf("%d", targetPort))

	if err := ClientConnect(conn, addr, "", ""); err != nil {
		t.Fatalf("ClientConnect FQDN failed: %v", err)
	}

	conn.Write([]byte("echo"))
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "echo" {
		t.Fatalf("expected echo, got %q", buf)
	}
}
