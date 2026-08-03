package assets

import (
	"regexp"
	"strings"
	"testing"
)

var reEmptyLogPathIf = regexp.MustCompile(`if\s*\(\s*g_logPath\.empty\(\)\s*\)\s*`)

// emptyLogPathBranch returns the BODY of xll_log.cpp's
// `if (g_logPath.empty()) …` branch, given comment-stripped source, in whatever
// brace style it happens to be written in (braced block, or a single statement
// with no braces at all).
//
// It exists because the assertion it feeds used to be
// `!strings.Contains(code, "if (g_logPath.empty()) return;")` — a check that says
// "the drop-everything branch is gone" but only recognises ONE spelling of it.
// Re-writing the drop as `if (g_logPath.empty()) { return; }` restored the exact
// defect with the assertion still green. Reading the branch body and requiring
// the fallback INSIDE it is the property that was meant.
func emptyLogPathBranch(code string) (string, bool) {
	loc := reEmptyLogPathIf.FindStringIndex(code)
	if loc == nil {
		return "", false
	}
	rest := code[loc[1]:]
	if rest == "" {
		return "", false
	}
	if rest[0] != '{' {
		// Braceless single-statement branch.
		if i := strings.IndexByte(rest, ';'); i >= 0 {
			return rest[:i+1], true
		}
		return "", false
	}
	depth := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[:i+1], true
			}
		}
	}
	return "", false
}

// Comment stripping is load-bearing for the ORDER asserts below, not a nicety: the
// doc comment above OnDisconnection names both `xll::GracefulTeardownOnce` and the
// guard, in the opposite order to the code, so a naive index comparison would report
// a false failure. (Mirrors internal/generator's helper of the same purpose; the two
// packages cannot share test-only code.)
//
// WHY A SCANNER AND NOT TWO REGEX PASSES (fixed 2026-08-03). This used to be
// `reBlockComment.ReplaceAll("")` followed by `reLineComment.ReplaceAll("")`, and
// that is wrong on real source. `src/ribbon_connect.cpp`'s header comment contains
// the literal `file(GLOB src/*.cpp)`, which the block-comment regex reads as an
// OPENING `/*`. For as long as no `*/` followed it anywhere in the file the pass was
// a silent no-op — but the moment a `/*allowBounce=*/` named-argument comment was
// added 275 lines further down, the non-greedy match swallowed everything between
// the two and every assertion in ribbon_connect_cpp_test.go failed at once. That
// failure was loud. The same swallow inside an ORDER assert is NOT: two markers that
// no longer exist in the stripped text simply report `-1 < -1`… or, worse, survive
// in a smaller window and read as correctly ordered. A single left-to-right scan
// that also understands string/char literals cannot mis-pair a delimiter that is
// itself inside a comment or a literal.
func stripCppCommentsAsset(s string) string { return stripCppCommentsScan(s) }

// stripCppCommentsScan walks s once, dropping // line comments and /* */ block
// comments while copying string and character literals through verbatim (so a
// "//" or "/*" inside a literal is not mistaken for a comment opener).
func stripCppCommentsScan(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			} else {
				i = len(s) // unterminated block comment: drop the remainder
			}
		case s[i] == '"' || s[i] == '\'':
			quote := s[i]
			b.WriteByte(s[i])
			i++
			for i < len(s) {
				c := s[i]
				b.WriteByte(c)
				i++
				if c == '\\' && i < len(s) {
					b.WriteByte(s[i])
					i++
					continue
				}
				if c == quote || c == '\n' {
					break
				}
			}
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
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
//   - `RibbonAddIn::OnDisconnection` publishes "Office is inside its own disconnect"
//     for the whole duration of the teardown it drives — which means the RAII guard
//     must be constructed BEFORE `xll::GracefulTeardownOnce`, not after (this file).
//   - the generated hook READS that flag and skips its explicit disconnect
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

// TestOnConnectionRefusesWithoutConnectContext pins the HAZARD GUARD that backlog
// line 120's fix opens (2026-08-03).
//
// That fix stopped the graceful teardown from deleting
// HKCU\Software\Microsoft\Office\Excel\Addins\<progId> and the ribbon's HKCU COM
// registration, so the COM Add-ins dialog row and a live InprocServer32 now
// SURVIVE the session that created them. That is the whole point — a user who
// unticks the box has to be able to tick it back — but it also means Office can
// COM-activate RibbonAddIn in a session where xlAutoOpen NEVER RAN: no ribbon XML,
// no images, no SHM host, no Go server. LoadBehavior is 0 on a fresh install so
// nothing autoloads at startup, but one tick in the dialog is enough to get there
// — and since 2026-08-03 RegisterOfficeAddinKey preserves the LoadBehavior=3
// Office records for that tick (registry_addin_key_cpp_test.go), so after one tick
// Office may also activate us during its own startup.
//
// OnConnection was a bare `return S_OK`, so that tick produced a half add-in that
// looks connected and does nothing. It must now consult
// xll::ribbon::ConnectContextPublished() and REFUSE — a clean failure Office can
// report, instead of a silent zombie.
//
// A BRANCH-STRUCTURE assert, like its neighbour above and for the same reason: a
// substring check for "ConnectContextPublished" would stay green if the refusal
// were softened back to S_OK.
func TestOnConnectionRefusesWithoutConnectContext(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}

	// --- The predicate must be declared in the connect header (its definition is
	//     the file-static ContextReady in src/ribbon_connect.cpp). ---
	hdr, ok := m["include/com/ribbon_connect.h"]
	if !ok {
		t.Fatalf("embedded asset include/com/ribbon_connect.h not found")
	}
	if !strings.Contains(stripCppCommentsAsset(hdr), "bool ConnectContextPublished();") {
		t.Errorf("com/ribbon_connect.h must declare bool ConnectContextPublished() — the readiness " +
			"predicate RibbonAddIn::OnConnection refuses on")
	}

	cpp, ok := m["src/ribbon_connect.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_connect.cpp not found")
	}
	cppCode := stripCppCommentsAsset(cpp)
	defIdx := strings.Index(cppCode, "bool ConnectContextPublished()")
	if defIdx < 0 {
		t.Fatalf("src/ribbon_connect.cpp must define ConnectContextPublished()")
	}
	// It must be the EXISTING readiness check, not a second, drifting copy of the
	// field list.
	defBody := cppCode[defIdx:]
	if e := strings.Index(defBody, "\n}"); e > 0 {
		defBody = defBody[:e]
	}
	if !strings.Contains(defBody, "ContextReady(") {
		t.Errorf("ConnectContextPublished() must delegate to the file-static ContextReady() — a second "+
			"hand-written field list would drift from ConnectContext\n---\n%s", defBody)
	}

	src, ok := m["src/ribbon_addin.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_addin.cpp not found")
	}
	code := stripCppCommentsAsset(src)

	ocIdx := strings.Index(code, "HRESULT __stdcall RibbonAddIn::OnConnection(")
	if ocIdx < 0 {
		t.Fatalf("RibbonAddIn::OnConnection not found in ribbon_addin.cpp")
	}
	body := code[ocIdx:]
	if e := strings.Index(body, "HRESULT __stdcall RibbonAddIn::OnDisconnection("); e > 0 {
		body = body[:e]
	}

	gateIdx := strings.Index(body, "xll::ribbon::ConnectContextPublished()")
	if gateIdx < 0 {
		t.Fatalf("RibbonAddIn::OnConnection must consult xll::ribbon::ConnectContextPublished(). "+
			"Without it, ticking the COM Add-ins row (which now SURVIVES a disable, by design) in a "+
			"session where xlAutoOpen never ran COM-activates a half add-in: no ribbon XML, no host, "+
			"no server\n---\n%s", body)
	}
	// The refusal must be a FAILURE hresult, and it must be reached from the
	// not-published branch — i.e. it precedes the success return.
	failIdx := strings.Index(body, "E_FAIL")
	okIdx := strings.Index(body, "return S_OK;")
	if failIdx < 0 {
		t.Errorf("OnConnection must return a FAILURE HRESULT (E_FAIL) when the context was never "+
			"published — returning S_OK is exactly the half-add-in this guard exists to refuse\n---\n%s", body)
	}
	if okIdx < 0 {
		t.Errorf("OnConnection must still return S_OK on the normal path\n---\n%s", body)
	}
	if failIdx >= 0 && okIdx >= 0 && failIdx > okIdx {
		t.Errorf("the E_FAIL refusal must precede the success return (fail@%d ok@%d): a refusal after "+
			"`return S_OK` is dead code\n---\n%s", failIdx, okIdx, body)
	}
	if !strings.Contains(body, "SAFE_LOG_WARN") {
		t.Errorf("the refusal must log once at WARN, or a user who ticked the box gets a silent "+
			"no-op with nothing to diagnose\n---\n%s", body)
	}

	// …AND THE WARN MUST HAVE A SINK. (2026-08-03. The rationale above used to
	// stop at "it logs at WARN", and that was FALSE for this guard.)
	//
	// SAFE_LOG_WARN -> LogWarn -> WriteLogUnconditional, which opened with
	// `if (g_logPath.empty()) return;`. g_logPath is written only by InitLog,
	// reached only from InitNativeLogging, called only from xlAutoOpen — and the
	// entire premise of this branch is that xlAutoOpen NEVER RAN. So the refusal
	// line was discarded exactly in the state the guard covers, and no assertion
	// over THIS file could have caught it. The fix is in the logger: it falls back
	// to the debug-output channel while there is no log file. Asserted here, in the
	// test that owns the requirement, because src/xll_log.cpp has no other reason
	// to keep that branch and deleting it would silently re-break this guard.
	// Behaviour is covered by TestNativeLogDebugSinkBehavior.
	//
	// STRUCTURAL, NOT TEXTUAL (corrected 2026-08-03). The first version of this
	// check was `!Contains(code, "if (g_logPath.empty()) return;")`, which a
	// brace-reformatted `if (g_logPath.empty()) { return; }` walks straight past —
	// the defect back, the assertion green. It now reads the branch BODY and
	// requires the fallback call to be in it, which is the property the paragraph
	// above claims.
	logSrc, ok := m["src/xll_log.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_log.cpp not found")
	}
	branch, found := emptyLogPathBranch(stripCppCommentsAsset(logSrc))
	if !found {
		t.Fatalf("src/xll_log.cpp no longer has an `if (g_logPath.empty())` branch in " +
			"WriteLogUnconditional; this guard's WARN depends on what that branch does, so this " +
			"test cannot be left to guess")
	}
	if !strings.Contains(branch, "WriteDebugOutputFallback(") {
		t.Errorf("src/xll_log.cpp drops every line while g_logPath is empty, so this refusal's WARN "+
			"is silently discarded — the log file is opened by InitNativeLogging inside xlAutoOpen, "+
			"and this branch runs precisely when xlAutoOpen never ran. The logger must fall back to "+
			"the debug-output channel (see WriteDebugOutputFallback / TestNativeLogDebugSinkContract).\n"+
			"the branch as written:\n%s", branch)
	}
	// The comment above the guard has to say WHERE to read the line. Pointing a
	// user at <proj>_native.log — which in this session does not exist — is the
	// misdirection the original comment shipped with.
	ocRawIdx := strings.Index(src, "HRESULT __stdcall RibbonAddIn::OnConnection(")
	if ocRawIdx < 0 {
		t.Fatalf("RibbonAddIn::OnConnection not found in the raw ribbon_addin.cpp")
	}
	rawComment := src[:ocRawIdx]
	if i := strings.LastIndex(rawComment, "\n}"); i > 0 {
		rawComment = rawComment[i:]
	}
	if !strings.Contains(rawComment, "OutputDebugString") {
		t.Errorf("the comment above OnConnection must name the sink the refusal line actually "+
			"reaches (OutputDebugStringW, because there is no log file in this state)\n---\n%s", rawComment)
	}

	// The late-bound IDispatch path must not bypass the refusal: Invoke used to
	// blanket-return S_OK for all five extensibility DISPIDs, OnConnection
	// included. A host that late-binds would then connect regardless.
	invIdx := strings.Index(code, "HRESULT __stdcall RibbonAddIn::Invoke(")
	if invIdx < 0 {
		t.Fatalf("RibbonAddIn::Invoke not found")
	}
	inv := code[invIdx:]
	if e := strings.Index(inv, "\n}\n"); e > 0 {
		inv = inv[:e]
	}
	extIdx := strings.Index(inv, "kDispIdExtBase")
	if extIdx < 0 {
		t.Fatalf("Invoke must still handle the late-bound extensibility DISPIDs\n---\n%s", inv)
	}
	if !strings.Contains(inv, "OnConnection(") {
		t.Errorf("Invoke must route the late-bound OnConnection DISPID to OnConnection() rather than "+
			"blanket-returning S_OK, or a host that late-binds _IDTExtensibility2 bypasses the "+
			"ConnectContextPublished() refusal entirely\n---\n%s", inv)
	}
}
