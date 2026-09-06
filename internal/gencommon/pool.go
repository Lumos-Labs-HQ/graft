package gencommon

import (
	"strings"
	"sync"
)

// StringBuilderPool provides pooled strings.Builder instances to reduce allocations
var StringBuilderPool = sync.Pool{
	New: func() any {
		b := new(strings.Builder)
		b.Grow(50000) // Pre-allocate 50KB for typical query file
		return b
	},
}

// GetBuilder retrieves a builder from the pool
func GetBuilder() *strings.Builder {
	return StringBuilderPool.Get().(*strings.Builder)
}

// PutBuilder returns a builder to the pool after resetting it
func PutBuilder(b *strings.Builder) {
	b.Reset()
	StringBuilderPool.Put(b)
}
