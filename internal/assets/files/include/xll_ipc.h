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
//
// These are APPLICATION-layer IDs: each one travels as the shm slot's msgType,
// so it shares one numbering space with shm's MsgType enum (shm/IPCUtils.h).
// 0-127 are RESERVED by the transport (NORMAL 0, HEARTBEAT_REQ 1,
// HEARTBEAT_RESP 2, SHUTDOWN 3, FLATBUFFER 10, GUEST_CALL 11, STREAM_START 13,
// STREAM_CHUNK 14, SYSTEM_ERROR 127) and must never be allocated below.
// Everything here must stay >= APP_START (128) and byte-identical to the Go
// mirror in pkg/msgid (AGENTS.md 18.6).

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

// Acknowledgement (139). Was 2 until 2026-08-03, which is shm's
// MsgType::HEARTBEAT_RESP; moved into the application range so an ACK response
// cannot be read as a transport heartbeat.
#define MSG_ACK 139

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
