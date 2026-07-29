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
// when the real request-buffer capacity is unknown (chunk.GuestBudget returns
// exactly this value in that case). It is an alias of the
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

// The per-chunk / single-slot budget itself is chunk.GuestBudget — the ONE
// implementation, shared with pkg/server's async-batch path. Both sites used to
// carry a hand-mirrored slot probe (AcquireGuestSlot/Release just to measure
// len(slot.RequestBuffer())) because the pkg/server->pkg/rtd import cycle kept
// them apart; shm >= v0.8.15's read-only MaxRequestSize accessor shrank the
// logic to something a leaf can host, so the mirror is gone.
//
// rtdClient itself deliberately does NOT require MaxRequestSize: the byte-
// identity stubs in the SendUpdate tests would have to grow a method they have
// no use for, and GuestBudget already answers onceGridChunkSize for anything
// that cannot report a capacity. The production client IS checked, here:
var _ chunk.MaxRequestSizer = (*shm.Client)(nil)

// connectCancel records the cancel func for one in-flight RTD connect handler,
// tagged with a per-registration generation. The generation makes the registry
// safe against topicID reuse: Excel reassigns a topicID after a disconnect, so a
// completing connect goroutine's deferred deregister must only remove its OWN
// entry — never a NEWER registration that happened to land on the same topicID.
type connectCancel struct {
	cancel context.CancelFunc
	gen    uint64
}

// ErrStopped is returned by every send entry point once Stop has latched the
// send gate. It is a sentinel (not a formatted string) so a long-lived pushing
// goroutine can tell "the server is shutting down, give up for good" apart from
// a transient failure such as a 1s RtdUpdate timeout against a busy host — the
// distinction matters, because retrying the former forever is a spin and
// abandoning the latter loses a live stream.
var ErrStopped = errors.New("rtd: manager stopped (server shutting down)")

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

	// stopped + sendWG are the shutdown gate (see Stop). stopped is guarded by
	// mu, NOT an atomic, precisely so that latching it and registering a send
	// with sendWG are mutually exclusive: that is what makes "no send can Add
	// after the drain has started" a guarantee instead of a hope. sendWG counts
	// guest->host sends that are (or are about to be) touching the SHM mapping.
	stopped bool
	sendWG  sync.WaitGroup
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

// beginSend claims the right to perform ONE guest->host send: it snapshots the
// client and registers with sendWG inside the SAME m.mu critical section that
// Stop latches `stopped` under. Returns ErrStopped after Stop, or a
// "not connected" error when no client has been set.
//
// The single critical section is the whole point. The classic
// "atomic flag + WaitGroup.Add" shape has a window where a sender observes the
// flag as clear, Stop then latches it and calls Wait (which returns, seeing a
// zero counter), and only afterwards does the sender Add and touch a mapping the
// caller has already unmapped. Doing both under mu closes it: after Stop
// releases mu, no beginSend can succeed, so the counter can only ever fall.
//
// The caller MUST pair a successful beginSend with endSend (defer).
func (m *RtdManager) beginSend() (rtdClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil, ErrStopped
	}
	if m.client == nil {
		return nil, fmt.Errorf("server not connected")
	}
	m.sendWG.Add(1)
	return m.client, nil
}

// beginSends is the Publish-shaped variant: one registration covering n sends
// issued back-to-back outside the lock. It exists so Publish does not have to
// re-take mu per topic (it already snapshots the topic set under one RLock).
func (m *RtdManager) beginSends(n int) (rtdClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil, ErrStopped
	}
	if m.client == nil {
		return nil, fmt.Errorf("RTD server not connected")
	}
	m.sendWG.Add(n)
	return m.client, nil
}

func (m *RtdManager) endSend() { m.sendWG.Done() }
func (m *RtdManager) endSends(n int) {
	for i := 0; i < n; i++ {
		m.sendWG.Done()
	}
}

// Stop latches the send gate — every later SendUpdate / SendErrorUpdate /
// Publish / SendOnceGrid returns ErrStopped WITHOUT touching the client — and
// then waits for the sends already in flight to return. It reports whether the
// drain completed within timeout.
//
// WHY IT EXISTS. RTD senders run on goroutines the RtdManager does not own: the
// detached OnRtdConnect goroutine, rtd.RunOnce/RunOnceGrid, and — the common case
// — a STREAMING handler's own pushing goroutine, which lives until its topic
// disconnects. None of those are tracked by shm's DirectGuest.wg, so
// `client.Close()` (which unmaps the segment after draining only ITS workers)
// could unmap underneath any of them. shm documents that as a use-after-free
// (shm/go/direct.go, DirectGuest.Close), and a fault on unmapped memory is a
// `fatal error: unexpected fault address` that recover() cannot catch — so the
// symptom is a full goroutine dump and a non-zero exit on EVERY Excel shutdown
// that had a live RTD stream, which is the ordinary shutdown path.
//
// A false return means the caller MUST NOT unmap: skipping the unmap is free at
// process exit (the OS reclaims it), whereas unmapping under a live sender is the
// exact fault being removed. Never turn a drain timeout into a UAF.
//
// Stop is idempotent. Subscription state is deliberately left alone: it is not
// what holds the mapping, and clearing it would change disconnect behavior for no
// benefit at exit.
func (m *RtdManager) Stop(timeout time.Duration) bool {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.sendWG.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(timeout):
		log.Warn("rtd: Stop timed out waiting for in-flight guest->host sends", "timeout", timeout)
		return false
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
	ids := m.keyToIDs[key]
	topicIDs := make([]int32, 0, len(ids))
	for id := range ids {
		topicIDs = append(topicIDs, id)
	}
	m.mu.RUnlock()

	if len(topicIDs) == 0 {
		return nil
	}

	// One registration for the whole fan-out; the sends themselves stay outside
	// the lock as before. beginSends also replaces the old `client == nil` check,
	// returning the same "RTD server not connected" error.
	client, err := m.beginSends(len(topicIDs))
	if err != nil {
		return err
	}
	defer m.endSends(len(topicIDs))

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
	client, err := m.beginSend()
	if err != nil {
		return err
	}
	defer m.endSend()
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
	client, err := m.beginSend()
	if err != nil {
		return err
	}
	defer m.endSend()
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
// budget is chunk.GuestBudget(client) — the REAL shm request-buffer capacity
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
	// beginSend both snapshots the client and registers this (potentially
	// multi-frame, seconds-long) transfer with the shutdown drain — this is the
	// LONGEST-lived guest->host send in the runtime, so it is the one most likely
	// to still be touching the mapping when the host goes away.
	client, err := m.beginSend()
	if err != nil {
		return err
	}
	defer m.endSend()

	if len(payload) == 0 {
		return fmt.Errorf("rtd.SendOnceGrid: empty payload for key %q", key)
	}

	// Budget = the ACTUAL request-buffer capacity minus framing. It gates BOTH
	// branches below, so the single-slot threshold and the chunk split boundary
	// can never disagree (they did before: a 512..950 KiB grid was sent whole
	// and rejected by shm as "data too large").
	budget := chunk.GuestBudget(client)

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
