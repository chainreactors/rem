package cryptor

import (
	"crypto/cipher"
	"errors"
)

var (
	ErrBlockNotConfigured = errors.New("cryptor: block is not configured")
	ErrInvalidIVLength    = errors.New("cryptor: invalid iv length")
	ErrBlockNotFound      = errors.New("cryptor: block not found")
)

type Stream interface {
	Encryptor(key, iv []byte) (cipher.Stream, error)
	Decryptor(key, iv []byte) (cipher.Stream, error)
}

type Block interface {
	Build(key []byte) (cipher.Block, error)
}

type BlockStream struct {
	Block Block
}

func NewBlockStream(block Block) *BlockStream {
	return &BlockStream{Block: block}
}

func (stream *BlockStream) Encryptor(key, iv []byte) (cipher.Stream, error) {
	if stream.Block == nil {
		return nil, ErrBlockNotConfigured
	}

	block, err := stream.Block.Build(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, ErrInvalidIVLength
	}
	seed := make([]byte, len(iv))
	copy(seed, iv)
	return cipher.NewCTR(block, seed), nil
}

func (stream *BlockStream) Decryptor(key, iv []byte) (cipher.Stream, error) {
	return stream.Encryptor(key, iv)
}
