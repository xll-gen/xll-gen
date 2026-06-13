# rtd-once grid/numgrid return (non-blocking spilling functions)

> Date: 2026-06-13 · Repo: xll-gen (+ types co-change) · Status: design (awaiting review)
> Driver: a Bloomberg-style Yahoo Finance demo wants `BDH(ticker, days)` to fetch
> off the calc thread (rtd-once UX) **and** spill a historical table. Today
> rtd-once returns are restricted to scalar/`any`; this spec lifts that to
> grid/numgrid via the only mechanism Excel allows.

## 1. Problem

`mode:"rtd-once"` gives a network/slow handler the ideal Excel UX: the cell shows
`#GETTING_DATA`, the handler runs **once off the calc thread**, and the cell
settles when the result arrives — without holding the calculation transaction
open (unlike `async`, §19.3). But rtd-once returns are limited to scalar/`any`
(`internal/config/config.go` rejects composite returns): the result currently
rides the **RTD push path**, and an RTD topic can deliver only a single scalar
value (Microsoft KB 286258 — "Excel RTD function cannot return an array").

We want a function that is simultaneously: **(a) non-blocking** (rtd-once, not
async), **(b) memoizable** (`memoize` / `memoize_ttl`), and **(c) spilling** a
grid/numgrid.

## 2. The Excel constraint and the chosen architecture

RTD physically cannot deliver an array. The standard pattern (Excel-DNA #446) is
the **RTD-key + dynamic-array wrapper**: split the *readiness signal* (RTD,
scalar — allowed) from *the array* (returned through the normal calc path —
spills). xll-gen already has every piece needed:

- The **re-run-on-RTD-update** behavior is already proven by scalar rtd-once: on
  an RTD update Excel marks the cell dirty and **re-enters the wrapper**, which
  then hits the cached result (`xll_main.cpp.tmpl` rtd-once block, step 2). We
  reuse this verbatim.
- The **sync grid→XLOPER12 spill** path already exists:
  `GridToXLOPER12(resp->result())` / `NumGridToFP12(resp->result())`.
- A **guest→host (Go→C++) payload channel** exists (the RTD push / guest-call
  path), including **streaming reassembly** for large payloads
  (`shm` StreamReassembler, bounded by `MaxConcurrentStreams`).

### Data flow (miss → fetch → spill)

```
calc#1 (calc thread, C++ wrapper for BDH):
  onceKey = MakeRtdOnceKey(["BDH", ticker, days])
  RtdOnceGridRegistry.TryGet(onceKey)  -> MISS
    -> ship composite-arg payloads (if any) once/cycle
    -> xlfRtd(progId,"", onceKey-strings)   // subscribe + trigger
    -> return #GETTING_DATA                 // calc transaction closes; NON-BLOCKING

ConnectData(topicID, strings)  [main STA thread]:
  RtdOnceRegistry.RegisterTopic(topicID, onceKey)   // reuse existing map
  return #GETTING_DATA placeholder

Go rtd-once dispatch (off calc thread, in the connect goroutine):
  result = user.BDH(ctx, ticker, days)   // network fetch, [][]any
  serialize result -> Grid/NumGrid Any
  send MSG_RTDONCE_GRID{key=onceKey, payload=Any} guest->host  // stream if large
      (synchronous guest call: wait for ACK that C++ stored it)
  then RTD-push a readiness token on the topic                 // wake Excel

C++ host guest-call handler (IPC thread):
  RtdOnceGridRegistry.Store(onceKey, payload bytes)            // memoize/TTL stamp

ProcessRtdUpdate(topicID -> grid-once topic) [IPC thread]:
  // grid-once topics: DO NOT StoreResult a scalar; just NotifyUpdate(cell)
  -> Excel recalcs the cell

calc#2 (re-enter wrapper):
  RtdOnceGridRegistry.TryGet(onceKey)  -> HIT
    -> GridToXLOPER12 / NumGridToFP12  -> return xltypeMulti  -> SPILLS
    -> (no xlfRtd) -> DisconnectData -> UnregisterTopic
```

Ordering guarantee: the grid is delivered and stored **before** the readiness
push, because the grid message is a synchronous guest call (Go waits for the ACK)
and the readiness push is issued only after. So when the recalc re-enters the
wrapper, the grid is already in the registry.

## 2A. Prior art (Excel-DNA, xlOil) — this is the battle-tested pattern

Our architecture is not novel; it is the established way both mature XLL frameworks
do non-blocking spilling functions:

- **Excel-DNA** (`ExcelAsyncUtil.Observe` / RxExcel). Govert Vandevoorde (author):
  *"An RTD server topic cannot return an array, only a simple value."* The fix —
  RTD delivers a scalar **key**; a wrapper UDF calls RTD, gets the key, looks the
  array up in an internal dictionary, and returns it, which **spills via dynamic
  arrays.** *"This is exactly how the `ExcelAsyncUtil.Observe` feature in
  Excel-DNA is implemented."* Requires dynamic-array Excel; on pre-DA Excel the
  array cannot spill. (GitHub issue [#446], Google Groups "Dynamic arrays and RTD
  again…".) Our wrapper is structurally identical — it calls `xlfRtd` internally
  and returns either `#GETTING_DATA` or the cached array. The one xll-gen-specific
  addition is **cross-process delivery**: the array is computed in the Go server,
  so it ships guest→host into the C++ registry, whereas Excel-DNA's dictionary is
  in-process.

- **xlOil**. Every `async def` is an RTD function; xlOil *"stores and compares all
  the function arguments to figure out if Excel wants the result of a previous
  calculation or to start a new calculation"* and lets you name the RTD topic
  manually for performance. That argument-identity-as-topic model is exactly our
  **content-hash once-key**, and the per-args memoization is our `memoize_ttl`.

**Refinement learned from Excel-DNA's GUID approach:** the readiness value pushed
on the topic should **change** between computes (Excel recalcs a cell when its RTD
topic *value changes*). For the one-shot case a single `#GETTING_DATA → token`
transition already changes the value, so a constant token works; but pushing a
**monotonic/changing token** (e.g. the `storedTick`, mirroring Excel-DNA's GUID)
is free insurance that the recalc always fires. The plan uses a changing token.

## 3. Components

### 3.1 config (`internal/config/config.go`)
- Allow `return: grid|numgrid` when `mode:"rtd-once"` (relax the rejection at the
  current rtd-once composite-return guard; keep scalar/`any` working). `range`
  stays rejected (consistent with §6 "range is not a return type").
- `memoize` / `memoize_ttl` remain valid and now apply to grid results too.
- A new internal flag/derivation so the generator and C++ know which rtd-once
  functions are grid-returning ("grid-once" subset).

### 3.2 types (co-change repo)
- A guest→host message carrying `{ key: string, payload: Any /* Grid|NumGrid */ }`.
  Reuse existing `Grid`/`NumGrid` flatbuffer tables. Either a new message type
  (`MSG_RTDONCE_GRID`) or a new request table; chosen in the plan. Bump the types
  pin in xll-gen after tagging.

### 3.3 pkg/rtd (Go) — `runonce.go`
- A grid variant of `RunOnce`: run the user handler → serialize `[][]any`
  (`fbany.BuildGrid`) or `[][]float64` (`BuildNumGrid`) → send the grid message
  guest→host (stream when over one slot) → on ACK, push the readiness token.
- Idempotency: connect handlers may run twice under a slow cold start (§ at-least
  once). Keying by onceKey + "store wins last" makes re-delivery harmless.

### 3.4 C++ assets — `RtdOnceGridRegistry` (`internal/assets/files/include/xll_rtd_once.h` or a sibling)
- Mirror `RtdOnceRegistry` exactly, but `Entry` stores **serialized grid bytes**
  (the `Any` payload) + `storedTick`, instead of a `VARIANT`.
- `Store(key, bytes)`, `TryGet(key, out_bytes)` with the **same memoize_ttl
  read-time expiry + liveness guard**, `ClearNonMemoized()` with the same
  once/memoize_ttl/memoize triad and live-topic guard. Mirror `FuncNameOfKey`,
  `KeyHasLiveTopic`, and its **own** `m_topicToKey` map. A topic is either
  scalar-once or grid-once; `ConnectData` routes the `RegisterTopic` call to the
  right registry by the grid-once function-name subset, and `ClearNonMemoized` /
  `xlAutoClose` fan out to both.
- Trivial dtor (§20.2 "leak, don't crash") — byte buffers leak at teardown, no
  oleaut32 calls.
- On read (`TryGet` hit) the wrapper hands the bytes to the existing
  `GridToXLOPER12`/`NumGridToFP12` (which already allocate DLL-freed XLOPERs).

### 3.5 templates
- `xll_main.cpp.tmpl`: a `rtd-once` + grid/numgrid branch. Same key build +
  composite-arg payload shipping as scalar rtd-once, but the cache check is
  `RtdOnceGridRegistry.TryGet` → on hit `GridToXLOPER12`/`NumGridToFP12` (spill);
  on miss `xlfRtd` + `#GETTING_DATA`. `SetFunctionNames` gains the grid-once subset.
- `server.go.tmpl`: rtd-once connect dispatch routes grid-returning functions to
  the grid `RunOnce`.
- `ProcessRtdUpdate` (C++ `xll_rtd.cpp`): for grid-once topics, skip `StoreResult`
  and only `NotifyUpdate` (trigger recalc). The grid arrives via the guest-call
  handler, not the RTD value.

### 3.6 generator funcmap
- `anyRtdOnce` already exists; add `anyRtdOnceGrid` (or reuse type checks) to gate
  the new includes/branches.

## 4. memoize / TTL semantics (unchanged model)
Identical to scalar rtd-once, evaluated on the grid registry:
- **once (default):** grid cleared on the first `CalculationEnded` after the topic
  disconnects → next user recalc refetches.
- **memoize_ttl:`<dur>`:** within the TTL a recalc hits the cached grid (no
  refetch); after expiry the next calc misses → refetch. Read-time + calc-end
  expiry, live-topic guarded.
- **memoize:true:** grid retained until `xlAutoClose`.

Content-addressed memoization for composite args is free (the content-hash token
flows into the once-key, exactly as for scalar rtd-once).

## 5. Phase 0 — confirmation spike (DO FIRST)
The whole feature hinges on one Excel behavior:

> Does a UDF that returns `#GETTING_DATA` on first calc and an `xltypeMulti`
> (array) on a later **RTD-triggered re-run** actually **spill** cleanly — no
> stale single-cell artifact, no `#SPILL!`?

Per §2A this is **proven in production by Excel-DNA's `ExcelAsyncUtil.Observe`**,
so the spike is a *confirmation* of our specific `xlfRtd`-based wrapper variant on
the target Excel build (2021/365), not a true unknown. We still run it before
investing in the framework. The spike: a temporary showcase function that returns
`#N/A` until an RTD topic ticks, then a fixed 3×3 array (see plan Task 0).
**Gate:** spills 3×3 → proceed. `#SPILL!` / stale single cell / no recalc → STOP
and fall back (async spill, or the explicit two-cell `=SPILL(key)` handle
pattern). Only this needs real Excel; ~1 hour, de-risks the whole feature.

## 6. Consumer: the showcase finance functions (separate follow-up spec)
Once the framework lands:
- `BDP(ticker, field)` — rtd-once **scalar/any**, `memoize_ttl:"60s"` (works today;
  not blocked on this feature).
- `BDH(ticker, days)` — rtd-once **grid**, `memoize_ttl:"60s"` — spills
  `Date|Open|High|Low|Close|Volume` from Yahoo `v8/finance/chart`.
- Yahoo client (`yahoo.go`, stdlib only), `BuildShowcaseSheet` "Live Market Data"
  block, regenerate + rebuild. Pins: adopt the new xll-gen/types after tagging.

## 7. Error handling
- Handler error / bad ticker / network timeout (ctx ~8s): the grid message carries
  an error sentinel → C++ stores nothing; the readiness push delivers the error
  string as a scalar so the cell shows it (not stuck at `#GETTING_DATA`). This
  reuses the existing rtd dispatch error path (`SendUpdate(topicID, err)`).
- Registry MISS after a server restart mid-cycle: wrapper re-issues `xlfRtd`
  (recompute), same as scalar rtd-once.

## 8. Testing
- **config:** grid/numgrid accepted for rtd-once; `range` still rejected; memoize
  flags still valid. (`internal/config/config_test.go`)
- **generator:** grid-once emits the grid registry include, the TryGet/spill
  branch, and the grid-once subset in `SetFunctionNames`.
  (`internal/generator/gen_rtd_once_test.go`)
- **C++ unit (if a harness fits):** `RtdOnceGridRegistry` store/get/TTL/liveness
  parity with the scalar registry.
- **smoketest:** extend `internal/smoketest/spill_rtdonce_test.go` (build tag
  `xll_spill`) — a grid-returning rtd-once function: `#GETTING_DATA` → spilled
  grid; memoize_ttl reuse vs refetch; once full-rebuild refetch.
- **real Excel E2E:** the Phase-0 spike, then the showcase `BDH` end to end.
- Reviews: `xll-cpp-reviewer` (opus) + `memory-safety-auditor` (opus) on the C++
  asset changes (registry byte-buffer ownership, XLOPER12 spill allocation).

## 9. Scope / risks
- **Make-or-break:** the Phase-0 spill behavior (§5). Mitigated by front-loading it.
- **Co-change:** types message + xll-gen generator/assets + (consume) showcase —
  one logical change across pins; tag types → bump xll-gen pin → tag xll-gen →
  bump showcase pin (cross-repo-coordinator before release).
- **Large grids:** rely on the streaming reassembly path (recently bounded by
  `MaxConcurrentStreams`); the demo grid is small (single slot).
- **at-least-once:** connect handler may run twice → grid recompute + re-store is
  idempotent on the key.

## 10. Out of scope
- `range` returns (intentionally unsupported, §6).
- Streaming/continuous RTD grids (this is one-shot only).
- Async-mode changes.
