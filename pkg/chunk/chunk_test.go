package chunk

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

// legacyBuildChunkResponse is a verbatim copy of the pre-R24
// pkg/server.BuildChunkResponse implementation, kept here as the byte-identity
// oracle. If BuildFrame ever diverges from this layout the C++ HandleChunk
// reassembler breaks; this test pins them together.
func legacyBuildChunkResponse(b *flatbuffers.Builder, chunkData []byte, id uint64, totalSize int, offset int, msgType uint32) []byte {
	b.Reset()
	dataOff := b.CreateByteVector(chunkData)
	protocol.ChunkStart(b)
	protocol.ChunkAddId(b, id)
	protocol.ChunkAddTotalSize(b, uint32(totalSize))
	protocol.ChunkAddOffset(b, uint32(offset))
	protocol.ChunkAddData(b, dataOff)
	protocol.ChunkAddMsgType(b, msgType)
	root := protocol.ChunkEnd(b)
	b.FinishWithFileIdentifier(root, []byte("XCHN"))
	return b.FinishedBytes()
}

// TestBuildFrame_ByteIdenticalToLegacy asserts the unified BuildFrame produces
// byte-for-byte the same wire frame as the old hand-built code across a matrix
// of payloads/ids/offsets/msgTypes. This is the load-bearing invariant of R24:
// the C++ side must not change.
func TestBuildFrame_ByteIdenticalToLegacy(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		id      uint64
		total   int
		offset  int
		msgType uint32
	}{
		{"empty data", []byte{}, 0x1122334455667788, 0, 0, 128},
		{"tiny", []byte("x"), 1, 1, 0, 138},
		{"midchunk", bytes.Repeat([]byte("AB"), 500), 0xdeadbeef, 4096, 1024, 129},
		{"max id", []byte("payload-bytes-here"), ^uint64(0), 18, 0, 140},
		{"high offset/total", bytes.Repeat([]byte("Z"), 300), 42, 1 << 20, 1 << 19, 135},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildFrame(flatbuffers.NewBuilder(0), c.data, c.id, c.total, c.offset, c.msgType)
			want := legacyBuildChunkResponse(flatbuffers.NewBuilder(0), c.data, c.id, c.total, c.offset, c.msgType)
			if !bytes.Equal(got, want) {
				t.Fatalf("frame mismatch\n got %x\nwant %x", got, want)
			}
			// Sanity: decodes back to the same fields.
			ch := protocol.GetRootAsChunk(got, 0)
			if ch.Id() != c.id || ch.TotalSize() != uint32(c.total) || ch.Offset() != uint32(c.offset) || ch.MsgType() != c.msgType {
				t.Fatalf("decoded fields wrong: id=%#x total=%d off=%d mt=%d", ch.Id(), ch.TotalSize(), ch.Offset(), ch.MsgType())
			}
			if !bytes.Equal(ch.DataBytes(), c.data) {
				t.Fatalf("decoded data mismatch")
			}
		})
	}
}

// collectFrames runs Sender.Send and decodes every delivered frame.
func collectFrames(t *testing.T, payload []byte, id uint64, chunkSize int, msgType uint32) []*protocol.Chunk {
	t.Helper()
	var frames []*protocol.Chunk
	s := &Sender{ChunkSize: chunkSize}
	send := func(frame []byte) error {
		cp := append([]byte(nil), frame...) // frame buffer is reused; copy
		frames = append(frames, protocol.GetRootAsChunk(cp, 0))
		return nil
	}
	if err := s.Send(payload, id, msgType, send, NoRetry); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return frames
}

// TestSender_SplitBoundaries covers payload < chunkSize (single frame), ==
// boundary (single frame, exact fill), and > chunkSize (multi-frame): asserting
// frame count, offset progression, last-frame size, and lossless reassembly.
func TestSender_SplitBoundaries(t *testing.T) {
	const cs = 100
	const mt = 138
	id := uint64(0xABCD)

	cases := []struct {
		name       string
		size       int
		wantFrames int
		wantLast   int
	}{
		{"empty", 0, 0, 0},
		{"sub-chunk", cs - 1, 1, cs - 1},
		{"exact boundary", cs, 1, cs},
		{"one over", cs + 1, 2, 1},
		{"two and a half", cs*2 + cs/2, 3, cs / 2},
		{"exact multiple", cs * 3, 3, cs},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload := make([]byte, c.size)
			for i := range payload {
				payload[i] = byte(i % 251)
			}
			frames := collectFrames(t, payload, id, cs, mt)
			if len(frames) != c.wantFrames {
				t.Fatalf("frame count = %d, want %d", len(frames), c.wantFrames)
			}
			if c.wantFrames == 0 {
				return
			}

			reassembled := make([]byte, c.size)
			wantOffset := 0
			for i, ch := range frames {
				if ch.Id() != id {
					t.Fatalf("frame %d id = %#x, want %#x", i, ch.Id(), id)
				}
				if ch.MsgType() != mt {
					t.Fatalf("frame %d msgType = %d, want %d", i, ch.MsgType(), mt)
				}
				if ch.TotalSize() != uint32(c.size) {
					t.Fatalf("frame %d total = %d, want %d", i, ch.TotalSize(), c.size)
				}
				if int(ch.Offset()) != wantOffset {
					t.Fatalf("frame %d offset = %d, want %d", i, ch.Offset(), wantOffset)
				}
				seg := ch.DataBytes()
				if i == len(frames)-1 && len(seg) != c.wantLast {
					t.Fatalf("last frame size = %d, want %d", len(seg), c.wantLast)
				}
				if i != len(frames)-1 && len(seg) != cs {
					t.Fatalf("non-last frame %d size = %d, want %d", i, len(seg), cs)
				}
				copy(reassembled[wantOffset:], seg)
				wantOffset += len(seg)
			}
			if wantOffset != c.size {
				t.Fatalf("reassembled %d bytes, want %d", wantOffset, c.size)
			}
			if !bytes.Equal(reassembled, payload) {
				t.Fatal("reassembled payload differs from original")
			}
		})
	}
}

// TestSender_RetryPolicy verifies the optional retry wrapper: with retry on, a
// SendFunc that fails the first N attempts then succeeds is ridden out; with
// retry off (NoRetry), the first error is returned immediately and no further
// attempts are made.
func TestSender_RetryPolicy(t *testing.T) {
	// Replace real sleeps so the backoff path runs instantly.
	orig := sleepFn
	sleepFn = func(time.Duration) {}
	defer func() { sleepFn = orig }()

	transient := errors.New("buffer full")

	t.Run("retry rides out transient failures", func(t *testing.T) {
		var attempts int
		send := func([]byte) error {
			attempts++
			if attempts < 3 {
				return transient
			}
			return nil
		}
		s := &Sender{ChunkSize: 1000}
		policy := RetryPolicy{Attempts: 5, BaseBackoff: time.Millisecond}
		if err := s.Send([]byte("small"), 1, 138, send, policy); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if attempts != 3 {
			t.Fatalf("attempts = %d, want 3 (2 fail + 1 success)", attempts)
		}
	})

	t.Run("retry exhausted returns last error", func(t *testing.T) {
		var attempts int
		send := func([]byte) error { attempts++; return transient }
		s := &Sender{ChunkSize: 1000}
		policy := RetryPolicy{Attempts: 4, BaseBackoff: time.Millisecond}
		err := s.Send([]byte("small"), 1, 138, send, policy)
		if !errors.Is(err, transient) {
			t.Fatalf("err = %v, want wrapped transient", err)
		}
		if attempts != 4 {
			t.Fatalf("attempts = %d, want 4", attempts)
		}
	})

	t.Run("NoRetry fails immediately", func(t *testing.T) {
		var attempts int
		send := func([]byte) error { attempts++; return transient }
		s := &Sender{ChunkSize: 1000}
		err := s.Send([]byte("small"), 1, 138, send, NoRetry)
		if !errors.Is(err, transient) {
			t.Fatalf("err = %v, want wrapped transient", err)
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1 (no retry)", attempts)
		}
	})

	t.Run("retry aborts whole transfer on a mid-stream chunk failure", func(t *testing.T) {
		// 3 chunks; fail permanently starting at the 2nd chunk's first send.
		var frameStarts int
		send := func(frame []byte) error {
			ch := protocol.GetRootAsChunk(append([]byte(nil), frame...), 0)
			if ch.Offset() == 0 {
				return nil
			}
			frameStarts++
			return transient
		}
		s := &Sender{ChunkSize: 100}
		payload := make([]byte, 250)
		err := s.Send(payload, 7, 138, send, RetryPolicy{Attempts: 2, BaseBackoff: time.Millisecond})
		if err == nil {
			t.Fatal("expected abort error on second chunk")
		}
		if !errors.Is(err, transient) {
			t.Fatalf("err = %v, want wrapped transient", err)
		}
		// Second chunk retried twice (Attempts=2) then aborted; third never sent.
		if frameStarts != 2 {
			t.Fatalf("offset>0 send attempts = %d, want 2", frameStarts)
		}
	})
}

// TestDefaultChunkSize pins the single-source constant AND the geometry it is
// derived from. Both pkg/server and pkg/rtd alias this.
//
// Regression for the 2026-07-25 P0: the constant was 950*1024 = 972800, derived
// as "1 MiB slot payload minus overhead" — which SILENTLY DROPPED the host's
// `payloadSize / 2` request/response split. The real guest->host request buffer
// is 512 KiB (524288), so 972800 was 1.86x too large and shm rejected EVERY
// chunk frame with "data too large".
func TestDefaultChunkSize(t *testing.T) {
	if SlotPayloadSize != 1024*1024 {
		t.Fatalf("SlotPayloadSize = %d, want %d (hostCfg.payloadSize in xll_main.cpp.tmpl)", SlotPayloadSize, 1024*1024)
	}
	// The /2 that the old constant missed. DirectHost.h: halfSize = slotSize/2.
	if HalfSlotSize != SlotPayloadSize/2 {
		t.Fatalf("HalfSlotSize = %d, want SlotPayloadSize/2 = %d", HalfSlotSize, SlotPayloadSize/2)
	}
	if HalfSlotSize != 524288 {
		t.Fatalf("HalfSlotSize = %d, want 524288", HalfSlotSize)
	}
	if want := HalfSlotSize - FramingOverhead; DefaultChunkSize != want {
		t.Fatalf("DefaultChunkSize = %d, want HalfSlotSize-FramingOverhead = %d", DefaultChunkSize, want)
	}
	// The invariant that actually matters on the wire.
	if DefaultChunkSize+FramingOverhead > HalfSlotSize {
		t.Fatalf("chunk payload %d + framing %d exceeds the %d-byte request buffer",
			DefaultChunkSize, FramingOverhead, HalfSlotSize)
	}
	if DefaultChunkSize >= 950*1024 {
		t.Fatalf("DefaultChunkSize = %d: back at/above the buggy 950 KiB value that overflows the 512 KiB request buffer", DefaultChunkSize)
	}
}

// TestFramingOverheadIsAnUpperBound measures the REAL bytes BuildFrame adds
// around a chunk payload and asserts FramingOverhead bounds it. Without this
// the overhead constant is an unverified guess, and a chunk sized to
// bufCap-FramingOverhead could still overflow bufCap after framing.
func TestFramingOverheadIsAnUpperBound(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	// Payload sizes spanning every FlatBuffers alignment/padding case, plus the
	// real DefaultChunkSize, plus values whose vector length needs all 4 bytes.
	sizes := []int{0, 1, 2, 3, 4, 7, 8, 15, 16, 63, 64, 255, 256, 257, 4095, 4096, 65535, 65536, DefaultChunkSize}
	worst := 0
	for _, n := range sizes {
		payload := make([]byte, n)
		// Max-valued header fields so no small-value encoding shortcut hides bytes.
		frame := BuildFrame(b, payload, ^uint64(0), int(^uint32(0)), int(^uint32(0)-1), ^uint32(0))
		over := len(frame) - n
		if over > worst {
			worst = over
		}
		if over > FramingOverhead {
			t.Fatalf("payload %d bytes framed to %d (overhead %d) — exceeds FramingOverhead=%d",
				n, len(frame), over, FramingOverhead)
		}
		if n+over > HalfSlotSize && n <= DefaultChunkSize {
			t.Fatalf("a %d-byte chunk framed to %d, overflowing the %d-byte request buffer", n, len(frame), HalfSlotSize)
		}
	}
	t.Logf("worst-case measured framing overhead: %d bytes (bound %d)", worst, FramingOverhead)
}

// TestBudget covers the shared budget arithmetic used by every chunking site.
func TestBudget(t *testing.T) {
	cases := []struct {
		name   string
		bufCap int
		want   int
	}{
		{"real 512 KiB request buffer", HalfSlotSize, DefaultChunkSize},
		{"1 MiB buffer caps at default", SlotPayloadSize, DefaultChunkSize},
		{"huge buffer caps at default", 64 << 20, DefaultChunkSize},
		{"small buffer scales down", 4096, 4096 - FramingOverhead},
		{"buffer below overhead floors at 1", FramingOverhead - 5, 1},
		{"zero floors at 1", 0, 1},
		{"negative floors at 1", -1, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Budget(c.bufCap); got != c.want {
				t.Fatalf("Budget(%d) = %d, want %d", c.bufCap, got, c.want)
			}
		})
	}
}

// ExampleBuildFrame keeps the godoc example compiling; also documents the frame
// file identifier so reviewers see the "XCHN" contract at a glance.
func ExampleBuildFrame() {
	b := flatbuffers.NewBuilder(0)
	frame := BuildFrame(b, []byte("hi"), 1, 2, 0, 138)
	fmt.Println(string(frame[4:8])) // file identifier is at bytes [4,8)
	// Output: XCHN
}
