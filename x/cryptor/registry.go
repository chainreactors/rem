package cryptor

import (
	"crypto/cipher"
	"sort"
	"strings"
	"sync"
)

var (
	registryMu sync.RWMutex
	blockMap   = map[string]func() Block{}
)

func RegisterBlock(name string, factory func() Block) {
	if factory == nil {
		return
	}
	normalized := normalizeName(name)
	if normalized == "" {
		return
	}

	registryMu.Lock()
	blockMap[normalized] = factory
	registryMu.Unlock()
}

func BlockByName(name string) (Block, error) {
	registryMu.RLock()
	factory, ok := blockMap[normalizeName(name)]
	registryMu.RUnlock()
	if !ok {
		return nil, ErrBlockNotFound
	}
	return factory(), nil
}

func BuildBlock(name string, key []byte) (cipher.Block, error) {
	block, err := BlockByName(name)
	if err != nil {
		return nil, err
	}
	return block.Build(key)
}

func StreamByName(name string) (Stream, error) {
	block, err := BlockByName(name)
	if err != nil {
		return nil, err
	}
	return StreamOf(block), nil
}

func BlockNames() []string {
	registryMu.RLock()
	names := make([]string, 0, len(blockMap))
	for name := range blockMap {
		names = append(names, name)
	}
	registryMu.RUnlock()
	sort.Strings(names)
	return names
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
