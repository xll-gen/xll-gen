# xll-gen

![cover](cover.png)

> This tool is currently in **beta** and under active development.

`xll-gen` is a command-line interface (CLI) tool designed to streamline the creation of Excel Add-ins (XLL) using an out-of-process architecture. By leveraging Shared Memory for high-performance Inter-Process Communication (IPC), it allows developers to write Excel extensions in languages like Go, bypassing the complexity and limitations of traditional C++ XLL development.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Configuration](#configuration-xllyaml)
- [CLI Reference](#cli-reference)
- [Debugging](#debugging)
- [Troubleshooting](#troubleshooting)
- [License](#license)

## Overview

Traditional Excel XLLs are Dynamic Link Libraries (DLLs) loaded directly into the Excel process. This model works well for C/C++ but poses significant challenges for garbage-collected languages like Go, which have heavy runtimes and are difficult to embed as shared libraries.

`xll-gen` solves this by decoupling the logic:
1.  **Excel Shim (C++)**: A lightweight, auto-generated XLL acts as a bridge. It runs inside Excel, registers functions, and forwards calls.
2.  **User Server (Go)**: Your business logic runs in a separate, standalone process.
3.  **Shared Memory IPC**: Data is exchanged via a low-latency shared memory ring buffer using Google Flatbuffers for serialization, ensuring near-native performance.

## Architecture

The system operates in a `singlefile` mode by default, providing a seamless user experience:

1.  **Excel Process**: Loads `project.xll`.
2.  **XLL Shim**:
    - Automatically extracts the embedded Go server executable to a temporary directory (using Zstd compression).
    - Initializes a shared memory region.
    - Spawns the extracted User Server process.
3.  **User Server**: Connects to the shared memory region and listens for requests.
4.  **Data Flow**:
    - Excel calls a function (e.g., `=Add(1, 2)`).
    - XLL serializes arguments to Flatbuffers and writes to Shared Memory.
    - User Server reads the request, computes the result, and writes the response back.
    - XLL deserializes the response and returns it to Excel.
5.  **Failure Handling**: If the User Server crashes, the XLL detects the process termination and alerts the user via a message box.

## Features

*   **Functions**: Support for both **Synchronous** and **Asynchronous** User Defined Functions (UDFs). Asynchronous functions prevent Excel UI freezing during long computations.
*   **Events**: Ability to handle Excel events, such as `CalculationEnded`, to trigger post-calculation logic.
*   **Commands**: Mechanism to schedule write operations (`xlSet`) and formatting changes (`xlcFormatNumber`) that are executed safely after the calculation cycle.
*   **Real-Time Data (RTD)**: Support for real-time data streaming using the standard Excel RTD interface.

## Prerequisites

Before using `xll-gen`, ensure you have the following installed:

*   **Go**: Version 1.24 or later.
*   **CMake**: Version 3.24 or later (required for building the C++ XLL).
*   **C++ Compiler**:
    *   **Windows**: MSVC (`cl.exe`) OR MinGW (`g++`/`gcc`).
    *   *Recommendation*: Install MinGW via winget:
        ```powershell
        winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT
        ```
*   **Excel**: Microsoft Excel 2007 or later (Windows).
*   **Task** (Required for `xll-gen build`): [go-task](https://taskfile.dev/) is used to orchestrate the build process.

## Installation

Install the tool using `go install`:

```bash
go install github.com/xll-gen/xll-gen@latest
```

Ensure that your `$(go env GOPATH)/bin` is in your system PATH.

## Quick Start

### 1. Initialize a Project

Create a new directory and scaffold a project:

```bash
xll-gen init my-quant-lib
cd my-quant-lib
```

This creates:
- `xll.yaml`: Project configuration.
- `main.go`: The entry point for your Go server.
- `Taskfile.yml`: Build automation script.

### 2. Configure Functions

Edit `xll.yaml` to define your Excel functions. See [Configuration](#configuration-xllyaml) for details.

### 3. Generate Code

Run the generator to create the necessary C++ and Go glue code:

```bash
xll-gen generate
```

### 4. Build

Build the project (Go server + C++ XLL):

```bash
xll-gen build
```
*Note: This command requires `task` to be installed. Alternatively, you can run `task build` directly.*

### 5. Run

Open Excel and load the generated XLL file located in `build/my-quant-lib.xll`. Your functions (e.g., `=Add(1,2)`) should now be available.

## Configuration (`xll.yaml`)

The `xll.yaml` file is the single source of truth for your add-in.

> **Strict parsing.** Unknown or misspelled keys are rejected: `xll-gen` fails
> with a line-numbered error (e.g. `line 12: field tImeout not found in type ...`)
> rather than silently ignoring a typo. Fix or remove the offending key.

```yaml
project:
  name: "my-project"
  version: "1.0.0"          # YOUR add-in version, independent of xll-gen

gen:
  go:
    # Generated Go package name (default "generated"). Also names the output
    # directory and import-path segment: code lands in <project>/<package>/
    # and is imported as "<module>/<package>". Must be a valid Go identifier.
    package: "generated"
  # disable_pid_suffix: by default the SHM name is "<project>_<pid>" so a
  # second XLL instance never collides with the first. Set to true ONLY for
  # tests/dev where you need a deterministic SHM name and guarantee no
  # concurrent instance.
  disable_pid_suffix: false

build:
  # 'xll' embeds the Go server executable inside the XLL file (default).
  singlefile: xll
  # Directory to extract embedded binary to (default: ${TEMP})
  temp_dir: "${TEMP}"

logging:
  level: "info"
  # ONE directory for BOTH log files (<proj>_native.log and <proj>_go.log).
  # Supports ${XLL_DIR}, ${BIN_DIR}, ${TEMP} and absolute paths.
  # Default (empty) = ${BIN_DIR}: the XLL directory in standalone mode, or the
  # extraction directory <temp_dir>\<project>\ in singlefile mode.
  dir: ""

server:
  workers: 0         # 0 = Use runtime.NumCPU()
  timeout: "10s"     # Default timeout for synchronous requests
  launch:
    enabled: true    # Automatically start the Go server when XLL loads
    # command: "${BIN}" # Optional: Defaults to the server executable
    # cwd: "${BIN_DIR}" # Optional: Defaults to the directory containing the executable
  # chunk: (optional, v0.3.5+) — tune the runtime ChunkManager.
  #   Omit the block to keep defaults: 256 MiB cap, 30s sweep, 60s idle TTL.
  # chunk:
  #   max_buffer_bytes: 134217728   # 128 MiB cap on per-transfer reassembly
  #   cleanup_interval: "30s"        # Sweep cadence
  #   buffer_ttl: "60s"              # Idle window before eviction

# Real-Time Data (RTD) Server Configuration
rtd:
  enabled: true
  prog_id: "MyProject.RTD"
  # throttle_interval: "250ms"  # optional — sets Application.RTD.ThrottleInterval
  #   at xlAutoOpen (Excel default: 2s). CAUTION: per-user, registry-persisted
  #   Excel setting; it stays changed after the add-in unloads. Only set when
  #   your RTD feeds need sub-2s pushes.

functions:
  - name: "StockQuote"
    description: "Streams live stock quotes"
    args:
      - name: "symbol"
        type: "string"
    return: "any"
    mode: "rtd"      # Real-time data mode

  - name: "Add"
    description: "Adds two integers"
    args:
      - name: "a"
        type: "int"
        description: "First number"
      - name: "b"
        type: "int"
        description: "Second number"
    return: "int"
    category: "Math"
    shortcut: "Ctrl+Shift+A"

  - name: "GetPrice"
    description: "Fetches price for a ticker"
    args:
      - name: "ticker"
        type: "string"
    return: "float"
    mode: "async"    # Asynchronous mode ('async: true' is deprecated)
    help_topic: "https://example.com/help/GetPrice" # Optional: Help topic URL
    caller: true     # Optional: Passes the calling cell range as an argument
```

### Launch Configuration Variables

The `server.launch` section supports the following variables:

*   `${BIN}`: Resolves to the full path of the server executable. (`command` only)
*   `${BIN_DIR}`: Resolves to the directory containing the server executable. In `singlefile` mode, this is the temporary extraction directory. (`command` and `cwd`)
*   `${XLL_DIR}`: Resolves to the directory containing the `.xll` file. (`command` and `cwd`)

Defaults: an empty/omitted `command` launches the server executable (equivalent
to `"${BIN}"`); an empty/omitted `cwd` uses the server executable's directory
(equivalent to `"${BIN_DIR}"`). A relative `cwd` resolves against `${BIN_DIR}`.
The legacy top-level `server.command` is still honored, but
`server.launch.command` wins when both are set.

To pass arguments (or use a wrapper), quote the executable token yourself,
e.g. `command: "\"${BIN}\" --my-flag"` — an unquoted multi-token command is
wrapped whole in one quote pair and treated as a single executable path.

### Supported Types

| Type | Description | Go Arg Type | Go Return Type | Excel Type |
| :--- | :--- | :--- | :--- | :--- |
| `int` | 32-bit Integer | `int32` | `int32` | `int` |
| `float` | 64-bit Float | `float64` | `float64` | `double` |
| `bool` | Boolean | `bool` | `bool` | `boolean` |
| `string` | Unicode String | `string` | `string` | `string` |
| `date` | Excel date (serial) | `time.Time` | *(not a return type)* | `double` (serial) |
| `any` | Any Value (Scalar/Array) | `*types.Any` | `any` | `CheckRange/Variant` |
| `range` | Reference to a range | `*types.Range` | *(not a return type)* | `Reference` |
| `grid` | Generic 2D Array (mixed cells) | `*types.Grid` | `[][]any` | `Array` (spills) |
| `numgrid` | Numeric 2D Array (dense doubles) | `*types.NumGrid` | `[][]float64` | `FP Array` (spills) |

When a handler returns `grid` (`[][]any`) or `numgrid` (`[][]float64`), the value
**spills** into the surrounding cells on Excel 2021+/365 — see *Dynamic arrays
(spill)* below. `range` and `date` are argument-only: `range` can't return a
live reference (it breaks Excel registration), and `date` has no response-path
encoding yet. A `date` argument arrives as an Excel serial and is decoded to a
`time.Time` in the generated server.

**Optional Function Flags**:
*   `caller: true`: Passes an additional `caller *types.Range` argument to the handler, representing the cell(s) calling the function. This is **position-only**: the wrapper calls `xlfCaller` (callable from any worksheet function) and reports the caller's range, but `caller.Format()` (the cell's number-format string) is left empty unless the function also sets `macro: true`. Caller-only functions stay **thread-safe**.
*   `macro: true`: Registers the function as a **macro-sheet equivalent** (`#`), granting macro-level C-API access inside the C++ wrapper — in particular the caller's number-format fetch (`xlfGetCell`) that populates `caller.Format()`. The cost is that Excel rejects the `#`+`$` combination, so a `macro: true` function is **not** registered thread-safe. It does **not** make Excel's COM object model writable from Go handlers during calculation — sheet writes belong in commands. `macro: true` is incompatible with `mode: "rtd-once"` (same as `caller: true`).

> **Note**: Nullable scalar types (`int?`, `float?`, `bool?`, `string?`) are **not supported**. Use `any` to handle missing or nil values (checking for `xltypeMissing`).

### Dynamic arrays (spill)

`sync` and `async` functions may return a 2D array that Excel **spills** across
the cells below/right of the formula cell:

* **`grid`** — handler returns `[][]any` (row-major). Each cell may be
  `nil`, `bool`, `string`, an integer (`int`/`int32`/…), or a float
  (`float32`/`float64`). Use this for tables with mixed types.
* **`numgrid`** — handler returns `[][]float64` (row-major, dense). Lower
  overhead than `grid` when every cell is numeric; serialized as an FP12 array.

Both must be **rectangular and non-empty** (every row the same length, at least
one cell). A malformed grid (jagged or empty) is reported as the function's
error, so the cell shows the message instead of garbage.

```yaml
functions:
  - name: Identity3
    mode: sync
    return: numgrid          # spills a 3x3 matrix
  - name: BuildTable
    mode: sync
    return: grid             # spills a mixed-type table
```

```go
func (s *Service) Identity3(ctx context.Context) ([][]float64, error) {
    return [][]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}, nil
}

func (s *Service) BuildTable(ctx context.Context) ([][]any, error) {
    return [][]any{
        {"Name", "Qty", "InStock"},
        {"Widget", 42, true},
        {"Gadget", 7, false},
    }, nil
}
```

**No version detection or registration flag is required.** An XLL function
registered with return code `Q` (for `grid`) or `K%`/FP12 (for `numgrid`) that
returns an array value spills automatically on **dynamic-array Excel
(2021+/365)**. On **pre-dynamic-array Excel** the formula returns the
**top-left cell**; to see all cells the user must enter the formula as a legacy
**CSE array** (`Ctrl+Shift+Enter`) over a pre-selected range. `async` functions
spill the same way (the async result is converted via the same array path).

### Choosing an Execution Mode (sync vs async vs rtd vs rtd-once)

A common surprise: **`mode: "async"` does not keep the sheet responsive.**
Excel holds the calculation transaction open until every pending async
result has been delivered via `xlAsyncReturn` — no new recalculation
(volatile functions, RTD-triggered recalcs) starts in the meantime, so a
single long async call *feels* exactly like a blocking sync call. What
async actually buys you is **concurrency**: N async calls in the same
calculation overlap (e.g. 3 × 1.5s calls complete in ~1.5s wall clock,
not 4.5s), and dependents only ever see the final value.

The RTD pattern is the opposite trade: the cell returns immediately
(placeholder), the calculation completes, the sheet stays live, and the
result arrives via an RTD push that recalculates just that topic. This is
the same reason Excel-DNA implements its async support on top of RTD.

| Handler duration | Recommended mode | Rationale |
| :--- | :--- | :--- |
| Up to a few hundred ms | `sync` (default) | Lowest overhead; no perceptible stall. |
| Hundreds of ms – a few s, downstream formulas must only see the final value | `async` | Calls overlap; dependents never compute against a placeholder. |
| Multiple seconds, interactive feel matters, **one-shot** result | `rtd-once` | Write a normal handler; the generator wraps it in an RTD lifecycle. Cell shows `#GETTING_DATA`, then the value. |
| A genuinely streaming/recurring topic (ticks, live quotes) | `rtd` | You manage the topic and push updates over its lifetime. |

#### `rtd-once`: long one-shot work without writing RTD plumbing

`mode: "rtd-once"` is the recommended mode for a single long-running
computation when you want the sheet to stay live. You author the handler
**exactly like a sync function** — args in, result out:

```yaml
rtd:
  enabled: true
  prog_id: "MyProject.Rtd"
  throttle_interval: "250ms"   # recommended: lower Excel's 2000ms default so the one-shot result appears promptly

functions:
  - name: SlowAdd
    mode: rtd-once
    args:
      - { name: a, type: int }
      - { name: b, type: float }
    return: float
    # memoize_ttl: 30s         # optional — reuse the result for 30s (see below)
    # memoize: true            # optional — see below
```

```go
// A NORMAL handler — NOT an _RTD push handler.
func (s *Service) SlowAdd(ctx context.Context, a int32, b float64) (float64, error) {
    time.Sleep(3 * time.Second) // long work
    return float64(a) + b, nil
}
```

The generator wraps this in an RTD topic lifecycle: connect → run the
handler **exactly once** → push the result → the cell resolves and the topic
is released. The cell shows **`#GETTING_DATA`** while waiting (not the textual
"Connecting…" used by plain `rtd`). On a handler error, the error string is
pushed so the cell stops waiting.

* **Scope (current):** scalar (`int`/`float`/`string`/`bool`) **or** composite
  (`range`/`grid`/`numgrid`/`any`) args, and a scalar or `any` return.
  Composite args travel the **content-hash payload path** (see below) — and
  because the content hash flows into the once-key, memoization is
  content-addressed: the same input grid hits the cached result, while an
  edited grid recomputes. Composite *returns* are still rejected (the RTD push
  result can carry only scalars / `any`).
* **Result lifecycle — the once / `memoize_ttl` / `memoize` triad** (keyed by
  function name + args; at most one of `memoize_ttl` / `memoize` may be set):
  * **once (default, neither flag set):** the completed result is cleared at
    end of calc cycle, so the next user-initiated recalc (F9) **recomputes** —
    normal worksheet semantics.
  * **`memoize_ttl: "<duration>"`** (e.g. `"30s"`, `"5m"`): the middle ground.
    The cached result is reused for recalcs **within** the TTL; once the TTL
    has elapsed, the next call **recomputes fresh**. Age is measured from when
    the result was stored, using a monotonic clock (immune to wall-clock
    changes). Must be a positive Go duration.
  * **`memoize: true`:** the completed result persists until the add-in unloads
    — an implicit per-input memoization cache with no expiry.
* **Throttle:** set `rtd.throttle_interval` (e.g. `"250ms"`) so the one-shot
  value surfaces quickly instead of waiting up to Excel's 2000ms default RTD
  batch window.

RTD caveats to keep in mind when using it for one-shot computations:

*   **Throttle**: Excel batches RTD updates on `Application.RTD.ThrottleInterval`
    (default **2000ms**), so short results can appear *later* than async would
    deliver them. Configure `rtd.throttle_interval` in `xll.yaml` to have the
    XLL set it explicitly at load — but note the setting is per-user and
    registry-persisted, so it outlives the add-in.
*   **Placeholder propagation**: the cell completes once with `#N/A` (or an
    initial value) before the real result arrives, and dependent formulas
    compute against that placeholder once.
*   **F9 semantics**: while a topic stays connected, recalculation does not
    re-run the handler (implicit memoization). Disconnect the topic after
    completion if you want recalc-reruns.
*   **Arguments**: scalar arguments are serialized directly into RTD topic
    strings. Composite arguments (`grid`/`range`/`numgrid`/`any`) use the
    **content-hash payload path** — the topic carries only a content hash of
    the argument, while the payload itself travels once per calc cycle over the
    normal shared-memory path and is cached hash→payload on the Go side. Topic
    identity therefore tracks the argument's *content*: the same grid maps to
    the same topic (so two cells with the same input share the work), and
    editing a cell in the input yields a new hash → a new topic → a fresh
    compute. This applies to both `mode: "rtd"` and `mode: "rtd-once"`.

### Custom FlatBuffers Includes

The code generator runs `flatc` with the `--no-includes` flag. This means:
1.  Code is generated **only** for the main `schema.fbs` (derived from `xll.yaml`).
2.  If you manually modify `schema.fbs` to `include` other custom `.fbs` files, their code will **not** be generated automatically.
3.  You must manually generate code for any extra included files if you need them.

This design ensures that the pre-compiled `protocol.fbs` (system types) is used efficiently without regeneration.

## Events

`xll-gen` can subscribe to Excel application events. Declare them in `xll.yaml` and implement matching handlers on your service:

```yaml
events:
  - type: CalculationEnded
    handler: OnCalculationEnded
```

```go
func (s *Service) OnCalculationEnded(ctx context.Context) error {
    // Runs after each Excel recalc completes. Safe place to issue
    // ScheduleSet / ScheduleFormat calls (see "Command Scheduling").
    return nil
}
```

> ⚠️ **Event handlers must NOT drive Excel synchronously over COM.** A `CalculationEnded` handler runs while Excel's main (STA) thread is **blocked** inside a synchronous round-trip: at calc-end the XLL calls the handler and waits for it to return so it can apply any scheduled commands in the same cycle. If the handler tries to manipulate Excel via COM — e.g. attaching with `sugar` and reading/writing cells (`UsedRange().Find(...)`, `Range(...).SetValue(...)`) — those calls need the very STA thread that is blocked, so they **deadlock until a ~2-second timeout fires on every recalc**, freezing Excel and making typing nearly impossible. To change cells or formatting from an event handler, use `generated.ScheduleSet` / `generated.ScheduleFormat` (see [Command Scheduling](#command-scheduling)): these enqueue deferred commands the XLL applies on the STA thread *after* the handler returns. Note this restriction applies to **event handlers only** — **command** handlers (ribbon/macro) run when the STA thread is free, so synchronous `sugar` COM automation is fine there.

Supported event types map onto Excel's `xlEvent*` constants — `CalculationEnded`, `CalculationCanceled`. If your handler is omitted but the event is needed internally (e.g. `any`-typed args or `cache.enabled`), `xll-gen` registers a built-in `CalculationEnded` handler automatically.

## Commands & Ribbon

> **Note:** "Commands" here are user-invoked macros (ribbon buttons, keyboard shortcuts, Alt+F8) — distinct from "Command Scheduling" below, which defers `xlSet`/formatting until after recalc.

`xll-gen` can register **commands** (Go handlers invoked as Excel macros) and optionally surface them as native **Ribbon** buttons. A command is independently invocable; a ribbon button merely points at one.

```yaml
commands:
  - name: RunReport            # exported command name; valid identifier [A-Za-z0-9_]+, no leading digit
    description: "Generate the monthly report"
    handler: RunReport         # Go method name; defaults to name
    shortcut: "R"              # optional single letter; Excel binds it as Ctrl+Shift+R

ribbon:                        # optional; the two modes below are MUTUALLY EXCLUSIVE
  # -- mode 1: structured (xll-gen generates the customUI XML) --
  tab: "My Tools"
  groups:
    - label: "Reports"
      buttons:
        - label: "Monthly Report"
          command: RunReport   # must match a commands[].name
          size: large          # large | normal (default normal)
          image: "report"      # built-in Office icon (imageMso name)
        - label: "Export"
          command: ExportData
          size: normal
          image: "./icons/export.png"   # OR an image file relative to xll.yaml

  # -- mode 2: raw XML escape hatch (full customUI control) --
  # xml: "ribbon.xml"          # path relative to xll.yaml; every onAction="X"
                               # must match a commands[].name (checked at build time)
```

Your service implements one method per command, following the event-handler shape (`ctx` first, `error` return):

```go
func (s *Service) RunReport(ctx context.Context, cmd server.CommandContext) error {
    // server = github.com/xll-gen/xll-gen/pkg/server
    // cmd.CommandName, cmd.ControlID (empty for shortcut/Alt+F8), cmd.ExcelPID
    // Optionally attach to the running Excel with sugar and manipulate it.
    return nil
}
```

Notes:

*   **Button images** (`image:`) accept either a built-in Office icon name (imageMso, e.g. `HappyFace`) **or** a path to an image file relative to `xll.yaml` (`.png` `.jpg` `.jpeg` `.bmp` `.gif` `.ico`). File images are embedded into the `.xll` at build time and decoded with GDI+ at runtime — PNG transparency is preserved. Recommended sizes: 16×16 for `size: normal`, 32×32 for `size: large`; JPG has no alpha channel so it renders as an opaque square. Max 1 MiB per file; the same file referenced by several buttons is embedded once. (File images require structured ribbon mode — the raw-XML escape hatch rejects them.)
*   **Shortcuts** are a single letter, bound by Excel as `Ctrl+Shift+<letter>`.
*   **Alt+F8**: registered commands are runnable by typing their exact name into Excel's macro dialog (XLL commands are runnable there but not *listed*).
*   **Fire-and-forget**: clicking a button or firing a shortcut returns to Excel immediately; the handler runs server-side in a goroutine. Errors/panics are logged server-side and do **not** surface to the cell/UI — give the user feedback from inside the handler (e.g. write a cell or the status bar via `sugar`).
*   **Graceful degradation**: on group-policy-locked desktops where the ribbon's HKCU COM-add-in registration is blocked, worksheet functions / RTD / async keep working unchanged and commands stay invocable via shortcut and Alt+F8. The failure is silent except for a logged warning — no startup error dialogs.

## Command Scheduling

You can schedule Excel commands (like `xlSet` or formatting) to run after the calculation cycle ends. This is useful for modifying cells or formatting, which is restricted during function execution.

1.  Register the `CalculationEnded` event in `xll.yaml`.
2.  In your UDF or event handler, use `generated.ScheduleSet` or `generated.ScheduleFormat`.

```go
func (s *Service) OnCalculationEnded(ctx context.Context) error {
    // Schedule setting cell A1 to 100
    // generated.ScheduleSet(targetRange, value)
    return nil
}
```

## CLI Reference

> **Colored output** is enabled only when writing to an interactive terminal.
> It auto-disables when output is piped/redirected or when the `NO_COLOR`
> environment variable is set.

### `init <name>`
Scaffolds a new project structure.
*   `-f, --force`: Overwrite existing directory.

### `generate`
Generates C++ and Go source code based on `xll.yaml`.

### `build`
Wraps `task build` to compile the project. Requires `task` to be installed.

### `doctor`
Checks the environment for required tools (C++ compiler, `flatc`). It enforces
minimum versions — **Go ≥ 1.24** and **CMake ≥ 3.24** — and warns when Visual
Studio is installed but `cl.exe` is not on `PATH` (run `xll-gen` from a
*Developer Command Prompt for VS* so the compiler is on `PATH`). In a
non-interactive shell (output piped, CI) `doctor` **suggests** the `winget`
install command instead of prompting to run it.

## Debugging

`xll-gen` supports debugging both the C++ shim and the Go server using VS Code.

**Setup**:
1.  Install the **Go** and **C/C++** extensions for VS Code.
2.  Use the generated `.vscode/launch.json` configuration. Generated projects
    git-ignore the whole `.vscode/` directory; `xll-gen init` regenerates
    `.vscode/launch.json` (with your machine's Excel path baked in) when needed.

**Steps**:
1.  **Build** the project.
2.  **Launch Excel**: Use the "Debug Excel (C++)" configuration to start Excel with your XLL.
3.  **Attach Go Debugger**: Use the "Attach to Go Server" configuration to attach to the automatically spawned `my-project.exe` (a process picker opens so you can select it).

## Troubleshooting

**"flatc not found"**:
Run `xll-gen doctor`. It will attempt to download the correct version of the FlatBuffers compiler automatically.

**"Shared Memory Open Failed"**:
Ensure the XLL and the Go server are using the same shared memory name.

**"Server Logs"**:
Both log files always live in the **same** directory, resolved from `logging.dir`:
*   `<Project>_native.log`: C++ XLL internal log.
*   `<Project>_go.log`: Go server log (stdout/stderr of the launched process).

Where that directory is:
*   **Standalone Mode**: `logging.dir` (default `${BIN_DIR}` = the XLL/exe directory).
*   **Singlefile Mode**: `${BIN_DIR}` resolves to the extraction directory `<temp_dir>\<ProjectName>\` (e.g., `%TEMP%\<ProjectName>\`), so by default both logs sit next to the extracted server executable. Set `logging.dir` explicitly (e.g., `${XLL_DIR}` or an absolute path) to move both logs elsewhere.

## License

This project is licensed under the GNU General Public License v3.0. Note that the generated Excel SDK files are subject to their own license terms.

This project uses third-party libraries during the build process via `FetchContent`. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for details.
