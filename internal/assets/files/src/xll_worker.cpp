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

// Set by WorkerLoop as its LAST act, so the teardown can observe "the worker has
// actually returned" without touching the std::thread object. See xll_worker.h
// (WorkerExited) for why the teardown must never park in join().
static std::atomic<bool> g_workerExited{true};

// Chunk Reassembly Logic
//
// CO-CHANGE ANCHOR (§18.6 style): this mirrors the Go-side reassembler in
// pkg/server/manager.go (ChunkManager/ChunkBuffer) + pkg/server/handlers.go
// (HandleChunk).
//
// "Mirrors" means SAME RULES, not same object: these are two INDEPENDENT
// reassemblers running in OPPOSITE DIRECTIONS. This one handles GUEST->HOST
// chunks (the Go server's async batch responses and rtd-once grid results
// arriving at the XLL); the Go one handles HOST->GUEST chunks (requests the XLL
// sends to the server). Nothing is shared at runtime, and the tuning story is
// NOT symmetric: the Go side's limits are `xll.yaml` `server.chunk` knobs
// (max_buffer_bytes / max_concurrent_transfers / cleanup_interval / buffer_ttl),
// while this direction's caps (xll_worker.h: kMaxChunkTotalSize,
// kMaxPartialMessages, kMaxPoisonedTransfers, kChunkStaleTtl) are COMPILE-TIME
// and have no template or YAML wiring — setting `server.chunk` in a project does
// NOT move them.
//
// SAME RULES, and the numbers line up — but NOT as a pair of constants in
// "lockstep". Be precise about which is which:
//   - This side's kMaxChunkTotalSize is FIXED at 256 MiB.
//   - server.DefaultMaxChunkBufferBytes is only the DEFAULT of a per-project
//     knob; a project that sets `server.chunk.max_buffer_bytes` moves the Go
//     side alone and is asymmetric by construction.
//   - The number that must actually track kMaxChunkTotalSize is the Go
//     SENDER's chunk.MaxTransferBytes (pkg/chunk), which refuses an over-cap
//     payload BEFORE framing it — a sender cannot discover this constant at
//     runtime (no template, no YAML, nothing negotiated on the wire), so it
//     hard-codes the same value on purpose.
// The rule mirror, layer by layer:
//   - receivedSegments +
//     xll::ClaimChunkSegment
//     (xll_worker.h)           <->  ChunkBuffer.Segments + ClaimSegment
//   - xll::ChunkRegistry
//     (xll_worker.h)           <->  ChunkManager (GetChunkBuffer admission,
//                                   PoisonTransfer/IsPoisoned, prune sweep)
//   - kMaxPartialMessages      <->  server.DefaultMaxConcurrentTransfers (1024)
//   - kMaxPoisonedTransfers    <->  server.DefaultMaxPoisonedTransfers (1024)
//   - kChunkStaleTtl           <->  server.DefaultChunkBufferTTL (60s)
//   - the == completion test   <->  handlers.go `buf.Received == buf.TotalSize`
//   - the len == 0 refusal     <->  handlers.go's `dataLen == 0` refusal
//   - the poison set           <->  ChunkManager.poisoned / PoisonTransfer /
//                                   IsPoisoned (pkg/server/manager.go)
//   - every reject path returns SYSTEM_ERROR <-> handlers.go's
//     shm.MsgTypeSystemError returns
// One rule is deliberately NOT mirrored — TOTAL-SIZE MISMATCH ON ID REUSE:
// Go's GetChunkBuffer, on finding a live buffer whose TotalSize differs from the
// arriving chunk's total, logs a warning and REPLACES the buffer in place so the
// re-opened transfer proceeds. This side has no such reset: the entry keeps the
// FIRST totalSize for its life (ChunkAdmission::Existing), so the re-open's
// chunks are bounds-checked against the stale total, trip the out-of-bounds
// path, and the transfer is discarded AND poisoned. The same wire sequence
// therefore succeeds against the Go guest and fails here. INTENTIONAL, and left
// alone on purpose: symmetrizing it means deciding which behavior is the
// contract, which is a separate change. Do not "align" one side without the
// other (§18.6).
//
// WHERE THE RULES LIVE. Both the segment arbiter (ClaimChunkSegment) and the
// transfer bookkeeping (ChunkRegistry: the admission caps, prune-then-refuse,
// the poison set with its TTL and oldest-eviction) are PURE types in
// xll_worker.h, not code in this file. That is deliberate: this translation unit
// reaches xlAsyncReturn, COM and the shm host, so it cannot be linked offline
// and everything inside it can only ever be compile-checked. As header types
// they are unit-gated by internal/assets/testdata/chunk_segments_native_test.cpp
// and chunk_registry_native_test.cpp (bare g++), against the same case tables
// internal/assets/chunk_cpp_test.go replays through the Go mirror. What is left
// here is what genuinely cannot leave: the FlatBuffers accessors, the logging,
// the memcpy, and the completion dispatch. Do NOT re-inline the rules
// (TestChunkSegmentLogicIsExtracted / TestChunkRegistryLogicIsExtracted fail if
// you do — the offline gates would keep passing while the shipped reassembler
// ran untested code).
//
// The offset type matches protocol::Chunk::offset() / total_size(), which the
// FlatBuffers schema (protocol.fbs) declares as uint32.
ChunkRegistry g_chunkRegistry;
std::mutex g_partialMessagesMutex;

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

    // One clock read for the whole call: the registry takes `now` as an
    // argument (which is what makes its TTL rules testable offline) and every
    // decision below should be made against the same instant anyway.
    const ChunkTime now = std::chrono::steady_clock::now();

    // Admission: poison check, entry lookup, and — for a NEW id — the zero-total
    // refusal, the total-size cap and the prune-then-refuse transfer bound. All
    // of it is xll::ChunkRegistry (xll_worker.h); what stays here is the logging
    // and the SYSTEM_ERROR return, which is the part that cannot leave this
    // translation unit. Every Refused* verdict leaves the registry's state
    // untouched apart from its two documented reclaims (an expired poison entry
    // is dropped; the count bound prunes stale entries first), so each of them
    // can simply return.
    PartialMessage* pmp = nullptr;
    switch (g_chunkRegistry.Admit(msgId, chunk->total_size(), chunk->msg_type(), now, &pmp)) {
    case ChunkAdmission::RefusedPoisoned:
        // A previous chunk of this transfer was refused for a protocol
        // violation. Refusing everything else on that id until the record
        // expires is what stops the next chunk from resurrecting a fresh
        // (permanently incomplete) buffer that would be acked as SUCCESS — which
        // is how a "rejection" used to keep the producer's retry ladder alive.
        if (!g_isUnloading) LogWarn("Chunk for a rejected transfer (id " + std::to_string(msgId) + "). Refusing.");
        return false;
    case ChunkAdmission::RefusedZeroTotal:
        // A zero total_size would resize(0) the buffer, satisfy the `0 == 0`
        // completion test immediately, and hand GetRoot<> a NULLPTR from
        // pm.buffer.data() -> access violation inside Excel. Unreachable from a
        // well-behaved Go producer, but this is the wire and the Go guest
        // already refuses it (GetChunkBuffer's `total <= 0`); keep the two
        // reject sets symmetric (CO-CHANGE ANCHOR, §18.6).
        if (!g_isUnloading) LogWarn("Chunk declares total_size 0. Rejecting transfer.");
        return false;
    case ChunkAdmission::RefusedTotalTooLarge:
        // Bound the wire-supplied total size to prevent a multi-GiB allocation
        // (DoS). See kMaxChunkTotalSize in xll_worker.h.
        if (!g_isUnloading) LogWarn("Chunk total size too large: " + std::to_string(chunk->total_size()) + " bytes. Rejecting transfer.");
        return false;
    case ChunkAdmission::RefusedTooManyTransfers:
        if (!g_isUnloading) LogWarn("Too many concurrent chunk transfers (" + std::to_string(g_chunkRegistry.transferCount()) + " >= " + std::to_string(g_chunkRegistry.maxTransfers()) + "). Rejecting transfer.");
        return false;
    case ChunkAdmission::Opened:
    case ChunkAdmission::Existing:
        break;
    default:
        // Fail closed, same reasoning as the ChunkSegmentClaim switch below:
        // -Wswitch is not enabled by the generated CMake, so a new
        // ChunkAdmission enumerator must not fall through into the reassembly
        // path with a null entry.
        if (!g_isUnloading) LogWarn("Unknown chunk admission verdict (id " + std::to_string(msgId) + "). Rejecting transfer.");
        return false;
    }
    if (!pmp) return false; // defensive: Opened/Existing always publish an entry

    PartialMessage& pm = *pmp;

    // A MISSING data vector is not something ClaimChunkSegment can see (it
    // takes an already-extracted offset/length pair), so it is checked here and
    // folded into the same out-of-bounds handling.
    if (!chunk->data()) {
        if (!g_isUnloading) LogWarn("Chunk carries no data vector (offset " + std::to_string(chunk->offset()) + ", total " + std::to_string(pm.totalSize) + "). Dropping transfer.");
        // Poison() erases the entry too, which INVALIDATES pm — nothing below
        // this point may touch it.
        g_chunkRegistry.Poison(msgId, now);
        return false;
    }

    const uint32_t off = chunk->offset();
    const uint32_t len = static_cast<uint32_t>(chunk->data()->size());

    // Bounds-check + zero-length refusal + overlap arbitration, all in the pure
    // helper in xll_worker.h so the rules can be exercised offline (g++ only,
    // no Excel/FlatBuffers/shm) by internal/assets/testdata/
    // chunk_segments_native_test.cpp — the same accept/reject set
    // pkg/server's TestChunkBuffer_ClaimSegment pins for the Go mirror.
    // ClaimChunkSegment RECORDS the range only on ::New; every reject verdict
    // leaves pm.receivedSegments untouched, which is what lets each of them
    // discard the transfer here without unwinding bookkeeping.
    //
    // pm.totalSize is a size_t but is bounded by kMaxChunkTotalSize (256 MiB)
    // at insert time, and protocol.fbs declares total_size as uint32 anyway, so
    // the narrowing cast cannot lose information.
    const ChunkSegmentClaim claim = ClaimChunkSegment(
        pm.receivedSegments, static_cast<uint32_t>(pm.totalSize), off, len);

    // Each reject path below discards AND poisons via ChunkRegistry::Poison,
    // which erases the entry — pm is INVALID after that call, so every one of
    // them returns immediately.
    switch (claim) {
    case ChunkSegmentClaim::OutOfBounds:
        // The transfer can never complete correctly, so discard it instead of
        // parking it until the TTL sweep, and tell the producer.
        if (!g_isUnloading) LogWarn("Chunk out of bounds (offset " + std::to_string(off) + ", len " + std::to_string(len) + ", total " + std::to_string(pm.totalSize) + "). Dropping transfer.");
        g_chunkRegistry.Poison(msgId, now);
        return false;
    case ChunkSegmentClaim::ZeroLength:
        if (!g_isUnloading) LogWarn("Zero-length chunk segment (offset " + std::to_string(off) + "). Dropping transfer.");
        g_chunkRegistry.Poison(msgId, now);
        return false;
    case ChunkSegmentClaim::Overlap:
        if (!g_isUnloading) LogWarn("Overlapping chunk range (offset " + std::to_string(off) + ", len " + std::to_string(len) + "). Dropping transfer.");
        g_chunkRegistry.Poison(msgId, now);
        return false;
    case ChunkSegmentClaim::Duplicate:
        // Exact retransmit: skip BOTH the copy and the advance.
        break;
    case ChunkSegmentClaim::New:
        // First time we see this range: copy + advance. The range is already
        // recorded in pm.receivedSegments by ClaimChunkSegment.
        std::memcpy(pm.buffer.data() + off, chunk->data()->Data(), len);
        pm.receivedSize += len;
        break;
    default:
        // Fail closed. -Wswitch is not enabled by the generated CMake, so a new
        // ChunkSegmentClaim enumerator would otherwise fall through to the
        // completion check with nothing copied and nothing advanced — a silently
        // stuck transfer. Treat an unknown claim like any other protocol
        // violation: drop and poison so the producer learns immediately.
        if (!g_isUnloading) LogWarn("Unknown chunk segment claim (offset " + std::to_string(off) + "). Dropping transfer.");
        g_chunkRegistry.Poison(msgId, now);
        return false;
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

        // Completed transfers are removed WITHOUT poisoning (Complete, not
        // Poison): a finished id is free to be reused immediately.
        g_chunkRegistry.Complete(msgId);
    }

    return true;
}

// Cleanup stale chunks. The sweep itself (both maps, one TTL) is
// ChunkRegistry::Prune; this is only the locking wrapper the worker loop calls.
void CleanupStaleChunks() {
    std::lock_guard<std::mutex> lock(g_partialMessagesMutex);
    g_chunkRegistry.Prune(std::chrono::steady_clock::now());
}

bool WorkerExited() { return g_workerExited.load(std::memory_order_acquire); }

bool WaitForWorkerExit(unsigned int timeoutMs) {
    using clock = std::chrono::steady_clock;
    auto deadline = clock::now() + std::chrono::milliseconds(timeoutMs);
    while (!g_workerExited.load(std::memory_order_acquire)) {
        if (clock::now() >= deadline) return false;
        std::this_thread::sleep_for(std::chrono::milliseconds(1));
    }
    return true;
}

// Worker loop
void WorkerLoop() {
    g_workerExited.store(false, std::memory_order_release);
    g_workerRunning = true;

    auto lastCleanup = std::chrono::steady_clock::now();

    while (g_workerRunning) {
        // Check for unloading/quiescing state to exit early. g_isQuiescing is
        // latched by the graceful teardown's Phase 1 BEFORE anything destructive
        // happens, so the worker stops dispatching guest calls (RTD updates,
        // async returns) while g_phost is still alive — see xll_lifecycle.h.
        if (g_isUnloading || g_isQuiescing) break;

        // PARK until a guest call arrives, instead of spinning. shm v0.8.21
        // exposes its §3.5 doorbell gate for exactly this: hostState on the guest
        // slots is published ONLY from inside that wait, so until this XLL called
        // it, the Go sender's `hostState == HOST_STATE_WAITING` doorbell could
        // never fire — which is why the spin was not merely wasteful but
        // load-bearing for latency. A hand-rolled wait here would have expired on
        // its own timeout on every single call. Do NOT replace this with a sleep.
        //
        // Hot path is unchanged: the gate spin-catches an in-window arrival with no
        // syscall on either side, so the ~150 ns RTT stands. The cost lands on the
        // FIRST call after an idle gap longer than shm's spin window — one
        // SetEvent + one wake — which for this add-in's traffic (RTD pushes behind
        // a throttle, async batch returns, reassembled grids) is invisible.
        //
        // kWorkerParkMs SITS BELOW kThreadReapBudgetMs (xll_lifecycle.cpp): a
        // MISSED wake must still let this thread exit inside the teardown reap
        // budget. If it did not, Phase 1 would DETACH a thread parked inside
        // WaitForSingleObject in code that lives in the XLL image, and on the
        // add-in-disable path that forces the module pin — i.e. the XLL stays
        // mapped for the rest of the session. StopWorker()'s wake is the other,
        // independent cover; neither is sufficient alone.
        constexpr unsigned kWorkerParkMs = 100;
        g_host.WaitForGuestCall(kWorkerParkMs);
        if (g_isUnloading || g_isQuiescing) break;

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
            // Check for unloading/quiescing inside the callback as well
            if (g_isUnloading || g_isQuiescing) return 0;

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
        // The second argument is shm's `limit` — the MAXIMUM NUMBER OF GUEST
        // CALLS drained per invocation (GuestCallWorker::ProcessGuestCalls,
        // `int limit = -1` = unbounded), NOT a timeout. This call still does not
        // block; it returns immediately when no slot is ready. The waiting is done
        // by WaitForGuestCall at the top of the loop (added 2026-08-03) — before
        // that, this loop was a pure spin that burned a core for the life of the
        // add-in. Bounding the batch still matters: it keeps the shutdown check at
        // the top of the loop reachable, which is why StopWorker() is observed
        // promptly even mid-drain.
        }, /*maxBatchSize=*/50);

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
    g_workerExited.store(true, std::memory_order_release);
}

void StartWorker() {
    if (g_workerRunning) return;
    g_workerExited.store(false, std::memory_order_release);
    if (g_workerThread.joinable()) {
        // Should not happen if StopWorker was called correctly, but for safety
        g_workerRunning = false;
        g_workerThread.join();
    }
    g_workerThread = std::thread(WorkerLoop);
}

void StopWorker() {
    g_workerRunning = false;
    // Pop the worker out of its park. The flag store above is invisible to a
    // thread already blocked in the OS wait, so without this the subsequent join
    // waits out up to kWorkerParkMs — and a teardown whose reap budget were ever
    // shortened below that would DETACH a thread still executing inside the XLL
    // image. Structural twin of BeginQuiesce signalling g_procInfo.hShutdownEvent
    // to pop the monitor thread, rather than relying on a timeout.
    //
    // Guarded on g_phost: StopWorker is reachable on teardown paths where the host
    // is already gone, and this must not be the thing that faults there.
    if (g_phost) {
        g_host.WakeGuestCallWaiter();
    }
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
