package generator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestUdfNullHostBranchReportsPostTeardownUse pins the VISIBILITY half of backlog
// line 134/191 (they are one item).
//
// THE DEFECT. A COM client that keeps its `Application` reference and calls
// `Application.Quit()` gets `OnBeginShutdown` delivered while Excel does NOT exit.
// That is a CONFIRMED-shutdown signal, so the full teardown runs — PIN, Phase 1,
// Phase 2, `delete g_phost`, Go server reaped by KILL_ON_JOB_CLOSE — and then
// Excel carries on living (measured: EXCEL.EXE survives 8/8 with the teardown
// having completed FULLY, so the ghost is not an incomplete teardown). Unticking
// the COM Add-ins box (`OnDisconnection(ext_dm_UserClosed)`) reaches the same
// state by a different door. Either way the XLL stays LOADED and its UDFs stay
// REGISTERED, so every cell then hits the `g_phost == nullptr` guard and returns
// #VALUE! for the rest of the session.
//
// WHY NOT NARROW THE SIGNAL INSTEAD. Asked and answered: it is impossible by
// construction. The ONLY authoritative "is the process really exiting"
// discriminator anywhere in the process is `lpReserved` at
// `DllMain(DLL_PROCESS_DETACH)`, which arrives strictly AFTER every point at
// which the distinction could be acted on (Phase 2 has already deleted g_phost
// and closed hJob, and DETACH runs under the loader lock where nothing may be
// undone). Office exposes no external-reference count; `Application.UserControl`
// reports who STARTED Excel; `Windows.Count`/`Visible` are 0/false in BOTH cases;
// `IRtdServer::ServerTerminate` fires on every zero-live-topic transition and is
// not a shutdown signal at all. So the fix is the CONSEQUENCE: make the state
// visible instead of silent. See AGENTS.md §20.3 point 3.
//
// WHAT THIS TEST PINS. Every rendered UDF wrapper's null-host branch must report
// it, for ALL FOUR return shapes — the branch has three different `return`
// statements and a fourth (async) that returns void, so a fix applied to one shape
// and not the others is the obvious way to get this half-right.
func TestUdfNullHostBranchReportsPostTeardownUse(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			// sync scalar -> returns &g_xlErrValue
			{Name: "SyncScalar", Return: "float", Args: []config.Arg{{Name: "a", Type: "int"}}},
			// sync grid -> returns &g_xlErrValue (LPXLOPER12) too, different body
			{Name: "SyncGrid", Return: "grid", Args: []config.Arg{{Name: "a", Type: "int"}}},
			// sync numgrid -> returns nullptr (FP12*, cannot carry an XLOPER12 sentinel)
			{Name: "SyncNumGrid", Return: "numgrid", Args: []config.Arg{{Name: "a", Type: "int"}}},
			// rtd-once numgrid -> ALSO FP12*, the shape that was once a compile BLOCKER
			{Name: "OnceNumGrid", Mode: "rtd-once", Return: "numgrid", Args: []config.Arg{{Name: "a", Type: "int"}}},
			// rtd streaming -> LPXLOPER12
			{Name: "StreamVal", Mode: "rtd", Return: "string", Args: []config.Arg{{Name: "a", Type: "int"}}},
			// async -> returns void
			{Name: "AsyncVal", Mode: "async", Async: true, Return: "float", Args: []config.Arg{{Name: "a", Type: "int"}}},
		},
		Rtd: config.RtdConfig{
			Enabled:     true,
			ProgID:      "TestProj.Rtd",
			Clsid:       "{11111111-2222-3333-4444-555555555555}",
			Description: "t",
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}

	code := stripCppComments(renderCppMain(t, cfg))

	for _, fn := range cfg.Functions {
		fn := fn
		t.Run(fn.Name, func(t *testing.T) {
			marker := fmt.Sprintf("__stdcall %s(", fn.Name)
			i := strings.Index(code, marker)
			if i < 0 {
				t.Fatalf("wrapper for %s not emitted", fn.Name)
			}
			body := code[i:]
			// Window: up to the next exported wrapper.
			if e := strings.Index(body[len(marker):], "__declspec(dllexport)"); e > 0 {
				body = body[:len(marker)+e]
			}

			guardIdx := strings.Index(body, "if (g_phost == nullptr) {")
			if guardIdx < 0 {
				t.Fatalf("the probe-unload / post-teardown null-host guard is gone from %s\n---\n%s", fn.Name, body)
			}
			call := fmt.Sprintf("xll::ReportPostTeardownUse(\"%s\");", fn.Name)
			callIdx := strings.Index(body, call)
			if callIdx < 0 {
				t.Fatalf("%s's null-host branch must call %s. Without it the mid-session destruction "+
					"described above is COMPLETELY SILENT: the native log ends at Phase 2 EXIT and every "+
					"cell shows #VALUE! with nothing to explain why\n---\n%s", fn.Name, call, body)
			}
			if callIdx < guardIdx {
				t.Errorf("%s: the report must be INSIDE the null-host branch (guard@%d call@%d) — "+
					"reporting unconditionally would fire on every healthy call\n---\n%s",
					fn.Name, guardIdx, callIdx, body)
			}

			// It must precede the branch's return, whichever of the four shapes
			// this function has: a report after the return is dead code.
			branch := body[guardIdx:]
			retIdx := strings.Index(branch, "return")
			if retIdx < 0 {
				t.Fatalf("%s's null-host branch has no return\n---\n%s", fn.Name, branch)
			}
			if callIdx-guardIdx > retIdx {
				t.Errorf("%s: the report must precede the null-host branch's return (call@%d return@%d "+
					"relative to the guard)\n---\n%s", fn.Name, callIdx-guardIdx, retIdx, branch)
			}
		})
	}

	// The one-shot latch lives in the ASSET (xll::ReportPostTeardownUse), not in the
	// template: this guard runs PER CELL, so a per-call log would be a per-cell log.
	// Pinned negatively here so nobody "improves" the template by inlining a
	// condition or a counter into every wrapper.
	if strings.Contains(code, "s_postTeardownReported") {
		t.Errorf("the one-shot latch must stay in src/xll_lifecycle.cpp, not be inlined into the " +
			"template — the guard is per-cell and there can be thousands of cells")
	}
}
