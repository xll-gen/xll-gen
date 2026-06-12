#include "xll_ipc.h"
#include "types/protocol_generated.h"
#include "xll_log.h"

#include <random>
#include <thread>
#include <iostream>
#include <vector>
#include <mutex>

// Global definitions
shm::DirectHost* g_phost = nullptr;
std::map<std::string, bool> g_sentRefCache;
std::mutex g_refCacheMutex;

// Helper for logging SHM errors
std::string SHMErrorToString(shm::Error err) {
    switch (err) {
        case shm::Error::None: return "None";
        case shm::Error::Timeout: return "Timeout";
        case shm::Error::BufferTooSmall: return "BufferTooSmall";
        case shm::Error::InvalidArgs: return "InvalidArgs";
        case shm::Error::NotConnected: return "NotConnected";
        case shm::Error::ResourceExhausted: return "ResourceExhausted";
        case shm::Error::ProtocolViolation: return "ProtocolViolation";
        default: return "Unknown (" + std::to_string((int)err) + ")";
    }
}

namespace xll {

bool SendRefCachePayloadOnce(const std::string& token, const uint8_t* payload, size_t size) {
    if (g_phost == nullptr || payload == nullptr || size == 0) return false;

    {
        // Already shipped this cycle? The Go RefCache holds it until calc-end
        // (HandleCalculationEnded/Canceled clears it, mirrored by
        // g_sentRefCache.clear() in xll_events.cpp), so a second cell sharing
        // the same content-hash token reuses the cached payload.
        std::lock_guard<std::mutex> lock(g_refCacheMutex);
        if (g_sentRefCache.find(token) != g_sentRefCache.end()) {
            return true;
        }
    }

    std::vector<uint8_t> respBuf;
    auto res = g_host.Send(payload, (int)size, (shm::MsgType)MSG_SETREFCACHE, respBuf, 2000);
    if (res.HasError()) {
        // Do NOT mark as sent — a later cell with the same token retries.
        return false;
    }

    {
        std::lock_guard<std::mutex> lock(g_refCacheMutex);
        g_sentRefCache[token] = true;
    }
    return true;
}

} // namespace xll
