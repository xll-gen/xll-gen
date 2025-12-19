# Bug Tracker

## Analysis Report (2025-01-27)

| ID | Category | Description | Severity | User Opinion | Status |
|----|----------|-------------|----------|--------------|--------|
| BUG-001 | Stability | **C++ Worker Thread Race Condition**: `StartWorker` in `xll_worker.cpp` creates a detached thread. `xlAutoClose` in `xll_main.cpp` signals it to stop but does not wait (`join`) for it to finish. If the XLL is unloaded while the thread is running (e.g., waiting on `ProcessGuestCalls`), the thread will attempt to execute code that has been unloaded, leading to a process crash. | **Critical** | Resolved | **Fixed** |
| BUG-002 | Stability | **C++ Monitor Thread Race Condition**: `MonitorThread` in `xll_main.cpp` is detached. `xlAutoClose` closes the process handles (`g_procInfo.hProcess`) that `MonitorThread` is waiting on. This causes undefined behavior or a crash if the thread accesses the closed handle or executes code after DLL unload. | **Critical** | Resolved | **Fixed** |
| BUG-003 | Memory | **Potential XLOPER12 Corruption in Async**: `ProcessAsyncBatchResponse` in `xll_async.cpp` calls `xlAutoFree12` on the result of `AnyToXLOPER12`. If `AnyToXLOPER12` (from the external `types` library) ever returns a pointer to a static/thread-local `XLOPER12` instead of a pool-allocated one, this will cause memory corruption. | Medium | Resolved | **Fixed** |
| BUG-004 | Performance| **Go Async Batcher Blocking**: `FlushAsyncBatch` in `pkg/server/async_batcher.go` uses a retry loop with sleep (up to 2.5s) if the shared memory buffer is full. This can block the async processing goroutine, potentially stalling all async results. | Low | Accepted | Reported |
