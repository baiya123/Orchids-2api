// Package perf provides performance optimization utilities including object pools.
package perf

import (
	"strings"
	"sync"
)

// StringBuilderPool provides reusable strings.Builder instances.
// Usage:
//
//	sb := perf.AcquireStringBuilder()
//	defer perf.ReleaseStringBuilder(sb)
//	sb.WriteString("hello")
var StringBuilderPool = sync.Pool{
	New: func() interface{} {
		return &strings.Builder{}
	},
}

// AcquireStringBuilder gets a strings.Builder from the pool.
func AcquireStringBuilder() *strings.Builder {
	return StringBuilderPool.Get().(*strings.Builder)
}

// ReleaseStringBuilder returns a strings.Builder to the pool after resetting it.
func ReleaseStringBuilder(sb *strings.Builder) {
	if sb == nil {
		return
	}
	sb.Reset()
	StringBuilderPool.Put(sb)
}

// MapPool provides reusable map[string]interface{} instances.
// Note: Maps must be cleared before returning to pool.
var MapPool = sync.Pool{
	New: func() interface{} {
		return make(map[string]interface{}, 16)
	},
}

// AcquireMap gets a map from the pool.
func AcquireMap() map[string]interface{} {
	return MapPool.Get().(map[string]interface{})
}

// ReleaseMap clears and returns a map to the pool.
func ReleaseMap(m map[string]interface{}) {
	if m == nil {
		return
	}
	// Clear map before returning
	for k := range m {
		delete(m, k)
	}
	MapPool.Put(m)
}

// ByteSlicePool provides reusable byte slices with default capacity of 4KB.
var ByteSlicePool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 0, 4096)
		return &b
	},
}

// AcquireByteSlice gets a byte slice from the pool.
func AcquireByteSlice() *[]byte {
	return ByteSlicePool.Get().(*[]byte)
}

// ReleaseByteSlice returns a byte slice to the pool after resetting length.
func ReleaseByteSlice(b *[]byte) {
	if b == nil {
		return
	}
	*b = (*b)[:0]
	ByteSlicePool.Put(b)
}

// StringSlicePool provides reusable []string slices.
var StringSlicePool = sync.Pool{
	New: func() interface{} {
		s := make([]string, 0, 16)
		return &s
	},
}

// AcquireStringSlice gets a string slice from the pool.
func AcquireStringSlice() *[]string {
	return StringSlicePool.Get().(*[]string)
}

// ReleaseStringSlice returns a string slice to the pool after resetting.
func ReleaseStringSlice(s *[]string) {
	if s == nil {
		return
	}
	*s = (*s)[:0]
	StringSlicePool.Put(s)
}
