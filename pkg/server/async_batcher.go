package server

import (
	"fmt"
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
	"github.com/xll-gen/xll-gen/pkg/chunk"
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

// asyncResultSender is the shm surface the async flush path needs: the
// guest->host send itself, plus the request-buffer capacity chunk.GuestBudget
// reads. *shm.Client satisfies it. It is an interface so the size-based batch
// splitting can be exercised against a stub with a small limit instead of a real
// 256 MiB payload.
//
// chunk.MaxRequestSizer is embedded rather than left to GuestBudget's runtime
// assertion so the production client's ability to report its budget is checked
// at COMPILE time here; a stub that wants the conservative fallback returns 0.
type asyncResultSender interface {
	chunk.MaxRequestSizer
	SendGuestCall(data []byte, msgType shm.MsgType) ([]byte, error)
}

// sendWithRetry sends payload as msgType via the SHM client, retrying up to
// 10 times with exponential backoff (5ms, 10ms, ... 1.28s; ~2.56s max total
// wait across the 9 inter-attempt sleeps) to ride out transient buffer
// fullness. It returns nil on success, or the last send error after exhausting
// all attempts. The sleep after the final attempt is skipped — there is no
// subsequent retry to space out, so sleeping would only delay the error return.
func sendWithRetry(client asyncResultSender, payload []byte, msgType shm.MsgType) error {
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

// FlushAsyncBatch serializes a batch of async results and delivers it to the
// XLL host, splitting or failing it explicitly when it cannot fit one
// guest->host transfer (see flushAsyncBatchBounded).
func FlushAsyncBatch(batch []PendingAsyncResult, client *shm.Client) {
	if len(batch) == 0 {
		return
	}
	if client == nil {
		// A typed-nil *shm.Client inside the asyncResultSender interface would
		// NOT compare equal to nil further down and would panic on the first
		// method call, so the check belongs here, at the concrete-type boundary.
		log.Error("FlushAsyncBatch: no SHM client; dropping batch", "batchSize", len(batch))
		return
	}
	flushAsyncBatchBounded(batch, client, chunk.MaxTransferBytes, false)
}

// oversizedAsyncResultError is the cell-visible diagnostic for a single async
// result that cannot be delivered at all. It is placed in AsyncResult.error, so
// the XLL's ProcessAsyncBatchResponse hands it to xlAsyncReturn as a STRING and
// the waiting cell shows this text instead of sitting at #GETTING_DATA forever.
func oversizedAsyncResultError(size, limit int) string {
	return fmt.Sprintf("xll-gen: async result too large to deliver: %d bytes exceeds the %d-byte "+
		"guest->host transfer limit; return fewer cells (or write the data to a file/refcache and return a handle)", size, limit)
}

// flushAsyncBatchBounded serializes batch and delivers it, keeping every
// transfer at or under limit bytes.
//
// WHY A LIMIT EXISTS ON THE SEND SIDE. The receiver — the XLL's HandleChunk
// (internal/assets/files/src/xll_worker.cpp) — refuses any transfer whose
// declared total_size exceeds its compile-time kMaxChunkTotalSize (256 MiB,
// mirrored here as chunk.MaxTransferBytes). Before this bound existed the whole
// batch was framed and pushed anyway: the host refused the first frame, the
// AsyncRetry ladder absorbed the SYSTEM_ERROR ten times, sendChunkedAsync logged
// one error, and EVERY handle in the batch was dropped — the Excel cells behind
// them stayed at #GETTING_DATA until Excel's own async timeout, with no
// diagnostic in any cell.
//
// The contract, in the two shapes that can produce an oversized payload:
//
//   - MANY results that only together exceed the limit: split the batch in half
//     and flush each half. Halving (rather than a per-result size estimate) uses
//     the REAL serialized size at every step, so it cannot be defeated by a
//     type whose on-wire size is mispredicted, and it costs an extra
//     serialization pass only on a path that would otherwise have lost data.
//     A 255-cell batch is no longer collateral damage of one huge sibling.
//
//   - ONE result that alone exceeds the limit (the realistic trigger: an async
//     grid of ~8-10M cells at fbany.AnyGridBytesPerCell). Splitting cannot help
//     — the cap is on ONE transfer's total_size, and a single AsyncResult is
//     indivisible at this layer. The result is REPLACED by an error result
//     carrying oversizedAsyncResultError, so the handle is answered and the cell
//     shows why. `substituted` marks that replacement so a pathologically small
//     limit cannot loop: if even the diagnostic does not fit, it is logged and
//     dropped.
func flushAsyncBatchBounded(batch []PendingAsyncResult, client asyncResultSender, limit int, substituted bool) {
	if len(batch) == 0 {
		return
	}

	b := heapBuilderPool.Get().(*flatbuffers.Builder)
	b.Reset()
	msgBytes := buildAsyncBatchPayload(b, batch)

	if size := len(msgBytes); size > limit {
		// The builder is deliberately NOT returned to heapBuilderPool here:
		// that pool retains capacity by design (see its doc comment), so
		// putting back a builder that just grew past the TRANSFER limit would
		// pin hundreds of MiB for the life of the process. Dropping it also
		// lets the GC reclaim the oversized buffer while the split below
		// re-serializes the halves.
		flushOversizedAsyncBatch(batch, client, limit, size, substituted)
		return
	}
	defer heapBuilderPool.Put(b)

	// Chunking Logic. maxPayload MUST be the real guest->host request-buffer
	// capacity, not a guessed constant: shm rejects any SendGuestCall whose
	// payload exceeds len(slot.reqBuffer) ("data too large"), and that buffer
	// is only HALF the slot payload (see pkg/chunk's geometry note). A too-large
	// threshold sent 512 KiB..950 KiB batches down the NON-chunked path, where
	// all 10 retries failed and the whole batch was dropped with only a
	// log.Error — silent loss of every async result in it.
	//
	// This is the PER-FRAME budget and is unrelated to `limit`, the per-TRANSFER
	// cap enforced above: a payload can be far too big for one slot (chunked,
	// fine) and still be well under the transfer cap.
	maxPayload := chunk.GuestBudget(client)
	if len(msgBytes) > maxPayload {
		sendChunkedAsync(msgBytes, client, maxPayload)
		return
	}

	// We implement a short retry loop to handle transient buffer fullness.
	if err := sendWithRetry(client, msgBytes, MsgBatchAsyncResponse); err != nil {
		log.Error("Error sending batch async response after retries", "error", err)
	}
}

// flushOversizedAsyncBatch handles a batch whose serialized size (`size`)
// exceeds the per-transfer limit: halve it, or — when it is a single
// indivisible result — answer the handle with a diagnostic error result. See
// flushAsyncBatchBounded for the contract.
func flushOversizedAsyncBatch(batch []PendingAsyncResult, client asyncResultSender, limit, size int, substituted bool) {
	if len(batch) > 1 {
		log.Warn("FlushAsyncBatch: batch exceeds the guest->host transfer limit; splitting",
			"bytes", size, "limit", limit, "results", len(batch))
		mid := len(batch) / 2
		flushAsyncBatchBounded(batch[:mid], client, limit, false)
		flushAsyncBatchBounded(batch[mid:], client, limit, false)
		return
	}
	if substituted {
		log.Error("FlushAsyncBatch: even the oversize diagnostic does not fit the transfer limit; dropping result",
			"bytes", size, "limit", limit, "handle", batch[0].Handle)
		return
	}
	log.Error("FlushAsyncBatch: single async result exceeds the guest->host transfer limit; answering the handle with an error",
		"bytes", size, "limit", limit, "valType", batch[0].ValType, "handle", batch[0].Handle)
	flushAsyncBatchBounded([]PendingAsyncResult{{
		Handle: batch[0].Handle,
		Err:    oversizedAsyncResultError(size, limit),
	}}, client, limit, true)
}

// buildAsyncBatchPayload serializes batch as a protocol.BatchAsyncResponse using
// b and returns b's finished bytes. The bytes alias the builder's buffer and are
// only valid until b is reset or returned to the pool.
func buildAsyncBatchPayload(b *flatbuffers.Builder, batch []PendingAsyncResult) []byte {
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

	return b.FinishedBytes()
}

// sendChunkedAsync splits an oversized batch-async response into protocol.Chunk
// frames and pushes each guest->host with retry. The split loop + frame build +
// budget arithmetic come from the shared pkg/chunk; the TRANSPORT model
// (push + AsyncRetry, abort on a chunk's final failure) stays async-specific.
// Each chunk frame carries msg_type=MsgBatchAsyncResponse so the host's
// HandleChunk dispatches the reassembled payload correctly; the slot-level
// message type is MsgChunk.
//
// chunkSize is the caller's chunk.GuestBudget(client) result, so the split
// boundary and the "does it need splitting at all" threshold can never disagree.
//
// The per-TRANSFER cap is already enforced by flushAsyncBatchBounded before we
// get here; chunk.Sender re-checks it (ErrTransferTooLarge) as a backstop for
// any future caller that forgets.
func sendChunkedAsync(data []byte, client asyncResultSender, chunkSize int) {
	b := heapBuilderPool.Get().(*flatbuffers.Builder)
	defer heapBuilderPool.Put(b)

	sender := &chunk.Sender{ChunkSize: chunkSize, Builder: b}
	send := func(frame []byte) error {
		_, err := client.SendGuestCall(frame, MsgChunk)
		return err
	}
	if err := sender.Send(data, transferid.New(), MsgBatchAsyncResponse, send, chunk.AsyncRetry); err != nil {
		log.Error("Failed to send async chunk", "error", err)
	}
}

// The per-chunk payload budget itself lives in chunk.GuestBudget — one
// implementation shared with pkg/rtd's one-shot grid path, which used to carry a
// hand-mirrored copy of the same probe (the server->rtd import cycle kept them
// apart until the logic shrank to a leaf-hostable read).
//
// *shm.Client must be able to report its own budget, or every guest->host
// transfer silently falls back to the compiled-in default and a project with a
// non-default hostCfg.payloadSize breaks. GuestBudget's assertion is a runtime
// one, so pin it here instead:
var _ chunk.MaxRequestSizer = (*shm.Client)(nil)
