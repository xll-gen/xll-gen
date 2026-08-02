package assets

import (
	"regexp"
	"strings"
	"testing"
)

// The comment stripper is test INFRASTRUCTURE, and it had a defect that could
// only ever fail silently, so it gets its own tests.
//
// THE DEFECT (found 2026-08-03). Both strippers used to be two regexes applied
// in sequence: `(?s)/\*.*?\*/` first, then `//[^\n]*`. Block-first is wrong,
// because a `/*` that appears INSIDE A LINE COMMENT is not a comment opener —
// but the block regex cannot know that, so it matched from there to the next
// `*/` ANYWHERE later in the file and deleted everything in between. Real
// trigger: a line comment containing `file(GLOB src/*.cpp)` sitting a few
// hundred lines above a `/*allowBounce=*/` argument.
//
// WHY IT MATTERS MORE THAN IT LOOKS. When the swallow ate a chunk that a
// POSITIVE assertion needed, the suite went loudly red and someone investigated.
// When it eats a chunk that an ORDER assertion (`guardIdx < teardownIdx`) or a
// negative "must NOT be re-inlined" assertion depends on, the assertion passes —
// the forbidden text is gone, after all. Those guards then read GREEN while
// checking nothing, which is the exact failure they exist to prevent.
func TestStripCppComments_LineCommentContainingBlockOpener(t *testing.T) {
	t.Parallel()
	// The shape that actually bit: a glob in a line comment, then code that must
	// survive, then a real inline block comment far below.
	src := strings.Join([]string{
		`// swept up by file(GLOB src/*.cpp) in every project`,
		`KEEP_ME_ONE;`,
		`KEEP_ME_TWO;`,
		`Call(/*allowBounce=*/true);`,
		`KEEP_ME_THREE;`,
	}, "\n")

	got := stripCppCommentsScan(src)
	for _, want := range []string{"KEEP_ME_ONE;", "KEEP_ME_TWO;", "KEEP_ME_THREE;", "Call(", "true);"} {
		if !strings.Contains(got, want) {
			t.Errorf("stripper swallowed %q — an order or negative assertion over this text "+
				"would now pass while checking nothing:\n%s", want, got)
		}
	}
	if strings.Contains(got, "allowBounce") {
		t.Errorf("the real block comment survived: %s", got)
	}

	// Pin the regression against the exact old implementation, so this test is
	// demonstrably non-vacuous rather than merely green.
	reBlock := regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLine := regexp.MustCompile(`//[^\n]*`)
	old := reLine.ReplaceAllString(reBlock.ReplaceAllString(src, ""), "")
	if strings.Contains(old, "KEEP_ME_TWO;") {
		t.Fatal("the old block-first stripper did NOT swallow this input, so this test no " +
			"longer reproduces the defect it was written for — rewrite it")
	}
}

func TestStripCppComments_LeavesLiteralsAlone(t *testing.T) {
	t.Parallel()
	// A "//" or "/*" inside a string literal is data, not a comment. Getting this
	// wrong would delete real code after any URL or path literal.
	cases := []struct{ name, src, want string }{
		{"url in string", `Log("see https://example.com/x"); KEEP;`, `KEEP;`},
		{"block opener in string", `Log("/* not a comment */"); KEEP;`, `KEEP;`},
		{"escaped quote", `Log("he said \" // still a string"); KEEP;`, `KEEP;`},
		{"char literal", `if (c == '/') { KEEP; }`, `KEEP;`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCppCommentsScan(tc.src); !strings.Contains(got, tc.want) {
				t.Errorf("lost %q from %q:\n%s", tc.want, tc.src, got)
			}
		})
	}
}

func TestStripCppComments_RemovesWhatItShould(t *testing.T) {
	t.Parallel()
	src := "CODE_A;\n// a line comment mentioning FORBIDDEN\nCODE_B; /* block\nspanning FORBIDDEN lines */ CODE_C;\n"
	got := stripCppCommentsScan(src)
	if strings.Contains(got, "FORBIDDEN") {
		t.Errorf("comment text survived, so a negative assertion could false-positive:\n%s", got)
	}
	for _, want := range []string{"CODE_A;", "CODE_B;", "CODE_C;"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
}

func TestStripCppComments_UnterminatedBlockDropsTheRemainder(t *testing.T) {
	t.Parallel()
	// Dropping the tail is the safe direction: it can only make a positive
	// assertion fail loudly, never make one pass spuriously.
	got := stripCppCommentsScan("CODE_A;\n/* never closed\nCODE_B;")
	if !strings.Contains(got, "CODE_A;") {
		t.Errorf("lost code before the unterminated comment: %q", got)
	}
	if strings.Contains(got, "CODE_B;") {
		t.Errorf("code inside an unterminated block comment survived: %q", got)
	}
}
