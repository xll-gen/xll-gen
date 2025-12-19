package server

import (
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
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
func BuildChunkResponse(b *flatbuffers.Builder, chunkData []byte, id uint64, totalSize int, offset int, msgType uint32) []byte {
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
