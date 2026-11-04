package speedlayer

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the single-node backend: the in-process state the
// gateway always kept, extracted behind the Store interface. It is also
// the store a process runs on until boot installs another, so behaviour
// with no configuration is byte-for-byte the pre-P3 behaviour.
type MemoryStore struct {
	mu       sync.Mutex
	counters map[string]memCounter
	kv       map[string]memValue
	rings    map[string][]string
}

type memCounter struct {
	value     int64
	expiresAt time.Time
}

type memValue struct {
	value     string
	expiresAt time.Time // zero = no expiry
}

// NewMemoryStore creates an empty in-memory backend.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		counters: make(map[string]memCounter),
		kv:       make(map[string]memValue),
		rings:    make(map[string][]string),
	}
}

func (m *MemoryStore) Name() string { return "memory" }

func (m *MemoryStore) Ping(context.Context) error { return nil }

func (m *MemoryStore) AddCounter(_ context.Context, key string, delta int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.counters[key]
	if !c.expiresAt.IsZero() && time.Now().After(c.expiresAt) {
		c.value = 0
	}
	c.value += delta
	c.expiresAt = time.Now().Add(counterTTL)
	m.counters[key] = c
	return c.value, nil
}

func (m *MemoryStore) Counters(_ context.Context, prefix string) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make(map[string]int64)
	for k, c := range m.counters {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !c.expiresAt.IsZero() && now.After(c.expiresAt) {
			delete(m.counters, k)
			continue
		}
		if c.value != 0 {
			out[k] = c.value
		}
	}
	return out, nil
}

func (m *MemoryStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := memValue{value: value}
	if ttl > 0 {
		v.expiresAt = time.Now().Add(ttl)
	}
	m.kv[key] = v
	return nil
}

func (m *MemoryStore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.kv[key]
	if !ok {
		return "", false, nil
	}
	if !v.expiresAt.IsZero() && time.Now().After(v.expiresAt) {
		delete(m.kv, key)
		return "", false, nil
	}
	return v.value, true, nil
}

func (m *MemoryStore) GetMany(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok, err := m.Get(ctx, k); err != nil {
			return nil, err
		} else if ok {
			out[k] = v
		}
	}
	return out, nil
}

func (m *MemoryStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.kv, key)
	delete(m.counters, key)
	return nil
}

func (m *MemoryStore) DeletePrefix(_ context.Context, prefix string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.kv {
		if strings.HasPrefix(k, prefix) {
			delete(m.kv, k)
			n++
		}
	}
	for k := range m.counters {
		if strings.HasPrefix(k, prefix) {
			delete(m.counters, k)
			n++
		}
	}
	return n, nil
}

func (m *MemoryStore) RingPush(_ context.Context, key, value string, cap int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ring := append([]string{value}, m.rings[key]...)
	if cap > 0 && len(ring) > cap {
		ring = ring[:cap]
	}
	m.rings[key] = ring
	return nil
}

func (m *MemoryStore) RingRead(_ context.Context, key string, limit int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ring := m.rings[key]
	if limit > 0 && len(ring) > limit {
		ring = ring[:limit]
	}
	out := make([]string, len(ring))
	copy(out, ring)
	return out, nil
}
