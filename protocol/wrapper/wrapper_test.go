package wrapper

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/chainreactors/rem/protocol/core"
)

func TestWrapperChainRoundTrip(t *testing.T) {
	// Simulate server <-> client with net.Pipe
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	opts := core.WrapperOptions{
		&core.WrapperOption{
			Name:    core.CryptorWrapper,
			Options: map[string]string{"algo": "aes", "key": "abcdefghijklmnopqrstuvwxyz012345", "iv": "abcdefghijklmnop"},
		},
	}

	serverChain, err := NewChainWrapper(serverConn, opts)
	if err != nil {
		t.Fatalf("server NewChainWrapper failed: %v", err)
	}

	clientChain, err := NewChainWrapper(clientConn, opts)
	if err != nil {
		t.Fatalf("client NewChainWrapper failed: %v", err)
	}

	testData := []byte("hello wrapper chain test")

	// Client writes, server reads
	errCh := make(chan error, 1)
	go func() {
		_, err := clientChain.Write(testData)
		errCh <- err
	}()

	buf := make([]byte, 1024)
	n, err := serverChain.Read(buf)
	if err != nil {
		t.Fatalf("server read failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("client write failed: %v", err)
	}

	if !bytes.Equal(buf[:n], testData) {
		t.Errorf("data mismatch:\n  got:  %q\n  want: %q", buf[:n], testData)
	}
	t.Logf("single cryptor(aes) wrapper round-trip OK: %q", buf[:n])
}

func TestWrapperChainMultiWrapper(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	opts := core.WrapperOptions{
		&core.WrapperOption{
			Name:    core.CryptorWrapper,
			Options: map[string]string{"algo": "aes", "key": "abcdefghijklmnopqrstuvwxyz012345", "iv": "abcdefghijklmnop"},
		},
		&core.WrapperOption{
			Name:    core.CryptorWrapper,
			Options: map[string]string{"algo": "xor", "key": "xorkey12345678901234567890123456", "iv": "xorivxorivxorivx"},
		},
	}

	serverChain, err := NewChainWrapper(serverConn, opts)
	if err != nil {
		t.Fatalf("server NewChainWrapper failed: %v", err)
	}

	clientChain, err := NewChainWrapper(clientConn, opts)
	if err != nil {
		t.Fatalf("client NewChainWrapper failed: %v", err)
	}

	testData := []byte("hello multi-wrapper chain test 1234567890")

	errCh := make(chan error, 1)
	go func() {
		_, err := clientChain.Write(testData)
		errCh <- err
	}()

	buf := make([]byte, 1024)
	n, err := serverChain.Read(buf)
	if err != nil {
		t.Fatalf("server read failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("client write failed: %v", err)
	}

	if !bytes.Equal(buf[:n], testData) {
		t.Errorf("data mismatch:\n  got:  %q\n  want: %q", buf[:n], testData)
	}
	t.Logf("multi-wrapper round-trip OK: %q", buf[:n])
}

func TestWrapperChainGeneratedOptions(t *testing.T) {
	// Use the same flow as NewConsole: generate random options, serialize, parse, create chain
	core.AvailableWrappers = []string{core.CryptorWrapper}

	key := core.DefaultKey
	opts := core.GenerateRandomWrapperOptions(2, 4)
	t.Logf("generated %d wrappers", len(opts))
	for i, o := range opts {
		t.Logf("  [%d] %s: %v", i, o.Name, o.Options)
	}

	// Serialize and parse (simulating server -> client URL transfer)
	encoded := opts.String(key)
	parsed, err := core.ParseWrapperOptions(encoded, key)
	if err != nil {
		t.Fatalf("ParseWrapperOptions failed: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Server uses original opts, client uses parsed opts
	serverChain, err := NewChainWrapper(serverConn, opts)
	if err != nil {
		t.Fatalf("server NewChainWrapper failed: %v", err)
	}

	clientChain, err := NewChainWrapper(clientConn, parsed)
	if err != nil {
		t.Fatalf("client NewChainWrapper failed: %v", err)
	}

	testData := []byte("generated wrapper options end-to-end test")

	errCh := make(chan error, 1)
	go func() {
		_, err := clientChain.Write(testData)
		errCh <- err
	}()

	buf := make([]byte, 1024)
	n, err := serverChain.Read(buf)
	if err != nil {
		t.Fatalf("server read failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("client write failed: %v", err)
	}

	if !bytes.Equal(buf[:n], testData) {
		t.Errorf("data mismatch:\n  got:  %q\n  want: %q", buf[:n], testData)
	}
	fmt.Printf("generated options end-to-end OK: %q\n", buf[:n])
}

func TestSingleCryptorAES(t *testing.T) {
	// Test a single cryptor wrapper with AES directly (no chain)
	var encrypted bytes.Buffer
	opt := map[string]string{"algo": "aes", "key": "abcdefghijklmnopqrstuvwxyz012345", "iv": "abcdefghijklmnop"}

	testData := []byte("direct AES wrapper test")

	// Encrypt
	encWrapper, err := NewCryptorWrapper(nil, &encrypted, opt)
	if err != nil {
		t.Fatalf("NewCryptorWrapper (encrypt) failed: %v", err)
	}
	_, err = encWrapper.Write(testData)
	if err != nil {
		t.Fatalf("encrypt write failed: %v", err)
	}
	t.Logf("encrypted %d bytes -> %d bytes", len(testData), encrypted.Len())

	// Decrypt
	decWrapper, err := NewCryptorWrapper(&encrypted, nil, opt)
	if err != nil {
		t.Fatalf("NewCryptorWrapper (decrypt) failed: %v", err)
	}
	buf := make([]byte, 1024)
	n, err := decWrapper.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("decrypt read failed: %v", err)
	}

	if !bytes.Equal(buf[:n], testData) {
		t.Errorf("cryptor(aes) direct mismatch:\n  got:  %q\n  want: %q", buf[:n], testData)
	}
	t.Logf("direct cryptor(aes) OK: %q", buf[:n])
}

func TestSingleCryptorXOR(t *testing.T) {
	var encrypted bytes.Buffer
	opt := map[string]string{"algo": "xor", "key": "xorkey12345678901234567890123456", "iv": "xorivxorivxorivx"}

	testData := []byte("direct XOR wrapper test")

	encWrapper, err := NewCryptorWrapper(nil, &encrypted, opt)
	if err != nil {
		t.Fatalf("NewCryptorWrapper (encrypt) failed: %v", err)
	}

	_, err = encWrapper.Write(testData)
	if err != nil {
		t.Fatalf("encrypt write failed: %v", err)
	}
	t.Logf("encrypted %d bytes -> %d bytes", len(testData), encrypted.Len())

	decWrapper, err := NewCryptorWrapper(&encrypted, nil, opt)
	if err != nil {
		t.Fatalf("NewCryptorWrapper (decrypt) failed: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := decWrapper.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("decrypt read failed: %v", err)
	}

	if !bytes.Equal(buf[:n], testData) {
		t.Errorf("cryptor(xor) direct mismatch:\n  got:  %q\n  want: %q", buf[:n], testData)
	}
	t.Logf("direct cryptor(xor) OK: %q", buf[:n])
}
