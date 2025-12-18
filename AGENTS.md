
## 16. Directory Structure & Asset Generation

Understanding how source files in the repository map to the generated project structure is crucial for correctly handling `#include` paths in C++.

### 16.1 Source Layout (`internal/assets/files`)

The embedded C++ assets are organized in the `xll-gen` repository as follows:

```text
internal/assets/files/
├── xll_worker.cpp          # Source files
├── xll_log.cpp
├── ...
├── include/                # Header files
│   ├── xll_worker.h
│   ├── xll_log.h
│   ├── ...
└── tools/
    └── compressor.cpp
```

### 16.2 Generated Layout (`generated/cpp`)

When `xll-gen generate` runs, it copies these assets to the user's project, **preserving the subdirectory structure** relative to `files/`. The target directory for assets is `generated/cpp/include/` (to keep the root clean), while `xll_main.cpp` (generated from template) goes to `generated/cpp/`.

```text
my-project/generated/cpp/
├── xll_main.cpp            # From xll_main.cpp.tmpl
├── CMakeLists.txt
├── include/                # Asset Root
│   ├── xll_worker.cpp      # Copied from files/xll_worker.cpp
│   ├── xll_log.cpp
│   ├── ...
│   └── include/            # Copied from files/include/
│       ├── xll_worker.h
│       ├── xll_log.h
│       └── ...
```

### 16.3 Include Paths & CMake

The generated `CMakeLists.txt` configures include directories to allow convenient access:

```cmake
target_include_directories(${PROJECT_NAME} PRIVATE
  ${CMAKE_CURRENT_SOURCE_DIR}          # generated/cpp
  ${CMAKE_CURRENT_SOURCE_DIR}/include  # generated/cpp/include
)
```

**Include Resolution Rules:**

1.  **From `xll_main.cpp` (in `generated/cpp/`):**
    *   To include `xll_worker.h` (located in `generated/cpp/include/include/xll_worker.h`):
    *   Use `#include "include/xll_worker.h"`
    *   *Resolution:* `${CMAKE_CURRENT_SOURCE_DIR}/include` (which is `generated/cpp/include`) + `/` + `include/xll_worker.h`.

2.  **From Asset Source (e.g., `xll_worker.cpp` in `generated/cpp/include/`):**
    *   To include `xll_worker.h` (located in `generated/cpp/include/include/xll_worker.h`):
    *   Use `#include "include/xll_worker.h"`
    *   *Resolution:* Relative path from `xll_worker.cpp` works: `./include/xll_worker.h`.

**Best Practice:**
*   Always structure headers in `internal/assets/files/include/`.
*   Always use `#include "include/Header.h"` in both templates (`xll_main.cpp.tmpl`) and asset source files (`xll_worker.cpp`).
*   This ensures consistent resolution regardless of where the file is located, thanks to the CMake configuration and relative path resolution.
