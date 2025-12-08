#ifndef XLL_IPC_H
#define XLL_IPC_H

#include <vector>
#include <cstdint>
#include <map>
#include "shm/DirectHost.h"
#include "shm/IPCUtils.h"

// System Message IDs
#define MSG_ACK 2
#define MSG_CHUNK 128
#define MSG_SETREFCACHE 129
#define MSG_CALCULATION_ENDED 130
#define MSG_CALCULATION_CANCELED 131
#define MSG_USER_START 132

// Sends data in chunks if it exceeds the slot limit.
// Uses DirectHost::Send (copy-based) because it might be called inside a ZeroCopy context (SetRefCache).
int SendChunked(shm::DirectHost& host, const uint8_t* data, size_t size, std::vector<uint8_t>& respBuf);

// Receives chunked data from a ZeroCopySlot.
const uint8_t* ReceiveChunked(shm::ZeroCopySlot& slot, int reqMsgId, size_t reqSize);

// Reassembles chunks for async returns
bool HandleAsyncChunk(const uint8_t* req, uint8_t* resp, std::vector<uint8_t>& fullPayload, uint32_t& originalMsgId, size_t& bytesWritten);

#endif // XLL_IPC_H
