package server

import (
	"fmt"
	"sync"
	"time"
)

// DefaultMaxChunkBufferBytes caps the per-transfer reassembly buffer that
// ChunkManager will allocate in response to a wire-supplied TotalSize. The
// wire-supplied size is attacker-controllable, so an unbounded allocation
// path is a DoS vector. 256 MiB is a sane ceiling for any Excel UDF payload.
// Override with ChunkManager.MaxChunkBufferBytes (or the constructor option).
const DefaultMaxChunkBufferBytes int64 = 256 << 20

type ChunkManager struct {
	chunkCache     map[uint64]*ChunkBuffer
	chunkMutex     sync.Mutex
	outgoingChunks map[uint64]*OutgoingChunk
	outgoingMutex  sync.Mutex

	// MaxChunkBufferBytes is the upper bound on the TotalSize a single
	// incoming transfer may declare. GetChunkBuffer refuses (returns an
	// error, does NOT insert into chunkCache) when a caller asks for a
	// larger allocation. Set via NewChunkManagerWithMax or by mutating
	// the field directly before traffic flows. Zero/negative means
	// DefaultMaxChunkBufferBytes is used.
	MaxChunkBufferBytes int64
}

func NewChunkManager() *ChunkManager {
	return NewChunkManagerWithMax(DefaultMaxChunkBufferBytes)
}

// NewChunkManagerWithMax constructs a ChunkManager with a configurable upper
// bound on per-transfer allocation size. Passing maxBytes <= 0 falls back to
// DefaultMaxChunkBufferBytes.
func NewChunkManagerWithMax(maxBytes int64) *ChunkManager {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxChunkBufferBytes
	}
	cm := &ChunkManager{
		chunkCache:          make(map[uint64]*ChunkBuffer),
		outgoingChunks:      make(map[uint64]*OutgoingChunk),
		MaxChunkBufferBytes: maxBytes,
	}
	go cm.cleanupLoop()
	return cm
}

func (cm *ChunkManager) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		cm.runCleanupOnce(time.Now(), 60*time.Second)
	}
}

// runCleanupOnce performs a single cleanup sweep, evicting any buffers whose
// LastAccess is older than ttl relative to now. Extracted from cleanupLoop so
// tests can drive cleanup deterministically without waiting for the 30-second
// ticker. Behavior is identical to the inlined sweep that runCleanupOnce
// replaced; do not change semantics here without updating the cache-visibility
// audit referenced in AGENTS.md §23.
func (cm *ChunkManager) runCleanupOnce(now time.Time, ttl time.Duration) {
	cm.chunkMutex.Lock()
	for id, buf := range cm.chunkCache {
		if now.Sub(buf.LastAccess) > ttl {
			delete(cm.chunkCache, id)
		}
	}
	cm.chunkMutex.Unlock()

	cm.outgoingMutex.Lock()
	for id, buf := range cm.outgoingChunks {
		if now.Sub(buf.LastAccess) > ttl {
			delete(cm.outgoingChunks, id)
		}
	}
	cm.outgoingMutex.Unlock()
}

// GetChunkBuffer returns the per-id reassembly buffer, allocating it on first
// touch. The wire-supplied `total` is the only thing telling us how big the
// payload will be, so it MUST be bounded: a malicious or corrupt producer
// could otherwise request a multi-GiB allocation (DoS). When total is
// non-positive or exceeds MaxChunkBufferBytes, no buffer is inserted into
// chunkCache and an error is returned; callers MUST propagate this and emit
// a MsgTypeSystemError to the wire. The defensive offset+len bounds check in
// HandleChunk remains load-bearing and is preserved separately
// (AGENTS.md §23, Cache Visibility Discipline).
func (cm *ChunkManager) GetChunkBuffer(id uint64, total int) (*ChunkBuffer, error) {
	if total <= 0 {
		return nil, fmt.Errorf("xll-gen/server: refusing chunk buffer allocation: non-positive total=%d (id=%#x)", total, id)
	}
	maxBytes := cm.MaxChunkBufferBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxChunkBufferBytes
	}
	if int64(total) > maxBytes {
		return nil, fmt.Errorf("xll-gen/server: refusing chunk buffer allocation: total=%d exceeds max=%d (id=%#x)", total, maxBytes, id)
	}

	cm.chunkMutex.Lock()
	buf, exists := cm.chunkCache[id]
	if !exists {
		buf = &ChunkBuffer{
			Data:            make([]byte, total),
			TotalSize:       total,
			ReceivedOffsets: make(map[uint32]bool),
			LastAccess:      time.Now(),
		}
		cm.chunkCache[id] = buf
	}
	buf.LastAccess = time.Now()
	cm.chunkMutex.Unlock()
	return buf, nil
}

func (cm *ChunkManager) RemoveChunkBuffer(id uint64) {
	cm.chunkMutex.Lock()
	delete(cm.chunkCache, id)
	cm.chunkMutex.Unlock()
}

func (cm *ChunkManager) AddOutgoingChunk(id uint64, chunk *OutgoingChunk) {
	cm.outgoingMutex.Lock()
	cm.outgoingChunks[id] = chunk
	cm.outgoingMutex.Unlock()
}

func (cm *ChunkManager) GetNextChunk(id uint64, maxSize int) (chunk []byte, msgType uint32, totalSize int, offset int, found bool) {
	cm.outgoingMutex.Lock()
	defer cm.outgoingMutex.Unlock()

	out, exists := cm.outgoingChunks[id]
	if !exists {
		return nil, 0, 0, 0, false
	}
	out.LastAccess = time.Now()

	currentOffset := out.Offset
	remaining := len(out.Data) - out.Offset
	currentSize := maxSize
	if remaining < maxSize {
		currentSize = remaining
	}

	if currentSize <= 0 {
		delete(cm.outgoingChunks, id)
		return nil, 0, 0, 0, false
	}

	chunk = out.Data[currentOffset : currentOffset+currentSize]
	msgType = out.MsgType
	totalSize = len(out.Data)
	offset = currentOffset

	out.Offset += currentSize
	if out.Offset >= len(out.Data) {
		delete(cm.outgoingChunks, id)
	}

	return chunk, msgType, totalSize, offset, true
}

func (cm *ChunkManager) RemoveOutgoingChunk(id uint64) {
	cm.outgoingMutex.Lock()
	delete(cm.outgoingChunks, id)
	cm.outgoingMutex.Unlock()
}
