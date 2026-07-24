package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestGenCpp_RtdStringArgCoerce pins the fix for the RTD / rtd-once `string`
// argument topic-building path (IMPROVEMENT_BACKLOG §2). A `string` arg is
// registered LPXLOPER12, so it is USUALLY xltypeStr — but a numeric/bool/blank
// cell arrives as xltypeNum/Bool/Missing. The pre-fix wrapper handled ONLY
// xltypeStr and left the topic component EMPTY for any other input, collapsing
// DISTINCT non-string values onto ONE RTD topic (and, for rtd-once, one memo
// entry via MakeRtdOnceKey) — the same value-identity bug class as the
// pre-v0.8.29 %f float truncation. The fix coerces non-string inputs with
// xlCoerce(xltypeStr); the Excel-allocated result must ride a
// ScopedXLOPER12Result so its xlFree pairs (XLL SDK ownership), with the
// historical empty-string fallback kept on coerce failure.
//
// FAIL->PASS: before the fix the wrapper emitted only the `== xltypeStr` guard
// and NO xlCoerce, so the xlCoerce assertions below failed.
func TestGenCpp_RtdStringArgCoerce(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Rtd: config.RtdConfig{
			Enabled: true,
			ProgID:  "TestProj.Rtd",
			Clsid:   "{11111111-2222-3333-4444-555555555555}",
		},
		Functions: []config.Function{
			// Plain rtd with a string arg (topic component path).
			{Name: "RtdStr", Mode: "rtd", Return: "any",
				Args: []config.Arg{{Name: "s", Type: "string"}}},
			// rtd-once with a string arg (topic feeds MakeRtdOnceKey).
			{Name: "OnceStr", Mode: "rtd-once", Return: "float",
				Args: []config.Arg{{Name: "s", Type: "string"}}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	content := renderCppMain(t, cfg)

	// One xlCoerce topic conversion per wrapper (plain rtd + rtd-once).
	if got := strings.Count(content, "xll::CallExcel(xlCoerce, xCoerced,"); got != 2 {
		t.Errorf("expected 2 xlCoerce topic conversions (rtd + rtd-once), got %d:\n%s", got, content)
	}
	// The Excel-allocated coerce result must ride a ScopedXLOPER12Result so its
	// xlFree pairs on scope exit.
	if !strings.Contains(content, "ScopedXLOPER12Result xCoerced;") {
		t.Errorf("coerced RTD topic string must ride a ScopedXLOPER12Result (xlFree pairing):\n%s", content)
	}
	// The coerce target must be xltypeStr.
	if !strings.Contains(content, "xStrType.val.w = xltypeStr;") {
		t.Errorf("coerce target type must be xltypeStr:\n%s", content)
	}
	// The xltypeStr fast path (and thus the empty-string fallback on failure)
	// must be preserved.
	if !strings.Contains(content, "if (s->xltype == xltypeStr) {") {
		t.Errorf("xltypeStr fast path must be preserved:\n%s", content)
	}
}
