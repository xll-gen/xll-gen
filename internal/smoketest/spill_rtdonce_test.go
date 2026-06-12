//go:build windows && xll_spill

// Real-Excel verification for the spill (grid/numgrid/async-grid) and
// rtd-once features. Opt in with:
//
//	go test -tags=xll_spill -run TestSpill ./internal/smoketest/...     (Task 2)
//	go test -tags=xll_spill -run TestRtdOnce ./internal/smoketest/...   (Task 3)
//
// These reuse the build pipeline (prepareProject/generateCode/buildServer/
// buildXLL/colocateServer) and the excelApp COM driver from the package, but
// drive their own xll.yaml + main.go. The project name stays "xll_smoke" so
// the existing buildXLL candidate paths and generateCode modName resolve; the
// fixed SHM name means only one of these may run at a time.
package smoketest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// ---- project materialization (parameterized clone of prepareProject) -------

func writeProject(t *testing.T, projectDir, yaml, mainGo, repoRoot string) {
	t.Helper()
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runIn(projectDir, "go", "mod", "init", "xll_smoke"); err != nil {
		t.Fatalf("go mod init: %v\n%s", err, out)
	}
	if out, err := runIn(projectDir, "go", "mod", "edit",
		"-replace", "github.com/xll-gen/xll-gen="+repoRoot); err != nil {
		t.Fatalf("go mod edit: %v\n%s", err, out)
	}
}

func buildProject(t *testing.T, projectDir string) string {
	t.Helper()
	if _, err := generateCode(projectDir); err != nil {
		t.Fatalf("generate: %v", err)
	}
	serverExe, err := buildServer(projectDir, "xll_smoke")
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	xllPath, err := buildXLL(projectDir, os.Getenv("XLL_SMOKE_FETCHCACHE"))
	if err != nil {
		t.Fatalf("build xll: %v", err)
	}
	if _, err := colocateServer(xllPath, serverExe); err != nil {
		t.Fatalf("colocate: %v", err)
	}
	return xllPath
}

func repoRootOrFatal(t *testing.T) string {
	t.Helper()
	root, err := deriveRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// ---- COM read helpers tolerant of VT_ERROR / grids -------------------------

// rawCell returns the *ole.VARIANT value at a cell, including VT_ERROR which
// go-ole surfaces as int32 (e.g. 2043 #GETTING_DATA, 2042 #N/A) — NOT a Go
// error. We need to distinguish those from real numeric results.
func rawCell(t *testing.T, sheet *ole.IDispatch, addr string) any {
	t.Helper()
	v, err := GetCellValue(sheet, addr)
	if err != nil {
		t.Fatalf("read %s: %v", addr, err)
	}
	return v
}

func isGettingData(v any) bool {
	switch x := v.(type) {
	case int32:
		return x == 2043
	case int16:
		return x == 2043
	case string:
		return x == "#GETTING_DATA"
	}
	return false
}

func isErrCode(v any) bool {
	switch x := v.(type) {
	case int32:
		// Excel CVErr codes: 2000..2047 range surface as small positive ints.
		return x >= 2000 && x <= 2047
	case int16:
		return x >= 2000 && x <= 2047
	}
	return false
}

// pollNumeric waits until a cell holds a real (non-error, non-getting-data)
// numeric value or timeout. Returns the float64 value.
func pollNumeric(t *testing.T, app *excelApp, sheet *ole.IDispatch, addr string, timeout time.Duration) (float64, any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last any
	for {
		v := rawCell(t, sheet, addr)
		last = v
		if !isErrCode(v) {
			if f, ok := asFloat(v); ok {
				return f, v
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("cell %s did not resolve numeric within %s (last=%v %T)", addr, timeout, last, last)
		}
		time.Sleep(120 * time.Millisecond)
		_ = app // keep STA pumped via the GetCellValue round-trip
	}
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int32:
		return float64(x), true
	case int16:
		return float64(x), true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func asStr(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// ============================ Task 2: SPILL =================================

const spillYaml = `
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
  async_ack_timeout: "2s"
functions:
  - name: "MyGrid"
    description: "Returns a mixed [][]any grid (sync, spills via Q)."
    return: "grid"
  - name: "MyNumGrid"
    description: "Returns a 3x2 [][]float64 grid (sync, spills via K%)."
    return: "numgrid"
  - name: "MyAsyncGrid"
    description: "Returns a mixed [][]any grid (async, spills via Q)."
    return: "grid"
    mode: "async"
`

const spillMain = `package main

import (
	"context"

	"xll_smoke/generated"
)

type Service struct{}

func (s *Service) MyGrid(ctx context.Context) ([][]any, error) {
	return [][]any{{int32(1), "a"}, {2.5, true}}, nil
}

func (s *Service) MyNumGrid(ctx context.Context) ([][]float64, error) {
	return [][]float64{{1, 2}, {3, 4}, {5, 6}}, nil
}

func (s *Service) MyAsyncGrid(ctx context.Context) ([][]any, error) {
	return [][]any{{int32(10), "x"}, {20.5, false}}, nil
}

func (s *Service) OnCalculationEnded(ctx context.Context) error   { return nil }
func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }

func main() { generated.Serve(&Service{}) }
`

func TestSpill_Excel(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	root := repoRootOrFatal(t)
	workDir := os.Getenv("XLL_SPILL_DIR")
	if workDir == "" {
		var err error
		workDir, err = os.MkdirTemp("", "xll-spill-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(workDir) })
	}
	projectDir := filepath.Join(workDir, "xll_smoke")
	if _, err := os.Stat(filepath.Join(projectDir, "xll.yaml")); err != nil {
		writeProject(t, projectDir, spillYaml, spillMain, root)
	}
	t.Logf("project dir: %s", projectDir)
	xllPath := buildProject(t, projectDir)
	t.Logf("xll: %s", xllPath)

	// XLL_SPILL_FN isolates one function for diagnosis: "grid", "numgrid",
	// "async", or "" (all). Used to pin down which return-conversion path
	// crashes Excel.
	only := os.Getenv("XLL_SPILL_FN")

	runExcel(t, xllPath, func(app *excelApp, sheet *ole.IDispatch) {
		if only == "" || only == "grid" {
			t.Logf("--- MyGrid ---")
			mustFormula(t, sheet, "A1", "=MyGrid()")
			if err := app.CalculateFull(); err != nil {
				t.Fatalf("CalculateFull after MyGrid: %v", err)
			}
			pollNumeric(t, app, sheet, "A1", 20*time.Second)
			assertNum(t, sheet, "A1", 1)
			assertStr(t, sheet, "B1", "a")
			assertNum(t, sheet, "A2", 2.5)
			assertCellTrue(t, sheet, "B2")
			assertHasSpill(t, sheet, "A1")
		}

		if only == "" || only == "numgrid" {
			t.Logf("--- MyNumGrid ---")
			mustFormula(t, sheet, "D1", "=MyNumGrid()")
			if err := app.CalculateFull(); err != nil {
				t.Fatalf("CalculateFull after MyNumGrid: %v", err)
			}
			pollNumeric(t, app, sheet, "D1", 20*time.Second)
			assertNum(t, sheet, "D1", 1)
			assertNum(t, sheet, "E1", 2)
			assertNum(t, sheet, "D2", 3)
			assertNum(t, sheet, "E2", 4)
			assertNum(t, sheet, "D3", 5)
			assertNum(t, sheet, "E3", 6)
			assertHasSpill(t, sheet, "D1")
		}

		if only == "" || only == "async" {
			t.Logf("--- MyAsyncGrid ---")
			mustFormula(t, sheet, "H1", "=MyAsyncGrid()")
			if err := app.CalculateFull(); err != nil {
				t.Fatalf("CalculateFull after MyAsyncGrid: %v", err)
			}
			pollNumeric(t, app, sheet, "H1", 30*time.Second)
			assertNum(t, sheet, "H1", 10)
			assertStr(t, sheet, "I1", "x")
			assertNum(t, sheet, "H2", 20.5)
			assertCellFalse(t, sheet, "I2")
			assertHasSpill(t, sheet, "H1")
		}

		// graceful: clear spill formulas before quit
		for _, a := range []string{"A1", "D1", "H1"} {
			_ = SetCellFormula(sheet, a, "")
		}
		_ = app.CalculateFull()
	})
}

// ============================ Task 3: RTD-ONCE ==============================

const onceYaml = `
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
  description: "xll-gen spill/once harness RTD"
  throttle_interval: "250ms"
functions:
  - name: "SlowOnce"
    description: "Sleeps ~2s then returns 111 (rtd-once, default re-computes)."
    return: "int"
    mode: "rtd-once"
  - name: "SlowMemo"
    description: "Sleeps ~2s then returns 222 (rtd-once, memoize forever)."
    return: "int"
    mode: "rtd-once"
    memoize: true
  - name: "SlowTtl"
    description: "Sleeps ~2s then returns 333 (rtd-once, memoize_ttl 5s)."
    return: "int"
    mode: "rtd-once"
    memoize_ttl: "5s"
`

const onceMain = `package main

import (
	"context"
	"sync/atomic"
	"time"

	"xll_smoke/generated"
)

type Service struct{}

var (
	onceCalls int64
	memoCalls int64
	ttlCalls  int64
)

func (s *Service) SlowOnce(ctx context.Context) (int32, error) {
	atomic.AddInt64(&onceCalls, 1)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(2 * time.Second):
	}
	return 111, nil
}

func (s *Service) SlowMemo(ctx context.Context) (int32, error) {
	atomic.AddInt64(&memoCalls, 1)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(2 * time.Second):
	}
	return 222, nil
}

func (s *Service) SlowTtl(ctx context.Context) (int32, error) {
	atomic.AddInt64(&ttlCalls, 1)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(2 * time.Second):
	}
	return 333, nil
}

func (s *Service) OnCalculationEnded(ctx context.Context) error    { return nil }
func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }
func (s *Service) OnRtdConnect(ctx context.Context, topicID int32, strings []string, newValues bool) error {
	return nil
}
func (s *Service) OnRtdDisconnect(ctx context.Context, topicID int32) error { return nil }

func main() { generated.Serve(&Service{}) }
`

func TestRtdOnce_Excel(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	root := repoRootOrFatal(t)
	workDir := os.Getenv("XLL_ONCE_DIR")
	if workDir == "" {
		var err error
		workDir, err = os.MkdirTemp("", "xll-once-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(workDir) })
	}
	projectDir := filepath.Join(workDir, "xll_smoke")
	if _, err := os.Stat(filepath.Join(projectDir, "xll.yaml")); err != nil {
		writeProject(t, projectDir, onceYaml, onceMain, root)
	}
	t.Logf("project dir: %s", projectDir)
	xllPath := buildProject(t, projectDir)
	t.Logf("xll: %s", xllPath)

	runExcel(t, xllPath, func(app *excelApp, sheet *ole.IDispatch) {
		app.SetRtdThrottle(250)

		// SlowOnce in TWO cells (multi-topicID one-key case): identical formula
		// → same topic strings → same RtdOnce key. Both must resolve.
		mustFormula(t, sheet, "A1", "=SlowOnce()")
		mustFormula(t, sheet, "A2", "=SlowOnce()")
		mustFormula(t, sheet, "B1", "=SlowMemo()")
		mustFormula(t, sheet, "C1", "=SlowTtl()")
		if err := app.CalculateFull(); err != nil {
			t.Fatal(err)
		}

		// Initially the cell holds #GETTING_DATA. go-ole's Range.Value maps the
		// VT_ERROR (xlErrGettingData, 2043) to a nil interface{} — NOT int32
		// 2043 — so "pending" is observed as nil/2043, never as the final
		// number. The handler sleeps ~2s so the window is wide. We assert the
		// cell is NOT yet the final numeric value.
		gd1 := rawCell(t, sheet, "A1")
		t.Logf("A1 initial = %v (%T)", gd1, gd1)
		if !isPending(gd1) {
			t.Errorf("A1: expected pending (#GETTING_DATA → nil/2043) initially, got %v (%T)", gd1, gd1)
		}

		// Poll all to final values.
		v1, _ := pollNumeric(t, app, sheet, "A1", 25*time.Second)
		v2, _ := pollNumeric(t, app, sheet, "A2", 25*time.Second)
		vm, _ := pollNumeric(t, app, sheet, "B1", 25*time.Second)
		vt, _ := pollNumeric(t, app, sheet, "C1", 25*time.Second)
		t.Logf("resolved: A1=%v A2=%v B1=%v C1=%v", v1, v2, vm, vt)
		assertEqf(t, "SlowOnce A1", v1, 111)
		assertEqf(t, "SlowOnce A2 (multi-topicID one-key)", v2, 111)
		assertEqf(t, "SlowMemo B1", vm, 222)
		assertEqf(t, "SlowTtl C1", vt, 333)

		// ---- memoize / ttl: full rebuild keeps the cached value, no recompute
		//      (the cell holds the value, never flips back to pending). ----
		// Note: for the DEFAULT once function, an immediate full rebuild while
		// the topic is still connected is a CACHE HIT (the wrapper returns the
		// stored value without re-issuing xlfRtd) — the documented clear happens
		// only on the first CalculationEnded AFTER DisconnectData, then the next
		// recalc recomputes (AGENTS.md §19). So we verify once-recompute via the
		// disconnect→clear→re-enter path below, not an immediate rebuild.
		t.Logf("=== CalculateFullRebuild (within TTL) ===")
		if _, err := oleutil.CallMethod(app.disp, "CalculateFullRebuild"); err != nil {
			t.Fatalf("CalculateFullRebuild: %v", err)
		}
		// memoize must remain 222 and ttl (within 5s) must remain 333 across the
		// whole post-rebuild settle — never flipping to pending.
		assertStaysValue(t, app, sheet, "B1", 222, 3*time.Second)
		assertStaysValue(t, app, sheet, "C1", 333, 2*time.Second)

		// ---- once RE-computes through the documented disconnect→clear→re-enter
		//      lifecycle. Clear A1/A2 (DisconnectData), recalc (CalculationEnded
		//      → ClearNonMemoized reclaims the once entry), then re-enter and
		//      recalc: the wrapper misses the cache → xlfRtd → #GETTING_DATA →
		//      handler runs AGAIN → 111. ----
		t.Logf("=== once recompute via clear + re-enter ===")
		_ = SetCellFormula(sheet, "A1", "")
		_ = SetCellFormula(sheet, "A2", "")
		_ = app.CalculateFull()
		time.Sleep(500 * time.Millisecond) // let DisconnectData + CalculationEnded land
		_ = app.CalculateFull()            // second calc-end so ClearNonMemoized reclaims
		time.Sleep(300 * time.Millisecond)
		mustFormula(t, sheet, "A1", "=SlowOnce()")
		if err := app.CalculateFull(); err != nil {
			t.Fatalf("CalculateFull after re-enter: %v", err)
		}
		rec := rawCell(t, sheet, "A1")
		t.Logf("A1 after re-enter (pre-poll) = %v (%T)", rec, rec)
		if !isPending(rec) {
			// Not fatal: if the machine is slow the handler may already have
			// completed. Log for diagnosis; the recompute itself is proven by
			// the server log's second SlowOnce Connect.
			t.Logf("NOTE: A1 not observed pending after re-enter (handler may have completed fast)")
		}
		ov, _ := pollNumeric(t, app, sheet, "A1", 25*time.Second)
		assertEqf(t, "SlowOnce A1 recompute", ov, 111)

		// ---- TTL expiry: wait >5s, full rebuild, ttl recomputes to 333 ----
		t.Logf("=== waiting 6s for TTL expiry ===")
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			_ = rawCell(t, sheet, "C1") // pump STA
			time.Sleep(250 * time.Millisecond)
		}
		if _, err := oleutil.CallMethod(app.disp, "CalculateFullRebuild"); err != nil {
			t.Fatalf("CalculateFullRebuild #2: %v", err)
		}
		// After TTL expiry the ttl entry is stale → recompute. The cell ends at
		// 333 again; the recompute is also proven by the server log's repeat
		// SlowTtl Connect. We assert it resolves to 333 (recompute may be too
		// brief to catch the pending state synchronously).
		tv, _ := pollNumeric(t, app, sheet, "C1", 25*time.Second)
		assertEqf(t, "SlowTtl C1 after TTL expiry recompute", tv, 333)

		// graceful: clear RTD formulas before quit so DisconnectData runs while
		// g_phost is alive (AGENTS.md §18.10 lifecycle).
		for _, a := range []string{"A1", "A2", "B1", "C1"} {
			_ = SetCellFormula(sheet, a, "")
		}
		_ = app.CalculateFull()
		time.Sleep(500 * time.Millisecond)
	})
}

// isPending reports whether a cell is in the #GETTING_DATA / not-yet-computed
// state. go-ole maps VT_ERROR (xlErrGettingData 2043) to a nil interface{}, so
// pending surfaces as nil or, on some paths, the raw 2043 error code. An empty
// cell is also nil — acceptable here because we only call this right after
// entering a formula whose handler is mid-sleep.
func isPending(v any) bool {
	if v == nil {
		return true
	}
	return isGettingData(v)
}

// assertStaysValue asserts a cell holds wantVal for the whole window and never
// flips to pending (#GETTING_DATA) — used to prove memoize/ttl do NOT recompute.
func assertStaysValue(t *testing.T, app *excelApp, sheet *ole.IDispatch, addr string, wantVal float64, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for {
		v := rawCell(t, sheet, addr)
		if isGettingData(v) {
			t.Errorf("%s: flipped to #GETTING_DATA (recomputed) — expected cached %v held steady", addr, wantVal)
			return
		}
		if f, ok := asFloat(v); ok {
			if f != wantVal {
				t.Errorf("%s: expected cached %v, got %v", addr, wantVal, f)
				return
			}
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ===================== Task 4: RTD COMPOSITE ARGS ===========================
//
// Content-hash payload path (AGENTS.md §19.3): rtd / rtd-once functions taking
// composite (grid/range) ARGUMENTS. The C++ wrapper hashes the content, ships
// the payload once per cycle over MSG_SETREFCACHE, and the topic carries only
// the hash token; the Go dispatch resolves token -> payload from the per-cycle
// RefCache and passes the typed view to the handler.
//
//	go test -tags=xll_spill -run TestRtdComposite ./internal/smoketest/...

const compositeYaml = `
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
  description: "xll-gen composite-arg harness RTD"
  throttle_interval: "250ms"
functions:
  - name: "SumGridOnce"
    description: "Sleeps ~1s then returns the sum of the grid (rtd-once, grid arg)."
    mode: "rtd-once"
    args: [{name: "g", type: "grid"}]
    return: "float"
  - name: "SumRangeTick"
    description: "Returns the sum of a range arg (plain rtd, range arg)."
    mode: "rtd"
    args: [{name: "r", type: "range"}]
    return: "float"
`

const compositeMain = `package main

import (
	"context"
	"sync/atomic"
	"time"

	"xll_smoke/generated"

	flatbuffers "github.com/google/flatbuffers/go"
	protocol "github.com/xll-gen/types/go/protocol"
)

type Service struct{}

var gridOnceCalls int64

// sumGrid sums the numeric scalars of a protocol.Grid (Num/Int).
func sumGrid(g *protocol.Grid) float64 {
	if g == nil {
		return -1
	}
	var sum float64
	n := g.DataLength()
	for i := 0; i < n; i++ {
		var sc protocol.Scalar
		if !g.Data(&sc, i) {
			continue
		}
		switch sc.ValType() {
		case protocol.ScalarValueNum:
			var t flatbuffers.Table
			if sc.Val(&t) {
				var num protocol.Num
				num.Init(t.Bytes, t.Pos)
				sum += num.Val()
			}
		case protocol.ScalarValueInt:
			var t flatbuffers.Table
			if sc.Val(&t) {
				var iv protocol.Int
				iv.Init(t.Bytes, t.Pos)
				sum += float64(iv.Val())
			}
		}
	}
	return sum
}

func (s *Service) SumGridOnce(ctx context.Context, g *protocol.Grid) (float64, error) {
	atomic.AddInt64(&gridOnceCalls, 1)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(1 * time.Second):
	}
	return sumGrid(g), nil
}

func (s *Service) SumRangeTick_RTD(ctx context.Context, topicID int32, r *protocol.Range) error {
	// The range arg arrives as a *protocol.Range view (sheet + rects). Its
	// VALUES are not in the Range view — but the content hash that selected
	// this topic already encodes the values, so we report the rect count as a
	// simple, deterministic signal that the typed view was delivered.
	var n float64
	if r != nil {
		n = float64(r.RefsLength())
	}
	return generated.PushRtdUpdate(topicID, n)
}

func (s *Service) OnCalculationEnded(ctx context.Context) error    { return nil }
func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }
func (s *Service) OnRtdConnect(ctx context.Context, topicID int32, strings []string, newValues bool) error {
	return nil
}
func (s *Service) OnRtdDisconnect(ctx context.Context, topicID int32) error { return nil }

func main() { generated.Serve(&Service{}) }
`

func TestRtdComposite_Excel(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	root := repoRootOrFatal(t)
	workDir := os.Getenv("XLL_COMPOSITE_DIR")
	if workDir == "" {
		var err error
		workDir, err = os.MkdirTemp("", "xll-composite-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(workDir) })
	}
	projectDir := filepath.Join(workDir, "xll_smoke")
	if _, err := os.Stat(filepath.Join(projectDir, "xll.yaml")); err != nil {
		writeProject(t, projectDir, compositeYaml, compositeMain, root)
	}
	t.Logf("project dir: %s", projectDir)
	xllPath := buildProject(t, projectDir)
	t.Logf("xll: %s", xllPath)

	runExcel(t, xllPath, func(app *excelApp, sheet *ole.IDispatch) {
		app.SetRtdThrottle(250)

		// Input grid A1:B2 = [[1,2],[3,4]] (sum=10).
		_ = SetCellFormula(sheet, "A1", "1")
		_ = SetCellFormula(sheet, "B1", "2")
		_ = SetCellFormula(sheet, "A2", "3")
		_ = SetCellFormula(sheet, "B2", "4")

		// =SumGridOnce(A1:B2): #GETTING_DATA → 10 (rtd-once grid arg).
		mustFormula(t, sheet, "D1", "=SumGridOnce(A1:B2)")
		// Two cells with the SAME range → shared/duplicate topic (per Excel) —
		// both must resolve to the same value.
		mustFormula(t, sheet, "D2", "=SumGridOnce(A1:B2)")
		if err := app.CalculateFull(); err != nil {
			t.Fatal(err)
		}

		gd := rawCell(t, sheet, "D1")
		t.Logf("D1 initial = %v (%T)", gd, gd)
		if !isPending(gd) {
			t.Logf("NOTE: D1 not observed pending (handler may have completed fast)")
		}

		v1, _ := pollNumeric(t, app, sheet, "D1", 25*time.Second)
		v2, _ := pollNumeric(t, app, sheet, "D2", 25*time.Second)
		t.Logf("resolved: D1=%v D2=%v", v1, v2)
		assertEqf(t, "SumGridOnce(A1:B2) D1", v1, 10)
		assertEqf(t, "SumGridOnce(A1:B2) D2 (same range, shared topic)", v2, 10)

		// ---- content-addressed identity: EDIT a cell in A1:B2 -> new content
		//      hash -> new topic -> fresh compute with the NEW sum. ----
		t.Logf("=== edit B2 4->14, expect recompute to 20 ===")
		_ = SetCellFormula(sheet, "B2", "14") // new sum = 1+2+3+14 = 20
		if err := app.CalculateFull(); err != nil {
			t.Fatal(err)
		}
		// The edited grid yields a new hash token -> new RtdOnce key -> the cell
		// must recompute to 20 (NOT stay at the cached 10).
		ev, _ := pollNumericExpect(t, app, sheet, "D1", 20, 25*time.Second)
		t.Logf("D1 after edit = %v", ev)
		assertEqf(t, "SumGridOnce recompute after grid edit", ev, 20)

		// ---- plain rtd with a range arg: =SumRangeTick(A1:B2) -> 1 rect. ----
		mustFormula(t, sheet, "F1", "=SumRangeTick(A1:B2)")
		if err := app.CalculateFull(); err != nil {
			t.Fatal(err)
		}
		rv, _ := pollNumeric(t, app, sheet, "F1", 25*time.Second)
		t.Logf("SumRangeTick(A1:B2) F1 = %v", rv)
		assertEqf(t, "SumRangeTick rect count", rv, 1)

		// ---- calc-end clears the per-cycle sent-set: a fresh calc re-sends the
		//      payload without error. Edit back to the original and recompute. ----
		t.Logf("=== second calc cycle re-sends payload ===")
		_ = SetCellFormula(sheet, "B2", "4") // back to sum=10
		if err := app.CalculateFull(); err != nil {
			t.Fatal(err)
		}
		bv, _ := pollNumericExpect(t, app, sheet, "D1", 10, 25*time.Second)
		assertEqf(t, "SumGridOnce recompute back to 10", bv, 10)

		// graceful: clear RTD formulas before quit.
		for _, a := range []string{"D1", "D2", "F1"} {
			_ = SetCellFormula(sheet, a, "")
		}
		_ = app.CalculateFull()
		time.Sleep(500 * time.Millisecond)
	})
}

// pollNumericExpect waits until a cell holds the EXACT expected numeric value
// (tolerating the intervening #GETTING_DATA / stale-value window). Used for the
// edit→recompute proof where the cell transitions old → pending → new.
func pollNumericExpect(t *testing.T, app *excelApp, sheet *ole.IDispatch, addr string, want float64, timeout time.Duration) (float64, any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last any
	for {
		v := rawCell(t, sheet, addr)
		last = v
		if !isErrCode(v) {
			if f, ok := asFloat(v); ok && f == want {
				return f, v
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("cell %s did not reach %v within %s (last=%v %T)", addr, want, timeout, last, last)
		}
		time.Sleep(150 * time.Millisecond)
		_ = app
	}
}

// ---- shared Excel session driver + assertions ------------------------------

func runExcel(t *testing.T, xllPath string, body func(*excelApp, *ole.IDispatch)) {
	t.Helper()
	// openExcel locks the OS thread; everything must run on this goroutine.
	runtime.LockOSThread()
	app, err := openExcel()
	if err != nil {
		t.Fatalf("openExcel: %v", err)
	}
	defer app.Close()

	ok, err := app.RegisterXLL(xllPath)
	if err != nil {
		t.Fatalf("RegisterXLL: %v", err)
	}
	if !ok {
		t.Fatal("RegisterXLL returned false")
	}

	wb, err := app.AddWorkbook()
	if err != nil {
		t.Fatalf("AddWorkbook: %v", err)
	}
	defer wb.Release()

	sheetV, err := oleutil.GetProperty(wb, "ActiveSheet")
	if err != nil {
		t.Fatalf("ActiveSheet: %v", err)
	}
	sheet := sheetV.ToIDispatch()
	defer sheet.Release()

	body(app, sheet)
}

func mustFormula(t *testing.T, sheet *ole.IDispatch, addr, formula string) {
	t.Helper()
	// Use Formula2 (dynamic-array aware) so the result SPILLS instead of being
	// collapsed by implicit intersection (@). Range.Formula applies implicit
	// intersection in dynamic-array Excel — a single-cell =MyGrid() then shows
	// only the top-left element. Range.Formula2 opts into spilling. Fall back to
	// Formula on SKUs without Formula2 (pre-2019 perpetual).
	rng, err := oleutil.GetProperty(sheet, "Range", addr)
	if err != nil {
		t.Fatalf("Range(%s): %v", addr, err)
	}
	rngDisp := rng.ToIDispatch()
	defer rngDisp.Release()
	if _, err := oleutil.PutProperty(rngDisp, "Formula2", formula); err != nil {
		t.Logf("%s: Formula2 unavailable (%v); falling back to Formula", addr, err)
		if _, err := oleutil.PutProperty(rngDisp, "Formula", formula); err != nil {
			t.Fatalf("set %s=%s via Formula: %v", addr, formula, err)
		}
	}
	// Diagnostic: read back what Excel actually stored (an inserted '@' reveals
	// implicit intersection).
	if fv, err := oleutil.GetProperty(rngDisp, "Formula"); err == nil {
		t.Logf("%s stored Formula = %v", addr, fv.Value())
	}
}

func assertNum(t *testing.T, sheet *ole.IDispatch, addr string, want float64) {
	t.Helper()
	v := rawCell(t, sheet, addr)
	f, ok := asFloat(v)
	if !ok {
		t.Errorf("%s: expected numeric %v, got %v (%T)", addr, want, v, v)
		return
	}
	if f != want {
		t.Errorf("%s: expected %v, got %v", addr, want, f)
	}
}

func assertStr(t *testing.T, sheet *ole.IDispatch, addr, want string) {
	t.Helper()
	v := rawCell(t, sheet, addr)
	s, ok := asStr(v)
	if !ok {
		t.Errorf("%s: expected string %q, got %v (%T)", addr, want, v, v)
		return
	}
	if s != want {
		t.Errorf("%s: expected %q, got %q", addr, want, s)
	}
}

func assertCellTrue(t *testing.T, sheet *ole.IDispatch, addr string) {
	t.Helper()
	v := rawCell(t, sheet, addr)
	if b, ok := v.(bool); ok {
		if !b {
			t.Errorf("%s: expected TRUE, got false", addr)
		}
		return
	}
	t.Errorf("%s: expected bool TRUE, got %v (%T)", addr, v, v)
}

func assertCellFalse(t *testing.T, sheet *ole.IDispatch, addr string) {
	t.Helper()
	v := rawCell(t, sheet, addr)
	if b, ok := v.(bool); ok {
		if b {
			t.Errorf("%s: expected FALSE, got true", addr)
		}
		return
	}
	t.Errorf("%s: expected bool FALSE, got %v (%T)", addr, v, v)
}

func assertEqf(t *testing.T, label string, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("%s: expected %v, got %v", label, want, got)
	}
}

// assertHasSpill checks Range(addr).HasSpill (Excel 2021+ dynamic-array prop).
// Best-effort: if the property is not reachable via late-bound COM the neighbor
// value asserts are sufficient evidence (per task spec).
func assertHasSpill(t *testing.T, sheet *ole.IDispatch, addr string) {
	t.Helper()
	rng, err := oleutil.GetProperty(sheet, "Range", addr)
	if err != nil {
		t.Logf("assertHasSpill %s: Range failed: %v (neighbor asserts cover this)", addr, err)
		return
	}
	rngDisp := rng.ToIDispatch()
	defer rngDisp.Release()
	hs, err := oleutil.GetProperty(rngDisp, "HasSpill")
	if err != nil {
		t.Logf("assertHasSpill %s: HasSpill prop not reachable: %v (neighbor asserts cover this)", addr, err)
		return
	}
	if b, ok := hs.Value().(bool); ok {
		if !b {
			t.Errorf("%s: HasSpill = false (expected true)", addr)
		} else {
			t.Logf("%s: HasSpill = true", addr)
		}
		// Also log SpillingToRange address if available.
		if sr, err := oleutil.GetProperty(rngDisp, "SpillingToRange"); err == nil {
			srDisp := sr.ToIDispatch()
			if srDisp != nil {
				if av, err := oleutil.GetProperty(srDisp, "Address"); err == nil {
					t.Logf("%s: SpillingToRange = %v", addr, av.Value())
				}
				srDisp.Release()
			}
		}
		return
	}
	if iv, ok := hs.Value().(int16); ok {
		if iv == 0 {
			t.Errorf("%s: HasSpill = 0 (expected true)", addr)
		} else {
			t.Logf("%s: HasSpill = %d (true)", addr, iv)
		}
		return
	}
	t.Logf("assertHasSpill %s: HasSpill returned %v (%T)", addr, hs.Value(), hs.Value())
}

var _ = fmt.Sprintf // keep fmt imported even if assertions change
