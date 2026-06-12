# AI Agent Instructions for xll-gen

This file is the authoritative guidance for AI agents and contributors working on `xll-gen`.

## 0. Scope & Companion Repos

`xll-gen` generates Excel XLL add-ins backed by an out-of-process Go server, communicating via shared memory + FlatBuffers. It coordinates three companion repos that each have their own `AGENTS.md`:

* **`github.com/xll-gen/shm`** — lock-free C++/Go shared-memory IPC. See its `AGENTS.md` before touching anything that crosses the IPC boundary.
* **`github.com/xll-gen/types`** — FlatBuffers protocol schema and C++ ↔ XLOPER12 converters. See its `AGENTS.md` when changing wire types.
* **`github.com/xll-gen/sugar`** — Windows COM automation in Go (xlwings-parity surface). Not in the generated runtime path; consult its `AGENTS.md` if you write tooling that drives Excel directly.

When a change crosses repo boundaries, update **all** affected `AGENTS.md` files in the same change.

## 0.1. Platform Support (HARD CONSTRAINT)

`xll-gen` is **Windows-only** and targets **x86 / x86-64 (Intel/AMD)** architectures exclusively. This is not a "primary focus" — it is a hard constraint:

* **OS**: Microsoft Windows. No Linux, no macOS, no WSL as a runtime target.
* **CPU**: x86 (32-bit) and x86-64 (64-bit, "x64"). **No ARM (incl. Windows-on-ARM, Apple Silicon).**
* **Excel**: A generated XLL's bitness MUST match the host Excel's bitness. 32-bit Excel → 32-bit XLL; 64-bit Excel → 64-bit XLL.
* **Memory model assumption**: x86/x64 provides Total Store Order (TSO). Implementations and reviews MAY rely on TSO guarantees — sequential consistency of acquire-release pairs is hardware-provided. ARM weak-memory-model concerns are out of scope for the xll-gen runtime path.

**Implications for agents and reviewers**:

* Findings phrased as "ARM-only bug" or "weak memory model concern" against xll-gen runtime code are **non-issues** unless they also affect x86 (rare).
* Cross-platform build infra (Linux CI for Go-only unit tests, etc.) is acceptable as a developer convenience but is NOT a supported deployment target.
* Companion repos have different platform stories: `shm` is cross-platform by design (its Linux backend exists for testing and potential reuse) but its production deployment via `xll-gen` is Windows x86/x64 only; `sugar` is Windows-only (COM-bound); `types` Go code is portable but its C++ side targets Windows + the SDK.

When in doubt about whether a concern applies, ask: "Does this affect Windows x86/x64 with stock MSVC/MinGW + recent Excel?" If no → out of scope for xll-gen.

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
- **Server**: Static server logic resides in `pkg/server`. This package is imported by the generated `server.go`.

### 17.2 C++ Dependencies
- **Types Library**: The generated `CMakeLists.txt` uses `FetchContent` to download `github.com/xll-gen/types`.
- **SHM Library**: The generated `CMakeLists.txt` uses `FetchContent` to download `github.com/xll-gen/shm`.
- **Internalized Assets**: Core runtime components like RTD and memory pooling are now part of the `xll-gen` assets (`internal/assets/files/include/rtd/`) and are automatically included in the generated project.
- **Include Paths**: Common headers are included via the `types/` or `rtd/` prefix:
    - `#include "types/protocol_generated.h"`
    - `#include "rtd/server.h"`
    - ...

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
1.  **C++ Build**: `internal/templates/CMakeLists.txt.tmpl` (defines `GIT_TAG` for `shm`, `types`, `flatbuffers`, `zstd`, and `phmap`).
2.  **Go Setup**: `internal/generator/dependencies.go` (hardcoded `go get` commands for `shm` and `types`).
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
1.  **Definitions**: `internal/assets/files/include/xll_ipc.h` and `pkg/server/types.go` define constants (e.g., `MSG_USER_START = 140`, `MSG_CALCULATION_ENDED = 131`, `MSG_RTD_CONNECT = 133`).
2.  **Generator (C++)**: `internal/templates/xll_main.cpp.tmpl` manually calculates user IDs (`140 + $i`).
3.  **Generator (Go)**: `internal/templates/server.go.tmpl` manually calculates user IDs (`140 + $i`).
4.  **Events**: `internal/generator/funcmap.go` hardcodes event IDs (e.g., `"131"` for `CalculationEnded`).
**Constraint**: If `MSG_USER_START` changes in `xll_ipc.h`, both templates, `pkg/server`, and `mock_host.cpp` must be updated.

### 18.7 Configuration System
The configuration structure is coupled with the generator templates.
1.  **Definition**: `internal/config/config.go` defines the `Config` struct and validation logic.
2.  **Templates**: `internal/templates/xll_main.cpp.tmpl` and `server.go.tmpl` rely on the specific field names and structure of the `Config` object.
**Constraint**: Adding or renaming fields in `xll.yaml` (and thus `Config`) requires verifying and updating both the validation logic and the usage in templates.

### 18.8 Import Path Rewriting
The generator dynamically rewrites generated Go imports to match the external `types` repository structure.
1.  **Rewriter**: `internal/generator/dependencies.go` (`fixGoImports`) contains regex logic to replace local `protocol` imports with `github.com/xll-gen/types/go/protocol`.
2.  **Target**: The external repository `github.com/xll-gen/types` must maintain this exact package path.
**Constraint**: If the `types` repository structure changes (e.g., moving `go/protocol` to `protocol`), the regex in `dependencies.go` must be updated immediately.

### 18.9 Template & Runtime Coupling
The logic in generated templates often relies on specific APIs exposed by the static runtime packages.
1.  **Go Server**: `internal/templates/server.go.tmpl` calls functions in `pkg/server` (e.g., `NewAsyncBatcher`, `ChunkManager`). Signatures must match exactly.
2.  **C++ Host**: `internal/templates/xll_main.cpp.tmpl` calls functions in `internal/assets/files/include/xll_ipc.h` (e.g., `StartWorker`).
**Constraint**: Refactoring `pkg/server` or C++ assets is a breaking change for the generator templates. Always verify templates compile after modifying static runtime code.

### 18.10 Smoke Test Assets
`internal/smoketest` ships a minimal end-to-end project that loads a generated XLL into real Excel via COM (`Application.RegisterXLL`) and round-trips sync, async, and RTD calls. The embedded fixtures must stay in sync with each other AND with the generator's expectations.
1.  **Project Manifest**: `internal/smoketest/testdata/xll.yaml` declares three functions (`Add` sync, `AsyncAdd` async, `RtdTick` rtd) plus the RTD config (`rtd.enabled`, `prog_id`). `gen.disable_pid_suffix: true` pins the SHM name to `xll_smoke` so XLL and server agree without runtime negotiation.
2.  **Server**: `internal/smoketest/testdata/main.go` provides `Add`, `AsyncAdd`, `RtdTick_RTD`, plus the mandatory `OnRtdConnect`/`OnRtdDisconnect`/`OnCalculation*` handlers. The package import path is hardcoded to `xll_smoke/generated` — keep `xll.yaml` project name aligned.
3.  **Driver**: `internal/smoketest/excel.go` drives Excel via `go-ole` (direct dep — promote with `go mod tidy` if removed). Polls `#GETTING_DATA` → numeric for async and `#N/A` → numeric for RTD with a fixed timeout.
4.  **Lifecycle**: graceful teardown clears the RTD formula BEFORE `Application.Quit` so `DisconnectData` runs while `g_phost` is still alive. The §23.0 drain (`WaitForRtdConnectDrain`) covers any in-flight Connect threads.
**Constraint**: Changes to `XllService` interface contract (e.g., adding mandatory handlers) require updating `testdata/main.go`. Changes to RTD subscription path or SHM lifecycle require running the smoke test (`go test -tags=xll_smoke ./cmd/... -run TestSmoke_All`) before release.

### 18.11 Commands & Ribbon
Native ribbon buttons and XLL commands (macros) form one tightly-coupled cluster spanning config, the ribbon XML generator, the templates, the C++ COM helper, and the IPC protocol. A change to any one of these almost always requires touching the others.

1.  **Config**: `internal/config/config.go` defines `Command` / `RibbonConfig` (+ command-name charset validation, structured-vs-raw-XML mutual exclusion, `buttons[].command` → `commands[].name` cross-check, `commands`/`functions` name-collision check).
2.  **Ribbon package**: `internal/ribbon/` (customUI XML generation, XML validation including the raw-XML `onAction` cross-check, and embedding the XML as a C++ string literal).
3.  **Templates**: `internal/templates/{interface.go.tmpl, server.go.tmpl, xll_main.cpp.tmpl, CMakeLists.txt.tmpl}` (generated handler interface method per command, dispatch wiring, command `xlfRegister` with `macroType=2`, and any new link/source entries).
4.  **C++ assets**: `internal/assets/files/include/com/*` + `src/ribbon_addin.cpp` (the `RibbonAddIn` COM class — `IDTExtensibility2` + `IRibbonExtensibility` + `IDispatch`).
5.  **Generator**: `internal/generator/gen_cpp.go` emits `ribbon_xml.h` (the embedded ribbon XML literal).

**Message-ID mirror** (same discipline as §18.6): `MSG_COMMAND_INVOKE` (`internal/assets/files/include/xll_ipc.h`) ↔ `MsgCommandInvoke` (`pkg/server/types.go`) ↔ `CommandInvokeRequest` / `CommandInvokeResponse` in `protocol.fbs` — and `protocol.fbs` lives in BOTH the templates copy (`internal/templates/protocol.fbs`) AND the external `github.com/xll-gen/types` repo copy. All four must agree (§18.1 cross-repo constraint applies).

**Threading contract (LOAD-BEARING — do not "optimize" away):** `RibbonAddIn::Invoke` and the generated `Cmd_*` command procs are **fire-and-forget**. They send `CommandInvokeRequest` over SHM and return immediately; they MUST NEVER wait on the Go handler. A handler may re-enter Excel via COM (sugar), which marshals back to Excel's STA thread — a synchronous wait from the same STA thread **deadlocks Excel**. The `CommandInvokeResponse` is a *delivery ack only* (routing success/failure, logged), not handler completion. The Go side runs each handler in its own panic-recovered goroutine, exactly like `HandleRtdConnect` / `HandleCalculationCanceled` in `pkg/server/handlers.go`.

**Teardown contract:** `xlAutoClose` ordering is:
1.  `SetRibbonConnected(false)` — Excel must release the live COM ref while the DLL is still mapped.
2.  `CoRevokeClassObject`.
3.  Unregister the HKCU COM-addin keys (best-effort; idempotent on next load).
4.  `WaitForCommandDrain(2000)` — bounded wait for in-flight command sends; 2 s timeout logged as a warning, does not block teardown.
5.  `OnAutoClose` (deletes `g_phost`).

Detached `SendCommandInvoke` threads follow the SAME `g_isUnloading` self-abort contract as RTD `ConnectData` (§20.2 / §23.0): on forced unload each thread re-checks `g_isUnloading` at every yield point and aborts before touching `g_host`.

**set-before-connect contract:** `SetCommands` / `SetRibbonXml` run on the STA thread inside `xlAutoOpen` **BEFORE** COM-addin registration and connect. The backing globals are intentionally **unsynchronized** — correctness depends on this strict ordering. NEVER move registration off-thread and NEVER introduce a message pump between the `Set*` calls and connect, or the globals become observably racy.

**Graceful degradation (see design §1.4):** if HKCU registration/connect fails (group-policy-locked desktops), worksheet functions / RTD / async must keep working unchanged, registered `commands` stay invocable via shortcut and by typing the name into Alt+F8 (`xlfRegister`/`macroType=2` does not depend on the COM/ribbon path), and failure is silent except for a logged warning.

**Constraint**: Adding or renaming a `commands`/`ribbon` field, changing the ribbon XML shape, or touching `CommandInvokeRequest/Response` requires walking all five locations above plus the message-ID mirror, and verifying the templates still compile.

## 19. Excel XLL Registration Rules

When generating the `xlfRegister` type string in `xll_main.cpp.tmpl`, follow these strict rules to avoid Excel registration failures or immediate unloads.

### 19.1 Type String Format
1.  **Thread Safety**: Append `$` to the end of the type string to mark the function as thread-safe — **except** for caller-aware functions. Caller-aware functions are registered as macro-sheet equivalents (`#`, required so the wrapper can call `xlfGetCell` for the caller's number format), and Excel rejects `#` combined with `$`: `xlfRegister` returns `xlretSuccess` but the register ID is `xltypeErr` and the worksheet name resolves to `#NAME?`. So: caller-aware → `...#` (no `$`), everything else → `...$`.
2.  **Synchronous Functions** (`mode: "sync"`):
    *   Format: `[ReturnTypeChar][ArgTypeChars]$`
    *   Example: `QJJ$` (Returns `LPXLOPER12`, takes two `long` integers).
3.  **Asynchronous Functions** (`mode: "async"`):
    *   **Note**: The `async: true` configuration field is deprecated. Use `mode: "async"` in `xll.yaml` instead.
    *   Format: `>[ArgTypeChars]X$`
    *   **CRITICAL**: Omit the return type character (e.g., `Q`). The `X` character (Async Handle) acts as the return parameter placeholder in the type string.
    *   Example: `>QX$` (Takes a string `Q`, uses async handle `X`).
4.  **RTD Functions** (`mode: "rtd"`):
    *   Format: `Q$` (Always returns `LPXLOPER12` via `xlfRtd`).

### 19.2 Argument Mapping
*   **Return Types**: Use `lookupXllType`. The return code is **always `Q`** for `LPXLOPER12` returns (and `K%` for `numgrid`). `U` is never valid in return position — wrappers return value XLOPER12s, not range references, and a `U` return breaks the registration (worksheet name → `#NAME?`).
*   **Argument Types**: Use `lookupArgXllType`.
    *   `int` -> `J` (long)
    *   `float` -> `B` (double)
    *   `bool` -> `A` (bool)
    *   `string` -> `Q` (LPXLOPER12, value)
    *   `any`/`range`/`grid` -> `U` (LPXLOPER12, reference allowed; argument position only)
*   **Mismatches**: Ensure the C++ function signature matches these types (e.g., `int32_t` for `J`, `double` for `B`). A mismatch will cause stack corruption or Excel crashes.

### 19.3 Execution-Mode Guidance (sync / async / rtd)

`mode: "async"` does **not** keep the sheet responsive: Excel holds the
calculation transaction open until all pending `xlAsyncReturn` results arrive,
so no new recalculation (volatile ticks, RTD-triggered recalcs) runs in the
meantime — a single long async call feels identical to sync. Async buys
**concurrency** (N calls in one calculation overlap) and the guarantee that
dependents only see the final value. For multi-second work where interactive
feel matters, the RTD pattern is the right tool (cell returns a placeholder
immediately; result arrives via RTD push) — the same approach Excel-DNA uses
for its async support. Full decision matrix + RTD caveats (2s default
throttle, placeholder propagation to dependents, no F9 re-run while the topic
is connected, topic-string argument limits): README "Choosing an Execution
Mode". A generated one-shot wrapper (`mode: "rtd-once"`) is a backlog design
item.

## 20. Excel Load/Unload Patterns & SHM Lifecycle

Excel exhibits a "Probe Unload" pattern where it loads the XLL, checks entry points, and immediately unloads it (`DLL_PROCESS_DETACH`) before reloading it for actual use. This also applies when an Add-in is disabled or forcefully unloaded while background threads are running.

### 20.1 Crash on Unload Issue
If global `std::thread` objects (like `g_monitorThread` or `g_workerThread`) are destroyed while they are still **joinable** during `DLL_PROCESS_DETACH`, the C++ runtime will call `std::terminate()`. This causes the Excel process to crash or the Add-in to "disappear" (detach) immediately.

### 20.2 The Detach Solution
To prevent this crash, we employ an **Explicit Detach Strategy** in `DllMain`:

1.  **Check Unload State**: If `DLL_PROCESS_DETACH` is called and our explicit cleanup function (`xlAutoClose`) has **not** run (indicated by `!g_isUnloading`), it means we are in a forced unload scenario.
2.  **Leak, Don't Crash**: In this specific case, we explicitly call `.detach()` on global thread objects.
    *   This prevents the destructor from calling `std::terminate()`.
    *   The threads continue running (leaked) until the OS cleans up the process resources.
    *   This is safer than crashing the host process.
3.  **Precedent**: This strategy is also observed in other advanced Excel frameworks like [xlOil](https://github.com/cunnane/xloil), which implements a `detachPlugins` mechanism to handle similar lifecycle challenges.

**Implementation Reference**: See `internal/assets/files/src/xll_lifecycle.cpp` (`DllMain`).

## 21. C++ Name Mangling & Export Rules

All functions intended to be called by Excel (entry points like `xlAutoOpen` and all user-defined XLL functions) must be exported with **C linkage** to prevent C++ name mangling.

### 21.1 The Requirement
If a function is defined as `__declspec(dllexport) void __stdcall MyFunc()`, the C++ compiler will mangle its name (e.g., `_Z7MyFuncv`). Excel's `xlfRegister` function expects the exact name provided in the registration string. If the name is mangled, registration will fail silently or return error code 1 (`xlretFailed`).

### 21.2 Correct Export Pattern
Always use `extern "C"` in combination with `__declspec(dllexport)` and `__stdcall`:

```cpp
extern "C" __declspec(dllexport) LPXLOPER12 __stdcall MyFunction(int32_t a) {
    // ...
}
```

### 21.3 Template Implementation
In `internal/templates/xll_main.cpp.tmpl`, all user-defined functions and built-in event handlers (like `CalculationEnded`) must be wrapped in `extern "C"`.

**Verification**: Use `dumpbin /exports <filename>.xll` (Windows SDK) or `nm -D <filename>.xll` (MinGW) to verify that the exported names are "clean" and not mangled.

## 22. RTD RefreshData SAFEARRAY Layout

The `IRtdServer::RefreshData` method must return a two-dimensional `SAFEARRAY` of `VARIANT`s with a specific layout for Excel to correctly process real-time updates.

### 22.1 Required Layout: `[TopicCount][2]`

Excel expects an array where topics are the primary dimension and each topic has an ID and a Value. In SafeArray terms, the dimension that changes fastest (Dimension 1) should be the Row index (ID/Value), and the dimension that changes slowest (Dimension 2) should be the Topic index.

### 22.2 SAFEARRAY Dimension Order (C++)

In C++, `SAFEARRAYBOUND` array is defined from the **least significant** (Dimension 1, rightmost) dimension to the **most significant** (Dimension 2, leftmost) dimension.

To achieve the correct layout:
1.  **`bounds[0]` (Rightmost / Dimension 1)**: Set `cElements` to `2`.
2.  **`bounds[1]` (Leftmost / Dimension 2)**: Set `cElements` to the number of topics being updated (`TopicCount`).

```cpp
SAFEARRAYBOUND bounds[2];
bounds[0].cElements = 2;           // Dim 1 (ID/Value)
bounds[0].lLbound = 0;
bounds[1].cElements = *TopicCount; // Dim 2 (Topics)
bounds[1].lLbound = 0;
```

### 22.3 Indexing with `SafeArrayPutElement`

The `indices` array passed to `SafeArrayPutElement` follows the order of dimensions in `SAFEARRAYBOUND` array, where `indices[0]` is the **rightmost** (least significant) dimension.

*   **Topic ID**: `indices[0] = 0` (Row 0), `indices[1] = i` (Topic i).
*   **Value**: `indices[0] = 1` (Row 1), `indices[1] = i` (Topic i).

Failure to follow this exact layout (e.g., swapping Rows and Columns) will result in Excel failing to update the cell values, causing them to stay stuck at "Connecting..." or show #N/A.

## 23. Known Improvement Backlog

These came out of a code review on 2026-05-16. Address them as part of normal work; do not block on a dedicated epic.

### 23.0 C++ Audit (2026-05-16) — Status

A focused C++ audit on 2026-05-16 produced 3 HIGH + 7 MED + 5 LOW findings. The items below tracked as **DONE** were patched the same day; **OPEN** items remain.

* **DONE — HIGH:** `internal/assets/files/src/xll_cache.cpp` `GetOrComputeRefHash`: stack buffer for `XLMREF12` was sized as `sizeof(WORD) + sizeof(XLREF12)` (18) but padding makes `sizeof(XLMREF12)==20` on common ABIs, overrunning by 2 bytes. Fixed by using `alignas(XLMREF12) char mrefBuf[sizeof(XLMREF12)]` and adding a file-scope `static_assert(sizeof(XLMREF12) >= sizeof(WORD) + sizeof(XLREF12), ...)`.
* **DONE — HIGH:** `internal/assets/files/include/xll_async.h` declared `int32_t ProcessAsyncBatchResponse(const uint8_t*, std::vector<XLOPER12>&, std::vector<XLOPER12>&)` while the implementation in `xll_async.cpp` was `void ProcessAsyncBatchResponse(const protocol::BatchAsyncResponse*)` — a latent ODR violation. Header updated to match the implementation; `xll_worker.cpp` now `#include`s `xll_async.h` instead of forward-declaring locally (single source of truth).
* **DONE — HIGH/MED:** `types/src/mem.cpp` `xlAutoFree12` lacked `__declspec(dllexport)`. When `types` is linked as a static library into the XLL, Excel cannot resolve the symbol by name and every `xlbitDLLFree`-marked `XLOPER12` leaks. Fixed by introducing a `TYPES_EXCEL_CALLBACK` macro (`extern "C" __declspec(dllexport) void __stdcall` on `_WIN32`, callback-only `extern "C" void __stdcall` elsewhere) in `types/include/types/mem.h` and applying it to the declaration and definition.
* **DONE — MED:** `internal/assets/files/src/xll_embed.cpp` had `extern HMODULE g_hModule;` while `xll_lifecycle.h` / `xll_lifecycle.cpp` define `HINSTANCE g_hModule`. Both alias `void*` on Windows so it linked, but it was ODR-divergent. Replaced the local `extern` with `#include "xll_lifecycle.h"` so there is one source of truth for the declaration.
* **DONE — MED:** `internal/assets/files/src/xll_lifecycle.cpp` `DllMain` forced-unload branch reordered so `SetEvent(g_procInfo.hShutdownEvent)` runs **before** `ForceTerminateWorker()` and `g_monitorThread.detach()`. This gives the threads a brief chance to observe shutdown before being orphaned, while still honoring §20.2 ("leak, don't crash") — no new work is added in `DLL_PROCESS_DETACH`, only existing steps reordered.
* **DONE — HIGH (memory-safety-auditor A4, 2026-05-16; integration completed 2026-05-17):** `internal/assets/files/src/xll_rtd.cpp` `RtdServer::ConnectData` spawns a detached `std::thread` whose lambda accesses `g_host`. On forced unload (per §20) or graceful close (`OnAutoClose` deletes `g_phost`), the lambda could touch freed memory. Patched in-file: the lambda now checks `xll::g_isUnloading` at every yield point (top, before `g_host.GetZeroCopySlot()`, before `slot.Send`); a file-static `g_rtdConnectInFlight` counter is incremented/decremented via an RAII guard; `WaitForRtdConnectDrain(timeoutMs)` is declared in `xll_rtd.h` and defined in `xll_rtd.cpp`. The integration is now wired in: `xll_lifecycle.cpp::OnAutoClose` (under `#ifdef XLL_RTD_ENABLED`) calls `WaitForRtdConnectDrain(2000)` immediately **before** `delete g_phost`. A 2-second timeout is logged as a warning but does not block teardown — the residual race (a Connect thread blocked >2s on SHM) is narrower than the unpatched window. Validated end-to-end by `internal/smoketest` (sync + async + RTD round-trip without segfault).

Open items from the same audit (remaining MED + all LOW) live in the lower §23.x subsections (where applicable) and in `types/AGENTS.md`'s backlog; the C++ reviewer agent should re-confirm on the next pass.

### 23.1 Code Quality
* **DONE (2026-05-17):** `internal/assets/assets.go` — replaced `init()` + `panic(err)` with a `sync.Once`-protected `Assets() (map[string]string, error)` lazy loader; `internal/generator/generator.go` now consumes it via the returned error path. An embed failure no longer takes down every importer.
* **DONE (2026-05-17):** `pkg/server/types.go` — doc comments added to `AnyValue`, `ScalarValue`, `OutgoingChunk`, `QueuedCommand`, `PendingAsyncResult`; `ChunkBuffer` already had one. Also folded `PendingAsyncResult.Val: interface{}` → `any`.
* **DONE (2026-05-17):** `pkg/log/logger.go` — `os.MkdirAll` and `os.OpenFile` now wrap with `fmt.Errorf("log: ... %q: %w", path, err)` so log-init failures point at the path.
* **NOT NEEDED (2026-05-17):** `internal/flatc/flatc.go::EnsureFlatc` already carries a doc comment (lines 22-28). Item removed from backlog after re-inspection.

### 23.2 Tunability
* **DONE (2026-05-17):** `pkg/server/manager.go` — promoted the 30s cleanup tick and 60s TTL to `ChunkManager.CleanupInterval` and `ChunkManager.ChunkBufferTTL` fields backed by `DefaultCleanupInterval` / `DefaultChunkBufferTTL` constants. YAML wiring: `xll.yaml` `server.chunk: {max_buffer_bytes, cleanup_interval, buffer_ttl}` → `config.ChunkConfig` → generated `server.go` calls `server.NewChunkManagerFromConfig` with the values captured before the cleanup goroutine starts. Omitting `server.chunk` keeps the existing defaults — no behavior change for projects that don't opt in.

### 23.3 Test Coverage
* RTD (`pkg/rtd/`) and async batching (`pkg/server/async_batcher.go`) still lack unit tests. Add table-driven tests covering: timeout, partial chunk arrival, duplicate chunk, oversized payload.
* **DONE (2026-05-17):** `internal/generator/gen_cpp_test.go::TestGenCpp_StringErrorReturn` was hardcoded to `MsgId 133` for the first user function. Fixed by deriving the expected IDs from `server.MsgUserStart + i` so future bumps to that constant don't desync the test silently.
* **DONE (2026-05-17):** the `cmd/` integration tier was broken on Windows — `setupMockFlatc` wrote batch script content to `flatc.exe` and Windows refused to load it as PE. Replaced with a real Go-built stub at `cmd/testdata/fakeflatc/main.go`; `setupMockFlatc` now compiles it once via `go build`, caches in user cache dir, and hands the absolute path to `generator.Options{FlatcPath: ...}`. The stub recognizes `--version`, `--go`, `--cpp`, `--go-namespace`, `-o` and writes minimal `<base>_generated.{go,h}` placeholders so the generator's post-processing (`fixCppImports`) finds something to rewrite. Also fixed a second rot — `TestRepro_MultipleAsync` was asserting on a refactored-away `queueAsyncResult` helper; updated to count `asyncBatcher.QueueResult(` call sites instead. All 5 previously-failing tests pass; `go test ./cmd/...` is green on Windows.
* Chunk reassembly (`pkg/server/manager.go`) is now covered by `pkg/server/manager_test.go` (`TestChunkManager`, `TestChunkManager_ConcurrentArrivals`), exercising all four edge cases under `-race`. **Resolved findings (2026-05-16, stabilization pass — Stabilizer):**
  * **Resolved — Duplicate chunk premature completion (HIGH, data corruption).** `ChunkBuffer.Received` was a naive byte counter, so a duplicate of the first chunk in a multi-chunk message pushed `Received` past `TotalSize` and triggered premature completion with the trailing bytes still zero. Fix: added `ChunkBuffer.ReceivedOffsets map[uint32]bool`; `HandleChunk` now skips the byte copy and `Received` bump when the offset has already been seen. The defensive `offset+dataLen <= len(buf.Data)` bounds check is preserved. Regression: `TestChunkManager/DuplicateChunk_IdempotentReceive` (calls `HandleChunk` end-to-end and asserts (a) duplicate does not complete, (b) reassembled buffer is byte-identical to the non-duplicate sequence).
  * **Resolved — `SendAckOrChunk` publication-order race (HIGH).** `AddOutgoingChunk` published the `OutgoingChunk` pointer to a concurrently-reachable map BEFORE `out.Offset = currentSize` was written, so a `HandleAck → GetNextChunk` racing this path could observe `Offset==0` and resend the first slice. Fix: write `out.Offset = currentSize` BEFORE the `cm.AddOutgoingChunk` call; load-bearing comment added at the call site. Regression: `TestChunkManager/SendAckOrChunk_OffsetPublishedBeforeMapInsert` (steady-state + 200-iter stress under `-race` — `-race` flags the data race on the previous code).
  * **Resolved — `GetChunkBuffer` unbounded allocation (HIGH, DoS).** The wire-supplied `total` was trusted as the allocation size. Fix: added `ChunkManager.MaxChunkBufferBytes` (default `256 << 20`, settable via `NewChunkManagerWithMax`); `GetChunkBuffer` now returns `(*ChunkBuffer, error)` and refuses requests > the cap without inserting into `chunkCache`. `HandleChunk` propagates refusal to the wire as `MsgSystemError` (value 127, mirroring `shm.MsgTypeSystemError` in shm@HEAD; defined locally in `pkg/server/types.go` because the pinned shm module v0.5.4 does not yet export that constant). Regressions: `TestChunkManager/OversizedTotal_AllocationRejected` (1 TiB request via direct API and via wire path), `TestChunkManager/OversizedTotal_CustomLimitHonored`.
  * **Resolved — concurrent duplicate FINAL chunk double-dispatch (HIGH, side-effect re-execution; 2026-06-10).** `HandleChunk` released `buf.Mutex` after computing `isComplete := buf.Received >= buf.TotalSize`, then dispatched outside the lock. A retransmitted FINAL chunk racing the original (e.g. after a dropped ACK) let BOTH goroutines observe completion under the lock and BOTH call `dispatch()` — the user function ran twice (side effects!) and two responses were written. Dedup-by-offset did not help: the dup's bytes are skipped, but the completion observation still fires on every arrival. Fix: added `ChunkBuffer.Dispatched bool`; the completion claim (`Received >= TotalSize && !Dispatched → Dispatched = true`) now happens INSIDE `buf.Mutex`, so exactly one goroutine wins and dispatches. Note on a late dup after `RemoveChunkBuffer`: `GetChunkBuffer` re-allocates a fresh buffer whose single final chunk cannot reach `TotalSize` for a multi-chunk transfer, so it never re-dispatches and is reaped at TTL (benign). Regression: `TestChunkManager_ConcurrentDuplicateFinalChunk` (50 outer × 64 concurrent FINAL replays; asserts exactly one dispatch + byte-identical reassembly; fails with `got 2` on parent commit under `-race`).
  * **Resolved — `ChunkManager` cleanup goroutine could never be stopped (MED, goroutine leak; 2026-06-10).** `cleanupLoop` was `for range ticker.C {}` with no exit path and no `Close()`, so the goroutine + ticker leaked for the manager's lifetime (one per `NewChunkManager*`). Fix: added a `stop chan struct{}` + idempotent `Close()` (guarded by `sync.Once`); the loop now `select`s on `ticker.C` and `cm.stop`, with `defer ticker.Stop()`. No existing shutdown path called the manager, so `Close()` is provided for the server teardown to wire in. Regression: `TestChunkManager_CloseStopsCleanupGoroutine` (spawns 25 managers on a 1ms tick, asserts goroutine count rises then drains back to baseline after `Close()`; also calls `Close()` twice to prove idempotency; fails with `leaked ~25` on parent commit).
  * **Resolved — `generateTransferID` constant-0 fallback (MED, correlation-key collision; 2026-06-10).** On the (essentially impossible) `crypto/rand.Read` error path, `generateTransferID` returned a constant `0`, collapsing every concurrent chunked transfer onto the same correlation key → guaranteed cross-talk/corruption. Fix: fall back to `math/rand/v2`'s lock-free auto-seeded `Uint64()` and log the degraded path. No dedicated test (the failure path is not reachable without injecting a crypto/rand fault); covered by inspection.
* `internal/regtest`: regression tests bind to Excel via COM (`internal/regtest/runner.go`). Add a short doc note explaining how to run them on a fresh Windows machine (which Excel SKUs work, what registry entries are needed).
* Follow-ups uncovered during the stabilization pass:
  * `MaxChunkBufferBytes` is currently only mutable through code (`ChunkManager` field or `NewChunkManagerWithMax`). Plumb it through `xll.yaml` → `internal/config` so deployments can tune it without rebuilds. Co-change cluster: pairs with the §23.2 cleanup-tick/TTL promotion.
  * **DONE (2026-05-17, xll-gen v0.3.8 / shm v0.6.0):** local `MsgSystemError` sentinel in `pkg/server/types.go` removed; `pkg/server/handlers.go` and `pkg/server/manager_test.go` now use `shm.MsgTypeSystemError` directly. shm exported the constant in v0.6.0 alongside the streaming API.
  * Wire `Chunk` schema (in `types/`) does not carry an explicit `total_chunks` field; dedup is keyed on offset (unique per chunk on first transmission) which is sufficient given chunk size is sender-controlled and offsets do not overlap. If a future change introduces variable-sized chunks within a transfer, revisit and key on `(offset, length)` or add an explicit chunk-index field.

### 23.4 Dependencies
* **DONE (2026-05-17):** `go.mod` — `golang.org/x/sys` bumped from `v0.1.0` (Jan 2022) to `v0.33.0`. We held back from `v0.34+` because those releases force the Go directive up to 1.25; `v0.33.0` is the newest line still compatible with our `go 1.24.3` floor. Revisit when the project itself is ready to require Go 1.25.
* Verify no other transitive deps are >2 years old at each release; if so, document why.

### 23.5 Windows-Specific Code Layout
* **DONE (2026-05-17):** Created `internal/platform/` with `_windows.go` / `_other.go` build-tagged constants. Migrated 6 `.exe`-extension branches (`internal/flatc/flatc.go`, `internal/regtest/prepare.go`, `internal/regtest/runner.go`, `cmd/regression_test.go`, `cmd/regression_helpers_test.go`) to `platform.ExeName`. Added `platform.FindBuiltExe` for the single-config vs multi-config cmake output layout (used by 2 sites). The remaining `runtime.GOOS == "windows"` checks in `cmd/doctor.go` are install-hint specific (winget) — not the same kind of duplication, intentionally left as-is. Smoketest files use file-level `//go:build windows`, already idiomatic.

## 24. CLAUDE.md / Agent Tool Compatibility

This repository is configured so that AI tools using `CLAUDE.md` (Claude Code) read this `AGENTS.md` as the authoritative source. **All durable agent guidance must live here, not in `CLAUDE.md`.** `CLAUDE.md`, if present, must contain only a one-line redirect to this file.
