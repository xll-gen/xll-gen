package assets

import (
	"strings"
	"testing"
)

// TestReportPostTeardownUseContract pins xll::ReportPostTeardownUse — the
// visibility fix for the held-reference Application.Quit() defect (AGENTS.md §20.3;
// it reached the backlog as two duplicate entries of one defect).
//
// THE STATE IT REPORTS. A confirmed-shutdown signal arrived, the full teardown ran
// (PIN, Phase 1, Phase 2, `delete g_phost`, Go server reaped), and Excel is
// DEMONSTRABLY STILL ALIVE — it just called into us. Two ways in: a COM client that
// holds the `Application` reference and calls `Application.Quit()` (delivers
// `OnBeginShutdown`, Excel survives), and unticking the COM Add-ins box
// (`OnDisconnection(ext_dm_UserClosed)` -> the full destructive Phase 2 while the
// XLL stays LOADED and its UDFs stay REGISTERED). Both leave every cell at #VALUE!
// for the rest of the session, silently. The silence IS the defect being fixed.
//
// THE LOGGER CHOICE IS THE WHOLE POINT — and it is the thing a "simplification"
// breaks. Phase 2 latches `g_isUnloading` as its first act, and
// LogInfo/LogWarn/LogError all SHORT-CIRCUIT on that flag (AGENTS.md §20.2.1
// rule 4); SAFE_LOG_* wrap those same functions behind the same flag, so they are
// double-suppressed. A report routed through any of them would be invisible in
// EXACTLY the state it exists to describe. Only LogTeardown/LogTeardownWarn bypass
// the suppression. Hence the NEGATIVE assertions below: they are the regression.
func TestReportPostTeardownUseContract(t *testing.T) {
	t.Parallel()

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}

	hdr, ok := m["include/xll_lifecycle.h"]
	if !ok {
		t.Fatalf("embedded asset include/xll_lifecycle.h not found")
	}
	if !strings.Contains(stripCppCommentsAsset(hdr), "void ReportPostTeardownUse(const char* site);") {
		t.Errorf("xll_lifecycle.h must declare void ReportPostTeardownUse(const char* site) — the " +
			"generated UDF wrappers, the RTD ConnectData gate and the ribbon command gate all call it")
	}

	src, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_lifecycle.cpp not found")
	}
	code := stripCppCommentsAsset(src)

	// Defined unqualified inside `namespace xll {`, like its neighbours
	// (SetRtdServerTerminated / HostShutdownTeardownArmed).
	defIdx := strings.Index(code, "void ReportPostTeardownUse(const char* site) {")
	if defIdx < 0 {
		t.Fatalf("src/xll_lifecycle.cpp must define xll::ReportPostTeardownUse")
	}
	// Window: to this function's own closing brace. It is indented one level (the
	// definition sits inside `namespace xll {`), so "\n    }" is the end. Cutting at
	// the namespace brace instead would let a neighbouring function's
	// LogTeardownWarn / CAS satisfy the positive assertions below — a false green in
	// a test whose entire job is to pin THIS body.
	body := code[defIdx:]
	if e := strings.Index(body, "\n    }"); e > 0 {
		body = body[:e]
	} else {
		t.Fatalf("could not find the end of ReportPostTeardownUse's body\n---\n%s", body)
	}

	// ONE-SHOT. The UDF guard runs per CELL; without a latch a recalculated sheet
	// would write one line per cell per recalculation.
	if !strings.Contains(body, "compare_exchange_strong") {
		t.Errorf("ReportPostTeardownUse must be latched by an atomic CAS so it fires exactly ONCE per "+
			"session: its call sites are per-cell\n---\n%s", body)
	}

	// It must use the logger that survives g_isUnloading.
	if !strings.Contains(body, "LogTeardownWarn(") {
		t.Errorf("ReportPostTeardownUse must log through LogTeardownWarn: Phase 2 latches g_isUnloading "+
			"as its first act and every ordinary logger short-circuits on it, so the line would be "+
			"invisible in exactly the state it describes (§20.2.1 rule 4)\n---\n%s", body)
	}
	// …and it must NOT use any of the suppressed ones. This is the regression: a
	// reviewer "restoring consistency" by switching to SAFE_LOG_WARN silently
	// deletes the whole fix while every string assertion above stays green.
	for _, forbidden := range []string{"LogWarn(", "LogError(", "LogInfo(", "SAFE_LOG_"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("ReportPostTeardownUse must NOT use %s — g_isUnloading is latched when this runs "+
				"and that logger swallows the line\n---\n%s", forbidden, body)
		}
	}

	// The message has to name the case and the recovery, or the line is a riddle.
	for _, want := range []string{"site", "reload"} {
		if !strings.Contains(body, want) {
			t.Errorf("the message must include %q — it must name the calling site and tell the user how "+
				"to recover\n---\n%s", want, body)
		}
	}

	// It must NOT try to repair anything. Resurrecting the session
	// (PrepareForFreshLoad + relaunch server + StartWorker + …) is a SEPARATE,
	// signed-off design with its own hazards (it must refuse on kUnrecoverable,
	// refuse while g_isQuiescing is latched, and be bounded to one resurrection per
	// session). This function is a report; keeping it one is what makes it safe to
	// call from a worksheet-function context.
	for _, forbidden := range []string{"PrepareForFreshLoad", "StartWorker", "new shm::DirectHost", "LaunchServer"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("ReportPostTeardownUse must only REPORT; found %q. Session resurrection is a "+
				"separate design with its own sign-off\n---\n%s", forbidden, body)
		}
	}
}

// TestPostTeardownUseReportedFromRtdAndCommandPaths: the UDF wrappers are pinned on
// the generator side (gen_post_teardown_visibility_test.go). RTD and ribbon commands
// reach the same dead state through their own doors, so they must report too.
//
// BOTH report from their STA-side ENTRY GATE, not from the `!g_phost` check inside
// their detached worker lambda. Two reasons, and both are hard rules: LogTeardown*
// must never be called from a detached thread on a forced unload (§20.2.1 rule 4's
// call-site restriction), and the in-lambda check is unreachable in this state
// anyway — the entry gate's TeardownStarted() already returned.
//
// The gate condition is `g_isUnloading && !g_phost`, i.e. Phase 2 COMPLETED, not
// merely TeardownStarted(): the latter is also true during a GENUINE quit's Phase 1,
// where an arriving ConnectData is entirely normal and reporting it would cry wolf
// on every clean shutdown.
func TestPostTeardownUseReportedFromRtdAndCommandPaths(t *testing.T) {
	t.Parallel()

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}

	for _, tc := range []struct {
		asset string
		fn    string
		site  string
	}{
		{"src/xll_rtd.cpp", "RtdServer::ConnectData(", "RtdServer::ConnectData"},
		{"src/ribbon_addin.cpp", "void SendCommandInvoke(", "SendCommandInvoke"},
	} {
		tc := tc
		t.Run(tc.asset, func(t *testing.T) {
			t.Parallel()
			src, ok := m[tc.asset]
			if !ok {
				t.Fatalf("embedded asset %s not found", tc.asset)
			}
			code := stripCppCommentsAsset(src)
			i := strings.Index(code, tc.fn)
			if i < 0 {
				t.Fatalf("%s not found in %s", tc.fn, tc.asset)
			}
			body := code[i:]

			gateIdx := strings.Index(body, "if (xll::TeardownStarted())")
			if gateIdx < 0 {
				t.Fatalf("%s lost its STA-side teardown entry gate\n---\n%s", tc.fn, body[:2000])
			}
			// Window: the entry gate's block plus a little slack.
			win := body[gateIdx:]
			if len(win) > 2000 {
				win = win[:2000]
			}
			call := "xll::ReportPostTeardownUse(\"" + tc.site + "\")"
			if !strings.Contains(win, call) {
				t.Errorf("%s's STA entry gate must call %s, so the RTD/command doors into the dead "+
					"state are as visible as the UDF one\n---\n%s", tc.fn, call, win)
			}
			// It must be conditioned on Phase 2 having COMPLETED, not on
			// TeardownStarted() alone — otherwise every clean quit logs it.
			if !strings.Contains(win, "g_isUnloading") || !strings.Contains(win, "!g_phost") {
				t.Errorf("%s must report only when Phase 2 COMPLETED (g_isUnloading latched AND g_phost "+
					"null). TeardownStarted() alone is also true during a genuine quit's Phase 1, where "+
					"this call is entirely normal\n---\n%s", tc.fn, win)
			}
		})
	}
}
