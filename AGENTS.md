## Development Setup

For optimal developer experience (DX), please ensure `go-task` and `flatc` are available before starting work.

1.  **Install `go-task`**:
    Refer to [Task Installation](https://taskfile.dev/installation/).

2.  **Prepare Environment**:
    Run the following command to verify tools and automatically download `flatc` v25.9.23 (if missing):
    ```bash
    task setup
    ```
    This ensures `flatc` is cached locally without creating binary artifacts in the repository.

## 16. Directory Structure & Asset Generation

Understanding how source files in the repository map to the generated project structure is crucial for correctly handling `#include` paths in C++.

### 16.1 Source Layout (`internal/assets/files`)

The embedded C++ assets are organized in the `xll-gen` repository as follows:

```text
internal/assets/files/
├── src/                    # Source files (.cpp)
│   ├── xll_worker.cpp
│   ├── xll_log.cpp
│   └── ...
├── include/                # Header files (.h)
│   ├── xll_worker.h
│   ├── xll_log.h
│   └── ...
└── tools/
    └── compressor.cpp
```

### 16.2 Generated Layout (`generated/cpp`)

When `xll-gen generate` runs, it restructures these assets into a clean C++ project layout within `generated/cpp/`.

```text
my-project/generated/cpp/
├── xll_main.cpp            # From xll_main.cpp.tmpl
├── CMakeLists.txt
├── src/                    # Implementation files
│   ├── xll_worker.cpp
│   ├── xll_log.cpp
│   └── ...
├── include/                # Header files
│   ├── xll_worker.h
│   ├── xll_log.h
│   └── ...
└── tools/
    └── compressor.cpp
```

### 16.3 Include Paths & CMake

The generated `CMakeLists.txt` configures include directories to allow **flat includes**:

```cmake
target_include_directories(${PROJECT_NAME} PRIVATE
  ${CMAKE_CURRENT_SOURCE_DIR}
  ${CMAKE_CURRENT_SOURCE_DIR}/include
)
```

**Include Resolution Rules:**

1.  **NO `include/` Prefix:**
    *   Do **not** use `#include "include/Header.h"`.
    *   **Correct:** `#include "Header.h"`.

2.  **Resolution Logic:**
    *   The build system adds `generated/cpp/include` to the include path.
    *   Therefore, `xll_worker.h` (in `generated/cpp/include/`) is found directly as `"xll_worker.h"`.
    *   This applies to `xll_main.cpp` (root), files in `src/`, and files in `include/`.

**Best Practice:**
*   Place `.cpp` files in `internal/assets/files/src/`.
*   Place `.h` files in `internal/assets/files/include/`.
*   In all C++ code (templates and assets), use **flat includes**: `#include "xll_log.h"`.
*   Never bake the directory structure (like `include/` or `src/`) into the `#include` directive.

## 17. Dependencies & External Types

As of v0.1.0, core Excel types and utilities have been extracted to the upstream library [github.com/xll-gen/types](https://github.com/xll-gen/types).

### 17.1 Go Dependencies
- **Protocol**: Go code for IPC (Flatbuffers) is imported from `github.com/xll-gen/types/go/protocol`. The local `pkg/protocol` has been removed.

### 17.2 C++ Dependencies
- **Types Library**: The generated `CMakeLists.txt` uses `FetchContent` to download `github.com/xll-gen/types`.
- **Include Paths**: Common headers are included via the `types/` prefix:
    - `#include "types/converters.h"`
    - `#include "types/mem.h"`
    - `#include "types/xlcall.h"`
    - `#include "types/utility.h"`
    - `#include "types/ObjectPool.h"`
    - `#include "types/PascalString.h"`

This reduces code duplication in `internal/assets/files` and ensures consistency across generated projects.

## 18. Co-Change Clusters

Certain parts of the codebase are tightly coupled and must be updated together to preserve consistency.

### 18.1 Protocol & Types
The `protocol.fbs` definition is critical.
1.  **Schema Source**: `internal/templates/protocol.fbs` is the source for user C++ generation.
2.  **Go Types**: `github.com/xll-gen/types` (External Repo) is the source for the Go server package.
**Constraint**: These must be byte-compatible. Any change to `internal/templates/protocol.fbs` requires a simultaneous update to `xll-gen/types`, a new release of `types`, and a `go get` update in `dependencies.go`.

### 18.2 Shared Dependencies
The versions of core dependencies must be synchronized across the build system, the generator, and the toolchain:
1.  **C++ Build**: `internal/templates/CMakeLists.txt.tmpl` (`GIT_TAG` for `shm`, `types`, `flatbuffers`).
2.  **Go Setup**: `internal/generator/dependencies.go` (hardcoded `go get` commands in `updateDependencies`).
3.  **Toolchain**: `internal/flatc/flatc.go` (defines `flatcVersion` which must match `flatbuffers` in CMake).
4.  **Verification**: `cmd/doctor_version_test.go` (`TestFlatbuffersVersionConsistency`) enforces that the `flatc` version in Go matches the CMake tag.
5.  **Self**: `go.mod` of the `xll-gen` repository itself (for regression testing and tool stability).

### 18.3 Event Handling
When adding a new Excel event (e.g., `SheetActivate`):
1.  **Config**: Update `internal/config/config.go` (`Event` struct validation).
2.  **Mapping**: Update `internal/generator/funcmap.go` (`lookupEventCode`, `lookupEventId`).
3.  **Upstream**: Ensure `github.com/xll-gen/types` contains the `xlEvent` constant.
4.  **Schema**: Update `internal/templates/protocol.fbs` if the event requires a specific payload structure.

### 18.4 Type System Extensions
When adding or modifying a data type (e.g., adding `date` support):
1.  **Configuration**: Update `internal/config/config.go` (`validArgTypes`, `validReturnTypes`).
2.  **Metadata**: Update `internal/generator/types.go` (`typeRegistry`).
3.  **Schema**: Update `internal/templates/protocol.fbs` (add table/union member).
4.  **Upstream**: Update `github.com/xll-gen/types` to handle the new type.

### 18.5 Regression Test Assets
The integration tests in `internal/regtest` rely on a fixed set of files that must stay in sync.
1.  **Test Project**: `internal/regtest/testdata/xll.yaml` defines the function signatures and order.
2.  **Mock Host**: `internal/regtest/testdata/mock_host.cpp` hardcodes message IDs (e.g., `133`) and payload structures based on `xll.yaml`.
3.  **Go Server**: `internal/regtest/testdata/server.go` implements handlers matching `xll.yaml`.
**Constraint**: Any change to `testdata/xll.yaml` (e.g., adding a function) requires updating `mock_host.cpp` (new ID/case) and `server.go`.

### 18.6 Message ID Allocation
Message IDs are distributed across multiple definitions and must match exactly.
1.  **Definitions**: `internal/assets/files/include/xll_ipc.h` and `pkg/server/types.go` define constants (e.g., `MSG_USER_START = 133`, `MSG_CALCULATION_ENDED = 131`).
2.  **Generator (C++)**: `internal/templates/xll_main.cpp.tmpl` manually calculates user IDs (`133 + $i`).
3.  **Generator (Go)**: `internal/templates/server.go.tmpl` manually calculates user IDs (`133 + $i`).
4.  **Events**: `internal/generator/funcmap.go` hardcodes event IDs (e.g., `"131"` for `CalculationEnded`).
**Constraint**: If `MSG_USER_START` changes in `xll_ipc.h`, both templates, `pkg/server`, and `mock_host.cpp` must be updated.

## 19. Excel XLL Registration Rules

When generating the `xlfRegister` type string in `xll_main.cpp.tmpl`, follow these strict rules to avoid Excel registration failures or immediate unloads.

### 19.1 Type String Format
1.  **Thread Safety**: Always append `$` to the end of the type string to mark the function as thread-safe.
2.  **Synchronous Functions**:
    *   Format: `[ReturnTypeChar][ArgTypeChars]$`
    *   Example: `QJJ$` (Returns `LPXLOPER12`, takes two `long` integers).
3.  **Asynchronous Functions**:
    *   Format: `>[ArgTypeChars]X$`
    *   **CRITICAL**: Omit the return type character (e.g., `Q`). The `X` character (Async Handle) acts as the return parameter placeholder in the type string.
    *   Example: `>QX$` (Takes a string `Q`, uses async handle `X`).

### 19.2 Argument Mapping
*   **Return Types**: Use `lookupXllType` (usually returns `Q` for `LPXLOPER12`).
*   **Argument Types**: Use `lookupArgXllType`.
    *   `int` -> `J` (long)
    *   `float` -> `B` (double)
    *   `bool` -> `A` (bool)
    *   `string`/`any`/`range` -> `Q`/`U` (LPXLOPER12)
*   **Mismatches**: Ensure the C++ function signature matches these types (e.g., `int32_t` for `J`, `double` for `B`). A mismatch will cause stack corruption or Excel crashes.

## 20. Excel Load/Unload Patterns & SHM Lifecycle

Excel exhibits a "Probe Unload" pattern where it loads the XLL, checks entry points, and immediately unloads it (`DLL_PROCESS_DETACH`) before reloading it for actual use.
This causes issues with Shared Memory (SHM) if the destructor of the global host object closes the handles, as the Go server might interpret this as a shutdown signal.

**Strategy:**
1.  **Heap Allocation**: `g_host` is a pointer (`shm::DirectHost* g_phost`) allocated on the heap in `xlAutoOpen`.
2.  **Ignore Detach**: We explicitly ignore `DLL_PROCESS_DETACH` in `DllMain`. No cleanup is performed there.
3.  **Explicit Cleanup**: We only delete `g_phost` and stop threads in `xlAutoClose`, which is the definitive signal that the user is deactivating the Add-in.
4.  **SEH**: `DllMain` is wrapped in SEH (`__try`/`__except`) to prevent crashes during these rapid load/unload cycles.
