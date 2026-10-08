// Package gosync provides Map, a generic concurrent map with the semantics of
// [sync.Map] and without its cost: no interface boxing of keys and values and no
// type assertions on the way out.
//
// Map is the hash-trie behind sync.Map since Go 1.24 (internal/sync.HashTrieMap),
// ported to user code: it hashes keys with [hash/maphash.Comparable] and locks
// with [sync.Mutex]. Loads are lock-free and never allocate; a store allocates
// one node for the entry, which readers may see immediately.
//
// Map fits the same workloads as sync.Map: caches and registries that are read
// far more often than written, or where disjoint sets of goroutines write
// disjoint keys. For write-heavy, high-churn maps see github.com/darxnet/goshard.
//
// [ComparableMap] adds CompareAndSwap and CompareAndDelete by == for comparable
// values; Map offers them with an equality function.
package gosync
