//go:build oss

package simplex

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// mockOSSConnector implements OSSConnector with in-memory storage and fault injection.
type mockOSSConnector struct {
	mu      sync.Mutex
	objects map[string][]byte

	// Fault injection (all accessed under mu)
	failPut    bool
	failGet    bool
	failList   bool
	failDelete bool
	latency    time.Duration
	lossRate   float64 // 0.0 - 1.0
}

func newMockOSSConnector() *mockOSSConnector {
	return &mockOSSConnector{
		objects: make(map[string][]byte),
	}
}

// setFault sets fault injection flags thread-safely.
func (m *mockOSSConnector) setFault(put, get bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failPut = put
	m.failGet = get
}

func (m *mockOSSConnector) getLatency() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.latency
}

func (m *mockOSSConnector) applyLatency() {
	lat := m.getLatency()
	if lat > 0 {
		time.Sleep(lat)
	}
}

func (m *mockOSSConnector) checkFault(fail *bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if *fail {
		return true
	}
	if m.lossRate > 0 {
		return rand.Float64() < m.lossRate
	}
	return false
}

func (m *mockOSSConnector) PutObject(key string, data []byte) error {
	m.applyLatency()
	if m.checkFault(&m.failPut) {
		return fmt.Errorf("mock: PutObject failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.objects[key] = cp
	return nil
}

func (m *mockOSSConnector) GetObject(key string) ([]byte, error) {
	m.applyLatency()
	if m.checkFault(&m.failGet) {
		return nil, fmt.Errorf("mock: GetObject failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("mock: object not found: %s", key)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (m *mockOSSConnector) HeadObject(key string) (bool, error) {
	m.applyLatency()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lossRate > 0 && rand.Float64() < m.lossRate {
		return false, fmt.Errorf("mock: HeadObject failed")
	}
	_, ok := m.objects[key]
	return ok, nil
}

func (m *mockOSSConnector) DeleteObject(key string) error {
	m.applyLatency()
	if m.checkFault(&m.failDelete) {
		return fmt.Errorf("mock: DeleteObject failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *mockOSSConnector) ListObjects(prefix string) ([]OSSObjectInfo, error) {
	m.applyLatency()
	if m.checkFault(&m.failList) {
		return nil, fmt.Errorf("mock: ListObjects failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []OSSObjectInfo
	for key, data := range m.objects {
		if strings.HasPrefix(key, prefix) {
			result = append(result, OSSObjectInfo{Key: key, Size: int64(len(data))})
		}
	}
	return result, nil
}

func (m *mockOSSConnector) GenerateOSSURL(key string) string {
	return "https://mock.oss.com/" + key
}

func (m *mockOSSConnector) Sign(req *http.Request, key string) error {
	return nil
}

func (m *mockOSSConnector) Validate() error {
	return nil
}
