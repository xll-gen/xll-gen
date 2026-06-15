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

func (s *Service) OnCalculationEnded(ctx context.Context) error    { return nil }
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

		// graceful: clear all formulas before quit so the harness exits cleanly.
		for _, a := range []string{"A1", "A3", "D1"} {
			_ = SetCellFormula(sheet, a, "")
		}
		_ = app.CalculateFull()
	})
}
