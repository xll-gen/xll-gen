#include "include/xll_worker.h"
#include "include/xll_async.h"
#include "include/xll_log.h"
#include "schema_generated.h"
#include <map>
#include <vector>

namespace xll {
    void StartWorker(shm::DirectHost& host) {
        // Reusable vectors for batch processing
        std::vector<XLOPER12> handles;
        std::vector<XLOPER12> values;
        handles.reserve(256);
        values.reserve(256);

        host.Start([&handles, &values](const uint8_t* req, int32_t size, uint8_t* resp, uint32_t capacity, shm::MsgType msgId) -> int32_t {
             static std::map<uint64_t, std::vector<uint8_t>> chunkBuffers;
             static std::map<uint64_t, size_t> chunkSizes;

             try {
                 switch (msgId) {
                 case (shm::MsgType)129: { // MSG_CHUNK
                     auto chunk = flatbuffers::GetRoot<ipc::Chunk>(req);
                     uint64_t id = chunk->id();

                     std::vector<uint8_t>& buf = chunkBuffers[id];
                     if (buf.empty()) {
                         buf.resize(chunk->total_size());
                         chunkSizes[id] = 0;
                     }

                     auto data = chunk->data();
                     if (data) {
                         size_t offset = chunk->offset();
                         size_t len = data->size();
                         if (offset + len <= buf.size()) {
                             std::memcpy(buf.data() + offset, data->data(), len);
                             chunkSizes[id] += len;
                         }
                     }

                     if (chunkSizes[id] >= buf.size()) {
                         shm::MsgType payloadType = (shm::MsgType)chunk->msg_type();
                         int32_t ret = 0;
                         if (payloadType == (shm::MsgType)128) {
                             ret = ProcessAsyncBatchResponse(buf.data(), handles, values);
                         }
                         chunkBuffers.erase(id);
                         chunkSizes.erase(id);
                         return ret;
                     }
                     return 1; // ACK
                 }
                 case (shm::MsgType)128: { // MSG_BATCH_ASYNC_RESPONSE
                     return ProcessAsyncBatchResponse(req, handles, values);
                 }
                 default:
                     return 0;
                 }
             } catch (const std::exception& e) {
                 LogError("Exception in guest call handler: " + std::string(e.what()));
                 return 0;
             } catch (...) {
                 LogError("Unknown exception in guest call handler");
                 return 0;
             }
        });
    }
}
