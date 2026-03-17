//go:build twofish

package cryptor

import (
	"crypto/cipher"

	"golang.org/x/crypto/twofish"
)

func init() {
	RegisterBlock("twofish", func() Block { return TwofishBlock{} })
}

type TwofishBlock struct{}

func (TwofishBlock) Build(key []byte) (cipher.Block, error) {
	return twofish.NewCipher(key)
}
