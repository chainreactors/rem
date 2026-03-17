//go:build sm4

package cryptor

import (
	"crypto/cipher"

	"github.com/tjfoc/gmsm/sm4"
)

func init() {
	RegisterBlock("sm4", func() Block { return SM4Block{} })
}

type SM4Block struct{}

func (SM4Block) Build(key []byte) (cipher.Block, error) {
	return sm4.NewCipher(key)
}
