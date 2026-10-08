// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-GO file.
//
// Modifications Copyright 2026 Yevhen Zabrodskyi.
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package gosync

// NewBadMap creates a Map for the provided key and value but with an
// intentionally bad hash function: every key hashes to 0, so all entries
// share one overflow chain. Everything should still work as expected.
func NewBadMap[K, V comparable]() *ComparableMap[K, V] {
	var m ComparableMap[K, V]
	m.init()
	m.hashMask = 0
	return &m
}

// NewTruncMap creates a Map for the provided key and value but with an
// intentionally bad hash function: only the lowest 4 bits of the hash are
// kept. This is useful to test independently to catch issues with near
// collisions, where only the last few bits of the hash differ.
func NewTruncMap[K, V comparable]() *ComparableMap[K, V] {
	var m ComparableMap[K, V]
	m.init()
	m.hashMask = nChildrenMask
	return &m
}
