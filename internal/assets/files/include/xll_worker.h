#pragma once

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <iterator> // std::prev over the segment map
#include <map>
#include <utility>  // std::move into the partial-message map
#include <vector>

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

// ---------------------------------------------------------------------------
// Chunk transfer bookkeeping — the registry the arbiter above runs inside.
// ---------------------------------------------------------------------------
//
// CO-CHANGE ANCHOR (AGENTS.md §18.6.1): this is the C++ mirror of Go's
// ChunkManager (pkg/server/manager.go) — GetChunkBuffer's admission rules,
// PoisonTransfer/IsPoisoned, and pruneStaleChunkBuffersLocked.
//
// It is extracted here for the SAME reason ClaimChunkSegment is: every rule
// below is pure bookkeeping over two std::maps, but living in xll_worker.cpp it
// could only ever be compile-checked, because that translation unit reaches
// xlAsyncReturn / COM / shm and cannot be linked offline. The 2026-07-26
// hardening (zero-total refusal, the total-size cap, prune-then-refuse at the
// transfer bound, the poison set with its TTL and oldest-eviction) shipped on a
// compile gate plus review. As a pure type it is exercised by
// internal/assets/testdata/chunk_registry_native_test.cpp under a bare g++,
// against the same case table internal/assets/chunk_cpp_test.go replays through
// the Go ChunkManager.
//
// NOT THREAD-SAFE, deliberately: xll_worker.cpp serializes every call under
// g_partialMessagesMutex, and taking a lock in here would make the offline gate
// depend on <mutex> for nothing. Keep the locking in the caller.
//
// TIME IS AN ARGUMENT, not a call to steady_clock::now(). Every TTL decision
// takes `now` from the caller, so the offline gate drives expiry deterministically
// instead of sleeping. The shipped caller passes steady_clock::now().

using ChunkTime = std::chrono::steady_clock::time_point;
using ChunkDuration = std::chrono::steady_clock::duration;

// Idle window after which a partially-reassembled transfer — or a poison-set
// entry — is reclaimed. Mirrors server.DefaultChunkBufferTTL (60s); unlike the
// Go twin this has no YAML wiring (see the tunability note in xll_worker.cpp).
inline constexpr std::chrono::seconds kChunkStaleTtl{60};

// Upper bound on the wire-supplied total_size a single inbound transfer may
// declare. The wire value is producer-controlled, so an unbounded allocation is
// a DoS vector. Mirrors the DEFAULT of the Go twin
// server.DefaultMaxChunkBufferBytes (256 MiB) — "default", because the Go side
// is per-project configurable (`xll.yaml server.chunk.max_buffer_bytes`) and
// this one is not. Also mirrored by the Go SENDER's chunk.MaxTransferBytes,
// which refuses an over-cap payload before framing it, because a sender cannot
// discover this number at runtime.
inline constexpr uint64_t kMaxChunkTotalSize = 256ull * 1024 * 1024;

// Upper bound on the NUMBER of partially-reassembled transfers held at once.
// kMaxChunkTotalSize caps ONE transfer; this caps the COUNT. The two do NOT
// compose into an aggregate-byte guard — they MULTIPLY (256 MiB x 1024 =
// 256 GiB worst case), and a producer that touches each transfer inside
// kChunkStaleTtl keeps the sweep from reclaiming anything. Mirrors
// server.DefaultMaxConcurrentTransfers and shm's maxConcurrentStreams
// (SPECIFICATION.md §3.3.4).
inline constexpr size_t kMaxPartialMessages = 1024;

// Bound on the poison set, mirroring the partial-message bound (and
// server.DefaultMaxPoisonedTransfers). Entries are tiny and expire with the
// TTL, but a peer spraying distinct bad ids must not grow the map without limit.
inline constexpr size_t kMaxPoisonedTransfers = 1024;

// PartialMessage is one transfer being reassembled.
struct PartialMessage {
    std::vector<uint8_t> buffer;
    size_t receivedSize = 0;
    size_t totalSize = 0;
    int32_t finalMsgType = 0;
    // Received byte ranges, keyed offset -> length, held disjoint by
    // ClaimChunkSegment above. It does two jobs:
    //   (a) DEDUP — an exact (offset, length) repeat is a retransmit (e.g.
    //       after a dropped ACK) and must skip BOTH the copy and the
    //       receivedSize advance, or the duplicate pushes receivedSize past
    //       totalSize and trips premature completion (AGENTS.md §23.3);
    //   (b) OVERLAP REJECTION — the old dedup keyed on offset ALONE, so
    //       partially-overlapping ranges (total=100 with (0,60) then (50,40))
    //       were both accepted, summed to totalSize, and reported COMPLETE
    //       while [90,100) had never been written. The consumer then read
    //       zero-fill as payload. Disjoint ranges + the exact-sum completion
    //       test are together the equivalent of shm's "every chunk exactly once
    //       AND Sum(payloadSize) == totalSize" (shm SPECIFICATION.md §3.3.4).
    // Note that `buffer` is zero-initialised by resize(), which is why a
    // coverage gap read as zeros rather than as leaked heap. That zero-fill is
    // load-bearing ONLY while the coverage contract is absent; with the
    // contract enforced it becomes unreachable belt-and-braces. Do not remove
    // one on the strength of the other.
    std::map<uint32_t, uint32_t> receivedSegments;
    ChunkTime lastUpdate;
};

// The verdict ChunkRegistry::Admit returns for an arriving chunk's transfer id.
enum class ChunkAdmission {
    // A new PartialMessage was created (buffer allocated, totalSize recorded)
    // and handed back through `entry`.
    Opened,
    // An entry for this id was already open; `entry` points at it and its
    // lastUpdate has been refreshed. NOTE the declared total_size is NOT
    // re-validated for an existing entry — the FIRST total wins for the life of
    // the entry. That is the documented, INTENTIONAL divergence from Go's
    // GetChunkBuffer, which resets the buffer in place on a total mismatch
    // (AGENTS.md §18.6.1). Do not "align" one side alone.
    Existing,
    // A previous chunk of this transfer was refused for a PROTOCOL VIOLATION
    // and the id is still inside the poison window. Refuse without touching
    // state. Mirrors Go's ChunkManager.IsPoisoned check in HandleChunk.
    RefusedPoisoned,
    // total_size == 0. A zero-size buffer satisfies the `0 == 0` completion
    // test immediately and hands GetRoot<> a NULLPTR -> access violation inside
    // Excel. Mirrors Go's GetChunkBuffer `total <= 0` refusal.
    RefusedZeroTotal,
    // total_size > maxTotalSize. Mirrors Go's MaxChunkBufferBytes refusal.
    RefusedTotalTooLarge,
    // At maxTransfers even after pruning stale entries. Mirrors Go's
    // MaxConcurrentTransfers refusal.
    RefusedTooManyTransfers,
};

// ChunkRegistry owns the two maps the guest->host reassembler keeps: the
// in-flight partial messages and the poison set.
//
// The caps are constructor parameters rather than hard-wired constants so the
// offline gate can drive every boundary (the transfer-count bound, the
// poison-set bound and its oldest-eviction) with three-entry maps instead of
// 1024-entry ones. The default constructor uses the shipped values, and that is
// what xll_worker.cpp instantiates.
class ChunkRegistry {
public:
    ChunkRegistry() = default;
    ChunkRegistry(uint64_t maxTotalSize, size_t maxTransfers, size_t maxPoisoned, ChunkDuration ttl)
        : maxTotalSize_(maxTotalSize), maxTransfers_(maxTransfers), maxPoisoned_(maxPoisoned), ttl_(ttl) {}

    // Admit resolves the transfer id an arriving chunk carries, creating the
    // PartialMessage on first touch. On Opened/Existing it stores the entry in
    // *entry and refreshes its lastUpdate; on every Refused* verdict it stores
    // nothing there and leaves both maps unchanged EXCEPT for two documented
    // reclaims: an EXPIRED poison entry is dropped, and the transfer-count
    // bound prunes stale entries before refusing.
    //
    // The check order is part of the mirror and must not be reshuffled — it is
    // the order Go's HandleChunk + GetChunkBuffer apply: poison, then existing
    // entry, then zero total, then the size cap, then the count cap.
    ChunkAdmission Admit(uint64_t id, uint32_t totalSize, int32_t msgType,
                         ChunkTime now, PartialMessage** entry) {
        // Poisoned id: refuse everything on it until the entry expires, instead
        // of letting this chunk resurrect a fresh (permanently incomplete)
        // buffer that is then acked as success — which would make the producer's
        // retry ladder see SUCCESS and never abort.
        auto pit = poisoned_.find(id);
        if (pit != poisoned_.end()) {
            if (now - pit->second <= ttl_) {
                return ChunkAdmission::RefusedPoisoned;
            }
            // Expired: the id is reusable from scratch.
            poisoned_.erase(pit);
        }

        auto it = partials_.find(id);
        if (it != partials_.end()) {
            it->second.lastUpdate = now;
            if (entry) *entry = &it->second;
            return ChunkAdmission::Existing;
        }

        if (totalSize == 0) {
            return ChunkAdmission::RefusedZeroTotal;
        }
        if (static_cast<uint64_t>(totalSize) > maxTotalSize_) {
            return ChunkAdmission::RefusedTotalTooLarge;
        }
        // Concurrent-transfer bound: PRUNE STALE FIRST, then refuse. Buffers
        // abandoned by a peer that stopped mid-transfer are exactly what we want
        // to reclaim at the bound, and the periodic sweep may be seconds away.
        // No LRU eviction: dropping a live transfer to admit a new one just
        // moves the failure onto an innocent producer. Same policy as shm's C++
        // StreamReassembler::Handle and Go's GetChunkBuffer.
        if (partials_.size() >= maxTransfers_) {
            Prune(now);
            if (partials_.size() >= maxTransfers_) {
                return ChunkAdmission::RefusedTooManyTransfers;
            }
        }

        PartialMessage pm;
        pm.totalSize = totalSize;
        pm.receivedSize = 0;
        pm.finalMsgType = msgType;
        pm.buffer.resize(pm.totalSize);
        pm.lastUpdate = now;
        it = partials_.insert({id, std::move(pm)}).first;
        if (entry) *entry = &it->second;
        return ChunkAdmission::Opened;
    }

    // Complete drops the entry for id WITHOUT poisoning: the plain removal used
    // when a transfer finished. Mirrors Go's RemoveChunkBuffer.
    void Complete(uint64_t id) { partials_.erase(id); }

    // Poison drops the entry for id AND records the id as refused, so every
    // later chunk carrying it is rejected until the record expires. Call it from
    // the PROTOCOL-VIOLATION paths only — resource refusals (zero total, size
    // cap, count cap) must NOT poison: they insert no buffer, so there is
    // nothing to resurrect, and a later retry may legitimately succeed.
    //
    // INVALIDATES any PartialMessage* Admit handed out for id.
    //
    // At the poison-set bound it prunes first and, if that frees nothing, drops
    // the OLDEST entry: the set is a fail-fast accelerator, not a correctness
    // invariant, so losing one entry only restores the pre-existing
    // resurrect-until-TTL behavior for that single id. Mirrors Go's
    // PoisonTransfer exactly, including that order.
    void Poison(uint64_t id, ChunkTime now) {
        partials_.erase(id);
        if (poisoned_.size() >= maxPoisoned_) {
            Prune(now);
            if (poisoned_.size() >= maxPoisoned_) {
                auto oldest = poisoned_.begin();
                for (auto i = poisoned_.begin(); i != poisoned_.end(); ++i) {
                    if (i->second < oldest->second) oldest = i;
                }
                poisoned_.erase(oldest);
            }
        }
        poisoned_[id] = now;
    }

    // IsPoisoned reports whether id is inside the poison window at `now`,
    // without mutating anything. Admit is the path that expires entries; this is
    // for diagnostics and tests.
    bool IsPoisoned(uint64_t id, ChunkTime now) const {
        auto it = poisoned_.find(id);
        return it != poisoned_.end() && (now - it->second) <= ttl_;
    }

    // Prune drops every partial transfer idle for longer than the TTL and
    // expires poison entries on the same clock, so an id refused for a protocol
    // violation becomes reusable after the window instead of being burned for
    // the life of the process. Backs both the periodic sweep and the on-demand
    // reclaim Admit/Poison perform at their bounds. Mirrors Go's
    // pruneStaleChunkBuffersLocked (which likewise sweeps BOTH maps).
    void Prune(ChunkTime now) {
        for (auto it = partials_.begin(); it != partials_.end(); ) {
            if (now - it->second.lastUpdate > ttl_) {
                it = partials_.erase(it);
            } else {
                ++it;
            }
        }
        for (auto it = poisoned_.begin(); it != poisoned_.end(); ) {
            if (now - it->second > ttl_) {
                it = poisoned_.erase(it);
            } else {
                ++it;
            }
        }
    }

    size_t transferCount() const { return partials_.size(); }
    size_t poisonCount() const { return poisoned_.size(); }
    uint64_t maxTotalSize() const { return maxTotalSize_; }
    size_t maxTransfers() const { return maxTransfers_; }
    size_t maxPoisoned() const { return maxPoisoned_; }

private:
    std::map<uint64_t, PartialMessage> partials_;
    std::map<uint64_t, ChunkTime> poisoned_;
    uint64_t maxTotalSize_ = kMaxChunkTotalSize;
    size_t maxTransfers_ = kMaxPartialMessages;
    size_t maxPoisoned_ = kMaxPoisonedTransfers;
    ChunkDuration ttl_ = kChunkStaleTtl;
};

} // namespace xll
