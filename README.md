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

Since the architecture involves two processes (Excel and your Go server), debugging requires a multi-target setup.

1.  **Prerequisites**: Install `golang.go` and `ms-vscode.cpptools` extensions.
2.  **Configuration**: Create `.vscode/launch.json`:

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "1. Debug Go Server",
            "type": "go",
            "request": "launch",
            "program": "${workspaceFolder}/main.go",
            "env": { "GOOS": "windows", "GOARCH": "amd64" }
        },
        {
            "name": "2. Debug XLL (Excel)",
            "type": "cppvsdbg",
            "request": "launch",
            "program": "C:\\Program Files\\Microsoft Office\\root\\Office16\\EXCEL.EXE",
            "args": ["${workspaceFolder}/build/Debug/YOUR_PROJECT.xll"],
            "stopAtEntry": false,
            "cwd": "${workspaceFolder}",
            "console": "externalTerminal"
        }
    ]
}
```

**Note**: Adjust the `program` path to match your Excel installation and `args` to point to your built XLL.

3.  **Workflow**:
    1.  **Start Excel**: Run "2. Debug XLL (Excel)". This loads the XLL and initializes Shared Memory.
    2.  **Start Go Server**: Run "1. Debug Go Server". It connects to the running XLL.
    3.  **Test**: Enter a function in Excel (e.g., `=Add(1, 2)`).

## License

GNU General Public License v3.0
