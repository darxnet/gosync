package gosync_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/darxnet/gosync"
)

func TestMapLoadOrCompute(t *testing.T) {
	t.Parallel()

	var (
		m     gosync.Map[string, int]
		calls int
	)

	compute := func() int {
		calls++

		return 42
	}

	// a zero map: the value is computed and stored
	v, loaded := m.LoadOrCompute("k", compute)
	if v != 42 || loaded || calls != 1 {
		t.Fatalf("LoadOrCompute on a missing key = (%d, %v), calls %d; want (42, false), 1", v, loaded, calls)
	}

	// a present key: compute is not called
	v, loaded = m.LoadOrCompute("k", compute)
	if v != 42 || !loaded || calls != 1 {
		t.Fatalf("LoadOrCompute on a present key = (%d, %v), calls %d; want (42, true), 1", v, loaded, calls)
	}

	if v, ok := m.Load("k"); !ok || v != 42 {
		t.Fatalf("Load after LoadOrCompute = (%d, %v)", v, ok)
	}

	// a key whose hash collides at the first levels is stored through expand
	m.Store("other", 1)

	if v, loaded := m.LoadOrCompute("third", func() int { return 3 }); v != 3 || loaded {
		t.Fatalf("LoadOrCompute with neighbours = (%d, %v)", v, loaded)
	}
}

// TestMapLoadOrComputeOnce checks the promise that sets LoadOrCompute apart
// from LoadOrStore(key, newValue()): with many goroutines racing on one key,
// compute runs exactly once.
func TestMapLoadOrComputeOnce(t *testing.T) {
	t.Parallel()

	const (
		goroutines = 64
		keys       = 32
	)

	var (
		m     gosync.Map[int, int]
		calls atomic.Int32
		wg    sync.WaitGroup
		start = make(chan struct{})
	)

	for g := range goroutines {
		wg.Add(1)

		go func() {
			defer wg.Done()

			<-start

			for k := range keys {
				v, _ := m.LoadOrCompute(k, func() int {
					calls.Add(1)

					return k*1000 + g
				})

				if v/1000 != k {
					t.Errorf("key %d got value %d", k, v)
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	if calls.Load() != keys {
		t.Fatalf("compute ran %d times for %d keys, want once per key", calls.Load(), keys)
	}

	if n := m.Len(); n != keys {
		t.Fatalf("Len = %d, want %d", n, keys)
	}
}
