package rtd

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
	"github.com/xll-gen/xll-gen/pkg/chunk"
	"github.com/xll-gen/xll-gen/pkg/log"
	"github.com/xll-gen/xll-gen/pkg/msgid"
	"github.com/xll-gen/xll-gen/pkg/pool"
	"github.com/xll-gen/xll-gen/pkg/transferid"
)

// onceGridChunkSize is the FALLBACK per-chunk payload byte budget for a chunked
// guest->host RtdOnceGrid transfer AND the fallback single-slot threshold, used
// when the real request-buffer capacity cannot be probed. It is an alias of the
// single source of truth, pkg/chunk.DefaultChunkSize, rather than a hand-copied
// literal: pkg/chunk is a leaf, so pkg/rtd can import it despite the
// pkg/server->pkg/rtd cycle (NewSystemHandler takes rtd.GlobalRtd) that
// previously blocked importing the constant from pkg/server.
//
// A grid payload at or below the budget goes in a single slot tagged
// MsgRtdOnceGrid; a larger one is split into protocol.Chunk messages (each
// tagged MsgChunk, carrying the real MsgRtdOnceGrid msg_type) that the C++
// host's HandleChunk reassembles before dispatching MSG_RTD_ONCE_GRID.
//
// The budget MUST NOT exceed len(shm request buffer) — that buffer is only HALF
// the slot payload. When it did (the old 950 KiB literal vs a real 512 KiB
// buffer) BOTH branches failed: 512..950 KiB grids went down the single-slot
// path and were rejected with "data too large", and larger grids were split into
// chunks that were each individually rejected too. See pkg/chunk's geometry note.
const onceGridChunkSize = chunk.DefaultChunkSize

// onceGridSendTimeout bounds each guest->host send for a one-shot grid. It is
// generous relative to the RtdUpdate 1s timeout because a grid (especially when
// chunked) carries far more bytes; the send must still complete synchronously
// before RunOnceGrid signals readiness.
const onceGridSendTimeout = 5 * time.Second

// rtdClient is the subset of *shm.Client the RtdManager uses. It is an
// interface so tests can inject slow/failing stubs without a real SHM
// segment.
type rtdClient interface {
	SendGuestCallWithTimeout(data []byte, msgType shm.MsgType, timeout time.Duration) ([]byte, error)
}

// chunkSender is the extra surface SendOnceGrid needs when a one-shot grid
// payload exceeds a single slot: it sends one already-framed protocol.Chunk
// message (tagged MsgChunk) guest->host. *shm.Client satisfies it via
// SendGuestCall. Kept separate from rtdClient so the byte-identity-focused
// rtdClient stub used by the SendUpdate tests need not grow this method.
type chunkSender interface {
	SendGuestCall(data []byte, msgType shm.MsgType) ([]byte, error)
}

// guestSlotAcquirer is the shm surface needed to learn the real guest->host
// request-buffer capacity. *shm.Client satisfies it; the test stubs do not, so
// they transparently fall back to the conservative constant.
type guestSlotAcquirer interface {
	AcquireGuestSlot() (*shm.GuestSlot, error)
}

// guestRequestBudget returns the per-chunk / single-slot payload budget for a
// guest->host send over client, derived from the ACTUAL request-buffer capacity
// shm published for this SHM segment (slot payload / 2, see pkg/chunk).
//
// shm exposes that capacity only through AcquireGuestSlot().RequestBuffer() —
// there is no read-only accessor on *shm.Client — so the probe briefly claims
// and immediately releases one guest slot. No Send is issued, so the host never
// observes the slot; Release CASes it straight back to SlotFree. The probe is
// non-blocking (AcquireGuestSlot makes a single pass and errors out if all guest
// slots are busy) and runs once per one-shot grid, so the cost is negligible.
//
// On any failure — including a client that is not an *shm.Client, e.g. the test
// stubs — it falls back to onceGridChunkSize (= chunk.DefaultChunkSize), which
// is correct for the 1 MiB slot payload the generated host configures. Mirrored
// by pkg/server.guestRequestBudget; keep the two in sync (AGENTS.md §18.4).
func guestRequestBudget(client rtdClient) int {
	acq, ok := client.(guestSlotAcquirer)
	if !ok {
		return onceGridChunkSize
	}
	slot, err := acq.AcquireGuestSlot()
	if err != nil || slot == nil {
		return onceGridChunkSize
	}
	capacity := len(slot.RequestBuffer())
	slot.Release()
	if capacity <= 0 {
		return onceGridChunkSize
	}
	return chunk.Budget(capacity)
}

// connectCancel records the cancel func for one in-flight RTD connect handler,
// tagged with a per-registration generation. The generation makes the registry
// safe against topicID reuse: Excel reassigns a topicID after a disconnect, so a
// completing connect goroutine's deferred deregister must only remove its OWN
// entry — never a NEWER registration that happened to land on the same topicID.
type connectCancel struct {
	cancel context.CancelFunc
	gen    uint64
}

// RtdManager manages RTD topic subscriptions and broadcasts.
type RtdManager struct {
	mu sync.RWMutex
	// map[Key] -> map[TopicID]struct{}
	keyToIDs map[string]map[int32]struct{}
	// map[TopicID] -> Key
	idToKey map[int32]string
	client  rtdClient

	// connectCancels maps an in-flight connect's topicID to its cancel func
	// (+ generation). Guarded by mu — the same lock as the subscription maps,
	// so a disconnect's Unsubscribe atomically drops the subscription AND
	// cancels the in-flight connect under one critical section.
	connectCancels map[int32]connectCancel
	// connectGen is a monotonic counter handing out a fresh generation to each
	// RegisterConnectCancel. Guarded by mu.
	connectGen uint64
}

// GlobalRtd is the singleton instance of RtdManager.
var GlobalRtd = NewRtdManager()

// NewRtdManager creates a new RtdManager.
func NewRtdManager() *RtdManager {
	return &RtdManager{
		keyToIDs:       make(map[string]map[int32]struct{}),
		idToKey:        make(map[int32]string),
		connectCancels: make(map[int32]connectCancel),
	}
}

// SetClient sets the SHM client used to send updates.
func (m *RtdManager) SetClient(c *shm.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c == nil {
		// Avoid storing a typed-nil in the interface field, which would
		// defeat the client == nil guards below.
		m.client = nil
		return
	}
	m.client = c
}

// Subscribe registers a TopicID to a logical key.
// Future calls to Publish(key, value) will update this TopicID.
func (m *RtdManager) Subscribe(key string, topicID int32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// If this topicID is already subscribed to a different key, unsubscribe first
	if oldKey, ok := m.idToKey[topicID]; ok {
		if oldKey == key {
			return // Already subscribed to this key
		}
		// Remove from old key's set
		delete(m.keyToIDs[oldKey], topicID)
		if len(m.keyToIDs[oldKey]) == 0 {
			delete(m.keyToIDs, oldKey)
		}
	}

	if _, ok := m.keyToIDs[key]; !ok {
		m.keyToIDs[key] = make(map[int32]struct{})
	}
	m.keyToIDs[key][topicID] = struct{}{}
	m.idToKey[topicID] = key
}

// Unsubscribe removes a TopicID from management AND cancels any in-flight
// connect handler registered for that topicID (see RegisterConnectCancel).
//
// Cancelling here is what makes a mid-flight disconnect actually stop a long
// rtd-once / OnRtdConnect handler: the handler's context.Context becomes Done,
// so a ctx-observing handler returns ctx.Err() and RunOnce/RunOnceGrid push the
// cancellation string instead of running to completion against a dead topic.
//
// The cancel func is invoked while holding m.mu. That is safe: a
// context.CancelFunc is non-blocking and re-enters nothing in RtdManager (it
// only closes the context's done channel). We deliberately do NOT call any
// RtdManager method from inside this critical section, so there is no
// lock-ordering hazard with Publish/SendUpdate/Subscribe (all of which also
// take m.mu).
func (m *RtdManager) Unsubscribe(topicID int32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if key, ok := m.idToKey[topicID]; ok {
		delete(m.keyToIDs[key], topicID)
		if len(m.keyToIDs[key]) == 0 {
			delete(m.keyToIDs, key)
		}
		delete(m.idToKey, topicID)
	}

	if cc, ok := m.connectCancels[topicID]; ok {
		delete(m.connectCancels, topicID)
		cc.cancel()
	}
}

// RegisterConnectCancel records cancel as the cancellation func for the
// in-flight connect handler of topicID, replacing (and cancelling) any stale
// registration still parked on the same topicID. It returns a deregister func
// that the connect goroutine MUST defer: on normal completion the deregister
// removes the entry so a later disconnect does not cancel an already-finished
// handler — and it is GENERATION-SAFE, removing the entry ONLY if it is still
// this registration (not a newer one for a reused topicID).
//
// Register synchronously, BEFORE launching the connect goroutine, so a
// disconnect arriving immediately after the connect ack cannot miss the cancel.
//
// Generation race handling: each registration gets a fresh monotonic
// generation. Unsubscribe and a replacing RegisterConnectCancel cancel+drop the
// current entry unconditionally (the caller wants the in-flight handler gone).
// The returned deregister, by contrast, is a no-op unless the entry it finds
// carries the SAME generation — so a slow handler whose own topicID was reused
// by a brand-new connect cannot clobber or cancel that newer registration when
// it finally finishes.
func (m *RtdManager) RegisterConnectCancel(topicID int32, cancel context.CancelFunc) (deregister func()) {
	m.mu.Lock()
	m.connectGen++
	gen := m.connectGen
	// A previous registration on this topicID (e.g. a connect that never
	// completed before Excel reused the id) is stale: cancel it so its handler
	// stops, then overwrite. Cancel under the lock — non-blocking, no
	// re-entrancy (same contract as Unsubscribe above).
	if prev, ok := m.connectCancels[topicID]; ok {
		prev.cancel()
	}
	m.connectCancels[topicID] = connectCancel{cancel: cancel, gen: gen}
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if cc, ok := m.connectCancels[topicID]; ok && cc.gen == gen {
			delete(m.connectCancels, topicID)
		}
	}
}

// Publish broadcasts a value to all TopicIDs subscribed to the given key.
//
// The subscription map and client are snapshotted under a short read lock and
// the (potentially slow, 1s-timeout-per-topic) SHM sends happen OUTSIDE the
// lock, so Subscribe/Unsubscribe/SetClient are never blocked by a stalled
// host. A send failure for one topic does not starve the remaining topics:
// every topic is attempted, each failure is logged, and the per-topic errors
// are returned joined via errors.Join (nil when all sends succeed).
func (m *RtdManager) Publish(key string, value interface{}) error {
	m.mu.RLock()
	client := m.client
	ids := m.keyToIDs[key]
	topicIDs := make([]int32, 0, len(ids))
	for id := range ids {
		topicIDs = append(topicIDs, id)
	}
	m.mu.RUnlock()

	if len(topicIDs) == 0 {
		return nil
	}

	if client == nil {
		return fmt.Errorf("RTD server not connected")
	}

	// Iterate and send updates (outside the lock; continue past errors)
	var errs []error
	for _, id := range topicIDs {
		if err := sendUpdate(client, id, value, false); err != nil {
			log.Error("RTD publish failed for topic", "key", key, "topicID", id, "error", err)
			errs = append(errs, fmt.Errorf("topic %d: %w", id, err))
		}
	}

	return errors.Join(errs...)
}

// SendUpdate sends a direct update to a specific TopicID carrying a COMPLETED
// (non-error) value. The RtdUpdate is marked is_error=false, so for an rtd-once
// topic the C++ consumer caches it as the topic's one-shot result and retains it
// per the function's declared lifecycle (once / memoize_ttl / memoize).
func (m *RtdManager) SendUpdate(topicID int32, value interface{}) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	return sendUpdate(client, topicID, value, false)
}

// SendErrorUpdate sends a direct update to a specific TopicID carrying an ERROR
// value (a handler error, a ctx cancellation, or a composite-arg resolve miss).
// It is byte-for-byte identical to SendUpdate EXCEPT the RtdUpdate is marked
// is_error=true. For an rtd-once topic that makes the C++ consumer cache the
// value as TRANSIENT: still cached (so the wrapper's next recalc HITS and the
// error string actually paints in the cell instead of the loading placeholder),
// but reclaimed as if the function were plain `once` — so memoize:true /
// memoize_ttl cannot freeze an error, and the following recalc re-runs the
// handler. See xll-gen AGENTS.md §19.3 and types RtdUpdate.is_error.
func (m *RtdManager) SendErrorUpdate(topicID int32, value interface{}) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	return sendUpdate(client, topicID, value, true)
}

// sendUpdate serializes value into an RtdUpdate message and sends it via
// client. It takes the client as a parameter (instead of reading m.client)
// so callers can snapshot the client under the manager lock and perform the
// blocking SHM send after releasing it. isError sets RtdUpdate.is_error (see
// SendErrorUpdate); the default-false flag is elided by flatc, so a false value
// keeps the wire bytes byte-identical to the pre-is_error encoding.
func sendUpdate(client rtdClient, topicID int32, value interface{}, isError bool) error {
	if client == nil {
		return fmt.Errorf("server not connected")
	}

	b := pool.GetBuilder(nil)
	defer pool.PutBuilder(b)

	// Map the Go value onto a protocol.Any union tag + payload (canonical
	// mapping shared with the generated sync/async `any`-return paths).
	anyOff := fbany.BuildGo(b, value)

	protocol.RtdUpdateStart(b)
	protocol.RtdUpdateAddTopicId(b, topicID)
	protocol.RtdUpdateAddVal(b, anyOff)
	protocol.RtdUpdateAddIsError(b, isError)
	root := protocol.RtdUpdateEnd(b)
	b.Finish(root)

	data := b.FinishedBytes()

	_, err := client.SendGuestCallWithTimeout(data, msgid.MsgRtdUpdate, 1000*time.Millisecond)
	return err
}

// SendOnceGrid ships a fully-serialized protocol.RtdOnceGridResult buffer
// (key + Grid/NumGrid Any) to the host as a one-shot grid result, keyed inside
// the payload by `key` (the RTD topic strings joined with \x1f). The host
// stores it in RtdOnceGridRegistry under that key; the C++ wrapper later pulls
// it back out when the readiness recalc re-enters.
//
// Transport mirrors the async-batch guest->host path (pkg/server.async_batcher).
// budget is guestRequestBudget(client) — the REAL shm request-buffer capacity
// minus framing, never a guessed constant:
//   - payload <= budget: one slot, tagged MsgRtdOnceGrid, which the host worker
//     dispatches directly to ProcessRtdOnceGrid.
//   - payload  > budget: split into protocol.Chunk messages, each tagged
//     MsgChunk and carrying msg_type=MsgRtdOnceGrid + the shared transfer
//     id/total/offset, which the host's HandleChunk reassembles and then
//     dispatches to ProcessRtdOnceGrid on completion.
//
// It is SYNCHRONOUS: every send waits for the host ACK, and (for the chunked
// case) all chunks are sent in order before returning. RunOnceGrid relies on
// this: it must not signal RTD readiness until the host actually holds the
// grid (see RunOnceGrid's ordering note). Returns the first send error
// (aborting the transfer), or nil once the whole payload is delivered+acked.
func (m *RtdManager) SendOnceGrid(key string, payload []byte) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("server not connected")
	}
	if len(payload) == 0 {
		return fmt.Errorf("rtd.SendOnceGrid: empty payload for key %q", key)
	}

	// Budget = the ACTUAL request-buffer capacity minus framing. It gates BOTH
	// branches below, so the single-slot threshold and the chunk split boundary
	// can never disagree (they did before: a 512..950 KiB grid was sent whole
	// and rejected by shm as "data too large").
	budget := guestRequestBudget(client)

	// Single-slot fast path: the whole RtdOnceGridResult fits in one request
	// buffer. The host worker recognizes MsgRtdOnceGrid directly.
	if len(payload) <= budget {
		_, err := client.SendGuestCallWithTimeout(payload, msgid.MsgRtdOnceGrid, onceGridSendTimeout)
		return err
	}

	// Chunked path: the grid is too large for a single slot. Frame it into
	// protocol.Chunk messages that carry the real MsgRtdOnceGrid msg_type, so
	// the host reassembles them and dispatches MSG_RTD_ONCE_GRID once complete.
	cs, ok := client.(chunkSender)
	if !ok {
		return fmt.Errorf("rtd.SendOnceGrid: payload of %d bytes exceeds single-slot budget %d but client does not support chunked send", len(payload), budget)
	}

	b := pool.GetBuilder(nil)
	defer pool.PutBuilder(b)

	// Split loop + frame build + chunk-size constant come from the shared
	// pkg/chunk.Sender (byte-identical frames to the host's HandleChunk). Each
	// frame carries msg_type=MsgRtdOnceGrid so the host dispatches the
	// reassembled grid correctly; the slot-level message type is MsgChunk.
	//
	// Retry policy: chunk.NoRetry — DELIBERATE, preserving the pre-R24 behavior.
	// This path is SYNCHRONOUS and the caller (RunOnceGrid) must observe the
	// first send failure immediately so it does NOT signal RTD readiness for a
	// grid the host never received. The async batch path uses chunk.AsyncRetry
	// because it is fire-and-forget and can tolerate riding out transient buffer
	// fullness; here a stuck send would block readiness anyway, so surfacing the
	// error up-front (and letting the RTD layer retry the whole one-shot) is the
	// safer policy. See AGENTS.md §23.3 (retry-policy divergence made explicit).
	sender := &chunk.Sender{ChunkSize: budget, Builder: b}
	send := func(frame []byte) error {
		_, err := cs.SendGuestCall(frame, msgid.MsgChunk)
		return err
	}
	if err := sender.Send(payload, transferid.New(), uint32(msgid.MsgRtdOnceGrid), send, chunk.NoRetry); err != nil {
		return fmt.Errorf("rtd.SendOnceGrid: %w", err)
	}

	return nil
}
