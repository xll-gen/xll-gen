#include "xll_ipc.h"
#include "schema_generated.h"
#include <random>
#include <thread>
#include <iostream>

// Global definitions
shm::DirectHost g_host;
std::map<std::string, bool> g_sentRefCache;
std::mutex g_refCacheMutex;

int SendChunked(const uint8_t* data, size_t size, std::vector<uint8_t>& respBuf, uint32_t timeoutMs) {
    // Thread-safe random number generation
    thread_local std::mt19937_64 rng(std::random_device{}());
    thread_local std::uniform_int_distribution<uint64_t> dist;
    uint64_t transferId = dist(rng);

    const size_t chunkSize = 950 * 1024; // 950KB
    size_t offset = 0;

    while (offset < size) {
        size_t len = std::min(chunkSize, size - offset);

        flatbuffers::FlatBufferBuilder b(1024 + len); // Approx
        auto dataOff = b.CreateVector(data + offset, len);
        // MSG_SETREFCACHE is 129
        auto chunk = ipc::CreateChunk(b, transferId, (uint32_t)size, (uint32_t)offset, dataOff, MSG_SETREFCACHE);
        b.Finish(chunk);

        // MsgID 128 for Chunk
        bool ok = g_host.Send(b.GetBufferPointer(), b.GetSize(), MSG_CHUNK, respBuf, timeoutMs);
        if (!ok) return -1; // Fail (Timeout)

        if (respBuf.empty()) return -1;

        auto ack = ipc::GetAck(respBuf.data());
        if (!ack || !ack->ok()) return -1;

        offset += len;
    }
    return 1; // Success
}

const uint8_t* ReceiveChunked(shm::ZeroCopySlot& slot, int reqMsgId, size_t reqSize, uint32_t timeoutMs) {
    bool ok = slot.Send(reqSize, reqMsgId, timeoutMs);
    if (!ok) return nullptr;

    if (ipc::ChunkBufferHasIdentifier(slot.GetRespBuffer())) {
        thread_local std::vector<uint8_t> buf;
        buf.clear();

        while(true) {
            auto chunk = ipc::GetChunk(slot.GetRespBuffer());
            auto data = chunk->data();
            buf.insert(buf.end(), data->begin(), data->end());

            if (buf.size() >= chunk->total_size()) return buf.data();

            // Request next chunk via Ack (MsgID 2)
            flatbuffers::FlatBufferBuilder b(128, nullptr, false, slot.GetReqBuffer());
            auto ack = ipc::CreateAck(b, chunk->id(), true);
            b.Finish(ack);

            if (!slot.Send(b.GetSize(), MSG_ACK, timeoutMs)) return nullptr;
            if (!ipc::ChunkBufferHasIdentifier(slot.GetRespBuffer())) return nullptr;
        }
    }
    return slot.GetRespBuffer();
}

int32_t HandleChunk(const uint8_t* req, uint8_t* resp, GuestHandlerFunc handler) {
    static std::map<uint64_t, std::vector<uint8_t>> asyncChunks;
    auto chunk = ipc::GetChunk(req);
    auto id = chunk->id();
    auto data = chunk->data();

    std::vector<uint8_t>& buf = asyncChunks[id];
    if (buf.empty()) buf.reserve(chunk->total_size());

    buf.insert(buf.end(), data->begin(), data->end());

    if (buf.size() >= chunk->total_size()) {
            uint32_t originalMsgId = chunk->msg_id();
            std::vector<uint8_t> fullPayload = buf;
            asyncChunks.erase(id);
            return handler(fullPayload.data(), resp, originalMsgId);
    }

    // Send Ack (MsgID 2) to request next
    flatbuffers::FlatBufferBuilder ackB(64);
    auto ack = ipc::CreateAck(ackB, id, true);
    ackB.Finish(ack);
    memcpy(resp, ackB.GetBufferPointer(), ackB.GetSize());
    return ackB.GetSize();
}
