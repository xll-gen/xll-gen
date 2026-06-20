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

// TestDefaultChunkSize pins the single-source constant. Both server and rtd now
// alias this; if it drifts, the C++ slot-geometry assumption (1 MiB - overhead)
// is violated.
func TestDefaultChunkSize(t *testing.T) {
	if DefaultChunkSize != 950*1024 {
		t.Fatalf("DefaultChunkSize = %d, want %d", DefaultChunkSize, 950*1024)
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
