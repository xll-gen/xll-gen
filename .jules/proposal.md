# Proposals for xll-gen

## 1. IPC Safety Improvements (Critical)
**Issue:** C++ IPC calls to `g_host.Send` often ignore the return value or check it incorrectly (checking `Value() > 0` without verifying `HasError()`).
**Impact:** If shared memory is full or connection is lost, the XLL might crash (accessing invalid result) or silently fail (SetRefCache not updating server), leading to data corruption or "phantom" bugs.
**Proposed Fixes:**
-   `xll_events.cpp`: Verify `!res.HasError()` before checking `res.Value()`.
-   `xll_converters.cpp`: Verify `SetRefCache` IPC result. If it fails, remove the key from local cache and fallback to sending the full data object instead of the cache key.
-   `xll_main.cpp`: Verify Async `Send` result. If it fails, invoke `xlAsyncReturn` with an error code to prevent Excel from hanging indefinitely.
**Status:** Fixed and Verified.

## 2. Protocol Error Code Alignment (Rejected)
**Issue:** `protocol.fbs` defines error codes like `Null = 2000`.
**Proposal:** Modify `xll_converters.cpp` to subtract 2000.
**Status:** Rejected by User. Code reverted.

## 3. Code Cleanup: Nullable Scalars
**Issue:** `internal/generator/types.go` contains entries for `int?`, `float?`, `bool?` which are explicitly unsupported by policy and validation logic.
**Impact:** Dead code that might confuse future maintenance.
**Proposed Fix:** Remove these entries from `types.go`.
**Status:** Fixed and Verified.

## 4. Refactor Async Chunk Logic
**Issue:** `pkg/server/async_batcher.go` duplicates the logic for constructing Chunk messages, which is already available in `pkg/server/protocol_helpers.go`.
**Impact:** Increased maintenance burden and risk of inconsistency if protocol changes.
**Proposed Fix:** Update `pkg/server/async_batcher.go` to use `server.BuildChunkResponse`.
**Status:** Fixed and Verified.
