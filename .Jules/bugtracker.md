# Bug Tracker

## Resolved Issues

### [C++ XLL] Invalid Pointer Cast in `GetSheetName` (Critical)
- **Status**: Resolved
- **Description**: The function `GetSheetName` contained invalid pointer casting logic (`idSheet` -> `WORD`) and incorrect `xlSheetNm` API usage that would lead to Excel crashes.
- **Resolution**: Upon review, `GetSheetName` was found to be unused in the current code path (although called, its return value `sheet` was ignored). The function and its call site were removed entirely, eliminating the risk.

### [C++ XLL] `GridToFlatBuffer` Returns Empty for Single Values (Major)
- **Status**: Resolved
- **Description**: `GridToFlatBuffer` returned an empty 0x0 grid when the input was a scalar (single value), causing data loss for scalar inputs to grid arguments.
- **Resolution**: Added logic to treat non-Multi (scalar) inputs as a 1x1 Grid containing the single scalar value.

## Pending User Approval

### [Go Server] Unbounded Buffer Growth in `CommandBatcher` (Major)
- **Severity**: Major
- **Location**: `pkg/server/command_batcher.go`
- **Description**: The `CommandBatcher` buffers `Set` and `Format` commands in `bufferedSets` and `bufferedFormats` maps. While it flushes when a *single* command exceeds `batchingThreshold`, it does not check the total size of the buffer when accumulating many small commands. A loop of 1,000,000 single-cell updates will cause the map to grow indefinitely until the request finishes, potentially causing an OOM crash.
- **Proposed Fix**: Check the size of `bufferedSets` and `bufferedFormats` after every insertion and flush if it exceeds a limit (e.g., 10,000 items).

### [C++ XLL] Shallow Free in `xlAutoFree12` for Strings (Minor)
- **Severity**: Minor
- **Location**: `internal/assets/files/xll_mem.cpp`
- **Description**: `xlAutoFree12` manually iterates `xltypeMulti` arrays and calls `delete[] elem->val.str`. This assumes that *all* strings in a returned array were allocated with `new XCHAR[]`. If `xll-gen` ever supports returning mixed arrays where some strings are static or managed differently, this will crash. Currently, `GridToXLOPER12` allocates them correctly, so this is safe but fragile.
- **Proposed Fix**: Ensure strict allocation policy is documented or check flags. (No code change needed strictly, but noted).

### [C++ XLL] Incorrect Error Handling in `GetSheetName` (Minor)
- **Status**: Resolved
- **Description**: `GetSheetName` returns `L""` on failure.
- **Resolution**: Function removed.
