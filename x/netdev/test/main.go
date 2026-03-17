//go:build tinygo

package main

import (
	"fmt"
	"net"
	"time"

	"github.com/chainreactors/rem/x/netdev"
	"github.com/chainreactors/rem/x/netdev/native"
)

func main() {
	fmt.Println("[1] Initializing native netdev...")
	netdev.UseNetdev(native.New())
	fmt.Println("[1] OK")

	fmt.Println("[2] Starting TCP listener on 127.0.0.1:19999...")
	ln, err := net.Listen("tcp", "127.0.0.1:19999")
	if err != nil {
		fmt.Println("[2] FAIL Listen:", err)
		return
	}
	defer ln.Close()
	fmt.Println("[2] OK")

	done := make(chan struct{})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			fmt.Println("[3] FAIL Accept:", err)
			return
		}
		defer conn.Close()
		fmt.Println("[3] Server accepted connection")

		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			fmt.Println("[3] FAIL Read:", err)
			return
		}
		fmt.Printf("[3] Server received: %s\n", string(buf[:n]))

		_, err = conn.Write([]byte("pong"))
		if err != nil {
			fmt.Println("[3] FAIL Write:", err)
			return
		}
		fmt.Println("[3] Server sent: pong")
	}()

	time.Sleep(100 * time.Millisecond)

	fmt.Println("[4] Client dialing 127.0.0.1:19999...")
	conn, err := net.Dial("tcp", "127.0.0.1:19999")
	if err != nil {
		fmt.Println("[4] FAIL Dial:", err)
		return
	}
	defer conn.Close()
	fmt.Println("[4] Client connected")

	_, err = conn.Write([]byte("ping"))
	if err != nil {
		fmt.Println("[4] FAIL Write:", err)
		return
	}
	fmt.Println("[4] Client sent: ping")

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		fmt.Println("[4] FAIL Read:", err)
		return
	}
	fmt.Printf("[4] Client received: %s\n", string(buf[:n]))

	<-done
	fmt.Println("\n[OK] net.Dial/net.Listen full roundtrip via native netdev!")
}
