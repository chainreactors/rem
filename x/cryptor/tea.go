//go:build tea

package cryptor

import (
	"crypto/cipher"

	"golang.org/x/crypto/tea"
)

func init() {
	RegisterBlock("tea", func() Block { return TEABlock{} })
}

type TEABlock struct{}

func (TEABlock) Build(key []byte) (cipher.Block, error) {
	return tea.NewCipherWithRounds(key, 16)
}
