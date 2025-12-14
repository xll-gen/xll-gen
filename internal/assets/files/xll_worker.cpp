#include "xll_ipc.h"
#include "xll_converters.h"
#include "xll_utility.h"
#include <vector>
#include <string>
#include <map>
#include <mutex>
#include <chrono>

// External declaration
void ProcessAsyncBatchResponse(const protocol::BatchAsyncResponse* batch);
void ExecuteCommands(const flatbuffers::Vector<flatbuffers::Offset<protocol::CommandWrapper>>* commands);

namespace xll {

std::atomic<bool> g_workerRunning = false;

// Chunk Reassembly Logic
struct PartialMessage {
    std::vector<uint8_t> buffer;
    size_t receivedSize;
    size_t totalSize;
    int32_t finalMsgType;
    std::chrono::steady_clock::time_point lastUpdate;
};

std::map<uint64_t, PartialMessage> g_partialMessages;
std::mutex g_partialMessagesMutex;

void HandleChunk(const protocol::Chunk* chunk) {
    if (!chunk) return;

    uint64_t msgId = chunk->id();

    std::lock_guard<std::mutex> lock(g_partialMessagesMutex);
    auto it = g_partialMessages.find(msgId);

    if (it == g_partialMessages.end()) {
        // New partial message
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

    // Validate offset and size
    if (chunk->offset() + chunk->data()->size() > pm.totalSize) {
        // Error: Overflow. Discard.
        g_partialMessages.erase(it);
        return;
    }

    // Copy data
    std::memcpy(pm.buffer.data() + chunk->offset(), chunk->data()->Data(), chunk->data()->size());
    pm.receivedSize += chunk->data()->size();

    // Check completion
    if (pm.receivedSize == pm.totalSize) {
        // Process the full message
        int32_t type = pm.finalMsgType;
        const uint8_t* data = pm.buffer.data();

        // Dispatch based on type
        if (type == MSG_BATCH_ASYNC_RESPONSE) {
             auto batch = flatbuffers::GetRoot<protocol::BatchAsyncResponse>(data);
             ProcessAsyncBatchResponse(batch);
        } else if (type == MSG_CALCULATION_ENDED) {
             auto resp = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(data);
             ExecuteCommands(resp->commands());
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
void WorkerLoop(int numGuestSlots) {
    g_workerRunning = true;

    auto lastCleanup = std::chrono::steady_clock::now();

    while (g_workerRunning) {
        bool processed = g_host.ProcessGuestCalls([](const void* data, size_t size, int32_t msgType) -> int32_t {
            if (msgType == MSG_BATCH_ASYNC_RESPONSE) {
                auto batch = flatbuffers::GetRoot<protocol::BatchAsyncResponse>(data);
                ProcessAsyncBatchResponse(batch);
                return 1;
            } else if (msgType == MSG_CALCULATION_ENDED) {
                auto resp = flatbuffers::GetRoot<protocol::CalculationEndedResponse>(data);
                ExecuteCommands(resp->commands());
                return 1;
            } else if (msgType == MSG_CHUNK) {
                auto chunk = flatbuffers::GetRoot<protocol::Chunk>(data);
                HandleChunk(chunk);
                return 1;
            }

            return 0; // Unknown
        }, 50); // 50ms timeout

        // Periodic cleanup
        auto now = std::chrono::steady_clock::now();
        if (now - lastCleanup > std::chrono::seconds(10)) {
            CleanupStaleChunks();
            lastCleanup = now;
        }
    }
}

void StartWorker(int numGuestSlots) {
    std::thread t(WorkerLoop, numGuestSlots);
    t.detach();
}

void StopWorker() {
    g_workerRunning = false;
}

} // namespace xll
