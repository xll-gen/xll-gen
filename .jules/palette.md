# Palette's Journal

## 2024-05-22 - Consistent CLI Output
**Learning:** This CLI tool (`xll-gen`) has a centralized UI package (`internal/ui`) and wrappers in `cmd/ui.go`, but some commands like `build` were bypassing it, leading to inconsistent user experience (plain text vs styled).
**Action:** When adding new CLI commands, always check for existing UI helpers in `cmd/` or `internal/ui` to maintain visual consistency and feedback patterns (Header -> Action -> Success/Failure).
