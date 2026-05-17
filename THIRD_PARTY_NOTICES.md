# Third Party Notices

This project uses the following open source software during build time via CMake `FetchContent` (C++ side) and `go.mod` (Go side).

## C++ — CMake FetchContent

### Google FlatBuffers
*   **Description**: Memory Efficient Serialization Library
*   **License**: Apache-2.0
*   **Upstream**: https://github.com/google/flatbuffers

### xll-gen/shm
*   **Description**: Shared Memory IPC Library
*   **License**: GPL-3.0
*   **Upstream**: https://github.com/xll-gen/shm

### xll-gen/types
*   **Description**: FlatBuffers protocol schema + C++ XLOPER12 converters shared between xll-gen, shm, and sugar.
*   **License**: GPL-3.0
*   **Upstream**: https://github.com/xll-gen/types

### Zstandard (Zstd)
*   **Description**: Fast real-time compression algorithm (used for `singlefile: xll` server embedding).
*   **License**: BSD-3-Clause / GPL-2.0
*   **Upstream**: https://github.com/facebook/zstd

### Parallel Hashmap (phmap)
*   **Description**: Header-only concurrent hash maps used by the runtime cache layer.
*   **License**: Apache-2.0 (copy at `LICENSES/PHASHMAP_LICENSE`)
*   **Upstream**: https://github.com/greg7mdp/parallel-hashmap

## Go — go.mod direct dependencies

| Module | Purpose | License |
|---|---|---|
| `github.com/google/flatbuffers` | Go FlatBuffers runtime | Apache-2.0 |
| `github.com/google/uuid` | RTD CLSID derivation | BSD-3-Clause |
| `github.com/spf13/cobra` | CLI framework | Apache-2.0 |
| `github.com/go-ole/go-ole` | Windows COM for `internal/smoketest` | MIT |
| `github.com/xll-gen/shm` | Go bindings for the SHM IPC library | GPL-3.0 |
| `github.com/xll-gen/sugar` | xlwings-parity Excel COM helpers | GPL-3.0 |
| `github.com/xll-gen/types` | Generated FlatBuffers Go types | GPL-3.0 |
| `gopkg.in/yaml.v3` | `xll.yaml` parser | Apache-2.0 / MIT |

Indirect dependencies (transitively pulled) are not enumerated here — see `go.sum` for the full set with checksums.
