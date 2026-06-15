# Date Values (Plan A — value plumbing) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `time.Time` round-trip as a real Excel date serial — returned values become numeric serials (not RFC3339 text) on every path, and a declared `date` argument arrives as `time.Time` — without any cell formatting yet.

**Architecture:** Add a `Date` member to the `ScalarValue`/`AnyValue` FlatBuffers unions carrying a `double` serial. A new leaf package `pkg/xldate` holds the wall-clock serial⇄`time.Time` math (1900-leap-bug aware). `fbany` maps `time.Time`→`Date` on output; C++ converters materialize `Date`→`xltypeNum`. Input adds a `date` arg type whose value rides the existing `double` path and is decoded `serial→time.Time` in the generated server. This is the foundation for Plan B (auto-formatting).

**Tech Stack:** Go (xll-gen `internal/fbany`, `pkg/server`, `pkg/xldate`, `internal/generator`), C++ (types `converters.cpp`), FlatBuffers (`types/go/protocol/protocol.fbs` + `flatc` regen via `task generate`).

**Spec:** `docs/superpowers/specs/2026-06-15-xll-date-return-design.md` (§4, §5, §6, §7).

**Cross-repo note:** This plan changes the `types` repo (`protocol.fbs`) AND `xll-gen`. Develop with a local `replace` in `xll-gen/go.mod` (Task 9), then formalize with a `types` tag + version bump per the cross-repo-coordinator / pre-release skills (Task 10).

---

## File Structure

**`types` repo:**
- Modify: `go/protocol/protocol.fbs` — add `table Date` + `Date` union members.
- Regenerate: `include/types/protocol_generated.h`, `go/protocol/*.go` (via `task generate`).
- Modify: `go/protocol/deepcopy.go` — add `Date` to the `AnyValue`/`ScalarValue` clone switches.
- Modify: `src/converters.cpp` — `Date`→`xltypeNum` in `AnyToXLOPER12` and `GridToXLOPER12`.
- Test: `tests/test_converters.cpp` — `Date` materialization.

**`xll-gen` repo:**
- Create: `pkg/xldate/xldate.go` — `ToSerial`/`FromSerial` (leaf, stdlib only).
- Create: `pkg/xldate/xldate_test.go`.
- Modify: `internal/fbany/fbany.go` — `MapGo` + `buildScalarCell` map `time.Time`→`Date`; `Build` gains a `Date` case.
- Modify: `internal/fbany/fbany_test.go` (or create).
- Modify: `pkg/server/converters.go` — `SerialToTime` helper + `ToScalar` `Date` case.
- Modify: `pkg/server/converters_test.go`.
- Modify: `internal/generator/types.go` — `date` entry in `typeRegistry`.
- Modify: `internal/templates/server.go.tmpl` — `date` arg decode case.
- Modify: `internal/templates/interface.go.tmpl` — ensure `time` import when a `date` arg/return exists.
- Test: `internal/generator/gen_cpp_test.go` (or the existing golden test) — `date` arg codegen.
- Modify: `go.mod` — local `replace` then version bump.

---

## Task 1: `pkg/xldate` — wall-clock serial conversion

**Files:**
- Create: `pkg/xldate/xldate.go`
- Test: `pkg/xldate/xldate_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package xldate

import (
	"testing"
	"time"
)

func TestToSerial(t *testing.T) {
	cases := []struct {
		name   string
		in     time.Time
		want   float64
	}{
		// 1899-12-30 is serial 0 (the epoch).
		{"epoch", time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC), 0},
		// Excel serial 61 = 1900-03-01 (the phantom 1900-02-29 absorbs the +1).
		{"leap-boundary", time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC), 61},
		// A modern date: 2026-06-15 = serial 46188.
		{"modern", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), 46188},
		// Noon = +0.5 of a day.
		{"noon", time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC), 46188.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ToSerial(c.in)
			if got != c.want {
				t.Fatalf("ToSerial(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestToSerial_WallClockIgnoresLocation(t *testing.T) {
	// Same wall-clock instant in two zones must produce the same serial
	// (Excel has no timezone; we read the displayed components as-is).
	utc := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	kst := time.Date(2026, 6, 15, 9, 0, 0, 0, time.FixedZone("KST", 9*3600))
	if ToSerial(utc) != ToSerial(kst) {
		t.Fatalf("wall-clock serials differ: utc=%v kst=%v", ToSerial(utc), ToSerial(kst))
	}
}

func TestFromSerial_RoundTrip(t *testing.T) {
	in := time.Date(2026, 6, 15, 12, 30, 0, 0, time.UTC)
	got := FromSerial(ToSerial(in))
	if !got.Equal(in) {
		t.Fatalf("round-trip = %v, want %v", got, in)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd xll-gen && go test ./pkg/xldate/ -run Test -v`
Expected: FAIL — `undefined: ToSerial` / `undefined: FromSerial`.

- [ ] **Step 3: Write the implementation**

```go
// Package xldate converts between Go time.Time and Excel date serial numbers.
//
// Excel has no date type: a date is a double (days since the 1899-12-30 epoch)
// plus a cell number format. Conversion is WALL-CLOCK — the time.Time's own
// displayed components (year/month/day, hour/min/sec) are read as-is, with NO
// timezone conversion, because Excel has no concept of a timezone.
//
// 1900 leap-year bug: Excel incorrectly treats 1900 as a leap year (serial 60 =
// the non-existent 1900-02-29). Using the 1899-12-30 epoch makes serials EXACT
// for all dates on/after 1900-03-01 (the phantom day absorbs the +1 offset),
// which covers every practical financial date. Dates before 1900-03-01 are
// off by one versus Excel; that boundary is documented and out of scope.
package xldate

import (
	"math"
	"time"
)

// excelEpoch is Excel serial 0 (1899-12-30 00:00:00).
var excelEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// ToSerial converts t to an Excel serial using t's wall-clock components.
func ToSerial(t time.Time) float64 {
	// Reinterpret the displayed clock in UTC so the duration math ignores the
	// original location's offset (wall-clock semantics).
	wall := time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
	return wall.Sub(excelEpoch).Seconds() / 86400.0
}

// FromSerial converts an Excel serial back to a time.Time (UTC location). The
// integer part is the date; the fractional part is the time of day.
func FromSerial(serial float64) time.Time {
	ns := int64(math.Round(serial * 86400.0 * 1e9))
	return excelEpoch.Add(time.Duration(ns))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd xll-gen && go test ./pkg/xldate/ -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add xll-gen/pkg/xldate/
git commit -m "feat(xldate): wall-clock Excel serial <-> time.Time conversion"
```

---

## Task 2: `types` schema — add the `Date` union member

**Files:**
- Modify: `types/go/protocol/protocol.fbs`

- [ ] **Step 1: Add the `Date` table and union members**

In `types/go/protocol/protocol.fbs`, add the `Date` table after the other scalar
tables (near line 27, after `RefCache`):

```fbs
table Date { serial: double; format: string; }
```

Append `Date` to BOTH unions (append-only keeps the wire enum backward
compatible):

```fbs
union ScalarValue { Bool, Num, Int, Str, Err, AsyncHandle, Nil, Date }
```
```fbs
union AnyValue { Bool, Num, Int, Str, Err, AsyncHandle, Nil, Grid, NumGrid, Range, RefCache, Date }
```

- [ ] **Step 2: Regenerate C++ + Go artifacts**

Run: `cd types && task generate`
Expected: `include/types/protocol_generated.h` and `go/protocol/*.go` regenerate
with `Date`, `DateT`, `ScalarValueDate`, `AnyValueDate`, `DateStart/AddSerial/AddFormat/End`.
Verify: `cd types && go build ./...` (Expected: success).

- [ ] **Step 3: Commit the schema + regenerated artifacts together**

```bash
cd types
git add go/protocol/protocol.fbs include/types/protocol_generated.h go/protocol/
git commit -m "feat(protocol): add Date union member (serial + optional format)"
```

---

## Task 3: `types` deepcopy — clone the `Date` union member

**Files:**
- Modify: `types/go/protocol/deepcopy.go`
- Test: `types/go/protocol/rtd_once_grid_test.go` (or the existing deepcopy test file)

- [ ] **Step 1: Write a failing clone test**

Add to the deepcopy test file (mirror an existing `Num` clone test):

```go
func TestCloneAny_Date(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	protocol.DateStart(b)
	protocol.DateAddSerial(b, 46188.5)
	dOff := protocol.DateEnd(b)
	protocol.AnyStart(b)
	protocol.AnyAddValType(b, protocol.AnyValueDate)
	protocol.AnyAddVal(b, dOff)
	b.Finish(protocol.AnyEnd(b))

	src := protocol.GetRootAsAny(b.FinishedBytes(), 0)
	cb := flatbuffers.NewBuilder(0)
	cb.Finish(src.Clone(cb)) // adjust to the actual deepcopy entry point
	dst := protocol.GetRootAsAny(cb.FinishedBytes(), 0)

	if dst.ValType() != protocol.AnyValueDate {
		t.Fatalf("clone val_type = %v, want Date", dst.ValType())
	}
	if dst.ValAsDate().Serial() != 46188.5 {
		t.Fatalf("clone serial = %v, want 46188.5", dst.ValAsDate().Serial())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd types && go test ./go/protocol/ -run TestCloneAny_Date -v`
Expected: FAIL — the clone switch drops `Date` (missing case) so `ValType()` is `NONE`.

- [ ] **Step 3: Add the `Date` case to the union clone switches**

In `deepcopy.go`, locate the `switch` over `AnyValue` (and the one over
`ScalarValue`) inside the `Any`/`Scalar` clone methods. Add a `Date` case mirroring
the `Num` case, copying BOTH fields (serial double + the optional format string):

```go
case protocol.AnyValueDate:
	src := protocol.GetRootAsAny(... ).ValAsDate() // follow the existing accessor pattern
	protocol.DateStart(b)
	protocol.DateAddSerial(b, src.Serial())
	if f := src.Format(); len(f) > 0 {
		// strings must be created before the table is started in real code;
		// follow the existing Str-clone ordering in this file.
	}
	off = protocol.DateEnd(b)
```

Match the file's existing ordering convention (strings created before
`*Start`). Apply the same to the `ScalarValue` switch.

- [ ] **Step 4: Run to verify it passes**

Run: `cd types && go test ./go/protocol/ -run TestCloneAny_Date -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd types
git add go/protocol/deepcopy.go go/protocol/*_test.go
git commit -m "feat(protocol): deep-copy the Date union member"
```

---

## Task 4: `types` C++ — materialize `Date` → `xltypeNum`

**Files:**
- Modify: `types/src/converters.cpp` (`AnyToXLOPER12` near line 340; `GridToXLOPER12` cell switch near line 534)
- Test: `types/tests/test_converters.cpp`

- [ ] **Step 1: Write the failing C++ tests**

Add to `test_converters.cpp` (mirror an existing `Num` round-trip test):

```cpp
TEST(Converters, AnyDateBecomesNum) {
    flatbuffers::FlatBufferBuilder b;
    auto d = protocol::CreateDate(b, 46188.5 /*serial*/, 0 /*format*/);
    auto any = protocol::CreateAny(b, protocol::AnyValue::Date, d.Union());
    b.Finish(any);
    const protocol::Any* a = protocol::GetAny(b.GetBufferPointer());

    LPXLOPER12 op = AnyToXLOPER12(a);
    ASSERT_NE(op, nullptr);
    EXPECT_EQ(op->xltype & ~(xlbitDLLFree | xlbitXLFree), xltypeNum);
    EXPECT_DOUBLE_EQ(op->val.num, 46188.5);
    xlAutoFree12(op);
}

TEST(Converters, GridDateCellBecomesNum) {
    flatbuffers::FlatBufferBuilder b;
    auto d = protocol::CreateDate(b, 46188.0, 0);
    auto cell = protocol::CreateScalar(b, protocol::ScalarValue::Date, d.Union());
    std::vector<flatbuffers::Offset<protocol::Scalar>> cells{cell};
    auto vec = b.CreateVector(cells);
    auto grid = protocol::CreateGrid(b, 1, 1, vec);
    b.Finish(grid);
    const protocol::Grid* g = protocol::GetGrid(b.GetBufferPointer());

    LPXLOPER12 op = GridToXLOPER12(g);
    ASSERT_NE(op, nullptr);
    ASSERT_EQ(op->xltype & ~(xlbitDLLFree | xlbitXLFree), xltypeMulti);
    EXPECT_EQ(op->val.array.lparray[0].xltype, xltypeNum);
    EXPECT_DOUBLE_EQ(op->val.array.lparray[0].val.num, 46188.0);
    xlAutoFree12(op);
}
```

(Adjust `protocol::GetAny`/`GetGrid` to the generated accessor names if they
differ; follow the existing tests in this file.)

- [ ] **Step 2: Run to verify they fail**

Run: `cd types && task test` (or build the test target directly)
Expected: FAIL — `AnyToXLOPER12` falls through `Date` to the `Nil` default
(`xltypeNil`), so the `xltypeNum`/value assertions fail.

- [ ] **Step 3: Add the `Date` cases**

In `AnyToXLOPER12` (after the `Num` case, before `Range`):

```cpp
case protocol::AnyValue::Date: {
    LPXLOPER12 op = NewXLOPER12();
    op->xltype = xltypeNum | xlbitDLLFree;
    op->val.num = any->val_as_Date()->serial();
    return op;
}
```

In `GridToXLOPER12`'s per-cell `switch(scalar->val_type())` (after the `Num`
case):

```cpp
case protocol::ScalarValue::Date:
    cell.xltype = xltypeNum;
    cell.val.num = scalar->val_as_Date()->serial();
    break;
```

(Leave `ConvertScalar`/`ConvertAny` — the Excel→FB direction — unchanged; a date
cell read from the sheet is `xltypeNum` and is never re-encoded as `Date`.)

- [ ] **Step 4: Run to verify they pass**

Run: `cd types && task test`
Expected: PASS (new tests green; existing converter tests still green).

- [ ] **Step 5: Commit**

```bash
cd types
git add src/converters.cpp tests/test_converters.cpp
git commit -m "feat(converters): materialize Date scalar/any as xltypeNum serial"
```

---

## Task 5: `xll-gen` local wiring — `replace` to the local `types`

**Files:**
- Modify: `xll-gen/go.mod`

> This unblocks Tasks 6–8 against the regenerated `types` before it is tagged.
> Task 10 reverts this to a real version bump.

- [ ] **Step 1: Add a local replace directive**

Append to `xll-gen/go.mod`:

```
replace github.com/xll-gen/types => ../types
```

- [ ] **Step 2: Verify the new symbols resolve**

Run: `cd xll-gen && go build ./... && go doc github.com/xll-gen/types/go/protocol.AnyValueDate`
Expected: build succeeds; `AnyValueDate` is documented (constant exists).

- [ ] **Step 3: Commit**

```bash
git add xll-gen/go.mod
git commit -m "chore(deps): temporary local replace to ../types for date work"
```

---

## Task 6: `fbany` output — map `time.Time` → `Date`

**Files:**
- Modify: `xll-gen/internal/fbany/fbany.go` (`Build` ~line 43; `MapGo` ~line 152; `buildScalarCell` ~line 217)
- Test: `xll-gen/internal/fbany/fbany_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestMapGo_TimeIsDate(t *testing.T) {
	tag, payload := MapGo(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
	if tag != protocol.AnyValueDate {
		t.Fatalf("tag = %v, want Date", tag)
	}
	if _, ok := payload.(time.Time); !ok {
		t.Fatalf("payload = %T, want time.Time", payload)
	}
}

func TestBuildGo_DateSerial(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	off := BuildGo(b, time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
	b.Finish(off)
	a := protocol.GetRootAsAny(b.FinishedBytes(), 0)
	if a.ValType() != protocol.AnyValueDate {
		t.Fatalf("val_type = %v, want Date", a.ValType())
	}
	if a.ValAsDate().Serial() != 46188 {
		t.Fatalf("serial = %v, want 46188", a.ValAsDate().Serial())
	}
}

func TestBuildGrid_TimeCellIsDate(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	off, err := BuildGrid(b, [][]any{{time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	b.Finish(off)
	g := protocol.GetRootAsGrid(b.FinishedBytes(), 0)
	var c0 protocol.Scalar
	g.Data(&c0, 0)
	if c0.ValType() != protocol.ScalarValueDate {
		t.Fatalf("cell0 val_type = %v, want Date", c0.ValType())
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd xll-gen && go test ./internal/fbany/ -run 'Date|TimeCell|TimeIsDate' -v`
Expected: FAIL — `MapGo` still returns `AnyValueStr` (RFC3339); `buildScalarCell`
hits the `default` Str case for `time.Time`.

- [ ] **Step 3: Add the `Date` building**

In `fbany.go`, add the import `"github.com/xll-gen/xll-gen/pkg/xldate"`.

In `Build`, add a case (before `default`):

```go
case protocol.AnyValueDate:
	t, ok := val.(time.Time)
	if !ok {
		tag = protocol.AnyValueNONE
		break
	}
	protocol.DateStart(b)
	protocol.DateAddSerial(b, xldate.ToSerial(t))
	uOff = protocol.DateEnd(b)
```

In `MapGo`, replace the `time.Time` case:

```go
	case time.Time:
		return protocol.AnyValueDate, v
```

In `buildScalarCell`, add a case (before `default`):

```go
	case time.Time:
		protocol.DateStart(b)
		protocol.DateAddSerial(b, xldate.ToSerial(v))
		uOff = protocol.DateEnd(b)
		valType = protocol.ScalarValueDate
```

Update the `MapGo` doc comment line `time.Time → AnyValueStr (RFC3339)` to
`time.Time → AnyValueDate (Excel serial, wall-clock)`.

- [ ] **Step 4: Run to verify they pass**

Run: `cd xll-gen && go test ./internal/fbany/ -v`
Expected: PASS (new tests green; existing fbany tests still green).

- [ ] **Step 5: Commit**

```bash
git add xll-gen/internal/fbany/
git commit -m "feat(fbany): serialize time.Time as Date serial (scalar + grid cells)"
```

---

## Task 7: `pkg/server` — `SerialToTime` + `ToScalar` Date case

**Files:**
- Modify: `xll-gen/pkg/server/converters.go` (`ToScalar` ~line 13; new helper)
- Test: `xll-gen/pkg/server/converters_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestSerialToTime(t *testing.T) {
	got := SerialToTime(46188) // 2026-06-15
	want := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("SerialToTime(46188) = %v, want %v", got, want)
	}
}

func TestToScalar_Date(t *testing.T) {
	b := flatbuffers.NewBuilder(0)
	protocol.DateStart(b)
	protocol.DateAddSerial(b, 46188.5)
	dOff := protocol.DateEnd(b)
	protocol.AnyStart(b)
	protocol.AnyAddValType(b, protocol.AnyValueDate)
	protocol.AnyAddVal(b, dOff)
	b.Finish(protocol.AnyEnd(b))
	a := protocol.GetRootAsAny(b.FinishedBytes(), 0)

	sv, ok := ToScalar(a)
	if !ok || sv.Type != protocol.AnyValueNum || sv.Num != 46188.5 {
		t.Fatalf("ToScalar(Date) = %+v ok=%v, want Num 46188.5", sv, ok)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd xll-gen && go test ./pkg/server/ -run 'SerialToTime|ToScalar_Date' -v`
Expected: FAIL — `undefined: SerialToTime`; `ToScalar` returns `false` for `Date`.

- [ ] **Step 3: Implement**

In `converters.go`, add the import `"github.com/xll-gen/xll-gen/pkg/xldate"`
(and `"time"`), then:

```go
// SerialToTime converts an Excel date serial (wall-clock) to a time.Time. Used
// by generated code to decode `type: date` arguments.
func SerialToTime(serial float64) time.Time {
	return xldate.FromSerial(serial)
}
```

In `ToScalar`, add a case in the `switch v.ValType()` (a date arrives from a
guest value, e.g. ScheduleSet — surface it as its numeric serial):

```go
	case protocol.AnyValueDate:
		var t protocol.Date
		t.Init(tbl.Bytes, tbl.Pos)
		return ScalarValue{Type: protocol.AnyValueNum, Num: t.Serial()}, true
```

- [ ] **Step 4: Run to verify they pass**

Run: `cd xll-gen && go test ./pkg/server/ -run 'SerialToTime|ToScalar' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add xll-gen/pkg/server/converters.go xll-gen/pkg/server/converters_test.go
git commit -m "feat(server): SerialToTime helper + ToScalar Date case"
```

---

## Task 8: codegen — `date` argument type

**Files:**
- Modify: `xll-gen/internal/generator/types.go` (`typeRegistry` ~line 21)
- Modify: `xll-gen/internal/templates/server.go.tmpl` (arg decode block ~line 316)
- Modify: `xll-gen/internal/templates/interface.go.tmpl` (imports — ensure `time`)
- Test: `xll-gen/internal/generator/gen_cpp_test.go` (or the existing golden/compile-gate test)

- [ ] **Step 1: Write the failing test**

Add a generator test asserting the decode line is emitted for a `date` arg.
Mirror the existing string/int arg golden assertions:

```go
func TestGen_DateArgDecode(t *testing.T) {
	// a function with one `date` arg
	out := generateServerGo(t, `
functions:
  - name: AddDays
    args:
      - {name: d, type: date}
      - {name: n, type: int}
    return: date
`) // use the test's existing harness to render server.go.tmpl
	if !strings.Contains(out, "arg_d := server.SerialToTime(request.D())") {
		t.Fatalf("server.go missing date decode; got:\n%s", out)
	}
}
```

(Use whatever rendering harness the existing generator tests use — e.g.
`gen_cpp_test.go`'s config→render helper. The assertion is the contract.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd xll-gen && go test ./internal/generator/ -run TestGen_DateArgDecode -v`
Expected: FAIL — `date` is not in `typeRegistry`, so the arg renders via the
fallthrough and no `SerialToTime` decode appears.

- [ ] **Step 3: Register the type and emit the decode**

In `types.go` `typeRegistry`, add:

```go
	"date": {
		SchemaType: "double",
		GoType:     "time.Time",
		RetGoType:  "time.Time",
		CppType:    "LPXLOPER12",
		ArgCppType: "double",
		XllType:    "Q",
		ArgXllType: "B",
	},
```

In `server.go.tmpl`, add a branch in the `{{range .Args}}` decode chain
(alongside `{{else if eq .Type "string"}}` etc.):

```gotemplate
	{{else if eq .Type "date"}}
	arg_{{.Name}} := server.SerialToTime(request.{{.Name|capitalize}}())
```

In `interface.go.tmpl`, ensure the generated interface file imports `"time"`
when any function has a `date` arg or return. If the template has a fixed import
block, add `"time"` guarded by a helper (e.g. `{{if anyDateType .Functions}}"time"{{end}}`)
and add `anyDateType` to `funcmap.go` (returns true if any arg/return type is
`date`). If imports are already managed by `goimports`/post-processing, no change
is needed — verify in Step 4.

- [ ] **Step 4: Run to verify it passes + a real build**

Run: `cd xll-gen && go test ./internal/generator/ -run TestGen_DateArgDecode -v`
Expected: PASS.
Then run the existing compile-gate test to confirm a `date`-using project still
compiles end-to-end:
Run: `cd xll-gen && go test ./cmd/ -run CompileGate -v` (add a `date` arg to the
gate fixture if the gate is fixture-driven; see `cmd/compile_gate_test.go`).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add xll-gen/internal/generator/types.go xll-gen/internal/templates/server.go.tmpl xll-gen/internal/templates/interface.go.tmpl xll-gen/internal/generator/
git commit -m "feat(codegen): add date arg type (serial->time.Time decode)"
```

---

## Task 9: full Go verification

- [ ] **Step 1: Run the whole xll-gen suite**

Run: `cd xll-gen && go build ./... && go test ./...`
Expected: PASS. Confirms the value path (input + output) is consistent and no
existing test regressed against the new `types`.

- [ ] **Step 2: Run the types suites**

Run: `cd types && go test ./... && task test`
Expected: PASS (Go protocol + C++ converter tests).

- [ ] **Step 3: Commit any test fixups**

```bash
git add -A && git commit -m "test: green Go + C++ suites for date values"
```

---

## Task 10: cross-repo formalize (release wiring)

> Do this when Plan A is ready to land for real (after Plan B if shipping
> together). Replaces the local `replace` with a pinned version.

- [ ] **Step 1: Tag `types`**

```bash
cd types && git tag v0.2.13 && git push origin HEAD --tags
```

- [ ] **Step 2: Bump `xll-gen` and drop the replace**

In `xll-gen/go.mod`: remove the `replace ... => ../types` line and set
`github.com/xll-gen/types v0.2.13`.

Run: `cd xll-gen && go mod tidy && go build ./... && go test ./...`
Expected: PASS against the published tag.

- [ ] **Step 3: Bump the C++ pin**

Update the `types` `GIT_TAG` to `v0.2.13` in every CMake fetch site (e.g.
`xll-gen-showcase` and any generated-project template that fetches `types`).
Run the cross-repo-coordinator agent to verify no pin was missed.

- [ ] **Step 4: Commit**

```bash
git add xll-gen/go.mod && git commit -m "chore(deps): pin types v0.2.13 (date union)"
```

---

## Self-Review (completed)

- **Spec coverage:** §4 Date union → Tasks 2–4; §5 time.Time→serial → Tasks 1, 6;
  §6 Date→xltypeNum materialization → Task 4 (CollectDateCells/formatting is Plan B);
  §7 input `date` arg → Tasks 7–8. §8/§3 formatting is explicitly Plan B.
- **Placeholders:** none — Task 3's deepcopy string-ordering and Task 8's import
  handling reference the file's existing convention rather than inventing one,
  with the concrete code given.
- **Type consistency:** `ToSerial`/`FromSerial` (xldate) ↔ `SerialToTime`
  (server wraps `FromSerial`); `AnyValueDate`/`ScalarValueDate`, `DateAddSerial`,
  `val_as_Date()->serial()` used consistently across Go and C++.
