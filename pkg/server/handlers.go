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
	// Defensive bounds check (load-bearing per AGENTS.md §23 Cache
	// Visibility Discipline): silently drop OOB writes.
	if offset+dataLen <= len(buf.Data) {
		// Dedup by chunk offset. A retransmit of the same chunk
		// (e.g. after a dropped ACK) MUST NOT advance Received,
		// otherwise duplicates push Received past TotalSize and
		// trigger premature completion with the trailing bytes
		// still zero — data corruption. See AGENTS.md §23.3.
		if !buf.ReceivedOffsets[offsetU32] {
			copy(buf.Data[offset:], reqObj.DataBytes())
			buf.Received += dataLen
			buf.ReceivedOffsets[offsetU32] = true
		}
	}
	// Claim the dispatch under buf.Mutex. When a retransmitted FINAL chunk
	// races the original, both goroutines can observe Received >= TotalSize.
	// Only the first to flip Dispatched (still holding Mutex) is permitted to
	// dispatch; the loser must not re-run the user function or emit a second
	// response. See AGENTS.md §23.3.
	claimedDispatch := false
	if buf.Received >= buf.TotalSize && !buf.Dispatched {
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

// HandleCalculationCanceled processes the calculation canceled event.
func (h *SystemHandler) HandleCalculationCanceled(onCanceled func(context.Context) error) (int32, shm.MsgType) {
	h.CommandBatcher.Clear()
	// Drop refs cached during the aborted cycle, symmetric with the
	// HandleCalculationEnded path (RefCache.Clear there). Without this, refs
	// from a canceled calc survive until the next calc-ended, so a run of
	// back-to-back cancellations accumulates RefCache entries unboundedly.
	h.RefCache.Clear()

	if onCanceled != nil {
		ctx := context.Background()
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("Panic in OnCalculationCanceled", "error", r)
				}
			}()
			if err := onCanceled(ctx); err != nil {
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

// SendAckOrChunk handles the complexity of sending a response that might be larger than the buffer.
// It uses ChunkManager if necessary.
// It returns the values expected by the shm.Handle callback.
func SendAckOrChunk(payload []byte, respBuf []byte, msgType shm.MsgType, cm *ChunkManager, b *flatbuffers.Builder) (int32, shm.MsgType) {
	if len(payload) <= len(respBuf) {
		copy(respBuf, payload)
		return int32(len(payload)), msgType
	}

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
		log.Error("Chunk header overhead too large", "size", len(chunkPayload))
		return 0, 0
	}
	copy(respBuf, chunkPayload)
	return int32(len(chunkPayload)), MsgChunk
}
