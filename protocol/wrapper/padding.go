package wrapper

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
)

func init() {
	core.WrapperRegister(core.PaddingWrapper, func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
		return NewPaddingWrapper(r, w, opt), nil
	})
}

// PaddingOption 定义padding wrapper的配置选项
type PaddingOption struct {
	Prefix []byte
	Suffix []byte
}

type PaddingWrapper struct {
	reader      *cio.Reader
	writer      *cio.Writer
	parseLength func(reader *cio.Reader) (uint32, error)
	genLength   func(p []byte) []byte
	suffix      []byte
	prefix      []byte
	pending     bytes.Buffer
}

func NewPaddingWrapper(r io.Reader, w io.Writer, opt map[string]string) core.Wrapper {
	var prefix, suffix string
	if p, ok := opt["prefix"]; ok {
		prefix = p
	}
	if s, ok := opt["suffix"]; ok {
		suffix = s
	}

	return &PaddingWrapper{
		reader:      cio.NewReader(r),
		writer:      cio.NewWriter(w),
		prefix:      []byte(prefix),
		suffix:      []byte(suffix),
		genLength:   defaultGenLength,
		parseLength: defaultParserLength,
	}
}

func (w *PaddingWrapper) Name() string {
	return "padding"
}

func (w *PaddingWrapper) Fill() error {
	if err := w.reader.PeekAndRead(w.prefix); err != nil {
		return err
	}
	n, err := w.parseLength(w.reader)
	if err != nil {
		return err
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(w.reader.Reader, payload); err != nil {
		return err
	}
	if err := w.reader.PeekAndRead(w.suffix); err != nil {
		return err
	}
	w.pending.Reset()
	_, _ = w.pending.Write(payload)
	return nil
}

func (w *PaddingWrapper) Read(p []byte) (n int, err error) {
	if w.pending.Len() == 0 {
		if err := w.Fill(); err != nil {
			return 0, err
		}
	}
	return w.pending.Read(p)
}

func (w *PaddingWrapper) Write(p []byte) (n int, err error) {
	var buf bytes.Buffer
	buf.Write(w.prefix)
	buf.Write(w.genLength(p))
	buf.Write(p)
	buf.Write(w.suffix)

	n, err = w.writer.Write(buf.Bytes())
	if err != nil {
		return n, err
	}
	return len(p), nil
}

func (w *PaddingWrapper) Close() error {
	return nil
}

func defaultParserLength(reader *cio.Reader) (uint32, error) {
	p := make([]byte, 4)
	_, err := reader.Read(p)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(p), nil
}

func defaultGenLength(p []byte) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(len(p)))
	return buf
}
