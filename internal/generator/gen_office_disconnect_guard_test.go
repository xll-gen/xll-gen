package generator

import (
	"strings"
	"testing"
)

// TestHookSkipsReentrantOfficeDisconnect pins the GENERATOR half of the 2026-07-30
// Office add-in-disconnect RE-ENTRANCY crash fix. The asset half (the depth guard in
// RibbonAddIn::OnDisconnection) is pinned by
// internal/assets/office_disconnect_guard_cpp_test.go.
//
// THE CRASH. GracefulComTeardownHook's step (0) is an explicit
// `Application.COMAddIns.Item(progId).Connect = false` (SetRibbonConnected(false)).
// Entered from OnBeginShutdown that is correct and wanted. Entered from
// RibbonAddIn::OnDisconnection it RE-ENTERS the same mso.dll put_Connect(false) that
// is already on the stack: the nested call finishes Office's disconnect and clears the
// interface pointers Office caches on its COMAddIn object, then the OUTER put_Connect
// resumes and Release()s one of them unconditionally — a NULL vtable read.
// EXCEL.EXE 0xC0000005 at mso.dll+0xa1d19e, 3/3 of the runs in which the nested
// disconnect executed, 0/6 after the fix.
//
// WHY THIS TEST EXISTS AT ALL (review MED #1). The pre-existing pin in
// gen_cancel_quit_test.go only asserted that the STRING "SetRibbonConnected(false)"
// appears somewhere in the hook body. Collapsing the guard back to a bare
// `if (!SetRibbonConnected(false))` therefore left `go test ./...` GREEN while
// restoring a 100%-reproducible Excel crash. A substring assert cannot pin this fix;
// the ORDER and the BRANCH STRUCTURE are the fix. Hence:
//
//   1. `xll::ribbon::OfficeDisconnectInProgress()` must appear BEFORE
//      `SetRibbonConnected(false)` in the hook body, and
//   2. `SetRibbonConnected(false)` must sit in the NOT-taken branch of that condition
//      (i.e. after an `} else`), so the skip is what happens when the flag is set.
func TestHookSkipsReentrantOfficeDisconnect(t *testing.T) {
	t.Parallel()

	// A ribbon project (a ribbon requires >=1 command, AGENTS.md 18.11) — the only
	// shape in which the teardown hook, and therefore this guard, is emitted at all.
	src := renderCppMain(t, ribbonConnectCfg())
	code := stripCppComments(src)

	hookIdx := strings.Index(code, "static void GracefulComTeardownHook(bool revokeRtdClassObject)")
	if hookIdx < 0 {
		t.Fatalf("GracefulComTeardownHook not emitted for a ribbon+commands+RTD project")
	}
	hook := code[hookIdx:]
	if e := strings.Index(hook, "\nextern \"C\""); e > 0 {
		hook = hook[:e]
	}

	guardIdx := strings.Index(hook, "xll::ribbon::OfficeDisconnectInProgress()")
	discIdx := strings.Index(hook, "SetRibbonConnected(false)")

	if discIdx < 0 {
		t.Fatalf("the hook must still perform the explicit COMAddIns disconnect on the "+
			"OnBeginShutdown path — dropping it entirely loses the early release Excel needs "+
			"there\n---\n%s", hook)
	}
	if guardIdx < 0 {
		t.Fatalf("the hook must consult xll::ribbon::OfficeDisconnectInProgress() before its explicit "+
			"COMAddIns disconnect. Without that check, a teardown entered from "+
			"RibbonAddIn::OnDisconnection re-enters mso.dll's put_Connect and Excel dies with "+
			"0xC0000005 at mso.dll+0xa1d19e (3/3 measured)\n---\n%s", hook)
	}
	if guardIdx > discIdx {
		t.Errorf("xll::ribbon::OfficeDisconnectInProgress() must be tested BEFORE "+
			"SetRibbonConnected(false) (guard@%d disconnect@%d): checking afterwards cannot prevent "+
			"the re-entrant call\n---\n%s", guardIdx, discIdx, hook)
	}

	// The disconnect must be in the NOT-taken branch: between the guard and the
	// disconnect there must be an `} else`, i.e. the skip is the guarded outcome.
	between := hook[guardIdx:discIdx]
	if !strings.Contains(between, "} else") {
		t.Errorf("SetRibbonConnected(false) must sit in the ELSE branch of the "+
			"OfficeDisconnectInProgress() test, so that a set flag SKIPS it. Found no `} else` "+
			"between the two — the disconnect may be running unconditionally\n---\n%s", between)
	}
	// And the guarded (skip) branch must not itself disconnect.
	skipBranch := between
	if i := strings.Index(skipBranch, "} else"); i >= 0 {
		skipBranch = skipBranch[:i]
	}
	if strings.Contains(skipBranch, "SetRibbonConnected") {
		t.Errorf("the OfficeDisconnectInProgress() branch must NOT disconnect — that is the whole "+
			"point of the skip\n---\n%s", skipBranch)
	}

	// The skip must be observable: this is a silent behaviour change otherwise, and the
	// real-Excel verification counted these log lines (0/8 on the window-close path,
	// which is how we know that path is unaffected).
	if !strings.Contains(skipBranch, "SAFE_LOG") {
		t.Errorf("the skip branch must log, so it can be counted in real-Excel verification\n---\n%s", skipBranch)
	}

	// Order sanity against the rest of the teardown: the guarded disconnect still has to
	// precede the revoke/unregister steps (com/ribbon_addin.h teardown-order contract).
	revokeIdx := strings.Index(hook, "CoRevokeClassObject(g_ribbonCookie)")
	if revokeIdx < 0 || discIdx > revokeIdx {
		t.Errorf("the (possibly skipped) disconnect must still precede CoRevokeClassObject "+
			"(disconnect@%d revoke@%d)", discIdx, revokeIdx)
	}
}
