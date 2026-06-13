# rtd-once grid/numgrid return (non-blocking spilling) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `mode:"rtd-once"` functions return `grid`/`numgrid` and **spill**, while keeping the rtd-once non-blocking + memoize/TTL UX.

**Architecture:** RTD can only deliver a scalar, so the readiness signal rides RTD while the grid is delivered guest→host (Go→C++) into a new C++ `RtdOnceGridRegistry`. The RTD update re-runs the cell's wrapper, which returns the cached grid as `xltypeMulti` → Excel spills it. Reuses the proven scalar rtd-once re-run flow, `GridToXLOPER12`/`NumGridToFP12` conversion, and the memoize/TTL/liveness logic in `xll_rtd_once.h`.

**Tech Stack:** Go (xll-gen generator, pkg/rtd, generated server), C++17 header-only XLL assets (Excel C-API, FlatBuffers), FlatBuffers schema (types repo), Go `text/template` generator templates.

**Spec:** `docs/superpowers/specs/2026-06-13-rtd-once-grid-spill-design.md`

**Repos touched:** `types` (new message), `xll-gen` (config, templates, C++ assets, generator, pkg/rtd), `xll-gen-showcase` (consumer — separate follow-up).

---

## PHASE 0 — Confirmation spike (GATE; needs real Excel) — ✅ PASSED 2026-06-13

> **RESULT (2026-06-13, real Excel):** `=SpillProbeGrid(A1)` SPILLED the 3×3 after
> the RTD-update recalc → the `#GETTING_DATA/error → array on RTD-triggered re-run
> → spill` mechanism is CONFIRMED on the user's Excel build. Gate passed; framework
> build proceeds. Spike reverted from the showcase.
>
> **Tracked risk surfaced by the spike:** the user saw a transient Excel "RTD server
> connection lost" message. Logs show the Go server did NOT crash (ran throughout,
> sync calls kept returning); the `SpillReady` topic disconnected→reconnected and
> the native log shows recurring `RTD Server registered ↔ Revoked RTD Class Object`
> churn when live topics drop to zero. rtd-once (scalar, shipped v0.5.0) uses the
> SAME connect→run→disconnect lifecycle, so this is not grid-specific. **Carry as a
> risk:** consider an Excel-DNA-style "keep the RTD server alive across zero-topic
> windows" policy to avoid the teardown/re-register blip. Validate in Task 8
> (smoketest watches for no spurious disconnect) and Task 9 (review) + the final
> real-Excel E2E. Get the exact dialog text/timing from the user before Task 9.

> Everything below Phase 0 is **gated**: do not start Phase 1 until the spike in Task 0 spills in real Excel. The pattern is proven in production by Excel-DNA's `ExcelAsyncUtil.Observe` (RTD scalar key + wrapper UDF returns the cached array → spills; see spec §2A), so this is a *confirmation* of our `xlfRtd`-based variant on the target Excel build, not a true unknown. If it nonetheless shows `#SPILL!`, a single stale cell, or never recalcs, STOP and report — fall back to async spill or an explicit `=SPILL(key)` second cell.

### Task 0: Minimal `#GETTING_DATA → array` spill probe

**Question to answer:** Does a UDF that returns `#GETTING_DATA` on first calc and an `xltypeMulti` 3×3 array on a later **RTD-triggered re-run** spill cleanly?

**Approach:** The fastest reliable probe reuses the existing showcase (which already has a working RTD server and an rtd-once-free build). Add ONE temporary function to the showcase that fakes the two-phase behavior with a process-global "ready" flag flipped by a background timer, and an RTD topic that triggers the recalc. This avoids hand-rolling a bare XLL + RTD server.

**Files:**
- Modify: `xll-gen-showcase/xll.yaml` (add a temporary `SpillProbe` rtd function — plain `mode:"rtd"`, return `any`, plus a normal sync `SpillProbeGrid` returning `grid`)
- Modify: `xll-gen-showcase/main.go` (temporary probe handlers)

> NOTE: This task is a THROWAWAY spike. It is reverted in Task 0.4 regardless of outcome. We are only learning an Excel behavior, not building product.

- [ ] **Step 1: Write the probe design as a comment in main.go**

The cleanest probe that isolates the exact unknown: a SYNC grid function whose handler returns `#GETTING_DATA` (an error) on the first call and the array on the second, driven by a global counter, combined with a volatile trigger so Excel re-runs it. But sync can't push `#GETTING_DATA` as a non-error and still re-run cleanly. So use the real mechanism instead: a plain `rtd` function `SpillReady()` that flips a cell from "wait" to "go" after 2s, and a sync `SpillProbeGrid()` that returns `#N/A` (xlerrNA) until `SpillReady` has fired, then a 3×3 array. Put `=SpillProbeGrid()` next to `=SpillReady()`; when `SpillReady` ticks "go", Excel recalcs the sheet (RTD update) and `SpillProbeGrid` re-runs.

Add this comment block above the handlers so the human running the spike knows the cell layout.

- [ ] **Step 2: Add the probe functions to `xll.yaml`**

```yaml
  - name: "SpillReady"
    description: "SPIKE: rtd, returns 'wait' then 'go' after 2s (recalc trigger)."
    category: "Showcase.Spike"
    mode: "rtd"
    return: "any"
  - name: "SpillProbeGrid"
    description: "SPIKE: returns #N/A until SpillReady fired, then a 3x3 array."
    category: "Showcase.Spike"
    args:
      - {name: "ready", type: "any", description: "Pass SpillReady()'s cell."}
    return: "grid"
```

- [ ] **Step 3: Implement the probe handlers in `main.go`**

```go
var spikeReady int64 // 0 = wait, 1 = go

func (s *Service) SpillReady_RTD(ctx context.Context, topicID int32) error {
	go func() {
		_ = generated.PushRtdUpdate(topicID, "wait")
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		atomic.StoreInt64(&spikeReady, 1)
		_ = generated.PushRtdUpdate(topicID, "go") // RTD update -> recalc
	}()
	return nil
}

// SpillProbeGrid returns #N/A until SpillReady has fired, then a 3x3 array.
// The `ready` arg makes the cell depend on SpillReady's cell so Excel re-runs
// this when SpillReady ticks "go".
func (s *Service) SpillProbeGrid(ctx context.Context, ready *protocol.Any) ([][]any, error) {
	if atomic.LoadInt64(&spikeReady) == 0 {
		return nil, fmt.Errorf("#N/A getting data")
	}
	return [][]any{
		{1.0, 2.0, 3.0},
		{4.0, 5.0, 6.0},
		{7.0, 8.0, 9.0},
	}, nil
}
```

Add `"sync/atomic"` to imports if not present.

- [ ] **Step 4: Regenerate + build (run from `xll-gen-showcase/`)**

Run:
```bash
go run github.com/xll-gen/xll-gen/cmd/xll-gen generate   # or: task generate
task build
```
Expected: `build/xll_showcase.xll` + `build/xll_showcase.exe` rebuilt, no errors.

- [ ] **Step 5: HUMAN — run the spike in real Excel**

1. Open Excel (2021/365), load `build/xll_showcase.xll`.
2. In a cell `A1` enter `=SpillReady()`. It should show `wait` then `go` after ~2s.
3. In `C1` enter `=SpillProbeGrid(A1)`. It should show `#N/A` (or the error text) initially.
4. When `A1` flips to `go`, watch `C1`.

**GATE — report which happened:**
- ✅ `C1` **spills a 3×3 grid** (C1:E3 filled 1..9) → feasibility CONFIRMED, proceed to Phase 1.
- ❌ `C1` shows `#SPILL!`, stays `#N/A`, or shows only `1` in one cell → feasibility FAILED. Stop; record the exact cell result and Excel version in this task, and switch to the fallback (async spill, or `=SPILL(key)` two-cell pattern). Do NOT proceed to Phase 1.

- [ ] **Step 6: Revert the spike (always)**

```bash
cd xll-gen-showcase
git checkout xll.yaml main.go
task generate && task build   # restore the clean showcase
```
Commit nothing from the spike. Record the GATE result in the plan/spec.

---

## PHASE 1 — Framework: config + types (gated on Task 0 ✅)

### Task 1: Allow grid/numgrid return for rtd-once (config)

**Files:**
- Modify: `xll-gen/internal/config/config.go` (the rtd-once composite-return rejection, ~line 459)
- Test: `xll-gen/internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `config_test.go`:
```go
func TestValidate_RtdOnce_AllowsGridReturn(t *testing.T) {
	for _, ret := range []string{"grid", "numgrid"} {
		cfg := minimalRtdCfg() // existing helper: rtd.enabled=true, one fn
		cfg.Functions = []Function{{Name: "BDH", Mode: "rtd-once", Return: ret,
			Args: []Arg{{Name: "t", Type: "string"}}}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("rtd-once return %q must be allowed, got: %v", ret, err)
		}
	}
}

func TestValidate_RtdOnce_StillRejectsRangeReturn(t *testing.T) {
	cfg := minimalRtdCfg()
	cfg.Functions = []Function{{Name: "Bad", Mode: "rtd-once", Return: "range"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("rtd-once return \"range\" must still be rejected")
	}
}
```
If `minimalRtdCfg` does not exist, inline a `*Config` literal with `Rtd.Enabled=true, Rtd.ProgID="X.Rtd"`.

- [ ] **Step 2: Run test — expect FAIL**

Run: `cd xll-gen && go test ./internal/config/ -run TestValidate_RtdOnce -v`
Expected: `TestValidate_RtdOnce_AllowsGridReturn` FAILs ("cannot return composite type 'grid'").

- [ ] **Step 3: Relax the rejection**

In `config.go`, find the rtd-once composite-return guard (the `fmt.Errorf("function '%s': mode:\"rtd-once\" cannot return composite type ...")`). Change the condition so `grid` and `numgrid` are allowed but `range` is not. Concretely, replace the blanket composite check for rtd-once with:
```go
// rtd-once may return scalar, "any", grid, or numgrid (grid/numgrid spill via
// the RtdOnceGridRegistry path). "range" stays unsupported as a return type.
if fn.Return == "range" {
    return fmt.Errorf("function '%s': mode:\"rtd-once\" cannot return \"range\" (range is not a return type; use grid/numgrid)", fn.Name)
}
```
Keep the existing scalar/any acceptance. Do NOT touch the plain `mode:"rtd"` composite rejection (rtd streaming still cannot spill).

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/config/ -run TestValidate_RtdOnce -v` → PASS
Run: `go test ./internal/config/` → all PASS (no regression to the plain-rtd reject test).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): allow grid/numgrid return for mode:rtd-once"
```

### Task 2: types — guest→host grid-result message

**Files:**
- Modify: `types/schema.fbs` (or the protocol .fbs that defines guest→host messages — read it first)
- Regenerate the Go + C++ bindings via the types build (`task` / CMake)
- Test: round-trip in `types/go`

- [ ] **Step 1: Read the existing message/union definitions**

Read `types/*.fbs` and `types/go/protocol/*.go` to find how `SetRefCacheRequest` and `RtdUpdate` are defined (these are the closest analogues). The new message carries `{ key: string, value: Any }` where `Any` already supports `Grid`/`NumGrid`.

- [ ] **Step 2: Write the failing round-trip test**

In `types/go/protocol` (or wherever builders live), add a test that builds a `RtdOnceGridResult{key:"BDH\x1f...", value: Grid([[1,2],[3,4]])}` and reads `key` + the grid back. (Mirror the existing SetRefCacheRequest round-trip test shape.)

- [ ] **Step 3: Run — expect FAIL (type undefined)**

Run: `cd types && go test ./go/...` → FAIL (RtdOnceGridResult undefined).

- [ ] **Step 4: Add the table to the schema + regenerate**

Add to the .fbs:
```fbs
table RtdOnceGridResult {
  key:string;
  value:Any;   // Grid or NumGrid
}
```
Add it to the message-type enum/union used on the guest→host path if one exists (match how SetRefCacheRequest is registered). Regenerate: `task generate` (or the documented FlatBuffers codegen step). Verify both `go/protocol` and the C++ headers regenerated.

- [ ] **Step 5: Run — expect PASS**

Run: `go test ./go/...` → PASS.

- [ ] **Step 6: Commit + tag types**

```bash
cd types
git add -A && git commit -m "feat: RtdOnceGridResult message for rtd-once grid delivery"
# tag/push handled at release time per pre-release checklist
```

---

## PHASE 2 — C++ runtime: RtdOnceGridRegistry + wiring

### Task 3: `RtdOnceGridRegistry` (header-only, mirrors RtdOnceRegistry)

**Files:**
- Create: `xll-gen/internal/assets/files/include/xll_rtd_once_grid.h`
- (read first: `xll-gen/internal/assets/files/include/xll_rtd_once.h` — copy its structure exactly)

- [ ] **Step 1: Implement the registry mirroring the scalar one**

Create `xll_rtd_once_grid.h` with a `RtdOnceGridRegistry` singleton identical in shape to `RtdOnceRegistry` but storing serialized grid bytes:
```cpp
#pragma once
#ifdef XLL_RTD_ENABLED
#include <windows.h>
#include <string>
#include <vector>
#include <map>
#include <set>
#include <mutex>
namespace xll {

class RtdOnceGridRegistry {
public:
    static RtdOnceGridRegistry& Instance() { static RtdOnceGridRegistry inst; return inst; }

    void SetFunctionNames(const std::vector<std::wstring>& names,
                          const std::vector<std::wstring>& memoizeNames,
                          const std::vector<std::pair<std::wstring, unsigned long long>>& ttlNames) {
        std::lock_guard<std::mutex> lock(m_mutex);
        m_funcNames.clear();  for (auto& n : names) m_funcNames.insert(n);
        m_memoizeNames.clear(); for (auto& n : memoizeNames) m_memoizeNames.insert(n);
        m_ttlNames.clear(); for (auto& nt : ttlNames) m_ttlNames[nt.first] = nt.second;
    }
    bool IsOnceGridFunction(const std::wstring& fn) const {
        std::lock_guard<std::mutex> lock(m_mutex); return m_funcNames.count(fn) != 0;
    }
    void RegisterTopic(long topicID, const std::wstring& key) {
        std::lock_guard<std::mutex> lock(m_mutex); m_topicToKey[topicID] = key;
    }
    void UnregisterTopic(long topicID) {
        std::lock_guard<std::mutex> lock(m_mutex); m_topicToKey.erase(topicID);
    }
    bool KeyForTopic(long topicID, std::wstring& key) const {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_topicToKey.find(topicID); if (it == m_topicToKey.end()) return false;
        key = it->second; return true;
    }
    // Stores the serialized grid Any payload for a key.
    void Store(const std::wstring& key, const uint8_t* data, size_t len) {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto& e = m_results[key];
        e.bytes.assign(data, data + len);
        e.storedTick = GetTickCount64();
    }
    // Returns the payload bytes if present and not expired (same TTL+liveness as
    // the scalar registry).
    bool TryGet(const std::wstring& key, std::vector<uint8_t>* out) {
        std::lock_guard<std::mutex> lock(m_mutex);
        auto it = m_results.find(key); if (it == m_results.end()) return false;
        std::wstring fn = FuncNameOfKey(key);
        auto ttlIt = m_ttlNames.find(fn);
        if (ttlIt != m_ttlNames.end() && !KeyHasLiveTopic(key)) {
            if (GetTickCount64() - it->second.storedTick > ttlIt->second) {
                m_results.erase(it); return false;
            }
        }
        if (out) *out = it->second.bytes;
        return true;
    }
    void ClearNonMemoized() {
        std::lock_guard<std::mutex> lock(m_mutex);
        std::set<std::wstring> liveKeys;
        for (auto& kv : m_topicToKey) liveKeys.insert(kv.second);
        ULONGLONG now = GetTickCount64();
        for (auto it = m_results.begin(); it != m_results.end();) {
            std::wstring fn = FuncNameOfKey(it->first);
            bool live = liveKeys.count(it->first) != 0, erase = false;
            if (!live) {
                if (m_memoizeNames.count(fn)) erase = false;
                else { auto t = m_ttlNames.find(fn);
                    erase = (t != m_ttlNames.end()) ? (now - it->second.storedTick > t->second) : true; }
            }
            if (erase) it = m_results.erase(it); else ++it;
        }
    }
private:
    struct Entry { std::vector<uint8_t> bytes; ULONGLONG storedTick = 0; };
    static std::wstring FuncNameOfKey(const std::wstring& key) {
        size_t s = key.find(L'\x1f'); return s == std::wstring::npos ? key : key.substr(0, s);
    }
    bool KeyHasLiveTopic(const std::wstring& key) const {
        for (auto& kv : m_topicToKey) if (kv.second == key) return true; return false;
    }
    RtdOnceGridRegistry() = default;
    ~RtdOnceGridRegistry() = default;  // trivial: §20.2 leak-don't-crash (no oleaut32)
    RtdOnceGridRegistry(const RtdOnceGridRegistry&) = delete;
    RtdOnceGridRegistry& operator=(const RtdOnceGridRegistry&) = delete;
    mutable std::mutex m_mutex;
    std::set<std::wstring> m_funcNames, m_memoizeNames;
    std::map<std::wstring, unsigned long long> m_ttlNames;
    std::map<long, std::wstring> m_topicToKey;
    std::map<std::wstring, Entry> m_results;
};

} // namespace xll
#endif
```
`std::vector<uint8_t>` has a real destructor (unlike the scalar VARIANT), so leaking on forced unload is safe and trivial — no special teardown.

- [ ] **Step 2: Commit (compiles only as part of a generated project; no standalone test here)**

```bash
git add internal/assets/files/include/xll_rtd_once_grid.h
git commit -m "feat(assets): RtdOnceGridRegistry for rtd-once grid results"
```

### Task 4: guest→host handler stores the grid; ProcessRtdUpdate skips scalar store for grid-once

**Files:**
- Modify: `xll-gen/internal/assets/files/src/xll_rtd.cpp` (ProcessRtdUpdate / guest-call dispatch — read it first)
- Modify: `xll-gen/internal/assets/files/src/xll_events.cpp` (CalculationEnded → also `RtdOnceGridRegistry::ClearNonMemoized()`)
- Modify: the guest-call message router (find where `MSG_SETREFCACHE` is handled on the C++ host side; read first)

- [ ] **Step 1: Read the guest-call message router + ProcessRtdUpdate**

Find where incoming guest→host messages are dispatched by type (the `MSG_SETREFCACHE` handler is the pattern to copy). Find `ProcessRtdUpdate` and how it currently calls `RtdOnceRegistry::StoreResult`.

- [ ] **Step 2: Add the RtdOnceGridResult handler**

In the router, add a case for the new message: deserialize `RtdOnceGridResult{key,value}`, call `xll::RtdOnceGridRegistry::Instance().Store(key, valueBytes, len)` where `valueBytes` is the serialized `Any` (store the raw `Any` table bytes the wrapper will later feed to `GridToXLOPER12`). ACK success so the Go side knows to proceed with the readiness push.

- [ ] **Step 3: Gate the scalar store in ProcessRtdUpdate**

In `ProcessRtdUpdate`, when the topic's function is a grid-once function (`RtdOnceGridRegistry::Instance().IsOnceGridFunction(funcNameOfKey)`), do NOT call `RtdOnceRegistry::StoreResult`; only trigger the recalc (the existing `NotifyUpdate`/topic-dirty path). The grid already arrived via the message in Step 2.

- [ ] **Step 4: Clear grid registry on CalculationEnded + xlAutoClose**

In `xll_events.cpp` wherever `RtdOnceRegistry::Instance().ClearNonMemoized()` is called, add `RtdOnceGridRegistry::Instance().ClearNonMemoized()` next to it. In the xlAutoClose teardown, no explicit clear needed (trivial dtor), but keep parity if the scalar registry is cleared there.

- [ ] **Step 5: Commit**

```bash
git add internal/assets/files/src/xll_rtd.cpp internal/assets/files/src/xll_events.cpp
git commit -m "feat(assets): store rtd-once grid results; skip scalar store for grid-once"
```

---

## PHASE 3 — Generator: templates + funcmap

### Task 5: funcmap — `anyRtdOnceGrid` + grid-once name subsets

**Files:**
- Modify: `xll-gen/internal/generator/funcmap.go`
- Test: `xll-gen/internal/generator/funcmap_test.go` (or gen_rtd_once_test.go)

- [ ] **Step 1: Write failing test**

Add a test asserting `anyRtdOnceGrid(funcs)` is true when a function has `Mode=="rtd-once"` and `Return` in {grid,numgrid}, false otherwise. Mirror `anyRtdOnce`’s existing test.

- [ ] **Step 2: Run — FAIL** (`anyRtdOnceGrid` undefined). `go test ./internal/generator/ -run RtdOnceGrid`.

- [ ] **Step 3: Implement** `anyRtdOnceGrid` in funcmap.go alongside `anyRtdOnce`:
```go
func anyRtdOnceGrid(fns []config.Function) bool {
	for _, f := range fns {
		if f.Mode == "rtd-once" && (f.Return == "grid" || f.Return == "numgrid") {
			return true
		}
	}
	return false
}
```
Register it in the template FuncMap next to `anyRtdOnce`.

- [ ] **Step 4: Run — PASS.**

- [ ] **Step 5: Commit** `feat(generator): anyRtdOnceGrid template helper`.

### Task 6: C++ wrapper template — grid-once branch

**Files:**
- Modify: `xll-gen/internal/templates/xll_main.cpp.tmpl` (the `{{else if eq .Mode "rtd-once"}}` block ~line 1093; the includes ~line 37; the `SetFunctionNames` block ~line 805)
- Test: `xll-gen/internal/generator/gen_rtd_once_test.go`

- [ ] **Step 1: Write failing generator test**

Add `TestGenCpp_RtdOnceGrid` (mirror `TestGenCpp_RtdOnce_Memoize`): build a cfg with `BDH{Mode:"rtd-once", Return:"grid", Args:[{string}]}`, generate, assert the C++ output:
- includes `xll_rtd_once_grid.h`,
- the BDH wrapper calls `RtdOnceGridRegistry::Instance().TryGet(onceKey, ...)` and `GridToXLOPER12(...)` on hit,
- registers BDH in `RtdOnceGridRegistry::Instance().SetFunctionNames(...)`.

- [ ] **Step 2: Run — FAIL.** `go test ./internal/generator/ -run RtdOnceGrid -v`.

- [ ] **Step 3: Implement the template branch**

In `xll_main.cpp.tmpl`:
- Include guard near line 37: `{{if anyRtdOnceGrid .Functions}}#include "xll_rtd_once_grid.h"{{end}}`.
- Near line 811, add a parallel `RtdOnceGridRegistry::Instance().SetFunctionNames({ grid-once names }, { grid-once memoize names }, { grid-once ttl pairs });` populated with `{{if and (eq .Mode "rtd-once") (or (eq .Return "grid") (eq .Return "numgrid"))}}`.
- In the rtd-once block, split on return type. For grid/numgrid: build the same topic strings + composite-arg payloads + onceKey, then:
```cpp
{ std::vector<uint8_t> gbytes;
  if (xll::RtdOnceGridRegistry::Instance().TryGet(onceKey, &gbytes)) {
      // gbytes is a serialized protocol::Any (Grid or NumGrid)
      auto any = protocol::GetAny(gbytes.data());
      {{if eq .Return "numgrid"}}return NumGridToFP12(any->value_as_NumGrid());
      {{else}}return GridToXLOPER12(any->value_as_Grid());{{end}}
  }
}
// miss: ship composite payloads (existing code) then xlfRtd + return #GETTING_DATA (existing code)
```
Keep the scalar rtd-once path unchanged for scalar/any returns (branch on `.Return`).

- [ ] **Step 4: Run — PASS** + `go test ./internal/generator/` (no regression to scalar rtd-once tests).

- [ ] **Step 5: Commit** `feat(template): rtd-once grid/numgrid spill wrapper branch`.

### Task 7: server template + pkg/rtd — grid RunOnce

**Files:**
- Modify: `xll-gen/internal/templates/server.go.tmpl` (rtd-once connect dispatch)
- Modify/Create: `xll-gen/pkg/rtd/runonce.go` (grid variant)
- Test: `xll-gen/internal/generator/gen_rtd_once_test.go` (server.go assertions) + `xll-gen/pkg/rtd` unit test

- [ ] **Step 1: Read** `pkg/rtd/runonce.go` and the rtd-once connect block in `server.go.tmpl` to learn the existing `RunOnce` signature and how the scalar result is pushed.

- [ ] **Step 2: Write failing pkg/rtd test**

Add a unit test for a new `RunOnceGrid` helper: given a handler returning `[][]any`, it (a) serializes to a `RtdOnceGridResult`, (b) invokes the guest→host send func (injected), (c) only after the send ACKs, calls the readiness-push func. Use fakes for the send/push funcs; assert ordering (grid send before readiness push) and that an error result pushes the error string (not a grid).

- [ ] **Step 3: Run — FAIL** (`RunOnceGrid` undefined).

- [ ] **Step 4: Implement `RunOnceGrid`**

```go
// RunOnceGrid runs a grid-returning rtd-once handler once, ships the grid to the
// host, then signals readiness so Excel recalcs and the wrapper spills it.
// sendGrid must block until the host has stored the grid (ordering guarantee).
func RunOnceGrid(key string, grid [][]any,
	sendGrid func(key string, payload []byte) error,
	pushReady func(token string) error, pushErr func(msg string)) {
	payload, err := fbany.BuildGridResult(key, grid) // serialize RtdOnceGridResult
	if err != nil { pushErr(err.Error()); return }
	if err := sendGrid(key, payload); err != nil { pushErr(err.Error()); return }
	// Push a CHANGING readiness token (mirrors Excel-DNA's GUID) so Excel always
	// sees a value change on the topic and recalcs the cell. A monotonic counter
	// or nanosecond stamp is sufficient; it is never displayed (the wrapper reads
	// the grid registry, not this value).
	_ = pushReady(readyToken())
}
```
Where `readyToken()` returns a process-monotonic string (e.g. `strconv.FormatInt(atomic.AddInt64(&readySeq, 1), 10)`). Add a `numgrid` analogue (or branch on a `[][]float64` overload) using `BuildNumGridResult`. Implement `fbany.BuildGridResult/BuildNumGridResult` if not present (wraps the existing grid builders into the new `RtdOnceGridResult` table).

- [ ] **Step 5: Wire the server template**

In `server.go.tmpl` rtd-once connect dispatch: for grid/numgrid functions, run the user handler, then call `rtd.RunOnceGrid(onceKey, result, s.sendGridToHost, func(tok string) error { return rtd.GlobalRtd.SendUpdate(topicID, tok) }, func(m string){ rtd.GlobalRtd.SendUpdate(topicID, m) })`. Wire `sendGridToHost` to the guest→host send path (streams when the payload exceeds one slot — reuse the existing stream sender).

- [ ] **Step 6: Run — PASS** (pkg/rtd unit + generator server.go assertion). `go test ./pkg/rtd/ ./internal/generator/`.

- [ ] **Step 7: Commit** `feat(server,rtd): RunOnceGrid delivers grid then signals readiness`.

---

## PHASE 4 — End-to-end smoketest + reviews

### Task 8: smoketest — grid-returning rtd-once

**Files:**
- Modify: `xll-gen/internal/smoketest/spill_rtdonce_test.go` (build tag `xll_spill`)

- [ ] **Step 1:** Add a `SlowGridOnce` function to the harness yaml/main (mode rtd-once, return grid, `memoize_ttl:"5s"`) returning a fixed 2×2 after ~1s.
- [ ] **Step 2:** Assert end-to-end: cell starts `#GETTING_DATA`, then spills the 2×2; within TTL a recalc keeps the cached grid (no recompute — counter unchanged); after TTL a recalc recomputes. Mirror the existing SlowTtl assertions but over a spilled range.
- [ ] **Step 3:** Run (Excel-driving harness): `go test ./internal/smoketest/ -tags xll_spill -run RtdOnceGrid -v`. Two-tier process cleanup (graceful → force-kill) per harness convention.
- [ ] **Step 4: Commit** `test(smoketest): rtd-once grid spill end-to-end`.

### Task 9: Reviews (mandatory before release)

- [ ] **Step 1:** Run `xll-cpp-reviewer` (opus) on the C++ asset diffs (`xll_rtd_once_grid.h`, `xll_rtd.cpp`, `xll_events.cpp`, `xll_main.cpp.tmpl` output). Fix findings.
- [ ] **Step 2:** Run `memory-safety-auditor` (opus) — focus: `RtdOnceGridRegistry` byte-buffer lifetime, `GridToXLOPER12`/`NumGridToFP12` DLL-free spill allocation, no XLOPER12 double-free on the spill path.
- [ ] **Step 2b (RTD-liveness risk, from Phase 0):** Verify the connect→run→spill→disconnect cycle does not surface Excel's "RTD server connection lost" dialog. If the native log shows `Revoked RTD Class Object` churn on zero live topics, evaluate keeping the RTD COM server registered across zero-topic windows (Excel-DNA pattern) rather than revoking. Confirm against the user's reported dialog text/timing.
- [ ] **Step 3:** Run `cross-repo-coordinator` before tagging — types message-ID parity, pin bumps (types → xll-gen → showcase).

### Task 10: AGENTS.md + release

- [ ] **Step 1:** Update `xll-gen/AGENTS.md` §19.3: rtd-once now accepts grid/numgrid returns; document the RtdOnceGridRegistry + readiness-signal mechanism and the memoize/TTL parity. Update the config error text reference.
- [ ] **Step 2:** Tag types (minor bump), bump xll-gen's types pin, tag xll-gen (minor bump — new capability), per the pre-release checklist.
- [ ] **Step 3:** Real-Excel E2E confirmation by the user (the showcase BDH, built in the follow-up consumer spec).

---

## PHASE 5 — Consumer (separate follow-up spec, not this plan)
After the framework lands and is tagged: write `…specs/2026-06-13-showcase-yahoo-finance-design.md` for `BDP` (rtd-once scalar, ttl 60s) + `BDH` (rtd-once grid, ttl 60s) + the Yahoo client + `BuildShowcaseSheet` block, and its own plan.

---

## Self-Review Notes
- **Spec coverage:** config (Task 1), types message (Task 2), RtdOnceGridRegistry (Task 3), guest→host store + ProcessRtdUpdate gate + calc-end clear (Task 4), funcmap (Task 5), C++ wrapper branch (Task 6), server/pkg-rtd RunOnceGrid (Task 7), smoketest (Task 8), reviews (Task 9), docs/release (Task 10), Phase-0 spike (Task 0), consumer deferred (Phase 5). All spec sections mapped.
- **Type consistency:** `RtdOnceGridRegistry` methods (`SetFunctionNames`/`IsOnceGridFunction`/`RegisterTopic`/`Store`/`TryGet`/`ClearNonMemoized`) used identically in Tasks 3/4/6. `RunOnceGrid` signature consistent across Task 7. Message `RtdOnceGridResult{key,value}` consistent across Tasks 2/4/7.
- **Gating:** Phase 0 is a hard prerequisite; Tasks 1–10 assume it passed.
- **Reads-required flagged** where exact surrounding code wasn't captured (Tasks 2,4,7) — these say "read X first" rather than fabricating exact code.
