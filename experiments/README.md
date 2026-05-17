# experiments/

Research artifacts and one-off proof-of-concepts that informed `xll-gen`'s
design. **Not part of the supported build surface** — these are kept in-tree
for the historical record and for anyone tracing why a particular decision
was made.

| Subdirectory | What it explored | Status |
|---|---|---|
| `go-embed/` | Embedding a Zstd-compressed Go executable inside a C++ host (singlefile XLL path). Findings rolled into `internal/templates/CMakeLists.txt.tmpl` (Build.Singlefile == "xll"). | Reference only |
| `probe_pointer_args/` | How Excel passes pointer arguments (`D%`, `N`, `E`) for empty cells / type mismatches. Findings rolled into `internal/templates/xll_main.cpp.tmpl` argument handling. | Reference only |

**Build/run policy**: each subdir carries its own README. Some have their
own CMakeLists or `go.mod`. CI does NOT build or test these — break them
and nothing in the main pipeline notices. If you depend on something here,
copy what you need into the main tree.
