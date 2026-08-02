package assets

import (
	"strings"
	"testing"
)

// The two pure-logic pieces of the ribbon "temp-workbook bounce" moved out of
// internal/templates/xll_main.cpp.tmpl into include/com/scratch_book.h +
// src/scratch_book.cpp (2026-08-02): naming the active workbook, and
// suppressing/restoring the Application event flags around the scratch close.
// This file is where the BODY invariants that used to be greps over the rendered
// template now live.
//
// WHERE EACH OLD ASSERTION WENT:
//
//	internal/generator/gen_ribbon_connect_test.go::TestXllMainRibbonDeferredConnect
//	  GetActiveWorkbookName / xlfGetDocument(88) / PascalToWString
//	                                                 -> TestScratchBookNamesTheActiveWorkbook
//	  scratchName / activeNow close-by-identity      -> stayed in the generator
//	                                                    (the comparison is in the
//	                                                    ribbon.bounce-branched
//	                                                    bounce, which is template code)
//	internal/generator/gen_ribbon_bounce_test.go::TestRibbonBounceFullSuppressesEventsAroundClose
//	  ScratchCloseEventSuppressor type + dtor        -> TestScratchCloseSuppressorRestoreContract
//	  the file-static pending record + AddRef        -> TestScratchCloseSuppressorRestoreContract
//	  armed-flag-gated restore of the ORIGINALS      -> TestScratchCloseSuppressorRestoreContract
//	  the logged (not swallowed) failed restore      -> TestScratchCloseSuppressorRestoreContract
//
// What stayed in internal/generator is the WIRING: which bounce modes instantiate
// the guard, that the guard is constructed BEFORE xlcFileClose, and that
// xlAutoOpen replays the idempotent restore after the SEH block.

// TestScratchBookNamesTheActiveWorkbook pins the HIGH (data-loss) hardening of the
// close-by-identity bounce: the scratch book's name is captured via the XLM macro
// function GET.DOCUMENT(88) (xlfGetDocument, selector 88 = active workbook name)
// so the close can be refused if a real user document became active in between.
func TestScratchBookNamesTheActiveWorkbook(t *testing.T) {
	t.Parallel()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/com/scratch_book.h"]
	if !ok {
		t.Fatalf("embedded asset include/com/scratch_book.h not found")
	}
	code, ok := m["src/scratch_book.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/scratch_book.cpp not found")
	}

	if !strings.Contains(hdr, "std::wstring GetActiveWorkbookName();") {
		t.Errorf("com/scratch_book.h must declare std::wstring GetActiveWorkbookName()")
	}
	for _, want := range []string{
		// Selector 88 is verified against types/include/types/xlcall.h
		// (#define xlfGetDocument 188) and the XLM GET.DOCUMENT reference.
		"xll::CallExcel(xlfGetDocument, xName, 88)",
		// The result is an Excel-allocated Pascal string.
		"PascalToWString(xName.get()->val.str)",
		// Held in a ScopedXLOPER12Result so it is xlFree'd.
		"ScopedXLOPER12Result xName;",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/scratch_book.cpp missing %q", want)
		}
	}
	// An indeterminate name must yield an EMPTY string, never a guess: the caller
	// treats empty as "refuse the close", which is the whole data-loss guard.
	if strings.Count(code, "return std::wstring();") < 2 {
		t.Errorf("GetActiveWorkbookName must return an empty string on BOTH failure shapes "+
			"(call rejected, and a non-string / null result) — the caller reads empty as "+
			"\"do not close\":\n%s", code)
	}
}

// TestScratchCloseSuppressorRestoreContract pins the §20.3 SEH restore contract.
//
// The guard sets Application.EnableEvents=false + DisplayAlerts=false around the
// scratch-book close so third-party WorkbookBeforeClose hooks (document
// classification / DLP add-ins with modal prompts) cannot fire mid-xlAutoOpen. The
// RESTORE is the load-bearing half: leaving EnableEvents=false would silence every
// add-in's Workbook events for the rest of the session. An async SEH fault inside
// xlcFileClose — the very scenario the guard defends against — unwinds through
// XLL_SAFE_BLOCK's __except WITHOUT running C++ destructors on /EHsc, so the
// suppression state must NOT live in the guard object: it lives in a file-static
// record and the restore is an idempotent free function that the caller replays.
func TestScratchCloseSuppressorRestoreContract(t *testing.T) {
	t.Parallel()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr := m["include/com/scratch_book.h"]
	code := m["src/scratch_book.cpp"]

	for _, want := range []string{
		"struct ScratchCloseEventSuppressor {",
		"explicit ScratchCloseEventSuppressor(IDispatch* app);",
		"void RestorePendingEventSuppression();",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("com/scratch_book.h missing %q", want)
		}
	}

	for _, want := range []string{
		// The record is file-static (not a member) so it outlives the frame on the
		// destructor-skipped SEH path...
		"PendingEventSuppression g_pendingSuppression;",
		// ...and holds an AddRef'd Application for the same reason.
		"p.app->AddRef();",
		// Both properties are read before being flipped, so the ORIGINALS can be
		// replayed (never a blind =true, which would clobber an automation host
		// that had them off on purpose).
		`xll::com::GetProperty(p.app, L"EnableEvents", &p.oldEnableEvents)`,
		`xll::com::GetProperty(p.app, L"DisplayAlerts", &p.oldDisplayAlerts)`,
		// Restore is gated on the armed flags, property by property, so partial
		// construction is covered.
		"if (p.armedEvents) {",
		"if (p.armedAlerts) {",
		`xll::com::Invoke(p.app, L"EnableEvents", DISPATCH_PROPERTYPUT, { p.oldEnableEvents }, nullptr)`,
		`xll::com::Invoke(p.app, L"DisplayAlerts", DISPATCH_PROPERTYPUT, { p.oldDisplayAlerts }, nullptr)`,
		// A failed restore is LOGGED, not swallowed.
		"failed to restore Application.EnableEvents",
		"failed to restore Application.DisplayAlerts",
		// The destructor routes through the same idempotent restore (normal path);
		// the caller replays it after the SAFE_BLOCK (SEH path).
		"ScratchCloseEventSuppressor::~ScratchCloseEventSuppressor() { RestorePendingEventSuppression(); }",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("src/scratch_book.cpp missing %q", want)
		}
	}

	// The record must be cleared even when NOTHING was flipped, or the AddRef
	// lingers for the life of the process.
	if !strings.Contains(code, "if (!p.armedEvents && !p.armedAlerts) {") {
		t.Errorf("the constructor must clear the record when neither property was flipped " +
			"(otherwise the AddRef'd Application leaks)")
	}
	// Restore must release the AddRef and null the pointer so a replay is a no-op.
	if !strings.Contains(code, "p.app->Release();") || !strings.Contains(code, "p.app = nullptr;") {
		t.Errorf("RestorePendingEventSuppression must release + null the AddRef'd Application " +
			"so the belt-and-braces replay is idempotent")
	}
	// The TU is ribbon-gated: file(GLOB src/*.cpp) compiles it into non-ribbon
	// projects too, which do not link the COM libraries it needs.
	if !strings.Contains(code, "#ifdef XLL_RIBBON_ENABLED") {
		t.Errorf("src/scratch_book.cpp must gate its body on #ifdef XLL_RIBBON_ENABLED")
	}
}
