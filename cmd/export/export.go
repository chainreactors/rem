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
	"sync"
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

func initDialerGo() int {
	proxyclient.InitBuiltinSchemes()
	return 0
}

//export InitDialer
func InitDialer() C.int {
	return C.int(initDialerGo())
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
			utils.Log.Errorf("[rem] handler exited: %v", err)
		}
		a.Close(err)
		agent.Agents.Map.Delete(a.ID)
		// Do NOT reconnect here — the caller (malefic Rust) manages
		// reconnection by re-calling RemDial. Having two reconnection
		// loops (Go + Rust) causes competing agents that kill each
		// other's memory listeners via CreateChannel race.
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

// ========================================================================
// BufferedConn — goroutine-buffered net.Conn for non-blocking TryRead.
//
// A background goroutine continuously reads from the underlying net.Conn
// into an internal buffer.  TryRead / TryWrite are pure memory operations
// that never touch deadlines or block on I/O — the FFI caller gets
// instant results (data or WouldBlock).
//
// This avoids multi-layer timeout conflicts:
//   - No SetReadDeadline / SetWriteDeadline manipulation
//   - The ONLY timeout is in the Rust session layer (read_exact_with_idle_timeout)
// ========================================================================

type BufferedConn struct {
	conn net.Conn

	// Read side: goroutine fills readBuf, TryRead drains it.
	readMu  sync.Mutex
	readBuf []byte
	readErr error // sticky: once set, all future TryRead return it after buf drain

	// Write: passed through to conn directly (Write is typically fast).
	// If non-blocking write is needed, add writeBuf + goroutine later.
}

func NewBufferedConn(conn net.Conn) *BufferedConn {
	bc := &BufferedConn{conn: conn}
	go bc.readLoop()
	return bc
}

func (bc *BufferedConn) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, err := bc.conn.Read(buf)

		bc.readMu.Lock()
		if n > 0 {
			bc.readBuf = append(bc.readBuf, buf[:n]...)
		}
		if err != nil {
			bc.readErr = err
		}
		bc.readMu.Unlock()

		if err != nil {
			return
		}
	}
}

// TryRead copies available data from the buffer.
// Returns (n, 0) if data was available, (0, ErrWouldBlock) if empty,
// or (0, ErrDialFailed) on connection error (after buffer is drained).
func (bc *BufferedConn) TryRead(dst []byte) (int, int) {
	bc.readMu.Lock()
	defer bc.readMu.Unlock()

	if len(bc.readBuf) > 0 {
		n := copy(dst, bc.readBuf)
		bc.readBuf = bc.readBuf[n:]
		return n, 0
	}

	// Buffer empty — check if the connection has an error.
	if bc.readErr != nil {
		if bc.readErr == io.EOF {
			return 0, 0 // EOF
		}
		return 0, ErrDialFailed
	}

	return 0, ErrWouldBlock
}

// Write passes through to the underlying conn (blocking).
func (bc *BufferedConn) Write(src []byte) (int, int) {
	n, err := bc.conn.Write(src)
	if err != nil {
		return 0, ErrDialFailed
	}
	return n, 0
}

// Close closes the underlying conn.  The read goroutine will see the
// error from Read and exit.
func (bc *BufferedConn) Close() int {
	err := bc.conn.Close()
	if err != nil {
		return ErrDialFailed
	}
	return 0
}

// BlockingRead reads from the underlying conn directly (for backward compat).
func (bc *BufferedConn) BlockingRead(dst []byte) (int, int) {
	// First drain any buffered data.
	bc.readMu.Lock()
	if len(bc.readBuf) > 0 {
		n := copy(dst, bc.readBuf)
		bc.readBuf = bc.readBuf[n:]
		bc.readMu.Unlock()
		return n, 0
	}
	readErr := bc.readErr
	bc.readMu.Unlock()

	if readErr != nil {
		if readErr == io.EOF {
			return 0, 0
		}
		return 0, ErrDialFailed
	}

	// Wait for the goroutine to produce data.
	for {
		time.Sleep(1 * time.Millisecond)
		bc.readMu.Lock()
		if len(bc.readBuf) > 0 {
			n := copy(dst, bc.readBuf)
			bc.readBuf = bc.readBuf[n:]
			bc.readMu.Unlock()
			return n, 0
		}
		if bc.readErr != nil {
			err := bc.readErr
			bc.readMu.Unlock()
			if err == io.EOF {
				return 0, 0
			}
			return 0, ErrDialFailed
		}
		bc.readMu.Unlock()
	}
}

// ========================================================================
// FFI exports
// ========================================================================

func memoryDialGo(memhandle string, dst string) (int, int) {
	memURL := &url.URL{
		Scheme: "memory",
		Host:   memhandle,
	}
	memClient, err := proxyclient.NewClient(memURL)
	if err != nil {
		return 0, ErrCreateConsole
	}

	conn, err := memClient(context.Background(), "tcp", dst)
	if err != nil {
		return 0, ErrDialFailed
	}

	connHandle := rand.Intn(0x7FFFFFFF)
	// Wrap in BufferedConn for non-blocking TryRead support.
	conns.Store(connHandle, NewBufferedConn(conn))
	return connHandle, 0
}

//export MemoryDial
func MemoryDial(memhandle *C.char, dst *C.char) (C.int, C.int) {
	connHandle, errCode := memoryDialGo(C.GoString(memhandle), C.GoString(dst))
	return C.int(connHandle), C.int(errCode)
}

func getBufferedConn(handleInt int) (*BufferedConn, int) {
	v, ok := conns.Load(handleInt)
	if !ok {
		return nil, ErrArgsParseFailed
	}
	return v.(*BufferedConn), 0
}

//export MemoryRead
func MemoryRead(chandle C.int, buf unsafe.Pointer, size C.int) (C.int, C.int) {
	bc, errCode := getBufferedConn(int(chandle))
	if errCode != 0 {
		return 0, C.int(errCode)
	}
	cBuf := (*[1 << 30]byte)(buf)
	n, ec := bc.BlockingRead(cBuf[:int(size)])
	return C.int(n), C.int(ec)
}

// MemoryTryRead is a non-blocking read: returns data from the internal
// buffer or ErrWouldBlock if no data is available.  No deadlines, no
// timeout parameters — pure memory operation.
//
//export MemoryTryRead
func MemoryTryRead(chandle C.int, buf unsafe.Pointer, size C.int) (C.int, C.int) {
	bc, errCode := getBufferedConn(int(chandle))
	if errCode != 0 {
		return 0, C.int(errCode)
	}
	cBuf := (*[1 << 30]byte)(buf)
	n, ec := bc.TryRead(cBuf[:int(size)])
	return C.int(n), C.int(ec)
}

// MemoryReadTimeout is kept for backward compatibility with the previous
// Rust transport version.  Prefer MemoryTryRead for new code.
//
//export MemoryReadTimeout
func MemoryReadTimeout(chandle C.int, buf unsafe.Pointer, size C.int, timeoutMs C.int) (C.int, C.int) {
	if timeoutMs == 0 {
		return MemoryTryRead(chandle, buf, size)
	}
	// Positive timeout: poll TryRead until data or deadline.
	bc, errCode := getBufferedConn(int(chandle))
	if errCode != 0 {
		return 0, C.int(errCode)
	}
	cBuf := (*[1 << 30]byte)(buf)
	deadline := time.Now().Add(time.Duration(int(timeoutMs)) * time.Millisecond)
	for {
		n, ec := bc.TryRead(cBuf[:int(size)])
		if ec != ErrWouldBlock || n > 0 {
			return C.int(n), C.int(ec)
		}
		if time.Now().After(deadline) {
			return 0, C.int(ErrWouldBlock)
		}
		time.Sleep(1 * time.Millisecond)
	}
}

//export MemoryWrite
func MemoryWrite(chandle C.int, buf unsafe.Pointer, size C.int) (C.int, C.int) {
	bc, errCode := getBufferedConn(int(chandle))
	if errCode != 0 {
		return 0, C.int(errCode)
	}
	cBuf := (*[1 << 30]byte)(buf)
	n, ec := bc.Write(cBuf[:int(size)])
	return C.int(n), C.int(ec)
}

func memoryCloseGo(handleInt int) int {
	bc, errCode := getBufferedConn(handleInt)
	if errCode != 0 {
		return errCode
	}
	ec := bc.Close()
	conns.Delete(handleInt)
	return ec
}

//export MemoryClose
func MemoryClose(chandle C.int) C.int {
	return C.int(memoryCloseGo(int(chandle)))
}

//export CleanupAgent
func CleanupAgent() {
	agent.Agents.Map.Range(func(key, value interface{}) bool {
		if a, ok := value.(*agent.Agent); ok {
			// Close removes the registry entry synchronously (compare-and-delete
			// by pointer), so the next RemDial can reuse the same alias
			// immediately after cleanup returns.
			a.Close(nil)
			agent.Agents.CompareAndDelete(key, a)
		}
		return true
	})
}
