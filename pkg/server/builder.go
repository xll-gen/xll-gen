package server

import (
	"sync"

	flatbuffers "github.com/google/flatbuffers/go"
)

var builderPool = sync.Pool{
	New: func() interface{} {
		return flatbuffers.NewBuilder(1024)
	},
}

// GetBuilder retrieves a FlatBuffers builder from the pool.
// If buf is provided and has capacity, it uses it as the underlying buffer.
// Returns the builder and a cleanup function that MUST be called.
func GetBuilder(buf []byte) (*flatbuffers.Builder, func()) {
	b := builderPool.Get().(*flatbuffers.Builder)
	if buf != nil && cap(buf) > 0 {
		b.Bytes = buf
	}
	b.Reset()
	return b, func() {
		b.Bytes = nil // Safety: Detach SHM buffer before returning to pool
		builderPool.Put(b)
	}
}
