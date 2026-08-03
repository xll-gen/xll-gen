# AI Agent Instructions for xll-gen

This file is the authoritative guidance for AI agents and contributors working on `xll-gen`.

## 0. Scope & Companion Repos

`xll-gen` generates Excel XLL add-ins backed by an out-of-process Go server, communicating via shared memory + FlatBuffers. It coordinates three companion repos that each have their own `AGENTS.md`:

* **`github.com/xll-gen/shm`** — lock-free C++/Go shared-memory IPC. See its `AGENTS.md` before touching anything that crosses the IPC boundary.
* **`github.com/xll-gen/types`** — FlatBuffers protocol schema and C++ ↔ XLOPER12 converters. See its `AGENTS.md` when changing wire types.
* **`github.com/xll-gen/sugar`** — Windows COM automation in Go (xlwings-parity surface). Not in the generated runtime path; consult its `AGENTS.md` if you write tooling that drives Excel directly.

When a change crosses repo boundaries, update **all** affected `AGENTS.md` files in the same change.

## 0.1. Platform Support (HARD CONSTRAINT)

`xll-gen` is **Windows-only** and targets **x86-64 (amd64/"x64", Intel/AMD)** exclusively. This is not a "primary focus" — it is a hard constraint:

* **OS**: Microsoft Windows. No Linux, no macOS, no WSL as a runtime target.
* **CPU**: x86-64 (64-bit, "x64") only. **32-bit x86 is NOT supported** (the `shm` Go guest's Win32 struct layouts are amd64-only and reject `windows/386` at compile time — see `shm/AGENTS.md` §"Platform Targets"). **No ARM (incl. Windows-on-ARM, Apple Silicon).**
* **Excel**: Only **64-bit Excel** is supported; a generated XLL is 64-bit and matches 64-bit Excel. **32-bit Excel is not supported** — there is no 32-bit XLL/server build, so a 32-bit Excel host cannot load a generated add-in.
* **Memory model assumption**: x64 (amd64) provides Total Store Order (TSO). Implementations and reviews MAY rely on TSO guarantees — sequential consistency of acquire-release pairs is hardware-provided. ARM weak-memory-model concerns are out of scope for the xll-gen runtime path.

**Implications for agents and reviewers**:

* Findings phrased as "ARM-only bug" or "weak memory model concern" against xll-gen runtime code are **non-issues** unless they also affect x64 (rare).
* Cross-platform build infra (Linux CI for Go-only unit tests, etc.) is acceptable as a developer convenience but is NOT a supported deployment target.
* Companion repos have different platform stories: `shm` is Windows/amd64-only (its former Linux backend was removed — see shm AGENTS.md, which marks it OBSOLETE; GOARCH=386 is rejected at compile time since shm v0.8.10); `sugar` is Windows-only (COM-bound); `types` Go code is portable but its C++ side targets Windows + the SDK.

When in doubt about whether a concern applies, ask: "Does this affect Windows x64 (amd64) with stock MSVC/MinGW + recent 64-bit Excel?" If no → out of scope for xll-gen.

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

**`src/`, `include/` and `tools/` are pruned before they are rewritten** (`generator.pruneGeneratedCpp`, driven by `generatedCppSubdirs`). This is not tidiness: `CMakeLists.txt.tmpl` compiles `file(GLOB "${CMAKE_CURRENT_SOURCE_DIR}/src/*.cpp")`, so a `.cpp` left behind by an older xll-gen is still **compiled and linked** — typically producing duplicate-symbol errors, or silently linking the old implementation, in a tree the user never edited. Conditional emission has the same shape (turning `ribbon.enabled` off used to leave the previous run's `ribbon_xml.h` in `include/`). Deleting these three is safe because every file in them is regenerated by the same call and none is user-authored: the generated package directory is gitignored wholesale (`{{.Package}}/` in `gitignore.tmpl`) and the CMake build tree is `build/cpp`, outside it. **If you ever emit a generated C++ file OUTSIDE these three subdirectories, add its directory to `generatedCppSubdirs`** — otherwise deletions of that asset become undetectable stale-file bugs. Nothing else under `generated/cpp/` is touched (`xll_main.cpp` and `CMakeLists.txt` are overwritten in place); `gen_prune_test.go` pins both the blast radius and the wiring.

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
5.  **Self**: `go.mod` of the `xll-gen` repository itself (for regression testing and tool stability). **Gated since 2026-08-03 by `cmd/doctor_version_test.go::TestGoModPinsMatchVersions`** — it was the only pin location on this list with no automated check, and it had silently drifted: `versions.go` said shm v0.8.20 while `go.mod` said v0.8.18, because the shm v0.8.19/v0.8.20 releases bumped the C++ pin and left the Go one behind (they had moved in lockstep up to v0.8.18). Generated projects were unaffected — MVS picks the higher version — but `pkg/server` and `pkg/rtd`, the two packages that actually import `shm/go`, were being **built and tested against the older shm**, so a Go-API regression in v0.8.19/v0.8.20 could not have been caught here. A stale pin in a test-only position is still a hole in what the tests are for. (sugar is deliberately not checked: `versions.go` carries no sugar pin — it feeds C++ FetchContent tags and sugar is Go-only — so `go.mod` is its single source of truth.)
6.  **Regtest static fixture**: `internal/regtest/testdata/CMakeLists.txt` hardcodes `GIT_TAG` for `flatbuffers`, `shm`, and `types`. It is a `go:embed` STATIC asset (the mock-host build), NOT templated from `versions.go`, so these three pins are hand-maintained. `cmd/doctor_version_test.go` (`TestRegtestCMakePinsMatchVersions`) fails CI if any drifts from `internal/versions/versions.go`. (The `XLLGEN_TYPES_SRC`/`XLLGEN_SHM_SRC` env overrides redirect only the source dir; the pinned tags here are otherwise authoritative for that fixture.)
7.  **The tool's own version**: `version/version.go`'s `Version` const must equal the release tag it ships under. It is stamped into the `Code generated by xll-gen <ver>.` banner of every generated file, so a stale const gives every downstream project false provenance — `Taskfile.yml`, `server.go` and the C++ tree claim a generator that did not produce them. **This step has no natural tripwire:** `internal/generator/golden_test.go` deliberately injects `goldenVersion = "golden"` so goldens do not churn on a bump, which is correct for the goldens and precisely why the bump gets forgotten. It was in fact forgotten for **v0.8.39** (const left at `v0.8.38`; caught only when a showcase regeneration was about to stamp the wrong banner, and corrected in v0.8.40 — the published tag was not moved). `cmd/version_tag_test.go` (`TestVersionConstIsNotBehindTheNewestTag`) now fails when the const falls behind the newest `v*` tag; it skips where git or tags are unavailable, so it is a backstop, not a substitute for bumping in the release commit.
8.  **doctor's CMake floor**: `cmd/doctor.go`'s `minCMakeVersion` must track `cmake_minimum_required(VERSION ...)` in `internal/templates/CMakeLists.txt.tmpl`. `doctor`'s contract is to block BEFORE the build does; a lower floor here reports a green checkmark and then hands the user CMake's own hard error at configure time, without the remediation hint xll-gen authored for that case. `cmd/doctor_version_test.go` (`TestDoctorCMakeMinMatchesTemplate`) fails CI on drift. Gating HIGHER than the template is allowed (a generator feature may need a newer CMake before the template is bumped); lower is not.

### 18.2.1 The XLL worker PARKS, and that is a teardown contract (2026-08-03)

`xll::WorkerLoop` blocks in `shm::DirectHost::WaitForGuestCall(kWorkerParkMs)` instead of
spinning. It burned a full core for the life of the add-in before this.

**Do NOT substitute a sleep.** `hostState` on the guest slots is published ONLY from
inside shm's wait, so an XLL that does not call it leaves that field
`HOST_STATE_ACTIVE` forever and the Go sender's `hostState == HOST_STATE_WAITING`
doorbell never fires. A hand-rolled wait would therefore expire on its own timeout on
every single guest call — the old spin was not merely wasteful, it was the only thing
holding the ~150 ns RTT. Requires shm ≥ v0.8.21.

**Two teardown invariants, both pinned by**
`internal/assets/worker_park_cpp_test.go::TestWorkerLoopParksAndIsWakeable`:

1. `StopWorker()` **signals** the parked worker (`WakeGuestCallWaiter`), it does not just
   clear `g_workerRunning` — a flag store is invisible to a thread already blocked in the
   OS wait. Structural twin of `BeginQuiesce` signalling `hShutdownEvent` for the monitor
   thread. Guarded on `g_phost`, because StopWorker runs on paths where the host is gone.
2. `kWorkerParkMs` (100) is **strictly less than** `kThreadReapBudgetMs` (500), so even a
   MISSED wake resolves inside Phase 1's bounded reap. If it did not, the reap would
   DETACH a thread still executing inside the XLL image, and on the add-in-disable path
   that forces `PinModuleToPreventUnmap` — the XLL stays mapped for the rest of the
   session, which is also the dev-mode "rebuild with Excel open" breakage.

Neither cover is sufficient alone. The test reads both constants out of the sources, so
bumping either cannot silently invert the relationship.

**Phase 2 is still non-parking** and the §20.2.1 co-change anchor in `xll_lifecycle.cpp`
now says why the park does not change that: this XLL still never calls
`DirectHost::Start()`, so `GuestCallWorker::Stop()`'s wake+join stays inside its
`guestWorkerRunning` guard and remains a no-op; and `delete g_phost` is gated on
`g_backgroundThreadsReaped`, so a worker still parked on an event that lives inside
`g_phost` can never have that memory freed under it — it is leaked instead.

Side effect to know about: `CleanupStaleChunks`' 10 s cadence can now fire up to
`kWorkerParkMs` late. Harmless against a 60 s TTL, but the loop no longer spins the
clock check, so it is no longer sub-millisecond punctual.

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
**Add functions APPEND-ONLY.** `mock_host.cpp` hardcodes each function's message ID as a literal (`(shm::MsgType)140`, `141`, …) and the generator assigns `MsgUserStart + index` (§18.6), so inserting a function anywhere but the END silently shifts every later ID and every mock-host case after it starts talking to the wrong handler. The compile still succeeds; only the assertions fail, and they fail confusingly. (Followed for `ErrEmptyString`/`ErrEmptyInt` at 153/154, added 2026-07-29.)

### 18.6 Message ID Allocation
Message IDs are distributed across multiple definitions and must match exactly.
1.  **Definitions**: the Go-side single source of truth is the leaf package `pkg/msgid` (e.g., `MsgUserStart = 140`, `MsgCalculationEnded = 131`, `MsgRtdConnect = 133`); `pkg/server/types.go` re-exports them as aliases (`MsgRtdUpdate = msgid.MsgRtdUpdate`, etc.) so all `server.Msg*` references — including generated code — keep compiling, and `pkg/rtd` imports `pkg/msgid` directly (no shadow copy). The C++ mirror is `internal/assets/files/include/xll_ipc.h` (the `MSG_*` #defines, e.g. `MSG_USER_START = 140`). `pkg/msgid/msgid_test.go` pins the numeric values. The Go side (`pkg/msgid`) and the C++ side (`xll_ipc.h`) must match exactly.
2.  **Generator (C++)**: `internal/templates/xll_main.cpp.tmpl` manually calculates user IDs (`140 + $i`).
3.  **Generator (Go)**: `internal/templates/server.go.tmpl` manually calculates user IDs (`140 + $i`).
4.  **Events**: `internal/generator/funcmap.go` hardcodes event IDs (e.g., `"131"` for `CalculationEnded`).
**Constraint**: If `MSG_USER_START` changes in `xll_ipc.h`, both templates, `pkg/server`, and `mock_host.cpp` must be updated.

#### 18.6.1 Chunk reassembly — two independent reassemblers, and their deliberate asymmetries

The chunk co-change pair (`internal/assets/files/src/xll_worker.cpp` ↔ `pkg/server/manager.go` + `pkg/server/handlers.go`) is a **mirror of rules, not a shared mechanism**. They are two reassemblers running in **opposite directions**, and the word "mirrors" in either file's comments must be read that way:

* **Go (`ChunkManager` / `HandleChunk`)** reassembles **host → guest** chunks: what the C++ XLL sends to the Go server.
* **C++ (`xll::ChunkRegistry` / `HandleChunk`)** reassembles **guest → host** chunks: async batch responses and rtd-once grid results arriving at the XLL.

**The segment arbiter is extracted and unit-gated (2026-07-26).** The C++ side's bounds check + zero-length refusal + overlap classification live in `xll::ClaimChunkSegment` — a **pure inline function** in `internal/assets/files/include/xll_worker.h`, not in `xll_worker.cpp`. That is deliberate: `xll_worker.cpp` cannot be linked outside the XLL (its completion path reaches `xlAsyncReturn`, COM `IRTDUpdateEvent` and the shm host), so before the extraction the C++ mirror had **no automated test at all** — the cmake gates only proved it compiles, and regtest's `mock_host` cases 16a–17e exercise the **Go** guest. The arbiter now has the same offline g++ gate `xll_cache.cpp` has: `internal/assets/testdata/chunk_segments_native_test.cpp`, driven by `internal/assets/chunk_cpp_test.go`. That Go file owns **one** case table which it replays against BOTH `pkg/server`'s `ClaimSegment` (plus HandleChunk's two caller-side guards) and, by emitting it as C++, against `ClaimChunkSegment` — so "same rules" is checked, not asserted in prose. **Do not re-inline the classification into `xll_worker.cpp`** (`TestChunkSegmentLogicIsExtracted` fails if you do: the offline gate would keep passing while the shipped reassembler ran untested code), and **add every new accept/reject rule to that shared table**, never to one side's tests alone.

**The transfer bookkeeping is extracted and unit-gated (2026-07-26).** The layer AROUND the arbiter — which transfers may be opened (`total_size == 0`, the `kMaxChunkTotalSize` cap, the `kMaxPartialMessages` bound with its **prune-then-refuse** reclaim) and how a refused one is remembered (the poison set: refuse-until-TTL, expiry, `kMaxPoisonedTransfers`, oldest-eviction) — now lives in `xll::ChunkRegistry`, a **pure class** in `xll_worker.h`, for the same reason `ClaimChunkSegment` does. `xll_worker.cpp` keeps only what cannot leave it: the FlatBuffers accessors, the `LogWarn` texts, the `memcpy` and the completion dispatch. Two properties make the class testable: **the caps are constructor parameters** (so the gate drives every bound with three-entry maps instead of 1024) and **`now` is an argument** (so TTL boundaries are exercised exactly, with no sleeping). Gate: `internal/assets/testdata/chunk_registry_native_test.cpp` + a SECOND shared table in `internal/assets/chunk_cpp_test.go` (`chunkRegistryCases`), replayed against Go's `ChunkManager` and emitted as C++ — same one-table discipline as the segment gate. `TestChunkRegistryLogicIsExtracted` fails if the maps are re-inlined. Supporting Go-side additions, all mirrors of something the C++ class already had: sentinel errors (`ErrChunkTotalNonPositive` / `ErrChunkTotalTooLarge` / `ErrTooManyChunkTransfers`) so the refusal REASON is matched structurally instead of by message text, `ChunkManagerConfig.Clock` (test-only injectable clock; production leaves it nil), `MaxPoisonedTransfers`, and `Sweep` / `TransferCount` / `PoisonedCount`.

**Tunability is one-sided, and "the two constants are in lockstep" is the WRONG mental model.** `xll.yaml` `server.chunk` (`max_buffer_bytes`, `max_concurrent_transfers`, `cleanup_interval`, `buffer_ttl`) configures the **Go/host→guest** side only. The C++ twins (`kMaxChunkTotalSize`, `kMaxPartialMessages`, `kMaxPoisonedTransfers`, `kChunkStaleTtl`, all in `xll_worker.h`) are **compile-time constants with no template or YAML wiring**. Wiring the C++ side through the template is a possible future change, not a current guarantee. There are **three** 256 MiB numbers and they play different roles — say which one you mean:

| number | direction | role | tunable? |
| --- | --- | --- | --- |
| `server.DefaultMaxChunkBufferBytes` | host→guest | Go RECEIVER cap | yes — `server.chunk.max_buffer_bytes` |
| `kMaxChunkTotalSize` (`xll_worker.h`) | guest→host | C++ RECEIVER cap | **no** |
| `chunk.MaxTransferBytes` (`pkg/chunk`) | guest→host | Go SENDER cap | no (must track `kMaxChunkTotalSize`) |

Only the **bottom two** have to be equal for correctness, and they are equal by **hand-copied constant**, because a sender cannot discover the receiver's cap: nothing on the wire negotiates a reassembly limit, and `kMaxChunkTotalSize` has no template/YAML exposure. `internal/assets`' `TestChunkReceiverCapsMatchGoConstants` reads the number out of the shipped header so the copy cannot drift silently. A project that lowers `server.chunk.max_buffer_bytes` is asymmetric by construction **and** re-opens the same defect in the opposite direction: the C++ host→guest sender (`xll_ipc.h`) has no knowledge of the lowered Go cap and will push a transfer this server refuses. That gap is **open** — see §23.3.

**Cap semantics — the two caps MULTIPLY.** `MaxChunkBufferBytes`/`kMaxChunkTotalSize` caps ONE transfer; `MaxConcurrentTransfers`/`kMaxPartialMessages` caps the transfer COUNT. Neither is an aggregate-byte guard, and their combination is not one either: worst-case resident footprint is `MaxBufferBytes × MaxConcurrentTransfers` = **256 GiB at the defaults**. The TTL sweep does not rescue that — a peer that pushes one chunk per transfer inside `ChunkBufferTTL` keeps every buffer's `LastAccess` fresh, so nothing is prunable. A real byte ceiling requires lowering `max_buffer_bytes`. Do not describe the count bound as an "aggregate guard" (that wording was corrected 2026-07-26 in `manager.go`, `xll_worker.cpp` and `README.md`).

**Intentional asymmetry — total-size mismatch on transfer-id reuse.** Go's `GetChunkBuffer`, on finding a live buffer whose `TotalSize` differs from the arriving chunk's declared total, logs a warning and **replaces the buffer in place**, letting the re-opened transfer proceed. The C++ side has **no such reset**: `PartialMessage` keeps the FIRST `totalSize` for the life of the entry, so the re-open's chunks are bounds-checked against the stale total, hit the out-of-bounds path, and the transfer is **discarded and poisoned**. Same wire sequence, opposite outcome — recovered by the guest, refused by the host. This divergence is **known and deliberately left as-is**; symmetrizing it means first deciding which behavior is the contract (the C++ refusal is the stricter reading; the Go reset is the more forgiving one for a producer restart) and is a separate change. **Never align one side in isolation.**

**Second-order asymmetry from the same family — cap validation on an ALREADY-OPEN id (identified 2026-07-26, left as-is).** Go's `GetChunkBuffer` validates `total` **before** looking the id up, so a chunk that declares `total_size == 0` (or a total over `MaxChunkBufferBytes`) for a transfer that is already open is **refused** with `MsgTypeSystemError`; the C++ `Admit` returns `Existing` without re-validating, because the first total wins for the life of the entry. Only reachable from a producer that changes `total_size` mid-transfer — i.e. the same malformed-producer class as the reset divergence above, and it resolves the same way (decide which side is the contract, change both). It is deliberately **NOT** in the shared registry case table; each side pins its own behavior instead (`TestChunkManager_TotalMismatchResetsBuffer`, `TestExistingEntryKeepsTheFirstTotal`).

**TOCTOU between `chunkMutex` and `buf.Mutex` is benign (documented, not a bug).** `GetChunkBuffer` releases `chunkMutex` before `HandleChunk` takes `buf.Mutex`, so a concurrent TTL sweep or rejection can unlink the buffer this call still writes into. That wastes a write and can make two goroutines report different outcomes for the same transfer, but it cannot corrupt data: a rejected segment is never copied, and the segments that DID land are disjoint and in-bounds by the `ClaimSegment` contract, so `Received == TotalSize` still implies exact coverage of `[0, TotalSize)`. Do NOT widen the locking to "fix" it — holding `chunkMutex` across `buf.Mutex` serializes every concurrent transfer and creates a lock-ordering hazard with `PoisonTransfer`.

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
4.  **C++ assets**: `internal/assets/files/include/com/*` + `src/ribbon_addin.cpp` (the `RibbonAddIn` COM class — `IDTExtensibility2` + `IRibbonExtensibility` + `IDispatch`), `src/ribbon_connect.cpp` (the COM add-in connect bootstrap — see §18.11.1) and `src/scratch_book.cpp` (the temp-workbook bounce helpers).
5.  **Generator**: `internal/generator/gen_cpp.go` emits `ribbon_xml.h` (the embedded ribbon XML literal).

#### 18.11.1 The connect machinery is a STATIC ASSET, not template code (2026-08-02)

`SetRibbonConnected`, `TryConnectRibbon`, the `0/1/2` state gate
(`g_ribbonConnectState` / `g_ribbonRegistered`), the `s_inConnect` STA
re-entrancy guard, the 60-attempt give-up cap, the `RibbonAttempt` outcome class
and the two `xlcOnTime` retry budgets + their counters live in
**`internal/assets/files/include/com/ribbon_connect.h` +
`src/ribbon_connect.cpp`**. Everything §18.11 / §20.3 / §23.6 "§3" says about
their BEHAVIOR still holds verbatim — the 2026-08-02 change was a pure
relocation, no semantics touched — but any prose below that names
`xll_main.cpp.tmpl` as the *file* is stale for these symbols. Same for
`GetActiveWorkbookName` / `ScratchCloseEventSuppressor` /
`RestorePendingEventSuppression`, which moved to `include/com/scratch_book.h` +
`src/scratch_book.cpp` in the same pass.

**Why:** none of it contained template variables, so every generated project was
receiving a byte-identical re-emitted copy that only a golden-string grep could
test. Standing project direction: keep generated templates minimal, prefer static
code (Go → `pkg/server`, C++ → `internal/assets/files/{include,src}`).

**What STAYS generated in `xll_main.cpp.tmpl`, and must:**
* the COM identity — `GetRibbonClsid()`, `g_szRibbonProgID`, `g_ribbonCookie`;
* the project-named registration strings (`L"<Project> Ribbon"` etc.);
* `GetExcelApplicationOrBounce()` — its body is `ribbon.bounce`-branched
  (`full` / `keep-open` / `off`), i.e. real generated code;
* the exported `__xllgen_RibbonConnectRetry()` OnTime macro **symbol** and its
  `xlfRegister`, because Excel resolves the registered procedure by NAME against
  an exported symbol, which cannot live in an asset. That is also why the retry
  budgets/counters are `extern` in the header rather than private to the `.cpp`.
  Its **body** is NOT generated — see §18.11.2;
* the `xlAutoOpen` **call** to `xll::ribbon::ArmConnectRetry()`. The arm must be
  issued from a VALID command context and `xlAutoOpen` is the one the first
  `xlcOnTime` gets (§23.6 HIGH #2, proven on real Excel with zero workbooks open);
  the arm's LOGIC is not generated — again §18.11.2.

**The wiring contract:** the asset reads NO generated symbol. `xlAutoOpen` fills
an `xll::ribbon::ConnectContext` (module handle, ProgID, CLSID, the three
registration strings, `kXllRibbonXml`, `&GetXllRibbonImages`,
`&GetExcelApplication`, `&GetExcelApplicationOrBounce`, `&g_ribbonCookie`) and
publishes it ONCE via `xll::ribbon::SetConnectContext`, **before**
`xll::SetGracefulTeardownHook` (whose hook calls
`xll::ribbon::SetRibbonConnected(false)`, which needs `acquireApp`) and before
the first `TryConnectRibbon`. `getImages` is a function POINTER on purpose so the
embedded icons are still built lazily, at most once, inside the registration
block. Adding a `ConnectContext` field without wiring it in the template fails
`internal/generator/gen_ribbon_connect_test.go::TestXllMainPublishesRibbonConnectContext`
(the field list is derived from the shipped header, not restated).

**`g_ribbonCookie` stays in the TEMPLATE** and the asset writes it through
`ConnectContext::pClassObjectCookie`. Deliberate: it keeps
`GracefulComTeardownHook` **byte-identical**, and that hook's statement order is
the fix for a 100%-reproducible `mso.dll` NULL-vtable crash (see the
Confirmed-Correct Decisions entry "Office add-in disconnect re-entrancy"). Two
crash releases came out of that path; a relocation must not perturb it, not even
to route two statements through accessors. Full rationale in the "COOKIE
OWNERSHIP" comment in `com/ribbon_connect.h`.

**Test split.** BODY invariants (signatures/defaults, the four `*pOutcome`
classification sites AND their order, the re-entrancy guard, the state gate, the
60-cap, every log string, the five budget constants and their VALUES, the absence
of the removed STA `WM_TIMER` residual) are pinned against the embedded asset in
`internal/assets/ribbon_connect_cpp_test.go` (and
`scratch_book_cpp_test.go`). WIRING (the include, the published context and its
ordering, the three trigger sites, the export and arm CALL sites) stays in
`internal/generator/gen_ribbon_connect_test.go`, which also carries a
**do-not-re-inline** guard in the spirit of §18.6.1's
`TestChunkSegmentLogicIsExtracted`: a re-inlined copy in the template would
shadow the asset, leave the asset tests green, and put untested code back in the
shipped XLL.

**Corollary for the `com/scratch_book.h` include:** it is gated
`{{if and .Ribbon.Enabled (eq .Ribbon.BounceMode "full")}}`. `BounceMode()` maps
an unset `ribbon.bounce` to `"full"` **regardless of `Ribbon.Enabled`**, so
gating on the bounce mode alone leaked the include into every non-ribbon project.

#### 18.11.2 The OnTime retry CHAIN is a STATIC ASSET too; only the exported symbol is generated (2026-08-03)

Second pass over the same cluster. `__xllgen_RibbonConnectRetry`'s BODY — the
teardown self-abort, the connect attempt, the state gate, the two-budget outcome
switch and the re-arm decision — and the `xlAutoOpen` ARM block — the unload gate,
the "only if still unconnected" gate, the `g_ribbonRetryArmed` start-once CAS and
the rejected-arm handling — had no template variables either. They are now
**`xll::ribbon::RunConnectRetryTick()`** and **`xll::ribbon::ArmConnectRetry()`**
in `include/com/ribbon_connect.h` + `src/ribbon_connect.cpp`. Everything §23.6
"§3" and its two FOLLOW-UPs say about their BEHAVIOR still holds verbatim — pure
relocation, no semantics touched, every log string and every budget number
preserved.

**What must stay in the template, and exactly why:**
* the exported symbol `__xllgen_RibbonConnectRetry()` — Excel resolves the ON.TIME
  procedure BY NAME against an exported entry point (§21). The shim is now the
  same three lines `__xllgen_RunDeferredCalcEnd` has: `XLL_SAFE_BLOCK_BEGIN` → one
  call → `XLL_SAFE_BLOCK_END(0)` → `return 1;`;
* the `xlfRegister` (`macroType=2`) of that name via
  `xll::RibbonConnectRetryMacroName()`;
* the `xlAutoOpen` CALL to `ArmConnectRetry()`, for the command-context reason
  given in §18.11.1.

**The SEH boundary stays at the caller**, and moving the bodies out of those
`__try` scopes is a strict improvement independent of the relocation: a
function-local static or a `std::string` temporary inside a `__try` is exactly the
hidden non-trivial construction the `XLL_SAFE_BLOCK` discipline avoids (half of
what MED #2(d) was about).

**Test split.** Body → `internal/assets/ribbon_connect_cpp_test.go`
(`TestRibbonConnectRetryTickGates`, `TestRibbonConnectRetryTickBudgetAccounting`,
`TestRibbonConnectRetryTickReArm`, `TestRibbonConnectArmIsStartOnceAndInspectsRc`).
Wiring → `internal/generator/gen_ribbon_connect_test.go`
(`TestXllMainRibbonOnTimeConnectRetry`, which now also asserts the shim contains
NOTHING but the call, and `TestXllMainRibbonRetryArmSiteIsWiredFromXlAutoOpen`,
which pins that the arm sits inside `xlAutoOpen` and after the load-time connect).
`TestXllMainRibbonRetryNoAppBudget` is RETIRED IN PLACE — its assertions moved
unweakened into the accounting test, which additionally proves the uncharged
branch charges nothing; the doc comment stays so the empty-Excel scenario is still
written down next to the wiring. `cmd/cpp_compile_gate_bounce_test.go`
(full/keep-open/off) compiles the result.

#### 18.11.3 `rtd.throttle_interval` is a STATIC ASSET; the millisecond value is a PARAMETER (2026-08-03)

`SetRtdThrottleInterval`, the `g_rtdThrottleState` 0/1/2 gate and the 10-attempt
bound live in **`internal/assets/files/{include/com/rtd_throttle.h,
src/rtd_throttle.cpp}`** as `xll::throttle::TryApplyRtdThrottle(long ms, const
char* phase)`. The block's ONLY template variable was
`parseTimeout .Rtd.ThrottleInterval 2000`, used twice (the call argument and the
log string); it is now the `ms` parameter, and the INFO line rebuilds the same text
with `std::to_string(ms)` (`parseTimeout` yields an int and `config.Validate`
bounds it to `0..MaxInt32`, so the rendered decimal is unchanged).

**Namespace `xll::throttle`, deliberately NOT `xll::rtd`:** `xll_main.cpp` does
`using namespace xll;` and then calls the TOP-LEVEL `rtd::` namespace
(`rtd::RegisterServer`, `rtd::ClassFactory`, `rtd::GlobalModule`). An `xll::rtd`
would make every one of those lookups ambiguous in that TU.

**Gating:** `src/rtd_throttle.cpp` is `#ifdef XLL_RTD_ENABLED` (CMake defines it
project-wide for RTD builds) because `file(GLOB src/*.cpp)` sweeps it into every
project; the header `#error`s without the macro so the failure lands at the include
rather than as an unresolved symbol at link. An RTD project with no
`rtd.throttle_interval` compiles the TU and never calls it.

**Template shrinkage this unlocked** (and why its negative assertions are worth
keeping): the applier acquires the Application itself through the header-only
`com/excel_app.h`, so a throttle-only (ribbon-off) project no longer emits the
template's `GetExcelApplication()` wrapper, `#include <oleacc.h>`,
`#include "com/excel_app.h"` or `#include "com/dispatch_helpers.h"` at all. Those
include gates went from `{{if or .Ribbon.Enabled (and .Rtd.Enabled
.Rtd.ThrottleInterval)}}` to `{{if .Ribbon.Enabled}}` / `{{if .Commands}}`. The
CMake `oleacc` link is unaffected — it has been unconditional since the always-on
date auto-format module started using the same route (§18.12 / `xll_date_format.cpp`).

**Test split.** Body → `internal/assets/rtd_throttle_cpp_test.go` (header contract,
the RTD gate ordering, the COM property path, the state gate + bound + both log
strings — none of which had any coverage while it was template text). Wiring →
`internal/generator/gen_cpp_test.go::TestGenCpp_RtdThrottle` (the include, the
three call sites, that the configured 250ms reaches the argument, the
do-not-re-inline guard, and the ribbon-off shrinkage above).
**`cppCompileGateYaml` gained `rtd.throttle_interval`** — no gate compiled the
throttle call sites before, so the signature change would have had nothing but
string greps behind it.

#### 18.11.4 `FormatDoubleRoundTrip` → `include/xll_topic.h` (2026-08-03)

Same pass, smallest piece. The `%.17g` RTD topic formatter had no template
variables and is now a header-only `inline` in `namespace xll`, so the generated
wrappers keep calling it unqualified through their `using namespace xll;`. The
header is **NOT** `XLL_RTD_ENABLED`-gated and the generated TU includes it
unconditionally: it pulls in nothing but `<cwchar>`/`<string>`, and a config built
directly in a generator test can declare an rtd function without `rtd.enabled`
(validation is skipped there), which the compile gates render from.
`#include <cwchar>` left the template with it. Body →
`internal/assets/topic_cpp_test.go`; the four per-argument CALL SITES and the
lossy-`std::to_wstring` ban stay in
`internal/generator/gen_escape_precision_test.go`, which gained the include
assertion and a do-not-re-inline guard.

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

#### 18.11.5 The ribbon's HKCU registration has an INSTALLED lifetime, not a SESSION one (2026-08-03)

**The question this answers** (backlog line 120): *after a COM add-in disable, does
the entry come back in the COM Add-ins UI list?* **No — and it was decidable from
the code, without Excel.**

`HKCU\Software\Microsoft\Office\Excel\Addins\<progId>` **IS** the row source of
File ▸ Options ▸ Add-Ins ▸ COM Add-Ins. `GracefulComTeardownHook` used to
`RegDeleteTreeW` that key (`rtd::UnregisterOfficeAddinKey`) **and** the whole COM
registration (`rtd::UnregisterServer` → `HKCU\Software\Classes\<progId>` +
`CLSID\{…}` incl. `InprocServer32`) on **every** confirmed teardown — including the
add-in-DISABLE teardown that the dialog's own untick drives. So unticking the box
deleted the row the user needs to tick it back on, and even a row surviving from
another hive had nothing left to activate. The older observation "`COMAddIn
'…Ribbon'` vanished from the collection" and "the dialog row is gone" were the
SAME `RegDeleteTreeW`.

**The change.** Both calls are gone from the hook; they now live in
`DllUnregisterServer` — the documented uninstall entry point — which is emitted for
a **ribbon** project as well as an RTD one (it used to be `{{if .Rtd.Enabled}}`
only). `LoadBehavior` stays **0** in `RegisterOfficeAddinKey`, so the surviving key
autoloads nothing at Excel startup; it is inert until something connects it.

There is deliberately **no ribbon half of `DllRegisterServer`**: the ribbon
self-registers at `xlAutoOpen` inside `TryConnectRibbon`, whose friendly-name /
description literals are published through `xll::ribbon::ConnectContext`, and
duplicating them in the template would be a drift hazard for no gain.

**Everything above the removed lines in the hook is untouched** — the
`OfficeDisconnectInProgress()` skip / `SetRibbonConnected(false)` / 
`CoRevokeClassObject(g_ribbonCookie)` block is the 2026-07-30 `mso.dll`
NULL-vtable crash fix (Confirmed-Correct Decisions, "Office add-in disconnect
re-entrancy") and its ORDER is load-bearing. No change to the PIN, the quiesce
latch, `g_officeDisconnectDepth`, the CAS guards or `DllMain`.

**THE HAZARD THIS OPENS, and its guard.** A surviving dialog row plus a live
`InprocServer32` means Office can COM-activate `RibbonAddIn` in a session where
`xlAutoOpen` **never ran** — no ribbon XML, no images, no SHM host, no Go server.
`RibbonAddIn::OnConnection` was a bare `return S_OK`, which would produce a ribbon
tab whose every button silently does nothing. It now consults
`xll::ribbon::ConnectContextPublished()` (a thin wrapper over the existing
`ContextReady`, so there is no second, drifting field list) and returns **`E_FAIL`**
with one `SAFE_LOG_WARN`. The late-bound `IDispatch` path in `RibbonAddIn::Invoke`
routes the `OnConnection` DISPID to that same body — a blanket `S_OK` there would
let a host that late-binds `_IDTExtensibility2` bypass the refusal entirely.

**ACCEPTED COST (state it in release notes).** An uninstalled or moved XLL leaves a
dialog row behind until `DllUnregisterServer` runs, and xll-gen ships no installer
that calls it — so in practice the row is permanent per project ProgID. Guard above
is what keeps a ticked stale row from producing a half-initialised add-in.

**LARGER ISSUE THIS EXPOSES BUT DOES NOT FIX — do not inherit it silently.**
Unticking the box drives `OnDisconnection(ext_dm_UserClosed)` →
`GracefulTeardownOnce(false)` → the **full destructive Phase 2** (`g_phost` deleted,
Go server killed via `KILL_ON_JOB_CLOSE`) while the XLL stays **LOADED** and its
UDFs stay **REGISTERED**. Every UDF then hits the `g_phost == nullptr` guard and
returns `#VALUE!` for the rest of the session: the user turned off a ribbon and lost
the whole add-in. Same root as §20.3 point 3 (a "confirmed shutdown" signal that is
not one); the visibility patch there is what surfaces this state.

Pinned by `internal/generator/gen_addin_key_lifetime_test.go` (hook must NOT
unregister, `DllUnregisterServer` must, for ribbon-with and ribbon-without RTD) and
`internal/assets/office_disconnect_guard_cpp_test.go::TestOnConnectionRefusesWithoutConnectContext`
(the refusal is a BRANCH-structure assert, not a substring one). Compiled by
`cmd/cpp_compile_gate_bounce_test.go`, which is a ribbon-**without**-RTD project and
therefore builds exactly the new `DllUnregisterServer` shape.

**STILL OPEN, needs real Excel** (the code answer above does not cover it): whether
an ALREADY-OPEN COM Add-ins dialog re-reads the hive, i.e. dialog REFRESH semantics.
And note the corollary that re-scopes the v0.8.42 A/B: with both keys deleted,
`COMAddIns.Item(progId)` could not resolve, so the recorded "re-enabling comes back
healthy (`kResetAfterTeardown`)" cannot have gone through the dialog /`COMAddIns`
path — it measured the **XLL registration** path (RegisterXLL / `AddIns.Installed`),
where `TryConnectRibbon` recreates both keys. The UI path was untested and, per the
code, broken. Do NOT use `AddIns.Installed = $false/$true` as the round-trip in a
harness: it is pinned-empty (`GracefulTeardownOnce` +0, `xlAutoClose` +0 — the XLL
never unloads).

#### 18.11.6 The connect failure is CLASSIFIED, and the classification is the deliverable (2026-08-03)

Backlog line 121 reports "ribbon COM connect fails sporadically (~3/20 starts)" and
asks whether it is fixable in code. **It was unknowable from the repo, and that was
the fixable defect.** `SetRibbonConnected` collapsed THREE distinct failures into
one bare `false` and discarded every HRESULT:

| class | meaning | is it a refusal? |
|---|---|---|
| `kNoComAddInsProperty` | `Application.COMAddIns` unreadable / not `VT_DISPATCH` | no — automation-surface fault |
| `kProgIdNotInCollection` | `COMAddIns.Item(progId)` failed: Excel does not list us at all | no — and NOT racy: persistent for the session |
| `kConnectPutRejected` | the `Connect` PROPERTYPUT ran and Office REFUSED | **yes** — Trust Center "Disable all Application Add-ins", or the ProgID parked in `Resiliency\DisabledItems` after an earlier crash |

`RibbonConnectFault` (in `com/ribbon_connect.h`, beside `RibbonAttempt`) is the
out-param; every step's HRESULT is captured into a local and all three reach the log
line. The 60-attempt give-up line now names the **DOMINANT** class (per-class
tallies, not "whichever happened last") and carries its HRESULT.

**THE GATE IS THE CARE POINT.** `SetRibbonConnected(false)` is the **teardown**
disconnect, called from `GracefulComTeardownHook` inside Phase 1, on the STA, in the
~80–100 ms window before Excel's `FreeLibrary`. The whole diagnostic — the log, the
COM property gets and above all the registry reads — is behind
`if (!ok && connected)`, so that call gains **zero** extra work. Asserted
structurally; do not "simplify" the condition.

`DumpConnectEnvironmentOnce` is a one-shot (atomic CAS), **strictly read-only**
probe fired only on the first `kConnectPutRejected`: the Addins key's presence +
`LoadBehavior`, Excel's `Resiliency\DisabledItems` (value count + whether our ProgID
appears in any blob), and `Application.COMAddIns.Count` (0 is the Trust Center
"disable all" signature). The Office **version segment is ENUMERATED**, not
hard-coded `16.0` — the Addins key we write is version-independent and a second,
silently-wrong version assumption in the same file is how a diagnostic starts lying.
There is no documented per-ProgID resiliency key: those values have opaque hashed
NAMES and carry the identity inside their `REG_BINARY` data as UTF-16, hence the
case-folded substring search.

**THE 60-CAP IS NOT AN UNBOUNDEDNESS BUG.** `g_ribbonConnectState` latches at 2 and
every subsequent entry short-circuits on the state gate. What it actually costs is
60 STA COM round-trips (each an `XLMAIN→XLDESK→EXCEL7` walk plus a `COMAddIns`
property get) spread over ~a minute. Do not "fix the unboundedness"; there is none.

**LEADING HYPOTHESIS, still unmeasured and falsifiable:** §18.11.5's fix leaves
`Excel\Addins\<progId>` in place across teardown, so the key is already there when
Excel builds its `COMAddIns` collection at startup. If `kProgIdNotInCollection` is
the dominant class in the field logs, that may take 3/20 to 0/20. The experiment is
a 20-start loop that greps the native log for the new fault-class line, run before
and after. **Nothing in this repo can decide it — do not close line 121 on the
classification alone.**

**STAGE B (pin the image from `xll::OnAutoClose`) is NOT IMPLEMENTED and must not
be added without lead sign-off.** §20.2.1 rule 1 states "exactly TWO pin call sites
exist; a third would break unload/re-enable", `gen_close_unload_review_test.go`
asserts the count is 2, and a third site would stop dev-mode
rebuild-while-Excel-is-open working for any RTD project after the user's first close
attempt (including a CANCELLED one) and stop `AddIns.Installed=$false` unloading the
image. That is a recorded decision and overriding it is a decision, not a gap to
fill. For the record, the fallback survey stands: for a GRACEFUL teardown there is
no substitute trigger — `IDTExtensibility2` reaches only a CONNECTED add-in,
`IRtdServer::ServerTerminate` is proven not to be a host-shutdown signal (Stage 4
Remediation #2), every self-owned idle callback is the §20.3 raw-code-pointer unmap
hazard, a bounded liveness watcher is the reverted Stage-4 BLOCKER, and
`GetModuleHandleExW(PIN)` at `DLL_PROCESS_DETACH` is too late (the unload decision
is already made).

### 18.12 Log Path Resolution (single-directory contract, 2026-07-10)

`logging.dir` resolves to **ONE directory** that holds BOTH log files:
`<proj>_native.log` (C++ XLL) and `<proj>_go.log` (the launched Go server's
redirected stdout/stderr). The resolution happens once, at `xlAutoOpen`, and
flows to all consumers:

1.  **Native bootstrap** (`internal/assets/files/src/xll_log.cpp`):
    `ResolveNativeLogPaths` resolves `logging.dir` (`${XLL_DIR}`, `${BIN_DIR}`,
    `${TEMP}` and any other `${VAR}`, plus the legacy bare tokens
    `XLL_DIR`/`TEMP_DIR`, which are WHOLE-STRING matches) into
    `NativeLogPaths::dir`; empty → `binDir`. In **singlefile** mode
    `${BIN_DIR}` = the extraction dir `<temp_dir>\<project>` (the dir
    `ExtractEmbeddedExe` uses), NOT the temp root — this is what keeps exe +
    both logs together. `InitNativeLogging` is the single entry point
    `xlAutoOpen` calls: resolve → `InitLog(...)` (native log) → report. The
    resolved `dir` also feeds `LaunchConfig.logDir` (Go log). The generated
    `xlAutoOpen` (`internal/templates/xll_main.cpp.tmpl`) only SUPPLIES the four
    xll.yaml values and forwards `logPaths.dir` — see §18.12.1.
2.  **Launcher** (`internal/assets/files/src/xll_launch.cpp::ResolveServerPath`):
    `outLogPath = cfg.logDir + L"\\<proj>_go.log"`; empty `logDir` falls back to
    the launch cwd (legacy), and an uncreatable configured dir also falls back
    to cwd so the launch never aborts on a bad log path.
3.  **Native sink** (`internal/assets/files/src/xll_log.cpp::InitLog`): creates
    the parent directory of the final path; its singlefile fallback branch
    (empty/`BIN_DIR`/`TEMP_DIR` configured path) also targets
    `<temp_dir>\<project>\`.
4.  **Go bootstrap** (`pkg/server/bootstrap.go::InitServerLogging`): picks the
    sink. `XLL_LOG_TO_STDOUT=1` — set by the launcher, which has ALREADY
    redirected this process's stdout into `<logDir>\<proj>_go.log` — means log
    to STDOUT and do NOT open that file a second time (two writers interleave
    partial lines, and the extra handle locks the file independently of the
    inherited one, which is the S2 orphaned-server symptom with a handle bolted
    on). Anything else means no launcher (dev `go run`, regtest, a user's own
    main), so `InitLog` expands `${XLL_DIR}`/`${BIN_DIR}` plus generic `${VAR}`
    env placeholders the same way and opens `<logDir>\<proj>_go.log` itself. A
    failed init is printed to stdout and SURVIVED, never fatal.

**Constraint**: changing how `logging.dir` is resolved (new placeholder,
default, or singlefile layout) requires walking all four locations above plus
the docs (`README.md` "Server Logs", `TUTORIAL.md` troubleshooting,
`internal/templates/xll.yaml.tmpl` comments) and regenerating the goldens
(`UPDATE_GOLDEN=1 go test ./internal/generator/ -run TestGolden`). Do NOT
reintroduce a per-side default (e.g. Go log hardcoded to the launch cwd while
the native log honors `logging.dir`) — the split-log inconsistency in
singlefile mode was the original bug.

#### 18.12.1 Both log bootstraps are STATIC code, not template code (2026-08-02)

The C++ `xlAutoOpen` logging preamble (the `${BIN_DIR}` derivation, the
`logging.dir` resolution, the `<proj>_native.log` name, the `InitLog` call and
its failure `MessageBox`) and the Go `Serve` logger block (the
`XLL_LOG_TO_STDOUT` if/else, the two `log.Init` sites and the two
`fmt.Printf` fallbacks) are no longer emitted into generated projects. They live
in **`internal/assets/files/{include/xll_log.h,src/xll_log.cpp}`**
(`xll::ResolveNativeLogPaths` / `xll::InitNativeLogging`) and
**`pkg/server/bootstrap.go`** (`server.InitServerLogging`). Everything the
prose above says about their BEHAVIOR still holds verbatim — the change was a
pure relocation, no semantics touched.

**Why:** neither block contained template variables in its LOGIC, only the
values it consumed, so every generated project received a byte-identical
re-emitted copy that nothing but a golden-string grep could test. Same move,
same reasoning as §18.11.1 (ribbon connect / scratch book).

**What STAYS generated, and must:**
* the four C++ values — `logging.dir` (wide literal, `escapeCppString`),
  `logging.level`, the project name, and the `tempPattern`/`isSingleFile` pair
  that `LaunchConfig` needs anyway;
* the three Go values, `logging.dir` emitted via `%q`;
* `cfg.logDir = logPaths.dir` — the single-directory contract is WIRING, and it
  is the one thing a relocation could silently drop.

**Where the tests are.** BODY invariants: `pkg/server/bootstrap_test.go` (sink
choice, level threading, placeholder expansion, the survive-a-bad-dir fallback
message), `internal/assets/log_bootstrap_cpp_test.go` (declarations, the
`InitLog` call, the MessageBox, the non-fatal rule) and
`internal/assets/testdata/log_paths_native_test.cpp` — an offline g++ harness
that EXECUTES `ResolveNativeLogPaths` and additionally diffs it against a
verbatim copy of the deleted template lines over a 27-case matrix, which is what
discharges "the relocation changed no behavior". WIRING:
`internal/generator/gen_log_bootstrap_test.go`, which also carries the
**do-not-re-inline** guard in the spirit of §18.6.1's
`TestChunkSegmentLogicIsExtracted`: a re-inlined copy in either template would
shadow the static code, leave those tests green, and put untested code back in
the shipped XLL / generated server.

**Trap this move already stepped on:** the two `fmt.Printf` fallbacks were
`fmt`'s only UNCONDITIONAL uses in `server.go.tmpl` — every other one sits
inside `{{if .Functions}}` / `{{if .Rtd.Enabled}}`. Removing them broke a project
with neither on an unused-import error, so the template's "force usage" block
gained `var _ = fmt.Sprintf`
(`gen_log_bootstrap_test.go::TestGenServer_LogBootstrapCompilesWithoutFunctionsOrRtd`).

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
   arg it computes `xll::ContentHashToken(typeTag, px)` (a STREAMING FNV-1a over
   the XLOPER12 via `HashXLOPERInto`, which coerces refs to their cell VALUES) —
   or `ContentHashTokenFP12(fp)` for `numgrid` (geometry + raw double bytes). Both
   yield an `"h:<typeTag><hex>"` token (`internal/assets/files/src/xll_cache.cpp`).
   The `typeTag` (`g`/`r`/`n`/`a`) namespaces the hash by WIRE-PAYLOAD shape:
   the same `A1:B2` serialized as a grid (values) vs a range (coordinates) is a
   different payload, so the tag keeps each (content, target-type) pair on its
   own topic and RefCache entry — without it a grid arg's payload could satisfy
   a range arg's lookup with the wrong union type.
   **The tag also SELECTS the digest, and it must cover everything the payload it
   keys carries** (HIGH, fixed 2026-07-26): `g`/`n` payloads are cell VALUES, so
   they hash values only and two equal-valued ranges correctly share one topic and
   one ship; `r`/`a` payloads are COORDINATES (`ConvertRange`, and `ConvertAny` of
   a reference also emits a `Range`), so they use
   `HashXLOPERContentWithRefIdentity`, which folds the sheet id + rect table into
   the same FNV-1a stream **ahead of** the coerced values. Hashing values alone
   there gave two DISTINCT ranges holding the same numbers ONE token →
   `SendRefCachePayloadOnce` deduped the second ship → `ResolveRangeArg` handed the
   handler the FIRST range's coordinates (silent wrong answer). Identity is folded
   *in addition to* the values, never instead: the value part is what keeps
   "edited cell → new token → new topic → fresh compute" alive; a coordinates-only
   digest would freeze such a topic across edits. Same reasoning applies to
   `MakeCacheKey`, which digests EVERY reference arg with the identity-folding
   variant — it cannot see the declared arg type, and over-discriminating a `grid`
   arg only costs a cache miss whereas under-discriminating a `range` arg returns
   the wrong answer. Regression: `internal/assets/testdata/cache_native_test.cpp`
   (`TestRefIdentityInToken`) + `cache_cpp_test.go`
   (`TestRefIdentityFoldedForCoordinatePayloads`). For `grid` args the wrapper
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
   **Already-shipped peek (both rtd AND rtd-once; the rtd-once half landed
   2026-07-26).** The ship dedups on the token, but without a pre-check all N
   cells sharing one range still pay the O(payload) BUILD and
   `SendRefCachePayloadOnce` drops N−1 of the buffers. Both wrappers therefore
   peek at the same `g_sentRefCache`/`g_refCacheMutex` pair
   (`{ lock_guard; find; }`) and skip the whole build — `FlatBufferBuilder`
   construction included — on a repeat. For rtd-once the peek sits AFTER the
   once-cache early-return (a memoize/TTL hit must not even peek) and BEFORE the
   builder. It matters MORE there, not less: the once-cache is what usually
   spares rtd-once the work, but on the FIRST calc pass it is empty, so all N
   cells miss it and all N reach the build. Racing another calc thread is benign
   in both directions — an entry is written only after a successful ack, so
   PRESENT means known-delivered, and a miss just falls through to
   `SendRefCachePayloadOnce`, which re-checks under the same mutex and no-ops.
   Never a lost ship. Regression:
   `internal/generator/gen_rtd_composite_test.go::TestGenCpp_RtdComposite_SkipBuildWhenAlreadyShipped`
   (plain rtd) and `::TestGenCpp_RtdOnceComposite_SkipBuildWhenAlreadyShipped`
   (rtd-once: markers + the lookup→peek→build→ship ordering + the builder being
   inside the guard).
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
   clear value to the topic
   (`rtd.GlobalRtd.SendErrorUpdate(topicID, err.Error())` — the error variant, so
   a scalar rtd-once cell shows the message for one cycle and then retries
   instead of freezing it under `memoize`; see "Error values on a scalar rtd-once
   topic" below) instead of hanging at `#GETTING_DATA`.
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
     retention exactly as for scalar rtd-once. `ProcessRtdUpdate` ROUTES grid-once
     topics (detected via the grid registry's `KeyForTopic`) away from the scalar
     `StoreResult`: a success is a no-op there (the grid already arrived via
     `MSG_RTD_ONCE_GRID`), an `is_error` update becomes a TRANSIENT grid entry
     (`StoreError`, see "Error values on a GRID/NUMGRID rtd-once topic" below).
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
  result via `SendUpdate`; on error (handler error, ctx already cancelled,
  composite-arg resolve miss, `SendOnceGrid` failure) it pushes the error
  STRING via `SendErrorUpdate` instead. Unit-testable in isolation
  (`pkg/rtd/runonce_test.go`).
* `pkg/rtd/manager.go` — `SendUpdate` (is_error=false) vs `SendErrorUpdate`
  (is_error=true); both funnel into one `sendUpdate(client, topicID, value,
  isError)`. The flag defaults to false and flatc elides defaults, so a
  non-error push stays byte-identical to the pre-`is_error` wire encoding.
  `protocol.fbs`: `RtdUpdate.is_error: bool = false` (single-sourced from
  `types`, §18.1).
* `internal/templates/xll_main.cpp.tmpl` — rtd-once registers like rtd
  (`Q<args>$`, returns `LPXLOPER12`); a distinct wrapper body (below);
  `RtdOnceRegistry::SetFunctionNames({names}, {memoizeNames}, {ttlPairs})` at
  xlAutoOpen (the third arg is name→ttl-ms pairs for memoize_ttl functions).
* `internal/assets/files/include/xll_rtd_once.h` — `RtdOnceRegistry` (the
  once-results map + topic bookkeeping, including `Entry::transient` and
  `StoreResult(key, value, transient=false)`) and `RtdOnceResultToXLOPER12`.
* `internal/assets/files/include/xll_rtd_once_grid.h` — `RtdOnceGridRegistry`,
  the byte-buffer twin, including `Entry::{errorText,transient}`, `StoreError`
  and the three-valued `OnceGridLookup TryGet(key, out, errOut)`.
* `internal/assets/files/src/xll_rtd.cpp` — `ConnectData` registers the
  topicID→key map for rtd-once topics and returns `#GETTING_DATA` for them; for
  plain-rtd topics it returns `RtdPlaceholderRegistry::MakeInitial(funcName)`
  (the configured initial value, default `#GETTING_DATA`) instead of the legacy
  "Connecting…"; `ProcessRtdUpdate` ROUTES by registry — scalar-once topics get
  `StoreResult(key, v, transient = update->is_error())`, grid-once topics get
  `StoreError(gridKey, text)` when `is_error` and nothing otherwise;
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
    **The store is UNCONDITIONAL** for non-grid-once topics (see "Error values"
    below) — the wrapper's ONLY way to put a value in the cell is a
    `TryGetResult` hit, so an un-stored value can never surface.
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

**Error values on a scalar rtd-once topic — the TRANSIENT entry (2026-07-25).**
When the one-shot handler fails (handler error, ctx already cancelled, or a
composite-arg resolve miss) the Go side pushes the error STRING with
`RtdUpdate.is_error=true` (`rtd.RunOnce` → `SendErrorUpdate`). The host then
stores it via `StoreResult(key, v, /*transient=*/true)`.

*Why it MUST be stored (the trap).* It is tempting to read `is_error` as "do not
cache this" and skip `StoreResult`. **That wedges the cell permanently** and was
caught as a HIGH regression on 2026-07-24. The scalar rtd-once wrapper puts a
value in the cell in exactly ONE place: the `TryGetResult` HIT in step 2. On a
miss it calls `xlfRtd`, **explicitly discards the synchronous result** (`(void)xRes;`)
and returns the `loading_placeholder`. So with no stored entry, every recalc
misses and re-paints the placeholder — while the topic is still **connected**, so
Excel never calls `ConnectData` again and the one-shot handler never re-runs. The
cell is stuck at `#GETTING_DATA` for the life of the XLL. This is the same
failure mode the `ClearNonMemoized` LIVENESS GUARD note describes; the fix is the
same shape (make the entry reclaimable), not "don't store".

*What `transient` actually does — RETENTION, not storage.* A transient entry is
reclaimed as if the function were plain `once`, **overriding** whatever the
function declared:

| declared lifecycle | normal entry (completed result) | transient entry (error) |
| --- | --- | --- |
| `once` (default)   | erase at first CalculationEnded after DisconnectData | same (no change) |
| `memoize_ttl: <d>` | erase once no live topic AND `age > d`                | erase as soon as no live topic (TTL ignored) |
| `memoize: true`    | never erase (until process teardown)                 | erase as soon as no live topic (memoize ignored) |

The liveness guard still applies to transient entries — while the producing topic
is connected they are kept, which is precisely the one recalc in which the cell
paints the error text. `TryGetResult` mirrors the rule on the read side (a
transient entry whose topic is gone is a MISS and is erased there), covering the
`DisconnectData`/`CalculationEnded` ordering window: if CalculationEnded fires
first the sweep sees a live topic and keeps the entry, and without the read-side
rule the next recalc would re-serve the stale error for an extra cycle instead of
recomputing. A later completed `StoreResult` on the same key re-stamps the flag,
promoting the entry back to normal retention. Net user-visible behavior:
**error text appears in the cell, and the next recalc retries the handler** — for
`once`, `memoize_ttl` and `memoize` alike. (Before `is_error` existed, plain
`once` already behaved this way by accident; the flag is what makes
`memoize`/`memoize_ttl` behave the same instead of freezing the error.)

**Error values on a GRID/NUMGRID rtd-once topic — the TRANSIENT grid entry
(2026-07-26).** Until this date the scalar rule above had no grid twin: a
`grid`/`numgrid` rtd-once topic is registered in `RtdOnceGridRegistry`,
`ProcessRtdUpdate` skipped the scalar `StoreResult` for it (GRID-ONCE GATE), and
its wrapper reads **only** the grid registry — so an `is_error` push was
DROPPED. The cell kept the `loading_placeholder` (grid) / an empty 0×0 FP12
(numgrid) **forever**: every recalc missed the grid cache, re-issued `xlfRtd`
against the STILL CONNECTED topic, `ConnectData` never re-fired, the one-shot
handler never re-ran. No error text, and no self-heal. Fixed by mirroring the
scalar transient semantics into the grid registry. **No wire-format change** —
the failure already crosses the wire as `RtdUpdate.is_error=true` (`rtd.RunOnceGrid`
→ `SendErrorUpdate`, which deliberately ships NO grid and NO readiness token);
the host simply stops discarding it.

* **Routing** (`xll_rtd.cpp` `ProcessRtdUpdate`): the grid-once branch is no
  longer a pure skip. `is_error=true` → `RtdOnceGridRegistry::StoreError(gridKey,
  text)` (text recovered from the VT_BSTR just built; a non-string/absent value
  falls back to `#ERROR: rtd-once handler failed (no message)` so the cell never
  shows an empty string). `is_error=false` is still a no-op there — it is only
  the readiness token; the grid itself arrived via `MSG_RTD_ONCE_GRID`.
* **Entry kind** (`xll_rtd_once_grid.h`): `Entry` gains `errorText` +
  `transient`, and the lookup becomes three-valued —
  `enum class OnceGridLookup { kMiss, kResult, kError }`. A `bool` hit could not
  distinguish an error with an empty message from a zero-byte payload, and
  `flatbuffers::GetRoot` over zero bytes is UB; `enum class` also makes a stale
  `if (TryGet(...))` fail to compile rather than silently mean the wrong thing.
* **Preservation matrix — identical to the scalar one, not a variant of it.**
  `once` = unchanged; `memoize_ttl` = TTL IGNORED for a transient entry (erase as
  soon as no live topic); `memoize:true` = memoize IGNORED (erase as soon as no
  live topic). The transient check runs FIRST inside `ClearNonMemoized`, and only
  inside the `!live` branch — the LIVENESS GUARD is untouched, so a transient
  entry whose topic is still connected survives a `CalculationEnded`. `TryGet`
  mirrors it on the read side (transient + no live topic → erase, `kMiss`).
  `Store` (a completed grid) re-stamps `transient=false` and clears `errorText`,
  promoting the entry back to normal retention.
* **What the CELL shows** — the one place the grid path cannot mirror the scalar
  path, decided from the ABI:
  - `grid` (registered `Q` / `LPXLOPER12`): the **error message text**, via
    `NewExcelString(gerr)` (DLL-owned, `xlbitDLLFree`, reclaimed by
    `xlAutoFree12` — the same allocation contract as
    `RtdOnceResultToXLOPER12`'s VT_BSTR branch). Chosen over `#VALUE!` because
    the whole point is a DIAGNOSABLE value and because it is byte-for-byte the
    paint a scalar rtd-once failure produces; `#VALUE!` already exists as the
    UNdiagnosable fallback for a malformed payload (`GridToXLOPER12(nullptr)`).
    A scalar string is a legal return for a `Q`-registered function: the anchor
    cell shows the text and nothing spills.
  - `numgrid` (registered `K%` / `FP12*`): **the empty 0×0 FP12**, unchanged —
    the FP12 ABI carries doubles only, so it can express neither text nor a cell
    error (the same constraint that forces its `loading_placeholder` to be 0×0).
    The message goes to the log (`SAFE_LOG_WARN`), which is the only diagnosable
    channel left. Deliberately NOT encoded as a magic double/NaN: that would be
    indistinguishable from data.
* **The real win for BOTH is the HIT, not the paint.** `kError` returns from the
  wrapper WITHOUT calling `xlfRtd`, so the cell drops its RTD reference → Excel
  calls `DisconnectData` at end of calc → `ClearNonMemoized` reclaims the
  transient entry → the next recalc misses → `xlfRtd` → new topic → the handler
  RE-RUNS. Returning `kMiss` for an error instead would wedge the cell exactly as
  the scalar "why it MUST be stored" trap describes. So even `numgrid`, whose
  paint is unchanged, goes from *frozen forever* to *self-healing on the next
  recalc*.

*Regression tests (grid).* `internal/assets/assets_test.go` —
`TestRtdOnceGridRegistryTransientRetention` (pins `OnceGridLookup`, `StoreError`,
the `Entry` fields, the transient-before-memoize ordering in `ClearNonMemoized`,
the `TryGet` read-side rule, and `Store`'s promotion re-stamp) and the extended
`TestRtdUpdateErrorStoresTransient` (pins the `isGridOnce` ROUTING split and that
the grid branch calls `StoreError` keyed off `update->is_error()`).
`internal/assets/rtd_once_grid_error_cpp_test.go::TestRtdOnceGridRegistryErrorBehavior`
is the BEHAVIORAL gate — the grid twin of the scalar compile+run driver, but with
NO `types` dependency (the header includes only `<windows.h>` + the STL, so it
needs no include dirs and no link libs): (a) error stored + read back as `kError`
while the topic is live, with the payload out-param CLEARED, (a2) liveness guard
across `ClearNonMemoized`, (b)/(b2) erased even for `memoize:true` / unexpired
`memoize_ttl`, (c) read-side miss, (d)–(f) the memoize/ttl/once controls, (g)
grid-over-error promotion, (h) error-over-grid replacement drops the stale
payload. Skipped without `g++` and under `-short`.

*Regression tests (scalar).* `internal/assets/assets_test.go` —
`TestRtdUpdateErrorStoresTransient` (pins `/*transient=*/update->is_error()` and
that the only remaining gate on the scalar `StoreResult` is the `isGridOnce`
routing split; explicitly fails on the rejected `!isGridOnce && !isError` form) and
`TestRtdOnceRegistryTransientRetention` (pins `Entry::transient`, the
`StoreResult` overload, the transient-before-memoize ordering in
`ClearNonMemoized`, and the `TryGetResult` read-side rule).
`internal/assets/rtd_once_transient_cpp_test.go::TestRtdOnceRegistryTransientBehavior`
is the BEHAVIORAL gate: it compiles the embedded `xll_rtd_once.h` with `g++
-std=gnu++17 -DXLL_RTD_ENABLED` against the local `types` include dir (needs
`gnu++17` — `types/xlcall.h` uses `_cdecl`/`pascal`) and runs it, asserting
(a) a transient entry is stored and readable while its topic is live,
(a2) the liveness guard keeps it across a CalculationEnded, (b)/(b2)
`ClearNonMemoized` erases it even for `memoize:true` / unexpired `memoize_ttl`,
(c) the read-side miss, and (d)–(g) the memoize / ttl / once / promotion
controls. Skipped when `g++` or the `types` checkout is absent, and under
`-short`.

**Thread-safety:** the wrapper runs on calc threads, `ProcessRtdUpdate` on the
IPC thread, calc-end/xlAutoClose on the STA thread — all `RtdOnceRegistry`
access goes through one mutex. The `#GETTING_DATA` scode (2043) is kept
byte-identical to `rtd/server.h`'s RefreshData placeholder (§22); do not
diverge. Unload-safety idioms (`g_isUnloading`, ConnectData drain) are
unchanged — rtd-once adds no new detached threads.

### 19.4 Calculation events — MEASURED semantics (2026-07-26, real Excel)

Everything the runtime hangs off `xleventCalculationEnded` (the RefCache clear,
`g_sentRefCache`, `RtdOnceRegistry::ClearNonMemoized`, the date-format drain, the
`MSG_CALCULATION_ENDED` round-trip) depends on WHEN Excel actually fires these
two events. The answers below are **measured**, not inferred: a minimal probe XLL
(one exported handler per event + UDFs that log every invocation) was driven
against **Excel 16.0.20131.20154 (64-bit)**. Treat these as settled — do not
re-derive them from documentation.

1. **One `CalculationEnded` per calculation CYCLE, not per iteration.** With
   `Application.Iteration = True` on a circular formula, Excel runs the circular
   group up to `MaxIterations` times inside ONE cycle and fires the event
   **once, after the last pass**:
   * `MaxIterations=5`, non-converging (`A1 = PROBEITER(A1)+D1*0`): 5 UDF
     invocations (arguments 0,1,2,3,4 — the values DO change every pass), then 1
     `CalculationEnded`.
   * converging (`B1 = PROBECONV(B1)+E1*0`, MaxChange 0.001): 14 invocations to
     convergence, then 1 event.
   * non-circular baseline: 1 invocation, 1 event.
   **Consequence (this was a real HIGH defect, fixed the same day):** any
   per-cycle cache keyed on something that changes BETWEEN iterations is stale
   for passes 2..N. That is exactly `CacheManager::refCache_` — key = pure
   coordinates `(idSheet, rect)`, value = the range's VALUE digest — so a
   cache-enabled function with a reference argument inside a circular group was
   served its own first-pass result for the rest of the cycle and the system
   "converged" on a wrong number. Fixed by the iterative-calculation gate
   (`CacheManager::RefreshIterativeCalcMode`, `xll_cache.h`): calc end reads
   `GET.DOCUMENT(15)` and, while iteration is on, `GetOrComputeRefHash` skips the
   MEMOIZATION (never the digest — the key bytes are identical either way).
   Residual, accepted: the gate is refreshed at calc END, so the FIRST cycle
   after iteration is switched on still memoizes; every later cycle is correct.
2. **`GET.DOCUMENT(15)` is the iteration flag, and it is callable from inside the
   calc-end callback.** Dumping `GET.DOCUMENT(1..90)` with `Application.Iteration`
   false vs true moved exactly two entries: **15** (`BOOL` 0→1, iteration
   enabled) and **16** (`num`, MaxIterations); 17 is MaxChange. The same dump run
   from inside the `xleventCalculationEnded` handler returned identical values —
   confirming the event callback is a valid macro-sheet/command context. It is
   NOT callable from the `$`-registered UDF wrappers (macro-sheet only), which is
   why the gate is refreshed at calc end and cached in an atomic.
3. **A cancelled calculation fires `CalculationCanceled` AND THEN
   `CalculationEnded`.** Interrupting a recalc with a real ESC produced, 3 times
   out of 3 (mid-work interruptions, native array formulas so Excel's own calc
   loop owns the interrupt check):
   `EVENT CalculationCanceled` → `EVENT CalculationEnded` **2–6 ms later, with no
   calculation work in between**. So `HandleCalculationEnded` — and therefore the
   RefCache clear, `g_sentRefCache.clear()`, the rtd-once `ClearNonMemoized`
   sweeps and the calc-end round-trip — **already runs after a cancel**.
   **Do NOT add cache clearing to the cancel path "to clear the caches on Esc":
   the premise is false and the change is not neutral.** Clearing there would
   (a) drop `ScheduleSet`/`ScheduleFormat` commands produced during that cycle a
   few ms BEFORE the Ended flush that is supposed to emit them, and (b)
   desynchronize `g_sentRefCache` (C++) from the Go `RefCache` — they stay in
   lockstep only because ONE event clears both; clearing one side re-ships
   nothing while the other has already forgotten the payload →
   `ResolveRangeArg` misses.
   See §19.4.1 for the shipped `CalculationCanceled` event semantics.

#### 19.4.1 `CalculationCanceled` as a user event (v0.8.36) — BOTH handlers fire

`CalculationCanceled` is a first-class, **opt-in** user event. Declaring

```yaml
events:
  - type: CalculationCanceled
    handler: OnEscape        # optional; defaults to OnCalculationCanceled
```

makes the XLL register `xleventCalculationCanceled` (it already did) **and**
forward it: the exported proc calls `xll::HandleCalculationCanceled()`
(`internal/assets/files/src/xll_events.cpp`), which sends
`MSG_CALCULATION_CANCELED` (132) and nothing else. Before v0.8.36 the named-event
stub in `xll_main.cpp.tmpl` only logged, so the whole Go chain
(`HandleCalculationCanceled` → `OnCalculationCanceled`) was dead code in every
generated project. An **undeclared** project registers no cancel callback and
pays no round-trip — the emission is gated on the declaration.

**The user contract — do not try to hide it.** One Esc-interrupted cycle invokes
**both** handlers: `OnCalculationCanceled` first, `OnCalculationEnded` 2–6 ms
later. "Cancelled" does NOT mean "Ended will not come". This is documented in the
generated `interface.go` doc comment, `README.md` and `TUTORIAL.md`; a user who
assumes the events are mutually exclusive will write broken cleanup code.

**Ordering is guaranteed by the synchronous send, not by luck.** Excel fires both
events on the same STA thread, cancel first; `g_host.Send` blocks that thread
until the guest dispatch returns, and
`pkg/server/handlers.go::HandleCalculationCanceled` invokes `onCanceled`
**synchronously** (like `HandleCalculationEnded`, unlike the RTD hooks). So
`OnCalculationEnded` cannot start until `OnCalculationCanceled` has finished. The
previous goroutine dispatch would have let them run concurrently or inverted.
The §18.3 rule applies unchanged: the handler runs while Excel's STA is blocked,
so no synchronous COM — use `ScheduleSet`/`ScheduleFormat`.

**`HandleCalculationCanceled` clears NOTHING** — not `CommandBatcher`, not
`RefCache` — for the two reasons in item 3 above, and replies with an empty
payload. Commands scheduled during the interrupted cycle (including by the
cancel handler itself) survive into the Ended flush. `CommandBatcher.Clear()`
consequently has no production caller; it is kept as a reset primitive and must
not be re-attached to a calc-boundary event.

**Regressions.** `internal/generator/gen_event_test.go` —
`TestGenCpp_CalculationCanceledForwardsToServer` (declared ⇒ registration +
`HandleCalculationCanceled();`, renamed handler included),
`TestGenCpp_CalculationCanceledNotEmittedWhenUndeclared` (undeclared ⇒ neither),
`TestCalcCanceled_AssetIsNotificationOnly` (the asset's cancel body, comments
stripped, must not name `g_sentRefCache` / `ClearRefCache` /
`ClearNonMemoized` / `DeferCalcEndCommands` / `RefreshIterativeCalcMode`),
`TestGenServer_CalculationCanceledDispatch`. `pkg/server/handlers_calc_test.go` —
`TestHandleCalculationCanceled_PreservesBatchedCommands` (5 sub-cases),
`…_InvokesHandlerSynchronously`, `TestCalculationCanceledThenEnded_HandlerOrder`,
`…_ReplyIsEmpty`; `pkg/server/refcache_test.go::TestHandleCalculationCanceled_LeavesRefCacheToTheEndedPath`
(replaces `…_ClearsRefCache`, which asserted the disproved behavior). Regtest
mock-host case **13a/13b** (cache survives the cancel and is cleared by the
Ended that follows; a `ScheduleSet` issued before a cancel still arrives in the
Ended response). The C++ gate fixture `cppCompileGateYaml` now declares the event
with a RENAMED handler, so the forwarding branch is actually compiled.

**Verification status — read before claiming this is proven end-to-end.** What
is verified: the Excel-level event ordering (the §19.4 item-3 probe, real Excel,
3/3); that the forwarding code compiles into a real MinGW-built XLL and that the
renamed callback is actually in the shipped **PE export table** (the C++ gate now
asserts it — Excel resolves `xlEventRegister` callbacks by exported name, so a
compiled-but-unexported handler fails registration silently); and the whole
guest-side contract over real SHM via the regtest mock host. What is **NOT** yet
verified: a live Esc-during-F9 in real Excel showing both Go handlers firing in
order. That needs a foreground keyboard-driven probe — `Application.Calculate()`
over COM fires NEITHER event and `PostMessage(WM_KEYDOWN, VK_ESCAPE)` is ignored
(§19.4 item 4), so the recipe is: build a project declaring the event plus a UDF
slow enough to interrupt, register it, `keybd_event` F9 with Excel in the
foreground, `keybd_event` ESC mid-calc, and read the ordered handler log. Do
that before treating the ordering as field-proven.

**No named event remains a no-op.** Excel's C API defines exactly two
`xlEvent*` constants (`types/include/types/xlcall.h:317-318`:
`xleventCalculationEnded` = 1, `xleventCalculationCanceled` = 2), and
`config.validEventTypes` contains exactly those two — both now fully wired. The
`{{else}}` branch of the named-event stub is therefore unreachable; it was
downgraded from `SAFE_LOG_INFO("Event X triggered")` to a `SAFE_LOG_WARN` that
says the event is not forwarded, so the next person who widens
`validEventTypes` without doing the §18.3 co-change sees it at runtime instead
of silently shipping another dead handler.
4. **Trigger matters when you test this.** `Application.Calculate()` over COM
   fires NEITHER event (observed repeatedly: the UDFs run, no event). A cell edit
   (`Range.Value2 = …`) and a real F9 keystroke both do. Excel's calculation
   interrupt is likewise driven by REAL keyboard input: `PostMessage(WM_KEYDOWN,
   VK_ESCAPE)` to `XLMAIN` is ignored, `keybd_event`/SendKeys with Excel in the
   foreground is observed (a UDF's `xlAbort` then returns TRUE). Any future
   probe must use an edit/F9 trigger, or it will "prove" that the events never
   fire.

### 19.5 Reference arguments: the request arena is the real bound (2026-07-26)

A `grid`/`range`/`any` argument is registered `U` (§19.2), so Excel hands the
wrapper **whatever reference the user typed** — a whole column, a union, a
cross-sheet range. Two defects on that path were reproduced against the SHIPPED
showcase build (Excel 16.0.20131.20154, caching disabled — the stock artifact):

```
=SumGrid($P$1:$R$10)                -> 2805      (control, 3/3)
=SumGrid($P:$R)                     -> Excel PROCESS DEATH   (3/3, and 5/5 in the original report)
=SumGrid(($P$1:$R$5,$P$6:$R$10))    -> 0, silently, no error (3/3)
```

**The crash mechanism is the ALLOCATOR, not the size of the coerce.** A sync/
async request is built **directly into the slot's request buffer** — `SHMAllocator`
over `slot.GetReqBuffer()` / `slot.GetMaxReqSize()`, 512 KiB with the stock slot
geometry — and there is **no chunking in the host→guest ARGUMENT direction**.
`flatbuffers::Allocator` has **no failure channel**: its base
`reallocate_downward` calls `allocate(new_size)` and then `memcpy_downward`
**unconditionally**. `SHMAllocator::allocate` returned `nullptr` past capacity,
so an over-capacity request did not fail — it memcpy'd into a near-null address
and took the Excel process with it.

That is why the cliff sits far below "3.1 million cells". Measured on the stock
build, one formula per Excel instance:

| grid argument | cells | outcome (pre-fix) |
| --- | --- | --- |
| `$P$1:$DK$100` (100×100) | 10,000 | 10000 ✔ |
| `$P$1:$EI$120` (120×120) | 14,400 | 14400 ✔ |
| `$P$1:$FC$140` (140×140) | 19,600 | **process death** |
| `$P$1:$HG$200` (200×200) | 40,000 | **process death** |
| `$P:$P` / `$P:$R` | 1.0M / 3.1M | **process death** |

19.6k cells × the measured **~28 B/cell** for a `protocol::Grid` (`Scalar` table
+ union member + vtable + vector slot, §23.3) ≈ 549 KB — i.e. the cliff is
exactly where the payload stops fitting 512 KiB. **The differing fault
signatures across reproductions** (`ucrtbase.dll 0xc0000005` vs `KERNELBASE.dll
0xe0000002`) are consistent with this: the near-null `memcpy` faults in the CRT,
while the pathological end additionally makes Excel materialize ~100 MB of
`XLOPER12`, which can fail earlier and differently.

**The fix is two layers, and only the first one is the cure:**

1. **`SHMAllocator` never returns `nullptr` into flatbuffers** (`include/SHMAllocator.h`).
   An over-capacity request is served from the **heap** and latched in a sticky
   `Overflowed()` flag; the caller refuses the call before `Send`. This is the
   root-cause fix — it converts *every* arena overflow (grids, long string args,
   an invalid slot whose `GetMaxReqSize()` is 0, any future arg type) from
   process death into `#VALUE!` plus a log line.
   **Contract: EVERY site that builds into a slot arena and sends MUST check
   `allocator.Overflowed()` between `Finish()` and `Send()`.** The overflow bytes
   are on the heap, so sending would publish a length that does not describe the
   shared memory — worse than the crash. The four sites are the generated UDF
   wrapper (`xll_main.cpp.tmpl`), `ribbon_addin.cpp` `SendCommandInvoke`, and
   `xll_rtd.cpp` `ConnectData` + `DisconnectData`. Gated by
   `internal/assets/gridarg_cpp_test.go::TestSlotArenaSendersCheckOverflow`,
   which counts `SHMAllocator allocator(` against `allocator.Overflowed()` per
   file — it caught the `DisconnectData` site that was missed on the first pass.
   (`regtest_main.cpp.tmpl` is NOT such a site: it uses flatbuffers' 4-arg
   caller-supplied-buffer constructor with the default heap allocator.)
2. **`xll::kMaxGridArgCells` (16384) is DEFENSE IN DEPTH, not the cure.** With
   layer 1 in place a whole-column argument is already survivable; the bound
   exists so the pathological case is refused *cheaply* — before `xlCoerce` is
   asked to materialize ~100 MB of `XLOPER12` for an answer that is going to be
   thrown away. 16384 sits just under the ~18.7k cells a 512 KiB slot can hold,
   so everything that worked before still works and everything that used to
   crash now returns `#VALUE!`. It is a PRE-FILTER, not a guarantee: 16k
   string-valued cells still will not fit, which is precisely why layer 1 has to
   exist. **Deliberately not wired to `xll.yaml`** — the only defensible value is
   a function of the SHM slot geometry, which the config surface does not
   expose, so a user-supplied cell count could not be validated against
   anything. Same posture as the C++ chunk-receiver caps (§18.6.1).
   **Layer 2 now covers the DIGEST path too (HIGH, fixed 2026-07-29).** Until
   then `kMaxGridArgCells` was applied in exactly ONE place, `ConvertGridArg` —
   and in the generated wrapper the digest is computed FIRST: `MakeCacheKey`
   before `ConvertGridArg` for sync/async, `ContentHashToken` before the payload
   build for rtd / rtd-once. So `=CachedSumGrid($P:$R)` and
   `=RtdComposite($P:$R, …)` still asked `xlCoerce` for the full 3,145,728-cell
   `XLOPER12` (~100 MB) and FNV-1a'd every cell — per cell using the range, per
   recalculation — and only then hit `kTooLarge`, i.e. precisely the cost the
   paragraph above cites as this layer's whole reason to exist. For an rtd token
   it was unconditional on every recalc, because the token is computed ABOVE the
   per-cycle `rcAlreadySent` dedup. Fixed by calling `xll::MeasureRefArg` BEFORE
   the coerce in `HashXLOPERIntoDepth`'s `xltypeRef`/`xltypeSRef` branch, and once
   over the WHOLE reference in `CacheManager::GetOrComputeRefHash` (whose rect loop
   hands computeFn one single-rect temporary per area, so the per-rect filter
   cannot see a union of individually-small rects). An over-bound reference folds
   `HashRefIdentity` — the SAME branch a coerce failure already takes — so two
   distinct oversized references still hash apart (no RefCache aliasing, no RTD
   topic collision), and nothing observable is lost because such a reference is
   refused downstream and never yields a value to cache or ship.

**Multi-area (union) references are refused, not summed.** `xlCoerce` cannot
produce "the union" of `(A1:B2,D1:E2)`; it answers **success with an error
value**, and `ConvertGrid`'s non-`xltypeMulti` fall-through wrapped that as a
1×1 grid the handler read as data and summed to **0**. For a financial add-in a
silent wrong answer is worse than a crash. Two independent guards now stop it:

* **structural** — `xltypeRef` with `lpmref->count > 1` is refused
  (`GridArgStatus::kMultiArea`) **before Excel is asked**, so the diagnosis does
  not depend on how `xlCoerce` chooses to fail. Confirmed to be the guard that
  actually fires: Excel passes a same-sheet union as `xltypeRef` with `count==2`
  (native log: `SumGrid: grid arg g rejected: multi-area (union) reference`).
* **post-coerce** — the result must be the `xltypeMulti` that was requested;
  anything else is `kNotAnArray`. This is the belt-and-braces that catches every
  other "success, but not what you asked for" answer.

`ConvertGridArg`'s out-param is therefore an **enum** (`xll::GridArgStatus`), not
a `bool`: a boolean could only ever say "coerce failed", including for the two
refusals that never call Excel. The generated wrapper logs
`GridArgStatusText(...)` and returns `#VALUE!` (sync), `xlAsyncReturn(#VALUE!)`
(async), or SKIPS the RTD payload ship (rtd / rtd-once content-hash path — see
§19.3; shipping a refusal's empty grid under a content-hash token would poison
the Go `RefCache` for every other cell using that range).

**Note on `range`/`any` args — read the SCOPE (corrected 2026-07-29):** they are
NOT affected by either defect **on the PAYLOAD**. Their payload is COORDINATES
(`ConvertRange` emits the full rect table, so a union is represented natively and
a whole column costs 16 bytes), not values. Only `grid` flattens to cell values,
and only `numgrid` (FP12, built by Excel itself) shares the arena-size exposure —
which layer 1 now covers.
**Their DIGEST was affected, and that was not a payload question.** A `range`/`any`
RTD argument's token (`ContentHashToken('r'|'a', px)`) and every reference argument
of a cache-enabled function (`MakeCacheKey`) route through
`HashXLOPERContentWithRefIdentity`, which folds the identity and then streams the
COERCED VALUES — down the same unbounded `xlCoerce` a `grid` argument used. So
`=RtdComposite($P:$R, …)` with a `range` parameter made Excel materialize ~100 MB
per recalculation even though the payload it shipped was 16 bytes. The layer-2
pre-filter added 2026-07-29 (see item 2 above) sits in `HashXLOPERIntoDepth`, so it
covers all four tags (`g`/`n`/`r`/`a`) at once. The original wording was written
about the crash and the wrong-answer defects, where it is correct; do not read it
as "the range/any path has no area exposure at all".

**Regressions:** `internal/assets/testdata/gridarg_native_test.cpp` +
`internal/assets/gridarg_cpp_test.go::TestGridArgNativeBehavior` (offline g++
gate, stubbed `Excel12v`; 40 checks: `MeasureRefArg` shapes, the contiguous
control, oversized-refused-without-coercing, union-refused-structurally,
coerce-error-is-not-a-1×1-grid, and the allocator overflow). FAIL-before is
mechanical, not asserted: neutering the allocator makes the harness exit
`0xc0000005`; neutering the two union guards yields 5 failures; neutering the
cell bound yields 6. Plus the always-on markers
`TestSHMAllocatorNeverReturnsNullIntoFlatbuffers`, `TestGridArgGuardsDeclared`,
`TestGridArgRefusalReachesTheCell`, `TestSlotArenaSendersCheckOverflow`, and the
updated `internal/generator` markers (`TestGenCpp_ArgMarshalling`,
`TestGenCpp_AsyncGridArgCoerces`, `gen_rtd_composite_test.go`).

**Was NOT the defect (checked, do not re-report).** The report that accompanied
this work also claimed `CacheManager::GetOrComputeRefHash` fails to arm the
iterative-calculation gate for same-sheet (`xltypeSRef`) ranges, because
`refPathUsed_.store(true)` sits after `if (refTy != xltypeRef) return
computeFn(pRef);`. The code reads that way, but it is **correct as written**:
`refPathUsed_` exists solely to decide whether calc end pays for the
`GET.DOCUMENT(15)` round-trip that gates the **RefCache memoization**, and an
`xltypeSRef` argument never enters `refCache_` at all (only an `xltypeRef`
carries the `(idSheet, rect)` key material — §22 "Confirmed-Correct Decisions").
There is nothing to gate on that path, so arming the flag there would only add
an Excel round-trip per cycle. `GetOrComputeRefHash` was left untouched.

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

#### 20.2.1 "Leak, don't crash" — CORRECTED 2026-07-29. It only holds while the image stays MAPPED.

The §20.2 wording above was **falsified by measurement**: leaking a running thread is safe
*only* if the XLL image is never unmapped while that thread (or Excel) still has code or a
vtable in it. There is a real, 100%-reproducible close path where Excel **does** unmap it.

**What is still TRUE (keep):**
* A joinable global `std::thread` destroyed at `DLL_PROCESS_DETACH` calls `std::terminate()`
  (§20.1). `.detach()` at DETACH is the correct fix for THAT, and DETACH must never join,
  drain, or run C++/SHM destructors under the loader lock.
* On a **normal Excel exit with no live RTD topics**, `DLL_PROCESS_DETACH` arrives from
  PROCESS TERMINATION (`lpReserved != NULL`), the image is **not** unmapped, and every
  leaked thread is killed by the OS before it can run again. Measured clean.

**What is WRONG (do not rely on it):**
* "The threads continue running (leaked) until the OS cleans up the process resources" —
  this silently assumes the image survives. **On a host shutdown with live streaming RTD
  topics, Excel calls `FreeLibrary` ~80–100 ms after `OnBeginShutdown` returns and the image
  is REALLY UNMAPPED** (`DLL_PROCESS_DETACH` with `lpReserved == NULL`; measured 2026-07-29,
  Excel x64 16.0.20228). Anything of ours still executing — a leaked worker loop, a leaked
  message-only `WndProc` the OS still dispatches (§20.3 already documents that hazard for the
  removed ribbon `TimerProc`), or a thread **parked inside `join()`** — resumes in a hole and
  dies with `0xC0000005` against `<proj>.xll_unloaded`. Excel's own side dies the same way,
  dereferencing an `IRtdServer` vtable in the hole (`0xC0000005` in `EXCEL.EXE` /
  `mso20win32client.dll`).
* `join()` is NOT exempt: `std::thread::join` parks in libwinpthread's `pthread_join`, whose
  code is linked **into the XLL** (`-static`). The measured faulting RIP was the instruction
  immediately after `WaitForSingleObject(handle, INFINITE)` inside `pthread_join`.

**The rules that follow (enforced by
`internal/generator/gen_close_unload_test.go`):**

1. **On a CONFIRMED host shutdown, PIN the image** — `GetModuleHandleExW(FROM_ADDRESS | PIN)`
   in `GracefulTeardownOnce`'s Phase 1 (`PinModuleToPreventUnmap`). This is also the plain
   COM contract: an in-process server must not be unmapped while its objects are still
   referenced, and Excel's XLL manager `FreeLibrary`s **without** consulting our exported
   `DllCanUnloadNow` (verified: it is never called on that path). It makes the RTD close
   behave exactly like the measured-clean no-RTD close. Scope is deliberately narrow — **exactly
   two call sites**: (i) a confirmed host shutdown, and (ii) the add-in-DISABLE path *only when
   Phase 1's bounded reap had to `detach()` a thread instead of reaping it*, because a detached
   thread there would be executing inside an image Excel unmaps the moment `OnDisconnection`
   returns. In the normal disable case (both threads reaped — the measured case) nothing is pinned,
   so unload/re-enable and its `DLL_PROCESS_ATTACH` flag resets still work and dev-mode
   rebuild-while-Excel-is-open is unaffected. **A pinned image gets no second
   `DLL_PROCESS_ATTACH`**, which is why the flag reset had to become callable — see rule 5.
   Never use a matched `LoadLibrary`/`FreeLibrary` pair: a self-`FreeLibrary` that dropped
   the last reference would unmap the image under its own return address.
2. **Stop background work EARLY, while Excel is still calling into us** — see §23.6 Stage 5.
3. **After the graceful Phase 1 returns, no code of ours may PARK.** Bounded kernel calls
   only. A `join()` is permitted only after the target thread's own exit flag
   (`xll::WorkerExited()` / `xll::MonitorExited()`) has been observed, which makes the join
   return immediately; a thread that misses its bounded budget is **detached**, and the miss
   is recorded so the deferred phase leaks `g_phost` instead of freeing memory a live thread
   may still read.
4. **The teardown must be VISIBLE.** `LogInfo`/`LogWarn`/`LogError` all short-circuit on
   `g_isUnloading`, which the destructive phase latches as its first act — so that whole
   phase used to be invisible, and its absence from the log was misread as "Phase 2 never
   ran" while it was in fact running and parked in a join. `xll::LogTeardown()`
   (`xll_log.{h,cpp}`) bypasses that suppression and is used by the teardown path only
   (STA, never `DllMain`, never a detached thread on a forced unload).
5. **A PINNED image must still be able to start fresh.** Every lifecycle flag used to be reset in
   exactly one place, `DLL_PROCESS_ATTACH` — sound only while every unload really unmapped the
   image. Once pinned, `FreeLibrary`/`LoadLibrary` only move the reference count and `DllMain` is
   never called again, so the flags would stay latched for the life of the process and the next
   `xlAutoOpen` would come back **silently half-dead** (`MonitorThread` returning at once,
   `WorkerLoop` breaking out immediately ⇒ no RTD updates, no async results) while still holding the
   XLL file lock. Reachable, not theoretical: `Application.Quit()` from a COM client that KEEPS its
   `Application` reference delivers `OnBeginShutdown` (so the teardown runs, and pins) while Excel
   does **not** exit. The reset is therefore `xll::ResetLifecycleStateForFreshLoad()`, called from
   ATTACH **and** from `xll::PrepareForFreshLoad()` at the top of `xlAutoOpen`; when the previous
   teardown had to detach a thread the verdict is `kUnrecoverable` and the load is REFUSED instead.

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
   **INVARIANT (2026-07-30):** the teardown hook must NOT re-enter Office's
   `COMAddIn::put_Connect` from inside Office's own add-in-disconnect stack.
   `OnDisconnection` therefore publishes `xll::ribbon::OfficeDisconnectInProgress()`
   (an RAII depth guard, constructed BEFORE `GracefulTeardownOnce`) and the hook SKIPS
   its explicit `COMAddIns…Connect = false` while that is set. Rationale, evidence and
   the two ways to get this wrong: §22 "Office add-in disconnect re-entrancy".

   **`OnBeginShutdown` is a promise about the ADD-IN's shutdown, NOT the PROCESS's —
   and the signal set CANNOT be narrowed (closed 2026-08-03, backlog line 134/191;
   do not re-propose).** Both entries described one defect: a COM client that keeps
   its `Application` reference and calls `Application.Quit()` gets `OnBeginShutdown`
   delivered, the confirmed-shutdown teardown runs to completion (PIN, Phase 1,
   Phase 2 EXIT, Go server reaped — all logged) and `EXCEL.EXE` **survives 8/8**. So
   the ghost is NOT an incomplete teardown. Unticking the add-in in the COM Add-ins
   dialog reaches the same dead state through `ext_dm_UserClosed` (§18.11.5).

   The narrowing question — can "confirmed shutdown" become "confirmed AND actually
   exiting"? — is **impossible by construction**. The ONLY authoritative
   discriminator anywhere in the process is `lpReserved` in
   `DllMain(DLL_PROCESS_DETACH)` (`src/xll_lifecycle.cpp`, the `DLL_PROCESS_DETACH`
   case): `!= NULL` ⇒ process termination, `== NULL` ⇒ `FreeLibrary`. It is
   unambiguous and it arrives **strictly after** every point at which the distinction
   could be acted on — Phase 1 has already quiesced, Phase 2 has already deleted
   `g_phost` and closed `hJob`, and DETACH runs under the loader lock where nothing
   may be undone. Every alternative fails for a named reason: Office exposes no
   external-reference count; `Application.UserControl` reports who STARTED Excel, not
   who still holds it; `Application.Windows.Count` / `Visible` are 0/false in BOTH
   cases (in the held-ref case Excel really does tear its UI down — that IS the
   ghost); `IRtdServer::ServerTerminate` fires on every zero-live-topic transition
   (Stage 4 Remediation #2) and is not a shutdown signal at all; and a bounded
   liveness watcher after Phase 1 is precisely the off-STA `g_phase2Watcher` the
   Stage-4 REMEDIATION deleted as a review BLOCKER.

   **So the CONSEQUENCE is what is fixed: the state is now VISIBLE.**
   `xll::ReportPostTeardownUse(site)` (`xll_lifecycle.{h,cpp}`) is a one-shot atomic
   CAS around a single `LogTeardownWarn`, called from the generated UDF wrappers'
   `g_phost == nullptr` guard (one line in `xll_main.cpp.tmpl`, body in the asset —
   the latch is what keeps a per-CELL guard from producing a per-cell log) and from
   `RtdServer::ConnectData`'s and `SendCommandInvoke`'s STA entry gates.
   * **`LogTeardownWarn`, NOT `LogWarn`/`SAFE_LOG_*`** — Phase 2 latches
     `g_isUnloading` as its first act and every ordinary logger short-circuits on it
     (§20.2.1 rule 4), so a report routed through them would be invisible in exactly
     the state it describes. Asserted NEGATIVELY in
     `internal/assets/lifecycle_post_teardown_cpp_test.go`; a reviewer "restoring
     consistency" is the regression that pin exists for.
   * The RTD/command sites report from the **STA entry gate**, gated on
     `g_isUnloading && !g_phost` (Phase 2's own marks) and NOT on
     `TeardownStarted()`: the latter is also true during a genuine quit's Phase 1,
     where an arriving `ConnectData` is entirely normal, so it would cry wolf on
     every clean shutdown. They are deliberately NOT reported from the `!g_phost`
     check inside the detached lambda — `LogTeardown*` must never be called from a
     detached thread.
   * **Cost, accepted:** `LogTeardownWarn` takes `g_logMutex` (a `std::timed_mutex`,
     bounded 50 ms wait) from a WORKSHEET-FUNCTION context, so worst case is one
     cell's evaluation delayed ≤50 ms, once per session. Do NOT "improve" it to
     `try_lock` — that drops the line under ordinary microsecond contention
     (explicit 2026-08-02 finding).
   * A FAIL-before gate must assert **the presence of the post-teardown-use line**,
     never a verdict enum: treating `kCleanLoad` as FAIL misreads the NORMAL case
     (an `Application.Quit()` that delivers no `OnBeginShutdown`, nothing latched).

   **NOT taken, and it needs its own design pass + sign-off:**
   `xll::EnsureSessionAlive()` from the same guard, routing through
   `PrepareForFreshLoad()` and re-running the init sequence. Feasible (the UDF guard
   runs on the STA and the functions are still registered, so no `xlfRegister` re-run
   is needed) but it turns every "host is gone" error path into a process-LAUNCHING
   path, and it would have to refuse on `kUnrecoverable`, refuse while
   `g_isQuiescing` is latched, never be entered from a detached thread or `DllMain`,
   and be bounded to one resurrection per session. On a pinned-after-host-shutdown
   image it is also the only thing that could un-zombie the session — which is
   exactly why it could resurrect the add-in during a genuine shutdown if the flag
   reasoning is wrong.

   **STILL UNMEASURED, and §20.2.1 must not be edited on the inference:** pin-site-2's
   rationale rests on "Excel unmaps the moment `OnDisconnection` returns", while a
   re-enable producing `kResetAfterTeardown` (flags still latched ⇒ no
   `DLL_PROCESS_ATTACH`) says the image was NOT unmapped on disable — the XLL
   manager's own reference holds it. Measure it (log `lpReserved` at DETACH on a
   disable) and then correct whichever record is wrong.

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
second `COMAddIns…Connect` cannot land while the bounce is in flight. (`TryConnectRibbon`,
`s_inConnect` and the close-by-identity helper `GetActiveWorkbookName` are static ASSETS since
2026-08-02 — §18.11.1; `GetExcelApplicationOrBounce` itself stays in `xll_main.cpp.tmpl` because
its body is `ribbon.bounce`-branched.)

The calc-end retry (`TryConnectRibbon("calc end")`, `allowBounce=false`) is KEPT as
a hazard-free defensive fallback: it is an Excel-registered event callback (no unmap
hazard) and only matters in the rare case the bounce itself fails (e.g. C API
unavailable). Graceful degradation throughout: a failed bounce logs a warning and
leaves functions/commands fully operational.

**`ribbon.bounce` modes (DLP interop, 2026-07-20).** The bounce's workbook
create/close fires third-party `Workbook` event hooks at a hostile time: DLP /
document-classification COM add-ins hook `WorkbookBeforeClose` with a
modal classification prompt, and closing an unclassified scratch book mid-`xlAutoOpen`
— before Excel's UI (and the add-in itself) finished initializing — can crash or hang
Excel at startup. `ribbon.bounce` in `xll.yaml` therefore selects one of three
template-gated shapes (`RibbonConfig.BounceMode()` maps unset → `full`; templates must
call `BounceMode`, never raw `.Bounce`, because generator tests construct configs
directly and skip default application):

- **`full`** (default) — the historical create + connect + close-by-identity sequence,
  hardened (2026-07-20): the close is wrapped in `ScratchCloseEventSuppressor`, which
  sets `Application.EnableEvents=false` + `DisplayAlerts=false` before `xlcFileClose`
  and restores the ORIGINAL captured values afterwards. The restore is load-bearing —
  leaving `EnableEvents=false` would silence every add-in's Workbook events for the
  session — so it does NOT rely on the dtor alone: the suppression state lives in a
  file-static pending record (AddRef'd Application + captured originals + armed flags)
  and the restore is the idempotent `RestorePendingEventSuppression()`, called from the
  dtor (normal path) AND from `xlAutoOpen` right after the connect attempt's
  `XLL_SAFE_BLOCK` — the belt-and-braces replay for the §20.3 SEH residual (an async
  fault inside `xlcFileClose` unwinds via `__except` WITHOUT running C++ dtors on
  /EHsc; xll-cpp-reviewer HIGH, 2026-07-20). Restores only what was actually flipped
  (armed flags, property-by-property so partial construction is covered), never a
  blind `=true`; a failed restore put is logged, not swallowed. The guard itself now lives in
  `internal/assets/files/{include/com/scratch_book.h,src/scratch_book.cpp}` (§18.11.1), so its BODY is
  pinned by `internal/assets/scratch_book_cpp_test.go::TestScratchCloseSuppressorRestoreContract`
  while `TestRibbonBounceFullSuppressesEventsAroundClose` pins the per-mode WIRING (which mode
  instantiates it, guard-before-close, and the post-SAFE_BLOCK replay); compiled by
  `cmd/cpp_compile_gate_bounce_test.go` (full is the only mode that renders it).
- **`keep-open`** — create the scratch workbook, connect, and **never close it** (no
  `xlcFileClose` is even emitted; the close-by-identity machinery
  `GetActiveWorkbookName`/`xlfGetDocument` is not rendered, and `xlcWorkbookInsert` is
  skipped so the leftover book is a plain 1-sheet Book1). This removes the
  highest-risk hook point (`WorkbookBeforeClose`) while keeping the ribbon connected
  at startup. Cost: a blank Book1 stays open (classic empty-start Excel behavior).
- **`off`** — never bounce (no `xlcNew` at all); the COMAddIns connect defers to the
  post-load retries, so the ribbon tab appears when the first workbook opens. The
  escape hatch when even scratch-workbook *creation* (`NewWorkbook` hooks) is hostile.
  Two retries cover this mode: the calc-end fallback (fires only if the book actually
  recalculates) and the bounded `xlcOnTime` connect retry armed at `xlAutoOpen`
  (§23.6 "§3"), which covers the manual-calc / no-formula book. The OnTime retry's
  "waiting for a workbook" window is finite (~10.5 min) — see the §3 FOLLOW-UP entry
  for the accepted residual hole. The `xlcOnTime`-at-`xlAutoOpen`-with-no-workbook step
  is no longer an assumption: it was proven accepted AND dispatched on real Excel
  (2026-07-26 probe, 3/3 chained dispatches — see the §3 FOLLOW-UP entry).

Pinned by `gen_ribbon_bounce_test.go` (keep-open/off shapes, unset≡full) and
`config_test.go` (value validation).

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
* `DestroyRtdNotifyWindow()` — called from the teardown's **Phase 1** (`BeginQuiesce`), on the
creating STA and AFTER the worker reap (`RunDestructiveTeardown` repeats it idempotently) (no post can race),
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

* **Office add-in disconnect re-entrancy: the hook's explicit `COMAddIns…Connect =
  false` is BOTH required AND conditionally skipped** (measured 2026-07-30). Two
  opposite "cleanups" are both WRONG; this entry exists to refuse both.
  * **The crash.** `GracefulComTeardownHook`'s step (0) sets
    `Application.COMAddIns.Item(progId).Connect = false`. When the teardown was entered
    from `RibbonAddIn::OnDisconnection`, that RE-ENTERS the same `mso.dll`
    `put_Connect(false)` already on the stack: the nested call completes Office's
    disconnect and clears the interface pointers Office caches on its `COMAddIn`
    object, then the OUTER `put_Connect` resumes and `Release()`s one of them
    unconditionally — **NULL vtable READ**. `EXCEL.EXE` `0xC0000005` at
    **`mso.dll+0xa1d19e`**. Same-source-tree control: **3/3 crash** with the nested
    disconnect executing, **0/6 crash + 6/6 PASS** with the skip. Window-close path
    logged the SKIP **0/8** times, i.e. that path is unchanged (there
    `OnBeginShutdown` wins the CAS first and the explicit disconnect still runs; a
    later `OnDisconnection` with `RemoveMode=1` = `ext_dm_UserClosed` is the nesting
    our OWN `put_Connect` caused, so we are the outer frame and the mechanism does not
    apply).
  * **NOT a regression of anything recent.** The offending call was introduced
    2026-06-11 (`d88911c`) and wired into the hook 2026-06-17 (`5afb1d0`); v0.8.41's
    close-time unmap fix (`b51523d`) touches it zero times, and the crash reproduces
    identically on v0.8.40.
  * **DO NOT delete the call.** On the `OnBeginShutdown` path Office has NOT started
    its add-in disconnect, and the explicit disconnect is what makes Excel release its
    `RibbonAddIn` reference EARLY. Deleting it loses that on the window-close path,
    which is the common one.
  * **DO NOT delete the guard.** It is not "a redundant double-check": without it the
    add-in-disable path crashes Excel 100% of the time. Pinned by
    `internal/generator/gen_office_disconnect_guard_test.go` and
    `internal/assets/office_disconnect_guard_cpp_test.go`, which assert ORDER and
    BRANCH STRUCTURE — a substring assert cannot pin this (the pre-existing
    `gen_cancel_quit_test.go` check for the string `"SetRibbonConnected(false)"` stayed
    GREEN with the whole guard removed).
  * **DO NOT convert the flag to `thread_local`.** The COM class is
    `ThreadingModel="Both"`, so a configuration that delivers `OnDisconnection` and the
    hook on different threads would make a `thread_local` MISS the skip — i.e. fail
    toward the crash. The process-wide atomic can only over-skip, and an over-skip is
    harmless (Office is disconnecting us anyway, and on a confirmed host shutdown the
    §20.2.1 PIN already prevents the unmap). The mis-diagnosis direction is the correct
    one; keep it.

* **`pkg/algo/greedy_mesh.go` int32-wrap boundary guard is unnecessary** (proven
  2026-06-13). `nextCol/nextRow = MaxInt32+1` wraps to `MinInt32`, but `GreedyMesh`
  sorts cells `(Row,Col)` ascending, marks `visited`, and expands; the wrap target
  `MinInt32` is the global minimum so it is **always processed/visited first**, and
  the `grid[next] && !visited[next]` guard blocks any wrong merge. The wrap is
  unreachable by any API/reuse path (invariant proof) — no observable behavior
  change exists, so a guard would be unverifiable dead code. Do not add it.
* **The calc-end caches do NOT need a `xleventCalculationCanceled` clear**
  (measured 2026-07-26, §19.4). Excel fires `CalculationCanceled` and then
  `CalculationEnded` **2–6 ms later with no work in between** (3/3 real-ESC
  interruptions on Excel 16.0.20131.20154), so `HandleCalculationEnded` — RefCache
  clear, `g_sentRefCache.clear()`, rtd-once `ClearNonMemoized`, the calc-end
  round-trip — already runs on a cancelled cycle. The recurring proposal
  "`refCache_` survives an Esc into the next cycle, register Canceled and clear
  there" is based on a false premise. Do not re-propose as a cache fix.
  `MSG_CALCULATION_CANCELED` (132) **is** wired as of v0.8.36, but strictly as a
  user-event NOTIFICATION: `HandleCalculationCanceled` clears nothing (no
  `CommandBatcher.Clear()` — that would drop the cycle's batched commands a few
  ms before the Ended flush; no `RefCache.Clear()` — that would desynchronize
  `g_sentRefCache` from the Go `RefCache`). See §19.4.1. Adding any clear back to
  that path is the regression this bullet exists to prevent.
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
* **`CacheManager::GetOrComputeRefHash`'s `xltype` guard is load-bearing, NOT a
  duplicate of its caller** (over-defensive-logic audit, 2026-06-25). The caller
  admits `xltypeRef | xltypeSRef`, but the function narrows to `xltypeRef`;
  because `xltypeRef (0x0008)` and `xltypeSRef (0x0400)` are distinct
  non-overlapping bits (from `types`/`xlcall.h`), an SRef-only input is selected
  by that narrowing. It is a behavior-selecting branch (**skip the per-`(sheet,
  rect)` RefCache**, which only an `xltypeRef` carries the key material for) not a
  no-op guard. Do not remove or "simplify". Likewise, the incoming XLOPER12 is
  **raw Excel input** — its type guards are external-boundary validation and stay
  regardless.
  **CORRECTION (2026-07-25):** the *early-return value* on that non-`xltypeRef`
  path WAS a bug, distinct from the guard itself. It returned an **empty string**,
  so `MakeCacheKey` contributed nothing for an `xltypeSRef` argument and every
  single-area reference argument collapsed onto one cache key (a call on `A1:B2`
  could be served the result computed for `C5:D9`); the unreachable
  `else if (xltype == xltypeSRef) ss << computeFn(pRef)` branch below the guard
  showed the intent was to compute. The non-`xltypeRef` path now **calls
  `computeFn` directly** — it skips the RefCache, not the hash. Regression:
  `internal/assets/cache_cpp_test.go::TestGetOrComputeRefHashHashesNonRefArgs`
  plus the native harness case "SRef argument content reaches the cache key".

### Over-defensive-logic audit (2026-06-25) — the boundary rule

A cross-repo audit hunted for guards made redundant by a caller's
completeness/safety guarantee. Across all four repos exactly **one** guard was
removed (a dead intra-function length re-check in `shm`); every xll-gen candidate
was correctly kept. The durable lesson: **a guard is safe to delete only when (a)
it lives entirely inside one trust domain over an internally-produced value, (b)
an earlier check or the caller's predicate *logically implies* it, and (c) it is
a pure no-op-on-pass guard, not a behavior-selecting branch.** It must stay when
it touches an exported API, the C++↔Go SHM wire, raw Excel `XLOPER12` /
FlatBuffers input (the UDF-argument and `xlAutoFree`/callback surfaces are all
external boundaries), a raw `operator[]` over independently-mutable state, or a
real initialization-order window. Local provability is necessary but never
sufficient.

### Final §6 migration (2026-07-26) — the workspace backlog's list is retired

The two entries below were the last ones still living only in the workspace
`IMPROVEMENT_BACKLOG.md` §6. That section is now retired, so this file is the
sole home for xll-gen's do-not-re-propose decisions.

* **The sync/async `any` argument's RefCache-MISS fallback (pass-through) is
  intended, test-pinned behavior** (adversarial verification 2026-07-23,
  REFUTED). A production XLL always serializes sync/async `any` INLINE —
  `ConvertAny` has no RefCache-emitting case — so there is no miss to "fix". A
  review that reports the fallback as a correctness hole has mistaken the RTD
  composite-arg path (which does use content-hash tokens) for the sync/async
  one. Do not re-report.
* **The rtd-once GRID path was never part of the error-memoize-sticking class**
  (verified 2026-07-23). The grid-once gate in `xll_rtd.cpp` bypasses the scalar
  `StoreResult` entirely, and `RtdOnceGridRegistry` stored only successful
  `MSG_RTD_ONCE_GRID` payloads; only the SCALAR path ever participated in that
  bug. Do not re-report the grid path for error-memoize sticking.
  **SCOPE — read this before citing the bullet (2026-07-26):** it says the grid
  path is not part of *that specific* class. It does **not** say the grid path
  had no error handling defect. A different one existed and was fixed in
  v0.8.37: a failing one-shot grid handler had nowhere to put its message, so
  the failure was dropped and the wrapper fell through to re-issue `xlfRtd`
  against an already-connected topic, wedging the cell at the loading
  placeholder forever. `StoreError` + transient entries + `OnceGridLookup` fixed
  it, verified on real Excel by
  `xll-gen-showcase/tools/verify-gridonce-error-uia.ps1` (4/4). Treat the
  original bullet as narrow, not as a blanket "the grid path is fine".

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
* **DONE (2026-08-03) — the "keep generated templates minimal, prefer static code" pass is FINISHED for `xll_main.cpp.tmpl`.** Four more relocations landed on top of the 2026-08-02 batch (lifecycle/jobpool/bootstrap, ribbon connect, scratch book, log bootstrap): the **OnTime retry chain** (§18.11.2), the **rtd.throttle_interval applier** (§18.11.3), the **`FormatDoubleRoundTrip` topic formatter** (§18.11.4), and the duplicate `{{if .Commands}}` include block was merged. `xll_main.cpp.tmpl` went 2190 → 2079 lines total; counting only ACTUAL CODE (C++ comments, template actions, template comments and blanks excluded) 737 → 664, i.e. **−73 lines of re-emitted-per-project logic, −10%**. `server.go.tmpl` is UNCHANGED (615 / 272): after the 2026-08-02 lifecycle/jobpool/bootstrap moves everything left in it is per-function dispatch or a `{{range}}` over user declarations — genuinely template work, so there is nothing further to take out without inventing an abstraction the generator would then have to describe. **What deliberately did NOT move, and why, so this is not re-litigated:** the `#include` blocks (they are the gating), `GetExcelApplicationOrBounce` (`ribbon.bounce`-branched body), the COM-identity trio `g_ribbonCookie`/`GetRibbonClsid`/`g_szRibbonProgID` (§18.11.1), `DllGetClassObject`/`DllRegisterServer` (CLSID literals), `GracefulComTeardownHook` (must stay byte-identical, §18.11.1), the exported symbols themselves (§21), the per-function wrappers and the dispatch switch in either template, and the `{{/* */}}` template comments — those render to NOTHING and document the template itself, so they are already in the right place. Do not "reduce" them.

### 23.2 Tunability
* **DONE (2026-05-17):** `pkg/server/manager.go` — promoted the 30s cleanup tick and 60s TTL to `ChunkManager.CleanupInterval` and `ChunkManager.ChunkBufferTTL` fields backed by `DefaultCleanupInterval` / `DefaultChunkBufferTTL` constants. YAML wiring: `xll.yaml` `server.chunk: {max_buffer_bytes, cleanup_interval, buffer_ttl}` → `config.ChunkConfig` → generated `server.go` calls `server.NewChunkManagerFromConfig` with the values captured before the cleanup goroutine starts. Omitting `server.chunk` keeps the existing defaults — no behavior change for projects that don't opt in.

### 23.3 Test Coverage

**The C++ compile gate builds TWO configurations, and both are load-bearing (2026-07-26).** `cmd/cpp_compile_gate_test.go::TestRtdOnceNumGridCppCompiles` configures the generated project twice: once with `XLL_DEBUG` at its `OFF` default and once with `-DXLL_DEBUG=ON` (which defines `SHM_DEBUG` + `XLL_DEBUG_LOGGING`). Before this, only the release side of the template's `#ifdef XLL_DEBUG_LOGGING` blocks was ever compiled, so debug-only generated code could stay broken indefinitely with every gate green. Use a SEPARATE build tree for the second configure (`build-debug`), never a reconfigure of the first: CMake retains unspecified cache variables, which is the same trap `Taskfile.yml` guards with an explicit `-DXLL_DEBUG=OFF`. FetchContent is already populated by the first configure, so the second costs a compile, not a download. Verified non-vacuous by injecting an undefined symbol inside an `#ifdef XLL_DEBUG_LOGGING` block: the release build passed and only the debug build failed. **Do not drop the second build to speed the gate up** — and when adding template code under `#ifdef`, this is the gate that covers it.

* **DONE (superseded 2026-08-02) — "RTD (`pkg/rtd/`) and async batching (`pkg/server/async_batcher.go`) still lack unit tests" is no longer true.** Both are covered and v0.8.39 added more: `pkg/rtd/` has `manager_test.go` (Publish/SendUpdate/SendOnceGrid incl. the chunked path), `manager_cancel_test.go` (the connect-cancel generation machinery, above), `runonce_test.go` (RunOnce/RunOnceGrid success, handler error, nil guards, cancellation) and `manager_stop_test.go` (every send path rejects after Stop; Stop waits for the in-flight send, reports false on a hang, is idempotent); async batching has `async_batcher.go`'s split/oversize/undeliverable cases in `async_batcher_test.go` and the teardown contract in `batcher_stop_test.go`. The four edge cases this bullet originally asked for belong to chunk reassembly and are covered by `pkg/server/manager_test.go` (see the chunk bullet below). Do not re-file this as a gap.
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
* **DONE (2026-06-20) — R24: chunk frame + split loop + chunk-size constant unified into leaf `pkg/chunk`.** The chunk-split loop, the `protocol.Chunk` frame build, and the 950 KiB chunk-size constant were copy-pasted across three sites with three intentionally-different transport models. They are now extracted to leaf package `pkg/chunk` (imports only flatbuffers + `types/protocol`; the `Sender.Send` retry path is transport-agnostic via a `SendFunc` callback so it pulls in no shm/transferid dependency). Like `pkg/msgid`/`pkg/transferid`, being a leaf dissolves the server→rtd import cycle that previously forced `pkg/rtd.onceGridChunkSize` to hand-copy `pkg/server.DefaultChunkSize`. **Single source of truth:** `chunk.DefaultChunkSize`; `pkg/server.DefaultChunkSize` and `pkg/rtd.onceGridChunkSize` are now aliases of it. `chunk.BuildFrame` is the one frame builder; `pkg/server.BuildChunkResponse` delegates to it and the rtd-once hand-built frame is gone — output bytes are **byte-identical** (`XCHN` identifier, same field set/order), so the C++ `HandleChunk` reassembler is untouched (no `internal/assets/files/**` change). **Transport models stay separate (NOT unified):** `SendAckOrChunk` keeps its ACK-pull/`ChunkManager` model and shares only the frame builder — the §23.3 Offset-publication-before-`AddOutgoingChunk` invariant and its load-bearing comment are preserved verbatim; `sendChunkedAsync` keeps push + `chunk.AsyncRetry` (10× exp backoff); `SendOnceGrid` keeps synchronous push. **(Amended v0.8.32 — read the ACK-pull clause as history.)** `SendAckOrChunk` no longer chunks: there is no C++ ACK consumer (`MSG_ACK` has zero senders anywhere in the assets, templates or `mock_host.cpp`, and the sync UDF parses the response as a `Response` without checking msgType), so an oversized payload is now refused with `(0, MsgTypeSystemError)` and nothing is written to `respBuf` — the caller sees `res.HasError()` BEFORE any `GetRoot`. The ACK-pull registration logic still exists, moved intact to `registerAckPullChunk`, which is production-unreachable and kept only so a future C++ consumer inherits the Offset-publication invariant rather than re-deriving it. Do not describe `SendAckOrChunk` as a chunking path. **Retry-policy divergence made explicit (was an implicit gap):** the policy is now a typed `chunk.RetryPolicy` argument. async = `chunk.AsyncRetry`; `SendOnceGrid` = `chunk.NoRetry`, a DELIBERATE choice documented at the call site — the synchronous path must surface the first send error immediately so `RunOnceGrid` does not signal RTD readiness for a grid the host never received (a stuck retry would block readiness anyway; the RTD layer re-drives the whole one-shot). Regression: `pkg/chunk/chunk_test.go` — `TestBuildFrame_ByteIdenticalToLegacy` (matrix vs a verbatim copy of the old hand-built frame; FAIL-confirmed by flipping the file identifier), `TestSender_SplitBoundaries` (sub/exact/over-chunk offset progression + lossless reassembly), `TestSender_RetryPolicy` (retry rides out N transient fails; `NoRetry` fails on attempt 1; mid-stream chunk failure aborts the transfer). The pre-existing `pkg/rtd` `TestSendOnceGrid_Chunked` and `pkg/server` batcher tests act as integration regressions and still pass (byte-identical, correctly-offset chunks). All pass under `-race -count=10`.
* **DONE (2026-07-26) — the guest→host chunk-budget slot probe is gone; one shared `chunk.GuestBudget` replaces two hand-mirrored copies.** Learning the real request-buffer capacity used to require *claiming* a guest slot (`AcquireGuestSlot()` → `len(slot.RequestBuffer())` → `Release()`), because shm exposed the geometry nowhere else — and because `pkg/server` imports `pkg/rtd` (`NewSystemHandler`), that probe existed TWICE, as `pkg/server.guestRequestBudget` and `pkg/rtd.guestRequestBudget`, with a "keep the two in sync" comment on each. shm **v0.8.15** added `Client.MaxRequestSize()` / `DirectGuest.MaxRequestSize()` — a pure read of `respOffset - reqOffset` from the header the Host published at Init, claiming no slot and writing no shared memory. Both probes are now one function in the leaf, `chunk.GuestBudget(any) int` (+ the `chunk.MaxRequestSizer` interface), which the server→rtd cycle no longer blocks. **Value-identical by construction:** shm slices `reqBuffer` from that same `respOffset - reqOffset`, so `MaxRequestSize() == len(GuestSlot.RequestBuffer())`. **Framing deducted exactly once:** `MaxRequestSize` is RAW capacity (shm subtracts nothing, and its own 24 B streaming `ChunkHeader` is `StreamSender`'s business — xll-gen does not use shm streams), so the single `Budget(cap) = cap - FramingOverhead` subtraction is the whole deduction. **The fallback survives**: the accessor answers 0 for a nil / not-connected client ("unknown" per its docstring), and 0 → `chunk.DefaultChunkSize`, never `Budget(0) == 1`. Side benefit — the probe was one non-blocking pass and silently fell back to the compiled-in default whenever every guest slot happened to be busy, i.e. exactly under load; a geometry read cannot fail that way. Regressions: `pkg/chunk/chunk_test.go::TestGuestBudget` + `TestGuestBudgetMatchesBudgetOfTheProbedCapacity` (arithmetic identity vs the retired probe across the capacity range), and against a REAL client on a real segment `pkg/server/realshm_chunk_test.go::TestRealShmClient_MaxRequestSizeMatchesTheSlotProbe` (both the 1 MiB and 256 KiB slot geometries — the small one is the discriminating case, since a broken accessor returning 0 would still land on the default budget at 1 MiB), `TestRealShmClient_BudgetIsAvailableWithEveryGuestSlotBusy`, `TestRealShmClient_BudgetFallsBackWhenUnknown`. The whole 2026-07-25 real-shm suite (512 KiB / 1 MiB / 3 MiB async + once-grid, `FlushAsyncBatchSilentLossBand`, `BudgetAdaptsToSmallerSlot`) passes unchanged — the budget value does not move.
* **DONE (2026-07-26) — HIGH: the per-cycle RefCache was stale across ITERATIONS of a circular calculation.** `CacheManager::refCache_` is keyed on pure coordinates (`RefKey{idSheet, rwFirst, rwLast, colFirst, colLast}`) and valued with the range's VALUE digest, and its only clear point is `HandleCalculationEnded`. Measured against real Excel (§19.4): `xleventCalculationEnded` fires **once per calculation cycle regardless of how many iterations that cycle runs**, so with `Application.Iteration = True` a cache-enabled function taking a reference argument inside a circular group kept resolving pass 1's digest for passes 2..N — same `MakeCacheKey` → result-cache hit → the cell froze at its first-pass value and the circular system converged on a wrong number. **Fix:** an iterative-calculation gate on the CacheManager — `RefreshIterativeCalcMode()` reads `GET.DOCUMENT(15)` at calc end (a valid command context; macro-sheet only, hence not callable from the `$` wrappers) and, while it is on, `GetOrComputeRefHash` bypasses the map lookup AND the store while still folding `computeFn`'s digest into the accumulator, so the emitted key bytes are byte-identical in both modes. The Excel query is skipped entirely unless a reference argument actually took the RefCache path during the cycle (`refPathUsed_`), and that flag is raised even while bypassing so the gate can be turned back OFF. `xll_cache.cpp` stays free of cross-TU calls (the transition is logged by the caller in `xll_events.cpp`) because the offline g++ gate links it against nothing but a stub `Excel12v`. Regressions: `internal/assets/cache_cpp_test.go::TestIterativeCalcGateWired` (source markers incl. the refresh-before-clear ORDER) and `internal/assets/testdata/cache_native_test.cpp::TestIterativeCalcBypassesRefCache` (behavioral: identical key bytes across modes, re-coercion per pass, a mid-cycle value change yields a NEW key, gate-off still memoizes, a failed query leaves the gate untouched, reversible). FAIL-before confirmed by neutering the gate to `bypassMemo = false` — 2 failures ("coerce calls: 1", stale key).
  * **First-cycle hole CLOSED (MED, same day).** Refreshing only at calc END meant the gate could not be armed before the first calculation ever ran: a workbook **saved** with `Application.Iteration = True` opened with the gate off, memoized pass-1 digests through that entire first cycle, and the wrong value then persisted for the whole session unless the cell was dirtied again — i.e. exactly the symptom the gate was built to remove. Fix: `CacheManager::ForceRefreshIterativeCalcMode()` — the same `GET.DOCUMENT(15)` query with the `refPathUsed_` precondition removed (at load that flag is necessarily false, so the gated entry point would never ask) — called **once from `xlAutoOpen`**, which is a valid command context (§19.4 / §23.6). A failed query (no active workbook yet) leaves the gate untouched, so the worst case is the previous behavior and the change cannot regress anything. Both entry points share the private `QueryIterativeCalcMode()`, so there is one query implementation. Regressions: `internal/assets/cache_cpp_test.go::TestIterativeCalcGatePrimedAtLoad` (template marker, FAIL-confirmed by removing the call) and 4 new behavioral checks in `cache_native_test.cpp` (the gated refresh makes ZERO Excel calls before any RefCache use; the forced one asks and arms; it can also clear; a failed forced query is a no-op) — the harness stub now answers `xlfGetDocument`. `internal/templates/xll_main.cpp.tmpl` changed, so `internal/generator/testdata/golden/xll_main.cpp.golden` was regenerated (`UPDATE_GOLDEN=1`; +25 lines, the only golden affected).
  * Memory ordering on `iterativeCalc_`/`refPathUsed_` was tightened from `relaxed` to release/acquire (zero extra instructions on x86 — plain MOV, and the exchange is already a locked XCHG) so the pairing with the `refCache_` operations the gate guards is stated rather than inferred from the target ISA.
  * The "refresh must run BEFORE `ClearRefCache` because the clear consumes the flag" comment in `xll_events.cpp` (and the matching assertion rationale in `cache_cpp_test.go`) described a dependency that **does not exist** — `ClearRefCache()` only empties `refCache_`. Both are reworded: the order is an INTENT pin (refresh decides on the cycle just observed, the clear ends it), not a correctness requirement. The assertion is kept because a future change that makes the clear reset the gate flags would make it load-bearing.
* **DONE (2026-07-26) — MED x3: the chunk reassembler's reject paths were not actually rejections.** Three defects on the same C++/Go mirror pair (`internal/assets/files/src/xll_worker.cpp` ↔ `pkg/server/handlers.go` + `manager.go`), fixed together as one §18.6 co-change:
  1. **A zero-length segment poisoned a healthy transfer.** A `protocol.Chunk` with a PRESENT but EMPTY data vector passed every guard (C++ only tested `!chunk->data()`; Go had no length test at all), so `ClaimSegment`/`receivedSegments` recorded an `(offset, 0)` range — and the REAL chunk arriving at that offset then classified as "same start offset, different length" → `ClaimOverlap` → the whole, otherwise perfectly healthy, transfer was discarded, with the diagnostic naming the innocent chunk. A zero-length segment advances nothing and can never be part of a valid transfer; both sides now refuse it explicitly.
  2. **A rejection was not final — the next chunk RESURRECTED the transfer.** Every reject path erased the buffer but remembered nothing, so the producer's next chunk found no buffer, a FRESH one was allocated, and the chunk was acked as SUCCESS. The resurrected buffer is missing every earlier chunk (can never complete; squats a `kMaxPartialMessages` / `MaxConcurrentTransfers` slot for the full 60 s TTL) and, worse, `pkg/chunk.AsyncRetry`'s first retry saw success and therefore never aborted — the async call hung to its own timeout. Exactly inverted from the fail-fast the refusal exists for. Fix: a **TTL-bounded poison set** (`g_poisonedTransfers`, `std::map<uint64_t, steady_clock::time_point>`; `ChunkManager.poisoned`, `map[uint64]time.Time`) consulted at the top of `HandleChunk`; every later chunk on a rejected id answers SYSTEM_ERROR. Entries expire on the same clock as reassembly buffers (drained by the existing stale prune, so an id is never permanently burned) and the set is capped at 1024 with oldest-eviction. **Poison set chosen over the minimal `offset == 0`-only-creates alternative**: the minimal fix would have baked in an unstated "producers send ascending" assumption (only true for two of the three senders — `SendAckOrChunk` is ACK-pull driven) and would still ack the resurrecting `offset == 0` retry, which is precisely the frame `AsyncRetry` sends first. **Deliberate asymmetry:** only PROTOCOL VIOLATIONS poison (out-of-bounds, zero-length, overlap — deterministic properties of the producer's framing). RESOURCE refusals (total cap, concurrent-transfer cap) do NOT: they insert no buffer, so there is nothing to resurrect, and a later retry may legitimately succeed.
  3. **C++ had no `total_size == 0` guard** (Go's `GetChunkBuffer` refuses `total <= 0`). `resize(0)` satisfies the `0 == 0` completion test immediately and hands `GetRoot<BatchAsyncResponse>` a **nullptr** from `buffer.data()` → access violation inside Excel. Unreachable from an honest Go producer, but this is the wire; closed for reject-path symmetry.
  Regressions: `pkg/server/manager_test.go::TestChunkManager_ZeroLengthSegmentRejected` and `::TestChunkManager_RejectedTransferIsPoisoned` (per-id scope, TTL expiry, resource-refusal must NOT poison) — FAIL-before confirmed by neutering both guards (6 failing sub-cases); plus regtest mock-host cases **16c** (rejection is final: neither a mid-stream continuation nor a from-scratch re-open may be acked), **16d** (poison is per-id), **16e** (empty data vector refused).
* **DONE (2026-07-26, v0.8.36) — a user-declared `CalculationCanceled` event never reached the Go handler.** `internal/templates/xll_main.cpp.tmpl`'s named-event stub (`{{else}}` branch, "Currently not fully implemented for named events") only logged, so `MSG_CALCULATION_CANCELED` (132) was never sent and the whole Go chain — `HandleCalculationCanceled` → `OnCalculationCanceled` (`server.go.tmpl`, `interface.go.tmpl`, documented in `README.md`/`TUTORIAL.md`) — was dead code in generated projects; only the regtest mock host exercised it. **Resolved as a product decision, with the semantics settled up front (full write-up in §19.4.1):** the event is opt-in and now forwards via a new `xll::HandleCalculationCanceled()` asset function; a cancelled cycle fires **both** `OnCalculationCanceled` and `OnCalculationEnded`, in that order, and that is the documented contract rather than something to hide. The two traps flagged here were both avoided: the cancel path clears **nothing** (no `CommandBatcher.Clear()` — a cancel is "the calculation was interrupted", not "discard the writes"; no `RefCache.Clear()` — the C++/Go clears stay on the SAME event so `g_sentRefCache` cannot diverge), and `onCanceled` is invoked **synchronously** so the guest cannot invert the measured Canceled → Ended order. No named event remains a no-op: Excel's C API defines exactly the two `xlEvent*` constants that `config.validEventTypes` admits.
* **DONE (2026-07-26) — MED: `BuildRtdOnceGridResult` started every grid on a fixed 1 KiB FlatBuffers builder.** `pkg/server/converters.go`'s rtd-once grid serializer (`internal/templates/server.go.tmpl`'s only caller) allocated `flatbuffers.NewBuilder(1024)` regardless of the grid's size, so every large result walked the whole doubling-realloc ladder from 1 KiB — each doubling copies the entire buffer, i.e. **O(payload) of pure memmove on top of the serialization**. It is the only NON-POOLED builder on a payload-sized path (the async batcher, `pkg/chunk` and `pkg/pool` builders are pooled and amortize their growth), so nothing else absorbed it. **Fix:** pre-size from the grid geometry via the new `fbany.GridBuilderSize(v, bytesPerCell)` + `fbany.AnyGridBytesPerCell` / `fbany.NumGridBytesPerCell`.
  * **The per-cell constants are MEASURED, and 24 is wrong.** A `[][]any` cell (a `Scalar` table + union member + vtable + vector slot) encodes at **~28 B/cell**, so the intuitive `rows*cols*24+512` is an UNDER-estimate: the last doubling still fires at 1000×1000 and the win collapses to zero. `AnyGridBytesPerCell = 32` (next power of two above the measurement) is what actually removes the ladder. `[][]float64` is exactly 8 B/cell (dense doubles, no per-cell table) and `rows*cols*8+512` is effectively optimal (82,000 B/op against an 80,080 B payload).
  * **Measured (Ryzen 9 3900X, `-benchtime 200x -count=3`, best of 3):** numgrid 100×100 **−48% ns / −82% B/op** (18→4 allocs); numgrid 1000×1000 **−32% ns / −71% B/op** (30→4); any 100×100 **−23% ns / −80% B/op** (25→7); any 1000×1000 **−68% B/op**, 37→7 allocs, ns within noise (the per-cell table building dominates at that size). Tiny grids are unaffected (a 10×10 numgrid's 1,312 B hint is a ~180 B overshoot vs the old 1,024 default).
  * **Safety:** the initial capacity is a pure allocation HINT — `FinishedBytes()` is byte-identical whatever it is. Pinned by `pkg/server/converters_test.go::TestBuildRtdOnceGridResult_PresizedBytesIdentical`, which diffs against a verbatim copy of the old fixed-1-KiB function over 10 shapes. `GridBuilderSize` also CLAMPS (degenerate geometry → 512; anything over 256 MiB → 256 MiB) so a hostile `rows*cols` near the int32 limit `ValidateGridDims` rejects cannot turn a clean error into a 64 GiB allocation.
  * **CO-CHANGE (§18.4):** if the R22 remainder ③ (moving the marshalling fragments into `internal/generator`'s `typeRegistry`) is ever picked up, these per-cell figures belong on **`TypeInfo.BytesPerCell`**, not in `internal/fbany`. Noted at both the constant and the `GridBuilderSize` doc comment.
  * **Reviewed and NOT applied elsewhere:** `fbany.BuildGrid`'s `cellOffsets` is already `make(..., 0, rows*cols)` and measures a scale-invariant ~0.4% of the function, and `fbany`'s builders are all CALLER-SUPPLIED — pre-sizing can only happen at a creation site, and every other creation site is pooled/reused, so there is no second application of this fix to make.
* **DONE (2026-07-26) — MED: rtd/rtd-once built composite-argument payloads eagerly, before the cache lookup that discards them.** In `internal/templates/xll_main.cpp.tmpl` the rtd-once wrapper built the full `SetRefCacheRequest` (`ConvertGridArg`/`ConvertRange`/… + `rcb.Finish` + a whole-buffer `refPayload.assign` copy) inside the topic-string loop — i.e. BEFORE `MakeRtdOnceKey` + `TryGetResult`. On a memoize / `memoize_ttl` hit the wrapper returns the cached value **without calling `xlfRtd`**, so all of that work was thrown away. `protocol::Grid` is one union table PER CELL, so a 10k-cell grid argument is hundreds of microseconds to milliseconds of pure waste per hit. **Fix:** only the content-hash TOKEN stays in the topic loop (it is what makes the once-key content-addressed); the build now happens in the cache-MISS branch, directly at the ship site — which also deletes the `std::vector<uint8_t> refPayload` staging copy.
  * **Plain rtd** has no cache branch to move into: the dedup lives inside `SendRefCachePayloadOnce` (the per-cycle `g_sentRefCache` token set), so 100 cells sharing one range each SERIALIZED the grid and 99 of those buffers were dropped at the ship. The wrapper now peeks at `g_sentRefCache` under `g_refCacheMutex` (both already exported from `xll_ipc.h`) and skips the build on a repeat. Racing is benign both ways: a present entry means the payload is known-delivered (written only after a successful ack, cleared only at `CalculationEnded`), and a missed entry just re-enters `SendRefCachePayloadOnce`, which re-checks under the same mutex and no-ops. Never a lost ship.
  * **FOLLOW-UP DONE (2026-07-26, same day) — the peek now applies to rtd-once too.** The sub-bullet above scoped the `g_sentRefCache` peek to plain rtd, on the reasoning that rtd-once already has a cache branch. That reasoning is only half right: the once-cache spares the work on a HIT, but on the FIRST calc pass it is EMPTY, so all N cells sharing one grid miss it, all N fall into the miss branch, all N build, and `SendRefCachePayloadOnce` drops N−1 buffers — exactly the plain-rtd waste, just one cycle later. The identical `{ lock_guard; find; }` peek now guards the rtd-once build (placed AFTER the once-cache early-return so a memoize/TTL hit does not even peek, and BEFORE the `FlatBufferBuilder rcb(1024)` so the arena construction is skipped too). Safety argument is unchanged from plain rtd. Regression: `::TestGenCpp_RtdOnceComposite_SkipBuildWhenAlreadyShipped` (markers + lookup→peek→build→ship ordering + builder inside the guard); golden regenerated (hunks confined to the two rtd-once composite ship blocks).
  * **Do NOT double-count the win.** The hit path still pays `ContentHashToken`'s coerce + hash (already ~90× cheaper since the 2026-07-25 streaming FNV-1a), so the realized saving is roughly HALF the composite-argument overhead, not all of it. Scalar-only rtd/rtd-once functions gain nothing (no block is emitted for them).
  * **Verification:** the `xlCoerce` round-trip saving is not measurable without Excel; the justification is the mechanical certainty of the removed work. Placement is pinned by `internal/generator/gen_rtd_composite_test.go::TestGenCpp_RtdOnceComposite_BuildAfterCacheLookup` (token before the lookup, converter + `CreateSetRefCacheRequest` + ship all after it, and no `refPayload` left) and `::TestGenCpp_RtdComposite_SkipBuildWhenAlreadyShipped`. `internal/generator/testdata/golden/xll_main.cpp.golden` regenerated (`UPDATE_GOLDEN=1`; +243/−117, every hunk inside the RTD/rtd-once wrapper bodies). The `cmd` C++ compile gate fixture (`cppCompileGateYaml`) gained `rtd` + `rtd-once` functions with `grid`/`range`/`numgrid`/`any` arguments — **no gate compiled the composite-argument wrapper before**, so a scope error in this move would have shipped.
* **DONE (2026-07-26) — MED: `RtdOnceGridRegistry` had no error/transient entry, so a grid rtd-once failure never reached the cell and never self-healed.** The scalar rtd-once error path was fixed on 2026-07-25 (transient entries); the grid path was explicitly left open. Because `ProcessRtdUpdate` skipped the scalar `StoreResult` for grid-once topics and the grid registry had only "completed payload" entries, an `is_error` push for a `grid`/`numgrid` rtd-once topic was DROPPED — the cell kept the `loading_placeholder` / empty 0×0 FP12 forever, and since the wrapper only returns on a cache HIT, every recalc re-issued `xlfRtd` against a still-connected topic, so `ConnectData` never re-fired and the one-shot handler never re-ran. Not a regression (pre-existing), but asymmetric with scalar. **Fixed by mirroring the scalar transient semantics into the grid registry with NO wire-format change** — the message already crosses as `RtdUpdate.is_error=true`. Full design, the preservation matrix, and the "what does the cell show" decision (grid → the message text via `NewExcelString`; numgrid → the empty 0×0 FP12 the FP12 ABI forces, message to the log; the real win for both being the HIT that lets the topic disconnect and the entry be reclaimed) are in **§19.3, "Error values on a GRID/NUMGRID rtd-once topic"**. Files: `internal/assets/files/include/xll_rtd_once_grid.h`, `internal/assets/files/src/xll_rtd.cpp`, `internal/templates/xll_main.cpp.tmpl`, `pkg/rtd/runonce.go` (comment only). Regression: `internal/assets/rtd_once_grid_error_cpp_test.go::TestRtdOnceGridRegistryErrorBehavior` (g++ compile+run of the header, 8 assertion groups), `internal/assets/assets_test.go::TestRtdOnceGridRegistryTransientRetention` + the extended `::TestRtdUpdateErrorStoresTransient`, and `internal/generator/gen_rtd_once_test.go::TestGenCpp_RtdOnceGrid` (wrapper reads the three-valued lookup). FAIL-before / PASS-after verified by stashing the three runtime files. Golden regenerated (hunks confined to the two grid-once wrapper bodies).
* **(LOW, from the 2026-07-26 pre-release C++ review) — documentation accuracy batch, DONE 2026-07-26.** All five documentation items below are now written down; none changed runtime behavior. The substance lives in **§18.6.1** (the chunk-reassembler anchor) — read that, not this list:
  * **DONE** — `GetChunkBuffer` / `DefaultMaxConcurrentTransfers` no longer claim an "aggregate footprint" bound. Corrected in `pkg/server/manager.go`, `internal/assets/files/src/xll_worker.cpp` (`kMaxPartialMessages`) and `README.md`: it is a COUNT bound, the two caps MULTIPLY (256 GiB worst case at the defaults), and TTL pruning is defeated by a peer that keeps each transfer fresh.
  * **DONE** — the C++ caps (`kMaxChunkTotalSize`/`kMaxPartialMessages`/`kChunkStaleTtl`) being compile-time while the Go twins are `xll.yaml`-tunable is now documented as a **direction** fact, not just a tunability gap: `server.chunk` moves the host→guest (Go) reassembler only; guest→host is a separate reassembler with hardcoded limits. Wiring the C++ side through the template remains an open OPTION, not a plan.
  * **DONE** — the total-mismatch-on-reuse divergence (Go resets in place and continues; C++ keeps the first `totalSize`, so the re-open is discarded + poisoned) is recorded in §18.6.1 as an **intentional asymmetry**, with comments on both sides. Symmetrizing it is a separate change.
  * **DONE** — `HandleChunk`'s `chunkMutex`→`buf.Mutex` TOCTOU is documented at the call site with the reason it is safe (rejected segments are never written; the surviving segments are disjoint + in-bounds, so an exact `Received == TotalSize` still means complete coverage — only the OBSERVED outcome can differ) plus an explicit "do not widen the locking" note.
  * **DONE** — `QueryIterativeCalcMode`'s header comment now states the scope: `GET.DOCUMENT(15)` reads the ACTIVE workbook while `iterativeCalc_` is process-global. Practically irrelevant (`Application.Iteration` is effectively app-global) and worst case is a lost optimization, never a wrong value.
* **DONE (2026-07-26) — MED: no sender-side 256 MiB bound on guest→host transfers.** The C++ receiver refuses any transfer whose declared `total_size` exceeds `kMaxChunkTotalSize`, but the SENDERS (`pkg/server.FlushAsyncBatch` → `sendChunkedAsync`, `pkg/rtd.SendOnceGrid`, both through `pkg/chunk.Sender`) had no corresponding bound. An over-cap payload was framed and pushed anyway: the host refused the FIRST frame on its `total_size`, the `AsyncRetry` ladder absorbed ten `SYSTEM_ERROR`s, and the transfer was abandoned with **one `log.Error`** while every Excel cell waiting on it sat at `#GETTING_DATA` until Excel's own async timeout — no diagnostic in any cell. Realistic trigger is a **single** async grid of ~8–10M cells (`fbany.AnyGridBytesPerCell = 32`, so ≈8.4M cells reach 256 MiB; e.g. 2900×2900, or 1M rows × 9 cols); the 256-result batch case only ADDS "255 unrelated cells die with it". **Fix, in two layers:** (1) `chunk.MaxTransferBytes` + `chunk.ErrTransferTooLarge` — `Sender.Send` refuses an over-cap payload **before the first frame**, which covers the rtd-once path for free (its failure is already pushed to the cell as an error string by `RunOnceGrid`); (2) `FlushAsyncBatch` is now **size-aware**: a batch over the cap is split by **recursive halving on the REAL serialized size** (no per-result estimate to mispredict), and a SINGLE result that alone exceeds the cap — which splitting cannot fix — is **replaced by an error result** carrying a diagnostic, so `ProcessAsyncBatchResponse` renders it into the cell as a string instead of leaving the handle unanswered. A `substituted` flag makes the replacement a fixpoint (a pathological limit logs and drops rather than recursing). The oversize builder is **dropped, not pooled** (`heapBuilderPool` retains capacity by design; returning a 300 MiB buffer would pin it for the process lifetime). **Can the sender learn the receiver's cap? No** — `kMaxChunkTotalSize` has no template/YAML wiring and nothing on the wire negotiates a reassembly limit, so the constant is hand-copied and pinned by `TestChunkReceiverCapsMatchGoConstants` (reads the shipped header) — see the three-numbers table in §18.6.1, which also corrects the older "the two constants are in lockstep" wording. Regression: `pkg/server/async_batcher_test.go` (split / single-oversized / mixed batch / fixpoint / fitting-batch-unchanged / nil client, all driven with a small `limit` so no 256 MiB payload is materialized) and `pkg/chunk` `TestSender_TransferTooLarge`. FAIL-before confirmed by disabling the size check: 4 of 5 tests fail, including "the giant's 3 small siblings were delivered as values" flipping to "all four lost in one refused transfer".
* **OPEN (identified 2026-07-26) — the mirror-image gap: the C++ host→guest sender does not know the Go receiver's cap.** `server.chunk.max_buffer_bytes` is per-project tunable, but the XLL's chunked SEND path has no corresponding bound, so a project that lowers it gets exactly the failure mode the item above fixes, in the other direction (the Go `HandleChunk` answers `MsgTypeSystemError`, the XLL retries and gives up). Not fixed here because it needs `xll_ipc.h` / `xll_main.cpp.tmpl` (config → template wiring), which is the same change as "wire the C++ caps through the template" in §18.6.1. Defaults are unaffected: at 256 MiB both directions agree.
* **DONE (2026-07-26) — offline C++ unit gate for the transfer BOOKKEEPING (the remaining untested layer next to the segment arbiter).** Was: the admission rules and the poison set were the last part of `xll_worker.cpp` covered by nothing but "it compiles". Closed by extracting them into the pure `xll::ChunkRegistry` (`xll_worker.h`) — caps as constructor parameters, `now` as an argument — and gating it with `internal/assets/testdata/chunk_registry_native_test.cpp` driven by a SECOND shared table (`chunkRegistryCases`) in `internal/assets/chunk_cpp_test.go`, replayed against Go's `ChunkManager` and emitted as C++. 9 shared cases (open/resume/complete/reopen, zero-total, the size-cap boundary, prune-then-refuse at the transfer bound, poison refuse-until-TTL-then-expire, oldest-eviction ×2, prune-before-evict, sweep-expires-both) + 5 native-only property tests = **275 native checks**; g++ only, like its sibling. `TestChunkRegistryLogicIsExtracted` bans the re-inlined shapes (`g_partialMessages.find`, `g_poisonedTransfers`, `PoisonTransferLocked`, `PruneStaleChunksLocked`). Side-effect parity with the pre-extraction code was checked path by path — every `LogWarn` string is byte-identical (the "too many concurrent transfers" count now comes from `transferCount()`/`maxTransfers()`, same values), every erase/poison/return is unchanged, and the only new paths are two fail-closed `default:` arms. **FAIL-before demonstrated by five mutations of the shipped header:** dropping prune-then-refuse (the reclaim case flips to `toomany`), evicting the poison map's `begin()` instead of the oldest (caught only because the table includes a case whose id order is REVERSED against its time order — with ascending ids the two coincide and the mutation survives, which is why that case exists), never expiring poison records, accepting `total_size == 0`, and dropping the poison map from the TTL sweep. A sixth mutation on the Go side (`PoisonTransfer` no longer drops the live buffer) proves the Go replay is live too. Also identified and documented, NOT fixed: the cap-validation-on-an-already-open-id asymmetry (§18.6.1).
* **DONE (2026-07-26) — offline C++ unit gate for the segment/overlap logic.** Was: "`xll_worker.cpp` is only covered through the cmake gates and the regtest mock host." Closed by extracting the bounds check + zero-length refusal + overlap classification into the pure `xll::ClaimChunkSegment` (`internal/assets/files/include/xll_worker.h`) and gating it with `internal/assets/testdata/chunk_segments_native_test.cpp` + `internal/assets/chunk_cpp_test.go` — see §18.6.1 for the contract and the "do not re-inline" rule. The gate needs **only g++** (no types/flatbuffers/phmap headers, no FetchContent cache), because the unit under test is a pure function over `std::map`. 25 shared cases × both reassemblers + 3 native-only property tests = 576 native checks. FAIL-before confirmed by three mutations of the shipped header: `>` → `>=` on the predecessor comparison (6 accept cases flip to overlap, i.e. every normal multi-chunk transfer breaks), the additive bounds form (the uint32-wrap case is accepted and records a segment past `totalSize`), and deleting the zero-length refusal (reproduces the documented "one empty frame kills a good transfer" chain).
* **WON'T DO for now (2026-07-26, re-evaluated after the gate above landed) — a harness that drives C++ `HandleChunk` with a real Go guest over real SHM.** The remaining value after the offline gate is integration-shaped only: framing/dispatch by `msg_type` and the `false` → `shm::MsgType::SYSTEM_ERROR` write-back. The cost is a **new C++ host binary** that links `xll_worker.cpp` — which drags in `xll_async.cpp` (`xlAsyncReturn`), `xll_rtd.cpp` (COM `IRTDUpdateEvent`), `xll_log`, `xll_lifecycle` and a live `shm::DirectHost` — plus a stubbed `Excel12v`, a new embedded CMake fixture with its own hand-maintained `flatbuffers`/`shm`/`types` pins and a new drift gate (§18.2 point 6), and a Go driver. That is regtest-scale infrastructure for three `if` branches, it only runs where MinGW+cmake exist (so it is skipped on CI exactly like every other gate here), and it is the flakiest class of test in the repo (real SHM, timeouts, child-process lifetime). The `SYSTEM_ERROR` write-back is additionally protected by a **build-breaking** coupling already documented in `xll_worker.cpp` (the handler lambda's `shm::MsgType&` parameter binds only via `ProcessGuestCalls`' template overload; narrowing it to the `GuestCallHandler` typedef fails to compile rather than silently losing the write), and the slot-level `SYSTEM_ERROR` mechanism itself is shm's contract, tested in shm's own suite.
  **Cheaper successor, if this gap is revisited:** extend the SAME offline pattern instead. The untested remainder that is worth covering is bookkeeping over two `std::map`s — the `total_size == 0` / `kMaxChunkTotalSize` / `kMaxPartialMessages` refusals, the prune-then-refuse reclaim, and the poison set's TTL expiry + `kMaxPoisonedTransfers` oldest-eviction. Extracting those into a pure registry type next to `ClaimChunkSegment` would make them g++-testable at roughly a tenth of the cost, and would leave only framing/dispatch — genuinely low-value — uncovered. Do that before building a process harness.
* **DONE (2026-08-03) — the two C++ comment-stripping test helpers could silently DELETE the code they were asked to inspect.** `internal/generator`'s `stripCppComments` and `internal/assets`' `stripCppCommentsAsset` were both a `(?s)/\*.*?\*/` pass followed by a `//[^\n]*` pass. Block-first is wrong on real source: a `/*` that appears inside a LINE comment — and `file(GLOB src/*.cpp)` appears inside one in `src/ribbon_connect.cpp`, `src/scratch_book.cpp` and `src/ribbon_addin.cpp` — is read as a real opener, and the non-greedy match then runs to the next `*/` ANYWHERE in the file. It stayed dormant only because no `*/` followed; adding a `/*allowBounce=*/` argument comment 275 lines below one of those glob comments made the pass swallow the entire class body and every assertion in `ribbon_connect_cpp_test.go` failed at once. That failure was loud, but the same swallow inside an ORDER assert (`guardIdx < teardownIdx`) or a do-not-re-inline NEGATIVE assert is silent and reads as GREEN — which is precisely the false green those guards exist to prevent, and there are several (`TestOnDisconnectionMarksOfficeDisconnectBeforeTeardown`, `TestCancelQuitOnAutoCloseNonDestructive`, every `stripCppComments`-based re-inline guard). Both helpers are now a single left-to-right scanner that also understands string and character literals, so a delimiter inside a comment or a literal cannot be mis-paired. No test was weakened; two `regexp` imports were dropped.
* **OPEN (LOW, still deferred from the same review):**
  * No RTD F9 smoke case in `internal/smoketest`.
  * Doxygen coverage of the new CacheManager/chunk entry points.
* `internal/regtest`: regression tests bind to Excel via COM (`internal/regtest/runner.go`). Add a short doc note explaining how to run them on a fresh Windows machine (which Excel SKUs work, what registry entries are needed).
* Follow-ups uncovered during the stabilization pass:
  * `MaxChunkBufferBytes` is currently only mutable through code (`ChunkManager` field or `NewChunkManagerWithMax`). Plumb it through `xll.yaml` → `internal/config` so deployments can tune it without rebuilds. Co-change cluster: pairs with the §23.2 cleanup-tick/TTL promotion.
  * **DONE (2026-05-17, xll-gen v0.3.8 / shm v0.6.0):** local `MsgSystemError` sentinel in `pkg/server/types.go` removed; `pkg/server/handlers.go` and `pkg/server/manager_test.go` now use `shm.MsgTypeSystemError` directly. shm exported the constant in v0.6.0 alongside the streaming API.
  * Wire `Chunk` schema (in `types/`) does not carry an explicit `total_chunks` field; dedup is keyed on offset (unique per chunk on first transmission) which is sufficient given chunk size is sender-controlled and offsets do not overlap. If a future change introduces variable-sized chunks within a transfer, revisit and key on `(offset, length)` or add an explicit chunk-index field.
* **DONE (2026-07-26) — HIGH + MED: a reference argument could kill Excel, or return a silently wrong number.** Full write-up in **§19.5**; the short version is that the sync/async request is built straight into the slot's 512 KiB request buffer, `flatbuffers::Allocator` has no failure channel, and `SHMAllocator::allocate` answered `nullptr` past capacity — which `Allocator::reallocate_downward` then `memcpy`'d into. The measured cliff on the shipped showcase was ~19.6k grid cells (120×120 fine, 140×140 dead), i.e. the `~28 B/cell` payload against 512 KiB — **not** "3.1M cells is too much for `xlCoerce`", which is why the two reported reproductions of `=SumGrid($P:$R)` showed different faulting modules. Fixed by making the allocator serve over-capacity requests from the heap behind a sticky `Overflowed()` latch that every slot-arena sender now checks between `Finish()` and `Send()` (4 sites, gated by `TestSlotArenaSendersCheckOverflow` — which caught the one that was missed). `xll::kMaxGridArgCells` (16384, compile-time, deliberately not YAML-wired) is the cheap pre-filter on top, so the pathological case never makes Excel materialize ~100 MB of `XLOPER12`. Separately, a multi-area (union) reference — `=SumGrid(($P$1:$R$5,$P$6:$R$10))` returned **0** while the contiguous equivalent returned 2805 — is now refused structurally before `xlCoerce`, with a post-coerce "did you actually answer `xltypeMulti`?" check behind it; `ConvertGridArg`'s out-param became `xll::GridArgStatus` so the log names WHICH refusal fired. Real Excel, 3 rounds each: `$P:$R` process death → `#VALUE!`; union `0` → `#VALUE!`; control `$P$1:$R$10` 2805 → 2805.
  * **Still open (LOW, deliberate):** the refusal is all-or-nothing. A user who legitimately wants to hand a handler more than ~16k cells has no path today — host→guest ARGUMENT chunking does not exist (only guest→host results are chunked, §18.6.1). Adding it is a protocol-level change (`shm-protocol-guardian` + `cross-repo-coordinator`), not a runtime fix. Until then `#VALUE!` plus the `SumGrid: grid arg g rejected: reference area exceeds the grid-argument cell limit` log line is the contract.
  * **FOLLOW-UP DONE (2026-07-29) — the cell bound was missing from the DIGEST path, which runs FIRST.** `kMaxGridArgCells` lived in exactly one function, `ConvertGridArg`, but the generated wrapper computes the digest BEFORE it (`MakeCacheKey` → `ConvertGridArg` for sync/async; `ContentHashToken` → the payload build for rtd / rtd-once). So a cache-enabled or RTD function taking a whole-column reference still made `xlCoerce` materialize ~3.1M `XLOPER12` cells (~100 MB) and hashed all of them, per cell using the range and per recalculation, *before* the argument was refused — the exact cost §19.5 item 2 names as this layer's reason to exist. The rtd token sits ABOVE the per-cycle `rcAlreadySent` dedup, so it paid it on EVERY recalc unconditionally; x86 Excel is an OOM candidate and x64 stalls for hundreds of ms to seconds per cell. Fixed with `xll::MeasureRefArg` before the coerce in `HashXLOPERIntoDepth` AND once over the whole reference in `GetOrComputeRefHash` (its rect loop splits an `xltypeRef` into one single-rect temporary per area, so the per-rect filter alone misses a union of individually-small rects). Over-bound folds `HashRefIdentity`, the same well-defined branch a coerce failure takes, so distinct oversized references still hash apart. **This also covers the `r`/`a` tokens** — §19.5's "range/any are not affected" is true of the PAYLOAD only, and that paragraph has been corrected to say so. Files: `internal/assets/files/src/xll_cache.cpp`. Regressions: `internal/assets/testdata/cache_native_test.cpp::TestOversizedRefIsNotCoerced` (zero Excel calls for all four tags, the exact-bound boundary, distinctness, the union-total case, and that a union which FITS is still coerced per rect) + `internal/assets/cache_cpp_test.go::TestDigestPathBoundsTheCoercedArea` (measure-before-coerce ORDER, the whole-reference measurement, the identity fallback). FAIL-before confirmed by neutering both bounds: 7 of 91 native checks fail, including "coerce calls: 3" on the union case.
  * **FOLLOW-UP DONE (2026-08-03) — the value-only digest family is safe by DOWNSTREAM REFUSAL, not by the digest, and that was nowhere written or executed (LOW, latent).** Excel's `xlCoerce` SUCCEEDS on a multi-area (union) reference like `(A1:B2,D1:E2)` and answers with an ERROR value, so `HashXLOPERIntoDepth` folds `kTagRefVal` then `kTagErr` and nothing else — **every union has a byte-identical value part**, and no value-only digest can separate two unions, not even ones on different sheets. Nothing observes it today, for two INDEPENDENT reasons: `'r'`/`'a'` and `MakeCacheKey` fold the reference IDENTITY ahead of the values (so they separate unions on sheet + rects), and `'g'`/`'n'` are safe only because `ConvertGridArg` REFUSES a union downstream (`GridArgStatus::kMultiArea`). The second reason is not a property of the digest at all, which is exactly why it needed pinning: **a new value-only tag whose converter accepts unions is a silent wrong answer on its first day** — two distinct unions, one token, the second ship deduped away by `SendRefCachePayloadOnce`, the consumer resolving the FIRST union's cells. No behavior change: this is a comment in `ContentHashToken` (naming the two escape routes and telling a tag-adder to pick one) plus `internal/assets/testdata/cache_native_test.cpp::TestMultiAreaRefHasNoValueDigest`, which EXECUTES both halves — that `'g'` genuinely cannot separate two disjoint unions (recorded as the current truth, with a note that making it separable is an improvement to be landed together with the comment) and that `'r'`/`'a'`/`MakeCacheKey` MUST, including two 2-area unions differing in a single rect. That last check is the non-vacuous one: mutating the identity fold to cover only the first rect of a union leaves every pre-existing check passing and fails exactly this one (verified).
* **DONE (2026-07-29) — HIGH: an empty handler error message killed Excel from a `string`-returning cell, and silently returned 0 everywhere else.** The generated server writes `Response.result` and `Response.error` in an EXCLUSIVE if/else, so an errored response carries no result field and flatc answers `nullptr` for it — but `b.CreateString("")` returns a NON-ZERO offset, so an error whose `Error()` is `""` still produced a PRESENT error field of size 0. Every C++ consumer keyed off `error() && error()->size() > 0`, so that response was routed to the RESULT path: `resp->result()->str()` read `length_` off offset 0 and took the Excel process down (a generated sync UDF is OUTSIDE `XLL_SAFE_BLOCK`/`__try`, so the AV was not contained and unsaved workbooks were lost), while `int`/`float`/`bool` painted the FlatBuffers default **0 / 0.0 / FALSE with no error at all** and `any` painted an empty cell — only `grid` was defended, by `GridToXLOPER12(nullptr)` → `#VALUE!`. `return "", errors.New("")`, or a custom error type whose message field is unset (`&MyErr{}`), was enough. The cache-HIT path reuses the same `returnConversion` block and reproduced identically. **Fixed on BOTH sides, because either alone leaves a live failure mode:** (a) `internal/templates/xll_main.cpp.tmpl` — the guard is now PRESENCE-only (`if (resp->error())`) plus an `if (!resp->result())` → `&g_xlErrValue` on the string path; (b) `internal/templates/server.go.tmpl` — every error-field site routes through the new `pkg/server.ErrorMessage`, which substitutes `"handler returned an error with an empty message"`. The C++ half alone paints an EMPTY cell against a non-normalizing server; the Go half alone leaves the nullptr dereference in an XLL built before the guard changed. The async path had the same root cause with a different symptom: `buildAsyncBatchPayload` selects the error branch on `res.Err != ""`, so an empty message was mistaken for a SUCCESS and the handle was answered with an absent `Any` → blank cell. Regressions: `internal/generator/gen_error_field_test.go` (presence-only guard, the rejected `size() > 0` shape banned, guard-before-deref ORDER, one guard per emission site with caching on and off, numgrid still excluded, plus a template-SOURCE scan so a future return type cannot add an unnormalized error site) and regtest **case 19a/19b** with two new `testdata` functions (`ErrEmptyString` / `ErrEmptyInt`, appended so no message ID shifts) returning an error with an empty `Error()`. FAIL-before verified for each half separately; case 19a fails over real SHM on the un-normalized server. `internal/generator/gen_cpp_test.go::TestGenCpp_SyncErrorSurfacedToCell` was UPDATED, not weakened: it used to pin the `size() > 0` conjunction, which was the defect; it now pins presence, which is what it actually meant.
* **DONE (2026-07-29) — HIGH: `client.Close()` ran while RTD / async sends were in flight, so ordinary Excel shutdown ended in an uncatchable fatal fault.** `internal/templates/server.go.tmpl` did `defer client.Close()` and the parent-death watcher called `client.Close()` directly. shm's contract is explicit (`shm/go/direct.go`, `DirectGuest.Close`): Close must not run concurrently with an in-flight `SendGuestCall`, because it unmaps the region. Its `wg.Wait()` drains only the workers `Start()` launched; the async batch flusher and every RTD pusher are CALLER-side and untracked, including goroutines the USER's handler spawned (a streaming pusher lives until its topic disconnects — and Excel dying or crashing disconnects nothing). The `closing` flag is only consulted on the slot-acquire FAILURE path, so a send starting after Close returned still claims a slot in an unmapped region: deterministic, not a race. The fault is `fatal error: unexpected fault address`, which `recover()` cannot catch (`shm/go/direct.go` says so), so every shutdown with a live RTD stream produced a full goroutine dump and a non-zero exit. **Chose the FULL drain over the minimal "just don't Close" option**, because both send paths are reachable only through `pkg/server`/`pkg/rtd` (audited: `SendGuestCall*` appears at exactly two sites) so the drain is provably complete — user goroutines push via `PushRtdUpdate` → `RtdManager.SendUpdate`, which the gate covers. New API: `AsyncBatcher.Stop(timeout) bool` (gate + wait for the in-flight flush; the queue channel is deliberately NEVER closed, since `QueueResult` is called from the SHM worker pool and the generated async job goroutines and closing it would turn a late result into a *panic*) and `RtdManager.Stop(timeout) bool` + `rtd.ErrStopped` (a sentinel, so a pusher can tell shutdown from a transient 1s timeout). The RTD gate latches `stopped` and does `sendWG.Add` in ONE `m.mu` critical section — the "atomic flag + WaitGroup" shape has an Add-after-Wait window that would defeat the whole point. The generated `shutdownAndClose` signals `Done()`, drains async (5s), drains RTD (10s), and **skips `client.Close()` entirely if either drain times out**: a missed unmap costs nothing at process exit (the OS reclaims mapping + handles), whereas unmapping under a live sender is the fault being removed — a drain timeout must never be promoted into a UAF. The scaffold `main.go.tmpl` pusher now selects on the new exported `Done()` channel alongside the per-topic ctx. Files: `pkg/server/batcher.go`, `pkg/rtd/manager.go`, `internal/templates/server.go.tmpl`, `internal/templates/main.go.tmpl`. Regressions: `pkg/server/batcher_stop_test.go` and `pkg/rtd/manager_stop_test.go` (Stop waits for the in-flight send; every path returns `ErrStopped` after; **32 live streaming pushers driven across a teardown assert ZERO sends BEGIN after Stop returns**; Stop reports `false` rather than lying when a send hangs; idempotent + concurrency-safe; "not connected" behavior preserved) — all under `-race`; plus `internal/generator/gen_teardown_drain_test.go` (both Close sites gone, exactly one `client.Close()`, the signal→async→rtd→close ORDER, the skip-on-timeout valve, `Done()` exported, and the scaffold pusher's Done() select). FAIL-before confirmed by reverting each piece independently.
* **DONE (2026-07-29) — HIGH: plain-rtd and rtd-once leaked `xlfRtd`'s Excel-allocated result on every call.** `Excel12v` FILLS IN the caller's `XLOPER12`, and the auxiliary memory behind `xltypeStr`/`xltypeMulti`/`xltypeRef` in such a result belongs to EXCEL. The SDK allows exactly two dispositions: release it with `xlFree`, or — for a value the UDF RETURNS — set `xlbitXLFree` and let Excel reclaim it after reading the cell value. The plain-rtd wrapper did NEITHER (returned `&xRes` with no ownership bit) and the three rtd-once wrappers discarded the value with `(void)xRes;`; the failure path additionally overwrote `xltype`, orphaning the previous call's payload. So every STRING-valued RTD topic leaked one Excel-heap Pascal buffer per cell per push — ~50–80 B/push, i.e. ≈4 MB/day for one cell and ≈400 MB/day for a 100-cell dashboard, with **no trace in any log**. Numeric/boolean topics were unaffected (nothing to own). `types` v0.2.9 had already fixed the same contract violation at the other `Excel12` result sites (see `ScopedXLOPER12Result::Free`'s comment); these two wrappers were simply missed. Fixed with ONE inline helper, `xll::ReleaseOrTransferExcelResult(XLOPER12&, bool transferToExcel)` in `internal/assets/files/include/xll_excel.h`, called from all four wrappers: plain rtd TRANSFERS (it must return the value verbatim — a placeholder would mean the stream never displays, §19.3), rtd-once RELEASES (it discards the value, and `xlbitXLFree` is only honored on a value Excel actually receives back; the numgrid wrapper does not even return an `XLOPER12`). **`xlbitXLFree` on a `static thread_local` operand is safe and the reason is in the helper's comment:** the bit asks Excel to free what the `XLOPER12` POINTS AT, never the struct, which stays ours and is overwritten by the next `xlfRtd`; the bit that would hand over the struct is `xlbitDLLFree`, routed to our exported `xlAutoFree12`. The helper's type switch keeps the bit off a scalar, and it refuses to touch an `xlbitDLLFree` payload (`xlFree` would corrupt our own allocator). Regressions: `internal/generator/gen_rtd_result_ownership_test.go` — the string-level gate the task asked for: `(void)xRes;` banned outright, one disposition per `xlfRtd` call site, plain rtd must transfer BEFORE `return &xRes;`, every rtd-once wrapper must release and must NOT set the bit, plus `TestReleaseOrTransferExcelResultContract` on the embedded header. FAIL-before confirmed by restoring `(void)xRes;`.

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

### 23.6 Close-time ghost Excel (S1') / orphaned server (S2) / use-after-unload — RESOLVED (Stage 4 2026-06-17, Stage 5 2026-07-29)

**STATUS: S1' ghost RESOLVED and SHIPPED (Stage 4, 2026-06-17). S2 was never regressed. The
close-time USE-AFTER-UNLOAD crash that the Stage-4 deferral exposed is RESOLVED in Stage 5
(2026-07-29) — read Stage 5 before touching any of this.**

**TWO LOAD-BEARING FACTS, MEASURED, THAT INVALIDATE EARLIER REASONING IN THIS SECTION:**
1. **Excel does NOT reliably call `IRtdServer::ServerTerminate`, and the whole deferred Phase 2
   must therefore never be load-bearing.** Nothing essential may depend on it firing. (It does
   fire on the current build's host-shutdown path; it did NOT in the Stage-1 diag, and it also
   fires on every zero-live-topic transition with the host alive — see the 2026-06-18 remediation.)
2. **On a host shutdown with live streaming RTD topics, Excel `FreeLibrary`s and truly UNMAPS the
   XLL ~80–100 ms after `OnBeginShutdown` returns** (`DLL_PROCESS_DETACH`, `lpReserved == NULL`).
   With no live topics it never does (`lpReserved != NULL`, process exit, no unmap). So the
   deferred Phase 2 ran concurrently with the unmap. See Stage 5 and §20.2.1.

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
  (defaults to `os.Exit`) for testability.
  **AMENDED 2026-07-29 — `onExit` must NOT be `client.Close()`.** It now calls the shared
  `shutdownAndClose(client)`, the same once-guarded drain `Serve` defers. Closing the client
  directly here was the WORSE of the two Close sites: the parent died with no handshake, so an RTD
  pusher is almost certainly mid-send, and `DirectGuest.Close` unmaps the region while it is
  reading a slot → `fatal error: unexpected fault address`, uncatchable. See the §23.3 entry
  "client.Close() ran while RTD / async sends were in flight" for the full contract and the
  skip-the-unmap-on-drain-timeout valve. Regressions: `internal/generator/gen_parent_watch_test.go`
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
* **HIGH #2 (xlcOnTime cancel de-queue) — RESOLVED / empirically characterized (2026-07-24).**
  The earlier "PARTIALLY OBSERVED — de-queue held" note **misread the return code**: `rc=2` is
  **`xlretInvXlfn`** (the Excel12 xlret STATUS of the call, i.e. "invalid function/context"), NOT an
  "xlcOnTime clear return". Excel12's return is the call status, not the boolean ON.TIME result (that
  would be in the `res` XLOPER, which we pass as `nullptr`). So on the ONE historical occasion a runner
  was actually armed at host-shutdown (native log 2026-06-20), the cancel was **REJECTED and de-queued
  NOTHING**. Every other close logged nothing (`g_onTimeArmed==false` → early return).
  - **Root cause (proven):** `CancelDeferredRunner` runs from `GracefulTeardownOnce`, driven by
    `RibbonAddIn::OnBeginShutdown`/`OnDisconnection` — a **COM-event context on the STA, NOT an
    Excel-dispatched macro/command context**. Excel does not permit command-class (`xlc*`) C-API calls
    there, hence `xlretInvXlfn`. (The SCHEDULE side succeeds because it is issued from the
    `xleventCalculationEnded` callback, which IS a valid command context — that asymmetry is the whole
    story.) Arg count is fine (4 args valid for ON.TIME); a bad count would be `xlretInvCount`(4), a bad
    operand `xlretInvXloper`(8) — so `rc=2` isolates the fault to context, not marshaling.
  - **Deterministic proof (real Excel, 3/3 runs, 2026-07-24):** two throwaway probe macros
    (`__xllgen_TestOnTimeSchedOnly`, `__xllgen_TestOnTimeCancel`) invoked via COM `Application.Run`
    (a VALID macro context). A dispatch counter (`DeferredRunnerDispatchCount`) is bumped at the top of
    `RunDeferredCalcEndCommands`. Control (schedule only) → `dispatched (count=1)` ~10ms later. Test
    (schedule+cancel) → `CancelDeferredRunner: ... cancelled (rc=xlretSuccess)` + **no dispatch in the
    following 6 s** (count stays 0). ⇒ the cancel **MECHANISM is correct and DOES de-queue when the
    context is valid**; the serial round-trip / `schedule=FALSE` marshaling are all fine.
  - **Net verdict:** de-queue on the **production teardown path does NOT happen** (rejected,
    `xlretInvXlfn`). It is **harmless** — the ghost was fixed by the §23.6 Stage-4 deferred-teardown
    split, and any leaked OnTime dispatch is neutralized by the runner's `g_isUnloading`/`g_phost`
    self-abort + Excel un-registering the XLL's macros on unload. `CancelDeferredRunner` on the
    host-shutdown path is therefore a **documented no-op / dead belt-and-suspenders**.
  - **Shipped fix (working tree):** self-documenting `xlret` decode in the cancel/schedule logs (so the
    rejection can never again be misread as success); dispatch counter + entry log in the runner;
    corrected the false "valid from this STA macro/command context" comment in
    `xll_deferred_commands.cpp`/`.h` and `xll_lifecycle.cpp`. **Open decision for xll-cpp-reviewer:**
    remove the non-functional teardown-path `xlcOnTime` cancel (proven no-op there) vs keep as
    documented best-effort. Files: `internal/assets/files/src/xll_deferred_commands.cpp`,
    `internal/assets/files/include/xll_deferred_commands.h`, `internal/assets/files/src/xll_lifecycle.cpp`.
  - **§3 (ribbon.bounce off / bounce-failed → OnTime-based connect retry) — DONE (2026-07-24).**
    Problem: when the ribbon does NOT connect at load (`ribbon.bounce: off`, or a bounce that failed)
    the only remaining connect trigger was `TryConnectRibbon("calc end")`, which never fires for a
    workbook that is OPEN (EXCEL7 exists) but never recalculates (manual calc mode / no-formula book),
    so the ribbon tab was delayed indefinitely. (Empty Excel with no EXCEL7 window is NOT the target —
    that is the genuine `noApp` case.) Fix: a bounded, state-gated `xlcOnTime` retry macro
    (`__xllgen_RibbonConnectRetry`, macroType=2) heeding the schedule/cancel-context asymmetry proven
    above. **Arm point:** `xlAutoOpen` (an SDK-standard command context, so `xlc*` is accepted), right
    after the load-time connect attempt, ONLY when not yet connected (`g_ribbonConnectState == 0`) —
    i.e. off-mode or a failed bounce. **Re-arm:** the runner re-schedules ITSELF via `xlcOnTime` from
    its own Excel-dispatched macro dispatch (also a valid command context) — never from a COM-event
    context, so it never hits the `xlretInvXlfn` wall. **Termination is by STATE GATE / SELF-ABORT, not
    a C-API cancel:** stops (no re-arm) once connected, once `TryConnectRibbon` gives up
    (`g_ribbonConnectState != 0`), when a bounded attempt budget is exhausted (originally a single
    `kRibbonRetryMaxAttempts` = 30 at `kRibbonRetryIntervalSec` = 2.0 s ≈ 60 s — **superseded by the
    two-budget split below**), or on `g_isUnloading`. A leaked schedule that fires post-unload
    no-ops on the `g_isUnloading` gate and Excel un-registers the macro on unload — so **NO new
    teardown-cancellation path is added** (§20/§23 surface unchanged). The calc-end fallback is KEPT
    (belt-and-braces for the workbook-already-recalculating case). Reuses the deferred-commands
    infra: new `xll::ScheduleOnTimeMacro(macroName, delaySeconds)` +
    `xll::RibbonConnectRetryMacroName()` in `xll_deferred_commands.{h,cpp}` (reusing that TU's
    `XlretName` decode). Template `internal/templates/xll_main.cpp.tmpl` gained the retry-budget
    constants, the macro registration, the `xlAutoOpen` arm, and the `__xllgen_RibbonConnectRetry`
    export — all gated `{{if .Ribbon.Enabled}}`. Files:
    `internal/assets/files/{include,src}/xll_deferred_commands.{h,cpp}`,
    `internal/templates/xll_main.cpp.tmpl`. Regression:
    `internal/generator/gen_ribbon_connect_test.go::TestXllMainRibbonOnTimeConnectRetry` (default + off
    renders: arm/re-arm/self-abort/budget/registration markers; asserts termination is self-abort, not
    an `xlcOnTime` cancel; ribbon-disabled render emits none of it) — FAIL-before / PASS-after
    confirmed by reverting the template. `cmd/cpp_compile_gate_bounce_test.go` (full/keep-open/off) all
    compile+link the emitted retry under MinGW. Golden updated (`xll_main.cpp.golden`: whitespace only,
    since the golden fixture has the ribbon disabled). Runtime C++ asset + template (not `DllMain`
    graceful-path logic) — confirm with `xll-cpp-reviewer` before commit.
  - **§3 FOLLOW-UP (2026-07-26) — retry budget split + arm-path hardening. DONE.** Post-ship review of
    the above found that the retry **still did not fix its own target scenario**, plus four smaller
    defects on the arm path.
    - **MED #1 — `noApp` attempts consumed the whole 30-attempt budget.** `TryConnectRibbon` has always
      deliberately declined to charge a "no `Application` object yet" attempt against its OWN give-up
      budget (`if (noApp) return false;` — that state is not a failure, the user simply has not opened
      a workbook). The retry runner did not make that distinction and charged every attempt to the
      single 30-attempt budget. So: empty Excel + `ribbon.bounce: off` → the retry polled 30× over 60 s
      against no workbook and stopped; the user opened a manual-calc / no-formula workbook at t≈90 s →
      budget already spent AND calc-end never fires for such a book → **ribbon tab still delayed
      indefinitely**. The scenario §3 was written for was not actually fixed (the "fills the gap"
      wording above was correspondingly overstated for the empty-start case).
      **Chosen fix: (a) — expose `bool* pNoApp` from `TryConnectRibbon` and give the `noApp` class its
      own budget.** Rejected (b) (blanket rebudget to 10 s × 30) because it (i) conflates the two
      failure classes, so a genuinely broken host — `Application` reachable, `Connect` rejected — would
      be hammered for 5 minutes instead of 60 s, (ii) costs up to 10 s of ribbon-tab latency after the
      workbook finally opens, and (iii) leaves the *accounting* bug in place, which is the actual
      defect: `TryConnectRibbon`'s documented intent already said noApp must not be charged; the runner
      was the site violating it. (b)'s good idea — a *time*-shaped rather than count-shaped budget — is
      kept for the noApp class only. Shape: productive attempts keep `kRibbonRetryMaxAttempts` = 30 @
      `kRibbonRetryIntervalSec` = 2.0 s; noApp attempts get `kRibbonRetryNoAppFastAttempts` = 15 @ 2.0 s
      (stay responsive for the first ~30 s so a workbook opened right after startup gets the tab within
      ~2 s), then relax to `kRibbonRetryNoAppIdleSec` = 10.0 s up to `kRibbonRetryNoAppMaxAttempts` = 75
      → **630 s (~10.5 min) total noApp window**, ~6 dispatches/min while idle.
      **RESIDUAL HOLE (accepted, deliberate):** the noApp window is FINITE. An Excel left empty for
      longer than ~10.5 min and *then* given a manual-calc / no-formula workbook still gets no ribbon
      tab until something recalculates. This is accepted over an unbounded poll — an add-in that polls
      the host forever is its own defect — and the user-visible escape hatches are unchanged
      (`ribbon.bounce: full`/`keep-open` connect at load; any recalculation fires the calc-end
      fallback; reloading the add-in re-arms).
    - **MED #2(a) — `ScheduleOnTimeMacro` had no self-gate.** It is exported as general-purpose API but
      relied on every caller remembering to check `g_isUnloading`; a schedule placed during teardown can
      only ever become a leaked OnTime dispatch. Now gates itself
      (`if (xll::g_isUnloading.load(acquire)) return xlretFailed;`) before any C-API call — structural,
      per §20, rather than an unenforced caller contract. Both existing callers keep their own gate.
    - **MED #2(b) — arm rc was discarded / logged too quietly.** A rejected `xlcOnTime` silently kills
      the self-re-arming chain. Non-success rc in `ScheduleOnTimeMacro` is now `LogWarn` (was `LogInfo`),
      and BOTH arm sites (`xlAutoOpen` initial arm, runner re-arm) capture the rc and emit a
      `SAFE_LOG_WARN` naming the consequence ("falling back to the calc-end retry only").
    - **MED #2(c) — misleading log.** The "xlfNow succeeded but returned a non-numeric operand" branch
      printed `nowRc` (= 0 there), rendering as `rc=0 (xlretSuccess)`. It now prints the returned
      `xltype` alongside, which is the only thing distinguishing the two failure shapes.
    - **MED #2(d) — shared counter across two chains.** `s_retryAttempts` was a function-local static
      inside `__xllgen_RibbonConnectRetry`'s SEH `__try`. A second `xlAutoOpen` in the same process
      generation (probe-unload-reuse, add-in disable→enable without a DLL unload) while still
      unconnected armed a SECOND chain sharing it — double dispatch rate, half the budget each. Counters
      moved to file scope (`g_ribbonRetryAttempts`, `g_ribbonRetryNoAppAttempts`) next to
      `g_ribbonConnectState`, and `g_ribbonRetryArmed` is a **start-once CAS latch** at the arm site.
      The latch is cleared ONLY when the `xlcOnTime` itself was rejected (nothing in flight, so a later
      `xlAutoOpen` may legitimately try again); terminal states (connected / gave up / budget exhausted)
      leave it latched so nothing restarts an already-decided chain.
    - **MED #2(e) — no cross-check between the export symbol and the header literal.** The macro name
      lives in `xll_deferred_commands.h`'s `*MacroName()` accessor AND as an exported C symbol in
      `xll_main.cpp.tmpl`, with nothing structurally coupling them: a one-sided rename compiles, passes
      every name-grepping generator test, and shows up only at runtime as an unresolvable ON.TIME macro
      (no ribbon tab / no deferred command drain). Now guarded from both ends, for BOTH macros
      (`__xllgen_RibbonConnectRetry` and the mirrored `__xllgen_RunDeferredCalcEnd`).
    - **Files (as of this entry; the connect machinery and the retry budgets have since moved to
      `internal/assets/files/{include/com/ribbon_connect.h,src/ribbon_connect.cpp}` — §18.11.1):**
      `internal/templates/xll_main.cpp.tmpl`,
      `internal/assets/files/src/xll_deferred_commands.cpp`,
      `internal/assets/files/include/xll_deferred_commands.h`.
    - **Regression:** `internal/generator/gen_ribbon_connect_test.go::TestXllMainRibbonRetryNoAppBudget`
      (budget separation, per-class spacing, hard stops, pre-fix single-counter shape absent),
      `::TestXllMainRibbonRetryArmRcAndSingleChain` (file-scope state, start-once CAS, both arm sites
      inspect the rc), `::TestOnTimeMacroNameExportsMatchHeaderLiterals` (derives the expected export
      from the header literal instead of restating it), and
      `internal/assets/assets_test.go::TestScheduleOnTimeMacroGuards` (unload gate precedes `xlfNow`,
      warn-level failure logs, `xltype` in the `xlfNow` branch) +
      `::TestOnTimeMacroNameLiterals`. FAIL-before / PASS-after verified against a clean HEAD checkout;
      the drift guard additionally mutation-tested by renaming the header literal alone.
      **Golden: NO update needed** — every hunk is inside `{{if .Ribbon.Enabled}}` and the golden fixture
      leaves `Ribbon` unset (verified by applying only these hunks to a clean HEAD and running
      `TestGolden`). `cmd/cpp_compile_gate_bounce_test.go` full/keep-open/off all compile+link.
    - **PROVEN ON REAL EXCEL (2026-07-26) — the `xlAutoOpen` arm works with zero workbooks open.**
      The only state in which the `xlAutoOpen` arm is actually reached is `ribbon.bounce: off` **with
      zero workbooks open**, and the prior empirical proof (§23.6 HIGH #2) had been taken in a calc-end
      context with a workbook open — a different context, so acceptance there did not transfer. Closed
      with a purpose-built probe XLL (MSVC, raw `Excel12`, mirroring `ScheduleOnTimeMacro`'s exact call
      shape: `xlfNow` → `+delay/86400` → `xlcOnTime(when, macro)`), loaded via `RegisterXLL` into an
      Excel started with **no workbook**. Result, empty start:
      `DOCUMENTS → err 42` (`#N/A` = zero documents, so the state is proven, not assumed) ·
      `xlfNow rc=0 xltype=0x1 (xltypeNum)` · **`xlcOnTime rc=0 res=BOOL 1` (ACCEPTED)** ·
      **`FIRED` at +2.5 s (DISPATCHED)** · re-arm issued from inside the fired macro also accepted and
      dispatched, **3/3 chained dispatches**, all on the same STA thread as `xlAutoOpen`. The control run
      (one workbook open before `RegisterXLL`, `DOCUMENTS → multi 1x1`) behaved identically, which is
      what validates the `err 42` reading rather than leaving it as inference. So neither acceptance nor
      dispatch is workbook-gated, and the MED #2(b) warn path is a genuine last resort rather than the
      expected case. Two incidental observations: dispatch latency ran ~2.0–2.5 s for a +2.0 s schedule
      (Excel rounds to its own OnTime tick — budget accounting should not assume exact spacing), and the
      fired macro runs on the main STA thread, which is what the runner already assumes.
      **Still not covered:** the "accepted, then silently DROPPED by Excel" case that the start-once
      latch cannot recover from (queue clear / workbook close / another add-in's `Application.OnTime`
      cancel). That is a different failure mode from rejection and remains unproven in both directions;
      the latch trade-off documented above stands, and code change is still not recommended without a
      repro.
  - **§3 FOLLOW-UP #2 (2026-07-26) — the last unbilled-attempt hole. DONE (LOW).** Same defect class as
    MED #1, one branch further out: `TryConnectRibbon` has TWO exits that return before ever calling
    `SetRibbonConnected` — the `g_isUnloading` bail (unreachable from the runner, which gates on the
    same flag first) and the **STA RE-ENTRANCY bail** (`s_inConnect` CAS failure), which is very much
    reachable: the COMAddIns `Connect` and the temp-workbook bounce both PUMP the STA message loop, so
    Excel can dispatch a queued OnTime macro mid-connect; that dispatch re-enters `TryConnectRibbon` on
    the same thread and is turned away. Both exits left `pNoApp` false, so the runner's `else` charged
    one of its 30 productive attempts for **a connect that never happened**. Fix: replace the
    `bool* pNoApp` out-param with an explicit outcome CLASS
    `enum class RibbonAttempt { kNotAttempted, kNoApp, kRejected, kConnected }`, defaulted to
    `kNotAttempted` and written at every exit, so an exit that forgets to classify itself is UNCHARGED
    rather than MIS-charged. The runner charges the productive budget only for `kRejected`, the noApp
    budget only for `kNoApp`, and nothing for `kNotAttempted` (it re-arms at the productive spacing and
    logs at debug). `kNotAttempted` deliberately gets NO budget of its own: it can only arise while an
    OUTER attempt is in flight on this same STA thread, so it is self-limiting — when that outer call
    returns, the next dispatch either sees a resolved state (chain stops on the state gate) or gets a
    real, chargeable outcome. **Files (as of this entry):** `internal/templates/xll_main.cpp.tmpl` only;
    `TryConnectRibbon` and the `RibbonAttempt` enum now live in
    `internal/assets/files/{include/com/ribbon_connect.h,src/ribbon_connect.cpp}` (§18.11.1), and the
    classification-order pin moved to `internal/assets/ribbon_connect_cpp_test.go::TestRibbonConnectOutcomeClassification`.
    **Regression:** `internal/generator/gen_ribbon_connect_test.go::TestXllMainRibbonRetryUnattemptedNotCharged`
    (enum + all four classification sites + the runner's `else if (retryOutcome == RibbonAttempt::kRejected)`,
    the classification ORDER — `kRejected` only after an actual `SetRibbonConnected` — and that the
    re-entrancy bail classifies itself as nothing; the pre-fix `bool retryNoApp` shape must be absent),
    plus the updated pins in `::TestXllMainRibbonDeferredConnect` / `::TestXllMainRibbonRetryNoAppBudget`.
    FAIL-before / PASS-after verified by stashing the template alone.
    **Golden: NO update needed** — the whole hunk is inside `{{if .Ribbon.Enabled}}` and the golden
    fixture leaves `Ribbon` unset. `cmd/cpp_compile_gate_bounce_test.go` full/keep-open/off compile+link.

  - **§3 FOLLOW-UP #3 (2026-08-03) — the teardown no-op was reported as an Excel rejection. DONE (LOW).**
    `ScheduleOnTimeMacro`'s §20 self-gate returned `xlretFailed`, which is a real Excel status, so a
    DELIBERATE refusal to schedule was indistinguishable from Excel actually rejecting the arm. Both arm
    sites (correctly) treat a rejection as serious — it silently ends the self-re-arming chain — and warn
    about it, so an orderly shutdown that caught a pending connect logged
    `Ribbon: OnTime connect retry could not re-arm (xlcOnTime rc=32); the retry chain ENDS here`: a WARN
    describing a defect that was not happening, in the log an operator reads for the ones that are.
    **Not a narrow race.** `RunConnectRetryTick` gates on `TeardownStarted()` at entry, but the very next
    thing it does is `TryConnectRibbon`, which PUMPS the STA message loop — so Excel can deliver
    `OnBeginShutdown` in the middle of the tick, and the window is the whole duration of a COM connect
    attempt. Fix: `constexpr int kOnTimeNotScheduledTeardown = -1` in
    `internal/assets/files/include/xll_deferred_commands.h`, returned by the gate; both arm sites branch on
    it and log at DEBUG while keeping the WARN for every other rc. **Negative on purpose** — every xlret is
    a non-negative bit value, so the sentinel cannot collide with a status Excel produced, and a caller who
    forgets to special-case it still reads "not success" instead of silently reading `0` as
    `xlretSuccess`. **A sentinel, not a re-check:** re-reading the teardown flags after a failure answers
    "are we tearing down NOW", not "is this rc the gate's", and the flags can latch in between.
    The un-latch of `g_ribbonRetryArmed` stays OUTSIDE the sentinel branch — nothing is in flight either
    way, and a stuck latch would stop a later `xlAutoOpen` from starting a fresh chain (the
    false-positive-shutdown → re-enable path v0.8.41 has to survive).
    **Regressions:** `internal/assets/assets_test.go::TestScheduleOnTimeMacroGuards` (the gate string, plus
    an explicit "did someone put `xlretFailed` back" assertion — the regression compiles, still means "not
    scheduled", and its only symptom is a misleading log line) and
    `::TestOnTimeTeardownSentinelContract` (the constant is `-1`; the sentinel is checked at exactly TWO
    sites; both real-rejection WARNs survive; both teardown paths are DEBUG; the un-latch count is 2).
    Four mutations verified to fail named tests: sentinel `= 0`, gate back to `xlretFailed`, an arm site
    that stops checking, and the un-latch moved inside the sentinel branch.
    `cmd/cpp_compile_gate_bounce_test.go` full/keep-open/off compiled and linked (66.9 s, not skipped).

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

**Stage 5 (2026-07-29) — close-time USE-AFTER-UNLOAD. Excel UNMAPS the XLL mid-teardown. Fixed by
pinning the image + a Phase-1 quiesce + a no-park rule. Measured 3/3 → 0/12.**

**The defect.** Closing the Excel window with a live **streaming plain-rtd** topic crashed 100% of
the time. WER, identical every run: faulting module `<proj>.xll_unloaded` (i.e. an image that had
ALREADY been unmapped), `0xc0000005`, plus a companion fault inside Excel itself
(`mso20win32client.dll`, later `EXCEL.EXE`). Minimal repro matrix (window close): streaming RTD
**2/2 crash**; `rtd-once` only **0/2**; no RTD **0/2**.

**Symbolication (do this, don't guess).** The Release XLL is `-s`-stripped, so `addr2line` was
useless. Disassembling the shipped binary at the WER offset (`objdump -d` around
`ImageBase + 0x70a6a`) landed on `mov 0x28(%rbx),%rcx` — the instruction **immediately after
`WaitForSingleObject(handle, INFINITE)`** inside a function that locks a mutex, looks up a handle,
calls `GetHandleInformation`, checks `TlsGetValue` for self-join, waits, then `CloseHandle`s: that
is libwinpthread's **`pthread_join`**, linked INTO the XLL by `-static`. Exactly one `pthread_join`
and one out-of-line `std::thread::join()` exist in the image, with three shutdown-relevant call
sites (worker join, monitor join, and `DirectHost::Shutdown`'s guest-worker join — the last
unreachable, `Start()` is never called). ⇒ **the crash was a thread PARKED IN A JOIN inside the
graceful teardown.**

**Then an instrumented Release build (unconditional `LogTeardown` + thread-id markers + the
`lpReserved` bit) gave the mechanism outright:**
```
Phase 1 (host shutdown) returns fast              t+0
ServerTerminate ENTRY armed=1 → Phase 2 ENTRY     t+95 ms   (STA)
WorkerLoop EXIT ; "JoinWorker done; monitor join..."        (STA parks in pthread_join)
DllMain DLL_PROCESS_DETACH  lpReserved=NULL                 ← FreeLibrary: REAL UNMAP
<crash: the parked STA resumes in unmapped pthread_join>
```
and the control (no live RTD topics, no crash):
```
Phase 1 returns fast ; Phase 2 never runs
DllMain DLL_PROCESS_DETACH  lpReserved=0x1                  ← PROCESS EXIT: no unmap
```

**Two corrections to earlier §23.6 / §20.2 claims — both were wrong:**
* **"Phase 2 never ran" is FALSE.** It ran every time. `RunDestructiveTeardown` latches
  `g_isUnloading` as its FIRST act and every logger short-circuits on that flag, so the entire
  phase was invisible. Absence of log lines was not absence of execution. Fixed structurally with
  `xll::LogTeardown` (§20.2.1 rule 4).
* **"leak, don't crash" (§20.2) does not hold here** — see §20.2.1. The image really is unmapped on
  this path.

**Also measured (do not re-assume):** on this Excel build and this close path
`RtdServer::DisconnectData` is **NEVER called** — only `ServerTerminate` is. Keeping `g_phost` alive
past Phase 1 therefore buys nothing *here*; it is nevertheless KEPT, because `DisconnectData` **is**
delivered on the zero-topic-blip path (plain workbook close with the Application held — verified,
2 disconnects reaching the Go server) and the June-2026 measurements saw it at host shutdown too.

**The fix — three parts, in `internal/assets/files/`:**
1. **PIN the image on the confirmed-host-shutdown path** (`PinModuleToPreventUnmap`,
   `GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS | _PIN`), FIRST, before the COM hook (which pumps the
   STA) and before the quiesce. See §20.2.1 rule 1 for the scope argument. **This is the part that
   actually stops the crash** — without it, every other variant still died (the crash simply moved
   from our `pthread_join` to Excel dereferencing our `IRtdServer` vtable in the hole).
2. **Split the overloaded `g_isUnloading` flag.** It meant two things at once: (a) "stop background
   work / self-abort" and (b) "the destructive teardown started, `g_phost` is going away". Phase 1
   needs (a) but must NOT publish (b) (`DisconnectData` requires `g_isUnloading == false`), so it
   latched NEITHER — leaving the worker thread dispatching RTD updates and the hidden notify window
   pumping `IRTDUpdateEvent::UpdateNotify` into an already-shutting-down Excel. New
   `xll::g_isQuiescing` + `xll::TeardownStarted()` carry (a) alone. Latched in **exactly one place**
   (`BeginQuiesce`), reached only from `GracefulTeardownOnce`, so the cancelled-quit exposure is
   **identical to before** (that same call already did the irreversible ribbon disconnect and
   registry unregister). Reset in `DLL_PROCESS_ATTACH`. `DisconnectData`'s send gate deliberately
   stays `g_isUnloading`-ONLY — pinned by a negative assertion, because sweeping it into
   `TeardownStarted()` would resurrect the S1' ghost.
3. **`BeginQuiesce` (Phase 1) + the no-park rule.** Phase 1, in this exact order (kept in sync with
   `xll_lifecycle.cpp`; the steps are lettered there):
   (a) latch `g_isQuiescing` → (b) `SetEvent(hShutdownEvent)` (private, unnamed, non-inherited event —
   the Go server never sees it; it only releases `MonitorProcess`) → (c) `StopWorker` (non-blocking
   flag store) → (d)+(e) both §23.0 drains, **capturing** their verdicts → (f) a **bounded,
   non-parking reap** of the worker and monitor (`WaitForWorkerExit`/`WaitForMonitorExit`, 500 ms
   budget each, then `join()` only if the thread's own exit flag says it already returned, else
   `detach()`; a `detach()` on the *unpinned* add-in-disable path additionally **pins**, see rule 1)
   → (g) `DestroyRtdNotifyWindow` (on the creating STA — killing the leaked-`WndProc` hazard —
   deliberately AFTER the reap so no live worker can `PostMessage` into the destroy) →
   (h) `ReleaseCallbackForTeardown` (its documented precondition is "after the worker is reaped") →
   record `g_backgroundThreadsReaped` = *all four* verdicts ANDed. Then, back in
   `GracefulTeardownOnce`: the pin (host shutdown only, and BEFORE all of the above),
   `CancelDeferredRunner`, the COM hook, fast return.
   `RunDestructiveTeardown` (Phase 2, still triggered from `ServerTerminate`) keeps ONLY
   non-parking work: latch `g_isUnloading`, idempotent notify-window destroy + callback release,
   `delete g_phost` **gated on both threads having actually been reaped**, and the three
   `CloseHandle`s. Nothing essential depends on `ServerTerminate` firing: if it never does, DETACH's
   unconditional `CloseHandle(hJob)` still reaps the server and `g_phost`/handles leak into process
   exit (§20.2-accepted).

**MEASURED, real Excel x64 16.0.20228, faithful UIA window close, live streaming topics:**
BEFORE **3/3 crash** (`xll_unloaded` + `mso20win32client`; independently 3/3 on v0.8.40 and 3/3 on
v0.8.38, so it is not a v0.8.39 regression) → AFTER **0 crash events in 22 close iterations**:
primary gate `rtd` **0/5** (all five settling at **t+0 s**), plus `rtd` 0/3+0/5, `both` 0/3,
`once` 0/2, `nortd` 0/2, COM `Quit` 0/2 — every one with **zero residual `EXCEL.EXE` and zero
residual server**, RTD streaming (`Value2`, 5 distinct values per run) and `rtd-once` values correct
throughout. Post-fix timeline: Phase 1 (pin + quiesce, ~2 ms quiesce / ~25 ms incl. the COM hook) →
`ServerTerminate` → Phase 2 (~7 ms, server reaped) → `DLL_PROCESS_DETACH` with
`lpReserved != NULL` (**process exit, no unmap**).

**Regression gates (all PASS, real Excel — re-measured on the post-review tree):**
| # | Gate | Result |
|---|------|--------|
| ① | no ghost Excel | `EXCEL.EXE` residual 0, "settled at t+0s", 5/5 |
| ② | no orphaned server | `xll_showcase.exe` residual 0, 5/5 |
| ③ | RTD still works | `Value2` real values; `Clock` 5 distinct/run; `StockTick`/`YDP`/`YDH` correct |
| ④ | `DisconnectData` alive | plain workbook close (Application held) → **2 `RTD Disconnect request received`** in the Go log, server SURVIVES, re-subscribe on a new book streams again, and **0** pin/quiesce/Phase-2 lines (that close is not a host shutdown) |
| ⑤ | cancelled quit | `xlAutoClose`=1 while `GracefulTeardownOnce`=0 and pin/quiesce/Phase-2=0; Excel and the Go server both still alive |
| ⑥ | add-in DISABLE does not pin | `COMAddIn.Connect=$false` → `GracefulTeardownOnce`=1, Phase-1 quiesce=1, Phase 2 synchronous (server reaped), **`PINNED`=0** — the image stays unloadable on that path. ⚠ The gate is **SELF-POISONING**: disabling leaves `LoadBehavior=0`, so the harness must restore it to `3` before each run or the add-in never connects, `OnDisconnection` never fires, and the gate fails for an *environment* reason |
| ⑦ | pinned image + reload in the SAME process | `Application.Quit()` while the client HOLDS the `Application` → `PINNED`=1 and Phase 2 EXIT=1 while Excel stays ALIVE; then `RegisterXLL` in that same process → `[TEARDOWN] … state reset for this fresh load` **followed by** the template's `[INFO]` line (which can only print once the flags are actually cleared). Pre-fix that reload came back **silently half-dead** — review HIGH #2 |

**REVIEW ROUND (xll-cpp-reviewer, GO-conditional) — six findings fixed, all re-verified.**
The review found hazards in the FIX, not in the original defect. Each is pinned by
`internal/generator/gen_close_unload_review_test.go` (10 tests; all 10 fail on the parent commit):

* **MED #4 — Phase 2 closed the two WAITABLE handles unconditionally.** `MonitorProcess` parks in
  `WaitForMultipleObjects(2, { hProcess, hShutdownEvent })` — exactly those two. If Phase 1 had to
  DETACH the monitor, closing them under it lets the handle VALUES be recycled, and the monitor then
  wakes into its `WAIT_OBJECT_0` branch and calls `GetExitCodeProcess` on a foreign handle → modal
  `MessageBoxW("Server Crash")` during shutdown. They are now gated on the SAME
  `g_backgroundThreadsReaped` load as `delete g_phost`, and leaked on a miss (the treatment DETACH
  already documents for this pair). `CloseHandle(hJob)` stays UNCONDITIONAL — its closure IS the
  `KILL_ON_JOB_CLOSE` server reap and nothing waits on it.
* **MED #3 — the reap strategy must not fork on the path, and the disable path needs the pin on a
  miss.** The no-park rule is UNCONDITIONAL: a bare blocking `join()` on the add-in-disable path
  would freeze Excel's STA behind `MonitorProcess`'s modal MessageBox. Both paths therefore use the
  bounded exit-flag-then-join reap. `hostShutdown` now selects ONE thing: a budget miss on the
  add-in-disable path (which was NOT pinned on the way in, and where Excel unmaps as soon as
  `OnDisconnection` returns) **pins the image**, so the thread we just detached cannot execute out of
  a hole. Exactly TWO pin call sites exist; a third would break unload/re-enable.
* **MED #2 — the §23.0 drains did not feed the reap flag.** A timed-out RTD-connect / command drain
  logged "may still be touching g_phost" and then let `delete g_phost` proceed anyway. All FOUR
  verdicts (worker, monitor, rtd drain, command drain) now AND into `g_backgroundThreadsReaped`. And
  both detached-sender sites now refuse to SPAWN once `TeardownStarted()`, which is what makes the
  drains sound at all: the in-flight counter is incremented INSIDE the lambda, so a thread created
  after a drain returned would be invisible to it.
* **HIGH #2 — the PIN introduced a new regression: the lifecycle flags could never be reset.** They
  were cleared in exactly one place, `DLL_PROCESS_ATTACH`, and a pinned image gets no second ATTACH
  (`FreeLibrary`/`LoadLibrary` only move the reference count). After a host-shutdown teardown the
  flags would stay latched for the life of the process, so the next `xlAutoOpen` would build a fresh
  `g_phost` and server while `MonitorThread` returned at once and `WorkerLoop` broke out
  immediately — **silently half-dead** (no RTD updates, no async results) while still holding the XLL
  file lock. Reachable: `Application.Quit()` from a COM client that KEEPS its `Application` reference
  delivers `OnBeginShutdown` (so it pins) while Excel does NOT exit — and the showcase's own tooling
  does exactly that (4/4 observed). Fixed with `ResetLifecycleStateForFreshLoad()` +
  `xll::PrepareForFreshLoad()`, called at the top of `xlAutoOpen`: reset when the previous teardown
  actually reaped everything, else return `kUnrecoverable` and **fail the load loudly** (reviving
  background work beside a still-running detached thread is worse than refusing).
* **HIGH #1 — pin + quiesce exist ONLY in COM add-in builds.** `GracefulTeardownOnce`'s only call
  sites are `RibbonAddIn::OnBeginShutdown`/`OnDisconnection`, compiled under `XLL_RIBBON_ENABLED`,
  which `CMakeLists.txt.tmpl` defines only when the project has **BOTH** commands **AND**
  `ribbon.enabled`. `XLL_RTD_ENABLED` is independent. **So `rtd.enabled: true` with no ribbon and no
  commands has NO teardown at all and the unmap hazard is unmitigated** — and the 0/N verification
  was obtained on a ribbon+commands project. §20.2.1 rule 1 and this section must be read with that
  dependency in mind. `generate` now WARNS on that shape
  (`cmd/generate.go::rtdWithoutComAddInWarning`, unit-tested). **BACKLOG:** register a minimal
  `IDTExtensibility2` for every RTD build so the teardown exists regardless of the ribbon.
* **LOW #5/#6/#7/#8 —** the notify-window destroy moved to AFTER the thread reap (only a returned
  worker cannot `PostMessage` into the `DestroyWindow`); `MarkMonitorStarting()` pre-publishes
  "monitor not exited" before the thread object exists (mirrors `StartWorker`, closes the stale-flag
  window); `LogTeardownWarn` (WARN-gated) carries the seven diagnosis-critical failure lines so they
  survive `logging.level: warn`; `TryConnectRibbon` / `__xllgen_RibbonConnectRetry` switched to
  `TeardownStarted()` for §20.2.1 rule 2 consistency.
* **Re-verified after the review fixes** (real Excel, same environment): close-crash **0/5** with all
  five settling at `t+0 s` and zero residual; `ghost-check.ps1` **no ghost / no orphan** and
  `-FastClose` likewise (`EXCEL` exits at t+1 s, both logs FREE); `verify-ydp-stranding-uia.ps1`
  **PASS, 3/3 rounds** (the zero-topic-blip regression: `ServerTerminate` must not tear down when
  unarmed); gate ④ **PASS** (2 disconnects reach the Go server, server survives, re-subscribe
  streams, and 0 pin/quiesce/Phase-2 lines); gate ⑤ **PASS**. `go build`/`go vet` clean,
  `go test ./... -count=1` all green including the offline g++ gates and the real-compile cmake gates.
* **NOT re-verified after the review fixes (accepted residual):** the add-in-DISABLE path could not
  be driven on this machine afterwards — Office reports our COM add-in `Connect=False` at startup
  (the Office key is written by `xlAutoOpen`, i.e. after Office enumerates `COMAddIns`, and each
  teardown unregisters it again) and a programmatic `Connect = $true` returns `E_ABORT`. Since MED #3
  CHANGED that path (bounded reap + pin-on-miss), the pre-review measurement does not fully transfer.
  It rests on the structural pins (`TestCloseUnloadDisablePathBoundedReapAndPin`, including the
  exactly-two-pin-call-sites count) until someone can drive a real disable.

* **Files:** `internal/assets/files/src/xll_lifecycle.cpp`,
  `internal/assets/files/include/xll_lifecycle.h`,
  `internal/assets/files/src/xll_worker.cpp`, `internal/assets/files/include/xll_worker.h`,
  `internal/assets/files/src/xll_rtd_notify.cpp`, `internal/assets/files/src/xll_rtd.cpp`,
  `internal/assets/files/src/ribbon_addin.cpp`, `internal/assets/files/src/xll_events.cpp`,
  `internal/assets/files/src/xll_deferred_commands.cpp`,
  `internal/assets/files/include/rtd/server.h`,
  `internal/assets/files/src/xll_log.cpp`, `internal/assets/files/include/xll_log.h`,
  `internal/templates/xll_main.cpp.tmpl` (the `PrepareForFreshLoad` gate at the top of
  `xlAutoOpen`, `MarkMonitorStarting` before the monitor thread is constructed, the monitor
  re-creation after `start_worker:` for a reload of a pinned image — which MUST
  `ResetEvent(hShutdownEvent)` first, because that event is manual-reset and Phase 1 leaves
  it signalled, so without the reset the new monitor returns through its shutdown branch
  immediately and the re-creation is inert (caught in review round 2); `launchLog` is
  `static` for the same reason, since the reload path GOTOs past `LaunchServer` — and the two
  `TeardownStarted()` gates in the ribbon connect/retry path),
  `internal/generator/testdata/golden/xll_main.cpp.golden` (regenerated — the two non-ribbon-gated
  template hunks), `cmd/generate.go` (the `rtd.enabled` without-COM-add-in warning),
  plus `pkg/server/handlers.go` (the missing `RTD Disconnect request received` Info log — the
  observability hole that made the DisconnectData claim uncheckable).

**NEW USER-VISIBLE BEHAVIOUR (three things, all deliberate):**
1. **`xlAutoOpen` can now REFUSE to load.** `xll::PrepareForFreshLoad()` returns `kUnrecoverable`
   when a previous teardown in this process left a background thread DETACHED rather than reaped;
   `xlAutoOpen` then logs through `LogTeardownWarn` and returns 0. Rationale: the alternative is
   re-arming background work beside a thread we no longer control. It is reported via
   `LogTeardownWarn`, NOT `SAFE_LOG_ERROR` — the latter expands to `if (!g_isUnloading) LogError(...)`
   and `g_isUnloading` is normally STILL LATCHED on that path, so the "loud" failure would have been
   double-suppressed into silence (the exact blind spot that made this defect expensive to diagnose).
2. **`xll-gen generate` warns** when `rtd.enabled: true` but the project has no ribbon AND no
   commands — that shape defines no `XLL_RIBBON_ENABLED`, hence no COM add-in, hence NO teardown and
   no pin at all (see the dependency note above). Advisory only.
3. **`xll::LogTeardownWarn()`** is a new WARN-gated companion to `LogTeardown`; both bypass the
   `g_isUnloading` suppression. All SEVEN teardown failure messages route through it so they survive
   `logging.level: warn` — which is exactly the level a user reporting a close-time problem is
   likely to be running. The step-narration lines stay on the INFO-gated `LogTeardown`.
* **Regression tests:** `internal/generator/gen_close_unload_test.go`
  (`TestCloseUnloadPinsImageOnHostShutdown`, `TestCloseUnloadQuiesceFlagSplit` — including the
  negative `DisconnectData` assertion, `TestCloseUnloadNoParkAfterPhase1`,
  `TestCloseUnloadTeardownIsLoggable`); every assertion fails on the parent commit. Updated to the
  new shape: `gen_close_ghost_test.go`, `gen_cancel_quit_test.go`, `gen_rtd_connect_test.go`,
  `gen_ribbon_connect_test.go`, `gen_rtd_notify_window_test.go`,
  `internal/assets/assets_test.go::TestScheduleOnTimeMacroGuards`.
* **C++ asset change touching `DllMain`/unload reasoning — `xll-cpp-reviewer` pass REQUIRED before
  commit.**
**REVIEW ROUND 2 (2026-07-29, `xll-cpp-reviewer` NO-GO → resolved). Six findings on the FIX itself,
all fixed; the design (host-shutdown-only pin + flag split + no-park) was confirmed and NOT reworked:**
* **BLOCKER — it did not compile.** `ResetLifecycleStateForFreshLoad()` was defined ABOVE the five
  statics it writes; namespace-scope lookup ends at the definition point. Definition moved below
  them. **PROCESS LESSON (this is the durable part):** the first `go test ./...` reported green
  because `./cmd` came from the BUILD CACHE. Any edit to a C++ asset MUST be followed by
  `go test -count=1 ./cmd/... -run 'CppCompile|Compiles'`, and "passed" may only be written about a
  run made on the FINAL tree. Third observation-tool illusion in this repo after `.Text` and the
  `g_isUnloading` log short-circuit.
* **HIGH — the "deliberately LOUD" refusal was silent.** `xlAutoOpen`'s `kUnrecoverable` arm used
  `SAFE_LOG_ERROR`, which expands to `if (!g_isUnloading) LogError(...)` — and the commonest way to
  reach that arm is with `g_isUnloading` STILL LATCHED from the previous Phase 2. Double-suppressed
  into nothing, leaving only "the add-in silently does not load". Now `xll::LogTeardownWarn`, pinned
  by a NEGATIVE assertion (`the arm must not contain SAFE_LOG_`).
* **MED — drain timeouts did not reach `g_backgroundThreadsReaped`.** Only the two threads fed it, so
  a timed-out RTD-connect/command drain logged "a sender may still be touching `g_phost`" and then
  let Phase 2 delete it anyway. The flag is now the AND of **all four** verdicts. Additionally,
  `ConnectData`/`SendCommandInvoke` now refuse to SPAWN a detached sender once `TeardownStarted()` —
  their in-flight guards are acquired INSIDE the lambda, so a request arriving in the ~95 ms window
  after the drain finished was invisible to it.
* **MED — the add-in-disable path's unconditional blocking `join()`** could freeze Excel's STA behind
  `MonitorProcess`'s modal `MessageBoxW("Server Crash")`. Both paths now use the bounded, exit-flag-first
  reap; a budget MISS on the (otherwise unpinned) disable path additionally **pins**, so the detached
  thread cannot be executing in a hole when Excel unmaps after `OnDisconnection` returns. §20.2.1
  rule 3 is therefore unconditional again — code and doc agree.
* **MED — doc drift** (the same class of defect that caused the original misdiagnosis): the Phase-1
  step order in §23.6/`xll_lifecycle.h`, §22.4's claim about where the notify window is destroyed, the
  Files list, and three unrecorded user-visible behaviours. All corrected here.
* **LOW** — the `Phase 2 MUST NOT PARK` doc block had drifted onto the wrong function; a co-change
  anchor now records WHY `delete g_phost` does not park (shm's `GuestCallWorker::Stop` only joins when
  `guestWorkerRunning`, and this XLL never calls `DirectHost::Start()`); `MarkMonitorStarting()`
  pre-publishes the monitor's exit flag like `StartWorker` does; a pinned reload now recreates the
  monitor thread after `start_worker:` — **`ResetEvent(hShutdownEvent)` first**, or the recreation is
  inert (manual-reset event still signalled from Phase 1 → the new monitor exits through its
  shutdown branch at once); `launchLog` is `static` so that path still has the Go log path to show
  on a later server crash; `WorkerExited`/`MonitorExited` documented as diagnostics.
  Verified separately: no harness greps on the `[INFO]`/`[WARN]` level tokens, so the new
  `TEARDOWN`/`TEARDOWN-WARN` tokens break nothing.

* **`DLL_PROCESS_DETACH` is UNCHANGED and still wait-free** (§20.1/§20.2): unconditional
  `CloseHandle(g_procInfo.hJob)` (a kernel call — the `KILL_ON_JOB_CLOSE` reap), then, only when no
  graceful teardown preceded it, `g_isUnloading = true` + `SetEvent` + **`.detach()`** of the
  worker/monitor. No join, no drain, no `delete g_phost`, no wait of any kind. After a host-shutdown
  Phase 1 both threads have already been reaped, so those `joinable()` checks are false and the
  detaches are no-ops.
* **Phase 1's added latency is BOUNDED** (it must return fast so Excel proceeds to its RTD
  handshake): the thread reap is `kThreadReapBudgetMs` = **500 ms per thread**, and the `join()` is
  issued ONLY after the thread's exit flag says it already returned, so the join itself cannot park.
  Over budget ⇒ `detach()` and record the miss (Phase 2 then leaks `g_phost` and the two waitable
  handles rather than freeing them under a live thread). The two §23.0 drains keep their pre-existing
  2 s caps. **WORST CASE = 2000 + 2000 + 500 + 500 ≈ 5 s** of bounded waiting inside
  `OnBeginShutdown`; **measured 2 ms** (the worker is a poll loop, the monitor is one
  `WaitForMultipleObjects` that Phase 1 signals). That 5 s ceiling is written down deliberately:
  "Phase 1 RETURNS FAST" is the load-bearing premise of the §23.6 Stage-4 ghost fix, so a future
  ghost regression will want to suspect this number. Exceeding the budget does NOT reopen the crash
  window — the image is pinned by then, which is exactly what makes "leak, don't crash" sound again.
* **Accepted residual (recorded, not fixed):** "the drain completed" does NOT strictly mean "that
  thread has left the image". The in-flight counters are decremented by an RAII destructor, so the
  lambda's own capture destructors and libstdc++/libwinpthread's thread-exit code still run inside
  the image afterwards. Harmless on the pinned path (nothing unmaps); it is the reason the drains are
  a soundness input to the reap flag rather than a proof of departure.
* **Also recorded:** `WriteLogUnconditional` takes a mutex, so `LogTeardown`/`LogTeardownWarn` can
  park briefly behind another logger. Bounded by that logger's own I/O and not on any unmap-exposed
  path; a `try_lock` + line-drop variant is the obvious hardening if it ever matters.
* **Residual / not covered:** a literal Save-prompt **Cancel** click could not be driven in the
  verification environment (synthetic keyboard input is refused by UIPI; `Close-ExcelWindowFaithful`
  auto-clicks Don't-Save; and the prompt did not surface as a top-level `#32770` for
  `WM_COMMAND`/IDCANCEL). Gate ⑤ therefore rests on (a) a measured close that ran `xlAutoClose` and
  then did NOT proceed — leaving exactly the add-in-visible cancelled-quit state, with
  `GracefulTeardownOnce`/pin/quiesce all absent and both processes alive — and (b) the
  single-latch-site argument above. A post-cancel cell re-read is NOT available: after a cancelled
  quit Excel de-registers itself from the ROT and refuses automation (measured).
  Separately observed and **pre-existing** (not caused by this fix): `Application.Quit()` from a COM
  client that KEEPS its `Application` reference delivers `OnBeginShutdown` even though Excel does
  **not** exit — so the confirmed-shutdown teardown runs and the add-in is left torn down in a
  session that continues. That is a §20.3 hole in the "confirmed shutdown" signal set, not in this
  change; it needs its own fix.

## 24. CLAUDE.md / Agent Tool Compatibility

This repository is configured so that AI tools using `CLAUDE.md` (Claude Code) read this `AGENTS.md` as the authoritative source. **All durable agent guidance must live here, not in `CLAUDE.md`.** `CLAUDE.md`, if present, must contain only a one-line redirect to this file.
