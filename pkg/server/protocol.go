package server

import (
	"log/slog"
	"math/rand"
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
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

		protocol.AsyncResultStart(b)
		protocol.AsyncResultAddHandle(b, res.Handle)
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
		slog.Error("Error sending batch async response after retries", "error", err)
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
			slog.Error("Failed to send async chunk", "error", err, "id", transferId, "offset", offset)
			return // Abort transfer
		}

		offset = end
	}
}

func ToScalar(v *protocol.Any) (ScalarValue, bool) {
	if v == nil {
		return ScalarValue{}, false
	}
	var tbl flatbuffers.Table
	if !v.Val(&tbl) {
		return ScalarValue{}, false
	}

	switch v.ValType() {
	case protocol.AnyValueInt:
		var t protocol.Int
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueInt, Int: t.Val()}, true
	case protocol.AnyValueNum:
		var t protocol.Num
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueNum, Num: t.Val()}, true
	case protocol.AnyValueBool:
		var t protocol.Bool
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueBool, Bool: t.Val()}, true
	case protocol.AnyValueStr:
		var t protocol.Str
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueStr, Str: string(t.Val())}, true
	case protocol.AnyValueErr:
		var t protocol.Err
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueErr, Err: int16(t.Val())}, true
	}
	return ScalarValue{}, false
}

func CreateScalarAny(b *flatbuffers.Builder, val ScalarValue) flatbuffers.UOffsetT {
	var uOff flatbuffers.UOffsetT
	switch val.Type {
	case protocol.AnyValueInt:
		protocol.IntStart(b)
		protocol.IntAddVal(b, val.Int)
		uOff = protocol.IntEnd(b)
	case protocol.AnyValueNum:
		protocol.NumStart(b)
		protocol.NumAddVal(b, val.Num)
		uOff = protocol.NumEnd(b)
	case protocol.AnyValueBool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, val.Bool)
		uOff = protocol.BoolEnd(b)
	case protocol.AnyValueStr:
		sOff := b.CreateString(val.Str)
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		uOff = protocol.StrEnd(b)
	case protocol.AnyValueErr:
		protocol.ErrStart(b)
		protocol.ErrAddVal(b, protocol.XlError(val.Err))
		uOff = protocol.ErrEnd(b)
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, val.Type)
	protocol.AnyAddVal(b, uOff)
	return protocol.AnyEnd(b)
}

func CloneRange(b *flatbuffers.Builder, r *protocol.Range) flatbuffers.UOffsetT {
	if r == nil {
		return 0
	}
	s := r.SheetName()
	sOff := b.CreateString(string(s))

	l := r.RefsLength()
	protocol.RangeStartRefsVector(b, l)
	for i := l - 1; i >= 0; i-- {
		obj := new(protocol.Rect)
		if r.Refs(obj, i) {
			protocol.CreateRect(b, obj.RowFirst(), obj.RowLast(), obj.ColFirst(), obj.ColLast())
		}
	}
	refsOff := b.EndVector(l)

	protocol.RangeStart(b)
	protocol.RangeAddSheetName(b, sOff)
	protocol.RangeAddRefs(b, refsOff)
	return protocol.RangeEnd(b)
}

func CloneAny(b *flatbuffers.Builder, a *protocol.Any) flatbuffers.UOffsetT {
	if a == nil {
		return 0
	}
	var uOff flatbuffers.UOffsetT
	t := a.ValType()

	var tbl flatbuffers.Table
	if a.Val(&tbl) {
		switch t {
		case protocol.AnyValueNum:
			var val protocol.Num
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.NumStart(b)
			protocol.NumAddVal(b, val.Val())
			uOff = protocol.NumEnd(b)
		case protocol.AnyValueInt:
			var val protocol.Int
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.IntStart(b)
			protocol.IntAddVal(b, val.Val())
			uOff = protocol.IntEnd(b)
		case protocol.AnyValueBool:
			var val protocol.Bool
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.BoolStart(b)
			protocol.BoolAddVal(b, val.Val())
			uOff = protocol.BoolEnd(b)
		case protocol.AnyValueStr:
			var val protocol.Str
			val.Init(tbl.Bytes, tbl.Pos)
			sOff := b.CreateString(string(val.Val()))
			protocol.StrStart(b)
			protocol.StrAddVal(b, sOff)
			uOff = protocol.StrEnd(b)
		case protocol.AnyValueErr:
			var val protocol.Err
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.ErrStart(b)
			protocol.ErrAddVal(b, val.Val())
			uOff = protocol.ErrEnd(b)
		case protocol.AnyValueNumGrid:
			var val protocol.NumGrid
			val.Init(tbl.Bytes, tbl.Pos)
			l := val.DataLength()
			protocol.NumGridStartDataVector(b, l)
			for i := l - 1; i >= 0; i-- {
				b.PrependFloat64(val.Data(i))
			}
			dataOff := b.EndVector(l)

			protocol.NumGridStart(b)
			protocol.NumGridAddRows(b, val.Rows())
			protocol.NumGridAddCols(b, val.Cols())
			protocol.NumGridAddData(b, dataOff)
			uOff = protocol.NumGridEnd(b)
		case protocol.AnyValueGrid:
			var val protocol.Grid
			val.Init(tbl.Bytes, tbl.Pos)
			uOff = CloneGrid(b, &val)
		case protocol.AnyValueRange:
			var val protocol.Range
			val.Init(tbl.Bytes, tbl.Pos)
			uOff = CloneRange(b, &val)
		case protocol.AnyValueRefCache:
			var val protocol.RefCache
			val.Init(tbl.Bytes, tbl.Pos)
			kOff := b.CreateString(string(val.Key()))
			protocol.RefCacheStart(b)
			protocol.RefCacheAddKey(b, kOff)
			uOff = protocol.RefCacheEnd(b)
		case protocol.AnyValueAsyncHandle:
			var val protocol.AsyncHandle
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.AsyncHandleStart(b)
			protocol.AsyncHandleAddVal(b, val.Val())
			uOff = protocol.AsyncHandleEnd(b)
		default:
			// Nil
			protocol.NilStart(b)
			uOff = protocol.NilEnd(b)
		}
	} else {
		protocol.NilStart(b)
		uOff = protocol.NilEnd(b)
	}

	protocol.AnyStart(b)
	protocol.AnyAddValType(b, t)
	protocol.AnyAddVal(b, uOff)
	return protocol.AnyEnd(b)
}

func CloneGrid(b *flatbuffers.Builder, g *protocol.Grid) flatbuffers.UOffsetT {
	if g == nil {
		return 0
	}
	l := g.DataLength()
	offsets := make([]flatbuffers.UOffsetT, l)
	for i := 0; i < l; i++ {
		var s protocol.Scalar
		if g.Data(&s, i) {
			offsets[i] = CloneScalar(b, &s)
		}
	}

	protocol.GridStartDataVector(b, l)
	for i := l - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	dataOff := b.EndVector(l)

	protocol.GridStart(b)
	protocol.GridAddRows(b, g.Rows())
	protocol.GridAddCols(b, g.Cols())
	protocol.GridAddData(b, dataOff)
	return protocol.GridEnd(b)
}

func CloneScalar(b *flatbuffers.Builder, s *protocol.Scalar) flatbuffers.UOffsetT {
	if s == nil {
		return 0
	}

	var uOff flatbuffers.UOffsetT
	t := s.ValType()

	var tbl flatbuffers.Table
	if s.Val(&tbl) {
		switch t {
		case protocol.ScalarValueInt:
			var val protocol.Int
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.IntStart(b)
			protocol.IntAddVal(b, val.Val())
			uOff = protocol.IntEnd(b)
		case protocol.ScalarValueNum:
			var val protocol.Num
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.NumStart(b)
			protocol.NumAddVal(b, val.Val())
			uOff = protocol.NumEnd(b)
		case protocol.ScalarValueBool:
			var val protocol.Bool
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.BoolStart(b)
			protocol.BoolAddVal(b, val.Val())
			uOff = protocol.BoolEnd(b)
		case protocol.ScalarValueStr:
			var val protocol.Str
			val.Init(tbl.Bytes, tbl.Pos)
			sOff := b.CreateString(string(val.Val()))
			protocol.StrStart(b)
			protocol.StrAddVal(b, sOff)
			uOff = protocol.StrEnd(b)
		case protocol.ScalarValueErr:
			var val protocol.Err
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.ErrStart(b)
			protocol.ErrAddVal(b, val.Val())
			uOff = protocol.ErrEnd(b)
		case protocol.ScalarValueAsyncHandle:
			var val protocol.AsyncHandle
			val.Init(tbl.Bytes, tbl.Pos)
			protocol.AsyncHandleStart(b)
			protocol.AsyncHandleAddVal(b, val.Val())
			uOff = protocol.AsyncHandleEnd(b)
		default:
			protocol.NilStart(b)
			uOff = protocol.NilEnd(b)
		}
	} else {
		protocol.NilStart(b)
		uOff = protocol.NilEnd(b)
	}

	protocol.ScalarStart(b)
	protocol.ScalarAddValType(b, t)
	protocol.ScalarAddVal(b, uOff)
	return protocol.ScalarEnd(b)
}
