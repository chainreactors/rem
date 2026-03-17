package cryptor

import "crypto/cipher"

const xorBlockSize = 16

func init() {
	RegisterBlock("xor", func() Block { return XORBlock{} })
}

type XORBlock struct{}

func (XORBlock) Build(key []byte) (cipher.Block, error) {
	if len(key) == 0 {
		key = []byte{0}
	}

	seed := make([]byte, len(key))
	copy(seed, key)
	return &xorBlock{key: seed}, nil
}

type xorBlock struct {
	key []byte
}

func (block *xorBlock) BlockSize() int {
	return xorBlockSize
}

func (block *xorBlock) Encrypt(dst, src []byte) {
	for i := 0; i < xorBlockSize; i++ {
		dst[i] = src[i] ^ block.key[i%len(block.key)]
	}
}

func (block *xorBlock) Decrypt(dst, src []byte) {
	block.Encrypt(dst, src)
}
