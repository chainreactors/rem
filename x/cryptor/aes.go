package cryptor

import (
	"crypto/aes"
	"crypto/cipher"
)

func init() {
	RegisterBlock("aes", func() Block { return AESBlock{} })
}

type AESBlock struct{}

func (AESBlock) Build(key []byte) (cipher.Block, error) {
	return aes.NewCipher(key)
}
