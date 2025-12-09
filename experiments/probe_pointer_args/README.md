# Excel Pointer Argument Probe Experiment

## Objective

This experiment explores the behavior of argument passing in Excel XLL functions, specifically focusing on how Excel passes pointers for various data types like `D%` (string), `N` (number pointer), and `E` (double pointer).

The goal is to verify:
1.  Memory addresses of passed arguments.
2.  Value of the passed arguments.
3.  Behavior when arguments reference empty cells.

## Summary of Findings

This experiment revealed several key behaviors in how Excel's C API handles pointer arguments:

1.  **No `NULL` for Empty Cells**: When passing a cell reference to a function expecting a pointer type (`D%`, `N`, `E`), an empty cell does *not* result in a `NULL` pointer. Instead, Excel passes a valid pointer to a representation of an "empty" value:
    *   For `D%` (string pointer), it's a pointer to a length-prefixed empty string (`""`).
    *   For `N` and `E` (number pointers), it's a pointer to a value of `0` (or `0.0`).

2.  **Correct Type Mapping**:
    *   **Type `N`** passes a pointer to a 32-bit signed integer (`int32_t*` or `long*` on Windows). It is *not* a `double*` as some older documentation might suggest.
    *   **Type `E`** passes a pointer to a 64-bit floating-point number (`double*`).

3.  **Implication for Optional Arguments**: Because Excel passes a valid pointer to a zero value for empty cells, **pointer types (`N`, `E`, `L`) cannot be used to detect missing or empty arguments reliably**. You cannot distinguish between an empty cell and a cell explicitly containing `0`.
    *   **Conclusion**: To handle optional arguments or detect empty cells, you must use type `Q` or `U` (`XLOPER12`) or `Any`, which allows checking for `xltypeMissing` or `xltypeNil`.

4.  **Memory Reuse**: Excel's calculation engine may reuse the same memory buffer for pointer arguments across multiple function calls. The tests showed that several calls to `ProbeIntPtr` and `ProbeDoublePtr` received the exact same pointer address, with Excel updating the value at that address before each call.

## Functions

*   `ProbeString(s)`: Takes a string (`D%`) and returns its pointer address and value.
*   `ProbeIntPtr(p)`: Takes a number pointer (`N`) and returns its pointer address and value (interpreted as an integer).
*   `ProbeDoublePtr(p)`: Takes a double pointer (`E`) and returns its pointer address and value.

## Compilation

To compile this experiment, navigate to the `experiments/probe_pointer_args` directory and use the provided CMake configuration.

1.  **Configure CMake**: `cmake -S . -B build`
2.  **Build the XLL**: `cmake --build build`

The resulting `ProbePointerArgs.xll` will be located in the `build` directory.
