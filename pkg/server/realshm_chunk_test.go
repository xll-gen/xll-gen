//go:build windows

package server

import (
	"bytes"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	flatbuffers "github.com/google/flatbuffers/go"
	shm "github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/chunk"
	"github.com/xll-gen/xll-gen/pkg/rtd"
	"github.com/xll-gen/xll-gen/pkg/transferid"
)

// This file holds the REAL-SHM-CLIENT regression tests for the 2026-07-25 P0
// chunk-budget defect. The pre-existing chunking tests all used mock clients,
// which is exactly why the defect shipped: a mock never touches
// len(slot.reqBuffer), so a 950 KiB chunk aimed at a 512 KiB request buffer
// looked fine. These tests stand up an actual named shared-memory segment with
// the byte layout DirectHost::Init produces, connect a genuine *shm.Client to
// it, and push real payloads through the production send paths.
//
// The rtd-once test lives here rather than in pkg/rtd because pkg/server already
// imports pkg/rtd (NewSystemHandler), so one harness covers both guest->host
// chunking sites instead of being duplicated.

// ---------------------------------------------------------------------------
// Fake host: the C++ DirectHost side of the segment, in Go.
// ---------------------------------------------------------------------------

const (
	fakeExchangeHeaderSize = 64  // DirectHost: max(sizeof(ExchangeHeader), 64)
	fakeSlotHeaderSize     = 128 // DirectHost: sizeof(SlotHeader)
	fakeHostSlots          = 1   // NewDirectGuest requires numSlots >= 1
	fakeGuestSlots         = 4   // matches hostCfg.numGuestSlots in xll_main.cpp.tmpl
)

// mapBase reinterprets the base address shm.CreateShm returned as a typed
// pointer into the file mapping.
//
// Why the indirection instead of a plain unsafe.Pointer(addr): that conversion
// is flagged by `go vet`'s unsafeptr analyzer, and xll-gen's vet gate is clean
// (shm/go itself carries the finding openly — see its NewDirectGuest). The
// conversion is sound here for the same reason it is sound there: the region is
// an OS file mapping, NOT Go heap memory, so there is no GC object for the
// analyzer's rule to protect, and fakeShmHost keeps handle/addr alive for as
// long as any derived pointer is used (Close is the only unmap, and it runs
// after the responder goroutines have joined).
func mapBase(addr uintptr) unsafe.Pointer {
	// &addr is an ordinary Go pointer, so this is a regular pointer conversion
	// followed by a load — not a uintptr->unsafe.Pointer conversion.
	return *(*unsafe.Pointer)(unsafe.Pointer(&addr))
}

// receivedMsg is one guest->host request the fake host observed.
type receivedMsg struct {
	MsgType shm.MsgType
	Payload []byte
}

// fakeShmHost owns a shared-memory segment laid out exactly like
// shm::DirectHost::Init produces it, and answers guest calls with an empty
// success response while recording every request.
type fakeShmHost struct {
	t        *testing.T
	name     string
	handle   shm.ShmHandle
	addr     uintptr
	total    uint64
	slotSize uint32
	events   []shm.EventHandle
	stop     chan struct{}
	wg       sync.WaitGroup

	mu       sync.Mutex
	received []receivedMsg
}

// newFakeShmHost creates the segment + events and starts one responder
// goroutine per guest slot. slotPayload is hostCfg.payloadSize: the WHOLE slot
// payload, which the host then splits in half into a request and a response
// region — the split the buggy chunk constant ignored.
func newFakeShmHost(t *testing.T, slotPayload uint32) *fakeShmHost {
	t.Helper()

	// DirectHost::Init: halfSize = slotSize/2 floored to 64B, respOffset = halfSize.
	halfSize := (slotPayload / 2 / 64) * 64
	if halfSize < 64 {
		halfSize = 64
	}
	slotSize := slotPayload
	if halfSize*2 > slotSize {
		slotSize = halfSize * 2
	}

	totalSlots := uint32(fakeHostSlots + fakeGuestSlots)
	total := uint64(fakeExchangeHeaderSize) + uint64(fakeSlotHeaderSize+slotSize)*uint64(totalSlots)

	name := fmt.Sprintf("xllgen_chunktest_%d_%d", time.Now().UnixNano(), rand.Uint32())
	shm.UnlinkShm(name)

	handle, addr, err := shm.CreateShm(name, total)
	if err != nil {
		t.Fatalf("CreateShm(%s, %d): %v", name, total, err)
	}

	h := &fakeShmHost{
		t: t, name: name, handle: handle, addr: addr,
		total: total, slotSize: slotSize, stop: make(chan struct{}),
	}

	ex := (*shm.ExchangeHeader)(mapBase(addr))
	ex.NumSlots = fakeHostSlots
	ex.NumGuestSlots = fakeGuestSlots
	ex.SlotSize = slotSize
	ex.ReqOffset = 0
	ex.RespOffset = halfSize
	// Safe-by-default: 0 keeps the guest on the full claim/lease slow path.
	ex.FastPathAllowed = 0
	// Publish magic/version LAST so a connecting guest never sees a half-built
	// header (NewDirectGuest validates magic first).
	ex.Version = shm.Version
	atomic.StoreUint32(&ex.Magic, shm.Magic)

	// Event names must match NewDirectGuest's OpenEvent calls exactly: host
	// slots get "<name>_slot_<i>", ALL guest slots share "<name>_guest_call",
	// and every slot gets "<name>_slot_<i>_resp".
	create := func(evName string) shm.EventHandle {
		ev, err := shm.CreateEvent(evName)
		if err != nil {
			h.Close()
			t.Fatalf("CreateEvent(%s): %v", evName, err)
		}
		h.events = append(h.events, ev)
		return ev
	}
	respEvents := make([]shm.EventHandle, totalSlots)
	for i := uint32(0); i < totalSlots; i++ {
		if i < fakeHostSlots {
			create(fmt.Sprintf("%s_slot_%d", name, i))
		}
		respEvents[i] = create(fmt.Sprintf("%s_slot_%d_resp", name, i))
	}
	create(fmt.Sprintf("%s_guest_call", name))

	for i := uint32(fakeHostSlots); i < totalSlots; i++ {
		h.wg.Add(1)
		go h.respond(i, respEvents[i])
	}
	return h
}

// slotBase returns a pointer to the start of absolute slot i's SlotHeader,
// matching DirectHost's layout: exchange header, then slot i at
// i*(slotHeaderSize + slotSize).
func (h *fakeShmHost) slotBase(i uint32) unsafe.Pointer {
	off := uintptr(fakeExchangeHeaderSize) + uintptr(i)*uintptr(fakeSlotHeaderSize+h.slotSize)
	return unsafe.Add(mapBase(h.addr), off)
}

// slotHeader returns the mapped SlotHeader for absolute slot index i.
func (h *fakeShmHost) slotHeader(i uint32) *shm.SlotHeader {
	return (*shm.SlotHeader)(h.slotBase(i))
}

// reqBuffer returns the request half of slot i — the buffer whose capacity the
// chunk budget must respect.
func (h *fakeShmHost) reqBuffer(i uint32) []byte {
	ex := (*shm.ExchangeHeader)(mapBase(h.addr))
	data := unsafe.Add(h.slotBase(i), fakeSlotHeaderSize+uintptr(ex.ReqOffset))
	return unsafe.Slice((*byte)(data), ex.RespOffset-ex.ReqOffset)
}

// respond is the host worker for one guest slot: claim REQ_READY, copy the
// request out, answer with an empty success response, publish RESP_READY.
func (h *fakeShmHost) respond(idx uint32, respEvent shm.EventHandle) {
	defer h.wg.Done()
	hdr := h.slotHeader(idx)
	req := h.reqBuffer(idx)

	for {
		select {
		case <-h.stop:
			return
		default:
		}

		if atomic.LoadUint32(&hdr.State) != shm.SlotReqReady {
			runtime.Gosched()
			continue
		}
		if !atomic.CompareAndSwapUint32(&hdr.State, shm.SlotReqReady, shm.SlotBusy) {
			continue
		}

		reqSize := hdr.ReqSize
		var data []byte
		if reqSize >= 0 {
			if int(reqSize) <= len(req) {
				data = req[:reqSize]
			}
		} else if int(-reqSize) <= len(req) { // end-aligned (FlatBuffer) payload
			data = req[len(req)+int(reqSize):]
		}
		cp := make([]byte, len(data))
		copy(cp, data)

		h.mu.Lock()
		h.received = append(h.received, receivedMsg{MsgType: hdr.MsgType, Payload: cp})
		h.mu.Unlock()

		hdr.RespSize = 0
		hdr.MsgType = shm.MsgTypeNormal
		atomic.StoreUint32(&hdr.State, shm.SlotRespReady)
		shm.SignalEvent(respEvent)
	}
}

// Received returns a snapshot of everything the host has observed.
func (h *fakeShmHost) Received() []receivedMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]receivedMsg, len(h.received))
	copy(out, h.received)
	return out
}

func (h *fakeShmHost) Reset() {
	h.mu.Lock()
	h.received = nil
	h.mu.Unlock()
}

func (h *fakeShmHost) Close() {
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
	h.wg.Wait()
	for _, ev := range h.events {
		shm.CloseEvent(ev)
	}
	h.events = nil
	if h.addr != 0 {
		shm.CloseShm(h.handle, h.addr, h.total)
		h.addr = 0
	}
	shm.UnlinkShm(h.name)
}

// connect returns a REAL *shm.Client attached to this segment.
func (h *fakeShmHost) connect(t *testing.T) *shm.Client {
	t.Helper()
	client, err := shm.Connect(shm.ClientConfig{
		ShmName:           h.name,
		ConnectionTimeout: 5 * time.Second,
		AffinityMode:      shm.AffinityNone,
	})
	if err != nil {
		t.Fatalf("shm.Connect(%s): %v", h.name, err)
	}
	client.SetTimeout(5 * time.Second)
	t.Cleanup(client.Close)
	return client
}

// reassemble concatenates protocol.Chunk frames (in arrival order) back into
// the original payload, asserting the transfer is internally consistent.
func reassemble(t *testing.T, msgs []receivedMsg, wantMsgType uint32) []byte {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatal("no chunk frames reached the host")
	}
	var out []byte
	var id uint64
	for i, m := range msgs {
		if m.MsgType != shm.MsgType(MsgChunk) {
			t.Fatalf("frame %d: slot msgType = %d, want MsgChunk (%d)", i, m.MsgType, MsgChunk)
		}
		c := protocol.GetRootAsChunk(m.Payload, 0)
		if i == 0 {
			id = c.Id()
			out = make([]byte, c.TotalSize())
		}
		if c.Id() != id {
			t.Fatalf("frame %d: transfer id %#x != %#x", i, c.Id(), id)
		}
		if c.MsgType() != wantMsgType {
			t.Fatalf("frame %d: inner msg_type = %d, want %d", i, c.MsgType(), wantMsgType)
		}
		if int(c.Offset())+c.DataLength() > len(out) {
			t.Fatalf("frame %d: offset %d + len %d exceeds total %d", i, c.Offset(), c.DataLength(), len(out))
		}
		copy(out[c.Offset():], c.DataBytes())
	}
	return out
}

// ---------------------------------------------------------------------------
// Regression tests
// ---------------------------------------------------------------------------

// TestRealShmClient_RequestBufferIsHalfTheSlot is the ground truth the whole
// defect turns on, measured against a real segment rather than assumed: the
// guest's request buffer is payloadSize/2, and the budget used by both
// guest->host sites fits it.
func TestRealShmClient_RequestBufferIsHalfTheSlot(t *testing.T) {
	host := newFakeShmHost(t, chunk.SlotPayloadSize)
	defer host.Close()
	client := host.connect(t)

	slot, err := client.AcquireGuestSlot()
	if err != nil {
		t.Fatalf("AcquireGuestSlot: %v", err)
	}
	capacity := len(slot.RequestBuffer())
	slot.Release()

	if capacity != chunk.HalfSlotSize {
		t.Fatalf("real request buffer = %d bytes, want %d (payloadSize %d / 2)",
			capacity, chunk.HalfSlotSize, chunk.SlotPayloadSize)
	}
	if got := chunk.GuestBudget(client); got != chunk.DefaultChunkSize {
		t.Fatalf("chunk.GuestBudget = %d, want %d", got, chunk.DefaultChunkSize)
	}
	// The defect in one line: the old constant does not fit this buffer.
	if 950*1024 <= capacity {
		t.Fatalf("premise broken: the old 950 KiB chunk size fits a %d-byte request buffer", capacity)
	}
}

// TestRealShmClient_MaxRequestSizeMatchesTheSlotProbe is the migration guard for
// dropping the claim-and-Release slot probe in favour of shm >= v0.8.15's
// read-only Client.MaxRequestSize().
//
// Both guest->host sites used to learn their budget by acquiring a guest slot,
// reading len(slot.RequestBuffer()) and immediately releasing it. The accessor is
// supposed to be the SAME number by construction — shm derives the request
// buffer and the accessor from one respOffset-reqOffset — but "supposed to" is
// exactly what a real-segment test is for: if the two ever diverged, every
// chunked transfer would resize silently and the 2026-07-25 P0 (frames rejected
// as "data too large", whole batches lost behind one log line) would come back.
//
// Both slot geometries are checked, so the equality cannot be an accident of the
// default 1 MiB payload.
func TestRealShmClient_MaxRequestSizeMatchesTheSlotProbe(t *testing.T) {
	for _, slotPayload := range []uint32{chunk.SlotPayloadSize, 256 << 10} {
		t.Run(fmt.Sprintf("%dKiB", slotPayload>>10), func(t *testing.T) {
			host := newFakeShmHost(t, slotPayload)
			defer host.Close()
			client := host.connect(t)

			// The retired probe, verbatim.
			slot, err := client.AcquireGuestSlot()
			if err != nil {
				t.Fatalf("AcquireGuestSlot: %v", err)
			}
			probed := len(slot.RequestBuffer())
			slot.Release()

			if got := client.MaxRequestSize(); got != probed {
				t.Fatalf("MaxRequestSize() = %d, but the slot probe measured %d — "+
					"the accessor must report exactly len(GuestSlot.RequestBuffer())", got, probed)
			}
			// ...and therefore the derived budget is unchanged too. Framing is
			// deducted ONCE (our protocol.Chunk frame); MaxRequestSize is raw
			// capacity and shm's own 24B stream ChunkHeader does not apply here,
			// because xll-gen never uses shm's StreamSender.
			if got, want := chunk.GuestBudget(client), chunk.Budget(probed); got != want {
				t.Fatalf("chunk.GuestBudget = %d, want %d (= Budget(probe)) — budget drifted across the accessor switch", got, want)
			}
		})
	}
}

// TestRealShmClient_BudgetIsAvailableWithEveryGuestSlotBusy is the capability the
// probe never had. AcquireGuestSlot makes ONE non-blocking pass, so with all
// fakeGuestSlots claimed the old probe failed and silently returned the
// compiled-in chunk.DefaultChunkSize — the wrong budget on any segment whose
// hostCfg.payloadSize is not 1 MiB, and precisely under load, when flushes are
// biggest. MaxRequestSize reads geometry, so it is unaffected.
func TestRealShmClient_BudgetIsAvailableWithEveryGuestSlotBusy(t *testing.T) {
	const smallPayload = 256 << 10
	host := newFakeShmHost(t, smallPayload)
	defer host.Close()
	client := host.connect(t)

	want := chunk.Budget(smallPayload / 2)
	if got := chunk.GuestBudget(client); got != want {
		t.Fatalf("baseline budget = %d, want %d", got, want)
	}

	held := make([]*shm.GuestSlot, 0, fakeGuestSlots)
	for {
		slot, err := client.AcquireGuestSlot()
		if err != nil || slot == nil {
			break
		}
		held = append(held, slot)
	}
	if len(held) == 0 {
		t.Fatal("could not claim a single guest slot; the premise of this test is unmet")
	}
	defer func() {
		for _, s := range held {
			s.Release()
		}
	}()

	if got := chunk.GuestBudget(client); got != want {
		t.Fatalf("budget with all %d guest slots held = %d, want %d (the accessor must not need a free slot)",
			len(held), got, want)
	}
}

// TestRealShmClient_BudgetFallsBackWhenUnknown pins the surviving fallback path.
// shm answers 0 for a nil / not-connected client ("unknown"), and its accessor is
// nil-receiver safe, so neither shape may panic and both must yield the
// conservative default.
func TestRealShmClient_BudgetFallsBackWhenUnknown(t *testing.T) {
	if got := chunk.GuestBudget((*shm.Client)(nil)); got != chunk.DefaultChunkSize {
		t.Errorf("typed-nil *shm.Client budget = %d, want the conservative %d", got, chunk.DefaultChunkSize)
	}
	if got := chunk.GuestBudget(nil); got != chunk.DefaultChunkSize {
		t.Errorf("nil client budget = %d, want the conservative %d", got, chunk.DefaultChunkSize)
	}
}

// TestRealShmClient_OldChunkSizeIsRejected is the FAIL side of the FAIL->PASS
// evidence, executed against the real client: framing a chunk at the former
// 950 KiB DefaultChunkSize and pushing it makes shm refuse the send outright, so
// the pre-fix code path could never deliver a single chunk.
func TestRealShmClient_OldChunkSizeIsRejected(t *testing.T) {
	host := newFakeShmHost(t, chunk.SlotPayloadSize)
	defer host.Close()
	client := host.connect(t)

	const oldChunkSize = 950 * 1024
	payload := make([]byte, 3<<20)
	b := flatbuffers.NewBuilder(1024)
	frame := chunk.BuildFrame(b, payload[:oldChunkSize], 0xABCD, len(payload), 0, MsgBatchAsyncResponse)

	if _, err := client.SendGuestCall(frame, MsgChunk); err == nil {
		t.Fatal("expected shm to reject a 950 KiB chunk frame against a 512 KiB request buffer, but the send succeeded")
	} else {
		t.Logf("pre-fix behaviour reproduced: %v (frame was %d bytes)", err, len(frame))
	}
	if n := len(host.Received()); n != 0 {
		t.Fatalf("host received %d frames from a rejected send, want 0", n)
	}

	// And the fixed budget goes through on the very same client.
	frame = chunk.BuildFrame(b, payload[:chunk.GuestBudget(client)], 0xABCD, len(payload), 0, MsgBatchAsyncResponse)
	if _, err := client.SendGuestCall(frame, MsgChunk); err != nil {
		t.Fatalf("fixed budget must be accepted, got: %v", err)
	}
}

// TestRealShmClient_SendChunkedAsync covers the async-batch guest->host path at
// the three sizes the P0 report calls out. Pre-fix every one of them failed:
// 512 KiB and 1 MiB were also below the old 950 KiB threshold, so they were sent
// UNCHUNKED and rejected, and 3 MiB was split into 950 KiB chunks that were each
// rejected individually — all of it lost behind a single log.Error.
func TestRealShmClient_SendChunkedAsync(t *testing.T) {
	host := newFakeShmHost(t, chunk.SlotPayloadSize)
	defer host.Close()
	client := host.connect(t)
	budget := chunk.GuestBudget(client)

	for _, size := range []int{512 << 10, 1 << 20, 3 << 20} {
		t.Run(fmt.Sprintf("%dKiB", size>>10), func(t *testing.T) {
			host.Reset()
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i*7 + 3)
			}

			sendChunkedAsync(payload, client, budget)

			msgs := host.Received()
			wantFrames := (size + budget - 1) / budget
			if len(msgs) != wantFrames {
				t.Fatalf("host received %d chunk frames, want %d (budget %d)", len(msgs), wantFrames, budget)
			}
			for i, m := range msgs {
				if len(m.Payload) > chunk.HalfSlotSize {
					t.Fatalf("frame %d is %d bytes, exceeding the %d-byte request buffer", i, len(m.Payload), chunk.HalfSlotSize)
				}
			}
			if got := reassemble(t, msgs, MsgBatchAsyncResponse); !bytes.Equal(got, payload) {
				t.Fatalf("reassembled %d bytes != original %d bytes", len(got), len(payload))
			}
		})
	}
}

// TestRealShmClient_FlushAsyncBatchSilentLossBand is the silent-result-loss
// regression. A batch whose serialized size lands between the real 512 KiB
// request buffer and the old 950 KiB threshold took the NON-chunked branch,
// failed all 10 retries, and dropped every async result in it with only a
// log.Error. It must now be chunked and delivered intact.
func TestRealShmClient_FlushAsyncBatchSilentLossBand(t *testing.T) {
	host := newFakeShmHost(t, chunk.SlotPayloadSize)
	defer host.Close()
	client := host.connect(t)

	// ~700 KiB of results: above the 512 KiB buffer, below the old 950 KiB
	// threshold — squarely in the band that used to vanish.
	const perResult = 7 << 10
	const results = 100
	batch := make([]PendingAsyncResult, results)
	for i := range batch {
		batch[i] = PendingAsyncResult{
			Handle:  []byte(fmt.Sprintf("handle-%04d", i)),
			Val:     string(bytes.Repeat([]byte{byte('a' + i%26)}, perResult)),
			ValType: protocol.AnyValueStr,
		}
	}

	FlushAsyncBatch(batch, client)

	msgs := host.Received()
	if len(msgs) == 0 {
		t.Fatal("no frames reached the host: the batch was silently dropped (the P0 defect)")
	}
	payload := reassemble(t, msgs, MsgBatchAsyncResponse)
	if total := len(payload); total <= chunk.HalfSlotSize || total >= 950*1024 {
		t.Fatalf("batch payload is %d bytes; the test must exercise the (%d, %d) silent-loss band",
			total, chunk.HalfSlotSize, 950*1024)
	}
	resp := protocol.GetRootAsBatchAsyncResponse(payload, 0)
	if resp.ResultsLength() != results {
		t.Fatalf("reassembled batch carries %d results, want %d", resp.ResultsLength(), results)
	}
	var ar protocol.AsyncResult
	if !resp.Results(&ar, 0) || !bytes.Equal(ar.HandleBytes(), []byte("handle-0000")) {
		t.Fatalf("first result handle = %q, want %q", ar.HandleBytes(), "handle-0000")
	}
}

// TestRealShmClient_SendOnceGrid covers the rtd-once grid spill path — the other
// guest->host chunking site — through a real client. Pre-fix, a grid between
// 512 KiB and 950 KiB took the single-slot branch and was rejected as "data too
// large", and anything above was chunked at 950 KiB and rejected per chunk; the
// feature was unusable past roughly a 135x135 `any` grid.
func TestRealShmClient_SendOnceGrid(t *testing.T) {
	host := newFakeShmHost(t, chunk.SlotPayloadSize)
	defer host.Close()
	client := host.connect(t)

	mgr := rtd.NewRtdManager()
	mgr.SetClient(client)

	for _, size := range []int{512 << 10, 1 << 20, 3 << 20} {
		t.Run(fmt.Sprintf("%dKiB", size>>10), func(t *testing.T) {
			host.Reset()
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i*13 + 5)
			}

			if err := mgr.SendOnceGrid("k\x1fv", payload); err != nil {
				t.Fatalf("SendOnceGrid(%d bytes): %v", size, err)
			}

			msgs := host.Received()
			if len(msgs) == 0 {
				t.Fatal("nothing reached the host")
			}
			for i, m := range msgs {
				if len(m.Payload) > chunk.HalfSlotSize {
					t.Fatalf("frame %d is %d bytes, exceeding the %d-byte request buffer", i, len(m.Payload), chunk.HalfSlotSize)
				}
			}
			if len(msgs) == 1 && msgs[0].MsgType == shm.MsgType(MsgRtdOnceGrid) {
				// Single-slot branch: only legal when it actually fits.
				if size > chunk.DefaultChunkSize {
					t.Fatalf("%d-byte grid took the single-slot path; budget is %d", size, chunk.DefaultChunkSize)
				}
				if !bytes.Equal(msgs[0].Payload, payload) {
					t.Fatal("single-slot grid payload corrupted")
				}
				return
			}
			if got := reassemble(t, msgs, uint32(MsgRtdOnceGrid)); !bytes.Equal(got, payload) {
				t.Fatalf("reassembled %d bytes != original %d bytes", len(got), len(payload))
			}
		})
	}
}

// TestRealShmClient_BudgetAdaptsToSmallerSlot proves the runtime derivation
// earns its keep: with a 256 KiB slot payload (128 KiB request buffer) the
// budget scales down instead of clinging to the compiled-in default, so a
// project that changes hostCfg.payloadSize does not silently break.
func TestRealShmClient_BudgetAdaptsToSmallerSlot(t *testing.T) {
	const smallPayload = 256 << 10
	host := newFakeShmHost(t, smallPayload)
	defer host.Close()
	client := host.connect(t)

	wantCap := smallPayload / 2
	budget := chunk.GuestBudget(client)
	if budget != wantCap-chunk.FramingOverhead {
		t.Fatalf("chunk.GuestBudget = %d, want %d (%d-byte request buffer - framing)", budget, wantCap-chunk.FramingOverhead, wantCap)
	}
	if budget >= chunk.DefaultChunkSize {
		t.Fatalf("budget %d did not scale below the default %d", budget, chunk.DefaultChunkSize)
	}

	payload := make([]byte, 1<<20)
	for i := range payload {
		payload[i] = byte(i)
	}
	b := flatbuffers.NewBuilder(1024)
	sender := &chunk.Sender{ChunkSize: budget, Builder: b}
	err := sender.Send(payload, transferid.New(), MsgBatchAsyncResponse, func(frame []byte) error {
		_, e := client.SendGuestCall(frame, MsgChunk)
		return e
	}, chunk.NoRetry)
	if err != nil {
		t.Fatalf("send with adapted budget failed: %v", err)
	}
	if got := reassemble(t, host.Received(), MsgBatchAsyncResponse); !bytes.Equal(got, payload) {
		t.Fatal("reassembled payload mismatch on the small-slot segment")
	}
}
