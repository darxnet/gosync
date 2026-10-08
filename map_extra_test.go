package gosync_test

import (
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"testing/quick"

	"github.com/darxnet/gosync"
)

func TestZeroMapReads(t *testing.T) {
	t.Parallel()

	var m gosync.ComparableMap[string, int]

	if v, ok := m.Load("a"); ok || v != 0 {
		t.Fatalf("Load on zero map = %v, %v", v, ok)
	}
	if v, ok := m.LoadAndDelete("a"); ok || v != 0 {
		t.Fatalf("LoadAndDelete on zero map = %v, %v", v, ok)
	}
	if m.CompareAndSwap("a", 1, 2) {
		t.Fatal("CompareAndSwap on zero map succeeded")
	}
	if m.CompareAndDelete("a", 1) {
		t.Fatal("CompareAndDelete on zero map succeeded")
	}
	m.Delete("a")
	m.Clear()
	m.Range(func(string, int) bool { t.Fatal("Range on zero map yielded"); return false })
	if !m.Empty() || m.Len() != 0 {
		t.Fatalf("zero map: Empty() = %v, Len() = %d", m.Empty(), m.Len())
	}
}

func TestLenEmpty(t *testing.T) {
	t.Parallel()

	var m gosync.Map[int, string]

	for i := range 1000 {
		m.Store(i, "x")
		if n := m.Len(); n != i+1 {
			t.Fatalf("Len() = %d, want %d", n, i+1)
		}
	}
	if m.Empty() {
		t.Fatal("Empty() = true on a map with 1000 entries")
	}

	for i := range 1000 {
		m.Delete(i)
	}
	if !m.Empty() || m.Len() != 0 {
		t.Fatalf("after deleting all: Empty() = %v, Len() = %d", m.Empty(), m.Len())
	}

	m.Store(1, "x")
	m.Clear()
	if !m.Empty() {
		t.Fatal("Empty() = false after Clear")
	}
}

func TestAllIterator(t *testing.T) {
	t.Parallel()

	var m gosync.Map[int, int]
	for i := range 100 {
		m.Store(i, i*i)
	}

	seen := 0
	for k, v := range m.All() {
		if v != k*k {
			t.Fatalf("All: key %d has value %d", k, v)
		}
		seen++
		if seen == 50 {
			break
		}
	}
	if seen != 50 {
		t.Fatalf("All visited %d entries before break, want 50", seen)
	}
}

type point struct{ x, y int }

func TestMapCompareWithFunc(t *testing.T) {
	t.Parallel()

	sameX := func(current, old []int) bool { return current[0] == old[0] }

	var m gosync.Map[string, []int]
	m.Store("p", []int{1, 2})

	if m.CompareAndSwap("p", []int{2}, []int{3}, sameX) {
		t.Fatal("CompareAndSwap matched a different value")
	}
	if !m.CompareAndSwap("p", []int{1}, []int{3}, sameX) {
		t.Fatal("CompareAndSwap did not match an equal value")
	}
	if v, _ := m.Load("p"); v[0] != 3 {
		t.Fatalf("value after CompareAndSwap = %v", v)
	}

	if m.CompareAndDelete("p", []int{1}, sameX) {
		t.Fatal("CompareAndDelete matched a different value")
	}
	if !m.CompareAndDelete("p", []int{3}, sameX) {
		t.Fatal("CompareAndDelete did not match an equal value")
	}
	if _, ok := m.Load("p"); ok {
		t.Fatal("entry still present after CompareAndDelete")
	}

	for name, f := range map[string]func(){
		"CompareAndSwap":   func() { m.CompareAndSwap("p", nil, nil, nil) },
		"CompareAndDelete": func() { m.CompareAndDelete("p", nil, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s with nil comparator did not panic", name)
				}
			}()
			f()
		}()
	}
}

func TestStructKeys(t *testing.T) {
	t.Parallel()

	var m gosync.ComparableMap[point, *point]

	for x := range 50 {
		for y := range 50 {
			p := &point{x, y}
			m.Store(*p, p)
		}
	}
	if n := m.Len(); n != 2500 {
		t.Fatalf("Len() = %d, want 2500", n)
	}
	for x := range 50 {
		for y := range 50 {
			v, ok := m.Load(point{x, y})
			if !ok || *v != (point{x, y}) {
				t.Fatalf("Load(%v) = %v, %v", point{x, y}, v, ok)
			}
		}
	}
}

// TestNoAlloc checks the hot paths that must not allocate.
func TestNoAlloc(t *testing.T) { //nolint:paralleltest // AllocsPerRun
	var m gosync.ComparableMap[string, int]
	keys := []string{"alpha", "beta", "gamma", "delta"}
	for i, k := range keys {
		m.Store(k, i)
	}

	checks := map[string]func(){
		"Load":              func() { m.Load("gamma") },
		"LoadMissing":       func() { m.Load("omega") },
		"LoadOrStoreExists": func() { m.LoadOrStore("gamma", 0) },
		"DeleteMissing":     func() { m.Delete("omega") },
		"CompareAndSwapNo":  func() { m.CompareAndSwap("gamma", -1, 0) },
		"Range":             func() { m.Range(func(string, int) bool { return true }) },
		"Len":               func() { m.Len() },
		"Empty":             func() { m.Empty() },
	}

	for name, f := range checks {
		if allocs := testing.AllocsPerRun(100, f); allocs != 0 {
			t.Errorf("%s: %v allocs/op, want 0", name, allocs)
		}
	}

	// A store of an existing key publishes one new entry node.
	if allocs := testing.AllocsPerRun(100, func() { m.Store("gamma", 7) }); allocs != 1 {
		t.Errorf("Store existing: %v allocs/op, want 1", allocs)
	}
}

func allBasicTypes() []reflect.Type {
	return []reflect.Type{
		reflect.TypeFor[*int](), reflect.TypeFor[*int8](), reflect.TypeFor[*int16](),
		reflect.TypeFor[*int32](), reflect.TypeFor[*int64](),
		reflect.TypeFor[*uint](), reflect.TypeFor[*uint8](), reflect.TypeFor[*uint16](),
		reflect.TypeFor[*uint32](), reflect.TypeFor[*uint64](), reflect.TypeFor[*uintptr](),
		reflect.TypeFor[*float32](), reflect.TypeFor[*float64](),
		reflect.TypeFor[*complex64](), reflect.TypeFor[*complex128](),
		reflect.TypeFor[*bool](), reflect.TypeFor[*string](),
	}
}

// TestQuickAgainstSyncMap checks, with random inputs, that Map behaves exactly
// as sync.Map for every basic key and value type.
func TestQuickAgainstSyncMap(t *testing.T) {
	t.Parallel()

	for _, typ := range allBasicTypes() {
		t.Run(typ.String(), func(t *testing.T) {
			t.Parallel()

			var (
				m1 gosync.ComparableMap[any, any]
				m2 sync.Map
			)

			config := &quick.Config{
				Values: func(values []reflect.Value, rand *rand.Rand) {
					for j := range values {
						values[j], _ = quick.Value(typ, rand)
					}
				},
			}

			for name, pair := range map[string][2]any{
				"Store":            {m1.Store, m2.Store},
				"LoadOrStore":      {m1.LoadOrStore, m2.LoadOrStore},
				"Load":             {m1.Load, m2.Load},
				"Swap":             {m1.Swap, m2.Swap},
				"CompareAndSwap":   {m1.CompareAndSwap, m2.CompareAndSwap},
				"CompareAndDelete": {m1.CompareAndDelete, m2.CompareAndDelete},
				"LoadAndDelete":    {m1.LoadAndDelete, m2.LoadAndDelete},
				"Delete":           {m1.Delete, m2.Delete},
			} {
				if err := quick.CheckEqual(pair[0], pair[1], config); err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}

			if m1.Len() == 0 {
				t.Error("Len() = 0 after random stores")
			}

			m1.Clear()
			m2.Clear()

			if !m1.Empty() {
				t.Error("Empty() = false after Clear")
			}
		})
	}
}

// TestConcurrentChurn hammers a few shared keys with every mutating operation
// at once, so the retry paths taken when a node changes between the lock-free
// lookup and the lock are exercised.
func TestConcurrentChurn(t *testing.T) {
	t.Parallel()

	const (
		keys       = 4
		iterations = 20_000
	)

	var (
		m  gosync.ComparableMap[int, int]
		wg sync.WaitGroup
	)

	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range iterations {
				k := i % keys
				switch (i + g) % 5 {
				case 0:
					m.Store(k, g)
				case 1:
					m.Swap(k, g)
				case 2:
					m.LoadAndDelete(k)
				case 3:
					m.CompareAndDelete(k, g)
				default:
					m.CompareAndSwap(k, g, g+1)
				}
				if v, ok := m.Load(k); ok && (v < 0 || v > 8) {
					t.Errorf("key %d has value %d from nowhere", k, v)
				}
			}
		}()
	}
	wg.Wait()

	if n := m.Len(); n > keys {
		t.Fatalf("Len() = %d, want at most %d", n, keys)
	}
}
