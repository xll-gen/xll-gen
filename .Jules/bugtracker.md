# Bug Tracker

| ID | Type | Severity | Status | Description |
| :--- | :--- | :--- | :--- | :--- |
| BUG-001 | Logic | **High** | **Resolved** | **Async Server Busy Hang**: In `server.go.tmpl`, when the worker pool is saturated, the `default` select case drops the request without notifying Excel. This causes the calling cell to hang indefinitely in `#GETTING_DATA`. Fix requires queuing an error result explicitly. |
| SEC-001 | Security | **Medium** | **Resolved** | **Command Injection / Path Safety**: In `xll_launch.cpp`, the `xll-shm` argument value is not quoted. If the project name (used for SHM name) contains spaces, the command line arguments will be parsed incorrectly. |
| MEM-001 | Memory | Low | Pending | **Fragile String Cleanup**: `GridToXLOPER12` creates `xltypeMulti` arrays with strings but does not set `xlbitDLLFree` on individual elements. `xlAutoFree12` correctly cleans them up by assuming ownership of all strings in a Multi, but this implicit contract is fragile. |
| MEM-002 | Memory | Low | Ignored | **Range Allocation Overhead**: `RangeToXLOPER12` allocates `sizeof(XLMREF12) + N * sizeof(XLREF12)`, which results in space for `N+1` references. This is safe (no buffer overflow) but slightly wasteful. No immediate action required. |
