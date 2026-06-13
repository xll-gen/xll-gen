package rtd

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/msgid"
)

// stubCall records one SendGuestCallWithTimeout invocation.
type stubCall struct {
	data    []byte
	msgType shm.MsgType
	topicID int32
}

// stubRtdClient is a controllable rtdClient: it can record payloads, block
// until released (simulating a stalled host), and fail selected topics.
type stubRtdClient struct {
	mu          sync.Mutex
	calls       []stubCall
	started     chan struct{} // closed when the first send begins (if non-nil)
	startedOnce sync.Once
	release     chan struct{} // sends block until closed (if non-nil)
	failTopics  map[int32]error
}

func (s *stubRtdClient) SendGuestCallWithTimeout(data []byte, msgType shm.MsgType, timeout time.Duration) ([]byte, error) {
	// Copy: the caller's builder (and its buffer) is pooled and reused
	// after the send returns.
	cp := append([]byte(nil), data...)
	topicID := protocol.GetRootAsRtdUpdate(cp, 0).TopicId()

	s.mu.Lock()
	s.calls = append(s.calls, stubCall{data: cp, msgType: msgType, topicID: topicID})
	s.mu.Unlock()

	if s.started != nil {
		s.startedOnce.Do(func() { close(s.started) })
	}
	if s.release != nil {
		<-s.release
	}
	if err := s.failTopics[topicID]; err != nil {
		return nil, err
	}
	return nil, nil
}

// onceGridCall records one guest->host send made by SendOnceGrid (either a
// single-slot MsgRtdOnceGrid or a MsgChunk frame).
type onceGridCall struct {
	data    []byte
	msgType shm.MsgType
}

// stubOnceGridClient is a controllable client that implements BOTH rtdClient
// (SendGuestCallWithTimeout) and chunkSender (SendGuestCall), recording every
// send so SendOnceGrid's single-slot vs chunked routing can be asserted.
type stubOnceGridClient struct {
	mu       sync.Mutex
	calls    []onceGridCall
	sendErr  error // returned by every send when non-nil
	chunkErr error // returned by SendGuestCall (chunk path) when non-nil
}

func (s *stubOnceGridClient) SendGuestCallWithTimeout(data []byte, msgType shm.MsgType, _ time.Duration) ([]byte, error) {
	cp := append([]byte(nil), data...)
	s.mu.Lock()
	s.calls = append(s.calls, onceGridCall{data: cp, msgType: msgType})
	s.mu.Unlock()
	return nil, s.sendErr
}

func (s *stubOnceGridClient) SendGuestCall(data []byte, msgType shm.MsgType) ([]byte, error) {
	cp := append([]byte(nil), data...)
	s.mu.Lock()
	s.calls = append(s.calls, onceGridCall{data: cp, msgType: msgType})
	s.mu.Unlock()
	if s.chunkErr != nil {
		return nil, s.chunkErr
	}
	return nil, s.sendErr
}

func (s *stubOnceGridClient) snapshot() []onceGridCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]onceGridCall(nil), s.calls...)
}

// TestSendOnceGrid_SingleSlot: a payload at/below the single-slot budget is sent
// as ONE MsgRtdOnceGrid message carrying the payload verbatim.
func TestSendOnceGrid_SingleSlot(t *testing.T) {
	stub := &stubOnceGridClient{}
	m := NewRtdManager()
	m.client = stub

	payload := []byte("small-rtd-once-grid-result")
	if err := m.SendOnceGrid("BDH\x1fAAPL", payload); err != nil {
		t.Fatalf("SendOnceGrid: %v", err)
	}

	calls := stub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("single-slot payload must be 1 send, got %d", len(calls))
	}
	if calls[0].msgType != msgid.MsgRtdOnceGrid {
		t.Fatalf("msgType = %d, want MsgRtdOnceGrid (%d)", calls[0].msgType, msgid.MsgRtdOnceGrid)
	}
	if !bytes.Equal(calls[0].data, payload) {
		t.Fatalf("single-slot payload not sent verbatim:\n got %x\nwant %x", calls[0].data, payload)
	}
}

// TestSendOnceGrid_Chunked: a payload larger than the single-slot budget is
// split into MsgChunk frames, each a protocol.Chunk carrying msg_type=
// MsgRtdOnceGrid and a shared transfer id/total, whose data segments reassemble
// to the original payload in offset order.
func TestSendOnceGrid_Chunked(t *testing.T) {
	stub := &stubOnceGridClient{}
	m := NewRtdManager()
	m.client = stub

	// 2.5 chunks worth of data so we get 3 chunk frames.
	payload := bytes.Repeat([]byte("X"), onceGridChunkSize*2+onceGridChunkSize/2)
	if err := m.SendOnceGrid("BIG\x1fkey", payload); err != nil {
		t.Fatalf("SendOnceGrid (chunked): %v", err)
	}

	calls := stub.snapshot()
	if len(calls) != 3 {
		t.Fatalf("expected 3 chunk frames, got %d", len(calls))
	}

	var (
		reassembled = make([]byte, len(payload))
		wantTotal   = uint32(len(payload))
		seenTotal   uint32
		transferID  uint64
		firstID     bool
	)
	for i, c := range calls {
		if c.msgType != msgid.MsgChunk {
			t.Fatalf("chunk %d msgType = %d, want MsgChunk (%d)", i, c.msgType, msgid.MsgChunk)
		}
		ch := protocol.GetRootAsChunk(c.data, 0)
		if ch.MsgType() != uint32(msgid.MsgRtdOnceGrid) {
			t.Fatalf("chunk %d msg_type = %d, want MsgRtdOnceGrid (%d)", i, ch.MsgType(), msgid.MsgRtdOnceGrid)
		}
		if ch.TotalSize() != wantTotal {
			t.Fatalf("chunk %d total_size = %d, want %d", i, ch.TotalSize(), wantTotal)
		}
		if !firstID {
			transferID = ch.Id()
			firstID = true
		} else if ch.Id() != transferID {
			t.Fatalf("chunk %d id = %#x, want shared %#x", i, ch.Id(), transferID)
		}
		off := int(ch.Offset())
		seg := ch.DataBytes()
		copy(reassembled[off:off+len(seg)], seg)
		seenTotal += uint32(len(seg))
	}
	if seenTotal != wantTotal {
		t.Fatalf("reassembled byte count = %d, want %d", seenTotal, wantTotal)
	}
	if !bytes.Equal(reassembled, payload) {
		t.Fatal("reassembled chunk payload does not match the original grid bytes")
	}
}

// TestSendOnceGrid_NoClient: with no client, SendOnceGrid reports not-connected
// and never panics.
func TestSendOnceGrid_NoClient(t *testing.T) {
	m := NewRtdManager()
	if err := m.SendOnceGrid("k", []byte("x")); err == nil {
		t.Fatal("expected error when client is not connected")
	}
}

// TestSendOnceGrid_SingleSlotErrorPropagates: a send failure on the single-slot
// path is returned so RunOnceGrid can surface it (and skip the readiness token).
func TestSendOnceGrid_SingleSlotErrorPropagates(t *testing.T) {
	stub := &stubOnceGridClient{sendErr: fmt.Errorf("host stalled")}
	m := NewRtdManager()
	m.client = stub
	if err := m.SendOnceGrid("k", []byte("small")); err == nil {
		t.Fatal("expected single-slot send error to propagate")
	}
}

func (s *stubRtdClient) snapshotCalls() []stubCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubCall(nil), s.calls...)
}

// legacyRtdUpdateBytes reproduces, verbatim, the serialization logic of the
// pre-refactor RtdManager.sendUpdateLocked (the ~110-line inline union
// switch) so the refactored path can be checked for byte identity.
func legacyRtdUpdateBytes(topicID int32, value interface{}) []byte {
	b := flatbuffers.NewBuilder(1024)

	// Encode Value
	var anyOff flatbuffers.UOffsetT

	switch v := value.(type) {
	case string:
		sOff := b.CreateString(v)
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		valOff := protocol.StrEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueStr)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	case int:
		// Go int can be 64-bit, so send as double to prevent truncation
		protocol.NumStart(b)
		protocol.NumAddVal(b, float64(v))
		valOff := protocol.NumEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueNum)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	case int32:
		protocol.IntStart(b)
		protocol.IntAddVal(b, v)
		valOff := protocol.IntEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueInt)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	case int64:
		// Protocol only supports 32-bit int, so we send as double to preserve value (up to 53 bits)
		protocol.NumStart(b)
		protocol.NumAddVal(b, float64(v))
		valOff := protocol.NumEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueNum)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	case float64:
		protocol.NumStart(b)
		protocol.NumAddVal(b, v)
		valOff := protocol.NumEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueNum)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	case float32:
		protocol.NumStart(b)
		protocol.NumAddVal(b, float64(v))
		valOff := protocol.NumEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueNum)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	case bool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, v)
		valOff := protocol.BoolEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueBool)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	case time.Time:
		sOff := b.CreateString(v.Format(time.RFC3339))
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		valOff := protocol.StrEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueStr)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	default:
		sOff := b.CreateString(fmt.Sprintf("%v", v))
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		valOff := protocol.StrEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueStr)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
	}

	protocol.RtdUpdateStart(b)
	protocol.RtdUpdateAddTopicId(b, topicID)
	protocol.RtdUpdateAddVal(b, anyOff)
	root := protocol.RtdUpdateEnd(b)
	b.Finish(root)

	return append([]byte(nil), b.FinishedBytes()...)
}

// TestSendUpdate_ByteIdenticalToLegacy proves the fbany-based sendUpdate
// produces byte-identical FlatBuffers output to the pre-refactor inline
// union building for every value type the switch handles.
func TestSendUpdate_ByteIdenticalToLegacy(t *testing.T) {
	ts := time.Date(2026, 6, 10, 12, 34, 56, 0, time.UTC)
	cases := []struct {
		name    string
		topicID int32
		value   interface{}
	}{
		{"string", 1, "hello"},
		{"string_empty", 2, ""},
		{"int", 3, int(1 << 40)},
		{"int32", 4, int32(-42)},
		{"int64", 5, int64(1<<53 - 1)},
		{"float64", 6, float64(3.14159)},
		{"float32", 7, float32(2.5)},
		{"bool_true", 8, true},
		{"bool_false", 9, false},
		{"time", 10, ts},
		{"default_fmt", 11, struct{ X int }{X: 7}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubRtdClient{}
			m := NewRtdManager()
			m.client = stub

			if err := m.SendUpdate(tc.topicID, tc.value); err != nil {
				t.Fatalf("SendUpdate failed: %v", err)
			}

			calls := stub.snapshotCalls()
			if len(calls) != 1 {
				t.Fatalf("expected 1 send, got %d", len(calls))
			}
			if calls[0].msgType != msgid.MsgRtdUpdate {
				t.Fatalf("expected msgType %d, got %d", msgid.MsgRtdUpdate, calls[0].msgType)
			}

			want := legacyRtdUpdateBytes(tc.topicID, tc.value)
			if !bytes.Equal(calls[0].data, want) {
				t.Fatalf("serialized bytes differ from legacy\n got: %x\nwant: %x", calls[0].data, want)
			}
		})
	}
}

func TestSendUpdate_NoClient(t *testing.T) {
	m := NewRtdManager()
	if err := m.SendUpdate(1, "x"); err == nil {
		t.Fatal("expected error when client is not set")
	}
	// SetClient(nil) must not store a typed-nil in the interface field.
	m.SetClient(nil)
	if err := m.SendUpdate(1, "x"); err == nil {
		t.Fatal("expected error after SetClient(nil)")
	}
}

func TestPublish_NoSubscribers(t *testing.T) {
	m := NewRtdManager() // no client either — must still be nil error
	if err := m.Publish("nokey", 1.0); err != nil {
		t.Fatalf("Publish with no subscribers must be a no-op, got %v", err)
	}
}

func TestPublish_NoClient(t *testing.T) {
	m := NewRtdManager()
	m.Subscribe("k", 1)
	if err := m.Publish("k", 1.0); err == nil {
		t.Fatal("expected error when client is not connected")
	}
}

// TestPublish_FailingTopicDoesNotStarveOthers proves that a send failure for
// one topic does not abort the broadcast: every subscribed topic is
// attempted and the per-topic errors are aggregated into the returned error.
func TestPublish_FailingTopicDoesNotStarveOthers(t *testing.T) {
	stub := &stubRtdClient{
		failTopics: map[int32]error{2: fmt.Errorf("host stalled")},
	}
	m := NewRtdManager()
	m.client = stub
	m.Subscribe("k", 1)
	m.Subscribe("k", 2)
	m.Subscribe("k", 3)

	err := m.Publish("k", 99.5)
	if err == nil {
		t.Fatal("expected aggregated error for failing topic")
	}
	if !strings.Contains(err.Error(), "topic 2") || !strings.Contains(err.Error(), "host stalled") {
		t.Fatalf("error should identify the failing topic, got: %v", err)
	}

	calls := stub.snapshotCalls()
	if len(calls) != 3 {
		t.Fatalf("expected all 3 topics attempted, got %d", len(calls))
	}
	seen := map[int32]bool{}
	for _, c := range calls {
		seen[c.topicID] = true
	}
	for _, id := range []int32{1, 2, 3} {
		if !seen[id] {
			t.Fatalf("topic %d was never attempted", id)
		}
	}
}

// TestPublish_DoesNotBlockSubscribeUnsubscribe proves the MEDIUM backlog fix:
// Publish performs its (potentially 1s-per-topic) SHM sends OUTSIDE the
// manager lock, so Subscribe/Unsubscribe complete even while a send is
// stalled on a slow host.
func TestPublish_DoesNotBlockSubscribeUnsubscribe(t *testing.T) {
	stub := &stubRtdClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	m := NewRtdManager()
	m.client = stub
	m.Subscribe("k", 1)
	m.Subscribe("k", 2)

	pubDone := make(chan error, 1)
	go func() {
		pubDone <- m.Publish("k", "tick")
	}()

	// Wait until Publish is inside a blocked send.
	select {
	case <-stub.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish never reached the client send")
	}

	// Subscribe/Unsubscribe (write-lock acquirers) must not be blocked by
	// the in-flight Publish. Before the fix, Publish held RLock across the
	// sends and this deadlocked until the stub was released.
	mutDone := make(chan struct{})
	go func() {
		m.Subscribe("other", 99)
		m.Unsubscribe(99)
		close(mutDone)
	}()
	select {
	case <-mutDone:
		// success: mutators completed while a send was still blocked
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe/Unsubscribe blocked while Publish was sending")
	}

	// Unblock the stub and let Publish finish cleanly.
	close(stub.release)
	select {
	case err := <-pubDone:
		if err != nil {
			t.Fatalf("Publish failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish never completed after release")
	}

	if got := len(stub.snapshotCalls()); got != 2 {
		t.Fatalf("expected 2 topic sends, got %d", got)
	}
}
