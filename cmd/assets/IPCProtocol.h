#pragma once
#include <cstdint>

namespace shm {

#pragma pack(push, 1)
/**
 * @brief Transport Header used in the Facade layer.
 *
 * Prepended to the user payload when using high-level abstractions
 * to match asynchronous requests with responses.
 */
struct TransportHeader {
    /** @brief Unique Request ID. */
    uint64_t req_id;
};
#pragma pack(pop)

}
