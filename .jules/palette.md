# Palette's Journal

## 2024-05-22 - Consistent CLI Output
**Learning:** This CLI tool (`xll-gen`) has a centralized UI package (`internal/ui`) and wrappers in `cmd/ui.go`, but some commands like `build` were bypassing it, leading to inconsistent user experience (plain text vs styled).
**Action:** When adding new CLI commands, always check for existing UI helpers in `cmd/` or `internal/ui` to maintain visual consistency and feedback patterns (Header -> Action -> Success/Failure).

## 2024-05-23 - Spinner Output Masking
**Learning:** CLI spinners inherently hide the stdout/stderr of the running process. If a command fails, the user loses critical context unless the output is captured and replayed.
**Action:** When adding spinners to existing commands, always capture `CombinedOutput()` and include it in the error message or print it explicitly on failure.

## 2024-10-24 - Actionable CLI Errors
**Learning:** Users often get stuck on missing dependency errors (like `task` or `cmake`). Providing a specific, copy-pasteable installation command (e.g., `go install ...` or `winget install ...`) significantly improves the "time to fix" compared to a generic "Not Found" message or a URL.
**Action:** When detecting missing tools, conditionally check for package managers (Go, Winget, Brew) and provide the exact command to run.

## 2024-10-25 - Safe Interactivity
**Learning:** Interactive prompts (like "Do you want to install X?") must be robust against non-interactive environments (CI/CD).
**Action:** Always handle `io.EOF` or read errors when using `bufio.ReadString`. In such cases, default to a safe, non-destructive action (e.g., "No") to prevent infinite loops or unwanted modifications.
