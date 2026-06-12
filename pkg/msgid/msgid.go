// Package msgid is the single Go-side source of truth for the SHM message-ID
// constants. It is a leaf package with no imports from pkg/server or pkg/rtd,
// which lets both depend on it without the import cycle that previously forced
// pkg/rtd to carry a shadow copy of MsgRtdUpdate.
//
// These values MUST stay byte-identical to the C++ mirror in
// internal/assets/files/include/xll_ipc.h (the MSG_* #defines). See
// AGENTS.md §18.6 (message-ID allocation / mirror discipline). pkg/server
// re-exports each constant as an alias so existing references through
// server.Msg* keep compiling unchanged.
package msgid

const (
	// MsgAck is the acknowledgement message (mirrors MSG_ACK).
	MsgAck = 2

	// System error signals are sourced from shm directly — see
	// shm.MsgTypeSystemError (value 127). No mirror is defined here.

	// MsgBatchAsyncResponse carries a coalesced batch of async results
	// (mirrors MSG_BATCH_ASYNC_RESPONSE).
	MsgBatchAsyncResponse = 128
	// MsgChunk is a single chunk of a chunked transfer (mirrors MSG_CHUNK).
	MsgChunk = 129
	// MsgSetRefCache primes the reference-hash cache (mirrors MSG_SETREFCACHE).
	MsgSetRefCache = 130
	// MsgCalculationEnded signals the end of a calculation cycle (mirrors
	// MSG_CALCULATION_ENDED).
	MsgCalculationEnded = 131
	// MsgCalculationCanceled signals a canceled calculation (mirrors
	// MSG_CALCULATION_CANCELED).
	MsgCalculationCanceled = 132

	// RTD Messages (133-136)

	// MsgRtdConnect is an RTD topic connect (mirrors MSG_RTD_CONNECT).
	MsgRtdConnect = 133
	// MsgRtdDisconnect is an RTD topic disconnect (mirrors MSG_RTD_DISCONNECT).
	MsgRtdDisconnect = 134
	// MsgRtdUpdate pushes a new value to an RTD topic (mirrors MSG_RTD_UPDATE).
	MsgRtdUpdate = 135
	// MsgRtdHeartbeat is the RTD keepalive (mirrors MSG_RTD_HEARTBEAT).
	MsgRtdHeartbeat = 136

	// MsgCommandInvoke is a ribbon/macro command invocation — must stay in
	// sync with MSG_COMMAND_INVOKE in xll_ipc.h.
	MsgCommandInvoke = 137

	// MsgUserStart is the first message ID allocated to user functions
	// (mirrors MSG_USER_START). User function i gets MsgUserStart + i.
	MsgUserStart = 140
)
