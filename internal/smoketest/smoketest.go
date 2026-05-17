//go:build windows

// Package smoketest drives a generated XLL inside a real Excel process and
// asserts that one round-trip formula evaluation returns the expected value.
// It is the end-to-end counterpart to internal/regtest (which uses a C++ mock
// host instead of Excel).
//
// The harness is intentionally minimal: a single function (Add(int,int)->int)
// exercises the full register → SHM-launch → recalc → unload path. Failures
// here mean something in that path regressed; the unit suites are responsible
// for narrowing it down.
//
// Caller obligations:
//   - Run on Windows with Excel + cmake + a C++ toolchain installed.
//   - Run with build tag `xll_smoke` (see cmd/smoke_test.go).
//   - Hold the harness lock — only one smoke run per machine (fixed SHM name).
package smoketest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-ole/go-ole/oleutil"
)

// Options tunes a Run() invocation. The zero value works.
type Options struct {
	// RepoRoot is the absolute path to the xll-gen repo root (containing go.mod).
	// If empty, Run derives it from runtime.Caller.
	RepoRoot string

	// WorkDir is the directory where the test project is materialized. If
	// empty, Run creates a fresh os.MkdirTemp directory and removes it on
	// success. If set, the directory is reused (skipping init if xll.yaml
	// already exists) and never removed — handy for iterative debugging
	// (set XLL_SMOKE_KEEP_DIR to use this path).
	WorkDir string

	// FetchContentCache is forwarded to cmake as -DFETCHCONTENT_BASE_DIR.
	// Empty means cmake's default (a per-build cache, rebuilt every run).
	FetchContentCache string

	// CalcTimeout bounds how long we wait for Excel to finish recalculation
	// after writing the formula. Default 30s — generous to absorb cold-start
	// server launch + SHM bring-up.
	CalcTimeout time.Duration
}

// Case captures one formula evaluation: a label, the formula written, the
// expected int32 value, and what Excel actually returned. Used so a single
// Result can carry all three (sync/async/rtd) cases at once.
type Case struct {
	Label    string
	Formula  string
	Want     int32
	Got      any
	Err      error
	Duration time.Duration
}

// Result captures what the harness observed. Cases is non-nil iff Excel
// driving started; individual entries may still have per-case Err set.
type Result struct {
	Err        error
	ProjectDir string
	XLLPath    string
	ServerExe  string
	Cases      []Case
}

// HasFailure returns true if anything went wrong: setup failure (Err) OR any
// case-level error / value mismatch.
func (r Result) HasFailure() bool {
	if r.Err != nil {
		return true
	}
	for _, c := range r.Cases {
		if c.Err != nil {
			return true
		}
		if got, ok := asInt32(c.Got); !ok || got != c.Want {
			return true
		}
	}
	return false
}

// Run performs the full smoke test and returns its observations. It never
// panics; failures are surfaced through Result.Err.
func Run(opts Options) Result {
	if opts.CalcTimeout == 0 {
		opts.CalcTimeout = 30 * time.Second
	}
	if opts.RepoRoot == "" {
		root, err := deriveRepoRoot()
		if err != nil {
			return Result{Err: err}
		}
		opts.RepoRoot = root
	}

	workDir, cleanup, err := setupWorkDir(opts.WorkDir)
	if err != nil {
		return Result{Err: err}
	}
	defer cleanup()

	projectDir := filepath.Join(workDir, "xll_smoke")
	res := Result{ProjectDir: projectDir}

	// Stage 1: scaffold + generate. Skip init if reusing a populated WorkDir.
	if _, err := os.Stat(filepath.Join(projectDir, "xll.yaml")); err != nil {
		if err := prepareProject(projectDir, opts.RepoRoot); err != nil {
			res.Err = fmt.Errorf("prepare: %w", err)
			return res
		}
	}
	if _, err := generateCode(projectDir); err != nil {
		res.Err = fmt.Errorf("generate: %w", err)
		return res
	}

	// Stage 2: build Go server + XLL.
	serverExe, err := buildServer(projectDir, "xll_smoke")
	if err != nil {
		res.Err = fmt.Errorf("build server: %w", err)
		return res
	}
	res.ServerExe = serverExe

	xllPath, err := buildXLL(projectDir, opts.FetchContentCache)
	if err != nil {
		res.Err = fmt.Errorf("build xll: %w", err)
		return res
	}
	res.XLLPath = xllPath

	// The generated XLL's xlAutoOpen launches the server from the XLL
	// directory by default; colocate so it finds xll_smoke.exe next to
	// xll_smoke.xll.
	colocated, err := colocateServer(xllPath, serverExe)
	if err != nil {
		res.Err = fmt.Errorf("colocate server: %w", err)
		return res
	}
	res.ServerExe = colocated

	// Stage 3: drive Excel through sync / async / rtd cases.
	cases, err := driveExcel(xllPath, opts.CalcTimeout)
	res.Cases = cases
	if err != nil {
		res.Err = err
	}
	return res
}

func driveExcel(xllPath string, calcTimeout time.Duration) ([]Case, error) {
	app, err := openExcel()
	if err != nil {
		return nil, err
	}
	defer app.Close()

	ok, err := app.RegisterXLL(xllPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("RegisterXLL returned false (Excel rejected the XLL)")
	}

	// Lower RTD throttle so we don't burn ~2s of wall time waiting for the
	// first RefreshData. Excel clamps this to a minimum (often 100ms) so
	// requesting 100 is the safest fast value.
	app.SetRtdThrottle(100)

	wb, err := app.AddWorkbook()
	if err != nil {
		return nil, err
	}
	defer wb.Release()

	sheetV, err := oleutil.GetProperty(wb, "ActiveSheet")
	if err != nil {
		return nil, fmt.Errorf("ActiveSheet: %w", err)
	}
	sheet := sheetV.ToIDispatch()
	defer sheet.Release()

	cases := []Case{
		{Label: "sync", Formula: "=Add(2,3)", Want: 5},
		{Label: "async", Formula: "=AsyncAdd(7,8)", Want: 15},
		{Label: "rtd", Formula: "=RtdTick(6)", Want: 42},
	}

	cells := []string{"A1", "A2", "A3"}
	for i, c := range cases {
		if err := SetCellFormula(sheet, cells[i], c.Formula); err != nil {
			cases[i].Err = err
		}
	}
	if err := app.CalculateFull(); err != nil {
		return cases, err
	}

	for i := range cases {
		if cases[i].Err != nil {
			continue
		}
		start := time.Now()
		_, got, err := app.PollUntilNumeric(sheet, cells[i], calcTimeout)
		cases[i].Duration = time.Since(start)
		cases[i].Got = got
		cases[i].Err = err
	}

	// Disconnect any RTD topics cleanly: clear the formula so Excel calls
	// DisconnectData on our RtdServer BEFORE Quit. Without this, Quit may
	// hit ServerTerminate while ConnectData threads are still in flight,
	// stressing the §23.0 drain we just wired up.
	for _, addr := range cells {
		_ = SetCellFormula(sheet, addr, "")
	}
	_ = app.CalculateFull()

	return cases, nil
}

func setupWorkDir(provided string) (string, func(), error) {
	if provided != "" {
		if err := os.MkdirAll(provided, 0o755); err != nil {
			return "", nil, err
		}
		return provided, func() {}, nil
	}
	if keep := os.Getenv("XLL_SMOKE_KEEP_DIR"); keep != "" {
		if err := os.MkdirAll(keep, 0o755); err != nil {
			return "", nil, err
		}
		return keep, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "xll-smoke-")
	if err != nil {
		return "", nil, err
	}
	return tmp, func() { os.RemoveAll(tmp) }, nil
}

// deriveRepoRoot walks upward from this source file (smoketest.go) to find
// the xll-gen repo root (the dir containing go.mod with module
// github.com/xll-gen/xll-gen). This file lives at
// <repo>/internal/smoketest/smoketest.go.
func deriveRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	dir := filepath.Dir(file) // internal/smoketest
	dir = filepath.Dir(dir)   // internal
	dir = filepath.Dir(dir)   // repo root
	if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
		if strings.Contains(string(data), "module github.com/xll-gen/xll-gen") {
			return dir, nil
		}
	}
	return "", fmt.Errorf("xll-gen repo root not found from %s", file)
}

func asInt32(v any) (int32, bool) {
	switch x := v.(type) {
	case int32:
		return x, true
	case int16:
		return int32(x), true
	case int64:
		return int32(x), true
	case int:
		return int32(x), true
	case float64:
		return int32(x), true
	case float32:
		return int32(x), true
	}
	return 0, false
}
