//go:build windows && xll_spill

// Real-Excel verification for the date auto-format feature (Plan B / Task 5).
// Proves that date-typed results display AS DATES (Excel applies a date number
// format at CalculationEnded) and that a BDH-style grid formats ONLY its date
// column. Opt in with:
//
//	go test -tags=xll_spill -run TestDateFormat_Excel ./internal/smoketest/...
//
// The smoke XLL must compile against the LOCAL types checkout (the pinned
// versions.Types tag lacks CollectDateCells / ScheduleDateFormatsForCaller /
// IsDateLikeFormat); point both the Go module replace and the CMake
// FetchContent at it via env XLLGEN_TYPES_SRC (writeProject + buildXLL honor it).
package smoketest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-ole/go-ole"
)

const dateYaml = `
project:
  name: "xll_smoke"
  version: "0.1.0"
gen:
  go:
    package: "generated"
  disable_pid_suffix: true
logging:
  level: "debug"
  dir: "TEMP_DIR"
server:
  workers: 2
  timeout: "10s"
rtd:
  enabled: true
  prog_id: "XllSmoke.Rtd"
  description: "xll-gen date-format harness RTD"
  throttle_interval: "250ms"
functions:
  - name: "DateScalar"
    description: "Returns a midnight date as any (auto-formats yyyy-mm-dd)."
    return: "any"
  - name: "DateTimeScalar"
    description: "Returns a datetime as any (auto-formats yyyy-mm-dd hh:mm:ss)."
    return: "any"
  - name: "BdhGrid"
    description: "Returns a [date,number] grid; only the date column formats."
    return: "grid"
  - name: "BdhGridOnce"
    description: "rtd-once [date,number] grid (YDH shape); date column must format."
    return: "grid"
    mode: "rtd-once"
    memoize_ttl: "60s"
  - name: "YdhGridOnce"
    description: "rtd-once grid WITH a string arg + LARGE date column (faithful YDH analogue)."
    return: "grid"
    mode: "rtd-once"
    memoize_ttl: "60s"
    args: [{name: "ticker", type: "string"}]
`

const dateMain = `package main

import (
	"context"
	"time"

	"xll_smoke/generated"
)

type Service struct{}

func (s *Service) DateScalar(ctx context.Context) (any, error) {
	return time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), nil
}

func (s *Service) DateTimeScalar(ctx context.Context) (any, error) {
	return time.Date(2026, 6, 15, 13, 30, 0, 0, time.UTC), nil
}

func (s *Service) BdhGrid(ctx context.Context) ([][]any, error) {
	return [][]any{
		{time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), 1.5},
		{time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC), 2.5},
	}, nil
}

// BdhGridOnce mirrors the YDH showcase shape: a header row of strings, then
// data rows whose column 0 is a time.Time WITH a time-of-day fraction (22:30 ->
// .9375 serial fraction, exactly YDH's failing case) and other columns numeric.
// mode:rtd-once means the grid is delivered guest->host and spilled on the
// readiness recalc — the path the sync BdhGrid above does NOT exercise.
func (s *Service) BdhGridOnce(ctx context.Context) ([][]any, error) {
	return [][]any{
		{"Date", "Px"},
		{time.Date(2026, 6, 15, 22, 30, 0, 0, time.UTC), 1.5},
		{time.Date(2026, 6, 16, 22, 30, 0, 0, time.UTC), 2.5},
	}, nil
}

// YdhGridOnce is the faithful YDH analogue: a rtd-once grid WITH a string arg
// (so it rides the content-hash payload path) returning a LARGE grid: a header
// row then 30 data rows whose column 0 is a descending series of datetimes
// (.9375 fraction). This stresses the calc#2 spill-materialization vs.
// CalculationEnded-drain timing on a many-row spill.
func (s *Service) YdhGridOnce(ctx context.Context, ticker string) ([][]any, error) {
	rows := make([][]any, 0, 31)
	rows = append(rows, []any{"Date", ticker})
	base := time.Date(2026, 6, 15, 22, 30, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		rows = append(rows, []any{base.AddDate(0, 0, -i), 100.0 + float64(i)})
	}
	return rows, nil
}

func (s *Service) OnCalculationEnded(ctx context.Context) error    { return nil }
func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }
func (s *Service) OnRtdConnect(ctx context.Context, topicID int32, strings []string, newValues bool) error {
	return nil
}
func (s *Service) OnRtdDisconnect(ctx context.Context, topicID int32) error { return nil }

func main() { generated.Serve(&Service{}) }
`

// dateEventYaml is the regression fixture for the showcase YDH bug: a project
// that DECLARES a user CalculationEnded event handler (OnRecalc). Pre-fix, the
// generated named-event stub only logged and the built-in CalculationEnded()
// macro (which runs DrainAndApplyDateFormats) was suppressed whenever a user
// handler existed — so the date column stayed raw serials (General). The fix
// routes the user handler through HandleCalculationEnded(). SYNC grid, no RTD:
// the bug is in the calc-end handler suppression and hits every mode equally,
// so a sync grid is the fastest faithful repro.
const dateEventYaml = `
project:
  name: "xll_smoke"
  version: "0.1.0"
gen:
  go:
    package: "generated"
  disable_pid_suffix: true
logging:
  level: "debug"
  dir: "TEMP_DIR"
server:
  workers: 2
  timeout: "10s"
events:
  - type: "CalculationEnded"
    handler: "OnRecalc"
functions:
  - name: "DateGridEv"
    description: "Sync [date,number] grid; only the date column auto-formats. Project declares a user CalculationEnded handler."
    return: "grid"
`

const dateEventMain = `package main

import (
	"context"
	"time"

	"xll_smoke/generated"
)

type Service struct{}

// DateGridEv returns a BDH-shaped grid: a string header row, then 2 data rows
// whose column 0 is a midnight time.Time (expected yyyy-mm-dd) and column 1 is
// a plain number (must NOT auto-format). Sync grid (return:grid, default mode).
func (s *Service) DateGridEv(ctx context.Context) ([][]any, error) {
	return [][]any{
		{"Date", "Px"},
		{time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), 1.5},
		{time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC), 2.5},
	}, nil
}

// OnRecalc is the user-declared CalculationEnded handler. The generated XLL
// must route this through HandleCalculationEnded() so the date-format drain
// still runs. A no-op returning nil is enough to exercise the suppression bug.
func (s *Service) OnRecalc(ctx context.Context) error { return nil }

// OnCalculationCanceled is still required by the generated interface (only the
// CalculationEnded event is declared, so OnCalculationEnded is replaced by
// OnRecalc but CalculationCanceled keeps its default handler name).
func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }

func main() { generated.Serve(&Service{}) }
`

// isDateLikeFmt mirrors types::IsDateLikeFormat: a format is date-like if it
// contains a y/m/d/h/s field token OUTSIDE quotes/brackets (so a literal
// "day" string or [Red] color section doesn't count). The default formats we
// apply are exactly "yyyy-mm-dd" and "yyyy-mm-dd hh:mm:ss".
func isDateLikeFmt(s string) bool {
	inQuote := false
	inBracket := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == '"' {
				inQuote = false
			}
			continue
		}
		if inBracket {
			if c == ']' {
				inBracket = false
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '[':
			inBracket = true
		case '\\':
			i++ // escaped next char is a literal
		case 'y', 'Y', 'm', 'M', 'd', 'D', 'h', 'H', 's', 'S':
			return true
		}
	}
	return false
}

// pollDateFormat waits until cellAddr's NumberFormat is date-like, or timeout.
// The format lands during the CalculationEnded drain callback, which runs
// asynchronously after CalculateFull returns, so we must poll. The STA pump
// happens implicitly via the GetCellNumberFormat COM round-trip.
func pollDateFormat(t *testing.T, sheet *ole.IDispatch, addr string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for {
		f, err := GetCellNumberFormat(sheet, addr)
		if err != nil {
			t.Fatalf("GetCellNumberFormat(%s): %v", addr, err)
		}
		last = f
		if isDateLikeFmt(f) {
			return f
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("cell %s NumberFormat did not become date-like within %s (last=%q)", addr, timeout, last)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// pollHasValue waits until cellAddr holds a non-pending, non-error value (a
// number, a time.Time, a non-empty string, or a bool), or timeout. Unlike
// pollNumeric it tolerates time.Time — go-ole surfaces a date-formatted cell's
// Value as VT_DATE -> time.Time, which is a fully-resolved value, not pending.
func pollHasValue(t *testing.T, sheet *ole.IDispatch, addr string, timeout time.Duration) any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last any
	for {
		v := rawCell(t, sheet, addr)
		last = v
		if !isPending(v) && !isErrCode(v) {
			switch x := v.(type) {
			case time.Time:
				return v
			case string:
				if x != "" {
					return v
				}
			default:
				if _, ok := asFloat(v); ok {
					return v
				}
				if _, ok := v.(bool); ok {
					return v
				}
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("cell %s did not resolve to a value within %s (last=%v %T)", addr, timeout, last, last)
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// assertDateValue accepts go-ole's two surfacings of a date cell's Value: a Go
// time.Time (VT_DATE) or the float64 serial. Asserts it corresponds to
// 2026-06-15 (serial 46188; Excel's 1900 date system, midnight).
func assertDateValue(t *testing.T, sheet *ole.IDispatch, addr string, wantSerial float64) {
	t.Helper()
	v := rawCell(t, sheet, addr)
	switch x := v.(type) {
	case time.Time:
		if x.Year() != 2026 || x.Month() != time.June || x.Day() != 15 {
			t.Errorf("%s: expected date 2026-06-15, got time.Time %v", addr, x)
		} else {
			t.Logf("%s value = time.Time %v (Y/M/D 2026/6/15 OK)", addr, x)
		}
	case float64:
		if x != wantSerial {
			t.Errorf("%s: expected serial %v (2026-06-15), got float64 %v", addr, wantSerial, x)
		} else {
			t.Logf("%s value = float64 serial %v (== %v OK)", addr, x, wantSerial)
		}
	default:
		t.Errorf("%s: expected time.Time or float64 serial, got %v (%T)", addr, v, v)
	}
}

func TestDateFormat_Excel(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	root := repoRootOrFatal(t)
	workDir := os.Getenv("XLL_DATE_DIR")
	if workDir == "" {
		var err error
		workDir, err = os.MkdirTemp("", "xll-date-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(workDir) })
	}
	projectDir := filepath.Join(workDir, "xll_smoke")
	if _, err := os.Stat(filepath.Join(projectDir, "xll.yaml")); err != nil {
		writeProject(t, projectDir, dateYaml, dateMain, root)
	}
	t.Logf("project dir: %s", projectDir)
	xllPath := buildProject(t, projectDir)
	t.Logf("xll: %s", xllPath)

	const serial20260615 = 46188 // Excel 1900 date-system serial for 2026-06-15

	runExcel(t, xllPath, func(app *excelApp, sheet *ole.IDispatch) {
		// ---------------------------------------------------------------
		// 1) Scalar midnight date: =DateScalar() -> A1 -> yyyy-mm-dd.
		// ---------------------------------------------------------------
		t.Logf("--- DateScalar (A1) ---")
		mustFormula(t, sheet, "A1", "=DateScalar()")
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull after DateScalar: %v", err)
		}
		// Cell must hold a value first, then the format lands at calc-end.
		pollHasValue(t, sheet, "A1", 15*time.Second)
		fmtA1 := pollDateFormat(t, sheet, "A1", 10*time.Second)
		t.Logf("A1 NumberFormat = %q", fmtA1)
		if !isDateLikeFmt(fmtA1) {
			t.Errorf("A1: NumberFormat %q is not date-like", fmtA1)
		}
		assertDateValue(t, sheet, "A1", serial20260615)

		// ---------------------------------------------------------------
		// 2) Scalar datetime: =DateTimeScalar() -> A3 -> yyyy-mm-dd hh:mm:ss.
		// ---------------------------------------------------------------
		t.Logf("--- DateTimeScalar (A3) ---")
		mustFormula(t, sheet, "A3", "=DateTimeScalar()")
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull after DateTimeScalar: %v", err)
		}
		pollHasValue(t, sheet, "A3", 15*time.Second)
		fmtA3 := pollDateFormat(t, sheet, "A3", 10*time.Second)
		t.Logf("A3 NumberFormat = %q", fmtA3)
		if !isDateLikeFmt(fmtA3) {
			t.Errorf("A3: NumberFormat %q is not date-like", fmtA3)
		}
		if !strings.ContainsAny(fmtA3, "hH") {
			t.Errorf("A3: datetime NumberFormat %q lacks a time token (expected hh:mm:ss)", fmtA3)
		}

		// ---------------------------------------------------------------
		// 3) BDH grid: =BdhGrid() -> D1, spills D1:E2. Column 0 (D) dates,
		//    column 1 (E) numbers. ONLY the date column must be formatted.
		// ---------------------------------------------------------------
		t.Logf("--- BdhGrid (D1:E2) ---")
		mustFormula(t, sheet, "D1", "=BdhGrid()")
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull after BdhGrid: %v", err)
		}
		pollHasValue(t, sheet, "D1", 15*time.Second)
		// Date column D1/D2 must become date-like.
		fmtD1 := pollDateFormat(t, sheet, "D1", 10*time.Second)
		fmtD2 := pollDateFormat(t, sheet, "D2", 10*time.Second)
		t.Logf("D1 NumberFormat = %q, D2 NumberFormat = %q", fmtD1, fmtD2)
		if !isDateLikeFmt(fmtD1) {
			t.Errorf("D1: NumberFormat %q is not date-like", fmtD1)
		}
		if !isDateLikeFmt(fmtD2) {
			t.Errorf("D2: NumberFormat %q is not date-like", fmtD2)
		}
		// Number column E1/E2 must NOT be date-like (proves column isolation).
		fmtE1, err := GetCellNumberFormat(sheet, "E1")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(E1): %v", err)
		}
		fmtE2, err := GetCellNumberFormat(sheet, "E2")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(E2): %v", err)
		}
		t.Logf("E1 NumberFormat = %q, E2 NumberFormat = %q", fmtE1, fmtE2)
		if isDateLikeFmt(fmtE1) {
			t.Errorf("E1: number column was date-formatted %q (expected General/number)", fmtE1)
		}
		if isDateLikeFmt(fmtE2) {
			t.Errorf("E2: number column was date-formatted %q (expected General/number)", fmtE2)
		}

		// ---------------------------------------------------------------
		// 4) Idempotency / conditional-skip: pre-format A1 to a DIFFERENT
		//    date format. DrainAndApplyDateFormats sees an existing date-like
		//    format (IsDateLikeFormat true) and SKIPS it, leaving it untouched.
		// ---------------------------------------------------------------
		t.Logf("--- idempotency (A1 pre-set dd/mm/yyyy) ---")
		const preFmt = "dd/mm/yyyy"
		if err := SetCellNumberFormat(sheet, "A1", preFmt); err != nil {
			t.Fatalf("SetCellNumberFormat(A1, %q): %v", preFmt, err)
		}
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull (idempotency): %v", err)
		}
		// Give the calc-end drain a window to (not) touch A1.
		time.Sleep(1500 * time.Millisecond)
		fmtA1after, err := GetCellNumberFormat(sheet, "A1")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(A1) after re-calc: %v", err)
		}
		t.Logf("A1 NumberFormat after re-calc = %q (pre-set %q)", fmtA1after, preFmt)
		if fmtA1after != preFmt {
			t.Errorf("idempotency: A1 format changed from %q to %q (conditional-skip should leave an existing date format untouched)", preFmt, fmtA1after)
		}

		// ---------------------------------------------------------------
		// 5) rtd-once GRID (YDH showcase): =BdhGridOnce() -> G1, spills
		//    G1:H3. Row 1 (G1:H1) is a string header; rows 2-3 column G
		//    (G2,G3) are datetimes (.9375 fraction). The date column MUST be
		//    auto-formatted as a datetime even though the spill happens on the
		//    RTD readiness recalc (calc#2), not the user recalc (calc#1). This
		//    is the path the sync BdhGrid above does NOT cover (the bug).
		// ---------------------------------------------------------------
		t.Logf("--- BdhGridOnce (G1:H3, rtd-once) ---")
		app.SetRtdThrottle(250)
		mustFormula(t, sheet, "G1", "=BdhGridOnce()")
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull after BdhGridOnce: %v", err)
		}
		// First paint is #GETTING_DATA; the readiness push recalcs and spills
		// the real grid. Wait for the date data cells to resolve to a value.
		pollHasValue(t, sheet, "G2", 25*time.Second)
		pollHasValue(t, sheet, "G3", 25*time.Second)
		// Date column G2/G3 must become date-like (datetime, has hh token).
		fmtG2 := pollDateFormat(t, sheet, "G2", 12*time.Second)
		fmtG3 := pollDateFormat(t, sheet, "G3", 12*time.Second)
		t.Logf("G2 NumberFormat = %q, G3 NumberFormat = %q", fmtG2, fmtG3)
		if !isDateLikeFmt(fmtG2) {
			t.Errorf("G2: rtd-once date column NumberFormat %q is not date-like", fmtG2)
		}
		if !isDateLikeFmt(fmtG3) {
			t.Errorf("G3: rtd-once date column NumberFormat %q is not date-like", fmtG3)
		}
		if !strings.ContainsAny(fmtG2, "hH") {
			t.Errorf("G2: datetime NumberFormat %q lacks a time token (expected hh:mm:ss)", fmtG2)
		}
		// Number column H2/H3 must NOT be date-like (column isolation holds).
		fmtH2, err := GetCellNumberFormat(sheet, "H2")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(H2): %v", err)
		}
		t.Logf("H2 NumberFormat = %q", fmtH2)
		if isDateLikeFmt(fmtH2) {
			t.Errorf("H2: number column was date-formatted %q (expected General/number)", fmtH2)
		}
		// Header row G1 (a string) must NOT be date-formatted.
		fmtG1, err := GetCellNumberFormat(sheet, "G1")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(G1): %v", err)
		}
		if isDateLikeFmt(fmtG1) {
			t.Errorf("G1: header (string) row was date-formatted %q", fmtG1)
		}

		// Idempotency across a re-calc: the format must survive (and not be
		// stomped by a re-spill clearing it).
		t.Logf("--- BdhGridOnce idempotency (re-calc) ---")
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull (rtd-once grid idempotency): %v", err)
		}
		time.Sleep(1500 * time.Millisecond)
		fmtG2after, err := GetCellNumberFormat(sheet, "G2")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(G2) after re-calc: %v", err)
		}
		t.Logf("G2 NumberFormat after re-calc = %q", fmtG2after)
		if !isDateLikeFmt(fmtG2after) {
			t.Errorf("idempotency: G2 lost its date format after re-calc (%q)", fmtG2after)
		}

		// ---------------------------------------------------------------
		// 6) FAITHFUL YDH analogue: =YdhGridOnce("AAPL") -> J1, spills
		//    J1:K31 (header + 30 data rows). Column J data cells (J2..J31)
		//    are datetimes. A LARGE rtd-once spill stresses the calc#2
		//    spill-materialization vs CalculationEnded-drain timing: if the
		//    drain runs before Excel commits the later spill rows, the bottom
		//    rows would be left unformatted (the YDH symptom: raw serials).
		// ---------------------------------------------------------------
		t.Logf("--- YdhGridOnce (J1:K31, rtd-once + arg, LARGE) ---")
		mustFormula(t, sheet, "J1", `=YdhGridOnce("AAPL")`)
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull after YdhGridOnce: %v", err)
		}
		// Wait for the spill to resolve (top and bottom data cells).
		pollHasValue(t, sheet, "J2", 25*time.Second)
		pollHasValue(t, sheet, "J31", 25*time.Second)
		// Sample date cells across the whole spill: top, middle, bottom.
		for _, addr := range []string{"J2", "J16", "J31"} {
			f := pollDateFormat(t, sheet, addr, 12*time.Second)
			t.Logf("%s NumberFormat = %q", addr, f)
			if !isDateLikeFmt(f) {
				t.Errorf("%s: YDH-analogue date cell NumberFormat %q is not date-like", addr, f)
			}
			if !strings.ContainsAny(f, "hH") {
				t.Errorf("%s: datetime NumberFormat %q lacks a time token", addr, f)
			}
		}
		// Number column K must NOT be date-like.
		fmtK16, err := GetCellNumberFormat(sheet, "K16")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(K16): %v", err)
		}
		if isDateLikeFmt(fmtK16) {
			t.Errorf("K16: number column was date-formatted %q", fmtK16)
		}

		// graceful: clear all formulas before quit so the harness exits cleanly.
		for _, a := range []string{"A1", "A3", "D1", "G1", "J1"} {
			_ = SetCellFormula(sheet, a, "")
		}
		_ = app.CalculateFull()
		time.Sleep(500 * time.Millisecond)
	})
}

// TestDateFormat_CalcEndedEvent_Excel is the real-Excel regression for the
// showcase YDH bug: a project that declares a USER CalculationEnded event
// handler (OnRecalc). The pre-fix template emitted a log-only named-event stub
// AND suppressed the built-in CalculationEnded() macro, so DrainAndApplyDateFormats
// never ran and the date column stayed raw serials (General). The fix routes the
// user handler through HandleCalculationEnded(). This test FAILS on the pre-fix
// template (date column General) and PASSES with the fix in the working tree.
//
// SYNC grid, no RTD: the suppression bug is in the calc-end handler and affects
// every mode equally, so a sync grid is the fastest faithful repro. Note the
// existing date e2e cases (TestDateFormat_Excel) do NOT declare a
// CalculationEnded event — which is exactly why they passed while the showcase
// (which declares one) failed.
func TestDateFormat_CalcEndedEvent_Excel(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	root := repoRootOrFatal(t)
	workDir := os.Getenv("XLL_DATEEV_DIR")
	if workDir == "" {
		var err error
		workDir, err = os.MkdirTemp("", "xll-dateev-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(workDir) })
	}
	projectDir := filepath.Join(workDir, "xll_smoke")
	if _, err := os.Stat(filepath.Join(projectDir, "xll.yaml")); err != nil {
		writeProject(t, projectDir, dateEventYaml, dateEventMain, root)
	}
	t.Logf("project dir: %s", projectDir)
	xllPath := buildProject(t, projectDir)
	t.Logf("xll: %s", xllPath)

	runExcel(t, xllPath, func(app *excelApp, sheet *ole.IDispatch) {
		// =DateGridEv() -> A1, spills A1:B3. Row 1 is a string header; rows 2-3
		// column A (A2,A3) are midnight dates, column B (B2,B3) are numbers.
		// The date column MUST become date-like (yyyy-mm-dd) EVEN THOUGH the
		// project declares a user CalculationEnded handler (OnRecalc) — that is
		// the variable that triggered the suppression bug.
		t.Logf("--- DateGridEv (A1:B3, sync grid, user CalculationEnded handler) ---")
		mustFormula(t, sheet, "A1", "=DateGridEv()")
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull after DateGridEv: %v", err)
		}
		// Wait for the grid to spill: the date data cells resolve to a value.
		pollHasValue(t, sheet, "A2", 25*time.Second)
		pollHasValue(t, sheet, "A3", 25*time.Second)

		// 2) Date column A2/A3 must become date-like (the fix's drain ran).
		fmtA2 := pollDateFormat(t, sheet, "A2", 12*time.Second)
		fmtA3 := pollDateFormat(t, sheet, "A3", 12*time.Second)
		t.Logf("A2 NumberFormat = %q, A3 NumberFormat = %q", fmtA2, fmtA3)
		if !isDateLikeFmt(fmtA2) {
			t.Errorf("A2: date column NumberFormat %q is not date-like (user CalculationEnded handler suppressed the drain?)", fmtA2)
		}
		if !isDateLikeFmt(fmtA3) {
			t.Errorf("A3: date column NumberFormat %q is not date-like (user CalculationEnded handler suppressed the drain?)", fmtA3)
		}

		// 3) Number column B2/B3 must NOT be date-like (column isolation holds).
		fmtB2, err := GetCellNumberFormat(sheet, "B2")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(B2): %v", err)
		}
		fmtB3, err := GetCellNumberFormat(sheet, "B3")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(B3): %v", err)
		}
		t.Logf("B2 NumberFormat = %q, B3 NumberFormat = %q", fmtB2, fmtB3)
		if isDateLikeFmt(fmtB2) {
			t.Errorf("B2: number column was date-formatted %q (expected General/number)", fmtB2)
		}
		if isDateLikeFmt(fmtB3) {
			t.Errorf("B3: number column was date-formatted %q (expected General/number)", fmtB3)
		}

		// Header row A1 (a string) must NOT be date-formatted.
		fmtA1, err := GetCellNumberFormat(sheet, "A1")
		if err != nil {
			t.Fatalf("GetCellNumberFormat(A1): %v", err)
		}
		if isDateLikeFmt(fmtA1) {
			t.Errorf("A1: header (string) row was date-formatted %q", fmtA1)
		}

		// 4) graceful clear + recalc before returning so the harness exits clean.
		_ = SetCellFormula(sheet, "A1", "")
		_ = app.CalculateFull()
		time.Sleep(500 * time.Millisecond)
	})
}
