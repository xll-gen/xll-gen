package assets

import (
	"regexp"
	"strings"
	"testing"
)

// Comment stripping is load-bearing for the ORDER asserts below, not a nicety: the
// doc comment above OnDisconnection names both `xll::GracefulTeardownOnce` and the
// guard, in the opposite order to the code, so a naive index comparison would report
// a false failure. (Mirrors internal/generator's helper of the same purpose; the two
// packages cannot share test-only code.)
var (
	reLineCommentAsset  = regexp.MustCompile(`//[^\n]*`)
	reBlockCommentAsset = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

func stripCppCommentsAsset(s string) string {
	s = reBlockCommentAsset.ReplaceAllString(s, "")
	s = reLineCommentAsset.ReplaceAllString(s, "")
	return s
}

// Regression pin for the 2026-07-30 Office add-in-disconnect RE-ENTRANCY crash.
//
// THE CRASH. The generated graceful-teardown hook's first step is an explicit
// `Application.COMAddIns.Item(progId).Connect = false`. That call is correct when the
// teardown was entered from `OnBeginShutdown`, but when it is entered from
// `RibbonAddIn::OnDisconnection` it RE-ENTERS the very `mso.dll` `put_Connect(false)`
// that is already on the stack: the nested call completes Office's disconnect and
// clears the interface pointers Office caches on its `COMAddIn` object, then the outer
// `put_Connect` resumes and `Release()`s one of them unconditionally — reading a NULL
// vtable. Measured: `EXCEL.EXE` 0xC0000005 at `mso.dll+0xa1d19e`, 3/3 of the runs in
// which the nested disconnect executed, 0/6 after the fix.
//
// THE FIX has two halves in two different translation units, and BOTH must hold:
//   * `RibbonAddIn::OnDisconnection` publishes "Office is inside its own disconnect"
//     for the whole duration of the teardown it drives — which means the RAII guard
//     must be constructed BEFORE `xll::GracefulTeardownOnce`, not after (this file).
//   * the generated hook READS that flag and skips its explicit disconnect
//     (`internal/generator/gen_cancel_quit_test.go::TestCancelQuitHookSkipsReentrantDisconnect`).
//
// WHY AN ORDER ASSERT AND NOT A SUBSTRING ONE. The pre-existing hook assertion only
// checked that the string `"SetRibbonConnected(false)"` was present, so deleting the
// whole guard branch left it green. A "remove the redundant guard" cleanup could
// therefore restore a 100%-reproducible crash with `go test ./...` GREEN. Assert the
// ORDER, or the pin is worthless.
func TestOnDisconnectionMarksOfficeDisconnectBeforeTeardown(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}

	// --- The accessor must be declared unconditionally (non-ribbon builds compile
	//     this TU too, for WaitForCommandDrain, and the generated hook references the
	//     accessor from a ribbon-gated block only — but the symbol must exist). ---
	hdr, ok := m["include/com/ribbon_addin.h"]
	if !ok {
		t.Fatalf("embedded asset include/com/ribbon_addin.h not found")
	}
	hdrCode := stripCppCommentsAsset(hdr)
	declIdx := strings.Index(hdrCode, "bool OfficeDisconnectInProgress();")
	if declIdx < 0 {
		t.Fatalf("com/ribbon_addin.h must declare bool OfficeDisconnectInProgress() — the flag the " +
			"generated teardown hook reads to skip its re-entrant COMAddIns disconnect")
	}
	if ribbonGate := strings.Index(hdrCode, "#ifdef XLL_RIBBON_ENABLED"); ribbonGate >= 0 && declIdx > ribbonGate {
		t.Errorf("OfficeDisconnectInProgress() must be declared OUTSIDE #ifdef XLL_RIBBON_ENABLED "+
			"(decl@%d gate@%d): ribbon_addin.cpp is compiled in non-ribbon builds too", declIdx, ribbonGate)
	}

	src, ok := m["src/ribbon_addin.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_addin.cpp not found")
	}
	code := stripCppCommentsAsset(src)

	// --- The counter + accessor exist, and the counter is a DEPTH, not a bool. ---
	if !strings.Contains(code, "std::atomic<int> g_officeDisconnectDepth{0}") {
		t.Errorf("ribbon_addin.cpp must define `static std::atomic<int> g_officeDisconnectDepth{0}` — a " +
			"DEPTH counter, not a bool: an RAII guard that stored/restored a bool could clear the flag " +
			"while an outer OnDisconnection is still on the stack")
	}
	if !strings.Contains(code, "bool OfficeDisconnectInProgress() {") {
		t.Errorf("ribbon_addin.cpp must define OfficeDisconnectInProgress()")
	}

	// --- THE ORDER ASSERT: guard constructed BEFORE GracefulTeardownOnce. ---
	odIdx := strings.Index(code, "HRESULT __stdcall RibbonAddIn::OnDisconnection(")
	if odIdx < 0 {
		t.Fatalf("RibbonAddIn::OnDisconnection not found in ribbon_addin.cpp")
	}
	body := code[odIdx:]
	if e := strings.Index(body, "HRESULT __stdcall RibbonAddIn::OnAddInsUpdate("); e > 0 {
		body = body[:e]
	}

	guardIdx := strings.Index(body, "DisconnectDepthGuard")
	teardownIdx := strings.Index(body, "xll::GracefulTeardownOnce(")
	if teardownIdx < 0 {
		t.Fatalf("OnDisconnection must still drive xll::GracefulTeardownOnce\n---\n%s", body)
	}
	if guardIdx < 0 {
		t.Fatalf("OnDisconnection must publish \"Office is inside its own add-in disconnect\" for the "+
			"duration of the teardown it drives (a DisconnectDepthGuard RAII object). Without it the "+
			"generated hook re-enters mso.dll's put_Connect and Excel dies with 0xC0000005 at "+
			"mso.dll+0xa1d19e\n---\n%s", body)
	}
	if guardIdx > teardownIdx {
		t.Errorf("the DisconnectDepthGuard must be constructed BEFORE xll::GracefulTeardownOnce "+
			"(guard@%d teardown@%d): the hook reads the flag from INSIDE that call, so a guard "+
			"declared after it publishes nothing\n---\n%s", guardIdx, teardownIdx, body)
	}

	// The guard must actually be an OBJECT (RAII), not a bare increment: the flag has
	// to be cleared on exception unwind too.
	declRe := "} disconnectDepth;"
	if !strings.Contains(body, declRe) {
		t.Errorf("the depth guard must be declared as a scoped OBJECT so the decrement runs on normal "+
			"AND exception unwind (expected a `%s` declaration)\n---\n%s", declRe, body)
	}
	// ...and it must increment on construction / decrement on destruction.
	gIdx := strings.Index(body, "struct DisconnectDepthGuard {")
	if gIdx < 0 {
		t.Fatalf("DisconnectDepthGuard definition not found\n---\n%s", body)
	}
	gBody := body[gIdx:]
	if e := strings.Index(gBody, "} disconnectDepth;"); e > 0 {
		gBody = gBody[:e]
	}
	if !strings.Contains(gBody, "fetch_add(1") || !strings.Contains(gBody, "fetch_sub(1") {
		t.Errorf("DisconnectDepthGuard must fetch_add on construction and fetch_sub on destruction\n---\n%s", gBody)
	}
}
