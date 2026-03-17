//go:build blowfish

package cryptor

import (
	"crypto/cipher"

	"golang.org/x/crypto/blowfish"
)

func init() {
	RegisterBlock("blowfish", func() Block { return BlowfishBlock{} })
}

type BlowfishBlock struct{}

func (BlowfishBlock) Build(key []byte) (cipher.Block, error) {
	return blowfish.NewCipher(key)
}
