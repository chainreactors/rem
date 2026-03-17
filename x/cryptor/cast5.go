//go:build cast5

package cryptor

import (
	"crypto/cipher"

	"golang.org/x/crypto/cast5"
)

func init() {
	RegisterBlock("cast5", func() Block { return Cast5Block{} })
}

type Cast5Block struct{}

func (Cast5Block) Build(key []byte) (cipher.Block, error) {
	return cast5.NewCipher(key)
}
