# Bug Tracker

<<<<<<< HEAD
## Analysis Report (2025-01-27)

| ID | Category | Description | Severity | User Opinion | Status |
|----|----------|-------------|----------|--------------|--------|
| BUG-001 | Stability | **C++ Worker Thread Race Condition**: `StartWorker` in `xll_worker.cpp` creates a detached thread. `xlAutoClose` in `xll_main.cpp` signals it to stop but does not wait (`join`) for it to finish. If the XLL is unloaded while the thread is running (e.g., waiting on `ProcessGuestCalls`), the thread will attempt to execute code that has been unloaded, leading to a process crash. | **Critical** | Resolved | **Fixed** |
| BUG-002 | Stability | **C++ Monitor Thread Race Condition**: `MonitorThread` in `xll_main.cpp` is detached. `xlAutoClose` closes the process handles (`g_procInfo.hProcess`) that `MonitorThread` is waiting on. This causes undefined behavior or a crash if the thread accesses the closed handle or executes code after DLL unload. | **Critical** | Resolved | **Fixed** |
| BUG-003 | Memory | **Potential XLOPER12 Corruption in Async**: `ProcessAsyncBatchResponse` in `xll_async.cpp` calls `xlAutoFree12` on the result of `AnyToXLOPER12`. If `AnyToXLOPER12` (from the external `types` library) ever returns a pointer to a static/thread-local `XLOPER12` instead of a pool-allocated one, this will cause memory corruption. | Medium | Resolved | **Fixed** |
| BUG-004 | Performance| **Go Async Batcher Blocking**: `FlushAsyncBatch` in `pkg/server/async_batcher.go` uses a retry loop with sleep (up to 2.5s) if the shared memory buffer is full. This can block the async processing goroutine, potentially stalling all async results. | Low | Accepted | Reported |
=======
| ID | Type | Severity | Status | Description |
| :--- | :--- | :--- | :--- | :--- |
| BUG-001 | Logic | **High** | **Resolved** | **Async Server Busy Hang**: In `server.go.tmpl`, when the worker pool is saturated, the `default` select case drops the request without notifying Excel. This causes the calling cell to hang indefinitely in `#GETTING_DATA`. Fix requires queuing an error result explicitly. |
| SEC-001 | Security | **Medium** | **Resolved** | **Command Injection / Path Safety**: In `xll_launch.cpp`, the `xll-shm` argument value is not quoted. If the project name (used for SHM name) contains spaces, the command line arguments will be parsed incorrectly. |
| MEM-001 | Memory | Low | Pending | **Fragile String Cleanup**: `GridToXLOPER12` creates `xltypeMulti` arrays with strings but does not set `xlbitDLLFree` on individual elements. `xlAutoFree12` correctly cleans them up by assuming ownership of all strings in a Multi, but this implicit contract is fragile. |
| MEM-002 | Memory | Low | Ignored | **Range Allocation Overhead**: `RangeToXLOPER12` allocates `sizeof(XLMREF12) + N * sizeof(XLREF12)`, which results in space for `N+1` references. This is safe (no buffer overflow) but slightly wasteful. No immediate action required. |
>>>>>>> origin/fix/logging-path-and-header-shadowing-4763239286566029706
