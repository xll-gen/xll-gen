# Probe D String Experiment

## Objective

This experiment explores the behavior of argument passing in Excel XLL functions, specifically focusing on how Excel passes pointers for optional/nullable types like `D%` (string), `N` (int*), and `E` (double*).

The goal is to verify:
1.  Memory addresses of passed arguments.
2.  Value of the passed arguments.
3.  Behavior when arguments are missing (optional).

## Functions

*   `ProbeString(s)`: Takes a string (`D%`) and returns its pointer address and value.
*   `ProbeIntPtr(p)`: Takes an integer pointer (`N`) and returns its pointer address and value.
*   `ProbeDoublePtr(p)`: Takes a double pointer (`E`) and returns its pointer address and value.

## Observation on `D%` Argument with Empty Strings

When passing an empty string to an XLL function expecting a `D%` argument (pointer to a C-style string), Excel's C API will provide a pointer to an *empty string* (e.g., `L""` or `""`), not a `NULL` pointer.

**Implication**: Developers should check for an empty string (`s[0] == L'\0'`) rather than `s == NULL` when handling optional string arguments in XLL functions to correctly identify and process empty string inputs.

## Compilation

To compile this experiment, navigate to the `experiments/probe_d_string` directory and follow these steps:

1.  Create a build directory: `mkdir build`
2.  Configure CMake: `cmake ..` (from within the `build` directory)
3.  Build the XLL: `cmake --build .` (from within the `build` directory)

The resulting `ProbeDString.xll` will be located in the `build` directory.
