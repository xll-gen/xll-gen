#pragma once
#include <windows.h>
#include <vector>
#include <map>
#include <mutex>
#include <functional>
#include "shm/DirectHost.h"

// System Message IDs
#define MSG_ACK 2
#define MSG_CHUNK 128
#define MSG_SETREFCACHE 129
#define MSG_CALCULATION_ENDED 130
#define MSG_CALCULATION_CANCELED 131
#define MSG_USER_START 132

// Global IPC Objects
extern shm::DirectHost g_host;
extern std::map<std::string, bool> g_sentRefCache;
extern std::mutex g_refCacheMutex;

// Chunking Helpers
int SendChunked(const uint8_t* data, size_t size, std::vector<uint8_t>& respBuf, uint32_t timeoutMs);
const uint8_t* ReceiveChunked(shm::ZeroCopySlot& slot, int reqMsgId, size_t reqSize, uint32_t timeoutMs);

// Guest Handler Type
typedef int32_t (*GuestHandlerFunc)(const uint8_t* req, uint8_t* resp, uint32_t msgId);

// Handle Chunk Request in Guest
int32_t HandleChunk(const uint8_t* req, uint8_t* resp, GuestHandlerFunc handler);
