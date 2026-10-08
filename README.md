# gosync

[![Go Reference](https://pkg.go.dev/badge/github.com/darxnet/gosync.svg)](https://pkg.go.dev/github.com/darxnet/gosync)
[![Go](https://github.com/darxnet/gosync/actions/workflows/release.yml/badge.svg)](https://github.com/darxnet/gosync/actions/workflows/release.yml)
![Coverage](https://img.shields.io/badge/Coverage-99%25-brightgreen)

`gosync.Map` is a generic, concurrent map with the semantics of `sync.Map` — and
without its cost: keys and values are stored as they are, so there is no
interface boxing on the way in and no type assertion on the way out.

It is the hash-trie behind `sync.Map` since Go 1.24 (`internal/sync.HashTrieMap`),
ported to user code and made generic.

## Why gosync?

The usual way to get a typed `sync.Map` is a thin generic wrapper:

```go
type Map[K comparable, V any] struct{ m sync.Map }

func (m *Map[K, V]) Load(key K) (V, bool) {
    v, ok := m.m.Load(key)   // key boxed into an interface
    return v.(V), ok         // value asserted back
}
```

Every call pays for the boxing, the type assertion and the dynamic type checks
inside `sync.Map`. `gosync.Map` removes all of it: the trie stores `K` and `V`
directly and hashes keys with `hash/maphash.Comparable`.

| | `gosync.Map[K, V]` | wrapper over `sync.Map` |
|---|---|---|
| **Type safety** | compile time | runtime assertions |
| **Load / LoadOrStore (hit)** | lock-free, **0 allocs** | lock-free, 0 allocs, boxing on every call |
| **Store (existing key)** | 1 alloc (new entry node) | 1–2 allocs (entry + boxed key) |
| **Iteration** | `All()` for `range`, `Range()` | `Range()` with `any` |
| **Values** | any type; `==` via `ComparableMap` | `any` |

## When to use it

The same workloads where `sync.Map` is the right tool:

- caches and registries that are **read far more often than written**;
- maps where **disjoint goroutines write disjoint keys**.

For write-heavy, high-churn maps (frequent stores, deletes, TTL evictions)
use [goshard](https://github.com/darxnet/goshard): a sharded, lock-based map
that allocates nothing on stores.

## Features

- **Drop-in for `sync.Map`** — `Load`, `Store`, `LoadOrStore`, `LoadAndDelete`,
  `Delete`, `Swap`, `CompareAndSwap`, `CompareAndDelete`, `Range`, `Clear`,
  with the same semantics and the same concurrency guarantees.
- **Zero-allocation reads** — `Load`, `LoadOrStore` of an existing key,
  `Delete` of a missing key, `Range`, `Len` and `Empty` never allocate.
- **Go 1.23+ iteration** — `for k, v := range m.All()`.
- **`Len` and `Empty`** — `Empty` stops at the first entry; `Len` walks the map.
- **`ComparableMap`** — `CompareAndSwap` and `CompareAndDelete` by `==`;
  `Map` offers them with an equality function, so values need not be comparable.
- **Reads of a zero map are free** — a `Map` that was never written is not
  initialized by `Load`, `Range` or `Delete`.
- **Zero dependencies** — only the standard library.

## Installation

```bash
go get github.com/darxnet/gosync
```

Requires Go 1.24 (`hash/maphash.Comparable`).

## Quick Start

```go
// The zero value is ready to use.
var m gosync.Map[string, *Session]

m.Store("sid-1", s)

if s, ok := m.Load("sid-1"); ok {
    fmt.Println(s.User)
}

// Get-or-create.
s, loaded := m.LoadOrStore("sid-2", newSession())

// Iterate.
for id, s := range m.All() {
    fmt.Println(id, s.User)
}

// Compare-and-swap by == needs comparable values.
var status gosync.ComparableMap[string, string]
status.Store("worker-1", "idle")
status.CompareAndSwap("worker-1", "idle", "busy") // true

// ...or by an equality function on any value type.
var versions gosync.Map[string, []byte]
versions.CompareAndSwap("cfg", old, new, bytes.Equal)
```

## Benchmarks

> **Environment:** Apple M3 Pro · darwin/arm64 · Go 1.26
> `go test -bench=. -benchmem -count=6 -cpu=1,4,8 ./...`, summarised with `benchstat`.
> Maps hold 16K entries with `uint32` keys and pointer values unless noted.
> `sync.Map wrapper` is the generic wrapper from the section above; `RWMutex map` is a plain map behind `sync.RWMutex`.

### Load (existing keys)

| Implementation | 1 CPU | 4 CPUs | 8 CPUs | Allocs/op |
|---|---|---|---|---|
| **`gosync.Map`** | 14.1 ns | **3.9 ns** | **2.2 ns** | **0** |
| `sync.Map` wrapper | 17.5 ns | 4.8 ns | 2.7 ns | 0 |
| `RWMutex` map | **9.0 ns** | 33.7 ns | 91.8 ns | 0 |

`gosync.Map` is **19% faster** than the wrapper on every CPU count: that is the boxing and the type
assertion. String keys show the same gap (16.4 vs 20.0 ns at 1 CPU); a map of 128K string keys loads
in 20.2 vs 24.9 ns.

### LoadOrStore (existing keys, as a warm cache)

| Implementation | 1 CPU | 4 CPUs | 8 CPUs | Allocs/op |
|---|---|---|---|---|
| **`gosync.Map`** | **15.1 ns** | **4.2 ns** | **2.3 ns** | **0** |
| `sync.Map` wrapper | 28.0 ns | 7.8 ns | 4.6 ns | 0 (3 B/op) |
| `RWMutex` map | 15.9 ns | 100.7 ns | 120.4 ns | 0 |

The wrapper boxes both the key and the value on every call, so `LoadOrStore` costs **2x** there.

### Store (existing keys) and Insert (new keys)

| Implementation | Store 1 CPU | Store 8 CPUs | Store allocs | Insert | Insert allocs |
|---|---|---|---|---|---|
| **`gosync.Map`** | 47.7 ns | **13.9 ns** | 1 (32 B) | 260 ns | **1 (85 B)** |
| `sync.Map` wrapper | 64.5 ns | 19.2 ns | 1 (51 B) | 338 ns | 2 (105 B) |
| `RWMutex` map | **12.7 ns** | 129.8 ns | **0** | **119 ns** | 0 (72 B) |

A store publishes a new entry node, so that lock-free readers never see a torn value: that is the one
allocation, the same as in `sync.Map`. The wrapper adds the boxed key on top of it.

### Write-Read-Delete cycle (high churn)

| Implementation | 1 CPU | 4 CPUs | 8 CPUs | Allocs/op |
|---|---|---|---|---|
| **`gosync.Map`** | 39.1 ns | 179 ns | 207 ns | 1 |
| `sync.Map` wrapper | 57.8 ns | 200 ns | 229 ns | 1-2 |
| `RWMutex` map | **30.2 ns** | **163 ns** | **185 ns** | **0** |

This is the workload `sync.Map` is not made for, and `gosync.Map` inherits that: a plain mutex map
wins, and [goshard](https://github.com/darxnet/goshard) wins by far more. Use `gosync.Map` for
read-mostly maps.

### Iteration

`Range` over 16K entries takes about 0.2 ms for both trie maps and 0.1 ms for a plain map; `Range`,
`Len` and `Empty` allocate nothing.

## License

Apache License 2.0. See [LICENSE](LICENSE) for details.

`map.go`, `map_test.go` and `export_test.go` are derived from the Go standard
library (`src/internal/sync/hashtriemap.go` and its tests), Copyright 2024 The
Go Authors, under the BSD 3-Clause License — see [LICENSE-GO](LICENSE-GO) and
[NOTICE](NOTICE).
