#pragma once
#include <windows.h>
#include <vector>
#include <map>
#include <mutex>
#include <functional>
#include "shm/DirectHost.h"

// System Message IDs
#define MSG_ACK 2
#define MSG_BATCH_ASYNC_RESPONSE 127
#define MSG_CHUNK 128
#define MSG_SETREFCACHE 129
#define MSG_CALCULATION_ENDED 130
#define MSG_CALCULATION_CANCELED 131
#define MSG_USER_START 132

// Global IPC Objects
extern shm::DirectHost g_host;
extern std::map<std::string, bool> g_sentRefCache;
extern std::mutex g_refCacheMutex;
