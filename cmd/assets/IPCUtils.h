#pragma once

#include <stdint.h>
#include <atomic>

// Message IDs for control messages

/**
 * @brief Message ID for normal data payload.
 */
#define MSG_ID_NORMAL 0

/**
 * @brief Message ID for heartbeat request (keep-alive).
 */
#define MSG_ID_HEARTBEAT_REQ 1

/**
 * @brief Message ID for heartbeat response.
 */
#define MSG_ID_HEARTBEAT_RESP 2

/**
 * @brief Message ID for shutdown signal.
 * Used to signal the Guest to terminate its worker loop.
 */
#define MSG_ID_SHUTDOWN 3

/**
 * @brief Message ID for FlatBuffer payload.
 * Used when sending Zero-Copy FlatBuffers where the data is aligned to the end of the buffer.
 */
#define MSG_ID_FLATBUFFER 10

// Host/Guest Sleeping States

/**
 * @brief Indicates the Host is active (spinning or processing).
 */
#define HOST_STATE_ACTIVE 0

/**
 * @brief Indicates the Host is waiting on the Response Event.
 */
#define HOST_STATE_WAITING 1

/**
 * @brief Indicates the Guest is active (spinning or processing).
 */
#define GUEST_STATE_ACTIVE 0

/**
 * @brief Indicates the Guest is waiting on the Request Event.
 */
#define GUEST_STATE_WAITING 1

namespace shm {

// Direct Mode Slot Header
// Aligned to 128 bytes to prevent false sharing
/**
 * @brief Header structure for a single Direct Mode slot.
 *
 * This structure resides in shared memory and coordinates the state
 * of a single request/response transaction.
 * Aligned to 128 bytes to prevent false sharing between slots.
 */
struct SlotHeader {
    /**
     * @brief Padding to ensure cache line alignment and avoid false sharing with ExchangeHeader or previous slot.
     */
    uint8_t pre_pad[64];

    /**
     * @brief Current state of the slot (Free, Busy, ReqReady, RespReady).
     * Accessed via atomic operations.
     */
    std::atomic<uint32_t> state;

    /**
     * @brief Size of the request payload in bytes.
     * Positive: Data starts at offset 0.
     * Negative: Data starts at end (size = -reqSize).
     */
    int32_t reqSize;

    /**
     * @brief Size of the response payload in bytes.
     * Positive: Data starts at offset 0.
     * Negative: Data starts at end (size = -respSize).
     */
    int32_t respSize;

    /**
     * @brief Message ID (e.g., MSG_ID_NORMAL, MSG_ID_SHUTDOWN).
     */
    uint32_t msgId;

    /**
     * @brief State of the Host (Active/Waiting).
     * Used by the Guest to determine whether to signal the Host.
     */
    std::atomic<uint32_t> hostState;

    /**
     * @brief State of the Guest (Active/Waiting).
     * Used by the Host to determine whether to signal the Guest.
     */
    std::atomic<uint32_t> guestState;

    /**
     * @brief Padding to align the struct to 128 bytes total size.
     */
    uint8_t padding[40];
};

// Slot State Constants
/**
 * @brief Enumeration of possible Slot states.
 */
enum SlotState {
    /** @brief Slot is free. Host can claim it. */
    SLOT_FREE = 0,
    /** @brief Request data is written. Ready for Guest to process. */
    SLOT_REQ_READY = 1,
    /** @brief Response data is written. Ready for Host to read. */
    SLOT_RESP_READY = 2,
    /** @brief Transaction complete (transient state). */
    SLOT_DONE = 3,
    /** @brief Slot is claimed by Host, writing request. */
    SLOT_BUSY = 4
};

// Direct Mode Exchange Header
// First 64 bytes of Shared Memory
/**
 * @brief Header structure located at the beginning of the Shared Memory region.
 *
 * Contains metadata about the shared memory layout, allowing the Guest
 * to map the memory correctly.
 */
struct ExchangeHeader {
    /** @brief Number of slots in the pool. */
    uint32_t numSlots;
    /** @brief Total size of each slot in bytes. */
    uint32_t slotSize;
    /** @brief Offset of the Request buffer within a slot. */
    uint32_t reqOffset;
    /** @brief Offset of the Response buffer within a slot. */
    uint32_t respOffset;
    /** @brief Padding to align to 64 bytes. */
    uint8_t padding[48];
};

}
