# xll-gen

A CLI tool designed to facilitate the creation of Excel Add-ins (XLL) using an out-of-process architecture.

## Overview
`xll-gen` allows you to write your business logic in Go (or other languages) and automatically generate the necessary C++ shim to interface with Excel via shared memory.

## License

This project is licensed under [LICENSE](LICENSE).

### Excel SDK

This project includes files from the Microsoft Excel XLL SDK (`include/xlcall.h`, `include/xlcall.cpp`).
These files are subject to Microsoft's license terms.
