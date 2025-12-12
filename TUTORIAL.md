# Getting Started with xll-gen

This tutorial will guide you through creating your first Excel Add-in using `xll-gen`. We will build a simple add-in called **FibDemo** that calculates Fibonacci numbers and reverses strings.

## Prerequisites

Before we begin, ensure you have the following installed:

1.  **Go** (v1.24+): [Download Go](https://go.dev/dl/)
2.  **C++ Compiler**:
    *   **Windows**: Visual Studio Build Tools (MSVC) or MinGW (`winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT`).
3.  **CMake** (v3.14+): [Download CMake](https://cmake.org/download/)
4.  **Task**: [Download Task](https://taskfile.dev/installation/) (Required for building).

## Step 1: Install xll-gen

Install the CLI tool using `go install`.

```bash
go install github.com/xll-gen/xll-gen@latest
```

Verify the installation:

```bash
xll-gen doctor
```

This command will check your environment and download necessary dependencies (like the FlatBuffers compiler).

## Step 2: Initialize the Project

Create a new directory for your project and initialize it.

```bash
xll-gen init fib-demo
cd fib-demo
```

This generates the following structure:
*   `xll.yaml`: The project configuration file.
*   `main.go`: The Go entry point where you'll write your code.
*   `Taskfile.yml`: A build script for automation.
*   `.vscode/`: Launch configurations for debugging.

## Step 3: Define Your Functions

Open `xll.yaml` in your text editor. This file defines the functions that will be exposed to Excel.

Replace the contents with the following:

```yaml
project:
  name: "FibDemo"
  version: "0.1.0"

gen:
  go:
    package: "generated"

build:
  singlefile: xll

logging:
  level: info
  path: FibDemo.log

server:
  workers: 0 # Use all CPUs
  timeout: "5s"
  launch:
    enabled: true

functions:
  - name: "Fibonacci"
    description: "Calculates the nth Fibonacci number"
    category: "FibDemo"
    args:
      - name: "n"
        type: "int"
        description: "The index (0-based)"
    return: "int"

  - name: "ReverseString"
    description: "Reverses a text string"
    category: "FibDemo"
    args:
      - name: "input"
        type: "string"
        description: "Text to reverse"
    return: "string"
```

## Step 4: Generate Code

Now that we've defined the interface, let `xll-gen` generate the necessary C++ and Go boilerplate.

```bash
xll-gen generate
```

This command creates a `generated/` folder. **Do not edit files in this folder manually**, as they will be overwritten next time you run `generate`.

## Step 5: Implement the Logic

Open `main.go`. You will see a `Handler` struct or similar (depending on the template). We need to implement the methods defined in our `xll.yaml`.

The `xll-gen` tool expects you to implement the interface defined in `generated/interface.go`.

Update `main.go` to match the following:

```go
package main

import (
	"context"
	"fib-demo/generated"
	"fmt"
)

// Handler implements the generated XllService interface.
type Handler struct{}

// Fibonacci calculates the nth Fibonacci number.
// Note: 'int' in YAML maps to 'int32' in Go.
func (h *Handler) Fibonacci(ctx context.Context, n int32) (int32, error) {
	if n < 0 {
		return 0, fmt.Errorf("negative input not allowed")
	}
	if n == 0 {
		return 0, nil
	}
	if n == 1 {
		return 1, nil
	}

	a, b := int32(0), int32(1)
	for i := int32(2); i <= n; i++ {
		a, b = b, a+b
	}
	return b, nil
}

// ReverseString reverses the input text.
func (h *Handler) ReverseString(ctx context.Context, input string) (string, error) {
	runes := []rune(input)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes), nil
}

func main() {
	// Start the server with our handler
	generated.Serve(&Handler{})
}
```

## Step 6: Build the Project

Use the `xll-gen build` command to build both the Go server and the C++ XLL add-in.

```bash
xll-gen build
```

This will run `task build` internally. On success, you will see a `build/` directory containing:
*   `FibDemo.xll` (The Excel Add-in with the embedded server)

## Step 7: Run in Excel

1.  Open Microsoft Excel.
2.  Go to **File > Open > Browse**.
3.  Navigate to your project's `build` directory.
4.  Select `FibDemo.xll`.

Excel might ask for security permissions; allow the add-in to run.

Once loaded:
1.  Type `=Fibonacci(10)` in a cell. You should see `55`.
2.  Type `=ReverseString("Hello")` in a cell. You should see `olleH`.

## Troubleshooting

*   **Excel crashes**: Ensure you are not returning invalid memory. Since we are using `xll-gen` (which uses shared memory), this is handled for you.
*   **"#VALUE!" Error**: This usually means the Go server is not running or crashed.
    *   Check `FibDemo.log` (as defined in `xll.yaml`) for server errors.
    *   If the server crashed, the XLL might display a message box with the error.
*   **Compilation Errors**: Run `xll-gen doctor` to ensure your C++ compiler is set up correctly.

## Next Steps

*   Explore **Asynchronous Functions** (`async: true` in `xll.yaml`) for long-running tasks.
*   Use **Ranges** (`type: any` or `type: grid`) to handle array inputs.
*   See `xll.yaml` comments for advanced configuration like `launch.command` or `events`.
