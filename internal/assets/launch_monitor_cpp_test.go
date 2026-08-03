package assets

import (
	"strings"
	"testing"
)

// funcBody returns the balanced-brace body of the function whose signature line
// contains sig, starting at that line. Brace matching, not "everything to EOF" or
// "up to the next function I happen to know about": MonitorProcess is currently the
// last function in xll_launch.cpp, and a test that relied on that would silently
// start scanning a NEW neighbour the day one is added -- reporting that neighbour's
// MessageBoxW as a MonitorProcess regression, or (worse, if the neighbour is added
// above) stop covering MonitorProcess at all.
//
// String and character literals are skipped so a brace inside one cannot unbalance
// the count.
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("signature %q not found", sig)
	}
	open := strings.Index(src[i:], "{")
	if open < 0 {
		t.Fatalf("no opening brace after %q", sig)
	}
	j := i + open
	depth := 0
	for k := j; k < len(src); k++ {
		switch src[k] {
		case '"', '\'':
			q := src[k]
			for k++; k < len(src); k++ {
				if src[k] == '\\' {
					k++
					continue
				}
				if src[k] == q {
					break
				}
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[i : k+1]
			}
		}
	}
	t.Fatalf("unbalanced braces after %q", sig)
	return ""
}

// TestMonitorProcessHasNoModalDialog pins the ABSENCE of a modal dialog on the
// server-monitor thread (2026-08-03).
//
// History, because the absence is load-bearing and the reason is not local to this
// file. MonitorProcess used to raise MessageBoxW("Server Crash") when it found the Go
// server dead. That thread is JOINED by teardown, so a crashed server plus an
// unattended machine turned close-time teardown into an indefinite park -- on the STA,
// holding Excel -- until a human clicked OK. It was removed on 2026-08-02 in favour of
// a native-log ERROR.
//
// Nothing enforced that afterwards. The only guard was a "NEVER put a modal dialog in
// here" comment, and a comment does not fail a build. Meanwhile THREE other places
// still cite the dialog as their justification (xll_lifecycle.cpp's bounded reap and
// its Phase 2 handle gating, plus gen_close_unload_review_test.go), so the invariant
// is depended upon from outside this file while being unchecked inside it.
//
// What is asserted:
//  1. MonitorProcess's own body has no MessageBoxW. This is the invariant.
//  2. The comment stating the rule is still there, so the next reader learns WHY
//     rather than re-adding the dialog as an obvious improvement.
//  3. The DIAGNOSTIC survived the dialog's removal. "Delete the dialog" and "delete
//     the crash report" look identical from a grep for MessageBoxW, and the second
//     would leave a dead Go server completely silent.
//  4. Exactly the two known MessageBoxW calls remain in the file, both OUTSIDE
//     MonitorProcess. They are on the xlAutoOpen launch path: STA, user-facing
//     startup, nothing joining them. Pinning the count means a third one has to be
//     justified deliberately instead of appearing by habit.
func TestMonitorProcessHasNoModalDialog(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_launch.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_launch.cpp not found in assets")
	}

	body := funcBody(t, src, "void MonitorProcess(")

	// (1) the invariant
	if strings.Contains(stripCppCommentsAsset(body), "MessageBoxW") {
		t.Errorf("MonitorProcess contains a MessageBoxW again. That thread is JOINED by " +
			"teardown (xll_lifecycle.cpp BeginQuiesce), so a modal dialog here parks Excel's " +
			"STA until a human dismisses it -- unbounded, which §20.2.1 rule 3 forbids " +
			"outright. Report through the native log instead (LogError).")
	}

	// (2) the reason, kept where the next author will be standing. It lives in the DOC
	//     BLOCK above the signature, not inside the body, so the window searched is the
	//     text preceding the function -- getting this wrong is how the first draft of
	//     this test failed against correct code.
	sigAt := strings.Index(src, "void MonitorProcess(")
	preamble := src[max(0, sigAt-2000):sigAt]
	if !strings.Contains(preamble, "NEVER put a modal dialog in here") {
		t.Errorf("MonitorProcess lost the comment stating why it must not raise a dialog; " +
			"without it the dialog is an obvious-looking improvement to re-add")
	}

	// (3) the diagnostic, which is a DIFFERENT thing from the dialog
	if !strings.Contains(body, `LogError("Server crash: "`) {
		t.Errorf("MonitorProcess no longer reports the server crash at all. Removing the " +
			"DIALOG was the fix; removing the REPORT would leave a dead Go server silent, " +
			"and both look the same to a grep for MessageBoxW")
	}

	// (4) the two deliberately-kept dialogs, and no third
	total := strings.Count(stripCppCommentsAsset(src), "MessageBoxW")
	if total != 2 {
		t.Errorf("xll_launch.cpp has %d MessageBoxW call(s), want exactly 2 (the xlAutoOpen "+
			"launch-path dialogs: \"XLL Error\" and \"Launch Error\" -- STA, user-facing "+
			"startup, nothing joins them). A new one needs a deliberate decision about who "+
			"might be waiting on the thread that raises it.", total)
	}
}
