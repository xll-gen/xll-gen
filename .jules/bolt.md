# Bolt's Journal

## 2025-05-23 - Greedy Meshing Bottleneck in CommandBatcher
**Learning:** The `CommandBatcher` was decomposing every `Range` update into individual `Cell`s to perform greedy meshing (optimizing overlapping updates). While this minimizes the number of commands sent to Excel, it has O(Cells) complexity. For large ranges (e.g., 1000x1000), this resulted in 1,000,000 map insertions and subsequent meshing, causing huge latency (~700ms) and memory pressure.
**Action:** Implemented a threshold-based bypass. Large ranges (>1024 cells) are now queued directly as `Rect`s, skipping the decomposition. This reduces processing time from ~700ms to ~0.5µs for large updates, while preserving the meshing optimization for small, sparse updates.
