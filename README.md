# xll-gen

![cover](cover.png)

> **WARNING: EXPERIMENTAL SOFTWARE**
> This tool is currently in an experimental stage and is not recommended for use in production environments.

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

```yaml
project:
  name: "my-project"
  version: "1.0.0"          # YOUR add-in version, independent of xll-gen

gen:
  go:
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
  dir: "" # Defaults to working directory

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

The `server.launch` section supports the following variables in `command` and `cwd`:

*   `${BIN}`: Resolves to the full path of the server executable.
*   `${BIN_DIR}`: Resolves to the directory containing the server executable. In `singlefile` mode, this is the temporary extraction directory.
*   `${XLL_DIR}`: Resolves to the directory containing the `.xll` file.

### Supported Types

| Type | Description | Go Type | Excel Type |
| :--- | :--- | :--- | :--- |
| `int` | 32-bit Integer | `int32` | `int` |
| `float` | 64-bit Float | `float64` | `double` |
| `bool` | Boolean | `bool` | `boolean` |
| `string` | Unicode String | `string` | `string` |
| `any` | Any Value (Scalar/Array) | `*types.Any` | `CheckRange/Variant` |
| `range` | Reference to a range | `*types.Range` | `Reference` |
| `grid` | Generic 2D Array | `*types.Grid` | `Array` |
| `numgrid` | Numeric 2D Array | `*types.NumGrid` | `FP Array` |

**Optional Function Flags**:
*   `caller: true`: Passes an additional `caller *types.Range` argument to the handler, representing the cell(s) calling the function.

> **Note**: Nullable scalar types (`int?`, `float?`, `bool?`, `string?`) are **not supported**. Use `any` to handle missing or nil values (checking for `xltypeMissing`).

### Choosing an Execution Mode (sync vs async vs rtd)

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
| Multiple seconds, interactive feel matters | `rtd` | Sheet keeps updating while the work runs. |

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
*   **Arguments** are serialized into RTD topic strings — unsuitable for
    `grid`/`range`-sized inputs.

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

### `init <name>`
Scaffolds a new project structure.
*   `-f, --force`: Overwrite existing directory.

### `generate`
Generates C++ and Go source code based on `xll.yaml`.

### `build`
Wraps `task build` to compile the project. Requires `task` to be installed.

### `doctor`
Checks the environment for required tools (C++ compiler, `flatc`).

## Debugging

`xll-gen` supports debugging both the C++ shim and the Go server using VS Code.

**Setup**:
1.  Install the **Go** and **C/C++** extensions for VS Code.
2.  Use the generated `.vscode/launch.json` configuration.

**Steps**:
1.  **Build** the project.
2.  **Launch Excel**: Use the "Debug XLL" configuration to start Excel with your XLL.
3.  **Attach Go Debugger**: Use the "Attach to Go Server" configuration to attach to the automatically spawned `my-project.exe`.

## Troubleshooting

**"flatc not found"**:
Run `xll-gen doctor`. It will attempt to download the correct version of the FlatBuffers compiler automatically.

**"Shared Memory Open Failed"**:
Ensure the XLL and the Go server are using the same shared memory name.

**"Server Logs"**:
*   **Standard Mode**: Logs are located in the directory specified by `logging.dir`.
*   **Singlefile Mode**: Logs are located in the temporary directory (e.g., `%TEMP%\<ProjectName>\`).
    *   `{ProjectName}_go.log`: Launch process stdout/stderr.
    *   `<Project>_native.log`: C++ XLL internal errors.
    *   `<Project>.log`: Go server logs (if configured).

## License

This project is licensed under the GNU General Public License v3.0. Note that the generated Excel SDK files are subject to their own license terms.

This project uses third-party libraries during the build process via `FetchContent`. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for details.
