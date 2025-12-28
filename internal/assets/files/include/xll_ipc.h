#pragma once
#include <windows.h>
#include "shm/DirectHost.h"
#include <map>
#include <mutex>
#include <string>

// Global IPC objects
extern shm::DirectHost* g_phost;
#define g_host (*g_phost)

extern std::map<std::string, bool> g_sentRefCache;
extern std::mutex g_refCacheMutex;

// Message IDs
// System (0-127)
#define MSG_ACK 2

// User/App (128+)
#define MSG_BATCH_ASYNC_RESPONSE 128
#define MSG_CHUNK 129
#define MSG_SETREFCACHE 130
#define MSG_CALCULATION_ENDED 131
#define MSG_CALCULATION_CANCELED 132

// RTD System Messages (133-139)
#define MSG_RTD_CONNECT 133
#define MSG_RTD_DISCONNECT 134
#define MSG_RTD_UPDATE 135
#define MSG_RTD_HEARTBEAT 136

// User Functions Start
#define MSG_USER_START 140

// Helper for logging SHM errors
std::string SHMErrorToString(shm::Error err);

// Function declarations
namespace xll {
    void StartWorker();
    void StopWorker();
}
