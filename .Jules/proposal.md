# Proposals for xll-gen

## 1. IPC Safety Improvements (Critical)
**Issue:** C++ IPC calls to `g_host.Send` often ignore the return value or check it incorrectly (checking `Value() > 0` without verifying `HasError()`).
**Impact:** If shared memory is full or connection is lost, the XLL might crash (accessing invalid result) or silently fail (SetRefCache not updating server), leading to data corruption or "phantom" bugs.
**Proposed Fixes:**
-   `xll_events.cpp`: Verify `!res.HasError()` before checking `res.Value()`.
-   `xll_converters.cpp`: Verify `SetRefCache` IPC result. If it fails, remove the key from local cache and fallback to sending the full data object instead of the cache key.
-   `xll_main.cpp`: Verify Async `Send` result. If it fails, invoke `xlAsyncReturn` with an error code to prevent Excel from hanging indefinitely.
**Status:** Fixed and Verified.

## 2. Protocol Error Code Alignment
**Issue:** `protocol.fbs` defines error codes like `Null = 2000`, matching Excel's `xltypeErr` display values or internal offset, but `XLOPER12.val.err` expects small integer constants (e.g., `xlerrNull = 0`, `xlerrValue = 15`). Casting `protocol::XlError` (2000+) directly to `int` for `val.err` results in invalid error codes (e.g., 2015 instead of 15).
**Impact:** Excel functions returning errors display `#NUM!` or incorrect error types instead of the intended `#VALUE!`, `#DIV/0!`, etc.
**Proposed Fix:** Modify `xll_converters.cpp` to subtract 2000 from the protocol error value before assigning it to `XLOPER12.val.err`.
**Status:** Fixed and Verified.

## 3. Code Cleanup: Nullable Scalars
**Issue:** `internal/generator/types.go` contains entries for `int?`, `float?`, `bool?` which are explicitly unsupported by policy and validation logic.
**Impact:** Dead code that might confuse future maintenance.
**Proposed Fix:** Remove these entries from `types.go`.
**Status:** Fixed and Verified.
