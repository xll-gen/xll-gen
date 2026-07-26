#pragma once

#include <cstdint>
#include <iterator> // std::prev over the segment map
#include <map>

namespace xll {
    void StartWorker();
    void StopWorker();
    void JoinWorker();
    void ForceTerminateWorker();

// ---------------------------------------------------------------------------
// Chunk segment bookkeeping — the guest->host reassembler's overlap arbiter.
// ---------------------------------------------------------------------------
//
// CO-CHANGE ANCHOR (AGENTS.md §18.6.1): this is the C++ mirror of Go's
// ChunkBuffer.ClaimSegment (pkg/server/types.go) plus the two caller-side
// guards HandleChunk applies ahead of it (pkg/server/handlers.go: the bounds
// check, then the zero-length refusal). The two reassemblers run in OPPOSITE
// directions and share no runtime state, so the ONLY thing keeping them
// aligned is that they implement the same rules — which is exactly what an
// offline test can pin.
//
// WHY IT LIVES IN A HEADER AS A PURE FUNCTION. xll_worker.cpp cannot be linked
// outside the XLL: HandleChunk's completion path reaches ProcessAsyncBatchResponse
// (xlAsyncReturn), ProcessRtdUpdate (COM IRTDUpdateEvent) and the shm host, and
// WorkerLoop pulls in the whole IPC stack. Keeping the classification here — no
// Excel, no FlatBuffers, no shm, no logging, no globals — lets
// internal/assets/testdata/chunk_segments_native_test.cpp compile and RUN it
// with a bare g++ and gives the segment rules the same offline gate
// xll_cache.cpp has, instead of "the cmake build succeeded".
//
// It is a free function over a caller-owned map rather than a method so the
// test never has to construct a PartialMessage (which owns a heap buffer and a
// steady_clock timestamp it does not need).
enum class ChunkSegmentClaim {
    // Disjoint from everything received so far, and RECORDED in `segments`:
    // the caller MUST copy the payload and advance receivedSize.
    New,
    // The exact same (offset, length) range already arrived — a benign
    // retransmit (e.g. after a dropped ACK). The caller MUST skip BOTH the
    // copy and the receivedSize advance, or the repeat pushes receivedSize
    // past totalSize and trips premature completion (AGENTS.md §23.3).
    Duplicate,
    // Partially overlaps an already-received range, INCLUDING the same start
    // offset with a different length (a producer that re-chunked mid-transfer).
    // PROTOCOL VIOLATION: the caller MUST drop and poison the transfer and
    // answer SYSTEM_ERROR. Mirrors Go's ClaimOverlap.
    Overlap,
    // A present-but-empty payload. Refused rather than recorded: it advances
    // nothing, so it can never be part of a valid transfer, and an (offset, 0)
    // entry would make the REAL chunk at that offset classify as Overlap and
    // kill an otherwise healthy transfer. Mirrors handlers.go's `dataLen == 0`.
    ZeroLength,
    // [offset, offset+length) does not fit inside [0, totalSize). PROTOCOL
    // VIOLATION, same handling as Overlap. Mirrors handlers.go's bounds check.
    OutOfBounds,
};

// ClaimChunkSegment classifies the arriving range [offset, offset+length)
// against the ranges already recorded in `segments` (offset -> length, held
// ascending and pairwise disjoint), recording it when — and only when — the
// verdict is New. Every reject verdict leaves `segments` untouched.
//
// The check ORDER is part of the mirror and must not be reshuffled: bounds
// first, then zero-length, then overlap — the same order handlers.go applies
// (a zero-length frame past the end is reported as out of bounds on both
// sides).
//
// All types are the WIRE types: protocol.fbs declares Chunk.offset and
// Chunk.total_size as uint32, so totalSize/offset/length are uint32 and every
// sum that could leave the range is widened to uint64 first.
inline ChunkSegmentClaim ClaimChunkSegment(std::map<uint32_t, uint32_t>& segments,
                                           uint32_t totalSize,
                                           uint32_t offset,
                                           uint32_t length) {
    // Bounds. Subtraction form: the additive check (offset + length > total)
    // can wrap for wire-supplied values near the unsigned max and pass
    // validation while the memcpy writes out of bounds.
    if (offset > totalSize || length > totalSize - offset) {
        return ChunkSegmentClaim::OutOfBounds;
    }
    if (length == 0) {
        return ChunkSegmentClaim::ZeroLength;
    }

    // std::map is ordered, so lower_bound gives the two neighbours the
    // arriving range has to be checked against — the equivalent of Go's
    // sort.Search over the ascending Segments slice.
    auto seg = segments.lower_bound(offset);
    if (seg != segments.end() && seg->first == offset) {
        // Same start offset: an exact-length repeat is the retransmit case,
        // anything else is a producer that re-chunked mid-transfer.
        return (seg->second == length) ? ChunkSegmentClaim::Duplicate
                                       : ChunkSegmentClaim::Overlap;
    }
    // The predecessor must end at or before us (touching is fine).
    if (seg != segments.begin()) {
        auto prev = std::prev(seg);
        if (static_cast<uint64_t>(prev->first) + prev->second > offset) {
            return ChunkSegmentClaim::Overlap;
        }
    }
    // ...and we must end at or before the successor's start.
    if (seg != segments.end() &&
        static_cast<uint64_t>(offset) + length > seg->first) {
        return ChunkSegmentClaim::Overlap;
    }

    segments.emplace(offset, length);
    return ChunkSegmentClaim::New;
}

} // namespace xll
