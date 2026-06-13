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

// RTD System Messages (133-136)
#define MSG_RTD_CONNECT 133
#define MSG_RTD_DISCONNECT 134
#define MSG_RTD_UPDATE 135
#define MSG_RTD_HEARTBEAT 136

// Command (ribbon/macro) Messages (137)
#define MSG_COMMAND_INVOKE 137

// RTD-once grid result (guest->host one-shot grid/numgrid delivery) (138)
#define MSG_RTD_ONCE_GRID 138

// User Functions Start
#define MSG_USER_START 140

// Helper for logging SHM errors
std::string SHMErrorToString(shm::Error err);

// Function declarations
namespace xll {
    void StartWorker();
    void StopWorker();

    // SendRefCachePayloadOnce ships a composite RTD argument's serialized
    // payload to the Go server exactly once per calc cycle, keyed by its
    // content-hash token (see ContentHashToken / AGENTS.md §19.3).
    //
    // `payload` is a FINISHED protocol::SetRefCacheRequest FlatBuffer (key =
    // token, val = the Any-wrapped grid/range/numgrid/any). If `token` has
    // already been sent this cycle (tracked in g_sentRefCache, cleared on
    // CalculationEnded) this is a no-op. Otherwise it sends MSG_SETREFCACHE
    // and, on a successful ack, records the token as sent.
    //
    // MUST be called BEFORE xlfRtd for that argument so the server has the
    // payload cached before ConnectData triggers the handler dispatch. Returns
    // true if the payload is known-delivered (already-sent OR sent-and-acked).
    bool SendRefCachePayloadOnce(const std::string& token, const uint8_t* payload, size_t size);
}
