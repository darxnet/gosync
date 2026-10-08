package gosync_test

import (
	"fmt"

	"github.com/darxnet/gosync"
)

// ExampleMap demonstrates basic store, load, and delete operations.
// The zero value is ready to use; no constructor call is required.
func ExampleMap() {
	var m gosync.Map[string, int]

	m.Store("active_users", 1024)

	if v, ok := m.Load("active_users"); ok {
		fmt.Println(v)
	}

	m.Delete("active_users")

	if _, ok := m.Load("active_users"); !ok {
		fmt.Println("gone")
	}

	// Output:
	// 1024
	// gone
}

// ExampleMap_loadOrStore demonstrates get-or-set semantics: the first
// call stores the value, the second returns the existing one.
func ExampleMap_loadOrStore() {
	var m gosync.Map[string, int]

	actual, loaded := m.LoadOrStore("limit", 100)
	fmt.Println(actual, loaded) // stored

	actual, loaded = m.LoadOrStore("limit", 999)
	fmt.Println(actual, loaded) // existing value returned

	// Output:
	// 100 false
	// 100 true
}

// ExampleMap_All iterates with a range-over-func loop.
func ExampleMap_All() {
	var m gosync.Map[int, string]
	m.Store(1, "one")

	for k, v := range m.All() {
		fmt.Println(k, v)
	}

	// Output:
	// 1 one
}

// ExampleMap_compareAndSwap compares values with a function, which lets a
// Map hold values that are not comparable.
func ExampleMap_compareAndSwap() {
	var m gosync.Map[string, []int]
	m.Store("ids", []int{1, 2})

	sameLen := func(current, old []int) bool { return len(current) == len(old) }

	fmt.Println(m.CompareAndSwap("ids", []int{0, 0}, []int{1, 2, 3}, sameLen))
	fmt.Println(m.CompareAndSwap("ids", []int{0, 0}, []int{9}, sameLen))

	// Output:
	// true
	// false
}

// ExampleComparableMap_compareAndSwap demonstrates compare-and-swap by == .
func ExampleComparableMap_compareAndSwap() {
	var m gosync.ComparableMap[string, string]
	m.Store("status", "idle")

	// Succeeds: current value matches "idle".
	fmt.Println(m.CompareAndSwap("status", "idle", "busy"))

	// Fails: current value is "busy", not "idle".
	fmt.Println(m.CompareAndSwap("status", "idle", "busy"))

	// Output:
	// true
	// false
}

// ExampleComparableMap_compareAndDelete deletes a key only when its value
// matches the expected one.
func ExampleComparableMap_compareAndDelete() {
	var m gosync.ComparableMap[string, int]
	m.Store("session", 42)

	fmt.Println(m.CompareAndDelete("session", 0))  // wrong value — no-op
	fmt.Println(m.CompareAndDelete("session", 42)) // matches — deleted

	_, ok := m.Load("session")
	fmt.Println(ok)

	// Output:
	// false
	// true
	// false
}
