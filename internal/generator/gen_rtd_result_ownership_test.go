package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
)

// rtdOwnershipCfg renders every wrapper shape that calls xlfRtd: plain rtd
// (returns xRes verbatim) and rtd-once with a scalar, a grid and a numgrid return
// (all three DISCARD xRes).
func rtdOwnershipCfg() *config.Config {
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "Clock", Mode: "rtd", Return: "string", Args: []config.Arg{}},
			{Name: "SlowText", Mode: "rtd-once", Return: "string", Args: []config.Arg{}},
			{Name: "SlowGrid", Mode: "rtd-once", Return: "grid", Args: []config.Arg{}},
			{Name: "SlowNums", Mode: "rtd-once", Return: "numgrid", Args: []config.Arg{}},
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
	*cfg.Server.Launch.Enabled = true
	return cfg
}

// TestGenCpp_XlfRtdResultOwnershipIsSettled pins the fix for the leaked xlfRtd
// result (HIGH, 2026-07-29).
//
// THE DEFECT. Excel12/Excel12v FILLS IN the caller's XLOPER12, and the auxiliary
// memory behind xltypeStr / xltypeMulti / xltypeRef in such a result belongs to
// EXCEL. The SDK allows exactly two dispositions: release it with xlFree, or — for
// a value the UDF RETURNS — set xlbitXLFree and let Excel reclaim it. The
// plain-rtd wrapper did NEITHER (it returned `&xRes` with no ownership bit) and
// the three rtd-once wrappers threw the value away with `(void)xRes;`. Every
// string-valued RTD push therefore leaked one Excel-heap Pascal buffer, per cell,
// per tick — invisible in every log, and unbounded in a dashboard left open
// (showcase Clock pushes every 1s, StockTick every 750ms). `types` v0.2.9 fixed
// the same contract violation for the other Excel12 result sites; these wrappers
// were missed.
//
// This is the string-level GATE the task asked for: generated code must emit an
// xlbitXLFree transfer or an xlFree release for the xlfRtd result — never nothing.
func TestGenCpp_XlfRtdResultOwnershipIsSettled(t *testing.T) {
	t.Parallel()
	rendered := renderCppMain(t, rtdOwnershipCfg())
	content := stripCppComments(rendered)

	// 1. The rejected shape is gone: an xlfRtd result must never be discarded
	//    with a bare cast-to-void.
	if strings.Contains(content, "(void)xRes;") {
		t.Errorf("an xlfRtd result is still discarded with `(void)xRes;`; the Excel-allocated " +
			"payload behind it leaks on every call")
	}

	// 2. Every xlfRtd call site is followed by a disposition. There are 4 wrappers
	//    (1 plain rtd + 3 rtd-once), and each has a success path plus a failure
	//    path, so the helper must appear at least once per wrapper.
	rtdCalls := strings.Count(content, "xll::CallExcel(xlfRtd, &xRes,")
	if rtdCalls != 4 {
		t.Fatalf("expected 4 xlfRtd call sites (1 rtd + 3 rtd-once), got %d; update this test", rtdCalls)
	}
	dispositions := strings.Count(content, "xll::ReleaseOrTransferExcelResult(xRes,")
	if dispositions < rtdCalls {
		t.Errorf("%d xlfRtd call sites but only %d ownership dispositions; at least one result "+
			"is neither transferred to Excel nor freed", rtdCalls, dispositions)
	}

	// 3. Plain rtd TRANSFERS (it returns the value, so xlbitXLFree is the only
	//    legal disposition — freeing it would hand Excel a dangling pointer).
	plain := sliceBetween(t, content, `__stdcall Clock(`, `__stdcall SlowText(`)
	if !strings.Contains(plain, "xll::ReleaseOrTransferExcelResult(xRes, true);") {
		t.Errorf("plain rtd must TRANSFER ownership (transferToExcel=true) before `return &xRes;`:\n%s", plain)
	}
	iTransfer := strings.Index(plain, "xll::ReleaseOrTransferExcelResult(xRes, true);")
	iReturn := strings.LastIndex(plain, "return &xRes;")
	if iTransfer < 0 || iReturn < 0 || iTransfer > iReturn {
		t.Errorf("the ownership transfer must come BEFORE `return &xRes;` (transfer=%d return=%d)", iTransfer, iReturn)
	}

	// 4. Every rtd-once wrapper RELEASES (it discards the value; xlbitXLFree is
	//    only honored on a value Excel actually receives back, and the numgrid
	//    wrapper does not even return an XLOPER12).
	for _, fn := range []struct{ name, next string }{
		{"SlowText", "__stdcall SlowGrid("},
		{"SlowGrid", "__stdcall SlowNums("},
	} {
		body := sliceBetween(t, content, "__stdcall "+fn.name+"(", fn.next)
		if !strings.Contains(body, "xll::ReleaseOrTransferExcelResult(xRes, false);") {
			t.Errorf("rtd-once %s discards xRes, so it must RELEASE it (transferToExcel=false):\n%s", fn.name, body)
		}
		if strings.Contains(body, "xll::ReleaseOrTransferExcelResult(xRes, true);") {
			t.Errorf("rtd-once %s must NOT set xlbitXLFree: the bit is only honored on a value "+
				"Excel receives back, and this wrapper returns a placeholder instead", fn.name)
		}
	}
	nums := content[strings.Index(content, "__stdcall SlowNums("):]
	if !strings.Contains(nums, "xll::ReleaseOrTransferExcelResult(xRes, false);") {
		t.Errorf("rtd-once numgrid must RELEASE xRes:\n%s", nums)
	}
}

// sliceBetween returns the text from `from` up to the next occurrence of `to`.
func sliceBetween(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	if i < 0 {
		t.Fatalf("marker %q not found", from)
	}
	rest := s[i:]
	j := strings.Index(rest, to)
	if j < 0 {
		t.Fatalf("marker %q not found after %q", to, from)
	}
	return rest[:j]
}

// TestReleaseOrTransferExcelResultContract pins the shared helper in the embedded
// xll_excel.h asset: the two dispositions, the type switch that keeps the bit off
// a value with nothing to own, and the refusal to touch a DLL-owned payload
// (xlFree would corrupt our allocator; xlbitXLFree would ask Excel to free memory
// it never allocated).
func TestReleaseOrTransferExcelResultContract(t *testing.T) {
	t.Parallel()
	files, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := files["include/xll_excel.h"]
	if !ok {
		t.Fatalf("include/xll_excel.h not embedded; available: %d files", len(files))
	}
	code := stripCppComments(src)

	if !strings.Contains(code, "inline void ReleaseOrTransferExcelResult(XLOPER12& op, bool transferToExcel)") {
		t.Fatalf("the shared helper is missing from xll_excel.h:\n%s", code)
	}
	// Only the pointer-bearing Excel types are acted on.
	for _, want := range []string{"case xltypeStr:", "case xltypeMulti:", "case xltypeRef:", "default:"} {
		if !strings.Contains(code, want) {
			t.Errorf("the helper's type switch is missing %q", want)
		}
	}
	// Both dispositions.
	if !strings.Contains(code, "op.xltype |= xlbitXLFree;") {
		t.Errorf("the transfer branch does not set xlbitXLFree")
	}
	if !strings.Contains(code, "CallExcel(xlFree, nullptr, &op);") {
		t.Errorf("the release branch does not call xlFree")
	}
	if !strings.Contains(code, "op.xltype = xltypeNil;") {
		t.Errorf("the release branch does not reset the operand, so a repeated call could double-free")
	}
	// A DLL-owned payload is never touched.
	if !strings.Contains(code, "(op.xltype & xlbitDLLFree) != 0") {
		t.Errorf("the helper does not refuse a DLL-owned (xlbitDLLFree) payload; xlFree on one " +
			"would corrupt our own allocator")
	}
}
