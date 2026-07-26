#include "xll_ipc.h"
#include "types/converters.h"
#include "types/utility.h"
#include "xll_worker.h"
#include "xll_log.h"
#include "xll_lifecycle.h"
#include "xll_async.h"
#include <windows.h>
#include <vector>
#include <string>
#include <map>
#include <mutex>
#include <chrono>
#include <thread>
#include <cstring>  // std::memcpy in HandleChunk
#include <iterator> // std::prev over receivedSegments

#ifdef XLL_RTD_ENABLED
#include "rtd/rtd.h" // Needed for IRTDUpdateEvent
// External declarations
void ProcessRtdUpdate(const protocol::RtdUpdate* update);
// Guest->host one-shot grid delivery: caches the result bytes in
// RtdOnceGridRegistry. `buf`/`len` is the full serialized
// protocol::RtdOnceGridResult buffer (see xll_rtd.cpp for the byte contract).
void ProcessRtdOnceGrid(const uint8_t* buf, size_t len);
#endif

// External declaration
void ExecuteCommands(const flatbuffers::Vector<flatbuffers::Offset<protocol::CommandWrapper>>* commands);

namespace xll {

std::atomic<bool> g_workerRunning = false;
std::thread g_workerThread;

// Chunk Reassembly Logic
//
// CO-CHANGE ANCHOR (§18.6 style): this mirrors the Go-side reassembler in
// pkg/server/manager.go (ChunkBuffer) + pkg/server/handlers.go (HandleChunk).
// Keep the two in lockstep:
//   - receivedSegments here    <->  ChunkBuffer.Segments + ClaimSegment
//   - kMaxChunkTotalSize       <->  server.DefaultMaxChunkBufferBytes (256 MiB)
//   - kMaxPartialMessages      <->  server.DefaultMaxConcurrentTransfers (1024)
//   - the == completion test   <->  handlers.go `buf.Received == buf.TotalSize`
//   - the len == 0 refusal     <->  handlers.go's `dataLen == 0` refusal
//   - g_poisonedTransfers      <->  ChunkManager.poisoned / PoisonTransfer /
//                                   IsPoisoned (pkg/server/manager.go)
//   - every reject path returns SYSTEM_ERROR <-> handlers.go's
//     shm.MsgTypeSystemError returns
// The offset type matches protocol::Chunk::offset() / total_size(), which the
// FlatBuffers schema (protocol.fbs) declares as uint32.
struct PartialMessage {
    std::vector<uint8_t> buffer;
    size_t receivedSize;
    size_t totalSize;
    int32_t finalMsgType;
    // Received byte ranges, keyed offset -> length, held disjoint. std::map is
    // ordered, so lower_bound gives the two neighbours an arriving range has to
    // be checked against. This is the C++ mirror of Go's
    // ChunkBuffer.Segments/ClaimSegment and it does two jobs:
    //   (a) DEDUP — an exact (offset, length) repeat is a retransmit (e.g.
    //       after a dropped ACK) and must skip BOTH the copy and the
    //       receivedSize advance, or the duplicate pushes receivedSize past
    //       totalSize and trips premature completion (AGENTS.md §23.3);
    //   (b) OVERLAP REJECTION — the old dedup keyed on offset ALONE, so
    //       partially-overlapping ranges (total=100 with (0,60) then (50,40))
    //       were both accepted, summed to totalSize, and reported COMPLETE
    //       while [90,100) had never been written. The consumer then read
    //       zero-fill as payload. Disjoint ranges + the exact-sum completion
    //       test below are together the equivalent of shm's "every chunk
    //       exactly once AND Sum(payloadSize) == totalSize"
    //       (shm SPECIFICATION.md §3.3.4).
    // Note that pm.buffer is zero-initialised by resize(), which is why a
    // coverage gap read as zeros rather than as leaked heap. That zero-fill is
    // load-bearing ONLY while the coverage contract is absent; with the
    // contract enforced it becomes unreachable belt-and-braces. Do not remove
    // one on the strength of the other.
    std::map<uint32_t, uint32_t> receivedSegments;
    std::chrono::steady_clock::time_point lastUpdate;
};

std::map<uint64_t, PartialMessage> g_partialMessages;
std::mutex g_partialMessagesMutex;

// Idle window after which a partially-reassembled transfer (and a poison-set
// entry, below) is reclaimed. Named because two places now depend on it.
static constexpr std::chrono::seconds kChunkStaleTtl{60};

// POISON SET — transfer ids whose reassembly was REFUSED for a protocol
// violation, held with the time of refusal.
//
// Without it a rejection is not final. Every reject path below erases the
// PartialMessage, so the producer's NEXT chunk for the same id (offset != 0)
// finds `it == g_partialMessages.end()` and RESURRECTS a fresh buffer — which
// is then acked as success. Two consequences, both the opposite of the
// fail-fast this refusal exists for:
//   (a) the resurrected buffer is missing every earlier chunk, so it can never
//       complete and squats a kMaxPartialMessages slot for the whole TTL;
//   (b) the Go producer's retry ladder (pkg/chunk AsyncRetry, 10x exponential
//       backoff) gets a SUCCESS on its first retry, so it never aborts and
//       keeps pushing — the async call hangs until its own timeout.
// Remembering the id turns every subsequent chunk of a poisoned transfer into
// SYSTEM_ERROR, so the producer stops on the first retry.
//
// Only PROTOCOL VIOLATIONS poison (out-of-bounds, zero-length, overlap): they
// are deterministic properties of the producer's framing, so retrying the same
// id cannot help. RESOURCE refusals (total_size cap, kMaxPartialMessages) do
// NOT poison — they are transient, they insert no buffer so there is nothing to
// resurrect, and a later retry may legitimately succeed.
//
// Mirrors ChunkManager.poisoned in pkg/server/manager.go (CO-CHANGE ANCHOR,
// §18.6): same TTL semantics, same set of poisoning paths.
static std::map<uint64_t, std::chrono::steady_clock::time_point> g_poisonedTransfers;

// Bound on the poison set, mirroring the partial-message bound. Entries are 16
// bytes and expire with the TTL, but a peer spraying distinct bad ids must not
// grow the map without limit.
static constexpr size_t kMaxPoisonedTransfers = 1024;

// Upper bound on the wire-supplied total_size a single inbound transfer may
// declare, mirroring the Go guest's server.DefaultMaxChunkBufferBytes
// (256 MiB, pkg/server/manager.go). Previously C++ capped at 128 MiB while Go
// accepted up to 256 MiB, so a ~200 MB response was accepted by Go but
// silently dropped here. Keep these two in lockstep (CO-CHANGE ANCHOR, §18.6).
static constexpr uint64_t kMaxChunkTotalSize = 256ull * 1024 * 1024;

// Upper bound on the NUMBER of partially-reassembled transfers held at once.
// kMaxChunkTotalSize caps ONE transfer; without a count bound the aggregate is
// still unbounded, and the only reclaim was the 60 s staleness sweep that runs
// every 10 s. Mirrors server.DefaultMaxConcurrentTransfers and shm's
// maxConcurrentStreams (SPECIFICATION.md §3.3.4). Reclaim policy matches shm's
// C++ StreamReassembler::Handle: at the bound, prune stale entries first and
// only refuse if that frees nothing (no LRU eviction — dropping a live transfer
// to admit a new one just moves the failure onto an innocent producer).
static constexpr size_t kMaxPartialMessages = 1024;

static void PruneStaleChunksLocked();

// PoisonTransferLocked records msgId as refused. Caller MUST hold
// g_partialMessagesMutex and MUST have already erased any PartialMessage.
static void PoisonTransferLocked(uint64_t msgId) {
    const auto now = std::chrono::steady_clock::now();
    if (g_poisonedTransfers.size() >= kMaxPoisonedTransfers) {
        PruneStaleChunksLocked();
        if (g_poisonedTransfers.size() >= kMaxPoisonedTransfers) {
            // Still full of live entries: drop the oldest. The poison set is a
            // fail-fast accelerator, not a correctness invariant — the worst
            // case of losing an entry is the pre-existing resurrect-until-TTL
            // behavior for that one id.
            auto oldest = g_poisonedTransfers.begin();
            for (auto i = g_poisonedTransfers.begin(); i != g_poisonedTransfers.end(); ++i) {
                if (i->second < oldest->second) oldest = i;
            }
            g_poisonedTransfers.erase(oldest);
        }
    }
    g_poisonedTransfers[msgId] = now;
}

// HandleChunk reassembles one inbound chunk.
//
// Returns false when the chunk is REFUSED (protocol violation, resource bound,
// or unload in progress). The caller turns that into a SYSTEM_ERROR response so
// the producer fails fast; every one of these paths used to `return`/`erase`
// silently (or, for the oversized-total case, log and no more), leaving the
// producer to receive a success ack and keep pushing a transfer that could
// never complete for up to the full 60 s TTL. The Go guest has always answered
// shm.MsgTypeSystemError on the equivalent paths (handlers.go) — this closes
// the C++-only gap.
bool HandleChunk(const protocol::Chunk* chunk) {
    if (!chunk) return false;

    // If we're unloading, bail out early to avoid touching global state.
    // Refuse rather than silently drop, so an in-flight producer stops
    // retransmitting into a host that is going away. No log here: logging
    // during unload can touch freed logging resources (§20.2).
    if (g_isUnloading) return false;

    uint64_t msgId = chunk->id();

    std::lock_guard<std::mutex> lock(g_partialMessagesMutex);

    // Poisoned id: a previous chunk of this transfer was refused for a protocol
    // violation. Refuse everything else on that id until the entry expires,
    // instead of letting the next chunk resurrect a fresh (permanently
    // incomplete) buffer and be acked as success. See g_poisonedTransfers.
    {
        auto pit = g_poisonedTransfers.find(msgId);
        if (pit != g_poisonedTransfers.end()) {
            if (std::chrono::steady_clock::now() - pit->second <= kChunkStaleTtl) {
                if (!g_isUnloading) LogWarn("Chunk for a rejected transfer (id " + std::to_string(msgId) + "). Refusing.");
                return false;
            }
            // Expired: the id is reusable from scratch.
            g_poisonedTransfers.erase(pit);
        }
    }

    auto it = g_partialMessages.find(msgId);

    if (it == g_partialMessages.end()) {
        // New partial message
        // A zero total_size would resize(0) the buffer, satisfy the `0 == 0`
        // completion test immediately, and hand GetRoot<> a NULLPTR from
        // pm.buffer.data() -> access violation inside Excel. Unreachable from a
        // well-behaved Go producer, but this is the wire and the Go guest
        // already refuses it (GetChunkBuffer's `total <= 0`); keep the two
        // reject sets symmetric (CO-CHANGE ANCHOR, §18.6).
        if (chunk->total_size() == 0) {
            if (!g_isUnloading) LogWarn("Chunk declares total_size 0. Rejecting transfer.");
            return false;
        }
        // Vulnerability Fix: bound the wire-supplied total size to prevent a
        // multi-GiB allocation (DoS). Aligned to the Go guest cap; see
        // kMaxChunkTotalSize above.
        if (chunk->total_size() > kMaxChunkTotalSize) {
             if (!g_isUnloading) LogWarn("Chunk total size too large: " + std::to_string(chunk->total_size()) + " bytes. Rejecting transfer.");
             return false;
        }

        // Concurrent-transfer bound: prune stale, then refuse.
        if (g_partialMessages.size() >= kMaxPartialMessages) {
            PruneStaleChunksLocked();
            if (g_partialMessages.size() >= kMaxPartialMessages) {
                if (!g_isUnloading) LogWarn("Too many concurrent chunk transfers (" + std::to_string(g_partialMessages.size()) + " >= " + std::to_string(kMaxPartialMessages) + "). Rejecting transfer.");
                return false;
            }
        }

        PartialMessage pm;
        pm.totalSize = chunk->total_size();
        pm.receivedSize = 0;
        pm.finalMsgType = chunk->msg_type();
        pm.buffer.resize(pm.totalSize);
        pm.lastUpdate = std::chrono::steady_clock::now();

        it = g_partialMessages.insert({msgId, std::move(pm)}).first;
    }

    PartialMessage& pm = it->second;
    pm.lastUpdate = std::chrono::steady_clock::now();

    // Validate offset and size. Subtraction form: the additive check
    // (offset + size > total) can wrap for wire-supplied values near the
    // unsigned max and pass validation while memcpy writes out of bounds.
    if (!chunk->data() ||
        chunk->offset() > pm.totalSize ||
        chunk->data()->size() > pm.totalSize - chunk->offset()) {
        // Out of bounds: the transfer can never complete correctly, so discard
        // it instead of parking it until the TTL sweep, and tell the producer.
        if (!g_isUnloading) LogWarn("Chunk out of bounds (offset " + std::to_string(chunk->offset()) + ", total " + std::to_string(pm.totalSize) + "). Dropping transfer.");
        g_partialMessages.erase(it);
        PoisonTransferLocked(msgId);
        return false;
    }

    const uint32_t off = chunk->offset();
    const uint32_t len = static_cast<uint32_t>(chunk->data()->size());

    // A PRESENT-but-EMPTY data vector is not the same as a missing one, and the
    // `!chunk->data()` test above does not catch it. Refuse it explicitly: a
    // (off, 0) segment records a zero-length range at `off`, and the REAL chunk
    // that later arrives at the same offset then hits "same start offset,
    // different length" -> overlap -> the whole (otherwise healthy) transfer is
    // discarded. One harmless-looking empty frame would poison a good transfer.
    // It also advances nothing, so it can never be a legitimate part of a
    // transfer. Mirrors handlers.go's `dataLen == 0` refusal (§18.6).
    if (len == 0) {
        if (!g_isUnloading) LogWarn("Zero-length chunk segment (offset " + std::to_string(off) + "). Dropping transfer.");
        g_partialMessages.erase(it);
        PoisonTransferLocked(msgId);
        return false;
    }

    // Classify the arriving range against what is already covered.
    bool duplicate = false;
    bool overlap = false;
    auto seg = pm.receivedSegments.lower_bound(off);
    if (seg != pm.receivedSegments.end() && seg->first == off) {
        // Same start offset: an exact-length repeat is the retransmit case,
        // anything else is a producer that re-chunked mid-transfer.
        duplicate = (seg->second == len);
        overlap = !duplicate;
    } else {
        if (seg != pm.receivedSegments.begin()) {
            auto prev = std::prev(seg);
            if (static_cast<uint64_t>(prev->first) + prev->second > off) overlap = true;
        }
        if (!overlap && seg != pm.receivedSegments.end() &&
            static_cast<uint64_t>(off) + len > seg->first) {
            overlap = true;
        }
    }

    if (overlap) {
        if (!g_isUnloading) LogWarn("Overlapping chunk range (offset " + std::to_string(off) + ", len " + std::to_string(len) + "). Dropping transfer.");
        g_partialMessages.erase(it);
        PoisonTransferLocked(msgId);
        return false;
    }

    if (!duplicate) {
        // First time we see this range: copy + advance.
        std::memcpy(pm.buffer.data() + off, chunk->data()->Data(), len);
        pm.receivedSize += len;
        pm.receivedSegments.emplace(off, len);
    }

    // Check completion. The test is `==`, not `>=`: with bounds-checked,
    // non-overlapping ranges, receivedSize == totalSize means every byte of
    // [0, totalSize) was written exactly once. `>=` was unreachable by
    // construction under those two rules and would only ever fire on a coverage
    // bug. Matches the Go side (handlers.go: `buf.Received == buf.TotalSize`).
    if (pm.receivedSize == pm.totalSize) {
        // Process the full message
        int32_t type = pm.finalMsgType;
        const uint8_t* data = pm.buffer.data();

        // Dispatch based on type
        if (type == (int32_t)MSG_BATCH_ASYNC_RESPONSE) {
             auto batch = flatbuffers::GetRoot<protocol::BatchAsyncResponse>(data);
             ProcessAsyncBatchResponse(batch);
        // Note: MSG_CALCULATION_ENDED is intentionally NOT handled here because it executes
        // xlSet/xlcFormatNumber which requires the MAIN thread. It is handled in xll_events.cpp.
#ifdef XLL_RTD_ENABLED
        } else if (type == (int32_t)MSG_RTD_UPDATE) {
             auto update = flatbuffers::GetRoot<protocol::RtdUpdate>(data);
             ProcessRtdUpdate(update);
        } else if (type == (int32_t)MSG_RTD_ONCE_GRID) {
             // One-shot grid result (possibly chunk-reassembled, since a Grid
             // can be large). Hand the full RtdOnceGridResult buffer to the
             // registry; ProcessRtdOnceGrid owns the parse + Store.
             ProcessRtdOnceGrid(data, pm.totalSize);
#endif
        }

        // Remove from map
        g_partialMessages.erase(it);
    }

    return true;
}

// PruneStaleChunksLocked drops every partial transfer idle for longer than the
// staleness window, and expires poison-set entries on the same clock. The
// caller MUST already hold g_partialMessagesMutex; it backs both the periodic
// sweep and the on-demand reclaim HandleChunk performs at the
// kMaxPartialMessages bound.
static void PruneStaleChunksLocked() {
    auto now = std::chrono::steady_clock::now();
    for (auto it = g_partialMessages.begin(); it != g_partialMessages.end(); ) {
        if (now - it->second.lastUpdate > kChunkStaleTtl) {
            it = g_partialMessages.erase(it);
        } else {
            ++it;
        }
    }
    // Poison entries age out with the same TTL so a transfer id is never
    // permanently burned; a producer that reconnects after the window gets a
    // clean slate. Kept in this one function so the periodic sweep and the
    // at-the-bound reclaim both drain it.
    for (auto it = g_poisonedTransfers.begin(); it != g_poisonedTransfers.end(); ) {
        if (now - it->second > kChunkStaleTtl) {
            it = g_poisonedTransfers.erase(it);
        } else {
            ++it;
        }
    }
}

// Cleanup stale chunks
void CleanupStaleChunks() {
    std::lock_guard<std::mutex> lock(g_partialMessagesMutex);
    PruneStaleChunksLocked();
}

// Worker loop
void WorkerLoop() {
    g_workerRunning = true;

    auto lastCleanup = std::chrono::steady_clock::now();

    while (g_workerRunning) {
        // Check for unloading state to exit early
        if (g_isUnloading) break;

        // Signature: (const uint8_t* reqBuf, int32_t reqSize, uint8_t* respBuf,
        //             uint32_t maxRespSize, shm::MsgType& msgType)
        //
        // msgType is taken BY REFERENCE deliberately. shm's GuestCallWorker
        // passes the live slot header field (`slot->header->msgType`) to the
        // handler and does not overwrite it afterwards, so storing
        // shm::MsgType::SYSTEM_ERROR here is what the guest observes: shm's Go
        // guest checks exactly that field in sendGuestCallInternal and turns it
        // into an error from SendGuestCall. This is the same mechanism shm's own
        // StreamReassembler::Handle uses (it, too, takes `MsgType&` and is
        // documented for use inside a ProcessGuestCalls handler), so nothing in
        // the shm headers needs to change — only this lambda's parameter.
        //
        // BUILD-BREAKING COUPLING (shm): the `shm::MsgType&` parameter binds
        // ONLY because DirectHost::ProcessGuestCalls / GuestCallWorker::
        // ProcessGuestCalls are `template <typename Handler>` with perfect
        // forwarding (`std::forward<Handler>(handler)`), so this lambda is
        // called directly and its reference parameter binds the live slot
        // header field. The `GuestCallHandler` typedef in the SAME header
        // (shm/GuestCallWorker.h) is `std::function<int32_t(const uint8_t*,
        // int32_t, uint8_t*, uint32_t, MsgType)>` — MsgType BY VALUE — and is
        // used only by the Start()/GuestWorkerLoop() path. If shm ever narrows
        // ProcessGuestCalls to that typedef (or someone "simplifies" this call
        // site onto Start()), this lambda STOPS COMPILING rather than silently
        // losing the write-back. Do not convert it to GuestCallHandler.
        bool processed = g_host.ProcessGuestCalls([](const uint8_t* reqBuf, int32_t reqSize, uint8_t* respBuf, uint32_t maxRespSize, shm::MsgType& msgType) -> int32_t {
            // Check for unloading inside the callback as well
            if (g_isUnloading) return 0;

            if (msgType == (shm::MsgType)MSG_BATCH_ASYNC_RESPONSE) {
                auto batch = flatbuffers::GetRoot<protocol::BatchAsyncResponse>(reqBuf);
                ProcessAsyncBatchResponse(batch);
                return 1;
            // Note: MSG_CALCULATION_ENDED is handled by the main thread (xll_events.cpp).
            // Do NOT handle it here in the background worker.
            } else if (msgType == (shm::MsgType)MSG_CHUNK) {
                auto chunk = flatbuffers::GetRoot<protocol::Chunk>(reqBuf);
                if (!HandleChunk(chunk)) {
                    // Refused (protocol violation / resource bound / unloading).
                    // Report it instead of acking success — otherwise the
                    // producer keeps pushing a transfer that can never complete
                    // until its own timeout, with no diagnostic on either side.
                    msgType = shm::MsgType::SYSTEM_ERROR;
                    return 0;
                }
                return 1;
#ifdef XLL_RTD_ENABLED
            } else if (msgType == (shm::MsgType)MSG_RTD_UPDATE) {
                auto update = flatbuffers::GetRoot<protocol::RtdUpdate>(reqBuf);
                ProcessRtdUpdate(update);
                return 1;
            } else if (msgType == (shm::MsgType)MSG_RTD_ONCE_GRID) {
                // One-shot grid result delivered in a single slot (not chunked).
                ProcessRtdOnceGrid(reqBuf, (size_t)reqSize);
                return 1;
#endif
            }

            return 0; // Unknown
        }, 50); // 50ms timeout

        // Avoid logging during unload to prevent touching freed logging resources
        if (processed && !g_isUnloading) {
            LogDebug("Call return guest call receive complete");
        }

        // Periodic cleanup
        auto now = std::chrono::steady_clock::now();
        if (now - lastCleanup > std::chrono::seconds(10)) {
            CleanupStaleChunks();
            lastCleanup = now;
        }
    }
}

void StartWorker() {
    if (g_workerRunning) return;
    if (g_workerThread.joinable()) {
        // Should not happen if StopWorker was called correctly, but for safety
        g_workerRunning = false;
        g_workerThread.join();
    }
    g_workerThread = std::thread(WorkerLoop);
}

void StopWorker() {
    g_workerRunning = false;
}

void JoinWorker() {
    if (g_workerThread.joinable()) {
        g_workerThread.join();
    }
}

void ForceTerminateWorker() {
    g_workerRunning = false;
    if (g_workerThread.joinable()) {
        g_workerThread.detach();
    }
}

} // namespace xll
