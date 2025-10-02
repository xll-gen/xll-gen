/*
Package excel provides the core engine for building Microsoft Excel XLL add-ins
in Go. It handles the low-level interactions with the Excel C API, manages the
add-in lifecycle, and offers high-level abstractions to simplify development.

This package works in tandem with the `xloper` package, which provides the Go
representation of Excel's `XLOPER12` data structures. While `xloper` handles the
data, `excel` handles the logic and communication.

# Core Components

  - `Excel`: This is the central struct that manages the connection to the Excel
    instance. It holds the callback procedure pointer required for all C API calls
    and manages the registration of User-Defined Functions (UDFs) and event handlers.
    Typically, a single `DefaultExcel` instance is created and used throughout the
    add-in's lifetime.

  - `Context`: Provides a request-scoped environment for a single function call or
    calculation cycle. It is crucial for performance and stability, offering:
  - Caching: A simple key-value cache (`Cacher`) to store results of expensive
    operations within a single calculation.
  - Request Coalescing: Uses `singleflight` to prevent redundant API calls for
    the same data (e.g., fetching a sheet ID multiple times) during concurrent
    or rapid calculations.
  - Cancellation: Integrates with Go's `context.Context` to handle cancellation
    when a calculation cycle is aborted by Excel.

  - `Range`: A high-level representation of an Excel range (e.g., "Sheet1!A1:C5").
    It simplifies working with cell references and converting them to and from the
    `xloper.Ref` types used by the C API.

# Typical Workflow

 1. **Initialization**: The `AutoOpen` function is called by Excel when the add-in
    is loaded. It creates a `DefaultExcel` instance, establishing the connection.

 2. **Function Registration**: UDFs and macros defined in your Go code are registered
    with Excel using `RegisterFunction`. This tells Excel about the function's name,
    arguments, and other properties.

 3. **Function Invocation**: When a user calls a UDF from a worksheet, Excel
    invokes the corresponding exported Go function. This function will typically
    use a `Context` to interact with Excel, for example, to get the calling cell's
    address (`Context.Caller()`) or to retrieve data from other cells (`Context.Coerce()`).

 4. **Cleanup**: When the add-in is unloaded, Excel calls `AutoClose`, which gracefully
    unregisters all functions and event handlers and cleans up resources.
*/
package excel
