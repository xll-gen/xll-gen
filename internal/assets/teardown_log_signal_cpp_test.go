package assets

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// THE DEFECT THESE TESTS PIN (2026-08-03)
//
// `OnBeginShutdown` is a promise about the ADD-IN's shutdown, not the PROCESS's.
// Measured 2026-08-03 on a showcase release build, 3 of 3 rounds, four clauses
// measured separately: (A) `OnBeginShutdown` was delivered and the teardown ran,
// (B) EXCEL.EXE survived the Quit, (C) the add-in stopped answering (`=Add(2,3)`
// gave 5 before and 0x800A03EC after), (D) the Go server was reaped (1 -> 0).
//
// The DECISION (user, 2026-08-03) was to document it and improve the diagnostic,
// NOT to change teardown behaviour: the blast radius is COM automation clients (a
// user closing by the X button or File > Exit is unaffected), and every behavioural
// alternative rewrites an ordering that v0.8.41 validated at 0/40 inside the code
// region that produced two separate 100%-reproducible Excel crashes. AGENTS.md
// §20.3 owns the full record.
//
// So the only shippable artefacts are DOCUMENTATION and a LOG LINE — and this file
// is honest about what that means: THESE ARE SOURCE-LEVEL ASSERTIONS. They cannot
// observe a teardown; they assert that the misleading sentence is gone, that the
// wording branches on the one fact that is knowable at that instant, and that the
// caveat is present. Nothing more is claimable at this level, and dressing it up as
// a behavioural gate would be worse than saying so.
// ---------------------------------------------------------------------------

var reIfIsHostShutdownBlock = regexp.MustCompile(`if\s*\(\s*isHostShutdown\s*\)\s*\{`)

// blockFrom returns s[i:] up to and including the brace that closes the block
// starting at the '{' at or after i. Returns "" if the braces do not balance.
func blockFrom(s string, i int) string {
	open := strings.IndexByte(s[i:], '{')
	if open < 0 {
		return ""
	}
	start := i + open
	depth := 0
	for j := start; j < len(s); j++ {
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : j+1]
			}
		}
	}
	return ""
}

// dropStringLiterals blanks the CONTENT of every "..." literal, leaving the quotes.
// The call-shape assertion below has to look at code, not at prose: the log message
// itself contains "Application.Quit()" and "(1)"/"(2)", which a naive scan for
// `identifier(` would happily read as function calls.
func dropStringLiterals(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '"' {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteString(`""`)
		i++
		for i < len(s) {
			if s[i] == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if s[i] == '"' {
				i++
				break
			}
			i++
		}
	}
	return b.String()
}

var reCallName = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_:]*)\s*\(`)

// cppNonCallKeywords are `name (` shapes that the regex above cannot tell from a
// call but that are not one. Listed rather than inferred so that a NEW keyword
// showing up in this region is a failure, not a silent pass.
var cppNonCallKeywords = map[string]bool{
	"if": true, "else": true, "for": true, "while": true, "do": true,
	"switch": true, "return": true, "catch": true, "sizeof": true,
}

// permittedInAnnounceRegion is an ALLOWLIST, and the allowlist is the whole point.
//
// The first version of this test scanned only the two branch BODIES. That is the
// wrong window: a survival probe does not have to live inside an arm. One line
// ABOVE the `if`, its result stored in a bool the branch reads, is the natural way
// someone would "resolve the ambiguity", and it left this test GREEN (demonstrated
// before this rewrite). So the window is now the entire announce region — the
// function's opening brace through `BeginQuiesce(` — and everything callable in it
// must be named here with a reason.
//
// Everything in this map predates the 2026-08-03 rework except LogInfo's second
// call site. Nothing may be added without deciding, in §20.3, that a new call
// belongs in a region whose ordering is deliberately frozen.
var permittedInAnnounceRegion = map[string]string{
	"xll::GracefulTeardownOnce": "the function's own signature opens the window",
	"compare_exchange_strong":   "the single-shot CAS that guards the whole body",
	"LogInfo":                   "the announcement itself — the only thing this rework ships",
	"PinModuleToPreventUnmap":   "§20.2.1's image pin; pre-existing, and deliberately before BeginQuiesce",
}

// TestGracefulTeardownLogNamesTheSignalNotTheOutcome pins the reworded opening log
// line of xll::GracefulTeardownOnce.
//
// WHAT WAS WRONG. The line read `"GracefulTeardownOnce: confirmed shutdown —
// beginning teardown..."`. A reader takes "confirmed shutdown" to mean "Excel is
// exiting" — and in the Application.Quit()-with-held-reference case that sentence is
// stamped over a session that goes on living, with nothing else in the log to tell
// the two apart. A teardown log that cannot distinguish "the host is exiting" from
// "the host is fine and your add-in just died" is the diagnostic gap.
//
// WHY THE FIX IS A BRANCH AND NOT A BETTER SENTENCE. Which case it is CANNOT be
// determined at that point in the function — the only authoritative discriminator in
// the process is `lpReserved` at DLL_PROCESS_DETACH, which arrives strictly after
// everything the teardown does (AGENTS.md §20.3 enumerates why UserControl,
// Windows.Count, Visible and ServerTerminate all fail as substitutes). What IS known
// is WHICH SIGNAL drove us, i.e. `isHostShutdown`. So the line reports the signal,
// and on the host-shutdown signal it names BOTH outcomes it is compatible with
// instead of asserting one.
//
// THE NEGATIVE ASSERTION IS THE POINT. Two regressions are equally likely and both
// leave a naive substring check green: (1) "simplifying" the branch back to one
// unconditional sentence, and (2) resolving the ambiguity with a runtime survival
// probe — which is precisely the behaviour change the 2026-08-03 decision refused.
// Hence: the branch must exist, and the ENTIRE announce region — not just the two
// arms — may call nothing outside a named allowlist. The arm-scoped version of that
// check was too narrow to be worth anything: a probe on the line above the `if`
// satisfied it.
func TestGracefulTeardownLogNamesTheSignalNotTheOutcome(t *testing.T) {
	t.Parallel()

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_lifecycle.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_lifecycle.cpp not found")
	}
	// Comment-stripped: the rationale comment above the branch QUOTES the old
	// sentence in order to explain why it is gone, so an assertion over raw source
	// would fail on the fix itself.
	code := stripCppCommentsAsset(src)

	fnIdx := strings.Index(code, "void xll::GracefulTeardownOnce(bool isHostShutdown) {")
	if fnIdx < 0 {
		t.Fatalf("xll::GracefulTeardownOnce(bool) not found in src/xll_lifecycle.cpp")
	}
	// Window: the function prologue, from its opening brace to BeginQuiesce() — the
	// announce-then-quiesce region. Scoping matters: xlAutoClose's own LogInfo
	// further up legitimately contains the words "confirmed shutdown signal" (it
	// names the SIGNAL SET, which is accurate), so a whole-file absence assertion
	// would fail on a correct line.
	prologue := code[fnIdx:]
	if e := strings.Index(prologue, "BeginQuiesce("); e > 0 {
		prologue = prologue[:e]
	} else {
		t.Fatalf("could not find BeginQuiesce( after GracefulTeardownOnce's opening — the window this " +
			"test scopes to no longer exists")
	}

	// --- 1. The misleading sentence is GONE. ---
	if strings.Contains(prologue, "confirmed shutdown") {
		t.Errorf("GracefulTeardownOnce still announces a \"confirmed shutdown\". Measured 2026-08-03 "+
			"(3/3): an automation client's Application.Quit() with a held Application reference "+
			"delivers OnBeginShutdown, this teardown runs to completion, and EXCEL.EXE keeps "+
			"running — so that sentence gets printed over a session that continues, and the log "+
			"reader has no way to tell the two cases apart. Report the SIGNAL, not the "+
			"outcome (AGENTS.md §20.3)\n---\n%s", prologue)
	}

	// --- 2. The wording branches on the only fact knowable at that point. ---
	loc := reIfIsHostShutdownBlock.FindStringIndex(prologue)
	if loc == nil {
		t.Fatalf("the opening log must branch on isHostShutdown: the ext_dm_UserClosed case is NOT "+
			"ambiguous (the session demonstrably continues) while the host-shutdown case is, so one "+
			"sentence cannot be honest for both\n---\n%s", prologue)
	}
	ifBlock := blockFrom(prologue, loc[0])
	if ifBlock == "" {
		t.Fatalf("could not brace-match the `if (isHostShutdown) {` block\n---\n%s", prologue)
	}
	rest := prologue[loc[0]+len(ifBlock):]
	elseIdx := strings.Index(rest, "else")
	if elseIdx < 0 {
		t.Fatalf("the isHostShutdown branch has no `else`: the add-in-disconnect case must get its "+
			"own, unambiguous line\n---\n%s", prologue)
	}
	elseBlock := blockFrom(rest, elseIdx)
	if elseBlock == "" {
		t.Fatalf("could not brace-match the `else` block\n---\n%s", rest)
	}

	// --- 3. The host-shutdown arm names BOTH outcomes and how to resolve them. ---
	for _, want := range []struct{ token, why string }{
		{"HOST-SHUTDOWN", "the line must name the signal it actually received"},
		{"EITHER", "it must present two possible outcomes, not assert one"},
		{"Application.Quit(", "it must name the case that makes the signal a lie"},
		{"EXCEL.EXE", "it must name the observation that resolves the ambiguity after the fact — " +
			"whether the process is still alive is the one thing this code cannot see"},
		{"reload", "it must say how to recover, or the warning is a riddle"},
	} {
		if !strings.Contains(ifBlock, want.token) {
			t.Errorf("the host-shutdown log line must contain %q — %s\n---\n%s", want.token, want.why, ifBlock)
		}
	}
	// --- 4. …and the add-in-disconnect arm states the unambiguous fact. ---
	if !strings.Contains(elseBlock, "CONTINUES") {
		t.Errorf("the ext_dm_UserClosed log line must say the Excel session CONTINUES — unlike the "+
			"host-shutdown case this one IS knowable, and hedging it would throw away real "+
			"information\n---\n%s", elseBlock)
	}

	// --- 5. Both arms must actually log. ---
	for _, arm := range []struct {
		name string
		body string
	}{{"if (isHostShutdown)", ifBlock}, {"else", elseBlock}} {
		if !strings.Contains(arm.body, "LogInfo(") {
			t.Errorf("the %s arm must actually log (LogInfo)\n---\n%s", arm.name, arm.body)
		}
	}

	// --- 6. NOTHING IN THE WHOLE ANNOUNCE REGION MAY DO ANYTHING BUT LOG. ---
	// This is the pin against "resolve the ambiguity at runtime" — a survival probe,
	// a process-liveness check, an Application round-trip. That is a BEHAVIOUR change
	// inside the teardown ordering v0.8.41 validated at 0/40, and it was explicitly
	// refused on 2026-08-03.
	//
	// The scan covers the region, NOT the two arms, because a probe hoisted one line
	// above the `if` — `const bool alive = ProbeHostAlive();` — reads the same to a
	// reviewer and passed the arm-scoped version of this check.
	for _, mt := range reCallName.FindAllStringSubmatch(dropStringLiterals(prologue), -1) {
		name := mt[1]
		if cppNonCallKeywords[name] {
			continue
		}
		if _, ok := permittedInAnnounceRegion[name]; ok {
			continue
		}
		t.Errorf("the teardown announce region (GracefulTeardownOnce's opening → BeginQuiesce) "+
			"calls %s(), which is not on the allowlist in permittedInAnnounceRegion. This region "+
			"may only CAS, pin and log. Deciding between the two outcomes at runtime — a survival "+
			"probe, a liveness check, an Application round-trip — is a behaviour change inside the "+
			"frozen teardown ordering and was refused on 2026-08-03. If the call really belongs "+
			"here, record the decision in AGENTS.md §20.3 and add it to the allowlist with its "+
			"reason\n---\n%s", name, prologue)
	}
}

// TestOnBeginShutdownCommentKeepsTheSurvivalCaveat pins the co-change in the other
// translation unit.
//
// The comment above RibbonAddIn::OnBeginShutdown used to assert that the callback
// "fires only on a REAL quit". That claim is the source of the wrong mental model
// the log line above inherited, and it is exactly what a future reader would
// "restore" while tidying. If it comes back, the branch in xll_lifecycle.cpp reads
// like defensive noise and gets deleted next.
//
// A COMMENT assertion, stated plainly as such: the decision was to change no
// behaviour, so the comment IS the deliverable here and a test over it is the only
// honest gate at this level.
func TestOnBeginShutdownCommentKeepsTheSurvivalCaveat(t *testing.T) {
	t.Parallel()

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/ribbon_addin.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/ribbon_addin.cpp not found")
	}

	// RAW source — the comment is the subject here, not the code.
	idx := strings.Index(src, "HRESULT __stdcall RibbonAddIn::OnBeginShutdown(")
	if idx < 0 {
		t.Fatalf("RibbonAddIn::OnBeginShutdown not found in src/ribbon_addin.cpp")
	}
	// The body's own comments carry the caveat, so take the leading comment block
	// AND the body, bounded by the previous function's closing brace.
	region := src[:idx]
	if i := strings.LastIndex(region, "\n}"); i > 0 {
		region = region[i:]
	}
	body := src[idx:]
	if e := strings.Index(body, "\nHRESULT __stdcall RibbonAddIn::GetCustomUI("); e > 0 {
		body = body[:e]
	}
	region += body

	// THE NEGATIVE, AND WHY IT IS SHAPED LIKE THIS. A flat "the string `fires only on
	// a REAL quit` must not appear" fails on the FIX ITSELF — the corrected comment
	// quotes the old claim in order to retract it ("this comment used to claim it did
	// (\"fires only on a REAL quit\")"), and deleting that retraction would make the
	// correction unreadable. The property that is actually meant is that the claim
	// never appears as a LIVE assertion.
	//
	// SO THE RETRACTION IS BOUND TO EACH OCCURRENCE, not to the region. The first
	// version asked only "does `used to` appear anywhere in the region" — which the
	// existing retraction satisfies forever, so a new paragraph re-asserting the claim
	// as fact ("Note: it fires only on a REAL quit, so...") was licensed by the very
	// sentence that retracts it, and stayed GREEN (demonstrated before this rewrite).
	// EVERY occurrence must therefore (a) be a quoted citation, and (b) have `used to`
	// in the ~240 characters before it. Both together are hard to satisfy by accident
	// and trivial to satisfy when you are genuinely quoting the old claim to bury it.
	const oldClaim = "fires only on a REAL quit"
	const retractWindow = 240
	for off := 0; ; {
		i := strings.Index(region[off:], oldClaim)
		if i < 0 {
			break
		}
		at := off + i
		off = at + len(oldClaim)

		quoted := at > 0 && region[at-1] == '"' && off < len(region) && region[off] == '"'
		lo := at - retractWindow
		if lo < 0 {
			lo = 0
		}
		retracted := strings.Contains(region[lo:at], "used to")
		if quoted && retracted {
			continue
		}
		why := "it is not immediately preceded by `used to` (within " +
			strconv.Itoa(retractWindow) + " chars), so it reads as a live assertion"
		if !quoted {
			why = "it is not enclosed in double quotes, so it is not a citation of the old claim " +
				"but a restatement of it"
		}
		t.Errorf("OnBeginShutdown's comment contains %q at offset %d and %s. Measured 2026-08-03 "+
			"(3/3): an automation client that holds an Application reference and calls "+
			"Application.Quit() gets this callback, the destructive teardown runs, and EXCEL.EXE "+
			"keeps running — the claim is FALSE and may appear only as a quoted, explicitly "+
			"retracted citation\n---\n%s", oldClaim, at, why, region)
	}
	for _, want := range []struct{ token, why string }{
		{"Application.Quit(", "the comment must name the case in which the signal does not mean exit"},
		{"§20.3", "it must point at the section that owns the full record and the decision"},
	} {
		if !strings.Contains(region, want.token) {
			t.Errorf("OnBeginShutdown's comment must mention %q — %s\n---\n%s", want.token, want.why, region)
		}
	}
	// The behaviour is frozen: it still drives the same one-shot teardown, with
	// isHostShutdown=true. If this changes, the decision changed and §20.3 is stale.
	if !strings.Contains(stripCppCommentsAsset(body), "GracefulTeardownOnce(true)") &&
		!strings.Contains(body, "GracefulTeardownOnce(/*isHostShutdown=*/true)") {
		t.Errorf("OnBeginShutdown must still call xll::GracefulTeardownOnce with isHostShutdown=true. "+
			"The 2026-08-03 decision was to document the hole and improve the diagnostic, NOT to "+
			"change teardown behaviour\n---\n%s", body)
	}
}
