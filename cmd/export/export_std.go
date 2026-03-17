//go:build !tinygo

package main

/*
#include <stdlib.h>
#include <sys/types.h>
*/
import "C"
import (
	"context"
	"io"
	"math/rand"
	"net"
	"net/url"
	"time"
	"unsafe"

	"github.com/chainreactors/logs"
	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/runner"
	"github.com/chainreactors/rem/x/utils"
	"github.com/kballard/go-shellquote"
)

func init() {
	utils.Log = logs.NewLogger(100)
}

//export InitDialer
func InitDialer() C.int {
	proxyclient.InitBuiltinSchemes()
	return 0
}

//export RemDial
func RemDial(cmdline *C.char) (*C.char, C.int) {
	var option runner.Options
	args, err := shellquote.Split(C.GoString(cmdline))
	if err != nil {
		return nil, ErrCmdParseFailed
	}
	err = option.ParseArgs(args)
	if err != nil {
		return nil, ErrArgsParseFailed
	}

	if option.Debug {
		utils.Log = logs.NewLogger(logs.DebugLevel)
	}
	if option.Detail {
		utils.Log.SetLevel(utils.IOLog)
	} else if option.Quiet {
		utils.Log.SetLevel(100)
	}

	r, err := option.Prepare()
	if err != nil {
		return nil, ErrPrepareFailed
	}
	if len(r.ConsoleURLs) == 0 {
		return nil, ErrNoConsoleURL
	}

	conURL := r.ConsoleURLs[0]

	console, err := runner.NewConsole(r, r.NewURLs(conURL))
	if err != nil {
		return nil, ErrCreateConsole
	}

	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		return nil, ErrDialFailed
	}

	go func() {
		err := a.Handler()
		if err != nil {
			utils.Log.Error(err)
		}
		agent.Agents.Map.Delete(a.ID)
	}()

	for {
		if a.Init {
			break
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return C.CString(a.ID), 0
}

//export MemoryDial
func MemoryDial(memhandle *C.char, dst *C.char) (C.int, C.int) {
	dstStr := C.GoString(dst)
	memhandleStr := C.GoString(memhandle)

	memURL := &url.URL{
		Scheme: "memory",
		Host:   memhandleStr,
	}
	memClient, err := proxyclient.NewClient(memURL)
	if err != nil {
		return 0, ErrCreateConsole
	}

	conn, err := memClient(context.Background(), "tcp", dstStr)
	if err != nil {
		return 0, ErrDialFailed
	}

	connHandle := rand.Intn(0x7FFFFFFF)
	conns.Store(connHandle, conn)
	return C.int(connHandle), 0
}

//export MemoryRead
func MemoryRead(chandle C.int, buf unsafe.Pointer, size C.int) (C.int, C.int) {
	handleInt := int(chandle)

	conn, ok := conns.Load(handleInt)
	if !ok {
		return 0, ErrArgsParseFailed
	}

	buffer := make([]byte, int(size))
	n, err := conn.(net.Conn).Read(buffer)
	if err != nil && err != io.EOF {
		return 0, ErrDialFailed
	}

	if n > 0 {
		cBuf := (*[1 << 30]byte)(buf)
		copy(cBuf[:n], buffer[:n])
	}

	return C.int(n), 0
}

//export MemoryWrite
func MemoryWrite(chandle C.int, buf unsafe.Pointer, size C.int) (C.int, C.int) {
	handleInt := int(chandle)

	conn, ok := conns.Load(handleInt)
	if !ok {
		return 0, ErrArgsParseFailed
	}

	buffer := make([]byte, int(size))
	cBuf := (*[1 << 30]byte)(buf)
	copy(buffer, cBuf[:size])

	n, err := conn.(net.Conn).Write(buffer)
	if err != nil {
		return 0, ErrDialFailed
	}

	return C.int(n), 0
}

//export MemoryClose
func MemoryClose(chandle C.int) C.int {
	handleInt := int(chandle)

	conn, ok := conns.Load(handleInt)
	if !ok {
		return ErrArgsParseFailed
	}

	err := conn.(net.Conn).Close()
	if err != nil {
		return ErrDialFailed
	}

	conns.Delete(handleInt)
	return 0
}

//export CleanupAgent
func CleanupAgent() {
	agent.Agents.Map.Range(func(key, value interface{}) bool {
		if a, ok := value.(*agent.Agent); ok {
			a.Close(nil)
		}
		return true
	})
}
