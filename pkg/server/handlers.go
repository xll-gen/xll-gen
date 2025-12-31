package server

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/log"
)

// SystemHandler processes generic system messages.
type SystemHandler struct {
	ChunkManager   *ChunkManager
	AsyncBatcher   *AsyncBatcher
	CommandBatcher *CommandBatcher
	RefCache       *RefCache
	RtdManager     *RtdManager
}

// NewSystemHandler creates a new SystemHandler.
func NewSystemHandler(cm *ChunkManager, ab *AsyncBatcher, cb *CommandBatcher, rc *RefCache, rtd *RtdManager) *SystemHandler {
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

	const chunkSize = 950 * 1024
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
	dataLen := reqObj.DataLength()

	buf := h.ChunkManager.GetChunkBuffer(id, total)

	buf.Mutex.Lock()
	if offset+dataLen <= len(buf.Data) {
		copy(buf.Data[offset:], reqObj.DataBytes())
		buf.Received += dataLen
	}
	isComplete := buf.Received >= buf.TotalSize
	buf.Mutex.Unlock()

	if isComplete {
		h.ChunkManager.RemoveChunkBuffer(id)
		payloadMsgType := reqObj.MsgType()
		return dispatch(buf.Data, respBuf, shm.MsgType(payloadMsgType))
	}

	payload := BuildAckResponse(b, id, true)

	return SendAckOrChunk(payload, respBuf, MsgChunk, h.ChunkManager, b)
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

	var strings []string
	if reqObj.StringsLength() > 0 {
		for i := 0; i < reqObj.StringsLength(); i++ {
			strings = append(strings, string(reqObj.Strings(i)))
		}
	}

    log.Info("RTD Connect request received", "topicID", topicID, "strings", strings)

	ctx := context.Background()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Panic in OnRtdConnect", "error", r)
			}
		}()
		if onConnect != nil {
			if err := onConnect(ctx, topicID, strings, newVal); err != nil {
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

// SendAckOrChunk handles the complexity of sending a response that might be larger than the buffer.
// It uses ChunkManager if necessary.
// It returns the values expected by the shm.Handle callback.
func SendAckOrChunk(payload []byte, respBuf []byte, msgType shm.MsgType, cm *ChunkManager, b *flatbuffers.Builder) (int32, shm.MsgType) {
     if len(payload) <= len(respBuf) {
         copy(respBuf, payload)
         return int32(len(payload)), msgType
     }

     // Chunking needed
     transferId := generateTransferID()
     out := &OutgoingChunk{
         Data:       make([]byte, len(payload)),
         Id:         transferId,
         MsgType:    uint32(msgType),
         LastAccess: time.Now(),
     }
     copy(out.Data, payload)
     cm.AddOutgoingChunk(transferId, out)

     const chunkSize = 950 * 1024
     currentSize := chunkSize
     if len(out.Data) < chunkSize {
         currentSize = len(out.Data)
     }

     chunkPayload := BuildChunkResponse(b, out.Data[0:currentSize], transferId, len(out.Data), 0, uint32(msgType))

     out.Offset = currentSize

     if len(chunkPayload) > len(respBuf) {
         log.Error("Chunk header overhead too large", "size", len(chunkPayload))
         return 0, 0
     }
     copy(respBuf, chunkPayload)
     return int32(len(chunkPayload)), MsgChunk
}

func generateTransferID() uint64 {
	var b [8]byte
	_, err := rand.Read(b[:])
	if err != nil {
		// Fallback to weak random if crypto fails (unlikely)
		return 0
	}
	return binary.LittleEndian.Uint64(b[:])
}
