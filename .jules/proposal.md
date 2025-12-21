# Proposal for Codebase Improvements

## 1. Clean up Development Scripts (Actioned)
**Status:** Fixed
**Description:** Moved `count_lines.py` and `test_xll.ps1` from the root directory to `scripts/` to maintain a cleaner repository structure.

## 2. Refactor String Utilities
**Status:** Proposed
**Description:**
There is code duplication in environment variable expansion logic between `xll_embed.cpp` (using `std::string` and `ExpandEnvironmentStringsA`) and `xll_log.cpp` (using `std::wstring` and `ExpandEnvironmentStringsW`).
**Recommendation:** Unify these utilities into a common header (e.g., `xll_util.h`) or leverage `types/utility.h` converters to implement a single `ExpandEnvVars` function that handles Unicode correctly, avoiding potential inconsistencies.

## 3. Verify C++ Standard
**Status:** Verified
**Description:** `xll_log.cpp` and other files use `std::filesystem`, requiring C++17. Checked `internal/templates/CMakeLists.txt.tmpl` and confirmed `set(CMAKE_CXX_STANDARD 17)` is present.

## 4. Centralize `MsgUserStart` definition for Generator Templates (Actioned)
**Status:** Fixed
**Description:** The Message ID for user functions (`MSG_USER_START`) was hardcoded as `133` in `internal/templates/server.go.tmpl` and `internal/templates/xll_main.cpp.tmpl`. To improve maintainability and avoid magic numbers, I added a `MsgUserStart` helper to `internal/generator/funcmap.go` and updated both templates to use it. This aligns with the "Co-Change Clusters" directive by reducing the risk of divergence if the ID changes in the future.
