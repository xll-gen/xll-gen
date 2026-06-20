package server

import (
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/chunk"
)

// BuildAckResponse constructs a standard ACK message using the provided builder.
// It returns the finished bytes.
func BuildAckResponse(b *flatbuffers.Builder, id uint64, ok bool) []byte {
	b.Reset()
	protocol.AckStart(b)
	if id > 0 {
		protocol.AckAddId(b, id)
	}
	protocol.AckAddOk(b, ok)
	root := protocol.AckEnd(b)
	b.Finish(root)
	return b.FinishedBytes()
}

// BuildChunkResponse constructs a Chunk message using the provided builder.
// It returns the finished bytes.
//
// This is a thin alias of chunk.BuildFrame (the single source of truth for the
// chunk wire layout, shared with pkg/rtd). Kept as a named function in
// pkg/server because SendAckOrChunk and the ACK-pull resend path call it
// directly; the frame bytes are byte-identical to the historical hand-built
// form so the C++ HandleChunk reassembler is unaffected. See AGENTS.md §18.4.
func BuildChunkResponse(b *flatbuffers.Builder, chunkData []byte, id uint64, totalSize int, offset int, msgType uint32) []byte {
	return chunk.BuildFrame(b, chunkData, id, totalSize, offset, msgType)
}
