#pragma once

#include <cstdint>
#include <vector>
#include "include/xlcall.h"

// Process a batched async response message from the server.
// Returns 1 if handled (Excel successful), 0 otherwise.
int32_t ProcessAsyncBatchResponse(const uint8_t* req, std::vector<XLOPER12>& handles, std::vector<XLOPER12>& values);
