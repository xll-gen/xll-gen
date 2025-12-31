# Bug Tracker

## Issues

| ID | Description | Severity | Status | User Judgment |
|----|-------------|----------|--------|---------------|
| BUG-001 | **DoS Vulnerability in `xll_worker.cpp`**: `HandleChunk` performs unbounded memory allocation (`pm.buffer.resize`) based on `total_size` from the incoming message header. A large value allows a guest to crash the host process. | Critical | Fixed | Approved |
| BUG-002 | **Memory Leak/Corruption Risk in `xll_async.cpp`**: `ProcessAsyncBatchResponse` allocates `XLOPER12` strings using `NewExcelString` and then calls `xlAutoFree12`. Fixed by distinguishing between `xlbitDLLFree` (using `xlAutoFree12`) and locally allocated nodes (using `ReleaseXLOPER12`). | High | Fixed | Approved |
| BUG-003 | **Lock Contention in `pkg/server/command_flush.go`**: `flushBuffers` holds `bufferLock` while running `algo.GreedyMesh`. Fixed by moving map processing outside the lock critical section. | Medium | Fixed | Approved |
| BUG-004 | **Thread-Safety Violation in `ExecuteCommands`**: `xll_worker.cpp` calls `ExecuteCommands` from a background thread (`WorkerLoop`). `ExecuteCommands` invokes `xlSet` and `xlcFormatNumber`, which are strictly main-thread-only Excel C API functions. This will cause Excel to crash or hang. | Critical | Fixed | Approved |
| BUG-005 | **Scalar Memory Leak in `xll_commands.cpp`**: `ExecuteCommands` blindly calls `xlAutoFree12` on `pxValue` (from `AnyToXLOPER12`). If `pxValue` is a scalar (e.g., number/bool) allocated from the object pool, it lacks `xlbitDLLFree`. `xlAutoFree12` ignores it, and the node is never returned to the `ObjectPool`, leading to a leak. | High | Fixed | Approved |
