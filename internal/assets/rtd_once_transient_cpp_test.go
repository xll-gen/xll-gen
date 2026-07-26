package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// rtdOnceTransientDriver is a self-contained C++ driver for the embedded
// include/xll_rtd_once.h. It exercises RtdOnceRegistry's TRANSIENT retention
// contract for real (compile + run), rather than pattern-matching the source.
//
// Only RtdOnceRegistry / MakeRtdOnceKey are used, so the only link dependency is
// oleaut32 (VariantInit/Copy/Clear + SysAllocString). RtdOnceResultToXLOPER12 is
// deliberately NOT called: it is `inline` and would pull in types' mem.cpp
// (NewXLOPER12 / NewExcelString), turning this into a link-the-world test.
//
// It prints one "FAIL: ..." line per violated assertion and exits non-zero, so
// the Go test can surface the exact contract that broke.
const rtdOnceTransientDriver = `
#include "xll_rtd_once.h"

#include <cstdio>
#include <string>

static int g_fail = 0;

static void Check(bool ok, const char* what) {
    if (!ok) { std::printf("FAIL: %s\n", what); ++g_fail; }
}

static VARIANT MakeStr(const wchar_t* s) {
    VARIANT v; VariantInit(&v);
    v.vt = VT_BSTR;
    v.bstrVal = SysAllocString(s);
    return v;
}

static VARIANT MakeNum(double d) {
    VARIANT v; VariantInit(&v);
    v.vt = VT_R8;
    v.dblVal = d;
    return v;
}

static bool IsStr(const VARIANT& v, const wchar_t* s) {
    return v.vt == VT_BSTR && v.bstrVal != nullptr && std::wstring(v.bstrVal) == s;
}

// StoreAndForget stores a copy in the registry and releases the caller's VARIANT.
static void StoreAndForget(xll::RtdOnceRegistry& reg, const std::wstring& key,
                           VARIANT v, bool transient) {
    reg.StoreResult(key, v, transient);
    VariantClear(&v);
}

int main() {
    auto& reg = xll::RtdOnceRegistry::Instance();
    // MemoFn: memoize:true.  TtlFn: memoize_ttl = 1h (never expires in-test).
    // OnceFn: plain once.
    reg.SetFunctionNames({L"MemoFn", L"TtlFn", L"OnceFn"},
                         {L"MemoFn"},
                         {{L"TtlFn", 3600000ULL}});

    const std::wstring memoKey = xll::MakeRtdOnceKey({L"MemoFn", L"7"});
    const std::wstring ttlKey  = xll::MakeRtdOnceKey({L"TtlFn", L"7"});
    const std::wstring onceKey = xll::MakeRtdOnceKey({L"OnceFn", L"7"});

    VARIANT out;

    // (a) A TRANSIENT error must be STORED and immediately readable while the
    //     topic that produced it is still connected. This is the one recalc in
    //     which the wrapper paints the error into the cell; a miss here means the
    //     wrapper re-issues xlfRtd against a still-connected topic, gets no new
    //     ConnectData, and the cell is stuck at #GETTING_DATA forever.
    reg.RegisterTopic(101, memoKey);
    StoreAndForget(reg, memoKey, MakeStr(L"boom: upstream timeout"), true);

    VariantInit(&out);
    Check(reg.TryGetResult(memoKey, &out),
          "(a) a transient error must be stored and readable while its topic is live "
          "(a miss = cell stuck at #GETTING_DATA forever)");
    Check(IsStr(out, L"boom: upstream timeout"),
          "(a) the transient read must return the error string verbatim");
    VariantClear(&out);

    // (a2) LIVENESS GUARD: a CalculationEnded firing before the wrapper's recalc
    //      must NOT drop a transient entry whose topic is still live.
    reg.ClearNonMemoized();
    VariantInit(&out);
    Check(reg.TryGetResult(memoKey, &out),
          "(a2) liveness guard: a transient entry with a LIVE topic must survive ClearNonMemoized");
    VariantClear(&out);

    // (b) After the topic disconnects, ClearNonMemoized must erase the transient
    //     entry EVEN THOUGH MemoFn declares memoize:true. Otherwise the error is
    //     frozen until XLL reload and the handler can never be retried.
    reg.UnregisterTopic(101);
    reg.ClearNonMemoized();
    VariantInit(&out);
    Check(!reg.TryGetResult(memoKey, &out),
          "(b) ClearNonMemoized must erase a transient entry even for memoize:true");
    VariantClear(&out);

    // (b2) Same for memoize_ttl: a transient entry ignores the (unexpired) TTL.
    reg.RegisterTopic(102, ttlKey);
    StoreAndForget(reg, ttlKey, MakeStr(L"ttl boom"), true);
    reg.UnregisterTopic(102);
    reg.ClearNonMemoized();
    VariantInit(&out);
    Check(!reg.TryGetResult(ttlKey, &out),
          "(b2) ClearNonMemoized must erase a transient entry even for memoize_ttl (unexpired)");
    VariantClear(&out);

    // (c) Read-side twin: if CalculationEnded fired BEFORE DisconnectData, the
    //     entry survives the sweep with a live topic; the next read (topic now
    //     gone) must be a MISS so the recalc recomputes instead of re-serving
    //     the stale error for another cycle.
    reg.RegisterTopic(103, memoKey);
    StoreAndForget(reg, memoKey, MakeStr(L"stale boom"), true);
    reg.UnregisterTopic(103);
    VariantInit(&out);
    Check(!reg.TryGetResult(memoKey, &out),
          "(c) TryGetResult must MISS a transient entry whose topic is gone, "
          "even before ClearNonMemoized runs");
    VariantClear(&out);

    // (d) CONTROL: a NON-transient memoize:true result must still survive the
    //     exact same disconnect + sweep sequence. The transient rule must not
    //     have degraded memoize into once.
    reg.RegisterTopic(104, memoKey);
    StoreAndForget(reg, memoKey, MakeNum(42.0), false);
    reg.UnregisterTopic(104);
    reg.ClearNonMemoized();
    VariantInit(&out);
    Check(reg.TryGetResult(memoKey, &out),
          "(d) control: a NON-transient memoize:true result must survive ClearNonMemoized");
    Check(out.vt == VT_R8 && out.dblVal == 42.0,
          "(d) control: the memoized result must read back unchanged");
    VariantClear(&out);

    // (e) CONTROL: a NON-transient memoize_ttl result that has not expired must
    //     survive the sweep.
    reg.RegisterTopic(105, ttlKey);
    StoreAndForget(reg, ttlKey, MakeNum(7.0), false);
    reg.UnregisterTopic(105);
    reg.ClearNonMemoized();
    VariantInit(&out);
    Check(reg.TryGetResult(ttlKey, &out),
          "(e) control: a NON-transient unexpired memoize_ttl result must survive ClearNonMemoized");
    VariantClear(&out);

    // (f) CONTROL: plain 'once' is unchanged — erased after disconnect.
    reg.RegisterTopic(106, onceKey);
    StoreAndForget(reg, onceKey, MakeNum(1.0), false);
    reg.UnregisterTopic(106);
    reg.ClearNonMemoized();
    VariantInit(&out);
    Check(!reg.TryGetResult(onceKey, &out),
          "(f) control: a plain 'once' result must be erased after its topic disconnects");
    VariantClear(&out);

    // (g) A completed result stored over a transient one must be PROMOTED back to
    //     normal (memoize) retention — the flag is re-stamped, not sticky.
    reg.RegisterTopic(107, memoKey);
    StoreAndForget(reg, memoKey, MakeStr(L"first attempt failed"), true);
    StoreAndForget(reg, memoKey, MakeNum(99.0), false);
    reg.UnregisterTopic(107);
    reg.ClearNonMemoized();
    VariantInit(&out);
    Check(reg.TryGetResult(memoKey, &out),
          "(g) a completed result stored over a transient one must regain memoize retention");
    Check(out.vt == VT_R8 && out.dblVal == 99.0,
          "(g) the promoted entry must hold the completed value");
    VariantClear(&out);

    if (g_fail != 0) {
        std::printf("%d assertion(s) failed\n", g_fail);
        return 1;
    }
    std::printf("OK\n");
    return 0;
}
`

// TestRtdOnceRegistryTransientBehavior is the BEHAVIORAL regression gate for the
// rtd-once error path. It compiles the embedded xll_rtd_once.h together with a
// driver and RUNS it, asserting the transient-entry contract:
//
//	(a) a transient (is_error) value IS stored and readable while its topic is
//	    live — so the cell paints the error instead of sticking at #GETTING_DATA
//	    forever (the HIGH regression the 2026-07-24 review caught: the previous
//	    revision SKIPPED StoreResult for errors, and because the wrapper only
//	    returns a value on a cache HIT while the topic stays connected, the cell
//	    could never recover);
//	(b) once the topic disconnects, ClearNonMemoized erases it EVEN FOR
//	    memoize:true / memoize_ttl — so the handler can be retried;
//	(c) plus the read-side miss and the memoize/ttl/once controls.
//
// Requires g++ (MinGW/MSYS2) and the sibling `types` checkout for
// types/xlcall.h + types/mem.h. Skipped (not failed) when either is absent, and
// under -short, like the heavier cmake gates. Point it at the checkout with
// XLLGEN_TYPES_SRC; it defaults to the ../types sibling of the repo root.
//
// NOTE: the pinned types tag is NOT required here — the header under test does
// not reference RtdUpdate.is_error (that lives in xll_rtd.cpp), so any types
// checkout with xlcall.h/mem.h works.
func TestRtdOnceRegistryTransientBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll_rtd_once.h is Windows-only (windows.h / oaidl.h)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping xll_rtd_once.h compile+run gate")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../internal/assets/<file> -> repo root -> workspace root (holds types/).
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	typesSrc := os.Getenv("XLLGEN_TYPES_SRC")
	if typesSrc == "" {
		typesSrc = filepath.Join(filepath.Dir(repoRoot), "types")
	}
	typesInc := filepath.Join(typesSrc, "include")
	if _, err := os.Stat(filepath.Join(typesInc, "types", "xlcall.h")); err != nil {
		t.Skipf("types headers not found under %s; skipping (set XLLGEN_TYPES_SRC)", typesInc)
	}

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_rtd_once.h"]
	if !ok {
		t.Fatalf("embedded include/xll_rtd_once.h not found in assets")
	}

	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "xll_rtd_once.h"), []byte(hdr), 0o644); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "driver.cpp")
	if err := os.WriteFile(srcPath, []byte(rtdOnceTransientDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	exePath := filepath.Join(dir, "driver.exe")

	// gnu++17, not c++17: types/xlcall.h uses the MS spellings `_cdecl`/`pascal`,
	// which MinGW only defines outside __STRICT_ANSI__. This matches the real
	// build (CMAKE_CXX_STANDARD 17 with CMAKE_CXX_EXTENSIONS default ON).
	build := exec.Command(gxx,
		"-std=gnu++17", "-DXLL_RTD_ENABLED",
		"-I", incDir, "-I", typesInc,
		"-o", exePath, srcPath,
		"-loleaut32",
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("xll_rtd_once.h driver failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("RtdOnceRegistry transient-retention contract violated: %v\n%s", err, out)
	}
}
