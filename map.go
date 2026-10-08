// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO file.
//
// Modifications Copyright 2026 Yevhen Zabrodskyi.
// Licensed under the Apache License, Version 2.0 (see LICENSE).

// This file is derived from Go's src/internal/sync/hashtriemap.go, the
// implementation behind sync.Map since Go 1.24. What changed:
//
//   - the map is exported and generic, so keys and values are stored as they
//     are, without interface boxing and without type assertions on the way out;
//   - keys are hashed with hash/maphash.Comparable instead of the runtime's
//     unexported type hasher;
//   - the runtime's internal mutex is replaced by sync.Mutex;
//   - CompareAndSwap and CompareAndDelete take an equality function on Map and
//     use == on ComparableMap, instead of the runtime's type equality function;
//   - reads of a map that was never written return without initializing it;
//   - Len and Empty are added.

package gosync

import (
	"hash/maphash"
	"iter"
	"sync"
	"sync/atomic"
	"unsafe"
)

// Map is a concurrent hash-trie with the semantics of [sync.Map]: it is safe
// for use by multiple goroutines without additional locking, loads are
// lock-free, and the iteration order is unspecified.
//
// Map is designed around frequent loads, but offers decent performance for
// stores and deletes as well, especially if the map is larger.
//
// The zero Map is empty and ready to use.
// It must not be copied after first use.
type Map[K comparable, V any] struct {
	inited atomic.Uint32
	initMu sync.Mutex
	root   atomic.Pointer[indirect[K, V]]
	seed   maphash.Seed
	// hashMask is applied to every key hash. It is all ones, except in tests
	// that truncate the hash to force collisions.
	hashMask uint64
}

// ComparableMap is a Map whose values are comparable, so CompareAndSwap and
// CompareAndDelete can compare them with == instead of an equality function.
type ComparableMap[K comparable, V comparable] struct {
	Map[K, V]
}

const (
	// hashBits is the number of bits of a key hash: maphash.Comparable returns uint64.
	hashBits = 64

	// 16 children. This seems to be the sweet spot for
	// load performance: any smaller and we lose out on
	// 50% or more in CPU performance. Any larger and the
	// returns are minuscule (~1% improvement for 32 children).
	nChildrenLog2 = 4
	nChildren     = 1 << nChildrenLog2
	nChildrenMask = nChildren - 1

	panicHashBitsIterate = "gosync: ran out of hash bits while iterating"
	panicHashBitsInsert  = "gosync: ran out of hash bits while inserting (incorrect use of unsafe or cgo, or data race?)"
)

func (m *Map[K, V]) init() {
	if m.inited.Load() == 0 {
		m.initSlow()
	}
}

//go:noinline
func (m *Map[K, V]) initSlow() {
	m.initMu.Lock()
	defer m.initMu.Unlock()

	if m.inited.Load() != 0 {
		// Someone got to it while we were waiting.
		return
	}

	// Set up the root node and the hash seed.
	m.root.Store(newIndirectNode[K, V](nil))
	m.seed = maphash.MakeSeed()
	m.hashMask = ^uint64(0)

	m.inited.Store(1)
}

func (m *Map[K, V]) hash(key K) uint64 {
	return maphash.Comparable(m.seed, key) & m.hashMask
}

// Load returns the value stored in the map for a key, or the zero value if no
// value is present.
// The ok result indicates whether value was found in the map.
func (m *Map[K, V]) Load(key K) (value V, ok bool) {
	if m.inited.Load() == 0 {
		return value, false
	}
	hash := m.hash(key)

	i := m.root.Load()
	hashShift := uint(hashBits)
	for hashShift != 0 {
		hashShift -= nChildrenLog2

		n := i.children[(hash>>hashShift)&nChildrenMask].Load()
		if n == nil {
			return value, false
		}
		if n.isEntry {
			return n.entry().lookup(key)
		}
		i = n.indirect()
	}
	panic(panicHashBitsIterate)
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *Map[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	m.init()
	hash := m.hash(key)
	var i *indirect[K, V]
	var hashShift uint
	var slot *atomic.Pointer[node[K, V]]
	var n *node[K, V]
	for {
		// Find the key or a candidate location for insertion.
		i = m.root.Load()
		hashShift = hashBits
		haveInsertPoint := false
		for hashShift != 0 {
			hashShift -= nChildrenLog2

			slot = &i.children[(hash>>hashShift)&nChildrenMask]
			n = slot.Load()
			if n == nil {
				// We found a nil slot which is a candidate for insertion.
				haveInsertPoint = true
				break
			}
			if n.isEntry {
				// We found an existing entry, which is as far as we can go.
				// If it stays this way, we'll have to replace it with an
				// indirect node.
				if v, ok := n.entry().lookup(key); ok {
					return v, true
				}
				haveInsertPoint = true
				break
			}
			i = n.indirect()
		}
		if !haveInsertPoint {
			panic(panicHashBitsIterate)
		}

		// Grab the lock and double-check what we saw.
		i.mu.Lock()
		n = slot.Load()
		if (n == nil || n.isEntry) && !i.dead.Load() {
			// What we saw is still true, so we can continue with the insert.
			break
		}
		// We have to start over.
		i.mu.Unlock()
	}
	// N.B. This lock is held from when we broke out of the outer loop above.
	// We specifically break this out so that we can use defer here safely.
	// One option is to break this out into a new function instead, but
	// there's so much local iteration state used below that this turns out
	// to be cleaner.
	defer i.mu.Unlock()

	var oldEntry *entry[K, V]
	if n != nil {
		oldEntry = n.entry()
		if v, ok := oldEntry.lookup(key); ok {
			// Easy case: by loading again, it turns out exactly what we wanted is here!
			return v, true
		}
	}
	newEntry := newEntryNode(key, value)
	if oldEntry == nil {
		// Easy case: create a new entry and store it.
		slot.Store(&newEntry.node)
	} else {
		// We possibly need to expand the entry already there into one or more new nodes.
		//
		// Publish the node last, which will make both oldEntry and newEntry visible. We
		// don't want readers to be able to observe that oldEntry isn't in the tree.
		slot.Store(m.expand(oldEntry, newEntry, hash, hashShift, i))
	}
	return value, false
}

// expand takes oldEntry and newEntry whose hashes conflict from bit 64 down to hashShift and
// produces a subtree of indirect nodes to hold the two new entries.
func (m *Map[K, V]) expand(
	oldEntry, newEntry *entry[K, V], newHash uint64, hashShift uint, parent *indirect[K, V],
) *node[K, V] {
	// Check for a hash collision.
	oldHash := m.hash(oldEntry.key)
	if oldHash == newHash {
		// Store the old entry in the new entry's overflow list, then store
		// the new entry.
		newEntry.overflow.Store(oldEntry)
		return &newEntry.node
	}
	// We have to add an indirect node. Worse still, we may need to add more than one.
	newIndirect := newIndirectNode(parent)
	top := newIndirect
	for {
		if hashShift == 0 {
			panic(panicHashBitsInsert)
		}
		hashShift -= nChildrenLog2 // hashShift is for the level parent is at. We need to go deeper.
		oi := (oldHash >> hashShift) & nChildrenMask
		ni := (newHash >> hashShift) & nChildrenMask
		if oi != ni {
			newIndirect.children[oi].Store(&oldEntry.node)
			newIndirect.children[ni].Store(&newEntry.node)
			break
		}
		nextIndirect := newIndirectNode(newIndirect)
		newIndirect.children[oi].Store(&nextIndirect.node)
		newIndirect = nextIndirect
	}
	return &top.node
}

// Store sets the value for a key.
func (m *Map[K, V]) Store(key K, value V) {
	_, _ = m.Swap(key, value)
}

// Swap swaps the value for a key and returns the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map[K, V]) Swap(key K, value V) (previous V, loaded bool) {
	m.init()
	hash := m.hash(key)
	var i *indirect[K, V]
	var hashShift uint
	var slot *atomic.Pointer[node[K, V]]
	var n *node[K, V]
	for {
		// Find the key or a candidate location for insertion.
		i = m.root.Load()
		hashShift = hashBits
		haveInsertPoint := false
		for hashShift != 0 {
			hashShift -= nChildrenLog2

			slot = &i.children[(hash>>hashShift)&nChildrenMask]
			n = slot.Load()
			if n == nil || n.isEntry {
				// We found a nil slot which is a candidate for insertion,
				// or an existing entry that we'll replace.
				haveInsertPoint = true
				break
			}
			i = n.indirect()
		}
		if !haveInsertPoint {
			panic(panicHashBitsIterate)
		}

		// Grab the lock and double-check what we saw.
		i.mu.Lock()
		n = slot.Load()
		if (n == nil || n.isEntry) && !i.dead.Load() {
			// What we saw is still true, so we can continue with the insert.
			break
		}
		// We have to start over.
		i.mu.Unlock()
	}
	// N.B. This lock is held from when we broke out of the outer loop above.
	// We specifically break this out so that we can use defer here safely.
	// One option is to break this out into a new function instead, but
	// there's so much local iteration state used below that this turns out
	// to be cleaner.
	defer i.mu.Unlock()

	var oldEntry *entry[K, V]
	if n != nil {
		// Swap if the keys compare.
		oldEntry = n.entry()
		newEntry, old, swapped := oldEntry.swap(key, value)
		if swapped {
			slot.Store(&newEntry.node)
			return old, true
		}
	}
	// The keys didn't compare, so we're doing an insertion.
	newEntry := newEntryNode(key, value)
	if oldEntry == nil {
		// Easy case: create a new entry and store it.
		slot.Store(&newEntry.node)
	} else {
		// We possibly need to expand the entry already there into one or more new nodes.
		//
		// Publish the node last, which will make both oldEntry and newEntry visible. We
		// don't want readers to be able to observe that oldEntry isn't in the tree.
		slot.Store(m.expand(oldEntry, newEntry, hash, hashShift, i))
	}
	return previous, false
}

// CompareAndSwap swaps the old and new values for key if the value stored in
// the map is equal to old according to eq. The eq function is called while the
// node of the key is locked.
//
// If there is no current value for key in the map, CompareAndSwap returns false.
func (m *Map[K, V]) CompareAndSwap(key K, old, new V, eq func(current, old V) bool) (swapped bool) {
	if eq == nil {
		panic("gosync: nil comparator")
	}
	if m.inited.Load() == 0 {
		return false
	}
	hash := m.hash(key)

	// Find a node with the key and compare with it. n != nil if we found the node.
	i, _, slot, n := m.find(key, hash, eq, old)
	if i != nil {
		defer i.mu.Unlock()
	}
	if n == nil {
		return false
	}

	// Try to swap the entry.
	e, swapped := n.entry().compareAndSwap(key, old, new, eq)
	if !swapped {
		// Nothing was actually swapped, which means the node is no longer there.
		return false
	}
	// Store the entry back because it changed.
	slot.Store(&e.node)
	return true
}

// CompareAndSwap swaps the old and new values for key
// if the value stored in the map is equal to old.
//
// If there is no current value for key in the map, CompareAndSwap returns false.
func (m *ComparableMap[K, V]) CompareAndSwap(key K, old, new V) (swapped bool) {
	return m.Map.CompareAndSwap(key, old, new, equal[V])
}

// LoadAndDelete deletes the value for a key, returning the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	if m.inited.Load() == 0 {
		return value, false
	}
	hash := m.hash(key)

	// Find a node with the key and compare with it. n != nil if we found the node.
	i, hashShift, slot, n := m.find(key, hash, nil, value)
	if n == nil {
		if i != nil {
			i.mu.Unlock()
		}
		return value, false
	}

	// Try to delete the entry.
	v, e, loaded := n.entry().loadAndDelete(key)
	if !loaded {
		// Nothing was actually deleted, which means the node is no longer there.
		i.mu.Unlock()
		return value, false
	}
	if e != nil {
		// We didn't actually delete the whole entry, just one entry in the chain.
		// Nothing else to do, since the parent is definitely not empty.
		slot.Store(&e.node)
		i.mu.Unlock()
		return v, true
	}
	// Delete the entry.
	slot.Store(nil)

	// Check if the node is now empty (and isn't the root), and delete it if able.
	m.pruneEmpty(i, hash, hashShift)
	return v, true
}

// Delete deletes the value for a key.
func (m *Map[K, V]) Delete(key K) {
	_, _ = m.LoadAndDelete(key)
}

// CompareAndDelete deletes the entry for key if its value is equal to old
// according to eq. The eq function is called while the node of the key is locked.
//
// If there is no current value for key in the map, CompareAndDelete returns false.
func (m *Map[K, V]) CompareAndDelete(key K, old V, eq func(current, old V) bool) (deleted bool) {
	if eq == nil {
		panic("gosync: nil comparator")
	}
	if m.inited.Load() == 0 {
		return false
	}
	hash := m.hash(key)

	// Find a node with the key. n != nil if we found the node.
	i, hashShift, slot, n := m.find(key, hash, nil, old)
	if n == nil {
		if i != nil {
			i.mu.Unlock()
		}
		return false
	}

	// Try to delete the entry.
	e, deleted := n.entry().compareAndDelete(key, old, eq)
	if !deleted {
		// Nothing was actually deleted, which means the node is no longer there.
		i.mu.Unlock()
		return false
	}
	if e != nil {
		// We didn't actually delete the whole entry, just one entry in the chain.
		// Nothing else to do, since the parent is definitely not empty.
		slot.Store(&e.node)
		i.mu.Unlock()
		return true
	}
	// Delete the entry.
	slot.Store(nil)

	// Check if the node is now empty (and isn't the root), and delete it if able.
	m.pruneEmpty(i, hash, hashShift)
	return true
}

// CompareAndDelete deletes the entry for key if its value is equal to old.
//
// If there is no current value for key in the map, CompareAndDelete returns false.
func (m *ComparableMap[K, V]) CompareAndDelete(key K, old V) (deleted bool) {
	return m.Map.CompareAndDelete(key, old, equal[V])
}

// pruneEmpty unlinks i from its parent while i is empty and not the root, going
// up the path of hash. i.mu must be locked; pruneEmpty unlocks it.
func (m *Map[K, V]) pruneEmpty(i *indirect[K, V], hash uint64, hashShift uint) {
	for i.parent != nil && i.empty() {
		if hashShift == hashBits {
			panic(panicHashBitsIterate)
		}
		hashShift += nChildrenLog2

		// Delete the current node in the parent.
		parent := i.parent
		parent.mu.Lock()
		i.dead.Store(true)
		parent.children[(hash>>hashShift)&nChildrenMask].Store(nil)
		i.mu.Unlock()
		i = parent
	}
	i.mu.Unlock()
}

// find searches the tree for a node that contains key (hash must be the hash of key).
// If eq != nil, then it will also enforce that the values are equal as well.
//
// Returns a non-nil node, which will always be an entry, if found.
//
// If i != nil then i.mu is locked, and it is the caller's responsibility to unlock it.
func (m *Map[K, V]) find(
	key K, hash uint64, eq func(current, old V) bool, value V,
) (i *indirect[K, V], hashShift uint, slot *atomic.Pointer[node[K, V]], n *node[K, V]) {
	for {
		// Find the key or return if it's not there.
		i = m.root.Load()
		hashShift = hashBits
		found := false
		for hashShift != 0 {
			hashShift -= nChildrenLog2

			slot = &i.children[(hash>>hashShift)&nChildrenMask]
			n = slot.Load()
			if n == nil {
				// Nothing to compare with. Give up.
				return nil, hashShift, slot, nil
			}
			if n.isEntry {
				// We found an entry. Check if it matches.
				if _, ok := n.entry().lookupWithValue(key, value, eq); !ok {
					// No match, comparison failed.
					return nil, hashShift, slot, nil
				}
				// We've got a match. Prepare to perform an operation on the key.
				found = true
				break
			}
			i = n.indirect()
		}
		if !found {
			panic(panicHashBitsIterate)
		}

		// Grab the lock and double-check what we saw.
		i.mu.Lock()
		n = slot.Load()
		if !i.dead.Load() && (n == nil || n.isEntry) {
			// Either we've got a valid node or the node is now nil under the lock.
			// In either case, we're done here.
			return i, hashShift, slot, n
		}
		// We have to start over.
		i.mu.Unlock()
	}
}

// All returns an iterator over each key and value present in the map.
//
// The iterator does not necessarily correspond to any consistent snapshot of the
// Map's contents: no key will be visited more than once, but if the value
// for any key is stored or deleted concurrently (including by yield), the iterator
// may reflect any mapping for that key from any point during iteration. The iterator
// does not block other methods on the receiver; even yield itself may call any
// method on the Map.
func (m *Map[K, V]) All() iter.Seq2[K, V] {
	return m.Range
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
//
// This exists for compatibility with sync.Map; All should be preferred.
// It provides the same guarantees as sync.Map, and All.
func (m *Map[K, V]) Range(f func(K, V) bool) {
	if m.inited.Load() == 0 {
		return
	}
	m.iter(m.root.Load(), f)
}

func (m *Map[K, V]) iter(i *indirect[K, V], yield func(key K, value V) bool) bool {
	for j := range i.children {
		n := i.children[j].Load()
		if n == nil {
			continue
		}
		if !n.isEntry {
			if !m.iter(n.indirect(), yield) {
				return false
			}
			continue
		}
		e := n.entry()
		for e != nil {
			if !yield(e.key, e.value) {
				return false
			}
			e = e.overflow.Load()
		}
	}
	return true
}

// Clear deletes all the entries, resulting in an empty Map.
func (m *Map[K, V]) Clear() {
	if m.inited.Load() == 0 {
		return
	}

	// It's sufficient to just drop the root on the floor, but the root
	// must always be non-nil.
	m.root.Store(newIndirectNode[K, V](nil))
}

// Len returns the number of entries in the map. It walks the whole map, with
// the same consistency as Range: entries stored or deleted concurrently may or
// may not be counted.
func (m *Map[K, V]) Len() int {
	n := 0
	m.Range(func(K, V) bool {
		n++
		return true
	})
	return n
}

// Empty reports whether the map has no entries. It stops at the first entry
// found, so it is cheap on a non-empty map.
func (m *Map[K, V]) Empty() bool {
	empty := true
	m.Range(func(K, V) bool {
		empty = false
		return false
	})
	return empty
}

// equal is the == comparator used by ComparableMap.
func equal[V comparable](a, b V) bool {
	return a == b
}

// indirect is an internal node in the hash-trie.
type indirect[K comparable, V any] struct {
	node[K, V]

	dead     atomic.Bool
	mu       sync.Mutex // Protects mutation to children and any children that are entry nodes.
	parent   *indirect[K, V]
	children [nChildren]atomic.Pointer[node[K, V]]
}

func newIndirectNode[K comparable, V any](parent *indirect[K, V]) *indirect[K, V] {
	return &indirect[K, V]{node: node[K, V]{isEntry: false}, parent: parent}
}

func (i *indirect[K, V]) empty() bool {
	for j := range i.children {
		if i.children[j].Load() != nil {
			return false
		}
	}
	return true
}

// entry is a leaf node in the hash-trie.
type entry[K comparable, V any] struct {
	node[K, V]

	overflow atomic.Pointer[entry[K, V]] // Overflow for hash collisions.
	key      K
	value    V
}

func newEntryNode[K comparable, V any](key K, value V) *entry[K, V] {
	return &entry[K, V]{
		node:  node[K, V]{isEntry: true},
		key:   key,
		value: value,
	}
}

func (e *entry[K, V]) lookup(key K) (value V, ok bool) {
	for e != nil {
		if e.key == key {
			return e.value, true
		}
		e = e.overflow.Load()
	}
	return value, false
}

func (e *entry[K, V]) lookupWithValue(key K, value V, eq func(current, old V) bool) (found V, ok bool) {
	for e != nil {
		if e.key == key && (eq == nil || eq(e.value, value)) {
			return e.value, true
		}
		e = e.overflow.Load()
	}
	return found, false
}

// swap replaces an entry in the overflow chain if keys compare equal. Returns the new entry chain,
// the old value, and whether or not anything was swapped.
//
// swap must be called under the mutex of the indirect node which e is a child of.
func (head *entry[K, V]) swap(key K, new V) (*entry[K, V], V, bool) {
	if head.key == key {
		// Return the new head of the list.
		e := newEntryNode(key, new)
		if chain := head.overflow.Load(); chain != nil {
			e.overflow.Store(chain)
		}
		return e, head.value, true
	}
	i := &head.overflow
	e := i.Load()
	for e != nil {
		if e.key == key {
			eNew := newEntryNode(key, new)
			eNew.overflow.Store(e.overflow.Load())
			i.Store(eNew)
			return head, e.value, true
		}
		i = &e.overflow
		e = e.overflow.Load()
	}
	var zero V
	return head, zero, false
}

// compareAndSwap replaces an entry in the overflow chain if both the key and value compare
// equal. Returns the new entry chain and whether or not anything was swapped.
//
// compareAndSwap must be called under the mutex of the indirect node which e is a child of.
func (head *entry[K, V]) compareAndSwap(key K, old, new V, eq func(current, old V) bool) (*entry[K, V], bool) {
	if head.key == key && eq(head.value, old) {
		// Return the new head of the list.
		e := newEntryNode(key, new)
		if chain := head.overflow.Load(); chain != nil {
			e.overflow.Store(chain)
		}
		return e, true
	}
	i := &head.overflow
	e := i.Load()
	for e != nil {
		if e.key == key && eq(e.value, old) {
			eNew := newEntryNode(key, new)
			eNew.overflow.Store(e.overflow.Load())
			i.Store(eNew)
			return head, true
		}
		i = &e.overflow
		e = e.overflow.Load()
	}
	return head, false
}

// loadAndDelete deletes an entry in the overflow chain by key. Returns the value for the key, the new
// entry chain and whether or not anything was loaded (and deleted).
//
// loadAndDelete must be called under the mutex of the indirect node which e is a child of.
func (head *entry[K, V]) loadAndDelete(key K) (value V, chain *entry[K, V], loaded bool) {
	if head.key == key {
		// Drop the head of the list.
		return head.value, head.overflow.Load(), true
	}
	i := &head.overflow
	e := i.Load()
	for e != nil {
		if e.key == key {
			i.Store(e.overflow.Load())
			return e.value, head, true
		}
		i = &e.overflow
		e = e.overflow.Load()
	}
	return value, head, false
}

// compareAndDelete deletes an entry in the overflow chain if both the key and value compare
// equal. Returns the new entry chain and whether or not anything was deleted.
//
// compareAndDelete must be called under the mutex of the indirect node which e is a child of.
func (head *entry[K, V]) compareAndDelete(key K, value V, eq func(current, old V) bool) (*entry[K, V], bool) {
	if head.key == key && eq(head.value, value) {
		// Drop the head of the list.
		return head.overflow.Load(), true
	}
	i := &head.overflow
	e := i.Load()
	for e != nil {
		if e.key == key && eq(e.value, value) {
			i.Store(e.overflow.Load())
			return head, true
		}
		i = &e.overflow
		e = e.overflow.Load()
	}
	return head, false
}

// node is the header for a node. It's polymorphic and
// is actually either an entry or an indirect.
type node[K comparable, V any] struct {
	isEntry bool
}

func (n *node[K, V]) entry() *entry[K, V] {
	if !n.isEntry {
		panic("gosync: called entry on non-entry node")
	}
	return (*entry[K, V])(unsafe.Pointer(n)) //nolint:gosec // G103 node is the first field of entry
}

func (n *node[K, V]) indirect() *indirect[K, V] {
	if n.isEntry {
		panic("gosync: called indirect on entry node")
	}
	return (*indirect[K, V])(unsafe.Pointer(n)) //nolint:gosec // G103 node is the first field of indirect
}
