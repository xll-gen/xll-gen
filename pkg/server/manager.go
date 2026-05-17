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

// DefaultCleanupInterval is how often the ChunkManager sweep loop scans for
// stale buffers. Override with ChunkManager.CleanupInterval before traffic
// flows (the loop captures the value once at startup).
const DefaultCleanupInterval = 30 * time.Second

// DefaultChunkBufferTTL is the per-buffer idle window after which a partially
// reassembled inbound transfer (or an unacked outbound chunk) is evicted.
// Buffers older than this are dropped on each cleanup sweep. Override with
// ChunkManager.ChunkBufferTTL before traffic flows.
const DefaultChunkBufferTTL = 60 * time.Second

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

	// CleanupInterval is how often the background sweep runs. Set before
	// the manager handles any traffic; the cleanup loop reads it once at
	// startup. Zero means DefaultCleanupInterval. Surfaced as a field
	// (rather than a constructor option) because deployments with very
	// short or very long chunked-message lifecycles need to tune it
	// without code changes — see AGENTS.md §23.2.
	CleanupInterval time.Duration

	// ChunkBufferTTL is the idle window before a partially-reassembled
	// inbound buffer or an unacked outbound chunk is evicted. Zero means
	// DefaultChunkBufferTTL. See AGENTS.md §23.2.
	ChunkBufferTTL time.Duration
}

func NewChunkManager() *ChunkManager {
	return NewChunkManagerWithMax(DefaultMaxChunkBufferBytes)
}

// NewChunkManagerWithMax constructs a ChunkManager with a configurable upper
// bound on per-transfer allocation size. Passing maxBytes <= 0 falls back to
// DefaultMaxChunkBufferBytes. The cleanup interval and TTL pick up their
// defaults; mutate ChunkManager.CleanupInterval / .ChunkBufferTTL on the
// returned value before traffic flows to override.
func NewChunkManagerWithMax(maxBytes int64) *ChunkManager {
	return NewChunkManagerFromConfig(ChunkManagerConfig{MaxBufferBytes: maxBytes})
}

// ChunkManagerConfig groups every knob ChunkManager exposes so generated
// servers can construct one from a YAML block without touching individual
// fields after the background goroutine has captured them. Zeros mean "use
// the corresponding Default* constant".
type ChunkManagerConfig struct {
	MaxBufferBytes  int64
	CleanupInterval time.Duration
	BufferTTL       time.Duration
}

// NewChunkManagerFromConfig builds a ChunkManager with all settings captured
// before the cleanup goroutine starts — the only safe way to override
// CleanupInterval/BufferTTL, since cleanupLoop reads them once on launch.
// Used by the generated server when xll.yaml carries a `server.chunk` block.
func NewChunkManagerFromConfig(c ChunkManagerConfig) *ChunkManager {
	maxBytes := c.MaxBufferBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxChunkBufferBytes
	}
	cm := &ChunkManager{
		chunkCache:          make(map[uint64]*ChunkBuffer),
		outgoingChunks:      make(map[uint64]*OutgoingChunk),
		MaxChunkBufferBytes: maxBytes,
		CleanupInterval:     c.CleanupInterval,
		ChunkBufferTTL:      c.BufferTTL,
	}
	go cm.cleanupLoop()
	return cm
}

func (cm *ChunkManager) cleanupLoop() {
	interval := cm.CleanupInterval
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	ttl := cm.ChunkBufferTTL
	if ttl <= 0 {
		ttl = DefaultChunkBufferTTL
	}
	ticker := time.NewTicker(interval)
	for range ticker.C {
		cm.runCleanupOnce(time.Now(), ttl)
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
