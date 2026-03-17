package wrapper

import "github.com/chainreactors/rem/x/cryptor"

func resolveAlgo(opt map[string]string, fallback string) string {
	if value, ok := opt["algo"]; ok && value != "" {
		return value
	}
	return fallback
}

func resolveIVByBlock(algo string, key []byte, opt map[string]string, fallback []byte) ([]byte, error) {
	block, err := cryptor.BuildBlock(algo, key)
	if err != nil {
		return nil, err
	}

	iv := make([]byte, block.BlockSize())
	if value, ok := opt["iv"]; ok {
		copy(iv, value)
	} else {
		copy(iv, fallback)
	}
	return iv, nil
}
