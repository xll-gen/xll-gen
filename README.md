# xll-gen

![cover](cover.png)

> **WARNING: EXPERIMENTAL SOFTWARE**
> This tool is currently in an experimental stage and is not recommended for use in production environments.

`xll-gen` is a command-line interface (CLI) tool designed to streamline the creation of Excel Add-ins (XLL) using an out-of-process architecture. By leveraging Shared Memory for high-performance Inter-Process Communication (IPC), it allows developers to write Excel extensions in languages like Go, bypassing the complexity and limitations of traditional C++ XLL development.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
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
  version: "0.2.0"

gen:
  go:
    package: "generated"
  disable_pid_suffix: false # Useful for testing/stable IPC names

build:
  # 'xll' embeds the Go server executable inside the XLL file (default).
  singlefile: xll
  # Directory to extract embedded binary to (default: ${TEMP})
  temp_dir: "${TEMP}"

logging:
  level: "info"
  dir: "" # Defaults to xll directory

server:
  workers: 0         # 0 = Use runtime.NumCPU()
  timeout: "10s"     # Default timeout for synchronous requests
  launch:
    enabled: true    # Automatically start the Go server when XLL loads
    # command: "${BIN}" # Optional: Defaults to the server executable
    # cwd: "${BIN_DIR}" # Optional: Defaults to the directory containing the executable

functions:
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
    async: true      # Asynchronous function
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

> **Note**: Nullable scalar types (`int?`, `float?`, `bool?`) are **not supported**. Use `any` to handle missing or nil values (checking for `xltypeMissing`).

### Custom FlatBuffers Includes

The code generator runs `flatc` with the `--no-includes` flag. This means:
1.  Code is generated **only** for the main `schema.fbs` (derived from `xll.yaml`).
2.  If you manually modify `schema.fbs` to `include` other custom `.fbs` files, their code will **not** be generated automatically.
3.  You must manually generate code for any extra included files if you need them.

This design ensures that the pre-compiled `protocol.fbs` (system types) is used efficiently without regeneration.

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
*   **Standard Mode**: Logs are located in the directory specified by `logging.path` (or `logging.dir`).
*   **Singlefile Mode**: Logs are located in the temporary directory (e.g., `%TEMP%\<ProjectName>\`).
    *   `xll_launch.log`: Launch process stdout/stderr.
    *   `<Project>_native.log`: C++ XLL internal errors.
    *   `<Project>.log`: Go server logs (if configured).

## License

This project is licensed under the GNU General Public License v3.0. Note that the generated Excel SDK files are subject to their own license terms.
