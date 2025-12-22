# Bug Tracker

## Issues

| ID | Description | Severity | Status | User Judgment |
|----|-------------|----------|--------|---------------|
| BUG-001 | **DoS Vulnerability in `xll_worker.cpp`**: `HandleChunk` performs unbounded memory allocation (`pm.buffer.resize`) based on `total_size` from the incoming message header. A large value allows a guest to crash the host process. | Critical | Fixed | Approved |
| BUG-002 | **Memory Leak/Corruption Risk in `xll_async.cpp`**: `ProcessAsyncBatchResponse` allocates `XLOPER12` strings using `NewExcelString` and then calls `xlAutoFree12`. Fixed by distinguishing between `xlbitDLLFree` (using `xlAutoFree12`) and locally allocated nodes (using `ReleaseXLOPER12`). | High | Fixed | Approved |
| BUG-003 | **Lock Contention in `pkg/server/command_flush.go`**: `flushBuffers` holds `bufferLock` while running `algo.GreedyMesh`. Fixed by moving map processing outside the lock critical section. | Medium | Fixed | Approved |
