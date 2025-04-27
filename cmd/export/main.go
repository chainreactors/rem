package main

/*
#include <stdlib.h>
#include <sys/types.h>
*/
import "C"
import (
	"github.com/chainreactors/logs"
	"io"
	"net"
	"net/url"
	"sync"
	"time"
	"unsafe"

	"github.com/chainreactors/proxyclient"
	"github.com/chainreactors/rem/agent"
	"github.com/chainreactors/rem/runner"
	"github.com/chainreactors/rem/x/utils"
	"github.com/kballard/go-shellquote"
)

var (
	conns sync.Map
)

func init() {
	utils.Log = logs.NewLogger(100)
}

func main() {
}

//export RemDial
func RemDial(cmdline *C.char) (*C.char, C.int) {
	var option runner.Options
	args, err := shellquote.Split(C.GoString(cmdline))
	if err != nil {
		return nil, 1 // 错误ID 1: 命令行解析错误
	}
	err = option.ParseArgs(args)
	if err != nil {
		return nil, 2 // 错误ID 2: 参数解析错误
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
		utils.Log.Debugf("RemDial error: failed to prepare options: %v", err)
		return nil, 3 // 错误ID 3: 准备失败
	}
	if len(r.ConsoleURLs) == 0 {
		utils.Log.Debug("RemDial error: no console URL provided")
		return nil, 4 // 错误ID 4: 无控制台URL
	}

	conURL := r.ConsoleURLs[0]

	console, err := runner.NewConsole(r, r.NewURLs(conURL))
	if err != nil {
		utils.Log.Debugf("RemDial error: failed to create console: %v", err)
		return nil, 5 // 错误ID 5: 创建控制台失败
	}

	a, err := console.Dial(console.ConsoleURL)
	if err != nil {
		utils.Log.Debugf("RemDial error: failed to dial console: %v", err)
		return nil, 6 // 错误ID 6: 连接失败
	}

	// 启动一个新的goroutine来处理agent
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

	return C.CString(a.ID), 0 // 成功，返回agent ID
}

//export MemoryDial
func MemoryDial(memhandle *C.char, dst *C.char) (C.int, C.int) {
	memURL := &url.URL{
		Scheme: "memory",
		Host:   C.GoString(memhandle),
	}
	memClient, err := proxyclient.NewClient(memURL)
	if err != nil {
		utils.Log.Debugf("MemoryDial error: failed to create memory client: %v", err)
		return 0, 1 // 错误ID 1: 创建客户端失败
	}

	conn, err := memClient.Dial("tcp", C.GoString(dst))
	if err != nil {
		utils.Log.Debugf("MemoryDial error: failed to dial destination: %v", err)
		return 0, 2 // 错误ID 2: 连接失败
	}

	connHandle := int(utils.RandomString(1)[0])
	conns.Store(connHandle, conn)
	return C.int(connHandle), 0
}

//export MemoryRead
func MemoryRead(chandle C.int, buf unsafe.Pointer, size C.int) (C.int, C.int) {
	conn, ok := conns.Load(int(chandle))
	if !ok {
		utils.Log.Debugf("MemoryRead error: invalid handle: %d", chandle)
		return 0, 1 // 错误ID 1: 无效的连接句柄
	}

	buffer := make([]byte, int(size))
	n, err := conn.(net.Conn).Read(buffer)
	if err != nil && err != io.EOF {
		utils.Log.Debugf("MemoryRead error: failed to read: %v", err)
		return 0, 2 // 错误ID 2: 读取错误
	}

	if n > 0 {
		cBuf := (*[1 << 30]byte)(buf)
		copy(cBuf[:n], buffer[:n])
	}

	return C.int(n), 0
}

//export MemoryWrite
func MemoryWrite(chandle C.int, buf unsafe.Pointer, size C.int) (C.int, C.int) {
	conn, ok := conns.Load(int(chandle))
	if !ok {
		utils.Log.Debugf("MemoryWrite error: invalid handle: %d", chandle)
		return 0, 1 // 错误ID 1: 无效的连接句柄
	}

	buffer := make([]byte, int(size))
	cBuf := (*[1 << 30]byte)(buf)
	copy(buffer, cBuf[:size])

	n, err := conn.(net.Conn).Write(buffer)
	if err != nil {
		utils.Log.Debugf("MemoryWrite error: failed to write: %v", err)
		return 0, 2 // 错误ID 2: 写入错误
	}

	return C.int(n), 0
}

//export MemoryClose
func MemoryClose(chandle C.int) C.int {
	conn, ok := conns.Load(int(chandle))
	if !ok {
		utils.Log.Debugf("MemoryClose error: invalid handle: %d", chandle)
		return 1 // 错误ID 1: 无效的连接句柄
	}

	err := conn.(net.Conn).Close()
	if err != nil {
		utils.Log.Debugf("MemoryClose error: failed to close connection: %v", err)
		return 2 // 错误ID 2: 关闭错误
	}

	conns.Delete(int(chandle))
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
