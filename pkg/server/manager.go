package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/xll-gen/xll-gen/pkg/log"
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

// DefaultMaxConcurrentTransfers bounds how many partially-reassembled inbound
// transfers may be resident at once.
//
// This is a COUNT bound, NOT an aggregate-byte bound — the two caps MULTIPLY.
// At the defaults the worst-case resident footprint is
// MaxChunkBufferBytes x MaxConcurrentTransfers = 256 MiB x 1024 = 256 GiB, and
// the TTL sweep does not cap it either: a peer that dribbles one chunk per
// transfer inside ChunkBufferTTL keeps every buffer's LastAccess fresh, so
// nothing is ever pruned. What the count bound actually buys is a bound on the
// NUMBER of transfers a peer can have open, which is what keeps the map (and
// the sweep) from growing without limit; before it existed the only reclaim was
// the TTL sweep, so a peer opening transfers it never finishes piled up buffers
// for a whole sweep period + TTL (~90 s at the defaults) with no ceiling at
// all. Deployments that need a real byte ceiling must lower
// MaxChunkBufferBytes: the product is the number to reason about, not either
// factor alone. rtd-once grid makes the pressure path realistic rather than
// theoretical: every topic draws a fresh transfer id, so a large RTD grid
// connecting many topics at once opens many concurrent transfers.
//
// 1024 mirrors shm's maxConcurrentStreams (SPECIFICATION.md §3.3.4). The
// reclaim mechanism is explicitly implementation-defined there; we use the
// simpler of shm's two (the C++ Host's "prune stale, then refuse" — see
// StreamReassembler::Handle) rather than LRU eviction, because evicting a live
// transfer to admit a new one just moves the failure onto an innocent peer.
// Override with ChunkManager.MaxConcurrentTransfers / ChunkManagerConfig, or
// xll.yaml `server.chunk.max_concurrent_transfers`.
//
// DIRECTION (AGENTS.md §18.6): this bound — and every other `server.chunk`
// knob — governs the HOST->GUEST inbound reassembler ONLY (chunks the C++ XLL
// sends to this Go server). The GUEST->HOST direction has its OWN, INDEPENDENT
// reassembler in internal/assets/files/src/xll_worker.cpp whose limits
// (kMaxChunkTotalSize, kMaxPartialMessages, kChunkStaleTtl) are compile-time
// constants with no template/YAML wiring. The two are deliberately kept at the
// same NUMBERS so the wire behaves symmetrically, but tuning `server.chunk`
// moves only this side.
const DefaultMaxConcurrentTransfers = 1024

// DefaultMaxPoisonedTransfers bounds the poison set (see ChunkManager.poisoned).
// Entries are tiny and expire with ChunkBufferTTL, but a peer spraying distinct
// malformed ids must not grow the map without limit. Mirrors
// kMaxPoisonedTransfers in internal/assets/files/src/xll_worker.cpp.
const DefaultMaxPoisonedTransfers = 1024

type ChunkManager struct {
	chunkCache     map[uint64]*ChunkBuffer
	chunkMutex     sync.Mutex
	outgoingChunks map[uint64]*OutgoingChunk
	outgoingMutex  sync.Mutex

	// poisoned holds transfer ids whose reassembly was REFUSED for a PROTOCOL
	// VIOLATION, with the time of refusal. Guarded by chunkMutex (the same lock
	// that guards chunkCache, so poison-and-remove is one atomic step).
	//
	// Why a rejection has to be remembered: every reject path in
	// HandleChunk drops the ChunkBuffer, so the producer's NEXT chunk for the
	// same id finds no buffer, GetChunkBuffer allocates a FRESH one, and the
	// chunk is acked as success. The resurrected buffer is missing every
	// earlier chunk, so it can never complete — it just occupies a
	// MaxConcurrentTransfers slot until the TTL sweep — and, worse, the
	// producer's retry ladder (pkg/chunk.AsyncRetry, 10x exponential backoff)
	// sees SUCCESS on its first retry and therefore never aborts: the async
	// call hangs until its own timeout. The refusal existed to make the
	// producer fail FAST and achieved the exact opposite.
	//
	// Only PROTOCOL VIOLATIONS poison — out-of-bounds range, zero-length
	// segment, overlapping range. Those are deterministic properties of the
	// producer's framing, so a retry on the same id cannot succeed. RESOURCE
	// refusals (total non-positive / over MaxChunkBufferBytes / over
	// MaxConcurrentTransfers) deliberately do NOT poison: they insert no
	// buffer (nothing to resurrect) and are transient, so a later retry may
	// legitimately succeed.
	//
	// Entries expire on the same clock as reassembly buffers (effectiveTTL),
	// pruned by pruneStaleChunkBuffersLocked, so an id is never permanently
	// burned. Mirrors g_poisonedTransfers in
	// internal/assets/files/src/xll_worker.cpp (CO-CHANGE ANCHOR, §18.6).
	poisoned map[uint64]time.Time

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

	// MaxConcurrentTransfers bounds the number of partially-reassembled
	// inbound transfers held at once. GetChunkBuffer prunes stale buffers
	// when the bound is hit and, if that frees nothing, refuses the new
	// transfer (returns an error, inserts nothing) so the caller emits
	// MsgTypeSystemError. Zero/negative means
	// DefaultMaxConcurrentTransfers.
	MaxConcurrentTransfers int

	// stop signals the cleanup goroutine to exit. Closed exactly once by
	// Close(); closeOnce makes Close idempotent so a double-shutdown (e.g.
	// a deferred Close plus an explicit one on a teardown path) does not
	// panic on a second channel close.
	stop      chan struct{}
	closeOnce sync.Once
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
	MaxBufferBytes         int64
	CleanupInterval        time.Duration
	BufferTTL              time.Duration
	MaxConcurrentTransfers int
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
		chunkCache:             make(map[uint64]*ChunkBuffer),
		outgoingChunks:         make(map[uint64]*OutgoingChunk),
		poisoned:               make(map[uint64]time.Time),
		MaxChunkBufferBytes:    maxBytes,
		CleanupInterval:        c.CleanupInterval,
		ChunkBufferTTL:         c.BufferTTL,
		MaxConcurrentTransfers: c.MaxConcurrentTransfers,
		stop:                   make(chan struct{}),
	}
	go cm.cleanupLoop()
	return cm
}

func (cm *ChunkManager) cleanupLoop() {
	interval := cm.CleanupInterval
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	ttl := cm.effectiveTTL()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cm.runCleanupOnce(time.Now(), ttl)
		case <-cm.stop:
			return
		}
	}
}

// Close stops the background cleanup goroutine and releases its ticker. It is
// idempotent and safe to call from any goroutine; subsequent calls are no-ops.
// Wire this into the server's shutdown path. After Close returns, the cleanup
// sweep no longer runs, but the manager's maps remain readable — Close does not
// invalidate in-flight buffer pointers, it only halts eviction.
func (cm *ChunkManager) Close() {
	cm.closeOnce.Do(func() {
		close(cm.stop)
	})
}

// runCleanupOnce performs a single cleanup sweep, evicting any buffers whose
// LastAccess is older than ttl relative to now. Extracted from cleanupLoop so
// tests can drive cleanup deterministically without waiting for the 30-second
// ticker. Behavior is identical to the inlined sweep that runCleanupOnce
// replaced; do not change semantics here without updating the cache-visibility
// audit referenced in AGENTS.md §23.
func (cm *ChunkManager) runCleanupOnce(now time.Time, ttl time.Duration) {
	cm.chunkMutex.Lock()
	cm.pruneStaleChunkBuffersLocked(now, ttl)
	cm.chunkMutex.Unlock()

	cm.outgoingMutex.Lock()
	for id, buf := range cm.outgoingChunks {
		if now.Sub(buf.LastAccess) > ttl {
			delete(cm.outgoingChunks, id)
		}
	}
	cm.outgoingMutex.Unlock()
}

// pruneStaleChunkBuffersLocked drops every inbound reassembly buffer idle for
// longer than ttl. The caller MUST hold cm.chunkMutex. It backs both the
// periodic sweep (runCleanupOnce) and the on-demand reclaim GetChunkBuffer runs
// when MaxConcurrentTransfers is hit — the same "prune, then refuse" order shm's
// C++ StreamReassembler::Handle uses at its maxStreams bound.
// It also expires poison-set entries on the same clock, so a transfer id
// refused for a protocol violation becomes reusable after the window instead of
// being burned for the life of the process.
func (cm *ChunkManager) pruneStaleChunkBuffersLocked(now time.Time, ttl time.Duration) {
	for id, buf := range cm.chunkCache {
		if now.Sub(buf.LastAccess) > ttl {
			delete(cm.chunkCache, id)
		}
	}
	for id, at := range cm.poisoned {
		if now.Sub(at) > ttl {
			delete(cm.poisoned, id)
		}
	}
}

// effectiveTTL resolves the configured idle window, falling back to
// DefaultChunkBufferTTL for the zero value.
func (cm *ChunkManager) effectiveTTL() time.Duration {
	if cm.ChunkBufferTTL > 0 {
		return cm.ChunkBufferTTL
	}
	return DefaultChunkBufferTTL
}

// effectiveMaxConcurrentTransfers resolves the configured concurrent-transfer
// bound, falling back to DefaultMaxConcurrentTransfers for the zero value.
func (cm *ChunkManager) effectiveMaxConcurrentTransfers() int {
	if cm.MaxConcurrentTransfers > 0 {
		return cm.MaxConcurrentTransfers
	}
	return DefaultMaxConcurrentTransfers
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
//
// The per-transfer byte cap is only half the bound: opening a NEW transfer also
// has to fit under MaxConcurrentTransfers, or the number of resident buffers is
// unbounded no matter how small each transfer is. At the bound we prune stale
// buffers and, if that frees nothing, refuse the same way (no insert, error
// returned).
//
// The two caps bound the transfer COUNT and the per-transfer SIZE — they do NOT
// compose into an aggregate-byte guard; they MULTIPLY (256 GiB at the defaults,
// and a peer that keeps each transfer fresh inside the TTL defeats pruning too).
// See DefaultMaxConcurrentTransfers.
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
	buf, present := cm.chunkCache[id]
	reuse := present
	if present && buf.TotalSize != total {
		// The id was reused with a different declared total. This can happen
		// when a transfer id collides with a stale, never-completed buffer
		// (e.g. a producer reset after a dropped final chunk). Keeping the old
		// buffer would wedge the transfer until the TTL sweep evicts it,
		// because the new chunks' offsets/total no longer match. Reset to a
		// fresh buffer sized for the new total. See IMPROVEMENT_BACKLOG.md §3.
		//
		// INTENTIONAL ASYMMETRY vs the C++ mirror (AGENTS.md §18.6): the
		// guest->host reassembler in xll_worker.cpp has NO such reset — it
		// keeps the FIRST totalSize for the life of the entry, so the same
		// re-open is measured against the stale total by its bounds check and
		// the transfer is DISCARDED (and poisoned) instead of restarted. Same
		// wire sequence, opposite outcome: recovered here, refused there. This
		// is a known divergence, deliberately left as-is — symmetrizing it is a
		// separate change and would have to pick which behavior is the contract
		// (the C++ side's refusal is arguably the stricter/more correct one; the
		// Go side's reset is more forgiving of a producer restart). Do NOT
		// "fix" one side in isolation.
		log.Warn("ChunkManager: chunk buffer total mismatch on reuse; resetting buffer",
			"id", id, "oldTotal", buf.TotalSize, "newTotal", total)
		reuse = false
	}
	if !reuse {
		// Concurrent-transfer bound. Only a genuinely NEW key grows the map —
		// a total-mismatch reset (present && !reuse) replaces an entry in
		// place and must not be refused by a cap it does not push against.
		if !present {
			maxTransfers := cm.effectiveMaxConcurrentTransfers()
			if len(cm.chunkCache) >= maxTransfers {
				// Prune first: at the bound, buffers abandoned by a peer that
				// stopped mid-transfer are exactly what we want to reclaim,
				// and the periodic sweep may be up to CleanupInterval away.
				cm.pruneStaleChunkBuffersLocked(time.Now(), cm.effectiveTTL())
				if len(cm.chunkCache) >= maxTransfers {
					n := len(cm.chunkCache)
					cm.chunkMutex.Unlock()
					return nil, fmt.Errorf("xll-gen/server: refusing chunk buffer allocation: %d concurrent transfers already in flight (max=%d) (id=%#x)", n, maxTransfers, id)
				}
			}
		}
		buf = &ChunkBuffer{
			Data:       make([]byte, total),
			TotalSize:  total,
			LastAccess: time.Now(),
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

// PoisonTransfer drops the reassembly buffer for id AND records the id as
// refused, so every later chunk carrying it is rejected until the entry expires
// (see ChunkManager.poisoned). Call it from the PROTOCOL-VIOLATION reject paths
// only; RemoveChunkBuffer stays the plain (completion / resource-refusal)
// removal.
func (cm *ChunkManager) PoisonTransfer(id uint64) {
	now := time.Now()
	cm.chunkMutex.Lock()
	delete(cm.chunkCache, id)
	if cm.poisoned == nil {
		cm.poisoned = make(map[uint64]time.Time)
	}
	if len(cm.poisoned) >= DefaultMaxPoisonedTransfers {
		cm.pruneStaleChunkBuffersLocked(now, cm.effectiveTTL())
		if len(cm.poisoned) >= DefaultMaxPoisonedTransfers {
			// Still full of live entries: evict the oldest. The poison set is a
			// fail-fast accelerator, not a correctness invariant — losing an
			// entry only restores the old resurrect-until-TTL behavior for that
			// single id.
			var oldestID uint64
			var oldestAt time.Time
			first := true
			for id, at := range cm.poisoned {
				if first || at.Before(oldestAt) {
					oldestID, oldestAt, first = id, at, false
				}
			}
			delete(cm.poisoned, oldestID)
		}
	}
	cm.poisoned[id] = now
	cm.chunkMutex.Unlock()
}

// IsPoisoned reports whether id was refused for a protocol violation within the
// TTL window. HandleChunk consults it before touching (or allocating) a buffer.
// An expired entry is dropped here so the id is immediately reusable.
func (cm *ChunkManager) IsPoisoned(id uint64) bool {
	cm.chunkMutex.Lock()
	defer cm.chunkMutex.Unlock()
	at, ok := cm.poisoned[id]
	if !ok {
		return false
	}
	if time.Since(at) > cm.effectiveTTL() {
		delete(cm.poisoned, id)
		return false
	}
	return true
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
