package pool

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
// If buf is provided and has capacity, it uses it as the underlying buffer
// (typically a borrowed SHM response buffer the caller writes into directly).
// The caller MUST call PutBuilder when finished.
func GetBuilder(buf []byte) *flatbuffers.Builder {
	b := builderPool.Get().(*flatbuffers.Builder)
	if buf != nil && cap(buf) > 0 {
		b.Bytes = buf
	}
	b.Reset()
	return b
}

// PutBuilder detaches the builder's output buffer and returns it to the pool.
//
// b.Bytes is unconditionally cleared because a builder obtained via
// GetBuilder(shmBuf) holds a borrowed SHM buffer that may be unmapped or
// reused by another slot after this call; a pooled builder must never retain
// that pointer. PutBuilder cannot tell a borrowed SHM buffer from a
// heap-grown one, so it conservatively detaches in all cases.
//
// TRADE-OFF: the output-buffer *capacity* is therefore not preserved across
// pool cycles — the next GetBuilder without a `buf` starts from New()'s 1 KiB
// (or reallocates on first grow). What pooling still saves is the Builder
// object and its internal vtable/offset slices (reused across Reset), which is
// the dominant per-call allocation. Do NOT "optimize" this to keep b.Bytes: it
// would alias SHM memory into the pool and risk use-after-free / cross-slot
// corruption. If output-buffer reuse ever matters, add a separate heap-only
// builder pool rather than relaxing this detach.
func PutBuilder(b *flatbuffers.Builder) {
	b.Bytes = nil
	builderPool.Put(b)
}
