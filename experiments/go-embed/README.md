# Go Embed Experiment

This experiment verifies the mechanism for embedding a Zstd-compressed Go executable into a C++ host application, supporting both Windows and Linux environments.

## Goal
To demonstrate and validate a robust build pipeline using CMake that:
1.  Builds a Go binary (`guest`).
2.  Compresses it using a custom C++ tool linked against Zstd.
3.  Embeds the compressed binary into a C++ Host executable.
4.  Decompresses and executes the Go binary at runtime.

## Success Factors (Key Learnings)

The following technical details were crucial for making this work, particularly for the Zstd integration via CMake:

### 1. Zstd CMake Integration
The standard `FetchContent_MakeAvailable(zstd)` fails for Zstd v1.5.5 because the `CMakeLists.txt` is located in `build/cmake`, not the root of the repository.

**Solution:**
Manually populate the content and add the specific subdirectory:
```cmake
FetchContent_GetProperties(zstd)
if(NOT zstd_POPULATED)
  FetchContent_Populate(zstd)
  add_subdirectory(${zstd_SOURCE_DIR}/build/cmake ${zstd_BINARY_DIR})
endif()
```

### 2. Correct Zstd Target Name
When building Zstd as a static library, the exposed CMake target is **`libzstd_static`**, not `zstd` or `zstd_static`.
```cmake
target_link_libraries(target PRIVATE libzstd_static)
```

### 3. Cross-Platform Embedding Strategy
*   **Windows:** Uses Resource Scripts (`.rc`) to embed the binary as `RCDATA`.
*   **Linux:** Uses a helper script (`bin2c.go`) to convert the binary into a C header file (`embedded_data.h`) containing a byte array, which is then compiled into the host.

### 4. C++ Host Requirements
The host loader uses `std::vector` and `std::string` for buffer management and path handling. Therefore, the source file must be **`main.cpp`** (C++) and not `main.c`, and the CMake project must enable `CXX`.

## Build & Run

```bash
mkdir build
cd build
cmake ..
cmake --build .
./host  # (or host.exe on Windows)
```
