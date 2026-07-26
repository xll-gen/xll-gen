package server

import (
	"context"
	"os"
	"runtime/debug"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/log"
	"github.com/xll-gen/xll-gen/pkg/rtd"
	"github.com/xll-gen/xll-gen/pkg/transferid"
)

// SystemHandler processes generic system messages.
type SystemHandler struct {
	ChunkManager   *ChunkManager
	AsyncBatcher   *AsyncBatcher
	CommandBatcher *CommandBatcher
	RefCache       *RefCache
	RtdManager     *rtd.RtdManager
}

// NewSystemHandler creates a new SystemHandler.
func NewSystemHandler(cm *ChunkManager, ab *AsyncBatcher, cb *CommandBatcher, rc *RefCache, rtd *rtd.RtdManager) *SystemHandler {
	return &SystemHandler{
		ChunkManager:   cm,
		AsyncBatcher:   ab,
		CommandBatcher: cb,
		RefCache:       rc,
		RtdManager:     rtd,
	}
}

// HandleAck processes an acknowledgment message.
func (h *SystemHandler) HandleAck(data []byte, respBuf []byte, b *flatbuffers.Builder) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsAck(data, 0)
	id := reqObj.Id()

	chunkSize := ChunkBudget(respBuf)
	chunkData, msgType, totalSize, offset, found := h.ChunkManager.GetNextChunk(id, chunkSize)

	if !found {
		return 0, 0
	}

	if len(chunkData) == 0 {
		return 0, 0
	}

	payload := BuildChunkResponse(b, chunkData, id, totalSize, offset, msgType)

	// Acks/Chunks are usually small, but use generic sender for safety
	return SendAckOrChunk(payload, respBuf, MsgChunk, h.ChunkManager, b)
}

// HandleChunk processes a chunk message.
func (h *SystemHandler) HandleChunk(data []byte, respBuf []byte, b *flatbuffers.Builder, dispatch func([]byte, []byte, shm.MsgType) (int32, shm.MsgType)) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsChunk(data, 0)
	id := reqObj.Id()
	total := int(reqObj.TotalSize())
	offset := int(reqObj.Offset())
	offsetU32 := reqObj.Offset()
	dataLen := reqObj.DataLength()

	// A previous chunk of this transfer was refused for a protocol violation.
	// Refuse everything else on that id until the poison entry expires: without
	// this, the reject paths below (which drop the buffer) let the producer's
	// NEXT chunk allocate a fresh buffer and be acked as SUCCESS, so the retry
	// ladder never aborts and the transfer hangs to its timeout. See
	// ChunkManager.poisoned; mirrored by g_poisonedTransfers in xll_worker.cpp.
	if h.ChunkManager.IsPoisoned(id) {
		log.Error("HandleChunk: chunk for a previously rejected transfer; refusing",
			"id", id, "offset", offsetU32, "len", dataLen)
		return 0, shm.MsgTypeSystemError
	}

	// LOCK HANDOFF, and why the gap between the two locks is safe.
	//
	// GetChunkBuffer takes and RELEASES cm.chunkMutex before this function takes
	// buf.Mutex, so between the two a concurrent goroutine can (a) have the TTL
	// sweep evict this exact buffer from chunkCache, or (b) reject/poison the
	// transfer and drop it. Either way we still hold a live *ChunkBuffer pointer
	// and will finish writing into it — into an object no longer reachable from
	// the map. That wastes the write; it cannot corrupt anything, because the
	// buffer is self-contained and every field is guarded by buf.Mutex.
	//
	// The same reasoning covers the reject-vs-dispatch race that looks scarier
	// than it is: a segment refused for overlap (below) is never copied into
	// buf.Data, so it contributes nothing to Received. The segments that DID
	// land are, by the ClaimSegment contract, disjoint and in-bounds — so if
	// their lengths sum to TotalSize, coverage of [0, TotalSize) is exact and
	// the dispatched payload is complete. A racing rejection can therefore make
	// the OBSERVED outcome differ (one goroutine sees SYSTEM_ERROR while
	// another completes and dispatches the same transfer) but can never hand a
	// consumer a partially-written buffer. No lock widening is needed; do not
	// "fix" this by holding chunkMutex across buf.Mutex (that would serialize
	// every concurrent transfer behind one lock, and introduce a lock-ordering
	// hazard with PoisonTransfer, which takes chunkMutex on its own).
	buf, err := h.ChunkManager.GetChunkBuffer(id, total)
	if err != nil {
		// Wire-supplied total was non-positive or exceeded
		// MaxChunkBufferBytes. Refuse: no buffer was inserted into
		// chunkCache. Surface a SystemError to the producer so it
		// stops retransmitting and the calling side can fail fast.
		log.Error("HandleChunk: rejecting allocation", "id", id, "total", total, "err", err)
		return 0, shm.MsgTypeSystemError
	}

	buf.Mutex.Lock()
	// Defensive bounds check (load-bearing per AGENTS.md §23 Cache Visibility
	// Discipline). Computed in uint64 rather than in `int`: the operands are
	// WIRE types (uint32 offset, int32 length) and uint64 keeps the arithmetic
	// explicitly safe against any wrap regardless of how they are widened. On
	// the supported target (amd64 only — AGENTS.md §0.1; shm v0.8.10 blocks a
	// 386 build outright) `int` is 64-bit so no wrap is reachable today; the
	// uint64 form states the intent instead of relying on that.
	if uint64(offsetU32)+uint64(dataLen) > uint64(len(buf.Data)) {
		buf.Mutex.Unlock()
		// A chunk that would write past TotalSize is a protocol violation,
		// not a retryable event: the transfer can never be completed
		// correctly, so keeping the buffer would only park it until the TTL
		// sweep while the producer keeps pushing. Drop it and answer
		// SystemError so the producer fails fast — mirrors shm
		// SPECIFICATION.md §3.3.4 ("if the running assembled length would
		// exceed totalSize, the stream is rejected with SYSTEM_ERROR") and
		// the C++ mirror in xll_worker.cpp, which erases the partial message
		// on the same condition.
		h.ChunkManager.PoisonTransfer(id)
		log.Error("HandleChunk: chunk out of bounds; dropping transfer",
			"id", id, "offset", offsetU32, "len", dataLen, "total", buf.TotalSize)
		return 0, shm.MsgTypeSystemError
	}

	// A ZERO-LENGTH segment is refused explicitly rather than tolerated. It
	// carries no payload, so it can never be a legitimate part of a transfer —
	// but ClaimSegment would happily record a (offset, 0) range, and the REAL
	// chunk that later arrives at that same offset then classifies as "same
	// start offset, different length" => ClaimOverlap => the whole otherwise
	// healthy transfer is discarded. One empty frame would kill a good
	// transfer. Mirrors the `len == 0` refusal in xll_worker.cpp (§18.6).
	if dataLen == 0 {
		buf.Mutex.Unlock()
		h.ChunkManager.PoisonTransfer(id)
		log.Error("HandleChunk: zero-length chunk segment; dropping transfer",
			"id", id, "offset", offsetU32, "total", buf.TotalSize)
		return 0, shm.MsgTypeSystemError
	}

	// Coverage bookkeeping. ClaimSegment separates the three cases the old
	// offset-only dedup conflated:
	//   - ClaimNew:       first arrival, copy + advance Received;
	//   - ClaimDuplicate: exact retransmit (e.g. after a dropped ACK) — skip
	//     BOTH the copy and the advance, otherwise the duplicate pushes
	//     Received past TotalSize (AGENTS.md §23.3);
	//   - ClaimOverlap:   a range that partially overlaps one already
	//     received. Previously this was accepted (distinct offsets, so the
	//     dedup set did not fire) and it could reach Received >= TotalSize
	//     while leaving an interior gap that was never written — the consumer
	//     then read zero-fill as if it were payload. Reject the transfer.
	claim := buf.ClaimSegment(offsetU32, uint32(dataLen))
	if claim == ClaimOverlap {
		buf.Mutex.Unlock()
		h.ChunkManager.PoisonTransfer(id)
		log.Error("HandleChunk: overlapping chunk range; dropping transfer",
			"id", id, "offset", offsetU32, "len", dataLen, "total", buf.TotalSize)
		return 0, shm.MsgTypeSystemError
	}
	if claim == ClaimNew {
		copy(buf.Data[offset:], reqObj.DataBytes())
		buf.Received += dataLen
	}

	// Claim the dispatch under buf.Mutex. When a retransmitted FINAL chunk
	// races the original, both goroutines can observe completion. Only the
	// first to flip Dispatched (still holding Mutex) is permitted to dispatch;
	// the loser must not re-run the user function or emit a second response.
	// See AGENTS.md §23.3.
	//
	// The test is `==`, not `>=`. With bounds-checked, non-overlapping
	// segments, Received == TotalSize means every byte of [0, TotalSize) was
	// written exactly once — the shm SPECIFICATION.md §3.3.4 completion
	// contract. `>=` was unreachable-by-construction under those two rules and
	// would only ever fire on a coverage bug, so an exact test is both
	// stricter and self-checking. Keep in lockstep with the C++ mirror
	// (xll_worker.cpp, CO-CHANGE ANCHOR §18.6).
	claimedDispatch := false
	if buf.Received == buf.TotalSize && !buf.Dispatched {
		buf.Dispatched = true
		claimedDispatch = true
	}
	buf.Mutex.Unlock()

	if claimedDispatch {
		h.ChunkManager.RemoveChunkBuffer(id)
		payloadMsgType := reqObj.MsgType()
		return dispatch(buf.Data, respBuf, shm.MsgType(payloadMsgType))
	}

	payload := BuildAckResponse(b, id, true)

	// This is an Ack response to a synchronous Chunk request, not chunk
	// data — label it MsgAck for consistency with HandleSetRefCache below
	// (which sends the identical Ack payload). The C++ host (xll_worker.cpp)
	// dispatches inbound chunks on the FlatBuffer's own chunk->msg_type() and
	// parses Ack responses by structure; the SHM response msgType on this
	// reply path is not inspected, so the previous MsgChunk label was a
	// harmless mislabel rather than a load-bearing distinction. Unified per
	// IMPROVEMENT_BACKLOG.md §3.
	return SendAckOrChunk(payload, respBuf, MsgAck, h.ChunkManager, b)
}

// HandleSetRefCache processes a request to store data in the reference cache.
func (h *SystemHandler) HandleSetRefCache(data []byte, respBuf []byte, b *flatbuffers.Builder) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsSetRefCacheRequest(data, 0)
	key := string(reqObj.Key())

	h.RefCache.Set(key, data)

	payload := BuildAckResponse(b, 0, true)
	log.Debug("ACK sent", "func", "SetRefCache")

	return SendAckOrChunk(payload, respBuf, MsgAck, h.ChunkManager, b)
}

// HandleRtdConnect processes an RTD connect request.
func (h *SystemHandler) HandleRtdConnect(data []byte, respBuf []byte, b *flatbuffers.Builder, onConnect func(context.Context, int32, []string, bool) error) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsRtdConnectRequest(data, 0)
	topicID := reqObj.TopicId()
	newVal := reqObj.NewValues()

	var topicStrings []string
	if reqObj.StringsLength() > 0 {
		for i := 0; i < reqObj.StringsLength(); i++ {
			topicStrings = append(topicStrings, string(reqObj.Strings(i)))
		}
	}

	log.Info("RTD Connect request received", "topicID", topicID, "strings", topicStrings)

	// Derive a cancellable context and hand its cancel func to the RtdManager,
	// keyed by topicID, BEFORE launching the goroutine. Ownership of `cancel`
	// transfers to the RtdManager: the ONLY things that may cancel this ctx are
	//   (a) HandleRtdDisconnect -> RtdManager.Unsubscribe (the topic went away), or
	//   (b) a later connect that reuses topicID -> RegisterConnectCancel cancels
	//       the prior registration before installing the new one.
	// We deliberately do NOT cancel on the connect goroutine's normal return:
	// STREAMING handlers (showcase Clock_RTD / StockTick_RTD) return nil
	// immediately but leave a goroutine pushing on this very ctx until the topic
	// disconnects. Cancelling on connect-return killed that stream after exactly
	// one value (the connect-time push), which Excel then saw as an unresponsive
	// RTD server. Run-to-completion handlers (rtd-once scalar/grid) don't touch
	// ctx after returning, so letting their ctx live until disconnect is harmless:
	// context.WithCancel spawns no goroutine, so an un-cancelled ctx is just GC'd
	// once unreferenced, and the live-topic count bounds outstanding ctxs.
	//
	// We deliberately do NOT deregister on the connect goroutine's normal return
	// either. A STREAMING handler returns nil immediately while its pushing
	// goroutine keeps the ctx in use; if normal-return deregistered the entry, a
	// later disconnect's Unsubscribe would find nothing to cancel and the stream
	// would run forever (and, symmetrically, the freeze we are fixing came from
	// the old defer cancel()). So removal of the connectCancels entry is owned by
	//   (a) disconnect (Unsubscribe deletes + cancels), and
	//   (b) reused-topicID connect (RegisterConnectCancel cancels + overwrites the
	//       prior generation).
	// Both keep the map bounded by the live-topic count: every connected topic is
	// eventually disconnected (or its id reused), which removes its entry. The
	// generation-safe deregister returned below is therefore unused on this path;
	// it remains part of RtdManager's API for unit coverage.
	ctx, cancel := context.WithCancel(context.Background())
	_ = h.RtdManager.RegisterConnectCancel(topicID, cancel)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Panic in OnRtdConnect", "error", r)
			}
		}()
		if onConnect != nil {
			if err := onConnect(ctx, topicID, topicStrings, newVal); err != nil {
				log.Error("OnRtdConnect failed", "error", err)
			}
		}
	}()

	b.Reset()
	protocol.RtdConnectResponseStart(b)
	root := protocol.RtdConnectResponseEnd(b)
	b.Finish(root)
	payload := b.FinishedBytes()

	return SendAckOrChunk(payload, respBuf, MsgRtdConnect, h.ChunkManager, b)
}

// HandleRtdDisconnect processes an RTD disconnect request.
func (h *SystemHandler) HandleRtdDisconnect(data []byte, respBuf []byte, b *flatbuffers.Builder, onDisconnect func(context.Context, int32) error) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsRtdDisconnectRequest(data, 0)
	topicID := reqObj.TopicId()

	h.RtdManager.Unsubscribe(topicID)

	ctx := context.Background()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Panic in OnRtdDisconnect", "error", r)
			}
		}()
		if onDisconnect != nil {
			if err := onDisconnect(ctx, topicID); err != nil {
				log.Error("OnRtdDisconnect failed", "error", err)
			}
		}
	}()
	return 0, 0
}

// HandleCalculationEnded processes the calculation ended event.
func (h *SystemHandler) HandleCalculationEnded(respBuf []byte, b *flatbuffers.Builder, onEnded func(context.Context) error) (int32, shm.MsgType) {
	h.RefCache.Clear()

	// We use a simple function call with recover block instead of goroutine+waitgroup,
	// because we want to block until handler finishes to include any scheduled commands in the response.
	if onEnded != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("Panic in OnCalculationEnded", "error", r)
				}
			}()
			if err := onEnded(context.Background()); err != nil {
				log.Error("Event handler OnCalculationEnded failed", "error", err)
			}
		}()
	}

	b.Reset()
	respBytes := h.CommandBatcher.FlushCommands(b)
	if len(respBytes) > 0 {
		return SendAckOrChunk(respBytes, respBuf, MsgCalculationEnded, h.ChunkManager, b)
	}
	return 0, 0
}

// HandleCalculationCanceled processes the calculation canceled event
// (MSG_CALCULATION_CANCELED, 132), sent by the XLL only when the project
// declares `- type: CalculationCanceled` in xll.yaml.
//
// It is a pure NOTIFICATION: it clears nothing and flushes nothing, and replies
// with an empty payload.
//
// Why no state is touched (this is load-bearing; see AGENTS.md §19.4, measured
// against real Excel):
//
//   - Excel fires CalculationCanceled and then CalculationEnded 2–6 ms later,
//     with no calculation work in between. HandleCalculationEnded therefore
//     already runs on a cancelled cycle and already does every per-cycle clear.
//
//   - CommandBatcher.Clear() here would be a silent DATA LOSS bug, not a
//     cleanup: commands scheduled during the cancelled cycle — including any
//     ScheduleSet/ScheduleFormat issued by the user's OnCalculationCanceled
//     handler itself — would be dropped a few milliseconds before the Ended
//     flush that is supposed to emit them. Cancellation means "the calculation
//     was interrupted", not "discard the writes you were asked to make".
//
//   - RefCache.Clear() here would desynchronize the two halves of one cache.
//     The C++ g_sentRefCache and this RefCache stay consistent precisely
//     because a SINGLE event clears both. Clearing only the Go side leaves C++
//     believing the payload was already shipped, so it is not re-sent and
//     ResolveRangeArg misses.
//
// onCanceled is invoked SYNCHRONOUSLY (like HandleCalculationEnded, unlike the
// RTD connect/disconnect hooks). That is what preserves the measured
// Canceled → Ended ordering for the user's handlers: the XLL's send blocks the
// Excel STA thread until this returns, so Excel cannot fire CalculationEnded —
// and the guest cannot start OnCalculationEnded — until OnCalculationCanceled
// has finished. Dispatching it on a goroutine would let the two handlers run
// concurrently or in the wrong order. The §18.3 rule applies unchanged: the
// handler must not drive Excel over COM while the STA is blocked; use
// ScheduleSet/ScheduleFormat, which this path deliberately preserves.
func (h *SystemHandler) HandleCalculationCanceled(onCanceled func(context.Context) error) (int32, shm.MsgType) {
	if onCanceled != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("Panic in OnCalculationCanceled", "error", r)
				}
			}()
			if err := onCanceled(context.Background()); err != nil {
				log.Error("Event handler OnCalculationCanceled failed", "error", err)
			}
		}()
	}
	return 0, 0
}

// excelParentPID is the PID of the process that spawned this server — the
// hosting Excel. Captured once at startup; handlers receive it via
// CommandContext.ExcelPID for multi-instance COM attachment.
var excelParentPID = uint32(os.Getppid())

// HandleCommandInvoke processes a ribbon/macro command invocation. The
// response is a delivery ack only — the handler runs fire-and-forget in its
// own goroutine, because the C++ side must return from onAction immediately
// (Excel's STA thread) and the handler may re-enter Excel via COM.
//
// Both success and unknown-command replies ride the MsgCommandInvoke type;
// the C++ side must branch on the payload's ok()/error() fields, not the SHM
// msgType (same payload-over-msgType idiom as HandleChunk's Ack replies).
//
// Unlike RTD ConnectData there is no shutdown drain for these goroutines:
// they live in the Go server process (not the XLL), so server exit reaps
// them whole and they never touch freed cross-process state.
func (h *SystemHandler) HandleCommandInvoke(data []byte, respBuf []byte, b *flatbuffers.Builder, resolve func(name string) (func(context.Context, CommandContext) error, bool)) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsCommandInvokeRequest(data, 0)
	name := string(reqObj.CommandName())
	controlID := string(reqObj.ControlId())

	errMsg := ""
	if fn, ok := resolve(name); ok {
		cmd := CommandContext{CommandName: name, ControlID: controlID, ExcelPID: excelParentPID}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("Panic in command handler", "command", name, "error", r, "stack", string(debug.Stack()))
				}
			}()
			if err := fn(context.Background(), cmd); err != nil {
				log.Error("Command handler failed", "command", name, "error", err)
			}
		}()
	} else {
		errMsg = "unknown command: " + name
		log.Error("CommandInvoke: unknown command", "name", name)
	}

	b.Reset()
	var errOff flatbuffers.UOffsetT
	if errMsg != "" {
		errOff = b.CreateString(errMsg)
	}
	protocol.CommandInvokeResponseStart(b)
	protocol.CommandInvokeResponseAddOk(b, errMsg == "")
	if errMsg != "" {
		protocol.CommandInvokeResponseAddError(b, errOff)
	}
	b.Finish(protocol.CommandInvokeResponseEnd(b))
	return SendAckOrChunk(b.FinishedBytes(), respBuf, MsgCommandInvoke, h.ChunkManager, b)
}

// SendAckOrChunk writes a host->guest response into respBuf.
//
// If the payload fits, it is copied verbatim and returned with the caller's
// msgType. If it does NOT fit, the response is REFUSED with
// shm.MsgTypeSystemError.
//
// Why refusal and not chunking (2026-07-25, defect B):
// The host->guest chunk transport is ACK-PULL — the Go guest parks the
// remainder in ChunkManager and hands back only the first protocol.Chunk frame,
// expecting the C++ host to come back with MSG_ACK requests to pull the rest.
// **No such C++ consumer exists.** MSG_ACK (xll_ipc.h) is only ever *defined*;
// grep finds zero senders across every asset, template and mock host, so
// HandleAck / GetNextChunk / OutgoingChunk are unreachable from production. The
// one path that DID reach the chunking branch was an oversized SYNCHRONOUS UDF
// response (e.g. a 200x200 `any` grid ~= 1.1 MB against a 512 KiB response
// buffer): the generated C++ does
//
//	auto resp = flatbuffers::GetRoot<ipc::XResponse>(slot.GetRespBuffer());
//
// with NO check of the response msgType, so it read the protocol.Chunk frame
// through the Response vtable — arbitrary garbage fields, i.e. silent data
// corruption, not an error.
//
// Returning shm.MsgTypeSystemError closes that hole: the Go worker stores it in
// the slot header, and shm's C++ SlotHandle::Send / SendFlatBuffer converts it
// to Error::InternalError, which the generated wrapper's existing
// `if (res.HasError())` branch turns into a cell error (#VALUE!, or a null FP12*
// for numgrid) BEFORE any GetRoot<Response> runs. Paired with the log.Error
// below, the failure is diagnosable instead of silently wrong.
//
// The ACK-pull machinery is intentionally RETAINED (see registerAckPullChunk):
// removing the MSG_ACK wire ID is a user decision, and the infrastructure
// becomes live the moment a C++ ACK-pull consumer is implemented. HandleAck's
// own resend path still works through this function because it pre-slices each
// chunk to ChunkBudget(respBuf), which always takes the fits-in-respBuf branch.
//
// It returns the values expected by the shm.Handle callback.
func SendAckOrChunk(payload []byte, respBuf []byte, msgType shm.MsgType, cm *ChunkManager, b *flatbuffers.Builder) (int32, shm.MsgType) {
	if len(payload) <= len(respBuf) {
		copy(respBuf, payload)
		return int32(len(payload)), msgType
	}

	log.Error("Response too large for the SHM response buffer; failing the call instead of emitting an unconsumable chunk frame",
		"payloadBytes", len(payload),
		"respBufBytes", len(respBuf),
		"msgType", uint32(msgType),
		"hint", "reduce the returned grid/string size, or raise hostCfg.payloadSize in the generated xll_main.cpp")
	return 0, shm.MsgTypeSystemError
}

// registerAckPullChunk is the RETAINED host->guest ACK-pull chunk transport:
// it parks payload in cm as an OutgoingChunk and returns the first
// protocol.Chunk frame written into respBuf, expecting the peer to pull the
// remainder with MSG_ACK requests (HandleAck -> GetNextChunk).
//
// It is deliberately NOT called by SendAckOrChunk: the C++ host has no MSG_ACK
// sender, so a chunk frame handed back in a Response slot would be misparsed
// (see SendAckOrChunk's note). Kept — with its regression tests — so the
// transport is one call away once a C++ ACK-pull consumer lands, and so the
// §23.3 Offset-publication-before-AddOutgoingChunk invariant is not lost.
func registerAckPullChunk(payload []byte, respBuf []byte, msgType shm.MsgType, cm *ChunkManager, b *flatbuffers.Builder) (int32, shm.MsgType) {
	// Chunking needed
	transferId := transferid.New()
	chunkSize := ChunkBudget(respBuf)
	out := &OutgoingChunk{
		Data:       make([]byte, len(payload)),
		Id:         transferId,
		MsgType:    uint32(msgType),
		LastAccess: time.Now(),
	}
	copy(out.Data, payload)

	currentSize := chunkSize
	if len(out.Data) < chunkSize {
		currentSize = len(out.Data)
	}

	// Publication order is load-bearing: set out.Offset BEFORE exposing
	// `out` to ChunkManager. AddOutgoingChunk publishes the pointer into
	// a concurrently-reachable map, so a HandleAck that races us here
	// could otherwise call GetNextChunk and observe Offset==0 — the
	// first slice would be resent and the consumer would double-receive
	// bytes [0, currentSize). Do NOT "optimize" this back to setting
	// Offset after publication. See AGENTS.md §23.3.
	out.Offset = currentSize
	cm.AddOutgoingChunk(transferId, out)

	chunkPayload := BuildChunkResponse(b, out.Data[0:currentSize], transferId, len(out.Data), 0, uint32(msgType))

	if len(chunkPayload) > len(respBuf) {
		// The transfer is dead on arrival: the framed first chunk cannot fit
		// respBuf, so no chunk was sent and the consumer will never ACK. Undo
		// the AddOutgoingChunk above immediately instead of leaving the entry
		// to be reclaimed by the TTL sweep — nothing will ever advance it.
		cm.RemoveOutgoingChunk(transferId)
		log.Error("Chunk header overhead too large", "size", len(chunkPayload))
		return 0, 0
	}
	copy(respBuf, chunkPayload)
	return int32(len(chunkPayload)), MsgChunk
}
