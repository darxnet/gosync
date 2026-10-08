package gosync

import "testing"

func TestInitSlowAlreadyInitialized(t *testing.T) {
	t.Parallel()

	var m Map[int, int]
	m.initSlow()
	root := m.root.Load()
	m.initSlow()

	if m.root.Load() != root {
		t.Fatal("root changed after second initSlow")
	}
}

func TestNodeCastPanics(t *testing.T) {
	t.Parallel()

	expectPanic := func(name string, f func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected panic", name)
			}
		}()
		f()
	}

	expectPanic("entry on indirect", func() { newIndirectNode[int, int](nil).entry() })
	expectPanic("indirect on entry", func() { newEntryNode(1, 1).indirect() })
}

// deepMap returns a map whose trie is a chain of indirect nodes along the
// all-zero path, deeper than the hash has bits, with hash 0 for every key.
// Walking it must end in the "ran out of hash bits" panic.
func deepMap() *Map[int, int] {
	var m Map[int, int]
	m.init()
	m.hashMask = 0

	i := m.root.Load()
	for range hashBits / nChildrenLog2 {
		next := newIndirectNode(i)
		i.children[0].Store(&next.node)
		i = next
	}

	return &m
}

func TestRanOutOfHashBitsPanics(t *testing.T) {
	t.Parallel()

	tests := map[string]func(m *Map[int, int]){
		"Load":           func(m *Map[int, int]) { m.Load(1) },
		"LoadOrStore":    func(m *Map[int, int]) { m.LoadOrStore(1, 1) },
		"Swap":           func(m *Map[int, int]) { m.Swap(1, 1) },
		"LoadAndDelete":  func(m *Map[int, int]) { m.LoadAndDelete(1) },
		"CompareAndSwap": func(m *Map[int, int]) { m.CompareAndSwap(1, 1, 2, equal[int]) },
	}

	for name, f := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if r := recover(); r != panicHashBitsIterate {
					t.Fatalf("recovered %v, want %q", r, panicHashBitsIterate)
				}
			}()

			f(deepMap())
		})
	}
}

func TestExpandRanOutOfHashBitsPanics(t *testing.T) {
	t.Parallel()

	var m Map[int, int]
	m.init()

	defer func() {
		if r := recover(); r != panicHashBitsInsert {
			t.Fatalf("recovered %v, want %q", r, panicHashBitsInsert)
		}
	}()

	// Two entries with different hashes but no hash bits left to tell them apart.
	m.expand(newEntryNode(1, 1), newEntryNode(2, 2), m.hash(2), 0, m.root.Load())
}

func TestPruneEmptyRanOutOfHashBitsPanics(t *testing.T) {
	t.Parallel()

	var m Map[int, int]
	m.init()

	defer func() {
		if r := recover(); r != panicHashBitsIterate {
			t.Fatalf("recovered %v, want %q", r, panicHashBitsIterate)
		}
	}()

	// An empty node with a parent at the top level cannot exist; pruning it must panic.
	i := newIndirectNode(m.root.Load())
	i.mu.Lock()
	m.pruneEmpty(i, 0, hashBits)
}

func TestEntryLoadAndDeleteMissing(t *testing.T) {
	t.Parallel()

	head := newEntryNode(1, 10)
	head.overflow.Store(newEntryNode(2, 20))

	v, chain, loaded := head.loadAndDelete(3)
	if loaded || v != 0 || chain != head {
		t.Fatalf("loadAndDelete(missing) = %v, %p, %v; want 0, head, false", v, chain, loaded)
	}
}
