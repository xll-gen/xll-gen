#include "xll_ipc.h"
#include <random>
#include <algorithm>
#include <thread>
#include "schema_generated.h"

int SendChunked(shm::DirectHost& host, const uint8_t* data, size_t size, std::vector<uint8_t>& respBuf) {
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
        int ok = host.Send(b.GetBufferPointer(), b.GetSize(), MSG_CHUNK, respBuf);
        if (ok < 0) return ok; // Fail

        // Response must be Ack (MsgID 2)
        if (ok != MSG_ACK) return -1; // Expecting Ack

        auto ack = ipc::GetAck(respBuf.data());
        if (!ack || !ack->ok()) return -1;

        offset += len;
    }
    return 1; // Success
}

const uint8_t* ReceiveChunked(shm::ZeroCopySlot& slot, int reqMsgId, size_t reqSize) {
    int ok = slot.Send(reqMsgId, reqSize);
    if (ok < 0) return nullptr;

    if (slot.GetRespMsgId() == MSG_CHUNK || ipc::ChunkBufferHasIdentifier(slot.GetRespBuffer())) {
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

            if (slot.Send(MSG_ACK, b.GetSize()) < 0) return nullptr;
            if (slot.GetRespMsgId() != MSG_CHUNK && !ipc::ChunkBufferHasIdentifier(slot.GetRespBuffer())) return nullptr;
        }
    }
    return slot.GetRespBuffer();
}

bool HandleAsyncChunk(const uint8_t* req, uint8_t* resp, std::vector<uint8_t>& fullPayload, uint32_t& originalMsgId, size_t& bytesWritten) {
    static std::map<uint64_t, std::vector<uint8_t>> asyncChunks;
    auto chunk = ipc::GetChunk(req);
    auto id = chunk->id();
    auto data = chunk->data();

    std::vector<uint8_t>& buf = asyncChunks[id];
    if (buf.empty()) buf.reserve(chunk->total_size());

    buf.insert(buf.end(), data->begin(), data->end());

    if (buf.size() >= chunk->total_size()) {
            originalMsgId = chunk->msg_id();
            fullPayload = buf; // Copy
            asyncChunks.erase(id);
            return true; // Complete
    }

    // Send Ack (MsgID 2) to request next
    flatbuffers::FlatBufferBuilder ackB(64);
    auto ack = ipc::CreateAck(ackB, id, true);
    ackB.Finish(ack);
    memcpy(resp, ackB.GetBufferPointer(), ackB.GetSize());
    bytesWritten = ackB.GetSize();
    return false; // Not complete
}
