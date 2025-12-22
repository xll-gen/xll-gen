# Proposals for xll-gen

## 1. Validate `Project.Name`
**Problem:** The `Project.Name` is used to generate filenames (e.g., log files, executable names) and is not currently validated. Characters like spaces, slashes, or special symbols can cause runtime errors in file creation or path resolution (e.g., in `xll_log.cpp` or `xll_launch.cpp`).
**Status:** **Implemented** in `internal/config/config.go`. The validation now restricts names to alphanumeric characters, underscores (`_`), and hyphens (`-`).

## 2. Clarify `ServerConfig.Command` Documentation
**Problem:** The `xll_launch.cpp` logic for resolving `server.launch.command` automatically quotes the command path if it doesn't detect quotes. This breaks if the user provides arguments (e.g., `${BIN} --flag`) without explicit quoting, as the entire string gets quoted.
**Status:** **Implemented**. The documentation in `internal/config/config.go` has been updated to explicitly state that users must quote the executable path (e.g., `"${BIN}"`) if they intend to provide arguments.

## 3. Verify `std::filesystem` Compatibility
**Observation:** `xll_log.cpp` uses `std::filesystem::u8path` which is deprecated in C++20.
**Status:** The project currently enforces C++17 in `CMakeLists.txt.tmpl`, so this is fine for now. No immediate action needed.
