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
// The caller MUST call PutBuilder when finished.
func GetBuilder(buf []byte) *flatbuffers.Builder {
	b := builderPool.Get().(*flatbuffers.Builder)
	if buf != nil && cap(buf) > 0 {
		b.Bytes = buf
	}
	b.Reset()
	return b
}

// PutBuilder resets the builder's buffer reference and returns it to the pool.
func PutBuilder(b *flatbuffers.Builder) {
	b.Bytes = nil // Safety: Detach SHM buffer before returning to pool
	builderPool.Put(b)
}
