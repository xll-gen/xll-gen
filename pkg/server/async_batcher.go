package server

import (
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
	"github.com/xll-gen/xll-gen/pkg/log"
	"github.com/xll-gen/xll-gen/pkg/transferid"
)

// Heap Builder Pool for outgoing messages (retains buffer capacity).
//
// NOT replaceable by pkg/pool.GetBuilder/PutBuilder: that pool's PutBuilder
// unconditionally sets b.Bytes = nil (to detach SHM-backed buffers), so the
// pool never retains capacity. Here the builders only ever own heap buffers,
// and retaining the grown capacity across flushes is the whole point.
var heapBuilderPool = sync.Pool{
	New: func() interface{} {
		return flatbuffers.NewBuilder(1024)
	},
}

// sendWithRetry sends payload as msgType via the SHM client, retrying up to
// 10 times with exponential backoff (5ms, 10ms, ... 1.28s; ~2.56s max total
// wait across the 9 inter-attempt sleeps) to ride out transient buffer
// fullness. It returns nil on success, or the last send error after exhausting
// all attempts. The sleep after the final attempt is skipped — there is no
// subsequent retry to space out, so sleeping would only delay the error return.
func sendWithRetry(client *shm.Client, payload []byte, msgType shm.MsgType) error {
	var err error
	for i := 0; i < 10; i++ {
		if _, err = client.SendGuestCall(payload, msgType); err == nil {
			return nil
		}
		if i == 9 {
			break // no retry follows the last attempt; don't sleep before returning
		}
		// Backoff: 5ms, 10ms, ... 1.28s
		time.Sleep(5 * time.Millisecond * time.Duration(1<<i))
	}
	return err
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
			anyOff = fbany.Build(b, res.ValType, res.Val)
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
	const maxPayload = DefaultChunkSize
	if len(msgBytes) > maxPayload {
		sendChunkedAsync(msgBytes, client)
		return
	}

	// We implement a short retry loop to handle transient buffer fullness.
	if err := sendWithRetry(client, msgBytes, MsgBatchAsyncResponse); err != nil {
		log.Error("Error sending batch async response after retries", "error", err)
	}
}

func sendChunkedAsync(data []byte, client *shm.Client) {
	transferId := transferid.New()
	total := len(data)
	offset := 0
	const chunkSize = DefaultChunkSize

	for offset < total {
		end := offset + chunkSize
		if end > total {
			end = total
		}
		chunkData := data[offset:end]

		b := heapBuilderPool.Get().(*flatbuffers.Builder)

		payload := BuildChunkResponse(b, chunkData, transferId, total, offset, MsgBatchAsyncResponse)

		// Send Chunk with Retry
		err := sendWithRetry(client, payload, MsgChunk)

		heapBuilderPool.Put(b)

		if err != nil {
			log.Error("Failed to send async chunk", "error", err, "id", transferId, "offset", offset)
			return // Abort transfer
		}

		offset = end
	}
}
