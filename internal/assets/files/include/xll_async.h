#pragma once

#include <cstdint>
#include <vector>
#include "types/xlcall.h"
#include "types/protocol_generated.h"

// Process a batched async response message from the server.
// `batch` is the FlatBuffers root of a MSG_BATCH_ASYNC_RESPONSE payload;
// the function dispatches xlAsyncReturn for each result it contains.
// A null or empty batch is a no-op.
void ProcessAsyncBatchResponse(const protocol::BatchAsyncResponse* batch);
