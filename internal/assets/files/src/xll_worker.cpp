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
#include <unordered_set>
#include <mutex>
#include <chrono>
#include <thread>

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
//   - receivedOffsets here  <->  ChunkBuffer.ReceivedOffsets (map[uint32]bool)
//   - kMaxChunkTotalSize    <->  server.DefaultMaxChunkBufferBytes (256 MiB)
//   - the >= completion test <-> handlers.go `buf.Received >= buf.TotalSize`
// The offset type matches protocol::Chunk::offset() / total_size(), which the
// FlatBuffers schema (protocol.fbs) declares as uint32.
struct PartialMessage {
    std::vector<uint8_t> buffer;
    size_t receivedSize;
    size_t totalSize;
    int32_t finalMsgType;
    // Dedup set keyed by chunk offset. A retransmitted chunk (e.g. after a
    // dropped ACK) MUST NOT re-copy + re-advance receivedSize, otherwise a
    // duplicate pushes receivedSize past totalSize and triggers premature
    // completion with trailing bytes still zero — data corruption. This is
    // the exact hazard fixed on the Go side (AGENTS.md §23.3).
    std::unordered_set<uint32_t> receivedOffsets;
    std::chrono::steady_clock::time_point lastUpdate;
};

std::map<uint64_t, PartialMessage> g_partialMessages;
std::mutex g_partialMessagesMutex;

// Upper bound on the wire-supplied total_size a single inbound transfer may
// declare, mirroring the Go guest's server.DefaultMaxChunkBufferBytes
// (256 MiB, pkg/server/manager.go). Previously C++ capped at 128 MiB while Go
// accepted up to 256 MiB, so a ~200 MB response was accepted by Go but
// silently dropped here. Keep these two in lockstep (CO-CHANGE ANCHOR, §18.6).
static constexpr uint64_t kMaxChunkTotalSize = 256ull * 1024 * 1024;

void HandleChunk(const protocol::Chunk* chunk) {
    if (!chunk) return;

    // If we're unloading, bail out early to avoid touching global state
    if (g_isUnloading) return;

    uint64_t msgId = chunk->id();

    std::lock_guard<std::mutex> lock(g_partialMessagesMutex);
    auto it = g_partialMessages.find(msgId);

    if (it == g_partialMessages.end()) {
        // New partial message
        // Vulnerability Fix: bound the wire-supplied total size to prevent a
        // multi-GiB allocation (DoS). Aligned to the Go guest cap; see
        // kMaxChunkTotalSize above.
        if (chunk->total_size() > kMaxChunkTotalSize) {
             if (!g_isUnloading) LogWarn("Chunk total size too large: " + std::to_string(chunk->total_size()) + " bytes. Dropping.");
             return;
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
        // Error: Overflow. Discard.
        g_partialMessages.erase(it);
        return;
    }

    // Dedup by chunk offset (mirrors Go ChunkBuffer.ReceivedOffsets,
    // AGENTS.md §23.3). A retransmitted chunk (e.g. after a dropped ACK) is
    // dropped here: we skip BOTH the copy and the receivedSize advance, exactly
    // as the Go side does. Without this, a duplicate advances receivedSize past
    // totalSize and trips premature completion with the trailing region still
    // zero (data corruption). emplace().second is false when the offset was
    // already seen, so the body runs only on first observation of each offset.
    if (pm.receivedOffsets.emplace(chunk->offset()).second) {
        // First time we see this offset: copy + advance.
        std::memcpy(pm.buffer.data() + chunk->offset(), chunk->data()->Data(), chunk->data()->size());
        pm.receivedSize += chunk->data()->size();
    }

    // Check completion. With offset dedup, receivedSize can only reach
    // totalSize via distinct offsets, so >= is safe and matches the Go-side
    // semantics (handlers.go: `buf.Received >= buf.TotalSize`). Using >=
    // rather than == keeps completion reachable even if a producer's chunk
    // sizes don't sum exactly as expected.
    if (pm.receivedSize >= pm.totalSize) {
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
}

// Cleanup stale chunks
void CleanupStaleChunks() {
    std::lock_guard<std::mutex> lock(g_partialMessagesMutex);
    auto now = std::chrono::steady_clock::now();
    for (auto it = g_partialMessages.begin(); it != g_partialMessages.end(); ) {
        if (now - it->second.lastUpdate > std::chrono::seconds(60)) {
            it = g_partialMessages.erase(it);
        } else {
            ++it;
        }
    }
}

// Worker loop
void WorkerLoop() {
    g_workerRunning = true;

    auto lastCleanup = std::chrono::steady_clock::now();

    while (g_workerRunning) {
        // Check for unloading state to exit early
        if (g_isUnloading) break;

        // Updated Signature: (const uint8_t* reqBuf, int32_t reqSize, uint8_t* respBuf, uint32_t maxRespSize, shm::MsgType msgType)
        bool processed = g_host.ProcessGuestCalls([](const uint8_t* reqBuf, int32_t reqSize, uint8_t* respBuf, uint32_t maxRespSize, shm::MsgType msgType) -> int32_t {
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
                HandleChunk(chunk);
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
