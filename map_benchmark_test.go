package gosync_test

import (
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"testing"

	"github.com/darxnet/gosync"
)

// syncMapTyped is the usual generic wrapper around sync.Map: every call boxes
// the key (and the value) into an interface and asserts the type on the way
// out. It is the design gosync.Map replaces, kept here for comparison.
type syncMapTyped[K comparable, V any] struct {
	m sync.Map
}

func (m *syncMapTyped[K, V]) Load(key K) (value V, ok bool) {
	v, ok := m.m.Load(key)
	if v != nil {
		value = v.(V) //nolint:forcetypeassert // the map holds only V
	}
	return value, ok
}

func (m *syncMapTyped[K, V]) Store(key K, value V) { m.m.Store(key, value) }

func (m *syncMapTyped[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	v, loaded := m.m.LoadOrStore(key, value)
	if v != nil {
		actual = v.(V) //nolint:forcetypeassert // the map holds only V
	}
	return actual, loaded
}

func (m *syncMapTyped[K, V]) Delete(key K) { m.m.Delete(key) }

func (m *syncMapTyped[K, V]) Range(f func(K, V) bool) {
	m.m.Range(func(key, value any) bool {
		return f(key.(K), value.(V)) //nolint:forcetypeassert // the map holds only K and V
	})
}

// rwMutexMap is a plain map behind a sync.RWMutex.
type rwMutexMap[K comparable, V any] struct {
	m  map[K]V
	rw sync.RWMutex
}

func (m *rwMutexMap[K, V]) Load(key K) (V, bool) {
	m.rw.RLock()
	v, ok := m.m[key]
	m.rw.RUnlock()
	return v, ok
}

func (m *rwMutexMap[K, V]) Store(key K, value V) {
	m.rw.Lock()
	if m.m == nil {
		m.m = make(map[K]V)
	}
	m.m[key] = value
	m.rw.Unlock()
}

func (m *rwMutexMap[K, V]) LoadOrStore(key K, value V) (V, bool) {
	m.rw.Lock()
	defer m.rw.Unlock()
	if v, ok := m.m[key]; ok {
		return v, true
	}
	if m.m == nil {
		m.m = make(map[K]V)
	}
	m.m[key] = value
	return value, false
}

func (m *rwMutexMap[K, V]) Delete(key K) {
	m.rw.Lock()
	delete(m.m, key)
	m.rw.Unlock()
}

func (m *rwMutexMap[K, V]) Range(f func(K, V) bool) {
	m.rw.RLock()
	defer m.rw.RUnlock()
	for k, v := range m.m {
		if !f(k, v) {
			return
		}
	}
}

// benchMap is what every benchmarked implementation offers.
type benchMap[K comparable, V any] interface {
	Load(key K) (value V, ok bool)
	Store(key K, value V)
	LoadOrStore(key K, value V) (actual V, loaded bool)
	Delete(key K)
	Range(f func(key K, value V) bool)
}

type record struct {
	id   uint32
	name string
}

const benchSize = 1 << 14 // entries in a prefilled map

func implementations[K comparable]() map[string]func() benchMap[K, *record] {
	return map[string]func() benchMap[K, *record]{
		"gosync":       func() benchMap[K, *record] { return new(gosync.Map[K, *record]) },
		"syncMapTyped": func() benchMap[K, *record] { return new(syncMapTyped[K, *record]) },
		"rwMutexMap":   func() benchMap[K, *record] { return new(rwMutexMap[K, *record]) },
	}
}

// names lists the implementations in a stable order.
var names = []string{"gosync", "syncMapTyped", "rwMutexMap"}

func uintKeys() []uint32 {
	keys := make([]uint32, benchSize)
	for i := range keys {
		keys[i] = uint32(i) * 2654435761 // spread out, mostly above 255 so boxing allocates
	}
	return keys
}

func stringKeys() []string {
	keys := make([]string, benchSize)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
	}
	return keys
}

func fill[K comparable](m benchMap[K, *record], keys []K) {
	for i, k := range keys {
		m.Store(k, &record{id: uint32(i), name: "r"})
	}
}

func benchLoad[K comparable](b *testing.B, keys []K) {
	b.Helper()
	impls := implementations[K]()

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			fill(m, keys)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					_, _ = m.Load(keys[i&(benchSize-1)])
					i++
				}
			})
		})
	}
}

// BenchmarkLoad is the read-mostly hot path: parallel loads of existing keys.
func BenchmarkLoad(b *testing.B) {
	b.Run("keys=uint32", func(b *testing.B) { benchLoad(b, uintKeys()) })
	b.Run("keys=string", func(b *testing.B) { benchLoad(b, stringKeys()) })
}

// BenchmarkLoadMissing loads keys that are not in the map.
func BenchmarkLoadMissing(b *testing.B) {
	keys := uintKeys()
	impls := implementations[uint32]()

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			fill(m, keys)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := uint32(1)
				for pb.Next() {
					_, _ = m.Load(i) // odd keys are never stored
					i += 2
				}
			})
		})
	}
}

// BenchmarkLoadOrStore hits existing keys, as a cache does after warm-up.
func BenchmarkLoadOrStore(b *testing.B) {
	keys := uintKeys()
	impls := implementations[uint32]()
	value := &record{}

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			fill(m, keys)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					_, _ = m.LoadOrStore(keys[i&(benchSize-1)], value)
					i++
				}
			})
		})
	}
}

// BenchmarkStore overwrites existing keys in parallel.
func BenchmarkStore(b *testing.B) {
	keys := uintKeys()
	impls := implementations[uint32]()
	value := &record{}

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			fill(m, keys)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					m.Store(keys[i&(benchSize-1)], value)
					i++
				}
			})
		})
	}
}

// BenchmarkInsert stores keys that are new to the map: the map grows.
func BenchmarkInsert(b *testing.B) {
	impls := implementations[uint32]()
	value := &record{}

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			b.ReportAllocs()
			for i := range b.N {
				m.Store(uint32(i), value)
			}
		})
	}
}

// BenchmarkWriteReadDeleteCycle is Store + Load + Delete of one key per
// iteration: the high-churn case where sync.Map is known to be weak.
func BenchmarkWriteReadDeleteCycle(b *testing.B) {
	keys := uintKeys()
	impls := implementations[uint32]()
	value := &record{}

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				i := rand.Intn(benchSize) //nolint:gosec // benchmark
				for pb.Next() {
					k := keys[i&(benchSize-1)]
					i++
					m.Store(k, value)
					_, _ = m.Load(k)
					m.Delete(k)
				}
			})
		})
	}
}

// BenchmarkRange walks a map of benchSize entries.
func BenchmarkRange(b *testing.B) {
	keys := uintKeys()
	impls := implementations[uint32]()

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			fill(m, keys)
			b.ReportAllocs()
			b.ResetTimer()
			n := 0
			for range b.N {
				m.Range(func(uint32, *record) bool { n++; return true })
			}
			if n != b.N*benchSize {
				b.Fatalf("visited %d entries, want %d", n, b.N*benchSize)
			}
		})
	}
}

// BenchmarkLoadLarge loads from a map of 128K string keys, as in the Go
// standard library benchmark of the hash-trie.
func BenchmarkLoadLarge(b *testing.B) {
	const n = 128 << 10
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("%b", i)
	}
	impls := implementations[string]()

	for _, name := range names {
		b.Run("impl="+name, func(b *testing.B) {
			m := impls[name]()
			fill(m, keys)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					_, _ = m.Load(keys[i])
					i++
					if i == n {
						i = 0
					}
				}
			})
		})
	}
}
