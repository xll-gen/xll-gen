# VS Code Debugging Guide

`xll-gen` projects involve two processes: the **Excel process (C++ XLL)** and the **Go Server process**, which communicate via Shared Memory. To debug effectively, you must attach VS Code to both.

This guide explains how to configure `.vscode/launch.json` for both environments.

## 1. Prerequisites

Ensure you have the following VS Code extensions installed:

1.  **Go**: `golang.go` (For Go debugging)
2.  **C/C++**: `ms-vscode.cpptools` (For C++ debugging)

## 2. launch.json Configuration

Create a `.vscode/launch.json` file with the content below. You may need to adjust paths (especially for Excel) to match your system.

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "1. Debug Go Server (Launch Manual)",
            "type": "go",
            "request": "launch",
            "mode": "auto",
            "program": "${workspaceFolder}/main.go",
            "env": {
                "GOOS": "windows",
                "GOARCH": "amd64"
            },
            "description": "Use this if server.launch.enabled is false."
        },
        {
            "name": "1. Debug Go Server (Attach)",
            "type": "go",
            "request": "attach",
            "mode": "local",
            "processId": 0,
            "description": "Attach to the auto-launched server process."
        },
        {
            "name": "2. Debug XLL (Excel) - MSVC",
            "type": "cppvsdbg",
            "request": "launch",
            "program": "C:\\Program Files\\Microsoft Office\\root\\Office16\\EXCEL.EXE",
            "args": ["${workspaceFolder}/build/Debug/YOUR_PROJECT_NAME.xll"],
            "stopAtEntry": false,
            "cwd": "${workspaceFolder}",
            "environment": [],
            "console": "externalTerminal",
            "description": "Select this if using MSVC (Visual Studio Compiler)."
        },
        {
            "name": "2. Debug XLL (Excel) - MinGW/GDB",
            "type": "cppdbg",
            "request": "launch",
            "program": "C:\\Program Files\\Microsoft Office\\root\\Office16\\EXCEL.EXE",
            "args": ["${workspaceFolder}/build/YOUR_PROJECT_NAME.xll"],
            "stopAtEntry": false,
            "cwd": "${workspaceFolder}",
            "environment": [],
            "externalConsole": true,
            "MIMode": "gdb",
            "miDebuggerPath": "C:\\ProgramData\\chocolatey\\bin\\gdb.exe",
            "setupCommands": [
                {
                    "description": "Enable pretty-printing for gdb",
                    "text": "-enable-pretty-printing",
                    "ignoreFailures": true
                }
            ],
            "description": "Select this if using MinGW (GCC). Adjust the gdb path as necessary."
        }
    ]
}
```

### ⚠️ Important Notes

1.  **Excel Path**: The `program` field (`EXCEL.EXE`) varies by installation. Verify your specific path.
2.  **XLL Path**: Update `args` to point to your actual built XLL file (`YOUR_PROJECT_NAME.xll`).
    *   MSVC (CMake) Default: `build/Debug/YOUR_PROJECT_NAME.xll`
    *   MinGW Default: `build/YOUR_PROJECT_NAME.xll`
3.  **GDB Path**: For MinGW users, update `miDebuggerPath` to the location of `gdb.exe` on your system.

## 3. Debugging Workflow

Due to the architecture, **Excel (XLL) must start first** to initialize the Shared Memory region. The Go server then connects to it.

1.  **Build**: Build your project (using CMake for C++ and `go build` for Go).
2.  **Start Excel (C++)**:
    *   Select **"2. Debug XLL (Excel)..."** in the Run and Debug view.
    *   Press F5. Excel will launch with your Add-in loaded.
3.  **Start Go Server**:
    *   **Auto-Launch Mode** (Default): Select **"1. Debug Go Server (Attach)"** and pick the `YOUR_PROJECT.exe` process spawned by Excel.
    *   **Manual Mode**: If you disabled `server.launch` in `xll.yaml`, select **"1. Debug Go Server (Launch Manual)"**.
4.  **Test**:
    *   In Excel, type a formula (e.g., `=Add(1, 2)`) to trigger your code and hit breakpoints.

## 4. Troubleshooting

*   **Go Server Exits Immediately**: This usually happens if Excel is not running or the XLL failed to load (Shared Memory not found). Start Excel first.
*   **Breakpoints Ignored**: Ensure PDB (Symbol) files are present in the build directory. Verify you built in `Debug` mode.
