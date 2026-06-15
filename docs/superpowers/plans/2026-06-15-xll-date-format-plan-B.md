# Date Auto-Formatting (Plan B — wrapper-driven) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Prerequisite:** Plan A (`2026-06-15-xll-date-values-plan-A.md`) must be complete — dates already serialize as `Date`/`xltypeNum`. This plan adds the *display format*.

**Goal:** A returned date cell is auto-formatted as a date (value-driven, idempotent), including only the date column(s) of a spilled grid (BDH), without clobbering a user's existing date/time format and without changing the user's handler signatures.

**Architecture:** Date-ness already travels in the payload (Plan A's `Date` member). The wrapper that materializes a value runs in the calling cell's **calc context** (sync return, rtd-once-grid spill), so it can call `xlfCaller` — but it MUST defer the format change (cell formatting is illegal during calc). The wrapper enqueues `(cell, format)` into a process-global `PendingDateFormats` queue; the `CalculationEnded` handler drains it and applies a **conditional** `xlcFormatNumber` (skip if the cell is already date/time-formatted). `xlfCaller` is callable from any XLL function, so capture is **runtime-conditional** (only when the result actually contains a date) — no codegen flag, no schema change beyond Plan A.

**Tech Stack:** C++ (types `utility`/`converters`; xll-gen assets `xll_date_format.{h,cpp}`, `xll_main.cpp.tmpl`, the `CalculationEnded` handler), gtest (types), regtest/smoketest (xll-gen).

**Spec:** `docs/superpowers/specs/2026-06-15-xll-date-return-design.md` (§3, §6, §8, §9, §11).

**Phasing:** This plan implements Phase-1 producers only — **sync** (scalar/grid) and **rtd-once-grid** (BDH). Async and streaming-rtd producers are Phase 2 (spec §10), out of scope here.

---

## File Structure

**`types` repo:**
- Modify: `include/types/utility.h` + `src/utility.cpp` — `IsDateLikeFormat(const std::wstring&)`.
- Modify: `include/types/converters.h` + `src/converters.cpp` — `CollectDateCells(const protocol::Any*, std::vector<DateCell>&)` + the `DateCell` struct.
- Test: `tests/test_converters.cpp` (CollectDateCells) and a new `tests/test_dateformat.cpp` (IsDateLikeFormat) wired into `tests/CMakeLists.txt`.

**`xll-gen` repo:**
- Create: `internal/assets/files/include/xll_date_format.h` — `PendingDateFormats` singleton + `ScheduleDateFormatsForCaller` + `DrainAndApplyDateFormats`.
- Create: `internal/assets/files/src/xll_date_format.cpp`.
- Modify: `internal/templates/xll_main.cpp.tmpl` — call `ScheduleDateFormatsForCaller` before sync returns (~1392/1567 + the scalar/any return) and in the rtd-once-grid spill (~1226–1234); call `DrainAndApplyDateFormats` from the `CalculationEnded` handler (the site that calls `RtdOnceGridRegistry::ClearNonMemoized()`).
- Modify: `internal/assets/assets.go` if it enumerates asset files explicitly (add the new `.h`/`.cpp`).
- Test: `internal/assets/assets_test.go` (compile/inclusion), `cmd/cpp_compile_gate_test.go` (gate still builds), `internal/regtest/` (end-to-end format applied).

---

## Task 1: `types` — `IsDateLikeFormat`

**Files:**
- Modify: `types/include/types/utility.h`, `types/src/utility.cpp`
- Create: `types/tests/test_dateformat.cpp` (+ register in `types/tests/CMakeLists.txt`)

- [ ] **Step 1: Write the failing gtest table**

```cpp
#include "types/utility.h"
#include <gtest/gtest.h>

TEST(IsDateLikeFormat, Classifies) {
    // date/time formats -> true (already formatted; must be skipped)
    EXPECT_TRUE(IsDateLikeFormat(L"yyyy-mm-dd"));
    EXPECT_TRUE(IsDateLikeFormat(L"m/d/yyyy"));
    EXPECT_TRUE(IsDateLikeFormat(L"h:mm:ss"));
    EXPECT_TRUE(IsDateLikeFormat(L"[$-409]m/d/yy h:mm AM/PM"));
    EXPECT_TRUE(IsDateLikeFormat(L"dd mmm yyyy"));
    // non-date -> false (General/numeric; a date here should be formatted)
    EXPECT_FALSE(IsDateLikeFormat(L"General"));
    EXPECT_FALSE(IsDateLikeFormat(L"0.00"));
    EXPECT_FALSE(IsDateLikeFormat(L"#,##0"));
    EXPECT_FALSE(IsDateLikeFormat(L"\"day\" 0")); // 'd' only inside a quoted literal
    EXPECT_FALSE(IsDateLikeFormat(L"")); // empty == General
}
```

Register in `types/tests/CMakeLists.txt` next to the existing test executables
(follow the pattern used by `test_converters.cpp`).

- [ ] **Step 2: Run to verify it fails**

Run: `cd types && task test`
Expected: FAIL — `IsDateLikeFormat` undefined (link/compile error).

- [ ] **Step 3: Implement**

In `utility.h`:

```cpp
// True if a number-format code displays its value as a date/time, i.e. it
// contains an unescaped date/time token (y/m/d/h/s) outside quoted literals
// ("...") and bracketed sections ([Red], [$-409], [h]). "General" and pure
// numeric codes return false. Used to decide whether a date cell already has a
// suitable format (skip) or needs the default applied.
bool IsDateLikeFormat(const std::wstring& fmt);
```

In `utility.cpp`:

```cpp
bool IsDateLikeFormat(const std::wstring& fmt) {
    bool inQuote = false;
    bool inBracket = false;
    for (size_t i = 0; i < fmt.size(); ++i) {
        wchar_t c = fmt[i];
        if (inQuote) { if (c == L'"') inQuote = false; continue; }
        if (inBracket) { if (c == L']') inBracket = false; continue; }
        switch (c) {
            case L'"': inQuote = true; break;
            case L'[': inBracket = true; break;
            case L'\\': ++i; break; // escaped next char is a literal
            case L'y': case L'Y':
            case L'm': case L'M':
            case L'd': case L'D':
            case L'h': case L'H':
            case L's': case L'S':
                return true;
        }
    }
    return false;
}
```

> Note: `m` is month-or-minute in Excel; for date/time *detection* either meaning
> means "date-like", so treating any unescaped `m` as date-like is correct here.

- [ ] **Step 4: Run to verify it passes**

Run: `cd types && task test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd types
git add include/types/utility.h src/utility.cpp tests/test_dateformat.cpp tests/CMakeLists.txt
git commit -m "feat(utility): IsDateLikeFormat number-format classifier"
```

---

## Task 2: `types` — `CollectDateCells`

**Files:**
- Modify: `types/include/types/converters.h`, `types/src/converters.cpp`
- Test: `types/tests/test_converters.cpp`

- [ ] **Step 1: Write the failing tests**

```cpp
TEST(CollectDateCells, ScalarDate) {
    flatbuffers::FlatBufferBuilder b;
    auto d = protocol::CreateDate(b, 46188.0, 0); // integer serial -> date-only
    auto any = protocol::CreateAny(b, protocol::AnyValue::Date, d.Union());
    b.Finish(any);
    std::vector<DateCell> out;
    CollectDateCells(protocol::GetAny(b.GetBufferPointer()), out);
    ASSERT_EQ(out.size(), 1u);
    EXPECT_EQ(out[0].rowOff, 0);
    EXPECT_EQ(out[0].colOff, 0);
    EXPECT_EQ(out[0].format, L"yyyy-mm-dd");
}

TEST(CollectDateCells, GridColumnZeroOnly) {
    // 2x2 grid: col 0 dates, col 1 nums (BDH shape)
    flatbuffers::FlatBufferBuilder b;
    std::vector<flatbuffers::Offset<protocol::Scalar>> cells;
    for (int r = 0; r < 2; ++r) {
        auto dd = protocol::CreateDate(b, 46188.0 + r, 0);
        cells.push_back(protocol::CreateScalar(b, protocol::ScalarValue::Date, dd.Union()));
        auto nn = protocol::CreateNum(b, 1.5);
        cells.push_back(protocol::CreateScalar(b, protocol::ScalarValue::Num, nn.Union()));
    }
    auto vec = b.CreateVector(cells);
    auto grid = protocol::CreateGrid(b, 2, 2, vec);
    auto any = protocol::CreateAny(b, protocol::AnyValue::Grid, grid.Union());
    b.Finish(any);
    std::vector<DateCell> out;
    CollectDateCells(protocol::GetAny(b.GetBufferPointer()), out);
    ASSERT_EQ(out.size(), 2u);
    EXPECT_EQ(out[0].colOff, 0); EXPECT_EQ(out[0].rowOff, 0);
    EXPECT_EQ(out[1].colOff, 0); EXPECT_EQ(out[1].rowOff, 1);
}

TEST(CollectDateCells, DatetimeUsesDatetimeFormat) {
    flatbuffers::FlatBufferBuilder b;
    auto d = protocol::CreateDate(b, 46188.5, 0); // fractional -> has time
    auto any = protocol::CreateAny(b, protocol::AnyValue::Date, d.Union());
    b.Finish(any);
    std::vector<DateCell> out;
    CollectDateCells(protocol::GetAny(b.GetBufferPointer()), out);
    ASSERT_EQ(out.size(), 1u);
    EXPECT_EQ(out[0].format, L"yyyy-mm-dd hh:mm:ss");
}

TEST(CollectDateCells, NumGridHasNoDates) {
    flatbuffers::FlatBufferBuilder b;
    std::vector<double> data{1.0, 2.0};
    auto vec = b.CreateVector(data);
    auto ng = protocol::CreateNumGrid(b, 1, 2, vec);
    auto any = protocol::CreateAny(b, protocol::AnyValue::NumGrid, ng.Union());
    b.Finish(any);
    std::vector<DateCell> out;
    CollectDateCells(protocol::GetAny(b.GetBufferPointer()), out);
    EXPECT_TRUE(out.empty());
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd types && task test`
Expected: FAIL — `DateCell` / `CollectDateCells` undefined.

- [ ] **Step 3: Implement**

In `converters.h`:

```cpp
#include <vector>
#include <string>

// A date cell's position RELATIVE to the anchor (caller top-left) plus the
// number-format code to apply. Produced by CollectDateCells, consumed by the
// wrapper which translates offsets to absolute cells.
struct DateCell {
    int rowOff;
    int colOff;
    std::wstring format;
};

// Walks `any` and appends one DateCell per Date value:
//   - scalar Date           -> {0,0}
//   - Grid with Date cells   -> one entry per Date element at its (row,col)
//   - anything else          -> nothing (NumGrid/numeric/string carry no dates)
// format = the Date.format field if non-empty, else auto from the serial's
// fractional part: integer serial -> L"yyyy-mm-dd", else L"yyyy-mm-dd hh:mm:ss".
void CollectDateCells(const protocol::Any* any, std::vector<DateCell>& out);
```

In `converters.cpp`:

```cpp
static std::wstring AutoDateFormat(double serial) {
    double frac = serial - std::floor(serial);
    return (frac == 0.0) ? L"yyyy-mm-dd" : L"yyyy-mm-dd hh:mm:ss";
}

static std::wstring DateFormatOf(const protocol::Date* d) {
    if (d->format() && d->format()->size() > 0) {
        return ConvertToWString(d->format()->c_str());
    }
    return AutoDateFormat(d->serial());
}

void CollectDateCells(const protocol::Any* any, std::vector<DateCell>& out) {
    if (!any) return;
    try {
        if (any->val_type() == protocol::AnyValue::Date) {
            out.push_back({0, 0, DateFormatOf(any->val_as_Date())});
            return;
        }
        if (any->val_type() == protocol::AnyValue::Grid) {
            const protocol::Grid* g = any->val_as_Grid();
            if (!g || !g->data()) return;
            int cols = g->cols();
            if (cols <= 0) return;
            const auto* data = g->data();
            for (flatbuffers::uoffset_t i = 0; i < data->size(); ++i) {
                const protocol::Scalar* s = data->Get(i);
                if (s && s->val_type() == protocol::ScalarValue::Date) {
                    out.push_back({(int)(i / cols), (int)(i % cols),
                                   DateFormatOf(s->val_as_Date())});
                }
            }
        }
    } catch (...) {
        // never throw into the wrapper; a collection failure just means no
        // auto-format (the value is already correct).
        out.clear();
    }
}
```

(`#include <cmath>` for `std::floor`; `ConvertToWString` already exists in
`utility.h`.)

- [ ] **Step 4: Run to verify they pass**

Run: `cd types && task test`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd types
git add include/types/converters.h src/converters.cpp tests/test_converters.cpp
git commit -m "feat(converters): CollectDateCells (value-driven date positions + auto format)"
```

> After Tasks 1–2, retag `types` (or extend the Plan A `replace`) so xll-gen sees
> the new symbols: `cd types && go build ./... && task generate` is not needed
> (no schema change); just ensure the local `replace`/checkout includes these
> C++ sources for the xll-gen build.

---

## Task 3: `xll-gen` — `PendingDateFormats` queue + apply logic

**Files:**
- Create: `xll-gen/internal/assets/files/include/xll_date_format.h`
- Create: `xll-gen/internal/assets/files/src/xll_date_format.cpp`

- [ ] **Step 1: Write the header (interface + queue)**

`xll_date_format.h`:

```cpp
#pragma once
#include <windows.h>
#include <vector>
#include <string>
#include <mutex>
#include "types/converters.h" // protocol::Any, DateCell

namespace xll {

// One absolute cell (single-sheet) to conditionally date-format at calc-end.
struct PendingFormat {
    IDSHEET idSheet;
    XLREF12 ref;          // single cell rect
    std::wstring format;
};

// Process-global queue. Populated on Excel CALC THREADS (the materializing
// wrapper), drained on the STA thread (CalculationEnded). Mirrors the mutex
// discipline of RtdOnceGridRegistry.
class PendingDateFormats {
public:
    static PendingDateFormats& Instance();
    void Enqueue(const std::vector<PendingFormat>& items);
    std::vector<PendingFormat> Drain();
private:
    std::mutex m_mutex;
    std::vector<PendingFormat> m_pending;
};

// Calc-context helper: if `result` contains any Date cells, capture xlfCaller
// (the anchor) and enqueue conditional format requests for each date cell. Safe
// to call from any wrapper; a no-op when there are no dates. NEVER throws.
void ScheduleDateFormatsForCaller(const protocol::Any* result);

// Calc-end helper (call from the CalculationEnded handler): drain the queue,
// and for each cell apply the format via xlcFormatNumber UNLESS the cell is
// already date/time-formatted (IsDateLikeFormat on xlfGetCell type 7).
void DrainAndApplyDateFormats();

} // namespace xll
```

- [ ] **Step 2: Write the implementation**

`xll_date_format.cpp`:

```cpp
#include "xll_date_format.h"
#include "xll_excel.h"          // xll::CallExcel
#include "types/utility.h"      // IsDateLikeFormat, PascalToWString
#include "types/ScopedXLOPER12.h"

namespace xll {

PendingDateFormats& PendingDateFormats::Instance() {
    static PendingDateFormats inst;
    return inst;
}
void PendingDateFormats::Enqueue(const std::vector<PendingFormat>& items) {
    if (items.empty()) return;
    std::lock_guard<std::mutex> lock(m_mutex);
    m_pending.insert(m_pending.end(), items.begin(), items.end());
}
std::vector<PendingFormat> PendingDateFormats::Drain() {
    std::lock_guard<std::mutex> lock(m_mutex);
    std::vector<PendingFormat> out;
    out.swap(m_pending);
    return out;
}

void ScheduleDateFormatsForCaller(const protocol::Any* result) {
    try {
        std::vector<DateCell> cells;
        CollectDateCells(result, cells);
        if (cells.empty()) return;

        // xlfCaller is callable from ANY XLL function (no macro/caller:true
        // needed). It returns the calling cell as an xltypeSRef/xltypeRef.
        ScopedXLOPER12 xCaller;
        if (xll::CallExcel(xlfCaller, xCaller) != xlretSuccess) return;

        IDSHEET idSheet = 0;
        XLREF12 anchor{};
        const DWORD t = xCaller.get()->xltype & ~(xlbitDLLFree | xlbitXLFree);
        if (t & xltypeSRef) {
            anchor = xCaller.get()->val.sref.ref;
            // idSheet 0 = active sheet; xlcSelect on an SRef-derived ref targets it.
        } else if (t & xltypeRef && xCaller.get()->val.mref.lpmref &&
                   xCaller.get()->val.mref.lpmref->count > 0) {
            idSheet = xCaller.get()->val.mref.idSheet;
            anchor = xCaller.get()->val.mref.lpmref->reftbl[0];
        } else {
            return;
        }

        std::vector<PendingFormat> items;
        items.reserve(cells.size());
        for (const auto& c : cells) {
            PendingFormat pf;
            pf.idSheet = idSheet;
            pf.ref.rwFirst = pf.ref.rwLast = anchor.rwFirst + c.rowOff;
            pf.ref.colFirst = pf.ref.colLast = anchor.colFirst + c.colOff;
            pf.format = c.format;
            items.push_back(std::move(pf));
        }
        PendingDateFormats::Instance().Enqueue(items);
    } catch (...) { /* never throw into the wrapper */ }
}

// Builds an xltypeRef XLOPER for one cell so we can xlcSelect it at calc-end.
static void MakeCellRef(const PendingFormat& pf, XLOPER12& out, XLMREF12& mref) {
    mref.count = 1;
    mref.reftbl[0] = pf.ref;
    out.xltype = xltypeRef;
    out.val.mref.idSheet = pf.idSheet;
    out.val.mref.lpmref = &mref;
}

void DrainAndApplyDateFormats() {
    auto items = PendingDateFormats::Instance().Drain();
    for (const auto& pf : items) {
        try {
            XLOPER12 ref; XLMREF12 mref;
            MakeCellRef(pf, ref, mref);

            // Conditional skip: read current number format (xlfGetCell type 7);
            // if already date/time-like, leave the user's format untouched.
            XLOPER12 xType; xType.xltype = xltypeInt; xType.val.w = 7;
            ScopedXLOPER12Result xFmt;
            if (xll::CallExcel(xlfGetCell, xFmt, &xType, &ref) == xlretSuccess &&
                xFmt->xltype == xltypeStr) {
                std::wstring cur = PascalToWString(xFmt->val.str);
                if (IsDateLikeFormat(cur)) continue;
            }

            // Apply: select the cell and set the number format. (Same idiom as
            // ExecuteCommands' FormatCommand path — legal in CalculationEnded.)
            xll::CallExcel(xlcSelect, nullptr, &ref);
            xll::CallExcel(xlcFormatNumber, nullptr, pf.format);
        } catch (...) { /* skip this cell; never break calc-end */ }
    }
}

} // namespace xll
```

> Coalescing (spec §11) is intentionally omitted in the first cut: per-cell
> `xlfGetCell`+`xlcFormatNumber` is correct and idempotent. If profiling shows a
> large BDH column is slow, add a per-column contiguous-run merge in `Drain()`
> later — behavior is unchanged, only the call count.

- [ ] **Step 3: Wire into the asset build**

If `internal/assets/assets.go` enumerates files explicitly, add
`xll_date_format.h`/`xll_date_format.cpp`. If it embeds a directory tree, no
change. Verify with `cd xll-gen && go test ./internal/assets/ -run . -v`.

- [ ] **Step 4: Build check**

Run: `cd xll-gen && go test ./cmd/ -run CompileGate -v`
Expected: the generated project (now including `xll_date_format.cpp`) compiles.
(The producers/drain are not yet called — this just confirms the new TU builds
and links against `types`.)

- [ ] **Step 5: Commit**

```bash
git add xll-gen/internal/assets/files/include/xll_date_format.h xll-gen/internal/assets/files/src/xll_date_format.cpp xll-gen/internal/assets/
git commit -m "feat(assets): PendingDateFormats queue + conditional calc-end apply"
```

---

## Task 4: producers + drain wiring in the template

**Files:**
- Modify: `xll-gen/internal/templates/xll_main.cpp.tmpl`
- Test: `xll-gen/internal/generator/gen_cpp_test.go` (golden: calls emitted)

- [ ] **Step 1: Write the failing golden assertions**

Add to a generator test (use the existing render harness):

```go
func TestGen_DateFormatProducerWired(t *testing.T) {
	out := renderXllMain(t, /* a config with one sync any-return fn and one rtd-once-grid fn */)
	// sync + spill return sites schedule formats:
	if strings.Count(out, "xll::ScheduleDateFormatsForCaller(") < 2 {
		t.Fatalf("expected ScheduleDateFormatsForCaller at sync + spill sites; got:\n%s", out)
	}
	// calc-end drains:
	if !strings.Contains(out, "xll::DrainAndApplyDateFormats()") {
		t.Fatalf("CalculationEnded handler missing DrainAndApplyDateFormats")
	}
	// header included:
	if !strings.Contains(out, "#include \"xll_date_format.h\"") {
		t.Fatalf("xll_main.cpp missing xll_date_format.h include")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd xll-gen && go test ./internal/generator/ -run TestGen_DateFormatProducerWired -v`
Expected: FAIL — none of the calls are emitted yet.

- [ ] **Step 3: Edit the template**

(a) Add the include near the other asset includes (top of `xll_main.cpp.tmpl`,
by the `xll_rtd_once_grid.h` include):

```cpp
#include "xll_date_format.h"
```

(b) **Sync returns.** Immediately before each sync materialization return of a
non-error result, schedule formats. At the scalar/any return site
(`return AnyToXLOPER12(resp->result());`) and the grid return sites
(`return GridToXLOPER12(resp->result());`, ~line 1392 and ~1567):

```cpp
xll::ScheduleDateFormatsForCaller(resp->result());
return GridToXLOPER12(resp->result());   // (or AnyToXLOPER12(...) at the scalar site)
```

(NumGrid/FP12 returns carry no dates — `CollectDateCells` is a no-op there, so it
is harmless to call or skip; prefer not adding the call on the `numgrid` branch.)

(c) **rtd-once-grid spill.** In the spill block (~line 1226–1234), after
obtaining the parsed `protocol::Any*` from the cached bytes and before the
grid/numgrid branch:

```cpp
// `gridAny` is the protocol::Any parsed from gbytes (the value the wrapper
// spills). Schedule date-column formatting against this calling cell.
xll::ScheduleDateFormatsForCaller(gridAny);
if (auto ng = gridAny->val_as_NumGrid()) { ... return NumGridToFP12(ng); }
auto gr = gridAny->val_as_Grid();
return GridToXLOPER12(gr);
```

(Match the existing variable name for the parsed `Any` in that block; if the code
currently does `val_as_Grid()`/`val_as_NumGrid()` off an inline expression,
introduce a local `const protocol::Any* gridAny = ...;` first.)

(d) **Drain at calc-end.** In the `CalculationEnded` handler — the same function
that calls `xll::RtdOnceGridRegistry::Instance().ClearNonMemoized()` — add after
the existing clears:

```cpp
xll::DrainAndApplyDateFormats();
```

If that handler is gated by `XLL_RTD_ENABLED`, place the drain OUTSIDE the gate
(sync date formatting must work in non-RTD builds too) — i.e. ensure the
`CalculationEnded` handler itself is registered for all builds. The spec notes
ribbon-enabled builds already register `CalculationEnded` unconditionally
(AGENTS.md §"SECONDARY — the calc-end callback"); confirm a plain build also
registers it, and if not, register it whenever any function can return a date
(always, cheaply — the drain is a single mutex-guarded empty-vector swap when
there is nothing pending).

- [ ] **Step 4: Run to verify it passes**

Run: `cd xll-gen && go test ./internal/generator/ -run TestGen_DateFormatProducerWired -v`
Expected: PASS.
Then: `cd xll-gen && go test ./cmd/ -run CompileGate -v` (Expected: compiles).

- [ ] **Step 5: Commit**

```bash
git add xll-gen/internal/templates/xll_main.cpp.tmpl xll-gen/internal/generator/
git commit -m "feat(codegen): wire date-format producers (sync + spill) and calc-end drain"
```

---

## Task 5: end-to-end regression (real Excel format applied)

**Files:**
- Modify: `xll-gen/internal/regtest/` (add a date-returning function + assertion) OR `internal/smoketest/`

- [ ] **Step 1: Add an e2e case**

Add a function to the regtest/smoketest fixture that returns a date scalar and a
BDH-shaped grid (`[][]any` with column 0 `time.Time`). After recalc, assert via
the host harness (`mock_host.cpp` / the smoketest Excel driver) that:
- the returned cell's value equals the expected serial, AND
- after `CalculationEnded`, the cell's number format reads as date-like
  (`xlfGetCell` type 7 contains date tokens), AND
- for the grid, only column 0 is date-formatted (column 1 stays General).

Follow the existing smoketest assertions in
`internal/smoketest/spill_rtdonce_test.go` for the spill/recalc driving pattern.

- [ ] **Step 2: Run it**

Run: `cd xll-gen && go test ./internal/smoketest/ -run Date -v`
(or the regtest target). Expected: PASS. Per the test-process-cleanup rule,
ensure the harness does graceful-exit + force-kill of any spawned Excel/host.

- [ ] **Step 3: Idempotency check**

Recalc twice in the test; assert the second `CalculationEnded` applies no format
(the cell is already date-like ⇒ `IsDateLikeFormat` skip). Assert no error and
the format is unchanged.

- [ ] **Step 4: Commit**

```bash
git add xll-gen/internal/smoketest/ xll-gen/internal/regtest/
git commit -m "test(e2e): date auto-format applied to scalar + BDH column, idempotent"
```

---

## Task 6: full verification + cross-repo review

- [ ] **Step 1: Build + test everything**

Run: `cd types && task test && go test ./...` then `cd xll-gen && go build ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 2: C++ asset review**

Per the repo rule (C++ asset changes require it), run the **xll-cpp-reviewer**
agent over `types/src/converters.cpp`, `types/src/utility.cpp`,
`xll-gen/internal/assets/files/src/xll_date_format.cpp`, and the
`xll_main.cpp.tmpl` diff — focus: XLOPER12 ownership in `DrainAndApplyDateFormats`
(stack `XLOPER12`/`XLMREF12` not freed by Excel; `ScopedXLOPER12Result` for the
`xlfGetCell` result), `xlfCaller`/`xlcFormatNumber` legality, and DllMain/unload
safety of the new singleton. Address findings before merge.

- [ ] **Step 3: Cross-repo coordination**

Run the **cross-repo-coordinator** agent (types ↔ xll-gen). If shipping with Plan
A, fold into Plan A Task 10's single `types` tag + version bump rather than two
tags.

- [ ] **Step 4: Final commit**

```bash
git add -A && git commit -m "chore: date auto-format review fixups"
```

---

## Self-Review (completed)

- **Spec coverage:** §3 wrapper-driven/calc-end legality → Tasks 3–4; §6
  CollectDateCells + Date→Num → Task 2 (+ Plan A Task 4); §8 PendingDateFormats +
  conditional `IsDateLikeFormat` skip → Tasks 1, 3, 4; §9 flows (sync + BDH) →
  Tasks 4–5; §11 anchor-skip/idempotency/coalescing → Task 3 note + Task 5.
  Async/streaming-rtd (§10 Phase 2) explicitly out of scope.
- **Refinement vs spec:** spec §3 proposed a codegen "DateAware" caller-capture
  flag; this plan uses **runtime-conditional `xlfCaller`** (capture only when the
  result contains a date), since `xlfCaller` is callable from any function. No
  schema/codegen flag — simpler, same behavior. (Update the spec's §3 note when
  merging.)
- **Placeholders:** none — template insertion points reference pinned line
  regions (~1226/1392/1567 + the `ClearNonMemoized()` site) with the actual code
  to insert; the only "match existing name" notes are for a local variable and
  the asset-enumeration style, both with concrete fallbacks.
- **Type consistency:** `DateCell{rowOff,colOff,format}` (Task 2) ↔ consumed in
  `ScheduleDateFormatsForCaller` (Task 3); `PendingFormat{idSheet,ref,format}`
  produced+drained consistently; `IsDateLikeFormat`/`CollectDateCells`/
  `ScheduleDateFormatsForCaller`/`DrainAndApplyDateFormats` names match across
  header, impl, and template.
