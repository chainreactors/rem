package simplex

import (
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

func ResolveSimplexAddr(network, address string) (*SimplexAddr, error) {
	// 使用RegisterSimplex注册的地址解析器
	if resolver, ok := simplexAddrResolvers[network]; ok {
		return resolver(network, address)
	} else {
		return nil, fmt.Errorf("unsupported network: %s", network)
	}
}

type SimplexAddr struct {
	*url.URL
	id          string
	interval    time.Duration
	maxBodySize int
	options     url.Values
	config      interface{} // 用于保存复杂配置对象，如 HTTPConfig, DNSConfig 等
	mu          sync.RWMutex
}

func (addr *SimplexAddr) Clone(ip string) *SimplexAddr {
	s, _ := url.Parse(addr.URL.String())
	s.Host = ip
	addr.mu.RLock()
	interval := addr.interval
	maxBodySize := addr.maxBodySize
	addr.mu.RUnlock()
	return &SimplexAddr{
		URL:         s,
		id:          addr.id,
		interval:    interval,
		maxBodySize: maxBodySize,
		options:     map[string][]string{},
	}
}

func (addr *SimplexAddr) Network() string {
	return addr.URL.Scheme
}

func (addr *SimplexAddr) String() string {
	addr.mu.RLock()
	addr.URL.RawQuery = addr.options.Encode()
	addr.mu.RUnlock()
	return addr.URL.String()
}

func (addr *SimplexAddr) Interval() time.Duration {
	addr.mu.RLock()
	defer addr.mu.RUnlock()
	return addr.interval
}

func (addr *SimplexAddr) MaxBodySize() int {
	return addr.maxBodySize - 5
}

func (addr *SimplexAddr) ID() string {
	return addr.id
}

func (addr *SimplexAddr) Config() interface{} {
	return addr.config
}

func (addr *SimplexAddr) SetConfig(config interface{}) {
	addr.config = config
}

func (addr *SimplexAddr) GetConfig() interface{} {
	return addr.config
}

// SetOption dynamically updates a configuration option.
// Known keys like "interval" also update the corresponding typed field.
func (addr *SimplexAddr) SetOption(key, value string) {
	addr.mu.Lock()
	defer addr.mu.Unlock()
	addr.options.Set(key, value)
	switch key {
	case "interval":
		if ms, err := strconv.Atoi(value); err == nil && ms > 0 {
			addr.interval = time.Duration(ms) * time.Millisecond
		}
	}
}

// GetOption returns a configuration option value (thread-safe).
func (addr *SimplexAddr) GetOption(key string) string {
	addr.mu.RLock()
	defer addr.mu.RUnlock()
	return addr.options.Get(key)
}
