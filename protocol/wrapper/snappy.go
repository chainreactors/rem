package wrapper

import (
	"github.com/chainreactors/rem/protocol/core"
	"github.com/golang/snappy"
	"io"
)

type SnappyWrapper struct {
	reader io.Reader
	writer *snappy.Writer
}

func NewSnappyWrapper(r io.Reader, w io.Writer, opt map[string]string) core.Wrapper {
	var reader io.Reader
	if r != nil {
		reader = snappy.NewReader(r)
	}
	var writer *snappy.Writer
	if w != nil {
		writer = snappy.NewBufferedWriter(w)
	}
	return &SnappyWrapper{
		reader: reader,
		writer: writer,
	}
}

func (w *SnappyWrapper) Name() string {
	return "snappy"
}

func (w *SnappyWrapper) Read(p []byte) (n int, err error) {
	if w.reader == nil {
		return 0, io.ErrClosedPipe
	}
	return w.reader.Read(p) // 从解压流读取数据
}

func (w *SnappyWrapper) Write(p []byte) (n int, err error) {
	if w.writer == nil {
		return 0, io.ErrClosedPipe
	}
	n, err = w.writer.Write(p)
	if err != nil {
		return n, err
	}
	if err := w.writer.Flush(); err != nil {
		return n, err
	}
	return n, nil
}

func (w *SnappyWrapper) Close() error {
	if w.writer != nil {
		return w.writer.Close()
	}
	return nil
}
