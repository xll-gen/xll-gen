# xll-gen

`xll-gen` is a CLI tool designed to facilitate the creation of Excel Add-ins (XLL) using an out-of-process architecture. It enables developers to write Excel extensions in Go (and potentially other languages) while bypassing the limitations of traditional DLLs by communicating via Shared Memory.

## Overview

Traditional Excel XLLs are DLLs that run inside the Excel process. This poses challenges for languages like Go, which have heavy runtimes or difficulty being compiled as shared libraries loaded by foreign hosts.

`xll-gen` solves this by:
1.  Generating a lightweight C++ XLL shim that runs in Excel.
2.  Running your logic in a separate "User Server" process (e.g., a Go binary).
3.  Using high-performance **Shared Memory** and **Flatbuffers** for Inter-Process Communication (IPC).

## Features

*   **Language Agnostic**: Logic runs out-of-process.
*   **Wails-like Experience**: Simple CLI commands (`init`, `build`, etc.) to manage the project.
*   **High Performance**: Low-latency IPC via Shared Memory.
*   **Automatic Glue Code**: Generates the C++ XLL and Flatbuffers schemas automatically.
*   **Environment Management**: Automatically manages dependencies like `flatc` (Flatbuffers compiler).

## Prerequisites

*   **Go**: 1.24 or later.
*   **C++ Compiler**:
    *   **Windows**: MSVC (`cl.exe`) or MinGW (`g++`/`gcc`).
    *   *Tip*: Install MinGW via winget: `winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT`
*   **Excel**: 2007 or later (for loading the XLL).

## Installation

```bash
git clone <repository-url>
cd xll-gen
go install
```

## Usage

### 1. Environment Check (`doctor`)

Run the `doctor` command to verify your environment. It checks for a C++ compiler and downloads `flatc` if missing.

```bash
xll-gen doctor
```

### 2. Initialize a Project (`init`)

Create a new project with a sample configuration and boilerplate.

```bash
xll-gen init my-project
cd my-project
```

This creates:
*   `xll.yaml`: Project configuration.
*   `main.go`: Your Go application entry point.

### 3. Generate Code (`generate`)

Parse `xll.yaml` and generate the necessary C++ and Go code.

```bash
xll-gen generate
```

### 4. Build (`build`)

Builds both the Go server and the C++ XLL.

```bash
xll-gen build
```

## Configuration (`xll.yaml`)

The `xll.yaml` file is the source of truth for your add-in.

```yaml
project:
  name: "my-quant-lib"
  version: "0.1.0"

gen:
  go:
    package: "generated"

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

  - name: "GetPrice"
    description: "Fetches price for a ticker"
    args:
      - name: "ticker"
        type: "string"
    return: "float"
    async: true
```

## Architecture

1.  **Excel Process**: Loads the generated XLL (C++).
2.  **Shared Memory**: Used for data transport.
3.  **User Process**: Your Go application, which implements the logic defined in `main.go`.

## Debugging in VS Code

Since the architecture involves two processes (Excel and your Go server), debugging requires a multi-target setup. You can debug both simultaneously or individually.

### Prerequisites
*   **Go Extension**: `golang.go`
*   **C++ Extension**: `ms-vscode.cpptools`

### Configuration (`.vscode/launch.json`)

Create or update `.vscode/launch.json` with the following configurations.

**Note**: You must adjust the `program` path to match your local Excel installation (e.g., `C:\\Program Files\\...\\EXCEL.EXE`).

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "Debug Go Server",
            "type": "go",
            "request": "launch",
            "mode": "auto",
            "program": "${workspaceFolder}/main.go",
            "env": { "GOOS": "windows", "GOARCH": "amd64" }
        },
        {
            "name": "Debug XLL (MSVC)",
            "type": "cppvsdbg",
            "request": "launch",
            "program": "C:\\Program Files\\Microsoft Office\\root\\Office16\\EXCEL.EXE",
            "args": ["${workspaceFolder}/build/YOUR_PROJECT_NAME.xll"],
            "cwd": "${workspaceFolder}",
            "console": "externalTerminal"
        },
        {
            "name": "Debug XLL (MinGW/GDB)",
            "type": "cppdbg",
            "request": "launch",
            "program": "C:\\Program Files\\Microsoft Office\\root\\Office16\\EXCEL.EXE",
            "args": ["${workspaceFolder}/build/YOUR_PROJECT_NAME.xll"],
            "cwd": "${workspaceFolder}",
            "MIMode": "gdb",
            "miDebuggerPath": "gdb.exe",
            "setupCommands": [
                {
                    "description": "Enable pretty-printing for gdb",
                    "text": "-enable-pretty-printing",
                    "ignoreFailures": true
                }
            ]
        }
    ]
}
```

### Debugging Steps

1.  **Build the project**: Run `task build` to ensure the XLL is up to date.
2.  **Start Excel (C++ Debugger)**:
    *   Select **Debug XLL (MSVC)** or **Debug XLL (MinGW/GDB)** depending on your compiler.
    *   Press `F5`. Excel will launch and load your XLL.
3.  **Start Go Server**:
    *   Select **Debug Go Server**.
    *   Press `F5`. The server will start and connect to the shared memory host (Excel).
4.  **Verify**: Type a function in Excel (e.g., `=Add(1, 2)`). You can now set breakpoints in both `main.go` and your C++ code.

## License

GNU General Public License v3.0
