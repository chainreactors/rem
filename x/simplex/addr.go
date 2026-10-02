package simplex

import (
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/chainreactors/rem/x/arq"
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
	id            string
	interval      time.Duration
	maxBodySize   int
	itemsPerCycle int // 0 = use default; set by transport-specific resolver
	options       url.Values
	config        interface{} // 用于保存复杂配置对象，如 HTTPConfig, DNSConfig 等
	mu            sync.RWMutex
}

type simplexARQConfigProvider interface {
	GetARQConfig() arq.ARQConfig
}

func (addr *SimplexAddr) Clone(ip string) *SimplexAddr {
	s, _ := url.Parse(addr.String())
	s.Host = ip
	addr.mu.RLock()
	interval := addr.interval
	maxBodySize := addr.maxBodySize
	itemsPerCycle := addr.itemsPerCycle
	config := addr.config
	addr.mu.RUnlock()
	return &SimplexAddr{
		URL:           s,
		id:            addr.id,
		interval:      interval,
		maxBodySize:   maxBodySize,
		itemsPerCycle: itemsPerCycle,
		config:        config,
		options:       map[string][]string{},
	}
}

func (addr *SimplexAddr) Network() string {
	return addr.URL.Scheme
}

func (addr *SimplexAddr) String() string {
	addr.mu.RLock()
	clonedURL := *addr.URL
	clonedURL.RawQuery = addr.options.Encode()
	addr.mu.RUnlock()
	return clonedURL.String()
}

func (addr *SimplexAddr) Interval() time.Duration {
	addr.mu.RLock()
	defer addr.mu.RUnlock()
	return addr.interval
}

func (addr *SimplexAddr) MaxBodySize() int {
	return addr.maxBodySize - 5
}

func (addr *SimplexAddr) ARQConfig() arq.ARQConfig {
	addr.mu.RLock()
	config := addr.config
	maxBodySize := addr.maxBodySize
	addr.mu.RUnlock()

	if provider, ok := config.(simplexARQConfigProvider); ok {
		return provider.GetARQConfig()
	}

	mtu := maxBodySize - 5
	if mtu <= 0 {
		mtu = arq.ARQ_MTU
	}
	if mtu > arq.ARQ_MAX_MTU {
		mtu = arq.ARQ_MAX_MTU
	}

	return arq.ARQConfig{
		MTU:                mtu,
		RTO:                arq.ARQ_RTO,
		MaxRetransmissions: arq.ARQ_MAX_RETRANS,
	}
}

// SimplexConfig builds a SimplexConfig from this address's parameters.
// 用户可通过 URL param `max_packet` 指定最大包大小，系统自动推导分片数。
func (addr *SimplexAddr) SimplexConfig() SimplexConfig {
	addr.mu.RLock()
	sc := SimplexConfig{
		Interval:      addr.interval,
		MaxBodySize:   addr.maxBodySize,
		ItemsPerCycle: addr.itemsPerCycle,
	}
	if v := addr.options.Get("max_packet"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			sc.MaxPacketSize = n
			sc.ItemsPerCycle = 0
		}
	}
	addr.mu.RUnlock()
	sc.Normalize()
	return sc
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
