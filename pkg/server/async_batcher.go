package server

import (
	"math/rand"
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/xll-gen/pkg/log"
	"github.com/xll-gen/xll-gen/pkg/protocol"
)

// Heap Builder Pool for outgoing messages (retains buffer capacity)
var heapBuilderPool = sync.Pool{
	New: func() interface{} {
		return flatbuffers.NewBuilder(1024)
	},
}

func FlushAsyncBatch(batch []PendingAsyncResult, client *shm.Client) {
	if len(batch) == 0 {
		return
	}

	b := heapBuilderPool.Get().(*flatbuffers.Builder)
	b.Reset()
	defer heapBuilderPool.Put(b)

	resultOffsets := make([]flatbuffers.UOffsetT, len(batch))

	for i, res := range batch {
		var anyOff flatbuffers.UOffsetT
		var errOff flatbuffers.UOffsetT

		if res.Err != "" {
			errOff = b.CreateString(res.Err)
		} else {
			// Build Any Table
			var uOff flatbuffers.UOffsetT
			switch res.ValType {
			case protocol.AnyValueInt:
				protocol.IntStart(b)
				protocol.IntAddVal(b, res.Val.(int32))
				uOff = protocol.IntEnd(b)
			case protocol.AnyValueNum:
				protocol.NumStart(b)
				protocol.NumAddVal(b, res.Val.(float64))
				uOff = protocol.NumEnd(b)
			case protocol.AnyValueBool:
				protocol.BoolStart(b)
				protocol.BoolAddVal(b, res.Val.(bool))
				uOff = protocol.BoolEnd(b)
			case protocol.AnyValueStr:
				sOff := b.CreateString(res.Val.(string))
				protocol.StrStart(b)
				protocol.StrAddVal(b, sOff)
				uOff = protocol.StrEnd(b)
			case protocol.AnyValueNil:
				protocol.NilStart(b)
				uOff = protocol.NilEnd(b)
			}

			protocol.AnyStart(b)
			protocol.AnyAddValType(b, res.ValType)
			protocol.AnyAddVal(b, uOff)
			anyOff = protocol.AnyEnd(b)
		}

		hOff := b.CreateByteVector(res.Handle)
		protocol.AsyncResultStart(b)
		protocol.AsyncResultAddHandle(b, hOff)
		if errOff > 0 {
			protocol.AsyncResultAddError(b, errOff)
		} else {
			protocol.AsyncResultAddResult(b, anyOff)
		}
		resultOffsets[i] = protocol.AsyncResultEnd(b)
	}

	protocol.BatchAsyncResponseStartResultsVector(b, len(resultOffsets))
	for i := len(resultOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(resultOffsets[i])
	}
	resultsVec := b.EndVector(len(resultOffsets))

	protocol.BatchAsyncResponseStart(b)
	protocol.BatchAsyncResponseAddResults(b, resultsVec)
	root := protocol.BatchAsyncResponseEnd(b)
	b.Finish(root)

	// Send Batch Message
	msgBytes := b.FinishedBytes()

	// Chunking Logic
	const maxPayload = 950 * 1024 // 1MB - overhead
	if len(msgBytes) > maxPayload {
		sendChunkedAsync(msgBytes, client)
		return
	}

	// We implement a short retry loop to handle transient buffer fullness.
	var err error
	for i := 0; i < 10; i++ {
		if _, err = client.SendGuestCall(msgBytes, MsgBatchAsyncResponse); err == nil {
			return
		}
		// Backoff: 5ms, 10ms, ... 2.5s max total wait
		time.Sleep(5 * time.Millisecond * time.Duration(1<<i))
	}

	if err != nil {
		log.Error("Error sending batch async response after retries", "error", err)
	}
}

func sendChunkedAsync(data []byte, client *shm.Client) {
	transferId := uint64(rand.Int63())
	total := len(data)
	offset := 0
	const chunkSize = 950 * 1024

	for offset < total {
		end := offset + chunkSize
		if end > total {
			end = total
		}
		chunkData := data[offset:end]

		b := heapBuilderPool.Get().(*flatbuffers.Builder)
		b.Reset()

		dataOff := b.CreateByteVector(chunkData)
		protocol.ChunkStart(b)
		protocol.ChunkAddId(b, transferId)
		protocol.ChunkAddTotalSize(b, uint32(total))
		protocol.ChunkAddOffset(b, uint32(offset))
		protocol.ChunkAddData(b, dataOff)
		protocol.ChunkAddMsgType(b, MsgBatchAsyncResponse)
		root := protocol.ChunkEnd(b)
		b.FinishWithFileIdentifier(root, []byte("XCHN"))

		payload := b.FinishedBytes()

		// Send Chunk with Retry
		var err error
		sent := false
		for i := 0; i < 10; i++ {
			if _, err = client.SendGuestCall(payload, MsgChunk); err == nil {
				sent = true
				break
			}
			time.Sleep(5 * time.Millisecond * time.Duration(1<<i))
		}

		heapBuilderPool.Put(b)

		if !sent {
			log.Error("Failed to send async chunk", "error", err, "id", transferId, "offset", offset)
			return // Abort transfer
		}

		offset = end
	}
}
