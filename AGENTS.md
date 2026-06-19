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

### 18.1 Protocol & Types — single-sourced from `types`
The `protocol.fbs` schema has **one source of truth: `types/go/protocol/protocol.fbs`** (the `github.com/xll-gen/types` repo). xll-gen does NOT hand-maintain its own schema.
1.  **Schema Source (SSOT)**: `types/go/protocol/protocol.fbs`.
2.  **Embedded copy (derived, do not hand-edit)**: `internal/templates/protocol.fbs` is a **verbatim, auto-synced copy** of the pinned `types` version. It is written into each generated project purely as a flatc parse-stub for `schema.fbs`'s `include "protocol.fbs"` — the generated project's actual protocol code (Go via import rewrite, C++ via `types` FetchContent + include rewrite; both `flatc --no-includes`) comes from the pinned `types` module, **never** from this copy.
3.  **Sync tool**: `go generate ./internal/templates/` runs `internal/templates/syncprotocol`, copying the pinned `types` version's `protocol.fbs` over the embedded copy (byte-exact).
4.  **Drift gate**: `cmd/protocol_fbs_sync_test.go` (`TestProtocolFbsMatchesPinnedTypes`) fails CI if the embedded copy is not byte-identical to the pinned `types` schema.

**Workflow to change the protocol schema:** edit `types/go/protocol/protocol.fbs` → regenerate types' own Go/C++ artifacts + release a new `types` tag → bump the pin in `xll-gen` (`go.mod` + `internal/versions/versions.go`) → run `go generate ./internal/templates/` to re-sync the embedded copy → the drift gate confirms parity. NEVER hand-edit `internal/templates/protocol.fbs`; it will be overwritten by the sync and rejected by the gate if it diverges from the pin.

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
4.  **Schema**: If the event requires a specific payload structure, change it in `types/go/protocol/protocol.fbs` (the SSOT) and re-sync the embedded copy per §18.1 — do NOT hand-edit `internal/templates/protocol.fbs`.

**⚠️ Event handlers must never perform synchronous Excel COM.** `CalculationEnded` (and any event) is delivered via a SYNCHRONOUS calc-end round-trip: the XLL fires `MSG_CALCULATION_ENDED` and BLOCKS Excel's STA thread inside `g_host.Send(..., 2000)` until the Go handler returns (it must block to fold scheduled commands into the same response — see `pkg/server/handlers.go::HandleCalculationEnded` and `internal/assets/files/src/xll_events.cpp`). If a handler drives Excel over COM (e.g. `sugar` `attachExcel` + `UsedRange().Find` / `Range().SetValue`), those calls need the STA thread that is blocked → hard deadlock until the 2000ms timeout, on EVERY recalc (`g_host.Send` does a non-pumping wait). Symptom: Excel freezes ~2s per recalc, typing becomes impossible. Event handlers must mutate Excel ONLY via `generated.ScheduleSet` / `generated.ScheduleFormat` (deferred commands, applied by the XLL on the STA thread AFTER the handler returns). COM is fine in COMMAND handlers (they run when the STA is free), NOT in event handlers. This footgun is also why a `CalculationEnded` handler cannot dynamically locate a target cell via COM (`Find`) — pass it a known address instead. (Observed 2026-06-15 in xll-gen-showcase `OnRecalc`; fixed there + documented in `README.md` Events.)

### 18.4 Type System Extensions
When adding or modifying a data type (e.g., adding `date` support):
1.  **Configuration**: Update `internal/config/config.go` (`validArgTypes`, `validReturnTypes`).
2.  **Metadata**: Update `internal/generator/types.go` (`typeRegistry`).
3.  **Schema**: Add the table/union member in `types/go/protocol/protocol.fbs` (the SSOT), release/bump the pin, then re-sync the embedded copy per §18.1 — do NOT hand-edit `internal/templates/protocol.fbs`.
4.  **Upstream**: Update `github.com/xll-gen/types` to handle the new type (converters + Go helpers).

> Note: a type maps to a `protocol.fbs` member **only if its `SchemaType` is that member**. `date` uses `SchemaType="double"` (see Confirmed-Correct Decisions), so it rides the existing `Num`/`double` path and does not require the `Date` union member in the generated project — adding `date`-like types backed by `double` needs no schema change at all.

**Argument-direction (cell → handler) request-builder codegen.** In `internal/templates/xll_main.cpp.tmpl` the per-arg encode is split in two loops: the **arg-decode loop** creates an `arg<N>` offset ONLY for composite/by-reference types (`string`/`grid`/`numgrid`/`range`/`any`), and the **request-builder loop** has a SCALAR branch (`add_<name>(<name>)`, passing the by-value C param directly) vs. an ELSE branch (`add_<name>(arg<N>)`, using the decoded offset). A **by-value scalar ARGUMENT** is any type whose `ArgCppType` is a plain scalar (`int`/`double`/`bool` — i.e. `int`, `float`, `bool`, and `date` (`ArgCppType="double"`: Excel passes the serial by value)). Every such type MUST be listed in the builder-loop scalar branch's `or` condition. Omitting one makes it fall to the else branch, which emits `add_<name>(arg<N>)` referencing an `arg<N>` that the decode loop never created → the generated C++ fails to compile. This bit `date` (fixed v0.8.10; regression `gen_date_test.go::TestGenCpp_DateArgRequestBuilder`). **When adding a new by-value scalar arg type, update that scalar branch (not just `typeRegistry`).**

**Return-direction (handler → cell) serialization.** A type valid as a RETURN
may need a distinct handler-facing Go type and a server-side serializer, because
FlatBuffers read views (`*protocol.Grid`, `*protocol.Any`) make sense as
arguments but cannot be constructed by a handler:
1.  Set `TypeInfo.RetGoType` in `typeRegistry` (e.g. `grid` → `[][]any`,
    `numgrid` → `[][]float64`, `any` → `any`); `interface.go.tmpl` uses
    `lookupRetGoType` for the return position and `lookupGoType` for args.
2.  Add a Go-value→FlatBuffers builder. Scalars/`any` route through
    `internal/fbany`; `grid`/`numgrid` are built by `fbany.BuildGrid` /
    `fbany.BuildNumGrid` and wrapped by `pkg/server.BuildGridFromGo` /
    `BuildNumGridFromGo` (sync). The async path serializes the same value at
    flush time via `fbany.Build` under the `AnyValueGrid`/`AnyValueNumGrid` tag,
    validated eagerly at queue time (`server.ValidateGrid`/`ValidateNumGrid`).
3.  Add the sync result branch in `server.go.tmpl` (offset-based `AddResult` +
    error routing) and the async branch (validate → `QueueResult` with the tag).
4.  Confirm the C++ return conversion: sync uses `GridToXLOPER12`/`NumGridToFP12`
    (already in `xll_main.cpp.tmpl`); async uses `AnyToXLOPER12` (handles
    Grid/NumGrid → xltypeMulti). Both live in `github.com/xll-gen/types`.
5.  Registration return code (`TypeInfo.XllType`): `Q` (LPXLOPER12 → xltypeMulti)
    or `K%` (FP12) — both spill in dynamic-array Excel. `U` is never valid in
    return position (§19.2). No version detection or registration flag is needed
    for spilling (`Function.Resizable` stays unconsumed).

### 18.5 Regression Test Assets
The integration tests in `internal/regtest` rely on a fixed set of files that must stay in sync.
1.  **Test Project**: `internal/regtest/testdata/xll.yaml` defines the function signatures and order.
2.  **Mock Host**: `internal/regtest/testdata/mock_host.cpp` hardcodes message IDs (e.g., `133`) and payload structures based on `xll.yaml`.
3.  **Go Server**: `internal/regtest/testdata/server.go` implements handlers matching `xll.yaml`.
**Constraint**: Any change to `testdata/xll.yaml` (e.g., adding a function) requires updating `mock_host.cpp` (new ID/case) and `server.go`.

### 18.6 Message ID Allocation
Message IDs are distributed across multiple definitions and must match exactly.
1.  **Definitions**: the Go-side single source of truth is the leaf package `pkg/msgid` (e.g., `MsgUserStart = 140`, `MsgCalculationEnded = 131`, `MsgRtdConnect = 133`); `pkg/server/types.go` re-exports them as aliases (`MsgRtdUpdate = msgid.MsgRtdUpdate`, etc.) so all `server.Msg*` references — including generated code — keep compiling, and `pkg/rtd` imports `pkg/msgid` directly (no shadow copy). The C++ mirror is `internal/assets/files/include/xll_ipc.h` (the `MSG_*` #defines, e.g. `MSG_USER_START = 140`). `pkg/msgid/msgid_test.go` pins the numeric values. The Go side (`pkg/msgid`) and the C++ side (`xll_ipc.h`) must match exactly.
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

**Teardown contract (REVISED 2026-06-13 — cancel-quit fix; see §20.3):**
`xlAutoClose` is now **non-destructive** (it must be, because Excel calls it
before the Save/Cancel prompt — a cancelled quit would otherwise zombie the
add-in). The destructive teardown is consolidated into the single-shot
`xll::GracefulTeardownOnce()` (`xll_lifecycle.cpp`), driven only by
**confirmed-shutdown** COM events (`RibbonAddIn::OnBeginShutdown` and
`OnDisconnection` on both `ext_dm_HostShutdown` and `ext_dm_UserClosed`), with
`DLL_PROCESS_DETACH` + the Job's `KILL_ON_JOB_CLOSE` as the universal backstop.
`GracefulTeardownOnce()` ordering (runs exactly once, CAS-guarded, on the STA
thread — safe, not the loader lock):
1.  Set `g_isUnloading=true`.
2.  Invoke the COM teardown hook (`GracefulComTeardownHook`, registered from
    `xlAutoOpen` via `SetGracefulTeardownHook`): `SetRibbonConnected(false)` →
    `CoRevokeClassObject` → unregister HKCU COM-addin keys → `ShutdownRibbonImageEngine`.
3.  `SetEvent(hShutdownEvent)`, `StopWorker`/`JoinWorker`/monitor join.
4.  Drain RTD Connects (`WaitForRtdConnectDrain(2000)`) and commands
    (`xll::ribbon::WaitForCommandDrain(2000)`). Both run AFTER `g_isUnloading=true`,
    so each detached thread re-checks the flag between its ≤200 ms per-attempt
    Sends and exits within ~one attempt — this is what closes the command/RTD-path
    UAF window before `delete g_phost`.
5.  `delete g_phost`, then close the process/job/event handles.
`WaitForCommandDrain` is declared OUTSIDE `XLL_RIBBON_ENABLED` and `ribbon_addin.cpp`
is always swept up by the CMake source glob, so the lifecycle call links in every
project. The old eager drain in the generated `xlAutoClose` (which ran BEFORE
`g_isUnloading` was set) is GONE with this fix.

Detached `SendCommandInvoke` threads follow the SAME `g_isUnloading` self-abort contract as RTD `ConnectData` (§20.2 / §23.0): on forced unload each thread re-checks `g_isUnloading` at every yield point and aborts before touching `g_host`.

**Delivery contract (at-least-once):** the first-click retry makes command delivery **at-least-once, not exactly-once**. A timed-out SHM `Send` does NOT prove the guest never consumed the request (the slot stays `SLOT_REQ_READY`; a guest attaching late can still read it), and a delivered-but-`SYSTEM_ERROR` response also retries (`res.HasError()` does not distinguish the two cases). The same applies to RTD `ConnectData`'s retry (§23.0): `RtdManager.Subscribe` is idempotent, but the **user's** command handler / `OnRtdConnect` / rtd-once handler may RUN TWICE under a slow cold start. Write command and connect handlers to be idempotent or side-effect-tolerant.

**deferred-connect contract (LOAD-BEARING — fixed 2026-06-12, timer added 2026-06-12):** the COMAddIns connect (`Application.COMAddIns.Item(progId).Connect = true`) needs the in-process `Application` object, reachable only through the `XLMAIN → XLDESK → EXCEL7` child-window chain. When the add-in loads with **no workbook open** (auto-loaded at Excel startup), the `EXCEL7` window does not exist, `GetExcelApplication()` returns `nullptr`, and a one-shot connect at `xlAutoOpen` fails permanently — the ribbon tab never appears even after the user opens a workbook. The connect therefore runs through `TryConnectRibbon(phase)` (idempotent, single-atomic state guard `g_ribbonConnectState`: 0=pending / 1=connected / 2=gave-up). It is driven by **TWO STA-thread retry triggers**:

1. **PRIMARY — a Win32 thread timer.** `ArmRibbonConnectTimer()` (called from `xlAutoOpen` when the first connect defers) arms `SetTimer(NULL, kRibbonConnectTimerId, 750ms, RibbonConnectTimerProc)`. `hwnd=NULL` binds the `WM_TIMER` to the arming thread's message pump — Excel's main STA thread, which pumps even when **fully idle with no workbook**. This is what fixes the v0.5.0 regression: a brand-new **EMPTY** workbook (`Workbooks.Add` / File>New) runs **no calculation**, so the calc-end hook never fires — only the timer does. The `TimerProc` retries `TryConnectRibbon("timer")` and self-`KillTimer`s once the connect resolves or `g_isUnloading` is set.
2. **SECONDARY — the calc-end callback** (`CalculationEnded` / a user `CalculationEnded` handler), kept as belt-and-braces for the workbook-already-open and active-recalc cases. Consequence: ribbon-enabled builds still register `CalculationEnded` unconditionally.

**Give-up budget semantics:** `SetRibbonConnected(connected, &noApp)` sets `noApp=true` when the *only* reason the connect failed is that no `Application` object is reachable yet (no workbook window). `TryConnectRibbon` returns early on `noApp` **without** consuming the 60-attempt give-up budget — otherwise a 750 ms timer on an idle no-workbook Excel would exhaust the budget (state→2, gave-up) in ~45 s, BEFORE a user who opens a workbook minutes later. The budget now only counts *real* connect rejections (Application reachable but `Connect` failed). The timer is bounded in practice by **teardown**, not the budget.

**Teardown ordering:** `StopRibbonConnectTimer()` (KillTimer) runs in `xlAutoClose` FIRST — before `SetRibbonConnected(false)` and `CoRevokeClassObject` — so no `WM_TIMER` can re-enter `TryConnectRibbon` (CoRegisterClassObject / Connect) mid-teardown. `KillTimer` runs on the same STA thread as the `TimerProc`, so no callback can be in flight after it returns; the `TimerProc` also self-guards on `g_isUnloading`. NEVER collapse this back to a single inline `SetRibbonConnected(true)` in `xlAutoOpen`, and NEVER remove the timer trigger (the calc-end hook alone does not cover the empty-workbook flow).

**first-click delivery contract (LOAD-BEARING — fixed 2026-06-12):** `SendCommandInvoke` (ribbon onAction AND `Cmd_*` shortcut/Alt+F8 procs) is fire-and-forget on a detached thread, but a click can land in the window between the server process launch (`xlAutoOpen`) and the Go guest attaching its receive workers to the host slots. In that window a host-initiated `slot.Send` has no reader and times out; with the result discarded the command is silently dropped (observed as "the button does nothing on the first click, then works after another click"). The detached thread therefore **inspects `res.HasError()` and retries with a bounded budget + short per-attempt timeout** (re-acquiring a fresh `ZeroCopySlot` each attempt — `Send` disowns its slot on timeout). This is the same first-request retry the regtest mock host uses deliberately (`internal/regtest/testdata/mock_host.cpp`). The retry runs OFF the STA thread, so it never blocks Excel; the per-attempt timeout is kept short so the `WaitForCommandDrain` teardown path is not stalled. NEVER revert to a single discard-the-result `slot.Send(..., 5000)`.

**set-before-connect contract:** `SetCommands` / `SetRibbonXml` run on the STA thread inside `xlAutoOpen` **BEFORE** COM-addin registration and connect. The backing globals are intentionally **unsynchronized** — correctness depends on this strict ordering. NEVER move registration off-thread and NEVER introduce a message pump between the `Set*` calls and connect, or the globals become observably racy.

**Graceful degradation (see design §1.4):** if HKCU registration/connect fails (group-policy-locked desktops), worksheet functions / RTD / async must keep working unchanged, registered `commands` stay invocable via shortcut and by typing the name into Alt+F8 (`xlfRegister`/`macroType=2` does not depend on the COM/ribbon path), and failure is silent except for a logged warning.

**Decision (2026-06-12, user-confirmed — do not re-propose):** raw-XML ribbon mode does **not** support image files. The `loadImage` rejection on the raw-XML path is by design, not a bug; projects that need file-based icons must use the structured mode (`tab`/`groups`).

**Constraint**: Adding or renaming a `commands`/`ribbon` field, changing the ribbon XML shape, or touching `CommandInvokeRequest/Response` requires walking all five locations above plus the message-ID mirror, and verifying the templates still compile.

## 19. Excel XLL Registration Rules

When generating the `xlfRegister` type string in `xll_main.cpp.tmpl`, follow these strict rules to avoid Excel registration failures or immediate unloads.

### 19.1 Type String Format
1.  **Thread Safety**: Append `$` to the end of the type string to mark the function as thread-safe — **except** for macro-sheet-equivalent functions. A function registered as a macro-sheet equivalent carries `#`, and Excel rejects `#` combined with `$`: `xlfRegister` returns `xlretSuccess` but the register ID is `xltypeErr` and the worksheet name resolves to `#NAME?`. So: macro-sheet → `...#` (no `$`), everything else → `...$`.
    *   The `#` is keyed off **`macro: true`** (config `Function.Macro`), NOT off `caller: true`. As of v0.5.0 caller-awareness and macro-sheet registration are split: `xlfCaller` (which reports the caller's position) is callable from ANY XLL function — it is an SDK-documented exception, as are `xlSheetNm`/`xlSheetId` — so `caller: true` alone stays thread-safe (`$`, no `#`) and is **position-only**. The macro-only `xlfGetCell` (used by the wrapper to fetch the caller's number-format string into `Range.Format()`) requires the `#` registration, which is what `macro: true` grants. Therefore: the caller number format is populated only when a function sets **both** `caller: true` and `macro: true`; `caller: true` by itself leaves `Range.Format()` empty. `macro: true` is rejected on `mode: "rtd-once"` (same as `caller: true` — the handler runs off the calc thread on a topic connect).
2.  **Synchronous Functions** (`mode: "sync"`):
    *   Format: `[ReturnTypeChar][ArgTypeChars]$`
    *   Example: `QJJ$` (Returns `LPXLOPER12`, takes two `long` integers).
3.  **Asynchronous Functions** (`mode: "async"`):
    *   **Note**: The `async: true` configuration field is deprecated. Use `mode: "async"` in `xll.yaml` instead.
    *   Format: `>[ArgTypeChars]X$`
    *   **CRITICAL**: Omit the return type character (e.g., `Q`). The `X` character (Async Handle) acts as the return parameter placeholder in the type string.
    *   Example: `>QX$` (Takes a string `Q`, uses async handle `X`).
4.  **RTD Functions** (`mode: "rtd"`):
    *   Format: `Q[ArgTypeChars]$` — always returns `LPXLOPER12` (the wrapper
        routes through `xlfRtd`), and the declared args are registered like any
        sync function (e.g. showcase `StockTick(symbol string)` → `QQ$`).

### 19.2 Argument Mapping
*   **Return Types**: Use `lookupXllType`. The return code is **always `Q`** for `LPXLOPER12` returns (and `K%` for `numgrid`). `U` is never valid in return position — wrappers return value XLOPER12s, not range references, and a `U` return breaks the registration (worksheet name → `#NAME?`).
*   **Command (`macroType=2`) return TypeText**: `xlfRegister` ignores a command's return value at dispatch, but the TypeText's leading return code should still match the exported C signature so the registration string is self-documenting and ABI-honest. Codes: `I` = 2-byte signed `short`, `J` = 4-byte signed `int`/`long`, `A` = Boolean. The calc-end deferred runner (`__xllgen_RunDeferredCalcEnd`, returns `short`) registers `"I"`; the user-command exports (`Cmd_<Name>`, return `int`) register `"J"`. Do not register a Boolean `"A"` for an integer-returning command (cosmetic-only today, but it misdescribes the signature).
*   **Argument Types**: Use `lookupArgXllType`.
    *   `int` -> `J` (long)
    *   `float` -> `B` (double)
    *   `bool` -> `A` (bool)
    *   `string` -> `Q` (LPXLOPER12, value)
    *   `any`/`range`/`grid` -> `U` (LPXLOPER12, reference allowed; argument position only)
*   **Mismatches**: Ensure the C++ function signature matches these types (e.g., `int32_t` for `J`, `double` for `B`). A mismatch will cause stack corruption or Excel crashes.

### 19.3 Execution-Mode Guidance (sync / async / rtd / rtd-once)

`mode: "async"` does **not** keep the sheet responsive: Excel holds the
calculation transaction open until all pending `xlAsyncReturn` results arrive,
so no new recalculation (volatile ticks, RTD-triggered recalcs) runs in the
meantime — a single long async call feels identical to sync. Async buys
**concurrency** (N calls in one calculation overlap) and the guarantee that
dependents only see the final value. For multi-second work where interactive
feel matters, the RTD pattern is the right tool (cell returns a placeholder
immediately; result arrives via RTD push) — the same approach Excel-DNA uses
for its async support. Full decision matrix + RTD caveats (2s default
throttle — explicitly configurable via `rtd.throttle_interval`, which is
registry-persisted per user; placeholder propagation to dependents; no F9
re-run while the topic is connected; topic-string argument limits): README
"Choosing an Execution Mode".

Plain `mode: "rtd"` accepts scalar (`int`/`float`/`string`/`bool`) AND
composite (`range`/`grid`/`numgrid`/`any`) ARGUMENTS; the return must be scalar
or `any`. Scalar args are stringified into the RTD topic; composite args travel
the **content-hash payload path** (below). Composite RETURNS stay rejected at
config time (`internal/config/config.go`) — the RTD push path (`pkg/rtd` →
`fbany.MapGo` / `RtdUpdate`'s `Any` union) carries scalars and `any` only and
would otherwise `fmt.Sprintf`-stringify a composite.

##### Content-hash payload path (composite RTD arguments)

The RTD topic string is value-identity for a topic, but a composite argument
(grid/range/numgrid/any) cannot be stringified into it without (a) losing its
contents and (b) colliding distinct values onto one topic (the old
`"[Complex]"` bug). The fix: the topic carries only a **content hash** of the
argument; the payload travels once per calc cycle over the normal SHM
`SetRefCache` path, cached hash→payload on the Go side. Topic identity then
tracks CONTENT — same grid → same topic, edited grid → new hash → new topic →
fresh compute — which is exactly correct RTD semantics. The mechanism reuses
the per-cycle ref-cache infrastructure end to end:

1. **C++ wrapper** (`xll_main.cpp.tmpl`, rtd + rtd-once). For each composite
   arg it computes `xll::ContentHashToken(typeTag, px)` (FNV-1a over
   `SerializeXLOPER`, which coerces refs to their cell VALUES) — or
   `ContentHashTokenFP12(fp)` for `numgrid` (geometry + raw double bytes). Both
   yield an `"h:<typeTag><hex>"` token (`internal/assets/files/src/xll_cache.cpp`).
   The `typeTag` (`g`/`r`/`n`/`a`) namespaces the hash by WIRE-PAYLOAD shape:
   the same `A1:B2` serialized as a grid (values) vs a range (coordinates) is a
   different payload, so the tag keeps each (content, target-type) pair on its
   own topic and RefCache entry — without it a grid arg's payload could satisfy
   a range arg's lookup with the wrong union type. For `grid` args the wrapper
   uses `xll::ConvertGridArg` (coerces the `U`-passed reference to cell values
   before `ConvertGrid`, which only understands `xltypeMulti`). It then builds a
   `protocol::SetRefCacheRequest{ key=token, val=Any(payload) }` via the
   existing `ConvertGrid`/`ConvertNumGrid`/`ConvertRange`/`ConvertAny`
   converters and ships it through `xll::SendRefCachePayloadOnce`
   (`xll_ipc.cpp`) — which dedups on the token via the per-cycle
   `g_sentRefCache` set (cleared on `CalculationEnded` alongside the RefCache,
   `xll_events.cpp`) and sends `MSG_SETREFCACHE` **before** `xlfRtd` so the
   server has the payload cached before `ConnectData` fires the handler. The
   topic string for that arg is the token.
2. **Token scheme.** The `"h:"` prefix is collision-proof against any token a
   scalar string arg could legitimately produce, but the Go dispatch does NOT
   sniff it — it decodes composite positions by the (generator-known) argument
   type. The prefix is for debuggability and so the once-key (below) is
   visibly content-addressed.
3. **Go dispatch** (`server.go.tmpl`, rtd/rtd-once connect). For composite-arg
   positions it calls `server.ResolveGridArg` / `ResolveNumGridArg` /
   `ResolveRangeArg` / `ResolveAnyArg` (`pkg/server/refarg.go`), which look the
   token up in the per-cycle `RefCache`, deserialize the cached
   `SetRefCacheRequest`'s `Any`, and return the typed read view — the SAME
   `*protocol.Grid`/`*protocol.Range`/… views sync handlers receive. **Copy
   safety vs the calc-end Clear:** `RefCache.Get` returns an INDEPENDENT COPY of
   the bytes, so the returned view aliases that copy, not the cache map — a
   concurrent `Clear()` (calc-end) cannot invalidate a value already resolved.
   The only failure mode is a MISS (payload cleared before this connect ran,
   e.g. server restart mid-cycle), surfaced as an error so the dispatch pushes a
   clear value to the topic (`rtd.GlobalRtd.SendUpdate(topicID, err.Error())`)
   instead of hanging at `#GETTING_DATA`.
4. **rtd-once content-addressed memoization.** The hash token flows naturally
   into `MakeRtdOnceKey` (the once-key is the topic strings joined by `\x1f`),
   so memoization/TTL become content-addressed for free: the same input grid
   hits the cached result; an edited grid yields a new token → new key → fresh
   compute. The liveness-guard/TTL machinery is unchanged — keys are still
   opaque strings.

#### `mode: "rtd-once"` — one-shot RTD wrapper

`rtd-once` lets a user write a **normal sync-shaped handler** (`func(ctx,
args...) (T, error)`) for a long one-shot computation; the generator wraps it
in an RTD topic lifecycle so the cell returns immediately with `#GETTING_DATA`
and later receives the value. Requires `rtd.enabled: true`. **Scope:** scalar
OR composite (`range`/`grid`/`numgrid`/`any`) args + scalar/`any`/`grid`/`numgrid`
return (`range` return stays rejected — it is not a return type). Composite args
travel the content-hash payload path (above) — the hash token flows into the
once-key, so memoization is content-addressed (same input grid → cached result;
edited grid → fresh compute).

**Grid/numgrid return = non-blocking spilling function.** An RTD topic can only
deliver a scalar (Excel limit; Microsoft KB 286258), so a grid result cannot ride
the RTD push. Instead — the same pattern Excel-DNA's `ExcelAsyncUtil.Observe` uses —
the RTD push carries only a **scalar readiness token** while the array travels a
separate channel and is returned through the normal calc path (which spills):
  1. The server runs the one-shot handler, serializes the `[][]any`/`[][]float64`
     into a `protocol.RtdOnceGridResult{key, value:Any(Grid|NumGrid)}`, and ships it
     **guest→host** as `MSG_RTD_ONCE_GRID` (= `msgid.MsgRtdOnceGrid` = 138; chunked
     via the `MsgChunk`/`protocol.Chunk` path when it exceeds one slot). `key` is the
     once-key (`MakeRtdOnceKey` = topic strings joined by `\x1f`; the Go side builds it
     as `strings.Join(args, "\x1f")` — byte-identical, tokens already substituted).
  2. The C++ host stores the payload bytes in `RtdOnceGridRegistry` (a twin of the
     scalar `RtdOnceRegistry` in `xll_rtd_once_grid.h`: same memoize/`memoize_ttl`/
     liveness-guard logic, byte-buffer entries, independent `m_topicToKey`). It then
     pushes a changing readiness token on the topic.
  3. The RTD update recalcs the cell; the generated wrapper re-enters, hits
     `RtdOnceGridRegistry::TryGet(onceKey)`, and returns the grid as `xltypeMulti`
     (`GridToXLOPER12`, registered `Q`/`LPXLOPER12`) or `FP12*` (`NumGridToFP12`,
     registered `K%` — **numgrid keeps the FP12 ABI even under rtd-once**), which
     **spills**. No `xlfRtd` on the hit → the topic disconnects; memoize/TTL govern
     retention exactly as for scalar rtd-once. `ProcessRtdUpdate` skips the scalar
     `StoreResult` for grid-once topics (detected via the grid registry's `KeyForTopic`);
     `CalculationEnded` clears both registries.
This gives non-blocking + memoize + spill — strictly better than `async` for slow
work (async holds the calc transaction open). Scalar/`any` rtd-once is unchanged
(value rides `RtdUpdate`'s `Any` union). caller-aware (`caller: true`) is rejected (the handler
runs on a topic connect, not in the calling cell's calc). A per-function `memoize: bool` flag
and a per-function `memoize_ttl: "<duration>"` flag are valid **only** with
rtd-once and are rejected on other modes; `memoize_ttl` is mutually exclusive
with `memoize: true` (it is the intermediate "cache for the TTL, then
recompute" option) and must parse to a positive Go duration.

**First-paint placeholder (`loading_placeholder`).** On a cache miss the
rtd-once wrapper does NOT return `xlfRtd`'s raw synchronous result — for a
brand-new topic that is `#N/A` (the topic is not yet connected when the
synchronous `xlfRtd` returns; `ConnectData`'s `#GETTING_DATA` only lands on a
later refresh, usually after the result push has already arrived, so the cell
flashes `#N/A` before the value). Instead the wrapper calls `xlfRtd` (to wire
the subscription) and returns a deterministic placeholder. The placeholder is
configurable: project-wide via `rtd.loading_placeholder` and per-function via a
`loading_placeholder` override (valid on the RTD-backed modes — `rtd` and
`rtd-once`; rejected on `sync`/`async`). Recognized values (case-insensitive,
per-function wins, then global, then the default): `""`/`getting_data` →
`#GETTING_DATA`, `na` → `#N/A`, any other string → verbatim text (e.g.
`"Loading…"`).

For **rtd-once** this is the wrapper's authoritative first paint: the wrapper
returns the placeholder directly (`g_xlErrGettingData` / `g_xlErrNA` static
sentinels — no `xlbitDLLFree`, like `g_xlErrValue` — or `NewExcelString` for
custom text, which is DLL-owned and reclaimed by `xlAutoFree12`), ignoring `xRes`
on the success path, so `ConnectData`'s initial value never surfaces. `numgrid`
(FP12) cannot carry an error/string, so it keeps its empty 0×0 placeholder.

For **plain rtd** the same setting governs `ConnectData`'s initial value (the
value shown from connect until the first stream push), replacing the legacy
`"Connecting…"` text. The streaming wrapper CANNOT substitute a placeholder for
its return — it must return `xlfRtd`'s live value verbatim, else the stream would
never display (and substituting `#GETTING_DATA` for `#N/A` would mask a value the
stream genuinely pushed). So the brief pre-connect `#N/A` micro-flash is inherent
and intentionally left. The mechanism is `RtdPlaceholderRegistry`
(`xll_rtd_placeholder.h`): xlAutoOpen populates a function-name → placeholder map
(resolved at generation time), and the shared, non-generated `ConnectData` looks
it up to build the initial VARIANT (`#GETTING_DATA`=scode 2043, `#N/A`=2042, or a
`SysAllocString`'d BSTR Excel then owns). An unregistered topic falls back to
`#GETTING_DATA`.

**Co-change cluster** (all must move together — same discipline as §18.7):
* `internal/config/config.go` — mode accepted in the mode switch; rtd-once
  return/arg/caller/memoize/memoize_ttl validation; `Function.Memoize` and
  `Function.MemoizeTTL` fields.
* `internal/generator/funcmap.go` — `isRtdLike` (rtd OR rtd-once, shares the
  C++ wrapper shape + the server-side handler-glue skip), `anyRtdOnce`
  (gates the C++ rtd-once machinery), `durationMillis` (computes the
  memoize_ttl milliseconds embedded in the `SetFunctionNames` call), and
  `rtdPlaceholderReturn` (emits the rtd-once first-paint `return …;` from the
  resolved `loading_placeholder`, escaping custom text via `cppWideLiteral`),
  `anyRtd` (gates the plain-rtd `RtdPlaceholderRegistry` include + population),
  and `rtdPlaceholderEntry` (emits one plain-rtd `{L"Name",{kind,L"text"}}`
  registry initializer). Resolution itself lives in
  `config.ResolveRtdPlaceholder` (shared by both rtd and rtd-once;
  `loading_placeholder` is validated only for those two modes).
* `internal/templates/server.go.tmpl` — rtd-once connect dispatch calls
  `rtd.RunOnce(ctx, rtd.GlobalRtd, topicID, func(ctx) (interface{}, error) {
  return handler.<Name>(ctx, <parsed scalar args>) })`; the sync/async
  `handle<Name>` and user-message dispatch case are skipped for rtd-once
  (gated by `not (isRtdLike .Mode)`).
* `internal/templates/interface.go.tmpl` — rtd-once falls into the normal
  (non-`_RTD`) signature branch, so the user implements an ordinary handler.
* `pkg/rtd/runonce.go` — `RunOnce` runs the handler once and pushes the
  result (or, on error, the error string) via `SendUpdate`. Unit-testable in
  isolation (`pkg/rtd/runonce_test.go`).
* `internal/templates/xll_main.cpp.tmpl` — rtd-once registers like rtd
  (`Q<args>$`, returns `LPXLOPER12`); a distinct wrapper body (below);
  `RtdOnceRegistry::SetFunctionNames({names}, {memoizeNames}, {ttlPairs})` at
  xlAutoOpen (the third arg is name→ttl-ms pairs for memoize_ttl functions).
* `internal/assets/files/include/xll_rtd_once.h` — `RtdOnceRegistry` (the
  once-results map + topic bookkeeping) and `RtdOnceResultToXLOPER12`.
* `internal/assets/files/src/xll_rtd.cpp` — `ConnectData` registers the
  topicID→key map for rtd-once topics and returns `#GETTING_DATA` for them; for
  plain-rtd topics it returns `RtdPlaceholderRegistry::MakeInitial(funcName)`
  (the configured initial value, default `#GETTING_DATA`) instead of the legacy
  "Connecting…"; `ProcessRtdUpdate` caches the value under the topic's key;
  `DisconnectData` drops the topicID→key map.
* `internal/assets/files/include/xll_rtd_placeholder.h` — `RtdPlaceholderRegistry`
  (plain-rtd function-name → first-paint placeholder map + `MakeInitial` VARIANT
  builder); populated at xlAutoOpen, read by `ConnectData`.
* `internal/assets/files/src/xll_events.cpp` — `HandleCalculationEnded` calls
  `RtdOnceRegistry::ClearNonMemoized()` (gated by `XLL_RTD_ENABLED`).
* `internal/assets/files/{include/xll_lifecycle.h,src/xll_lifecycle.cpp}` —
  `g_xlErrGettingData` / `g_xlErrNA` first-paint sentinels (defined
  unconditionally, initialized in `DllMain` alongside `g_xlErrValue`); the
  rtd-once wrapper returns one of these (or `NewExcelString` for custom text)
  per `loading_placeholder`.

**Once/memoize_ttl/memoize lifecycle mechanism (as implemented):**
1.  The wrapper builds the same topic strings as plain rtd (`t0`=function
    name, `t1..`=stringified scalar args) and a key = those strings joined by
    `\x1f` (`xll::MakeRtdOnceKey`).
2.  On call, the wrapper checks `RtdOnceRegistry::TryGetResult(key)`. **Hit →
    return the cached value directly, WITHOUT calling `xlfRtd`.** The cell then
    holds no RTD reference, so Excel calls `DisconnectData` at end of calc and
    the topic is torn down (Go unsubscribed via the existing path).
3.  **Miss → `xlfRtd`.** Excel calls `ConnectData(topicID, strings)`. Because
    `strings[0]` is in the rtd-once function-name set, ConnectData records
    `topicID → key` and returns `#GETTING_DATA`. The Go server runs the
    handler once (`rtd.RunOnce`) and pushes one `RtdUpdate`.
4.  `ProcessRtdUpdate` looks up `topicID → key`; for rtd-once topics it stores
    the VARIANT under the key, then does the normal `UpdateTopic` +
    `NotifyUpdate` so Excel recalcs the cell → step 2 hits the cache.
5.  **once (default):** `HandleCalculationEnded` calls `ClearNonMemoized()`,
    which drops completed entries — but **only for keys with no live topic**
    (no `topicID → key` mapping left). The liveness guard closes a race: a
    CalculationEnded firing between `StoreResult` and the NotifyUpdate-driven
    recalc would otherwise erase the value before the wrapper reads it; the
    wrapper would re-issue `xlfRtd` against the still-connected topic, Excel
    would replay `#GETTING_DATA`, and (the one-shot handler having already
    run) the cell would be stuck. With the guard, an entry is reclaimed on the
    first CalculationEnded **after** DisconnectData — the next user-initiated
    recalc (F9) then recomputes fresh. **memoize:true:** the function name is
    in the memoize subset, so `ClearNonMemoized` always skips it; the entry
    persists until process teardown. The registry dtor is deliberately trivial
    (§20.2 "leak, don't crash" — no `VariantClear`/`SysFreeString` from static
    destructors on a forced unload).

**Thread-safety:** the wrapper runs on calc threads, `ProcessRtdUpdate` on the
IPC thread, calc-end/xlAutoClose on the STA thread — all `RtdOnceRegistry`
access goes through one mutex. The `#GETTING_DATA` scode (2043) is kept
byte-identical to `rtd/server.h`'s RefreshData placeholder (§22); do not
diverge. Unload-safety idioms (`g_isUnloading`, ConnectData drain) are
unchanged — rtd-once adds no new detached threads.

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

### 20.3 Cancel-Quit Teardown Model (2026-06-13) — non-destructive `xlAutoClose` + reap on real exit

Source of truth: `docs/superpowers/specs/2026-06-13-cancel-quit-teardown-design.md`.

**The bug it fixes.** Excel calls `xlAutoClose` **before** the "Save changes? /
Cancel" dialog when the user quits or closes the last dirty workbook (confirmed
against Excel-DNA's "AutoClose and Excel shutdown" docs). `xlAutoClose` is the
**only** callback that fires on a **cancelled** quit. The pre-fix
`OnAutoClose()` (and the eager ribbon disconnect / `CoRevokeClassObject` /
unregister / drains in the generated `xlAutoClose`) did **irreversible** teardown
at that too-early point: latched `g_isUnloading=true`, `SetEvent(hShutdownEvent)`,
stopped/joined the worker, `delete g_phost`, `CloseHandle(hJob)` (Job has
`KILL_ON_JOB_CLOSE` → killed the Go server), disconnected the ribbon, revoked the
class object. On a **cancelled** quit the DLL stayed loaded but the add-in became
a **zombie**: every UDF hit the `g_phost==nullptr` guard and returned `#VALUE!`,
RTD/commands/ribbon were dead, the server was gone, `g_isUnloading` was stuck
true, and no second `xlAutoOpen` ever ran.

**The model now.**

1. **`xll::OnAutoClose()` (and the generated `xlAutoClose`) are NON-DESTRUCTIVE.**
   They log and `return 1`. They do NOT set `g_isUnloading`, `SetEvent`, kill the
   server, `CloseHandle(hJob)`, stop/join the worker, run the §23.0 drains, delete
   `g_phost`, disconnect the ribbon, or revoke the class object. On a cancelled
   quit everything stays alive and the registered UDFs keep working.

2. **`xll::GracefulTeardownOnce()`** (`xll_lifecycle.cpp`, exported via
   `xll_lifecycle.h`) holds the destructive graceful path, guarded by an
   `std::atomic<bool> g_teardownDone` **CAS so it runs EXACTLY ONCE**. It sets
   `g_isUnloading=true`, signals the shutdown event, invokes the registered COM
   teardown hook (ribbon disconnect / `CoRevokeClassObject` / registry unregister
   / GDI+ down — which live in the template TU and are plumbed in via
   `SetGracefulTeardownHook`), `SetEvent(hShutdownEvent)`,
   `StopWorker`/`JoinWorker`/monitor join, runs the §23.0 drains
   (`WaitForRtdConnectDrain`, `WaitForCommandDrain`), `delete g_phost`, and closes
   the process/job/event handles. It runs on the **STA thread** (COM event
   delivery) — NOT the loader lock — so the joins/drains/`delete` are safe.

   **STA re-entrancy (hardened 2026-06-13).** The teardown hook's
   `SetRibbonConnected(false)` PUMPS the STA message loop, during which Excel can
   deliver `OnDisconnection(ext_dm_HostShutdown)` and **re-enter
   `GracefulTeardownOnce()` on the same thread**. The `g_teardownDone` CAS makes
   that re-entrant call a **pure no-op** — it returns at the CAS and never reaches
   the joins / drains / `delete g_phost` (which the winning outer call owns and may
   be running further down the same stack). `g_isUnloading=true` and the first
   `SetEvent(hShutdownEvent)` are done **before** the hook so anything pumped in
   observes unloading and self-aborts. A dedicated `static std::atomic<bool>
   s_inHook` re-entrancy guard (RAII-cleared on normal return and C++ exception
   unwind; an async SEH fault under `/EHsc` may skip it, harmless because the
   `g_teardownDone` CAS already prevents a second hook call) wraps the hook
   invocation as defense-in-depth so the hook body itself is never run twice on
   one stack. `DLL_PROCESS_ATTACH` resets BOTH `g_isUnloading=false` and
   `g_teardownDone=false` (probe-unload-reuse symmetry).

3. **Drivers — confirmed-shutdown signals only** (`RibbonAddIn`,
   `ribbon_addin.cpp`, COM-add-in builds only):
   * `OnBeginShutdown` → `GracefulTeardownOnce()` (fires only on a REAL quit,
     after the cancel decision; never on a cancelled quit).
   * `OnDisconnection` → `GracefulTeardownOnce()` on **both** `ext_dm_HostShutdown`
     (host shutdown) and `ext_dm_UserClosed` (add-in disabled, session continues).
   The CAS makes these idempotent with each other and with the DETACH backstop.

4. **`DLL_PROCESS_DETACH` = universal backstop** (covers the non-ribbon path and
   any case where `OnBeginShutdown` did not run; it NEVER fires on a cancelled
   quit because the DLL stays loaded). It keeps the §20.2 loader-lock discipline.
   **`CloseHandle(g_procInfo.hJob)` runs UNCONDITIONALLY** at the top of the
   DETACH case — OUTSIDE the `!g_isUnloading` guard (null-checked + idempotent;
   NULLs the field). Rationale (hardened 2026-06-13): `GracefulTeardownOnce()`
   sets `g_isUnloading=true` EARLY but closes `hJob` near its end; if it aborted
   mid-way (the hook's SEH/`XLL_SAFE_BLOCK` swallowed a fault before its own
   `CloseHandle(hJob)`), a `!g_isUnloading`-gated close at DETACH would SKIP the
   reap and **orphan the Go server** for the rest of the session on add-in-disable.
   The always-close is a kernel call (loader-lock-safe) that reaps the server via
   `KILL_ON_JOB_CLOSE`. The REST of the backstop stays under the `!g_isUnloading`
   guard: set `g_isUnloading`, `SetEvent(hShutdownEvent)`, then DETACH (NOT join)
   the worker/monitor threads. `hProcess` and `hShutdownEvent` are
   **intentionally leaked** on this forced-unload path (one-session, §20.2-accepted;
   OS reclaims on process exit) — only `hJob` is closed because its closure has the
   side effect we need. DETACH deliberately does **NOT** call
   `GracefulTeardownOnce()` / run the drains / `delete g_phost`: blocking on a
   thread join or running C++/SHM destructors under the loader lock is unsafe per
   §20.2. On a real process exit the OS reclaims the leaked `g_phost`.

**§23.0 reconciliation.** The RTD/command drains moved from `OnAutoClose` into
`GracefulTeardownOnce()` and now run on the STA thread (a *safer* context). The
§23.0 UAF window actually **shrinks**: `g_phost` is only ever deleted inside the
single-shot `GracefulTeardownOnce()` after the drains, and never at DETACH.

**EXPERIMENT-GATED FOLLOW-UP (design §5 / §8 decision 2 — NOT implemented).** This
model assumes that after `xlAutoClose` + Cancel, Excel keeps the XLL's functions
**registered**. If a real-Excel experiment shows Excel **unregisters** the XLL at
`xlAutoClose` (the cancelled `=Add(2,3)` recalc returns `#VALUE!`/`#NAME?` instead
of the value), the documented follow-up is to **re-register** (re-run the
`xlfRegister` loop) from the first `CalculationEnded` after a cancelled
`xlAutoClose`. That re-registration is deliberately **not** coded yet — it is
gated on running that experiment. Code comments in `OnAutoClose` and the generated
`xlAutoClose` flag this.

**Regression tests** (`internal/generator/gen_cancel_quit_test.go`): assert the
embedded `OnAutoClose` is non-destructive, `GracefulTeardownOnce` holds the
single-shot CAS + relocated teardown, DETACH closes `hJob` while preserving §20.2,
the ribbon COM events drive the teardown, the generated `xlAutoClose` no longer
disconnects/revokes/drains in the early path, and the non-COM build emits no hook.

**Previously a residual, now RESOLVED — the ribbon STA retry timer (documented
2026-06-12, fixed 2026-06-13):**
The original problem: the ribbon COMAddIns connect needs the in-process
`Application` object, reachable only via the `XLMAIN→XLDESK→EXCEL7` window walk.
When the add-in auto-loads at Excel startup with NO workbook open there is no
`EXCEL7` child window, so the connect deferred. The first fix retried on an idle
`SetTimer(NULL,…)` STA thread timer (`ArmRibbonConnectTimer`/`RibbonConnectTimerProc`),
which carried an unavoidable unmap hazard: "leak, don't crash" does NOT transfer
to a Win32 thread timer. A leaked thread keeps running harmlessly; a leaked
`TimerProc` is a raw code pointer INTO the DLL that the OS dispatches on the next
`WM_TIMER` — after a forced `FreeLibrary` WITHOUT `xlAutoClose` that is a
guaranteed 0xC0000005, and the `g_isUnloading` guard inside the proc cannot help
(the guard itself is unmapped code). `KillTimer` could only run from the owning
STA thread, so a DllMain disarm was impossible (`DLL_PROCESS_DETACH` may run on
the FreeLibrary caller's thread). Every idle-callback alternative (TimerProc,
message-only-window WndProc) had the identical hazard.

**The fix** removes the timer entirely and replaces it with a **synchronous
temporary-workbook bounce** at `xlAutoOpen`, adopting Excel-DNA's proven mechanism
(`Source/ExcelDna.Integration/Excel.cs`, `GetApplicationFromNewWorkbook`). When
`GetExcelApplication()` returns nullptr (no workbook), `GetExcelApplicationOrBounce()`
(`xll_main.cpp.tmpl`) issues `xlcNew(5)` + `xlcWorkbookInsert(6)` to materialize a
workbook (and the `EXCEL7` window), re-acquires the `Application`, then closes the
scratch workbook with `xlcFileClose(false)` (no save) in a guaranteed cleanup path.
The COMAddIns connection binds to the `Application`, not the workbook, so it
**survives the temp workbook closing** — the ribbon tab appears normally when the
user later opens a workbook. These `xlc*` command opcodes are callable only from a
macro/command context; `xlAutoOpen` qualifies (the bounce is gated to the
`xlAutoOpen` first-attempt path only via a `bool allowBounce` parameter — never on
calc-end, never from worksheet-function/RTD contexts). The accepted cost is a
brief startup flicker when Excel starts with no workbook. Because there is no
longer any self-owned idle callback, the forced-unload crash residual is gone.

**Caveat (data-loss boundary).** The bounce only runs when `GetExcelApplication()`
returned null at `xlAutoOpen` entry — i.e. no `EXCEL7` window was reachable, which
strongly implies no document was open — so the blast radius is bounded to the
empty-startup case. As a hard guard, `xlcFileClose(false)` is now **close-by-identity**:
the scratch book's name is captured via `GET.DOCUMENT(88)` (`xlfGetDocument`, selector
88 = active workbook name) immediately after creation, re-read just before the close,
and the close is issued ONLY if the active workbook is still that scratch book. If a
real user document became active in between (e.g. `excel.exe somedoc.xlsx` with the
add-in auto-loading, ordering not contractual), or if either name capture fails, the
close is skipped and a warning logged — leaking a blank scratch book is strictly safer
than discarding a user's unsaved document, so the bounce can never cause data loss.
`TryConnectRibbon` is also guarded against STA re-entrancy (a `CalculationEnded`
callback firing mid-bounce) via a function-static `std::atomic<bool> s_inConnect`, so a
second `COMAddIns…Connect` cannot land while the bounce is in flight.

The calc-end retry (`TryConnectRibbon("calc end")`, `allowBounce=false`) is KEPT as
a hazard-free defensive fallback: it is an Excel-registered event callback (no unmap
hazard) and only matters in the rare case the bounce itself fails (e.g. C API
unavailable). Graceful degradation throughout: a failed bounce logs a warning and
leaves functions/commands fully operational.

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

### 22.4 `UpdateNotify` must run on the STA (worker → hidden message window) — SHIPPED v0.8.11

Excel hands us the `IRTDUpdateEvent` callback (`m_callback`) on its main STA thread in
`RtdServerBase::ServerStart`. But RTD value updates are dispatched on the **background worker thread**
(`xll_worker.cpp WorkerLoop`, a plain `std::thread` with no `CoInitialize`). Calling
`m_callback->UpdateNotify()` directly from there (as the code did pre-v0.8.11) is a **raw cross-apartment
COM call** on an unmarshalled "Both"-threaded interface — a latent defect — and the synchronous call can
head-of-line block the single worker while the STA is busy (e.g. COM-automation-driven sheet building),
stalling all RTD updates then delivering a burst.

**Fix:** route the notify onto the STA via a hidden `HWND_MESSAGE` window. New assets
`include/xll_rtd_notify.h` + `src/xll_rtd_notify.cpp` (`XLL_RTD_ENABLED`-gated):
* `CreateRtdNotifyWindow()` — registers a window class (`g_hModule` HINSTANCE) + creates a message-only
  window, called at `xlAutoOpen` ON THE STA, before `StartWorker()`.
* `SignalRtdUpdate()` — called by the worker from `ProcessRtdUpdate` instead of `NotifyUpdate()`. A
  coalescing `std::atomic<bool>` ensures only the 0→1 transition `PostMessageW`s `WM_APP+1`; PostMessage is
  **non-blocking** even when the STA is busy, so the worker never blocks. Bursts collapse to one notify per
  pump cycle (`UpdateTopic` already stored every value under `m_topicMutex`, so `RefreshData` reads them all).
* WndProc (dispatched by Excel's STA pump → correct apartment) clears the coalescing flag FIRST (so an
  update arriving during the call re-posts, none lost), guards `g_isUnloading`/`g_rtdServer`, then calls
  `NotifyUpdate()`. Wrapped in `XLL_SAFE_BLOCK` (C-ABI fault containment, §20).
* `DestroyRtdNotifyWindow()` — called in `RunDestructiveTeardown` AFTER `JoinWorker` (no post can race),
  ON THE STA (same thread that created it — required by `DestroyWindow`). Reached on BOTH teardown paths
  (real quit via `ServerTerminate`→`RunDestructiveTeardown`; add-in disable via `GracefulTeardownOnce`).
  Forced `DLL_PROCESS_DETACH` (no graceful teardown) LEAKS the window — accepted §20.2 residual (worker
  stopped, WndProc `g_isUnloading`-guarded); do NOT `DestroyWindow` under the loader lock.

**Why a window and not `xlcOnTime`** (which §"calc-end deferred xlSet" chose to avoid the WndProc teardown
hazard): `xlcOnTime` is a C-API (`Excel12`) call that can only be issued from an Excel/macro thread. The
calc-end deferral runs in `HandleCalculationEnded` (already on the STA), so it CAN use `xlcOnTime`. The RTD
notify originates on the **worker thread**, which is not an Excel thread and cannot call `Excel12` at all —
the only way an arbitrary thread can hand work to the STA is `PostMessage` to a window it owns. So the
hidden message window is unavoidable here. Regression: `internal/generator/gen_rtd_notify_window_test.go`.

## Confirmed-Correct Decisions (Do NOT Change)

Synced from the workspace `IMPROVEMENT_BACKLOG.md` §6. These were flagged by past
reviews and confirmed correct — do not "fix", "harden", or re-propose:

* **`pkg/algo/greedy_mesh.go` int32-wrap boundary guard is unnecessary** (proven
  2026-06-13). `nextCol/nextRow = MaxInt32+1` wraps to `MinInt32`, but `GreedyMesh`
  sorts cells `(Row,Col)` ascending, marks `visited`, and expands; the wrap target
  `MinInt32` is the global minimum so it is **always processed/visited first**, and
  the `grid[next] && !visited[next]` guard blocks any wrong merge. The wrap is
  unreachable by any API/reuse path (invariant proof) — no observable behavior
  change exists, so a guard would be unverifiable dead code. Do not add it.
* **`range` is intentionally unsupported as a RETURN type** (v0.5.0 decision). A
  range in value position is meaningless and a `U` return breaks registration
  (worksheet name → `#NAME?`). Use grid/numgrid returns. See §19.2 / §19.3. Do
  not re-propose.
* **raw-XML ribbon mode does not support image files** (2026-06-12, user-confirmed).
  The `loadImage` rejection on the raw-XML path is by design; file-based icons
  require structured mode (`tab`/`groups`). See §18 ribbon decision. Do not re-propose.
* **`date` maps to `SchemaType="double"`, NOT to a `protocol::Date` union member**
  (verified 2026-06-16). Generated schemas encode dates as `double`; generated C++
  never references `protocol::Date`. This is why, before the schema was
  single-sourced (§18.1), a shipped showcase whose `generated/protocol.fbs` lacked
  the `Date` table still compiled and passed date-I/O e2e — the local `protocol.fbs`
  is only a flatc parse-stub and the real protocol code comes from the pinned
  `types` module. The embedded copy is now auto-synced from pinned `types` (so it
  *does* carry the `Date` table) and the drift gate enforces parity, but the
  invariant stands: a cross-repo audit that flags `protocol.fbs` `Date` "drift" as
  a blocker is wrong — date rides the `double` path. Do not re-report it as a
  blocker, and do not "wire `date` to the `Date` union" without an explicit design
  decision (it is intentionally `double`-backed today).
* **RTD RefreshData SAFEARRAY 2-D layout (§22.2/§22.3), async type strings, and
  the `extern "C"` export scheme** are confirmed correct — do not alter the
  dimension order or export shape.
* **x86/x64 TSO assumption (§0.1)** — acquire-release pairs rely on hardware TSO;
  ARM weak-memory concerns are out of scope. Mirror of `shm`'s rule.

## 23. Known Improvement Backlog

These came out of a code review on 2026-05-16. Address them as part of normal work; do not block on a dedicated epic.

### 23.0 C++ Audit (2026-05-16) — Status

A focused C++ audit on 2026-05-16 produced 3 HIGH + 7 MED + 5 LOW findings. The items below tracked as **DONE** were patched the same day; **OPEN** items remain.

* **DONE — HIGH:** `internal/assets/files/src/xll_cache.cpp` `GetOrComputeRefHash`: stack buffer for `XLMREF12` was sized as `sizeof(WORD) + sizeof(XLREF12)` (18) but padding makes `sizeof(XLMREF12)==20` on common ABIs, overrunning by 2 bytes. Fixed by using `alignas(XLMREF12) char mrefBuf[sizeof(XLMREF12)]` and adding a file-scope `static_assert(sizeof(XLMREF12) >= sizeof(WORD) + sizeof(XLREF12), ...)`.
* **DONE — HIGH:** `internal/assets/files/include/xll_async.h` declared `int32_t ProcessAsyncBatchResponse(const uint8_t*, std::vector<XLOPER12>&, std::vector<XLOPER12>&)` while the implementation in `xll_async.cpp` was `void ProcessAsyncBatchResponse(const protocol::BatchAsyncResponse*)` — a latent ODR violation. Header updated to match the implementation; `xll_worker.cpp` now `#include`s `xll_async.h` instead of forward-declaring locally (single source of truth).
* **DONE — HIGH/MED:** `types/src/mem.cpp` `xlAutoFree12` lacked `__declspec(dllexport)`. When `types` is linked as a static library into the XLL, Excel cannot resolve the symbol by name and every `xlbitDLLFree`-marked `XLOPER12` leaks. Fixed by introducing a `TYPES_EXCEL_CALLBACK` macro (`extern "C" __declspec(dllexport) void __stdcall` on `_WIN32`, callback-only `extern "C" void __stdcall` elsewhere) in `types/include/types/mem.h` and applying it to the declaration and definition.
* **DONE — MED:** `internal/assets/files/src/xll_embed.cpp` had `extern HMODULE g_hModule;` while `xll_lifecycle.h` / `xll_lifecycle.cpp` define `HINSTANCE g_hModule`. Both alias `void*` on Windows so it linked, but it was ODR-divergent. Replaced the local `extern` with `#include "xll_lifecycle.h"` so there is one source of truth for the declaration.
* **DONE — MED:** `internal/assets/files/src/xll_lifecycle.cpp` `DllMain` forced-unload branch reordered so `SetEvent(g_procInfo.hShutdownEvent)` runs **before** `ForceTerminateWorker()` and `g_monitorThread.detach()`. This gives the threads a brief chance to observe shutdown before being orphaned, while still honoring §20.2 ("leak, don't crash") — no new work is added in `DLL_PROCESS_DETACH`, only existing steps reordered.
* **DONE — HIGH (memory-safety-auditor A4, 2026-05-16; integration completed 2026-05-17):** `internal/assets/files/src/xll_rtd.cpp` `RtdServer::ConnectData` spawns a detached `std::thread` whose lambda accesses `g_host`. On forced unload (per §20) or graceful close (`OnAutoClose` deletes `g_phost`), the lambda could touch freed memory. Patched in-file: the lambda now checks `xll::g_isUnloading` at every yield point (top, before `g_host.GetZeroCopySlot()`, before `slot.Send`); a file-static `g_rtdConnectInFlight` counter is incremented/decremented via an RAII guard; `WaitForRtdConnectDrain(timeoutMs)` is declared in `xll_rtd.h` and defined in `xll_rtd.cpp`. The integration is now wired in: `xll_lifecycle.cpp::OnAutoClose` (under `#ifdef XLL_RTD_ENABLED`) calls `WaitForRtdConnectDrain(2000)` immediately **before** `delete g_phost`. Validated end-to-end by `internal/smoketest` (sync + async + RTD round-trip without segfault).
* **DONE — MED (drain-cap gap closed 2026-06-12, formerly an "accepted residual"):** the A4 fix above wired in a 2000 ms drain cap, but `ConnectData`'s detached thread sent via a SINGLE `slot.Send(..., MSG_RTD_CONNECT, 5000)` — a single Send could block in SHM up to 5000 ms. A Connect blocked >2 s outlived the drain, so `OnAutoClose` reached `delete g_phost` while that Send was still touching the slot — a narrow use-after-free. **Closed** by replacing the single 5000 ms Send with a bounded retry loop (`kMaxAttempts = 20`, `kAttemptTimeoutMs = 250`) that re-checks `xll::g_isUnloading` between attempts and before every slot acquire/Send, re-acquiring a FRESH `ZeroCopySlot` each attempt (shm `DirectHost.h` `ZeroCopySlot::Send` disowns its slot on timeout — `slotIdx = -1` — so a slot object cannot be reused). With <=250 ms per-attempt + unload re-checks, an in-flight Connect thread returns within ~250 ms of `g_isUnloading` being set, so the existing 2000 ms drain cap is sufficient with margin — no UAF window. Total retry budget (20 × 250 ms = 5000 ms) keeps slow-but-alive-host behavior identical to the old single 5000 ms Send. Mirrors `ribbon_addin.cpp::SendCommandInvoke` (same structural problem, §18.11 first-click-delivery contract). **Duplication-on-retry, verified benign:** a timed-out `Send` does NOT mean the guest never consumed the request — `DirectHost::WaitResponse` publishes `SLOT_REQ_READY` first, then waits for the guest to flip to `SLOT_RESP_READY`; a timeout means "no response in budget", so a retry can deliver `MSG_RTD_CONNECT` twice. This is safe: `RtdManager.Subscribe` (`pkg/rtd/manager.go`) is idempotent on `(topicID, key)` (a repeat early-returns), so the subscription map is unchanged; the user's `OnRtdConnect` may run twice (panic-recovered goroutine), exactly as the ribbon CommandInvoke path may double-fire — no dedup added. `DisconnectData` (synchronous on the STA thread, not drain-covered) keeps its single 500 ms Send (already < the 2000 ms cap) but gains the `g_isUnloading` re-check + `g_phost` null-check to guard the forced-unload race. Asset regression: `internal/generator/gen_rtd_connect_test.go::TestRtdConnectDrainCapAlignment` pins the retry loop + unload re-check markers and asserts the old single 5000 ms Send is gone.

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
* **DONE (2026-06-16) — RTD connect/disconnect never wired context cancellation.** `HandleRtdConnect` (`pkg/server/handlers.go`) injected `ctx := context.Background()` into the detached `onConnect` goroutine, so the `rtd.RunOnce`/`RunOnceGrid` ctx-cancellation machinery (and the `ctx.Err()` fast-path) was dead code in production: a topic disconnected mid-flight kept running a long one-shot handler and then `SendUpdate`'d to an already-unsubscribed topic. **Fix:** `RtdManager` (`pkg/rtd/manager.go`) gained a per-topic cancel registry — `RegisterConnectCancel(topicID, context.CancelFunc) (deregister func())` — guarded by the existing `m.mu`; `Unsubscribe(topicID)` now cancels + drops any registered in-flight connect under the same critical section (calling the non-blocking `CancelFunc` while holding `mu` is safe — no RtdManager re-entrancy, no lock-ordering hazard vs Publish/SendUpdate). `HandleRtdConnect` derives `context.WithCancel(context.Background())`, registers the cancel keyed by topicID **synchronously before** launching the goroutine (so a disconnect immediately after the connect ack can't miss it), and `defer`s `cancel()` + the (generation-safe) `deregister()`. **Generation race:** topicIDs are reused by Excel after disconnect; each registration carries a monotonic generation, so a slow connect's deferred deregister only removes the entry if it is still its OWN generation — it cannot clobber/cancel a newer registration that reused the topicID; a replacing `RegisterConnectCancel` cancels the stale one. Template needs NO change (`server.go.tmpl` already threads the param `ctx` into `rtd.RunOnce`/`RunOnceGrid`); `HandleRtdDisconnect`'s `onDisconnect` user-hook ctx left as `context.Background()` (out of scope — non-RTD command/event handlers also keep `context.Background()`). Regressions: `pkg/rtd/manager_cancel_test.go` (Unsubscribe cancels; deregister stops a later Unsubscribe; **generation safety** — a stale deregister after topicID reuse does NOT cancel/remove the new registration, FAIL-before confirmed by removing the `cc.gen == gen` guard; replace-cancels-stale) and `pkg/server/handlers_rtd_test.go` (end-to-end, pure Go no Excel: a blocking-on-`ctx.Done()` connect handler IS cancelled when `HandleRtdDisconnect(topicID)` runs mid-flight; reused-topicID path — FAIL-before confirmed by reverting `HandleRtdConnect` to `ctx := context.Background()`). All pass under `-race -count=10`.
* **DONE (2026-05-17):** `internal/generator/gen_cpp_test.go::TestGenCpp_StringErrorReturn` was hardcoded to `MsgId 133` for the first user function. Fixed by deriving the expected IDs from `server.MsgUserStart + i` so future bumps to that constant don't desync the test silently.
* **DONE (2026-05-17):** the `cmd/` integration tier was broken on Windows — `setupMockFlatc` wrote batch script content to `flatc.exe` and Windows refused to load it as PE. Replaced with a real Go-built stub at `cmd/testdata/fakeflatc/main.go`; `setupMockFlatc` now compiles it once via `go build`, caches in user cache dir, and hands the absolute path to `generator.Options{FlatcPath: ...}`. The stub recognizes `--version`, `--go`, `--cpp`, `--go-namespace`, `-o` and writes minimal `<base>_generated.{go,h}` placeholders so the generator's post-processing (`fixCppImports`) finds something to rewrite. Also fixed a second rot — `TestRepro_MultipleAsync` was asserting on a refactored-away `queueAsyncResult` helper; updated to count `asyncBatcher.QueueResult(` call sites instead. All 5 previously-failing tests pass; `go test ./cmd/...` is green on Windows.
* Chunk reassembly (`pkg/server/manager.go`) is now covered by `pkg/server/manager_test.go` (`TestChunkManager`, `TestChunkManager_ConcurrentArrivals`), exercising all four edge cases under `-race`. **Resolved findings (2026-05-16, stabilization pass — Stabilizer):**
  * **Resolved — Duplicate chunk premature completion (HIGH, data corruption).** `ChunkBuffer.Received` was a naive byte counter, so a duplicate of the first chunk in a multi-chunk message pushed `Received` past `TotalSize` and triggered premature completion with the trailing bytes still zero. Fix: added `ChunkBuffer.ReceivedOffsets map[uint32]bool`; `HandleChunk` now skips the byte copy and `Received` bump when the offset has already been seen. The defensive `offset+dataLen <= len(buf.Data)` bounds check is preserved. Regression: `TestChunkManager/DuplicateChunk_IdempotentReceive` (calls `HandleChunk` end-to-end and asserts (a) duplicate does not complete, (b) reassembled buffer is byte-identical to the non-duplicate sequence).
  * **Resolved — `SendAckOrChunk` publication-order race (HIGH).** `AddOutgoingChunk` published the `OutgoingChunk` pointer to a concurrently-reachable map BEFORE `out.Offset = currentSize` was written, so a `HandleAck → GetNextChunk` racing this path could observe `Offset==0` and resend the first slice. Fix: write `out.Offset = currentSize` BEFORE the `cm.AddOutgoingChunk` call; load-bearing comment added at the call site. Regression: `TestChunkManager/SendAckOrChunk_OffsetPublishedBeforeMapInsert` (steady-state + 200-iter stress under `-race` — `-race` flags the data race on the previous code).
  * **Resolved — `GetChunkBuffer` unbounded allocation (HIGH, DoS).** The wire-supplied `total` was trusted as the allocation size. Fix: added `ChunkManager.MaxChunkBufferBytes` (default `256 << 20`, settable via `NewChunkManagerWithMax`); `GetChunkBuffer` now returns `(*ChunkBuffer, error)` and refuses requests > the cap without inserting into `chunkCache`. `HandleChunk` propagates refusal to the wire as `MsgSystemError` (value 127, mirroring `shm.MsgTypeSystemError` in shm@HEAD; defined locally in `pkg/server/types.go` because the pinned shm module v0.5.4 does not yet export that constant). Regressions: `TestChunkManager/OversizedTotal_AllocationRejected` (1 TiB request via direct API and via wire path), `TestChunkManager/OversizedTotal_CustomLimitHonored`.
  * **Resolved — concurrent duplicate FINAL chunk double-dispatch (HIGH, side-effect re-execution; 2026-06-10).** `HandleChunk` released `buf.Mutex` after computing `isComplete := buf.Received >= buf.TotalSize`, then dispatched outside the lock. A retransmitted FINAL chunk racing the original (e.g. after a dropped ACK) let BOTH goroutines observe completion under the lock and BOTH call `dispatch()` — the user function ran twice (side effects!) and two responses were written. Dedup-by-offset did not help: the dup's bytes are skipped, but the completion observation still fires on every arrival. Fix: added `ChunkBuffer.Dispatched bool`; the completion claim (`Received >= TotalSize && !Dispatched → Dispatched = true`) now happens INSIDE `buf.Mutex`, so exactly one goroutine wins and dispatches. Note on a late dup after `RemoveChunkBuffer`: `GetChunkBuffer` re-allocates a fresh buffer whose single final chunk cannot reach `TotalSize` for a multi-chunk transfer, so it never re-dispatches and is reaped at TTL (benign). Regression: `TestChunkManager_ConcurrentDuplicateFinalChunk` (50 outer × 64 concurrent FINAL replays; asserts exactly one dispatch + byte-identical reassembly; fails with `got 2` on parent commit under `-race`).
  * **Resolved — `ChunkManager` cleanup goroutine could never be stopped (MED, goroutine leak; 2026-06-10).** `cleanupLoop` was `for range ticker.C {}` with no exit path and no `Close()`, so the goroutine + ticker leaked for the manager's lifetime (one per `NewChunkManager*`). Fix: added a `stop chan struct{}` + idempotent `Close()` (guarded by `sync.Once`); the loop now `select`s on `ticker.C` and `cm.stop`, with `defer ticker.Stop()`. No existing shutdown path called the manager, so `Close()` is provided for the server teardown to wire in. Regression: `TestChunkManager_CloseStopsCleanupGoroutine` (spawns 25 managers on a 1ms tick, asserts goroutine count rises then drains back to baseline after `Close()`; also calls `Close()` twice to prove idempotency; fails with `leaked ~25` on parent commit).
  * **Resolved — `generateTransferID` constant-0 fallback (MED, correlation-key collision; 2026-06-10).** On the (essentially impossible) `crypto/rand.Read` error path, `generateTransferID` returned a constant `0`, collapsing every concurrent chunked transfer onto the same correlation key → guaranteed cross-talk/corruption. Fix: fall back to `math/rand/v2`'s lock-free auto-seeded `Uint64()` and log the degraded path. No dedicated test (the failure path is not reachable without injecting a crypto/rand fault); covered by inspection.
    * **Unified across all three chunk-framing sites (2026-06-19).** The generator now lives in leaf package `pkg/transferid` (`transferid.New()`). Previously only `SendAckOrChunk` (host→guest) used the hardened crypto/rand path; the guest→host sites `async_batcher.go` (`sendChunkedAsync`) and `rtd/manager.go` (`SendOnceGrid`) each used `uint64(rand.Int63())` — a 63-bit value from the global-mutex `math/rand`, which both halved the keyspace and serialized concurrent senders. `pkg/transferid` is a leaf (imports only `pkg/log`) so both `pkg/server` and `pkg/rtd` share one 64-bit generator despite the server→rtd import cycle (`NewSystemHandler`). **New transfer-ID sites MUST call `transferid.New()`, never roll their own `rand`.**
* `internal/regtest`: regression tests bind to Excel via COM (`internal/regtest/runner.go`). Add a short doc note explaining how to run them on a fresh Windows machine (which Excel SKUs work, what registry entries are needed).
* Follow-ups uncovered during the stabilization pass:
  * `MaxChunkBufferBytes` is currently only mutable through code (`ChunkManager` field or `NewChunkManagerWithMax`). Plumb it through `xll.yaml` → `internal/config` so deployments can tune it without rebuilds. Co-change cluster: pairs with the §23.2 cleanup-tick/TTL promotion.
  * **DONE (2026-05-17, xll-gen v0.3.8 / shm v0.6.0):** local `MsgSystemError` sentinel in `pkg/server/types.go` removed; `pkg/server/handlers.go` and `pkg/server/manager_test.go` now use `shm.MsgTypeSystemError` directly. shm exported the constant in v0.6.0 alongside the streaming API.
  * Wire `Chunk` schema (in `types/`) does not carry an explicit `total_chunks` field; dedup is keyed on offset (unique per chunk on first transmission) which is sufficient given chunk size is sender-controlled and offsets do not overlap. If a future change introduces variable-sized chunks within a transfer, revisit and key on `(offset, length)` or add an explicit chunk-index field.

### 23.3.1 Real-Excel verification (2026-06-12, smoke + spill + rtd-once pass)

A full real-Excel run (Excel 2021 / C2R 16.0.19127 x64, MinGW UCRT x86_64) of the
smoke harness plus first-ever real-Excel runs of the spill and rtd-once features
surfaced two real product bugs, both fixed in runtime C++ assets with marker-based
regression tests in `internal/generator/`.

* **DONE — RTD ConnectData detached-thread did not compile under MinGW (build break, HIGH).** The drain-cap retry loop added 2026-06-12 (§23.0) declared `constexpr int kMaxAttempts`/`constexpr unsigned int kAttemptTimeoutMs` in the scope ENCLOSING the detached-thread lambda, but the lambda (`[TopicID, strings, newVal]`, no capture-default) odr-uses `kAttemptTimeoutMs` (passed by value to `std::chrono::milliseconds` and `slot.Send`). MSVC silently accepts an uncaptured odr-use of a constexpr; **MinGW/GCC rejects it** (`'kAttemptTimeoutMs' is not captured`), so any RTD-enabled project failed to build under the supported MinGW toolchain — the smoke test could not even compile. Fix: move both `constexpr` declarations INSIDE the lambda body (where `ribbon_addin.cpp::SendCommandInvoke` already correctly puts its equivalents). File: `internal/assets/files/src/xll_rtd.cpp`. Regression: `internal/generator/gen_rtd_connect_test.go::TestRtdConnectDrainCapAlignment` extended to assert the constexpr declaration sites come AFTER the `std::thread([...])` opener (fails on the parent form, passes after the move).
* **DONE — async grid/numgrid return corrupted Excel's heap (HIGH, crash).** `=MyAsyncGrid()` (async function returning a spilling `[][]any`/`[][]float64`) crashed Excel on every recalc with `STATUS_HEAP_CORRUPTION` (`0xc0000374`, faulting module `ntdll`). Sync `grid`/`numgrid` spilled fine; only the async path crashed, deterministically. Root cause: `ProcessAsyncBatchResponse` (`internal/assets/files/src/xll_async.cpp`) assumed `xlAsyncReturn` deep-copies the ENTIRE result XLOPER12 synchronously, then freed it immediately via `xlAutoFree12(pxResult)`. True for scalars (value copied inline — which is why `AsyncAdd` worked), but FALSE for `xltypeMulti`: Excel retains the `lparray` pointer to populate the spill range AFTER the calc transaction, so the synchronous `delete[]` of `lparray` was a use-after-free. Fix: a result carrying `xlbitDLLFree` is owned by Excel after the handoff — Excel invokes the exported `xlAutoFree12` callback (deferred) when done; the asset must NOT free it itself and only returns borrowed pool nodes (no `xlbitDLLFree`) via `ReleaseXLOPER12`. This mirrors the always-working SYNC return path (which relies on Excel's deferred `xlAutoFree12`). The ownership bit is captured BEFORE `xlAsyncReturn` so it is never read off an XLOPER12 Excel may already be processing. Regression: `internal/generator/gen_async_grid_test.go::TestAsyncResultDeferredFreeForDLLOwned` (fails on the parent form — the synchronous `xlAutoFree12(pxResult)` — passes after the fix). Verified end-to-end in real Excel: async grid now spills to its full range with `HasSpill=true`, no crash. **Note:** the async grid converter (`AnyToXLOPER12` → `GridToXLOPER12`) and `xlAutoFree12` live in `github.com/xll-gen/types` and were already correct; the bug was purely the asset's premature free, so no `types` change was needed.

  Both C++ asset changes touch the runtime path (not `DllMain`); they should be confirmed by `xll-cpp-reviewer` on the next pass.

* **DONE (2026-06-15) — calc-end deferred `xlSet` re-entered Excel and crashed it during the rtd-once disconnect window (BLOCKER, 0xc0000005 faulting module EXCEL.EXE).** The showcase hard-crashed Excel ~7-8s after "Build Showcase Sheet". Single-variable bisection against a 100%-real-Excel repro (`xll-gen-showcase/tools/repro-crash-uia.ps1`) pinned it: `HandleCalculationEnded()` (`internal/assets/files/src/xll_events.cpp`) — which runs INSIDE the `xleventCalculationEnded` macro callback — called `ExecuteCommands(commands)` (`xlSet`) and `DrainAndApplyDateFormats()` (`xlcSelect`/`xlcFormatNumber`) SYNCHRONOUSLY. When that cell write fires during an rtd-once scalar materialize recalc — where the rtd-once cache-hit wrapper withholds `xlfRtd` and Excel is concurrently DISCONNECTing the rtd-once topics — it re-enters Excel's calc/RTD machinery at a fragile point → AV. Bisection: removing the deferred `xlSet` (`OnRecalc` no-op) SURVIVES while keeping it CRASHES, a clean single-variable crash↔survive toggle; ordinary RTD/async calc-end `xlSet` (no rtd-once) is safe. **Fix:** cell mutation must NOT happen inside the event. `HandleCalculationEnded` keeps the synchronous `MSG_CALCULATION_ENDED` round-trip (IPC blocking is NOT the crash — proven) but COPIES the response buffer into a process-global FIFO (`xll::DeferredCalcEndQueue`) and schedules a registered runner macro via `xlcOnTime(now, "__xllgen_RunDeferredCalcEnd")`. Excel dispatches that macro on the STA thread at an idle point OUTSIDE the event, where the `xlSet`/format calls are safe. `DrainAndApplyDateFormats` rides the same deferral (same in-event cell-mutation class; idempotent once-per-cell so low-risk). Command ORDERING preserved (FIFO queue + in-order command vector). All other calc-end semantics unchanged (RefCache clear, `RtdOnce*Registry::ClearNonMemoized`, the Go-handler round-trip). **Mechanism choice:** `xlcOnTime` over a hidden STA message-window + `PostMessage` — materially simpler (no window class / WndProc / §20.2 WndProc teardown hazard); Excel queues the macro and runs it off the recalc/RTD-teardown path. **Lifecycle/unload (§20.2):** the runner self-aborts on `g_isUnloading` / `g_phost == nullptr` (a leaked `xlcOnTime` macro firing post-unload no-ops and discards the queue); the macro is auto-unregistered when the DLL unloads, so no explicit teardown/WndProc to tear down. New assets `include/xll_deferred_commands.h` + `src/xll_deferred_commands.cpp`; modified `src/xll_events.cpp` and template `internal/templates/xll_main.cpp.tmpl` (registers the runner macro near the `xlEventRegister` block; exports `__xllgen_RunDeferredCalcEnd`). No wire-format/`types` change (only the TIMING of command execution moved). Regression: `internal/generator/gen_calcend_defer_test.go` (structural invariant — `HandleCalculationEnded` must NOT call `ExecuteCommands`/`DrainAndApplyDateFormats` inline and MUST route through `DeferCalcEndCommands`; template registers + exports the runner; scheduler uses `xlcOnTime` and self-aborts on unload). `gen_date_test.go` sub-case (c) reconciled to assert the drain now lives in the deferred runner, not inline. FAIL-before / PASS-after confirmed by reverting the asset to the inline form. Runtime C++ asset (not `DllMain`) — confirm with `xll-cpp-reviewer`. **Empirical gate (real-Excel) pending:** the showcase regenerates its C++ from these assets, so it needs a full C++ rebuild (`task build-cpp-debug`), NOT just `task build-go-debug`, before `tools/repro-crash-uia.ps1` is re-run to confirm CRASH→ALIVE.

* **DONE (2026-06-15) — date auto-format made typing laggy (perf regression, runtime C++ asset).** A recalc fires on EVERY keystroke. `xll::DrainAndApplyDateFormats` (`internal/assets/files/src/xll_date_format.cpp`) issued a SYNCHRONOUS `xlfGetCell` (type 7) COM round-trip PER pending date cell — to read its current number format and conditionally skip already-date-formatted cells — and the producer re-enqueued every date cell every recalc. On a workbook with many date cells that is O(N) UI-thread COM calls per keystroke → typing lag. Fix (two parts): (A) `PendingDateFormats` now owns a mutex-guarded process-global "formatted set" keyed by `(IDSHEET,row,col)` with `AlreadyFormatted`/`MarkFormatted`; once a cell is in the set, ALL later recalcs do ZERO COM work for it (both the producer `EnqueueDateFormatsForCaller` and the drain consult it). (B) the `xlfGetCell`/`IsDateLikeFormat` conditional-skip read is REMOVED entirely — first touch applies the auto-format UNCONDITIONALLY (overriding any pre-existing user format that one time), then marks the cell; on COM failure it does NOT mark, allowing retry next cycle. Intentional behavior change (user-confirmed, value-driven: a date displays as a date). The set is NOT cleared at CalculationEnded (persists for the loaded-DLL lifetime; memory bounded by # distinct date cells in the session); tradeoff: a cell's format is not upgraded later (date→date+time). Now-dead `#include "types/utility.h"` removed from `xll_date_format.cpp` (its `IsDateLikeFormat`/`PascalToWString` uses are gone; the symbols stay used by other TUs — no `types` change). Files: `internal/assets/files/{include/xll_date_format.h, src/xll_date_format.cpp}`. Tests reconciled: `internal/smoketest/date_format_test.go` sub-case 4 (was "conditional-skip leaves a pre-existing date format untouched", which the removed `xlfGetCell` guard can no longer satisfy) rewritten to the once-per-cell assertion (hand-set A1 to a NON-date format `0.00` AFTER first touch, recalc, assert the drain does NOT revert it — proving the formatted set skipped it). Producer-wiring tests (`internal/generator/gen_date_test.go`) unchanged and still pass. Runtime C++ asset (not `DllMain`) — confirm with `xll-cpp-reviewer`. **WON'T DO (decided 2026-06-15):** streaming `mode: "rtd"` (continuous) is NOT wired to date auto-format and will NOT be — its wrapper returns `xlfRtd`'s result directly and never reaches `ScheduleDateFormatsForCaller`, so wiring it would need a separate RTD-delivery-side mechanism. This is an intentional non-goal, not a gap to close: do not add it to the backlog or "fix" it in a future pass. Streaming-RTD date columns simply stay unformatted (users format them manually / via a command).

### 23.4 Dependencies
* **DONE (2026-05-17):** `go.mod` — `golang.org/x/sys` bumped from `v0.1.0` (Jan 2022) to `v0.33.0`. We held back from `v0.34+` because those releases force the Go directive up to 1.25; `v0.33.0` is the newest line still compatible with our `go 1.24.3` floor. Revisit when the project itself is ready to require Go 1.25.
* Verify no other transitive deps are >2 years old at each release; if so, document why.

### 23.5 Windows-Specific Code Layout
* **DONE (2026-05-17):** Created `internal/platform/` with `_windows.go` / `_other.go` build-tagged constants. Migrated 6 `.exe`-extension branches (`internal/flatc/flatc.go`, `internal/regtest/prepare.go`, `internal/regtest/runner.go`, `cmd/regression_test.go`, `cmd/regression_helpers_test.go`) to `platform.ExeName`. Added `platform.FindBuiltExe` for the single-config vs multi-config cmake output layout (used by 2 sites). The remaining `runtime.GOOS == "windows"` checks in `cmd/doctor.go` are install-hint specific (winget) — not the same kind of duplication, intentionally left as-is. Smoketest files use file-level `//go:build windows`, already idiomatic.

### 23.6 Close-time ghost Excel (S1') / orphaned server (S2) — RESOLVED Stage 4 (2026-06-17)

**STATUS: S1' ghost RESOLVED and SHIPPED (Stage 4, 2026-06-17). S2 was never regressed.**
The deferred-teardown fix (Phase 1/Phase 2 split, below) makes `EXCEL.EXE` exit cleanly on a
real quit with live RTD topics. The history (Stages 1–3) is kept below for context; the
authoritative current behavior is the **Stage 4** block at the end of this section. The
temporary `DiagLog` instrumentation has been **REMOVED**; the teardown timeline is now visible
via the normal `LogInfo`/`LogDebug` channel (Phase 1 latches `g_isUnloading` only inside Phase 2,
so the Phase-1 / watcher log lines are not suppressed).


Two reproduced close-time bugs (repro: `xll-gen-showcase/tools/diagnose-close-uia.ps1`,
UIA faithful close + `-KillExcelOnClose`):
* **S1' — ghost Excel:** after the user closes the last window, `EXCEL.EXE` lingers
  windowless 40s+ instead of exiting. (S1 proper — a window REOPENING — was NOT
  reproduced in this pass; only the windowless-ghost variant.)
* **S2 — orphaned Go server / locked log:** an orphaned server holding the inherited
  `<proj>_go.log` handle leaves the file undeletable while no Excel exists.

**Stage-1 fixes landed (all runtime assets/templates, NOT `DllMain`-graceful-path logic):**
* **DONE — #2a job-assignment robustness** (`internal/assets/files/src/xll_launch.cpp::LaunchProcess`):
  `CreateProcessW` now uses `CREATE_SUSPENDED`, `AssignProcessToJobObject` is RESULT-CHECKED
  (loud `LogWarn` on failure, naming the locked-environment case), then `ResumeThread`. Closes
  the assign race and surfaces the locked-env failure that would otherwise silently orphan the
  server (the Job `KILL_ON_JOB_CLOSE` reap would not cover it).
* **DONE — #2b Go parent-death watcher** (`internal/templates/server.go.tmpl`): `Serve()` starts
  `go watchParentDeath(os.Getppid(), …)` which `OpenProcess(SYNCHRONIZE)` + `WaitForSingleObject(INFINITE)`
  on the parent Excel and, on parent exit, `client.Close()` + `osExit(0)`. Inline in the template
  (uses `golang.org/x/sys/windows`, already an available dep) because the showcase pins xll-gen
  v0.8.5 with no `replace`, so `pkg/server/*` changes would not propagate. This is the robust
  backstop that reaps the server even when the Job reap is denied (locked env) — directly fixes S2.
  Graceful skip on `getppid()==0` / `OpenProcess` failure. `osExit` is an injectable var
  (defaults to `os.Exit`) for testability. Regressions: `internal/generator/gen_parent_watch_test.go`
  (structural: watcher wired, FAIL-before confirmed) + `xll-gen-showcase/generated/server_parent_watch_test.go`
  (behavioral matrix under `-race`: skip/open-fail/wait-fail/reap-on-exit; survives regeneration —
  separate filename).
* **DONE — #3 cancel pending `xlcOnTime`** (`internal/assets/files/src/xll_deferred_commands.cpp`):
  `ScheduleDeferredRunner` saves the exact `xlfNow` serial; new `CancelDeferredRunner()` cancels via
  `xlcOnTime(savedSerial, macro, /*tolerance*/missing, /*schedule*/FALSE)`; called from
  `GracefulTeardownOnce` (`xll_lifecycle.cpp`) on the confirmed-teardown path (after `g_isUnloading`,
  before the hook/drains). Removes the post-teardown dispatch of a runner armed by a late
  `CalculationEnded`. Regression: `internal/generator/gen_calcend_defer_test.go::TestCalcEnd_DeferredRunner_CancelOnTeardown`
  (FAIL-before confirmed).
* **Instrumentation (TEMPORARY — REMOVE LATER):** `xll::DiagLog` (`xll_log.cpp`/`xll_log.h`) writes
  UNCONDITIONALLY (bypasses the `g_isUnloading` suppression) to a separate `<proj>_diag.log`. Markers
  at entry of `RibbonAddIn::OnDisconnection`/`OnBeginShutdown`, `GracefulTeardownOnce` (entry+exit+CAS-lost),
  `DllMain DLL_PROCESS_DETACH`, `RtdServer::ServerTerminate`, `RtdServer::DisconnectData`.

**EVIDENCE-BASED ROOT CAUSE of the still-open S1' ghost (do NOT guess-fix; this is the Stage-2 target):**
The `<proj>_diag.log` teardown timeline on a faithful close (server already reaped at t=0; Excel then
ghosts 40s+) shows the FULL graceful path runs and completes fast, and the DLL actually unloads:
```
OnBeginShutdown → GracefulTeardownOnce entry
OnDisconnection RemoveMode=1 (re-entrant during hook STA pump) → GracefulTeardownOnce CAS-lost no-op
GracefulTeardownOnce exit (handles closed, server reaped)   [~20 ms total]
DllMain DLL_PROCESS_DETACH entry                            [~245 ms later — real unload]
```
Two diagnostic facts pin the cause:
1. **`RtdServer::ServerTerminate` and `RtdServer::DisconnectData` NEVER appear in the log.** Excel never
   calls `IRtdServer::ServerTerminate` (nor DisconnectData) before/around its own shutdown for our RTD
   server. Our RTD server (`rtd/server.h::RtdServerBase`) holds an AddRef'd `IRTDUpdateEvent` callback
   (`m_callback`) that is released ONLY in `ServerTerminate`/the destructor — neither of which runs.
2. **`DLL_PROCESS_DETACH` fires (~245 ms after teardown)** — so the XLL fully unloads; nothing in OUR
   DLL code is holding Excel alive after that point. The lingering process is Excel's OWN shutdown
   stalling, consistent with an RTD COM teardown that never completes (Excel's RTD machinery still
   considers the server/topic live because `ServerTerminate` was not driven and the `IRTDUpdateEvent`
   reference was never released).
**Conclusion / Stage-2 hypothesis:** the ghost is Excel waiting on RTD COM teardown that we never
complete because Excel does not call `ServerTerminate` on this shutdown path. The graceful teardown
revokes the class object (`CoRevokeClassObject` in `GracefulComTeardownHook`) but does NOT proactively
release the RTD server's `IRTDUpdateEvent` callback / drive `ServerTerminate`-equivalent cleanup, and
the streaming RTD topics are not torn down (no `DisconnectData`). Stage 2 should investigate
proactively releasing `m_callback` / signalling the RTD topics dead on the confirmed-teardown path
BEFORE the class-object revoke (see §22 / §20.3), and verify against the diag log that the ghost
clears. **A speculative #1/ghost fix was intentionally NOT applied in Stage 1.**

**Stage 2 (2026-06-17) — proactive `m_callback` release APPLIED; ghost NOT cleared. New evidence narrows the holder to Excel's live-topic RTD machinery, NOT our DLL.**
* **DONE (kept) — proactive callback release:** `rtd::RtdServerBase::ReleaseCallbackForTeardown()`
  (`include/rtd/server.h`) mirrors `ServerTerminate`'s `m_callback->Release(); m_callback=nullptr;`
  under `m_callbackMutex`, idempotent + null-checked (safe if `ServerTerminate` later runs — no
  double-free). Wired in `xll::GracefulTeardownOnce` (`xll_lifecycle.cpp`) via `g_rtdServer`, placed
  **AFTER `JoinWorker()` and the `WaitForRtdConnectDrain`** so no in-flight `NotifyUpdate`/
  `ProcessRtdUpdate` (worker thread) or `ConnectData` (detached thread) can race the release. This
  breaks OUR half of the documented Excel↔RtdServer ref cycle and is a correct leak fix; **keep it.**
  Diag log confirms it runs: `RtdServer.ReleaseCallbackForTeardown: m_callback released`.
* **STILL OPEN — ghost persists with the release applied.** Reproduced deterministically
  (`xll-gen-showcase/tools/ghost-check.ps1`, faithful UIA close with active RTD streaming):
  EXCEL.EXE lingers windowless 30s+ even though server is reaped, both logs are FREE, the DLL
  unloads (`DLL_PROCESS_DETACH` fires), AND `m_callback` is released. So releasing our callback ref
  is **necessary but not sufficient.**
* **DECISIVE new evidence — the ghost correlates with LIVE, actively-refreshing RTD topics, not
  with our COM refs:** `ghost-check.ps1 -FastClose` (close ~250 ms after the recalc, BEFORE the RTD
  server instance is even created / topics established — diag shows no `ReleaseCallbackForTeardown`
  line because `g_rtdServer` is still null) → **EXCEL EXITS CLEANLY, no ghost.** A normal slow close
  (2 topics `StockTick`+`Clock` streaming `NotifyUpdate` ~1/s right up to close) → **ghost every
  time.** `ServerTerminate`/`DisconnectData` never fire on either path.
* **Conclusion:** the holder is Excel's OWN RTD subsystem keeping the process alive for its 2 live
  topic subscriptions (it never drove `DisconnectData`/`ServerTerminate`, and likely keeps an
  internal RTD refresh timer/`Application.RTD` throttle alive). Our DLL holds nothing after DETACH.
  Releasing `m_callback` cannot make Excel's RTD manager consider its own teardown complete.
* **NEXT (lead decision required — NOT shipped):** options to evaluate, in order of preference:
  (1) drive an explicit topic teardown on the confirmed path while the host is still reachable
  (e.g. from the worksheet/STA context, force the RTD cells dead so Excel issues `DisconnectData`
  before close) — needs investigation of whether the C API allows this on the close path;
  (2) probe whether NOT revoking the RTD class object (Excel-DNA does not aggressively revoke) lets
  Excel complete its own RTD `ServerTerminate` handshake; (3) last-resort destructive teardown at
  `xlAutoClose` — the user authorized this ONLY as a last resort and ONLY with lead sign-off, since
  it breaks the cancelled-quit invariant (§20). DiagLog instrumentation is **KEPT** (ghost unresolved).

**Stage 3 (2026-06-17) — revoke-skip + in-OnBeginShutdown STA pump tested. Pump REVERTED (cannot work);
revoke-skip KEPT (made Excel start its handshake but insufficient alone). Refined root cause: teardown
is too eager — Excel runs its RTD teardown only AFTER OnBeginShutdown returns, by which point g_phost is
already deleted.**
* **KEPT — host-shutdown RTD revoke-skip (Stage B, mode-threaded).** `GracefulTeardownOnce` now takes
  `bool isHostShutdown`; `RibbonAddIn::OnBeginShutdown` passes `true`, `OnDisconnection` passes
  `RemoveMode == ext_dm_HostShutdown`. The COM hook (`GracefulComTeardownHook(bool revokeRtdClassObject)`)
  SKIPS `CoRevokeClassObject(g_rtdCookie)` on host shutdown (revokes on add-in disable — session
  continues). **Effect, proven by diag:** with the revoke skipped Excel NOW issues `DisconnectData` on
  every live topic (TopicID 5,4,3 in the repro) — previously it issued NONE (the eager revoke blocked
  the handshake start). This confirms the Option-2 hypothesis was directionally right.
* **REVERTED — the in-OnBeginShutdown STA message pump.** A `PeekMessage`/`DispatchMessage` loop placed
  in `GracefulTeardownOnce` (after the hook, before the drains, awaiting `RtdServer::ServerTerminate`
  via `SetRtdServerTerminated`) was tried and removed. **It could not work, and the diag proves why:**
  the pump ran the full 3 s cap with `sawServerTerminate=false`, then `DisconnectData` for all 3 topics
  fired ~100 ms AFTER `GracefulTeardownOnce` returned, and `ServerTerminate` never came. Excel does NOT
  dispatch its RTD-teardown COM calls (DisconnectData/ServerTerminate) while we are still INSIDE
  `OnBeginShutdown` — it serializes: call OnBeginShutdown, wait for return, THEN run RTD teardown. So a
  pump nested inside the teardown finds an empty queue. (`SetRtdServerTerminated`/`g_rtdServerTerminated`
  are KEPT as the readiness signal the refined fix will use; the pump itself is gone.)
* **DECISIVE timeline (default ghost-check.ps1, 3 live topics, faithful UIA close):**
  `OnBeginShutdown` → `GracefulTeardownOnce(isHostShutdown=true)` → hook (revoke skipped) →
  [reverted pump ran 3 s, saw nothing] → `ReleaseCallbackForTeardown: m_callback released` →
  `GracefulTeardownOnce exit (server reaped, g_phost deleted)` → **THEN** `DisconnectData TopicID=5/4/3`
  (g_phost already null → MSG_RTD_DISCONNECT suppressed) → `DLL_PROCESS_DETACH`. **No `ServerTerminate`.
  EXCEL.EXE lingers windowless 30 s+ (ghost).** S2 clean throughout (`-KillExcelOnClose`: EXCEL & server
  both gone at t=0, both logs FREE).
* **REFINED ROOT CAUSE / NEXT (lead decision required — NOT shipped):** the destructive teardown must be
  DEFERRED OUT of `OnBeginShutdown` so the call returns fast, Excel proceeds to its RTD teardown
  (DisconnectData on each topic, then ServerTerminate) WHILE `g_phost` is still alive, and only THEN do
  the drains + `delete g_phost` + reap run. Candidate shapes (need lead sign-off; each must preserve the
  §20 cancelled-quit invariant and §23.0 g_phost-last ordering): (a) on host shutdown, have
  `OnBeginShutdown`/`OnDisconnection(HostShutdown)` set `g_isUnloading` + signal + run the COM hook
  (revoke-skip) and RETURN immediately, then complete the heavy teardown from `DLL_PROCESS_DETACH`'s
  loader-lock-safe path or a dedicated post-handshake trigger (e.g. when `g_rtdServerTerminated` is
  observed, or when the last `DisconnectData` arrives) — but DETACH cannot join/drain/delete under the
  loader lock (§20.2), so this needs a non-loader-lock completion site; (b) keep `g_phost` alive past
  `GracefulTeardownOnce` (move only `delete g_phost`/reap to a later, post-ServerTerminate point keyed
  off `g_rtdServerTerminated`) so the 3 `DisconnectData` sends actually reach the server and Excel can
  finish; (c) the authorized last-resort force-exit on the CONFIRMED host-shutdown path only (since
  OnBeginShutdown/GracefulTeardownOnce run ONLY on a real quit, a force-exit there does NOT break the
  cancelled-quit invariant). DiagLog instrumentation is **KEPT** (ghost still unresolved).
* **HIGH #2 (xlcOnTime cancel de-queue) — Stage 3 update: NOW PARTIALLY OBSERVED.** In the Stage-3 repro
  `CancelDeferredRunner: xlcOnTime cancel rc=2` logged on the confirmed-teardown path (a runner WAS
  armed at close this time, and the cancel issued). rc=2 is the xlcOnTime "clear" return; the de-queue
  proof (no subsequent `RunDeferredCalcEnd`) held (none logged post-cancel). Still worth the unit/harness
  hook below for a deterministic assertion.
* **HIGH #2 (xlcOnTime cancel de-queue) — NOT yet proven on a real armed runner.** Across all
  faithful-close runs (slow and `-FastClose`) `CancelDeferredRunner` logged nothing → the deferred
  runner was never armed at close (`g_onTimeArmed==false`): the showcase Build recalc's calc-end
  either enqueued no deferred commands/date-formats, or the runner drained its xlcOnTime before
  close. To prove the cancel actually de-queues, need a scenario that leaves a runner pending in the
  narrow CalculationEnded-armed → OnTime-dispatched window (inherently racy). Proposed proof: a unit
  /harness hook that calls `ScheduleDeferredRunner()` then `CancelDeferredRunner()` back-to-back and
  asserts `rc==xlretSuccess` + no subsequent `RunDeferredCalcEnd` dispatch, OR a C-API-level test of
  `xlcOnTime(serial, macro, missing, FALSE)` against a known-scheduled serial.

**Stage 4 (2026-06-17) — SHIPPED. Deferred destructive teardown (Phase 1 / Phase 2 split). Ghost CLEARED.**

The refined root cause from Stage 3 was correct: the teardown was too eager. Excel does NOT
dispatch its RTD teardown COM calls (`DisconnectData` per topic, then `ServerTerminate`) until
AFTER `OnBeginShutdown` returns — it serializes. The pre-Stage-4 `GracefulTeardownOnce` deleted
`g_phost` + reaped the server synchronously inside `OnBeginShutdown`, so by the time Excel issued
`DisconnectData` there was no live host, `MSG_RTD_DISCONNECT` went nowhere, `ServerTerminate` never
completed, and Excel ghosted holding its live topics. The fix splits the host-shutdown path:

* **Phase 1 (synchronous, inside `OnBeginShutdown`/`OnDisconnection(HostShutdown)`):** run only the
  fast prep — `CancelDeferredRunner`, then the COM hook with the **RTD class-object revoke SKIPPED**
  — arm a **Phase-2 watcher thread**, and RETURN FAST. Phase 1 deliberately does NOT set
  `g_isUnloading`, NOT `StopWorker`/`JoinWorker`, NOT drain, NOT `delete g_phost`, NOT reap — because
  `xll_rtd.cpp::DisconnectData` requires BOTH `g_phost` alive AND `g_isUnloading==false` to actually
  send `MSG_RTD_DISCONNECT`. RTD stays fully usable across Excel's handshake.
* **Phase 2 (deferred, `RunDestructiveTeardown`):** the watcher thread (NOT the STA, NOT the loader
  lock) waits on `g_rtdServerTerminated` (set by `RtdServer::ServerTerminate`) OR a bounded 5 s
  timeout, then runs the existing destructive sequence in the §23.0-safe order: set `g_isUnloading`,
  `SetEvent`, `StopWorker`, `JoinWorker`, monitor join, `WaitForRtdConnectDrain`,
  `ReleaseCallbackForTeardown`, `WaitForCommandDrain`, **`delete g_phost` (only AFTER the drains)**,
  `CloseHandle(hProcess/hJob/hShutdownEvent)`. A separate CAS (`g_destructiveDone`) makes Phase 2 run
  exactly once across the ServerTerminate trigger, the timeout, and the synchronous non-host-shutdown
  path.
* **Non-host-shutdown (add-in DISABLE / `ext_dm_UserClosed`) UNCHANGED:** `GracefulTeardownOnce` runs
  `RunDestructiveTeardown` synchronously (RTD class object revoked — session continues, no Excel RTD
  handshake to wait for). DLL_PROCESS_DETACH unchanged (loader-lock-safe minimum, §20.2).
* **Cancelled-quit invariant preserved (§20):** the host-shutdown deferral only runs from
  `OnBeginShutdown`/`OnDisconnection(HostShutdown)`, which fire ONLY on a CONFIRMED real quit, never
  on a cancelled quit. `xlAutoClose` stays non-destructive.

**PROVEN timeline (ghost-check.ps1 default, 3+ live RTD topics, faithful UIA close — verified
repeatedly, 4/4 clean closes once the harness actually closes the window):**
`OnBeginShutdown` → `GracefulTeardownOnce(isHostShutdown=true)` → COM hook (RTD revoke skipped) →
**Phase 1 returns fast** → Excel issues `DisconnectData TopicID=5/4/3/6` (all topics, against a LIVE
`g_phost`) → `ServerTerminate` → **Phase-2 watcher observes `g_rtdServerTerminated`** →
`RunDestructiveTeardown` (drains + `delete g_phost` + server reap) → `DLL_PROCESS_DETACH`. **`EXCEL.EXE`
EXITS within a few seconds; no windowless ghost.** S2 clean throughout (`-KillExcelOnClose`: EXCEL &
server both gone at t=0, both logs FREE).

* **Files:** `internal/assets/files/src/xll_lifecycle.cpp` (Phase 1/2 split + `g_destructiveDone` +
  `g_phase2Watcher` + `RunDestructiveTeardown`), `internal/assets/files/include/xll_lifecycle.h`
  (doc), `internal/assets/files/include/rtd/server.h` (`ServerTerminate` signals; doc),
  `internal/assets/files/src/xll_deferred_commands.cpp` (`DiagLog`→`LogInfo`). DiagLog removed from
  `xll_log.cpp/.h`, `ribbon_addin.cpp`, `xll_rtd.cpp`.
* **Regression tests (generator pinned-signature):** `internal/generator/gen_close_ghost_test.go`
  (`TestCloseGhostPhaseSplit`, `TestCloseGhostServerTerminateSignals`,
  `TestCloseGhostNoDiagInstrumentation`).
* **Harness:** `xll-gen-showcase/tools/ghost-check.ps1` gained a faithful Alt+F4 fallback for
  environments where `WindowPattern.Close()` dismisses only the workbook and leaves the app frame up
  (otherwise no shutdown signal fires and the run is inconclusive).
* **Follow-up (lower priority):** off-STA Phase-2 vs `DLL_PROCESS_DETACH` race — RESOLVED by the
  Stage-4 remediation below (the watcher is gone; Phase 2 runs on the STA from `ServerTerminate`).

**Stage 4 REMEDIATION (2026-06-17) — Phase 2 moved ONTO THE STA (watcher thread removed). C++ review NO-GO cleared.**

The Stage-4 watcher-thread trigger functioned (Excel exited, ghost gone) but a C++ review returned
NO-GO: the destructive Phase 2 ran on an OFF-STA, free-running `std::thread` (`g_phase2Watcher`),
which (a) could be terminated mid-`delete g_phost`/`CloseHandle` by the loader at process exit (or
`std::terminate` on an in-flight join) racing `DLL_PROCESS_DETACH`'s unmap [BLOCKER]; (b) touched
`g_rtdServer` / released `m_callback` off the COM apartment [HIGH UAF + apartment violation]; (c)
left a stale detachable thread across probe-unload-reuse [HIGH]. **The fix keeps every working part
(Phase-1 fast return, revoke-skip, the `RunDestructiveTeardown` body, the `g_destructiveDone` CAS,
the §23.0 ordering, the DETACH backstop) and replaces ONLY the Phase-2 trigger mechanism:**

* **Phase 2 is now triggered DIRECTLY from `RtdServer::ServerTerminate`** (`include/rtd/server.h`).
  Excel calls `ServerTerminate` ON THE STA, AFTER every `DisconnectData`, once its RTD handshake
  against a still-live `g_phost` completes — the correct, COM-apartment-safe, naturally-serialized
  point. `ServerTerminate` releases `m_callback` on the STA (its normal job, mutex scoped so it is
  not held across teardown) and then calls `xll::RunDestructiveTeardown()`. This is the SAME
  thread-class (STA) and SAME blocking profile the original synchronous teardown had inside
  `OnBeginShutdown` — just correctly TIMED. `RunDestructiveTeardown` is now declared in
  `include/xll_lifecycle.h` so `server.h` can call it.
* **`GracefulTeardownOnce` Phase 1 (host shutdown) arms NOTHING** — it runs `CancelDeferredRunner` +
  the COM hook (RTD revoke skipped) and RETURNS FAST. The `g_phase2Watcher` `std::thread`, its 5 s
  timeout loop, and the `<chrono>` include are DELETED. `g_rtdServerTerminated` is kept (set by
  `SetRtdServerTerminated`) for diagnosability/idempotence only — it is no longer polled.
* **`DLL_PROCESS_DETACH` is the UNCHANGED backstop** for the no-RTD / no-live-topic / Excel-skips
  path (where `ServerTerminate` never fires): it closes `hJob` (reaps the server via
  `KILL_ON_JOB_CLOSE`) + detaches threads, per §20.2. Verified `-FastClose`: Excel exits ~1 s, server
  reaped, logs FREE.
* **`hJob` double-close vs DETACH — RESOLVED:** `RunDestructiveTeardown` (CAS-guarded, single STA
  site) closes `hJob` and NULLs `g_procInfo.hJob`; DETACH's unconditional close is null-checked, so a
  prior ServerTerminate-driven close makes it a no-op. On a host shutdown ServerTerminate (hence
  Phase 2) completes synchronously BEFORE Excel unloads the DLL (DETACH) — the same ordering the
  pre-Stage-4 synchronous STA teardown relied on — so there is no concurrent double-close.
* **MED (Phase-1 window late `DisconnectData`):** the 5 s timeout is gone; `g_isUnloading` is latched
  only inside `RunDestructiveTeardown` (now from `ServerTerminate`, after ALL `DisconnectData`), so
  late-disconnect suppression by a too-short timer cannot occur. `xll_rtd.cpp::DisconnectData` now
  `LogDebug`s if `MSG_RTD_DISCONNECT` is ever suppressed by the `g_isUnloading`/null-`g_phost` guard.
* **Verified (real Excel, `tools/ghost-check.ps1`):** default live-RTD-topic close 3/3 clean — EXCEL
  EXITS (15–18 s), server reaped, logs FREE, no ghost. `-FastClose` clean (1 s exit). S2 not
  regressed (no orphan server, logs FREE). Native-log timeline: `OnBeginShutdown` →
  `GracefulTeardownOnce(host) Phase 1 fast return` → `DisconnectData(all topics)` → `ServerTerminate`
  → destructive teardown (subsequent logs silenced by the early `g_isUnloading` latch) → exit.
* **Files:** `internal/assets/files/src/xll_lifecycle.cpp` (remove watcher + `<chrono>`; Phase 1
  returns fast), `internal/assets/files/include/xll_lifecycle.h` (declare `RunDestructiveTeardown`),
  `internal/assets/files/include/rtd/server.h` (`ServerTerminate` drives teardown on the STA),
  `internal/assets/files/src/xll_rtd.cpp` (`DisconnectData` suppression `LogDebug`).
* **Regression tests:** `internal/generator/gen_close_ghost_test.go` updated to the
  ServerTerminate-driven shape — `TestCloseGhostPhaseSplit` now asserts `g_phase2Watcher` /
  `std::this_thread::sleep_for` / `steady_clock` are ABSENT and Phase 1 spawns no thread;
  `TestCloseGhostServerTerminateDrivesTeardown` (renamed) asserts `ServerTerminate` signals, releases
  `m_callback`, and calls `xll::RunDestructiveTeardown()` (comment-stripped so a doc-comment cannot
  mask removal), and that `xll_lifecycle.h` declares `RunDestructiveTeardown`. FAIL-before /
  PASS-after confirmed by reintroducing the watcher token and deleting the teardown call.
* **C++ asset change (not `DllMain` graceful-path logic, but touches `DllMain` DETACH reasoning) —
  re-review by `xll-cpp-reviewer` required before commit.**

**Stage 4 REMEDIATION #2 (2026-06-18) — SHIPPED v0.8.10. ServerTerminate gated on CONFIRMED host shutdown.**

The Remediation #1 trigger ("Phase 2 runs directly from `RtdServer::ServerTerminate` on the STA") rested
on a FALSE premise: that Excel delivers `ServerTerminate` **only at host shutdown**. **It does not.**
Excel calls `IRtdServer::ServerTerminate` whenever the RTD server's live-topic count drops to zero —
**including on a plain workbook close while the Excel Application stays alive** (e.g. a COM-automation
client that holds the `Application` ref, or any "close one workbook, keep Excel open" flow; this is the
same zero-topic revoke/re-register "liveness blip" noted for the RTD server lifecycle). On such a close
`OnBeginShutdown`/`GracefulTeardownOnce` NEVER fire, yet `ServerTerminate` does — so the
unconditionally-wired `RunDestructiveTeardown()` ran the FULL destructive teardown (`g_isUnloading`,
`StopWorker`/`JoinWorker`, `delete g_phost`, `CloseHandle(hJob)`=`KILL_ON_JOB_CLOSE` killing the Go
server) **while the XLL stayed loaded and Excel kept running**. The next workbook/recalc then hit a dead
server / null `g_phost` → **RPC `0x800706BA` / AV on reopen** (a fast close→reopen of a live YDH rtd-once
grid workbook crashed Excel; non-regression — present since the Remediation #1 wiring shipped in v0.8.7).

**Fix — gate the destructive trigger on a CONFIRMED host shutdown.** `ServerTerminate` ALWAYS releases
`m_callback` (its normal COM-cycle-break job, mutex-scoped), but now drives `RunDestructiveTeardown()`
ONLY when armed:

* New file-static `std::atomic<bool> g_hostShutdownTeardownArmed` in `xll_lifecycle.cpp` + exported
  accessor `xll::HostShutdownTeardownArmed()` (load-acquire). `GracefulTeardownOnce`'s `isHostShutdown`
  Phase-1 branch arms it (`store(true, release)`) **before its fast return** — the unique confirmed-
  real-quit signal. Reset to `false` in `DLL_PROCESS_ATTACH` (probe-unload-reuse symmetry, alongside
  `g_destructiveDone`/`g_rtdServerTerminated`).
* `RtdServer::ServerTerminate` (`include/rtd/server.h`): `if (xll::HostShutdownTeardownArmed())
  RunDestructiveTeardown();` else log + `return S_OK` leaving `g_phost`/server ALIVE so reopen works.
* **Real-quit path preserved unchanged:** `OnBeginShutdown` → `GracefulTeardownOnce(host)` arms → fast
  return → Excel `DisconnectData` per topic → `ServerTerminate` reads armed → teardown runs. The arm
  is sequenced before the Phase-1 return, so it is observable by the time `ServerTerminate` fires. The
  new flag is an ADDITIONAL gate; `g_teardownDone`/`g_destructiveDone` CAS guards are untouched. Add-in-
  disable path (synchronous `RunDestructiveTeardown` from `GracefulTeardownOnce`) is unaffected.
* **LESSON (do not regress):** `IRtdServer::ServerTerminate` is NOT a host-shutdown signal — it fires on
  every zero-live-topic transition. Any destructive / process-scoped teardown must be gated on a
  separate confirmed-host-shutdown signal (here `HostShutdownTeardownArmed()`), never driven by
  `ServerTerminate` alone. `DisconnectData` is likewise per-topic, not a shutdown signal.
* **Files:** `internal/assets/files/include/rtd/server.h`, `internal/assets/files/src/xll_lifecycle.cpp`,
  `internal/assets/files/include/xll_lifecycle.h`.
* **Regression test:** `internal/generator/gen_rtd_terminate_gate_test.go`
  (`TestRtdServerTerminateGatedOnHostShutdown`) — comment-stripped asserts of the gate, the arm-exactly-
  once inside the `isHostShutdown` branch before its return, the `DLL_PROCESS_ATTACH` reset, and that
  `m_callback` is still released unconditionally. FAIL-before / PASS-after confirmed.
* **Verified:** `xll-cpp-reviewer` GO (0 BLOCKER/0 HIGH). Real Excel `verify-ydp-stranding-uia.ps1`
  (`$rounds=3`) went WEAK (round-2 `0x800706BA`, Excel gone before round 3) → **PASS** (3 rounds settle,
  Excel alive). C++ asset change touching `DllMain` reasoning — `xll-cpp-reviewer` pass obtained before
  commit.

## 24. CLAUDE.md / Agent Tool Compatibility

This repository is configured so that AI tools using `CLAUDE.md` (Claude Code) read this `AGENTS.md` as the authoritative source. **All durable agent guidance must live here, not in `CLAUDE.md`.** `CLAUDE.md`, if present, must contain only a one-line redirect to this file.
