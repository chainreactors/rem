//go:build tripledes

package cryptor

import (
	"crypto/cipher"
	"crypto/des"
)

func init() {
	RegisterBlock("tripledes", func() Block { return TripleDESBlock{} })
}

type TripleDESBlock struct{}

func (TripleDESBlock) Build(key []byte) (cipher.Block, error) {
	return des.NewTripleDESCipher(key)
}
