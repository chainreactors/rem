package wrapper

import (
	"crypto/cipher"
	"io"

	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/cryptor"
)

type StreamBlockWrapper struct {
	name         string
	sourceReader io.Reader
	sourceWriter io.Writer
	reader       io.Reader
	writer       io.Writer
	encStream    cipher.Stream
	decStream    cipher.Stream
	stream       cryptor.Stream
	key          []byte
	iv           []byte
}

func newStreamBlockWrapper(name, fallbackAlgo string, defaultKey []byte, r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
	algo := resolveAlgo(opt, fallbackAlgo)

	key := make([]byte, len(defaultKey))
	copy(key, defaultKey)
	if value, ok := opt["key"]; ok {
		key = []byte(value)
	}

	iv, err := resolveIVByBlock(algo, key, opt, key)
	if err != nil {
		return nil, err
	}

	stream, err := cryptor.StreamByName(algo)
	if err != nil {
		return nil, err
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	ivCopy := make([]byte, len(iv))
	copy(ivCopy, iv)

	wrapper := &StreamBlockWrapper{
		name:         name,
		sourceReader: r,
		sourceWriter: w,
		stream:       stream,
		key:          keyCopy,
		iv:           ivCopy,
	}

	if err := wrapper.reset(); err != nil {
		return nil, err
	}

	return wrapper, nil
}

func NewStreamBlockWrapper(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
	return newStreamBlockWrapper(core.CryptorWrapper, "aes", make([]byte, 32), r, w, opt)
}

func (wrapper *StreamBlockWrapper) Name() string {
	return wrapper.name
}

func (wrapper *StreamBlockWrapper) Read(p []byte) (n int, err error) {
	return wrapper.reader.Read(p)
}

func (wrapper *StreamBlockWrapper) Write(p []byte) (n int, err error) {
	return wrapper.writer.Write(p)
}

func (wrapper *StreamBlockWrapper) Close() error {
	return wrapper.reset()
}

func (wrapper *StreamBlockWrapper) reset() error {
	encStream, err := wrapper.stream.Encryptor(wrapper.key, wrapper.iv)
	if err != nil {
		return err
	}

	decStream, err := wrapper.stream.Decryptor(wrapper.key, wrapper.iv)
	if err != nil {
		return err
	}

	wrapper.encStream = encStream
	wrapper.decStream = decStream
	wrapper.reader = &cipher.StreamReader{S: wrapper.decStream, R: wrapper.sourceReader}
	wrapper.writer = &cipher.StreamWriter{S: wrapper.encStream, W: wrapper.sourceWriter}
	return nil
}
