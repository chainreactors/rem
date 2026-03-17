package cryptor

func StreamOf(block Block) Stream {
	return NewBlockStream(block)
}
