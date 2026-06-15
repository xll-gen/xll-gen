# Date values in xll-gen (input + output) with wrapper-driven auto-formatting

> Date: 2026-06-15 · Repo: xll-gen (+ types co-change) · Status: design (awaiting review)
> Driver: handlers want to **return dates** and have cells display as dates
> (not raw serials or RFC3339 text), and **accept dates** as `time.Time`
> parameters. The motivating case is a Bloomberg `BDH`-style historical grid
> where **only the date column** must be date-formatted, fetched via
> `rtd-once-grid`.

## 1. Problem

Excel has **no date type**. A date is a `double` (serial, days since the
1899-12-30 epoch) plus a *cell number format*. An `XLOPER12` carries only the
value, never a format channel — so a returned date is just a number unless the
cell's format makes it look like a date. Excel auto-formats cells only for its
**built-in** date functions (`TODAY`, `DATE`, …); that special-casing is not
exposed to XLL UDFs.

Today in xll-gen:

- **Output**: `fbany.MapGo` serializes `time.Time` → **RFC3339 string**
  (`fbany.go:152`). The cell shows `2026-06-15T00:00:00Z` as text — not a date,
  not numeric, useless for arithmetic. This is the worst of all outcomes.
- **Input**: there is no `date` argument type; a date cell arrives as a bare
  `float64` serial with no convenience conversion to `time.Time`.

Excel-DNA and xlOil both resolve this the same way: the **value** becomes a
serial number automatically, and the **display format** is applied *outside* the
calculation cycle (Excel-DNA `ExcelAsyncUtil.QueueAsMacro` + `Range.NumberFormat`;
xlOil documents "format the cells as dates after the function returns"). There is
no return-value shortcut for formatting in any XLL framework — deferral is the
only correct path. This spec brings the same model to xll-gen, made automatic.

## 2. Decisions (from brainstorming)

| # | Decision | Choice |
|---|----------|--------|
| 1 | What triggers `time.Time` → serial (output)? | **Implicit by Go type** — always, all paths. |
| 2 | How are date cells identified in a grid? | **Value-driven** — a cell whose value is a date formats as a date; BDH date column "just works". |
| 3 | Auto-format opt-in? | **Always on**, but **skip when no change is needed**. |
| 4 | "No change needed" rule | **Skip if the cell already has a date/time format**; apply default only to `General`/numeric. |
| 5 | Default format / time component | **Value-driven**: integer serial → `"yyyy-mm-dd"`; has fractional time → `"yyyy-mm-dd hh:mm:ss"`. Respect an existing date/time format (decision 4). |
| 6 | Timezone | **Wall-clock as-is** — use the `time.Time`'s own location components; no TZ conversion (Excel has no TZ). |
| 7 | Where do date-ness and the caller cell meet? | **Wrapper-driven**: date-ness travels in the payload; the materializing wrapper (calc context) captures `xlfCaller` and enqueues a deferred format; a C++-local queue is drained at `CalculationEnded`. |
| 8 | Input direction | **Declared arg type `date`** (interpretation A): arg arrives as a serial `double`, FB→Go converts `serial → time.Time`. No C++/protocol change. |

Format-string auto-detection from a cell's display format (input interpretation B)
is **out of scope** — it requires reference args + `macro:true` + `xlfGetCell`
and only works for `U`-typed args. Noted as a possible future niche.

## 3. Why wrapper-driven (the key architectural fact)

The premise "RTD has no caller cell" is **false for the wrapper**. Only the
*handler* (which produces the value) runs off the calc thread. The *wrapper* that
materializes the value into the cell runs in the **calling cell's calc context**
in every mode:

- **sync**: the generated worksheet function calls the server, gets an `Any`,
  calls `AnyToXLOPER12`, returns — all in calc context.
- **rtd-once-grid**: on the readiness recalc Excel **re-enters the wrapper**
  (`xll_rtd_once_grid.h` flow), which `TryGet`s the payload and calls
  `GridToXLOPER12` to spill — in calc context. `xll_rtd_once_grid.h` states:
  *"the wrapper runs on Excel calc threads; … CalculationEnded/xlAutoClose run on
  the main STA thread."*

So `xlfCaller` is valid at materialization in every mode. What is **not** legal
in calc context is changing a cell's format (`xlcFormatNumber` is command-only).
That must be deferred — and a deferral point already exists: the
**`CalculationEnded` round-trip** that flushes batched Set/Format commands
(`CalculationEndedResponse` carries `commands: [CommandWrapper]`;
`xll_events.cpp:HandleCalculationEnded`). `ExecuteCommands` already calls
`xlcFormatNumber` and `xlfGetCell` in that context, proving both are legal there.

The remaining gap: **date-ness lives in the server** (it holds the original
`time.Time`), **the caller cell lives in the wrapper** (calc context). Decision 7
resolves this by carrying date-ness **in the payload** so the wrapper — which has
the caller — can schedule the format itself, with no server round-trip and no
server-side cross-call state.

## 4. Data model — `types/protocol.fbs` (co-change)

Add a `Date` member to the **`ScalarValue`** and **`AnyValue`** unions:

```fbs
table Date {
  serial: double;   // Excel serial computed by Go (wall-clock, 1900-bug aware)
  format: string;   // optional override; empty = auto (decision 5)
}

union ScalarValue { Nil, Num, Int, Bool, Str, Err, Date }   // + Date
union AnyValue    { ..., Date }                              // + Date
```

`FormatCommand` is **unchanged** — formatting is wrapper-driven, not routed
through the Go command batcher.

Direction: `Date` is produced **only** FB→Excel (Go→cell). The Excel→FB
converters (`ConvertScalar`/`ConvertAny`) **never emit `Date`** — a date cell
read from the sheet is `xltypeNum`, so there is nothing to classify (input dates
are handled by the declared arg type, §7). `ToScalar` (host reading guest
scalars, e.g. `ScheduleSet`) treats `Date` as its `serial` `Num` for the value.

Per `types/CLAUDE.md`: `protocol.fbs` is the source of truth; the C++ header
(`include/types/protocol_generated.h`) and Go files (`go/protocol/`) are
regenerated **in the same commit**.

## 5. Value conversion — `internal/fbany` (output, always on)

`MapGo` and the grid/scalar builders map `time.Time` → `Date{serial}` (replacing
the RFC3339 string at `fbany.go:152`). Conversion is **wall-clock**:

```
serial = daysFrom18991230(t.Year, t.Month, t.Day) + frac(t.Hour, t.Min, t.Sec, t.Nsec)
```

- Epoch: serial `0.0` = 1899-12-30 00:00:00.
- **1900 leap-year bug**: Excel treats 1900 as a leap year (serial 60 = the
  non-existent 1900-02-29). Dates ≥ 1900-03-01 are `(t - 1899-12-30).Days`;
  the helper must reproduce Excel's off-by-one below that boundary (documented +
  table-tested).
- `frac` is the time-of-day as a fraction of 86400s. Integer serial ⇒ midnight
  ⇒ date-only; non-zero fraction ⇒ datetime (drives the auto format, §6).
- No timezone conversion: `t.Date()`/`t.Clock()` are read in `t`'s own location.

This single change fixes the **value** on **all** paths (sync, async, grid,
rtd-once-grid, RTD) because they all funnel through `fbany`. `[][]float64`
(numgrid) cannot carry dates and is untouched.

## 6. Value materialization + date collection — `types` C++ `converters.cpp`

`AnyToXLOPER12` and `GridToXLOPER12` gain a `Date` case that produces
`xltypeNum` with `val.num = serial` (the displayed value is a plain number).

New helper:

```cpp
struct DateCell { int rowOff; int colOff; std::wstring format; };
void CollectDateCells(const protocol::Any* any, std::vector<DateCell>& out);
```

Walks the `Any` and records the **relative** position of every `Date` cell:

- scalar `Date` → `{0, 0, fmt}`;
- `Grid` → one `DateCell` per `Date` element at its `(row, col)` offset (BDH:
  only column 0 is `Date` ⇒ only column-0 offsets emitted);
- `format` = the `Date.format` field if non-empty, else auto from the serial's
  fractional part (decision 5).

`CollectDateCells` leaves `AnyToXLOPER12`/`GridToXLOPER12` otherwise unchanged
(they just gain the `Date`→`xltypeNum` case); the wrapper calls it separately so
the value path and the format path stay decoupled.

## 7. Input — declared `date` arg type (interpretation A, Phase 1)

Add to `internal/generator/types.go` `typeRegistry`:

```go
"date": {
    SchemaType: "double",      // arg rides the existing Num path — NO .fbs change
    GoType:     "time.Time",
    CppType:    "LPXLOPER12",
    ArgCppType: "double",
    XllType:    "Q",
    ArgXllType: "B",           // Excel coerces the arg to a double (serial)
    RetGoType:  "time.Time",   // `return: date` is optional sugar; output is implicit by Go type
},
```

`server.go.tmpl`'s `handle{Name}` arg-decode (the `{{range .Args}}` block at
`server.go.tmpl:316`) gains a `date` case:

```go
arg_{{.Name}} := server.SerialToTime(request.{{.Name|capitalize}}())   // float64 serial → time.Time
```

`server.SerialToTime` is the inverse of §5 (wall-clock, same epoch/leap handling).
**No C++ change, no protocol change, no macro/reference arg** — the value is
already a serial double; the *declared type* drives the conversion, exactly as
Excel-DNA/xlOil do. `date`/`datetime`/`time` are accepted as aliases that all map
to `time.Time` (input conversion is identical regardless). A nullable `date?`
mirroring `float?` is a future addition, not Phase 1.

## 8. Format scheduling — wrapper-driven, C++ (`PendingDateFormats`)

New process-global, mutex-guarded queue (mirrors `RtdOnceGridRegistry`'s
threading discipline — populated on calc threads, drained on the STA thread):

```cpp
struct PendingFormat { XLREF12 cell; std::wstring format; };  // absolute, single-sheet
class PendingDateFormats {  // Instance() singleton
  void Enqueue(const std::vector<PendingFormat>&);   // calc threads
  std::vector<PendingFormat> Drain();                // STA / CalculationEnded
};
```

**Producers** (both in calc context). `xlfCaller` is callable from **any** XLL
function — so caller capture is **runtime-conditional** (invoked only when the
result actually contains a date), needing no `caller:true`/`macro:true`
registration and no codegen flag:

- **sync wrapper** (generated, `xll_main.cpp.tmpl`): after receiving the response
  `Any`, call `CollectDateCells`; if non-empty, `xlfCaller` → anchor `XLREF12`;
  translate each `DateCell` offset to an absolute cell; `Enqueue`. Then
  `AnyToXLOPER12` as today.
- **rtd-once-grid spill wrapper**: after `TryGet(key)` yields the payload `Any`,
  same `CollectDateCells` + `xlfCaller` + `Enqueue`, then `GridToXLOPER12`.

**Consumer** — `HandleCalculationEnded` (`xll_events.cpp`), after the existing
work: `Drain()`, coalesce cells into rectangles with a **C++-side per-column
merge** (the `GreedyMesh` in `pkg/algo` is Go-only and not reachable here; a
contiguous-run-per-column merge is sufficient for the BDH column shape), and for
each rect apply a **conditional** format:

```
read current format via xlfGetCell(type 7, cell)
if IsDateLikeFormat(current):  skip            // decisions 3 & 4 — never clobber
else:                          xlcSelect(rect); xlcFormatNumber(format)
```

`IsDateLikeFormat(fmt)` returns true when the format code contains an unescaped
date/time token (`y`/`m`/`d`/`h`/`s` outside quoted literals and `[...]`
sections like `[Red]`/`[$-409]`). `General` and pure-numeric codes (`0.00`,
`#,##0`) return false ⇒ a date there gets formatted.

This makes the apply **idempotent**: once a column is date-formatted, later
recalcs (BDH re-run, RTD update) read it as date-like and skip.

## 9. End-to-end flows

**sync scalar date** — `=Today2()` returning `time.Time`:
```
wrapper(calc): server resp Any{Date} -> CollectDateCells=[{0,0,"yyyy-mm-dd"}]
               -> xlfCaller=B2 -> Enqueue({B2,"yyyy-mm-dd"})
               -> AnyToXLOPER12 -> xltypeNum(serial) returned (cell shows 46187)
CalculationEnded: Drain -> B2 is General -> xlcFormatNumber("yyyy-mm-dd")
                  cell now shows 2026-06-15
```

**BDH grid (rtd-once-grid)** — `=BDH("AAPL",30)`, column 0 = dates:
```
calc#1: miss -> xlfRtd subscribe -> #GETTING_DATA   (handler fetches off-thread)
handler: [][]any with col0 time.Time -> Grid Any (Date cells in col0) -> Store -> readiness push
readiness recalc (wrapper, calc): TryGet -> payload Any
        CollectDateCells -> col0 offsets [{0,0},{1,0},...,{29,0}] fmt "yyyy-mm-dd"
        xlfCaller=A2 -> Enqueue absolute A2:A31
        GridToXLOPER12 -> spill (col0 shows serials briefly)
CalculationEnded: Drain -> merge A2:A31 -> General -> xlcFormatNumber -> date column
```

**input date** — `=AddDays(A1, 7)` where A1 is a date, `A1: type date`:
```
A1 holds serial 46187.0 -> Excel passes double -> request.A1()=46187.0
handle: arg_A1 = server.SerialToTime(46187.0) = time.Date(2026,6,15,...)  // wall-clock
handler returns time.Time -> (output path as above)
```

## 10. Scope / phasing

**Phase 1** (this spec — covers the real use cases):
- §4 `Date` union member (+ regen) · §5 `time.Time`→serial (all paths) ·
  §6 materialization + `CollectDateCells` · §7 `date` **input** arg type ·
  §8 `PendingDateFormats` + `CalculationEnded` drain + `IsDateLikeFormat` ·
  producers for **sync** (scalar/grid) and **rtd-once-grid**.

**Phase 2** (secondary, deferred):
- **async**: capture `xlfCaller` at first invocation (calc context), stash with
  the async handle, enqueue on delivery (value arrives off calc thread).
- **streaming `rtd` scalar dates**: wrapper runs every tick in calc context;
  enqueue + idempotent skip dedups. (Rare — most streaming RTD is numeric.)
- **`date?`** nullable input; input interpretation B (format auto-detection for
  `U`/macro reference args).

## 11. Known limits

- **Coalesced-rect skip uses the anchor cell.** Spill cells are uniform, so BDH
  re-runs are exact; a column with *heterogeneous pre-existing* formats (some
  date, some General) is approximated (whole rect decided by its top cell).
- **One-cycle visual lag**: a date may show as a serial for the recalc frame
  before `CalculationEnded` formats it. Idempotent thereafter.
- **Input coercion edges** (`ArgXllType "B"`): an empty arg coerces to `0`
  (1899-12-30); a non-numeric string may `#VALUE`. Same class as `float` args
  today; documented.
- **Spill resize** leaving stale formats on vacated cells is pre-existing
  behavior, out of scope.

## 12. Testing

- **Go** (`fbany`, `server`): serial round-trip (epoch, 1900-leap serial 60/61,
  fractional time, TZ independence of wall-clock); `time.Time`→`Date` for
  scalar/grid/mixed; numgrid excluded; `SerialToTime` inverse for input.
- **C++** (`types`): `IsDateLikeFormat` classification table (`General`,
  `0.00`, `m/d/yyyy`, `h:mm:ss`, `[$-409]m/d/yy`, quoted-literal traps);
  `CollectDateCells` for scalar / grid / BDH column-0; `Date`→`xltypeNum` value;
  `PendingDateFormats` enqueue/drain + conditional skip idempotency.
- **Codegen golden**: a `time.Time`-returning sync function emits the
  `CollectDateCells`+`xlfCaller`+`Enqueue` block; rtd-once-grid spill wrapper
  likewise; a `date` arg emits `SerialToTime` decode; numeric/string returns are
  unchanged.
- **Regression**: existing `caller:true`, Set/Format commands, scalar &
  grid rtd-once, and ribbon `CalculationEnded` registration all still pass.

## 13. Cross-repo (co-change cluster)

`types/protocol.fbs` (`Date` union member) → regen `types`
`protocol_generated.h` + `go/protocol/*` (same commit) → `xll-gen` consumes in
`fbany`, `converters.cpp`, `xll_main.cpp.tmpl`, `xll_events.cpp`,
`server.go.tmpl`, `types.go`. `shm` is **ABI-unchanged** (the message envelope
and stream paths are identical; only a union member is added). Reviews:
**xll-cpp-reviewer** (XLOPER12 ownership of the new `Date`→`xltypeNum` path,
`xlcFormatNumber`/`xlfGetCell` calc-end legality) and **shm-protocol-guardian**
(confirm no byte-level ABI drift beyond the additive union member).
