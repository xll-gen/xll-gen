## 2025-05-18 - Go FlatBuffers Builder Reuse
**Learning:** `flatbuffers.Builder` in Go loses its internal buffer capacity if `Bytes` is manually set to `nil` (detached) and then returned to a `sync.Pool`. This defeats the purpose of pooling if the intention is to reuse the underlying buffer.
**Action:** Split builder pools into two categories:
1. `shmBuilderPool`: For "Zero-Copy" operations where the builder wraps an external buffer (SHM) and must detach it before returning (setting `Bytes = nil`).
2. `heapBuilderPool`: For "Allocation" operations (e.g., outgoing messages) where the builder allocates its own buffer. These should NOT detach `Bytes` so that `Reset()` can reuse the underlying slice capacity, significantly reducing allocations.

## 2025-05-18 - Closure Allocation in Hot Paths
**Learning:** Defining closures inside hot-path handlers (like IPC loops) causes a heap allocation for the closure context on every execution, even if the closure captures no variables from the immediate scope (just outer scope).
**Action:** Hoist closure definitions out of the loop. If the closure requires recursion, declare it as a variable first (`var f func(...)`) then define it (`f = func(...) { ... }`), then pass it to the handler.
