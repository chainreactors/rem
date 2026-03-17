//go:build xtea

package cryptor

import (
	"crypto/cipher"

	"golang.org/x/crypto/xtea"
)

func init() {
	RegisterBlock("xtea", func() Block { return XTEABlock{} })
}

type XTEABlock struct{}

func (XTEABlock) Build(key []byte) (cipher.Block, error) {
	return xtea.NewCipher(key)
}
