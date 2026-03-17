package wrapper

import (
	"bytes"
	"io"
	"net"
	"testing"

	"github.com/chainreactors/rem/protocol/core"
)

func benchAESOptions() core.WrapperOptions {
	return core.WrapperOptions{
		&core.WrapperOption{
			Name: core.CryptorWrapper,
			Options: map[string]string{
				"algo": "aes",
				"key":  "abcdefghijklmnopqrstuvwxyz012345",
				"iv":   "abcdefghijklmnop",
			},
		},
	}
}

func BenchmarkNewChainWrapper(b *testing.B) {
	opts := benchAESOptions()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c1, c2 := net.Pipe()
		w1, err := NewChainWrapper(c1, opts)
		if err != nil {
			b.Fatal(err)
		}
		w2, err := NewChainWrapper(c2, opts)
		if err != nil {
			b.Fatal(err)
		}
		_ = w1.Close()
		_ = w2.Close()
	}
}

func BenchmarkWrapperChainRoundTrip(b *testing.B) {
	for _, size := range []int{128, 1024, 4096} {
		b.Run("size="+itoa(size), func(b *testing.B) {
			opts := benchAESOptions()
			serverConn, clientConn := net.Pipe()
			defer serverConn.Close()
			defer clientConn.Close()

			serverChain, err := NewChainWrapper(serverConn, opts)
			if err != nil {
				b.Fatal(err)
			}
			clientChain, err := NewChainWrapper(clientConn, opts)
			if err != nil {
				b.Fatal(err)
			}
			defer serverChain.Close()
			defer clientChain.Close()

			payload := bytes.Repeat([]byte("x"), size)
			readErr := make(chan error, 1)
			go func() {
				buf := make([]byte, size)
				for i := 0; i < b.N; i++ {
					if _, err := io.ReadFull(serverChain, buf); err != nil {
						readErr <- err
						return
					}
				}
				readErr <- nil
			}()

			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := clientChain.Write(payload); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if err := <-readErr; err != nil {
				b.Fatal(err)
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var a [20]byte
	i := len(a)
	for n > 0 {
		i--
		a[i] = byte('0' + n%10)
		n /= 10
	}
	return string(a[i:])
}
