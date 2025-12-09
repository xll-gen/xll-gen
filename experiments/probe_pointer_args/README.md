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
    *   For `N` and `E` (number pointers), it's a pointer to a `double` with a value of `0.0`.

2.  **`N` and `E` Types Require `double*`**: Both `N` (number pointer) and `E` (double pointer) argument types in the registration string require the C++ function signature to be `double*`. Excel passes a pointer to a 64-bit floating-point number for all numeric pointer types. Attempting to use `int*` or `long*` will lead to misinterpreting the memory and reading incorrect values.

3.  **Memory Reuse**: Excel's calculation engine may reuse the same memory buffer for pointer arguments across multiple function calls. The tests showed that several calls to `ProbeIntPtr` and `ProbeDoublePtr` received the exact same pointer address, with Excel updating the value at that address before each call.

4.  **Floating-Point to Integer Casting**: When converting the `double` value received from a number pointer to an integer type (`int` or `long`), direct C-style casting (`(long)double_val`) can be unreliable. Due to floating-point representation inaccuracies (e.g., `1.0` being stored as `0.999...`), the truncation inherent in the cast can result in an off-by-one error (e.g., yielding `0` instead of `1`). A more robust method is to `round()` the value to the nearest whole number before casting.

## Functions

*   `ProbeString(s)`: Takes a string (`D%`) and returns its pointer address and value.
*   `ProbeIntPtr(p)`: Takes a number pointer (`N`) and returns its pointer address and value (interpreted as an integer).
*   `ProbeDoublePtr(p)`: Takes a double pointer (`E`) and returns its pointer address and value.

## Compilation

To compile this experiment, navigate to the `experiments/probe_pointer_args` directory and use the provided CMake configuration.

1.  **Configure CMake**: `cmake -S . -B build`
2.  **Build the XLL**: `cmake --build build`

The resulting `ProbePointerArgs.xll` will be located in the `build` directory.
