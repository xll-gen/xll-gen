# XLOPER for Go

The `xloper` package is designed to facilitate seamless interaction between the Go language and `XLOPER12`, the fundamental data structure used in the Microsoft Excel C API. It acts as a bridge between Go's type safety and Excel's flexible data type system, making XLL add-in development easier and safer.

The companion `excel` package provides higher-level APIs for interacting with Excel itself, including calling worksheet functions, managing ranges, and handling Excel contexts. Together, these packages allow you to build robust Excel XLL add-ins in Go.

## Package Overview

- **`xloper`**: Low-level XLOPER type mapping, memory management, and conversion utilities.
- **`excel`**: High-level Excel API wrappers for calling functions, managing ranges, and handling Excel-specific contexts.

## Key Features

* **Type Safety**: Maps each of Excel's `XLOPER` types to clear Go structs like `Number`, `String`, `Bool`, and `Multi`.
* **Memory Management**: Utilizes `runtime.Pinner` to prevent `XLOPER` data from being moved by the Go garbage collector, ensuring that Excel can safely access the memory.
* **Bidirectional Conversion**:
    * Easily convert Go data to Excel `XLOPER`s using `New...` functions (e.g., `NewString`, `NewNumber`).
    * Safely view `XLOPER` pointers received from Excel as Go types using `View...` functions (e.g., `ViewString`, `ViewNumber`).
* **`Any` Type**: A universal container that can hold any `XLOPER` type. The `Value()` method makes it easy to extract the internal data as a Go `any` type.
* **`Multi` Type Support**: Supports converting 2D slices (`[][]any`) to Excel's `xltypeMulti` and vice versa, allowing for efficient handling of large amounts of data.
* **Excel Integration**: The `excel` package lets you call worksheet functions, manage ranges, and interact with Excel contexts from Go.

## Core Interface: `XLOPER`

All `XLOPER` types implement the following interface:

```go
type XLOPER interface {
    // Type returns the type of the XLOPER (e.g., xloper.TypeNum, xloper.TypeStr).
    Type() XlType

    // Value converts the content of the XLOPER to a standard Go type (e.g., float64, string, [][]any) and returns it.
    Value() any

    // Pin pins the Go memory associated with the XLOPER (e.g., a string buffer)
    // so that C code (Excel) can access it safely.
    Pin(p runtime.Pinner)
}
```

## Basic Usage

### Converting Go Data to XLOPER

You can convert Go values to XLOPERs using the `New` function or type-specific `New...` functions.

```go
import "github.com/xll-gen/xll-gen/xloper"

// Create a number (float64) XLOPER
numOper := xloper.NewNumber(123.45)

// Create a string XLOPER
strOper, err := xloper.NewString("Hello, Excel!")

// Create a 2D array XLOPER
multiData := [][]any{
    {1, "A"},
    {2, "B"},
}
multiOper, err := xloper.NewMulti(multiData)

// The New function automatically handles various types
anyOper, err := xloper.New(true) // Returns *xloper.Bool
```


### Converting XLOPER to Go Data

When you receive an XLOPER from Excel in the form of an `unsafe.Pointer`, you can use the `View` function to safely convert it to a Go type and extract its value.

```go
import (
    "fmt"
    "unsafe"
    "github.com/xll-gen/xll-gen/xloper"
)

func ProcessOper(ptr unsafe.Pointer) {
    // Convert the pointer to an XLOPER interface
    oper := xloper.View(ptr)

    // Branch based on the type
    switch v := oper.(type) {
    case *xloper.Number:
        fmt.Printf("Received a number: %f\n", v.Float64())
    case *xloper.String:
        fmt.Printf("Received a string: %s\n", v.String())
    case *xloper.Multi:
        // The Value() method converts it to a [][]any type.
        data := v.Value().([][]any)
        fmt.Printf("Received a multi-array: %v\n", data)
    case *xloper.NilType:
        fmt.Println("Received a nil/empty value.")
    default:
        fmt.Printf("Received an unhandled type: %T\n", v)
    }
}
```

## Supported XLOPER Types

| Excel Type      | Go Struct             | Description                                      |
| --------------- | --------------------- | ------------------------------------------------ |
| `xltypeNum`     | `xloper.Number`       | Stores a float64 value.                          |
| `xltypeStr`     | `xloper.String`       | Stores a Go string.                              |
| `xltypeBool`    | `xloper.Bool`         | Stores a bool value.                             |
| `xltypeInt`     | `xloper.Int32`        | Stores an int32 value.                           |
| `xltypeMulti`   | `xloper.Multi`        | Stores a 2D array of XLOPERs.                    |
| `xltypeErr`     | `xloper.Error`        | Stores an Excel error code.                      |
| `xltypeNil`     | `xloper.NilType`      | Represents an empty cell (Nil).                  |
| `xltypeMissing` | `xloper.NilType`      | Represents a missing argument.                   |
| `xltypeBigData` | `xloper.AsyncHandle`  | Handles async function handles or large binary data. |
| (All types)     | `xloper.Any`          | A universal struct that holds any XLOPER type.   |

## Excel API Integration

The `excel` package provides:

- **Function Calls**: Call Excel worksheet functions from Go.
- **Range Management**: Work with Excel ranges and cells.
- **Context Handling**: Manage Excel contexts for advanced add-in scenarios.

See the `excel` package documentation for more details.

## License

Unless otherwise noted in individual files, this project is licensed under the GNU Affero General Public License v3.0 (AGPLv3).  
Some files may be licensed under other terms (e.g., BSD); please refer to the header of each source file for specific licensing information.  
See the LICENSE file for full details.