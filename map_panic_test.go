package gosync_test

import (
	"testing"
	"time"

	"github.com/darxnet/gosync"
)

// mustPanic calls f and fails the test if it does not panic.
func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", name)
		}
	}()

	f()
}

// completes fails the test if f does not return in time: a node left locked
// by a panic makes the next write to its subtree block forever.
func completes(t *testing.T, name string, f func()) {
	t.Helper()

	done := make(chan struct{})

	go func() {
		defer close(done)

		f()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s blocked: a node is still locked after a panic in user code", name)
	}
}

// TestUserCodePanicUnlocks checks that a panic in a user function called with
// a node locked (eq, compute, or == on values of a dynamic type that is not
// comparable) unlocks the node, so later writes to the same key do not hang.
func TestUserCodePanicUnlocks(t *testing.T) {
	t.Parallel()

	panicky := func(_, _ int) bool { panic("eq") }

	t.Run("CompareAndDelete", func(t *testing.T) {
		t.Parallel()

		var m gosync.Map[string, int]
		m.Store("k", 1)

		mustPanic(t, "CompareAndDelete", func() { m.CompareAndDelete("k", 1, panicky) })
		completes(t, "Store after CompareAndDelete", func() { m.Store("k", 2) })

		if !m.CompareAndDelete("k", 2, func(a, b int) bool { return a == b }) {
			t.Fatal("CompareAndDelete after the panic did not delete")
		}
	})

	t.Run("CompareAndDeleteNotComparable", func(t *testing.T) {
		t.Parallel()

		var m gosync.ComparableMap[string, any]
		m.Store("k", []byte("v"))

		mustPanic(t, "CompareAndDelete", func() { m.CompareAndDelete("k", []byte("v")) })
		completes(t, "Store after CompareAndDelete", func() { m.Store("k", 2) })
	})

	t.Run("CompareAndDeleteOverflow", func(t *testing.T) {
		t.Parallel()

		// every key hashes to 0, so "b" sits in the overflow chain of "a"
		m := gosync.NewBadMap[string, any]()
		m.Store("b", []byte("v"))
		m.Store("a", 1)

		mustPanic(t, "CompareAndDelete", func() { m.CompareAndDelete("b", []byte("v")) })
		completes(t, "Store after CompareAndDelete", func() { m.Store("c", 3) })
	})

	t.Run("CompareAndSwap", func(t *testing.T) {
		t.Parallel()

		var m gosync.Map[string, int]
		m.Store("k", 1)

		// the first call is the unlocked lookup, the second runs locked
		calls := 0
		eq := func(a, b int) bool {
			calls++
			if calls > 1 {
				panic("eq")
			}

			return a == b
		}

		mustPanic(t, "CompareAndSwap", func() { m.CompareAndSwap("k", 1, 2, eq) })
		completes(t, "Store after CompareAndSwap", func() { m.Store("k", 2) })
	})

	t.Run("LoadOrCompute", func(t *testing.T) {
		t.Parallel()

		var m gosync.Map[string, int]

		mustPanic(t, "LoadOrCompute", func() { m.LoadOrCompute("k", func() int { panic("compute") }) })
		completes(t, "Store after LoadOrCompute", func() { m.Store("k", 2) })
	})
}
