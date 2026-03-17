package cio

import (
	"io"
	"testing"
)

func TestBuffer_OldPipeRoundtrip(t *testing.T) {
	buf := NewBuffer(1024)
	defer buf.Close()

	go func() {
		buf.Write([]byte("hello"))
		buf.Write([]byte("hello"))
		buf.Write([]byte("hello"))
		buf.Write([]byte("hello"))
	}()

	a := make([]byte, 5)
	_, err := io.ReadFull(buf, a)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != "hello" {
		t.Fatalf("got %q", a)
	}

	b := make([]byte, 15)
	_, err = io.ReadFull(buf, b)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hellohellohello" {
		t.Fatalf("got %q", b)
	}
}
