/*
Package xloper provides a Go-native interface for interacting with the Microsoft
Excel C API's `XLOPER12` data structure. It serves as a crucial bridge between
Go's type-safe environment and Excel's flexible, C-based data system, enabling
the development of robust and high-performance XLL add-ins in Go.

The package is designed to handle the complexities of memory layout and data
conversion, offering a safe and idiomatic Go API over `unsafe` C-style memory
operations.

# Core Concepts

1. XLOPER Interface:

The central piece of the package is the `XLOPER` interface. All specific XLOPER
types (like `Number`, `String`, `Multi`, etc.) implement this interface, providing
a consistent way to handle different data types.

	type XLOPER interface {
		Type() XlType
		Value() any
		Pin(p *runtime.Pinner)
	}

2. Type-Specific Structs:

Each fundamental Excel data type is represented by a specific Go struct that
precisely matches the memory layout of the C `XLOPER12` union.

  - `Number`: For `float64` values (`xltypeNum`).
  - `Int32`: For `int32` values (`xltypeInt`).
  - `String`: For Pascal-style wide character strings (`xltypeStr`).
  - `Bool`: For boolean values (`xltypeBool`).
  - `Multi`: For two-dimensional arrays of other XLOPERs (`xltypeMulti`).
  - `Ref`, `Sref`, `Mref`: For worksheet cell/range references (`xltypeRef`, `xltypeSRef`).
  - `Error`: For Excel error values like `#N/A` (`xltypeErr`).
  - `Nil`: For empty or missing arguments (`xltypeNil`, `xltypeMissing`).
  - `AsyncHandle`: For asynchronous function handles (`xltypeBigData`).

3. The `Any` Type:

The `Any` struct is a generic container that has the same size and alignment as an
`XLOPER12`. It can hold any XLOPER type and is particularly useful for creating
arrays of mixed-type data (`xltypeMulti`) or for situations where the data type
is not known at compile time.

4. Memory Management with `runtime.Pinner`:

Since Go's garbage collector can move objects in memory, passing pointers from Go
to C code (like Excel) is inherently unsafe. This package solves the problem by
using `runtime.Pinner`. The `Pin` method on the `XLOPER` interface ensures that
the struct itself and any associated Go-managed memory (like a string's backing
buffer) are "pinned," preventing the GC from moving them while Excel has a
pointer to them.

5. Bidirectional Conversion:

  - From Go to XLOPER: Use the `New...` functions (e.g., `NewString`, `NewNumber`,
    `NewMulti`) to create XLOPER types from standard Go values.
  - From XLOPER to Go: When receiving an `unsafe.Pointer` from Excel, use the
    `View` function or type-specific `View...` functions (e.g., `ViewString`,
    `ViewNumber`) to safely cast the pointer to the appropriate Go `XLOPER` type.
    The `Value()` method can then be used to extract the data as a standard Go `any` type.
*/
package xloper
