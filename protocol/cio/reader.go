package cio

import (
	"bufio"
	"bytes"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/pkg/errors"
	"io"
	"sync"
)

var (
	ErrorInvalidPadding = errors.New("invalid padding")
	ErrorInvalidLength  = errors.New("invalid length")
)

type Reader struct {
	Buffer    *Buffer
	Reader    *bufio.Reader
	fillMutex sync.Mutex // fill 锁
}

func NewReader(conn io.Reader) *Reader {
	return &Reader{
		Buffer: NewBuffer(core.MaxBufferSize),
		Reader: bufio.NewReader(conn),
	}
}

func (r *Reader) FillN(n int64) error {
	r.fillMutex.Lock()
	defer r.fillMutex.Unlock()
	recv, err := io.CopyN(r.Buffer, r.Reader, n)
	if err != nil {
		return err
	}
	if recv != n {
		return ErrorInvalidLength
	}
	return nil
}

func (r *Reader) PeekAndRead(bs []byte) error {
	if len(bs) == 0 {
		return nil
	}
	prefix, err := r.Peek(len(bs))
	if err != nil {
		return err
	}
	if !bytes.Equal(prefix, bs) {
		return ErrorInvalidPadding
	} else {
		_, err = r.Reader.Read(make([]byte, len(bs)))
		return err
	}
}

// Read 从缓冲区读取数据。
func (r *Reader) Read(p []byte) (int, error) {
	if r.Buffer.Size() > 0 {
		return r.Buffer.Read(p)
	}
	r.fillMutex.Lock()
	defer r.fillMutex.Unlock()
	return r.Reader.Read(p)
}

// Peek 查看缓冲区中的数据而不移动读取指针。
func (r *Reader) Peek(n int) ([]byte, error) {
	return r.Reader.Peek(n)
}
