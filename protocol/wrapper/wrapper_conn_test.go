package wrapper

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/chainreactors/rem/protocol/cio"
	"github.com/chainreactors/rem/protocol/core"
	"github.com/chainreactors/rem/x/cryptor"
)

type wrapperConstructor func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error)

func exchangeOverConn(t *testing.T, writer core.Wrapper, reader core.Wrapper, payload []byte) {
	t.Helper()

	writeErrCh := make(chan error, 1)
	go func() {
		_, err := writer.Write(payload)
		writeErrCh <- err
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, got); err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if err := <-writeErrCh; err != nil {
		t.Fatalf("write failed: %v", err)
	}

	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch:\n  got:  %q\n  want: %q", got, payload)
	}
}

func runWrapperConnCase(t *testing.T, constructor wrapperConstructor, opt map[string]string) {
	t.Helper()

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverWrapper, err := constructor(serverConn, serverConn, opt)
	if err != nil {
		t.Fatalf("server constructor failed: %v", err)
	}
	clientWrapper, err := constructor(clientConn, clientConn, opt)
	if err != nil {
		t.Fatalf("client constructor failed: %v", err)
	}

	forwardPayload := bytes.Repeat([]byte("forward-net-conn-payload-"), 128)
	reversePayload := bytes.Repeat([]byte("reverse-net-conn-payload-"), 96)
	resetPayload := bytes.Repeat([]byte("reset-net-conn-payload-"), 64)

	exchangeOverConn(t, clientWrapper, serverWrapper, forwardPayload)
	exchangeOverConn(t, serverWrapper, clientWrapper, reversePayload)

	if err := serverWrapper.Close(); err != nil {
		t.Fatalf("server wrapper reset failed: %v", err)
	}
	if err := clientWrapper.Close(); err != nil {
		t.Fatalf("client wrapper reset failed: %v", err)
	}

	exchangeOverConn(t, clientWrapper, serverWrapper, resetPayload)
}

func TestWrapperNetConnDefaultAlgorithms(t *testing.T) {
	t.Parallel()

	aesOption := map[string]string{
		"algo": "aes",
		"key":  "abcdefghijklmnopqrstuvwxyz012345",
		"iv":   "abcdefghijklmnop",
	}
	xorOption := map[string]string{
		"algo": "xor",
		"key":  "xorkey12345678901234567890123456",
		"iv":   "xorivxorivxorivx",
	}

	t.Run("aes-wrapper-with-aes", func(t *testing.T) {
		runWrapperConnCase(t, NewCryptorWrapper, aesOption)
	})
	t.Run("xor-wrapper-with-xor", func(t *testing.T) {
		runWrapperConnCase(t, NewCryptorWrapper, xorOption)
	})
	t.Run("aes-wrapper-with-xor", func(t *testing.T) {
		runWrapperConnCase(t, NewCryptorWrapper, xorOption)
	})
	t.Run("xor-wrapper-with-aes", func(t *testing.T) {
		runWrapperConnCase(t, NewCryptorWrapper, aesOption)
	})
}

func TestWrapperNetConnTaggedAlgorithms(t *testing.T) {
	t.Parallel()

	taggedOptions := map[string]map[string]string{
		"sm4":       {"algo": "sm4", "key": "1234567890abcdef", "iv": "1234567890abcdef"},
		"twofish":   {"algo": "twofish", "key": "12345678901234567890123456789012", "iv": "1234567890abcdef"},
		"tripledes": {"algo": "tripledes", "key": "123456789012345678901234", "iv": "12345678"},
		"cast5":     {"algo": "cast5", "key": "1234567890abcdef", "iv": "12345678"},
		"blowfish":  {"algo": "blowfish", "key": "1234567890abcdef", "iv": "12345678"},
		"tea":       {"algo": "tea", "key": "1234567890abcdef", "iv": "12345678"},
		"xtea":      {"algo": "xtea", "key": "1234567890abcdef", "iv": "12345678"},
	}

	for _, name := range cryptor.BlockNames() {
		if name == "aes" || name == "xor" {
			continue
		}

		option, ok := taggedOptions[name]
		if !ok {
			continue
		}

		currentName := name
		currentOption := option
		t.Run(currentName, func(t *testing.T) {
			runWrapperConnCase(t, NewCryptorWrapper, currentOption)
		})
	}
}

func wrapSingleWrapperConn(t *testing.T, constructor wrapperConstructor, raw net.Conn, opt map[string]string) net.Conn {
	t.Helper()

	wrap, err := constructor(raw, raw, opt)
	if err != nil {
		t.Fatalf("constructor failed: %v", err)
	}
	return cio.WrapConnWithUnderlyingClose(raw, wrap)
}

func TestWrappedSingleWrapperCloseClosesUnderlyingConn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		constructor wrapperConstructor
		opt         map[string]string
	}{
		{
			name:        "cryptor-aes",
			constructor: NewCryptorWrapper,
			opt: map[string]string{
				"algo": "aes",
				"key":  "abcdefghijklmnopqrstuvwxyz012345",
				"iv":   "abcdefghijklmnop",
			},
		},
		{
			name: "padding",
			constructor: func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
				return NewPaddingWrapper(r, w, opt), nil
			},
			opt: map[string]string{
				"prefix": "begin",
				"suffix": "end",
			},
		},
		{
			name: "snappy",
			constructor: func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
				return NewSnappyWrapper(r, w, opt), nil
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			serverRaw, clientRaw := net.Pipe()
			defer clientRaw.Close()

			conn := wrapSingleWrapperConn(t, tc.constructor, serverRaw, tc.opt)

			readDone := make(chan error, 1)
			go func() {
				buf := make([]byte, 1)
				_, err := clientRaw.Read(buf)
				readDone <- err
			}()

			if err := conn.Close(); err != nil {
				t.Fatalf("close failed: %v", err)
			}

			select {
			case err := <-readDone:
				if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
					t.Fatalf("peer read error = %v, want EOF/closed", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("peer did not observe close")
			}
		})
	}
}

func TestWrappedSingleWrapperReadDeadline(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		constructor wrapperConstructor
		opt         map[string]string
	}{
		{
			name:        "cryptor-aes",
			constructor: NewCryptorWrapper,
			opt: map[string]string{
				"algo": "aes",
				"key":  "abcdefghijklmnopqrstuvwxyz012345",
				"iv":   "abcdefghijklmnop",
			},
		},
		{
			name: "padding",
			constructor: func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
				return NewPaddingWrapper(r, w, opt), nil
			},
			opt: map[string]string{
				"prefix": "begin",
				"suffix": "end",
			},
		},
		{
			name: "snappy",
			constructor: func(r io.Reader, w io.Writer, opt map[string]string) (core.Wrapper, error) {
				return NewSnappyWrapper(r, w, opt), nil
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			serverRaw, clientRaw := net.Pipe()
			defer clientRaw.Close()

			conn := wrapSingleWrapperConn(t, tc.constructor, serverRaw, tc.opt)
			defer conn.Close()

			if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
				t.Fatalf("set read deadline failed: %v", err)
			}

			_, err := conn.Read(make([]byte, 1))
			if err == nil {
				t.Fatal("expected read deadline error")
			}
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("expected timeout net.Error, got %T: %v", err, err)
			}
		})
	}
}
