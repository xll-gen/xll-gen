package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// rtdOnceGridErrorDriver is a self-contained C++ driver for the embedded
// include/xll_rtd_once_grid.h. It exercises RtdOnceGridRegistry's TRANSIENT
// (error) entry contract for real (compile + run), rather than pattern-matching
// the source. It is the grid twin of rtdOnceTransientDriver.
//
// The header pulls in only <windows.h> + the STL (no oaidl/VARIANT, no
// types/xlcall.h), so this driver needs no extra include dirs and no link
// libraries beyond the CRT — GetTickCount64 comes from kernel32, which MinGW
// links by default.
//
// It prints one "FAIL: ..." line per violated assertion and exits non-zero, so
// the Go test can surface the exact contract that broke.
const rtdOnceGridErrorDriver = `
#include "xll_rtd_once_grid.h"

#include <cstdio>
#include <string>
#include <vector>

static int g_fail = 0;

static void Check(bool ok, const char* what) {
    if (!ok) { std::printf("FAIL: %s\n", what); ++g_fail; }
}

// A stand-in for a serialized protocol::RtdOnceGridResult buffer. The registry
// is byte-agnostic; only its length and identity matter here.
static const uint8_t kGridBytes[] = { 0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02 };

// OnceKey builds the same key MakeRtdOnceKey (xll_rtd_once.h) does: the topic
// strings joined with the U+001F unit separator. Spelled out with an explicit
// code point so this driver depends on the GRID header alone (no
// types/xlcall.h, no oleaut32) and no control character has to survive a
// round-trip through this Go string literal.
static std::wstring OnceKey(const wchar_t* funcName) {
    std::wstring k(funcName);
    k.push_back(static_cast<wchar_t>(0x1f));
    k += L"h:g1234";
    return k;
}

static void StoreGrid(xll::RtdOnceGridRegistry& reg, const std::wstring& key) {
    reg.Store(key, kGridBytes, sizeof(kGridBytes));
}

int main() {
    using LK = xll::OnceGridLookup;
    auto& reg = xll::RtdOnceGridRegistry::Instance();
    // MemoFn: memoize:true.  TtlFn: memoize_ttl = 1h (never expires in-test).
    // OnceFn: plain once.
    reg.SetFunctionNames({L"MemoFn", L"TtlFn", L"OnceFn"},
                         {L"MemoFn"},
                         {{L"TtlFn", 3600000ULL}});

    const std::wstring memoKey = OnceKey(L"MemoFn");
    const std::wstring ttlKey  = OnceKey(L"TtlFn");
    const std::wstring onceKey = OnceKey(L"OnceFn");

    std::vector<uint8_t> out;
    std::wstring err;

    // (a) A TRANSIENT error must be STORED and reported as kError (a HIT, not a
    //     miss) while the topic that produced it is still connected. That is the
    //     one recalc in which the wrapper paints the failure; a kMiss here makes
    //     the wrapper re-issue xlfRtd against a still-connected topic, no new
    //     ConnectData fires, the one-shot handler never re-runs, and the cell is
    //     frozen on the loading placeholder forever.
    reg.RegisterTopic(201, memoKey);
    reg.StoreError(memoKey, L"boom: upstream timeout");

    out.assign(1, 0xFF); err.clear();
    Check(reg.TryGet(memoKey, &out, &err) == LK::kError,
          "(a) a transient error must be stored and read back as kError while its topic is live "
          "(a miss = grid/numgrid cell frozen on the loading placeholder forever)");
    Check(err == L"boom: upstream timeout",
          "(a) the transient read must return the error message verbatim");
    Check(out.empty(),
          "(a) an error entry must clear the payload out-param — a stale buffer would be "
          "handed to flatbuffers::GetRoot as if it were a grid");

    // (a2) LIVENESS GUARD: a CalculationEnded firing before the wrapper's recalc
    //      must NOT drop a transient entry whose topic is still live.
    reg.ClearNonMemoized();
    err.clear();
    Check(reg.TryGet(memoKey, &out, &err) == LK::kError,
          "(a2) liveness guard: a transient entry with a LIVE topic must survive ClearNonMemoized");

    // (b) After the topic disconnects, ClearNonMemoized must erase the transient
    //     entry EVEN THOUGH MemoFn declares memoize:true. Otherwise the error is
    //     frozen until XLL reload and the handler can never be retried.
    reg.UnregisterTopic(201);
    reg.ClearNonMemoized();
    Check(reg.TryGet(memoKey, &out, &err) == LK::kMiss,
          "(b) ClearNonMemoized must erase a transient entry even for memoize:true");

    // (b2) Same for memoize_ttl: a transient entry ignores the (unexpired) TTL.
    reg.RegisterTopic(202, ttlKey);
    reg.StoreError(ttlKey, L"ttl boom");
    reg.UnregisterTopic(202);
    reg.ClearNonMemoized();
    Check(reg.TryGet(ttlKey, &out, &err) == LK::kMiss,
          "(b2) ClearNonMemoized must erase a transient entry even for memoize_ttl (unexpired)");

    // (c) Read-side twin: if CalculationEnded fired BEFORE DisconnectData, the
    //     entry survives the sweep with a live topic; the next read (topic now
    //     gone) must be a MISS so the recalc recomputes instead of re-serving
    //     the stale error for another cycle.
    reg.RegisterTopic(203, memoKey);
    reg.StoreError(memoKey, L"stale boom");
    reg.UnregisterTopic(203);
    Check(reg.TryGet(memoKey, &out, &err) == LK::kMiss,
          "(c) TryGet must MISS a transient entry whose topic is gone, "
          "even before ClearNonMemoized runs");

    // (d) CONTROL: a NON-transient memoize:true payload must still survive the
    //     exact same disconnect + sweep sequence. The transient rule must not
    //     have degraded memoize into once.
    reg.RegisterTopic(204, memoKey);
    StoreGrid(reg, memoKey);
    reg.UnregisterTopic(204);
    reg.ClearNonMemoized();
    out.clear();
    Check(reg.TryGet(memoKey, &out, &err) == LK::kResult,
          "(d) control: a NON-transient memoize:true grid must survive ClearNonMemoized");
    Check(out.size() == sizeof(kGridBytes) && out[0] == 0xDE,
          "(d) control: the memoized grid bytes must read back unchanged");

    // (e) CONTROL: a NON-transient memoize_ttl payload that has not expired must
    //     survive the sweep.
    reg.RegisterTopic(205, ttlKey);
    StoreGrid(reg, ttlKey);
    reg.UnregisterTopic(205);
    reg.ClearNonMemoized();
    Check(reg.TryGet(ttlKey, &out, &err) == LK::kResult,
          "(e) control: a NON-transient unexpired memoize_ttl grid must survive ClearNonMemoized");

    // (f) CONTROL: plain 'once' is unchanged — erased after disconnect.
    reg.RegisterTopic(206, onceKey);
    StoreGrid(reg, onceKey);
    reg.UnregisterTopic(206);
    reg.ClearNonMemoized();
    Check(reg.TryGet(onceKey, &out, &err) == LK::kMiss,
          "(f) control: a plain 'once' grid must be erased after its topic disconnects");

    // (g) A completed grid stored over a transient error must be PROMOTED back to
    //     normal (memoize) retention — the flag is re-stamped, not sticky — and the
    //     stale error text must be gone.
    reg.RegisterTopic(207, memoKey);
    reg.StoreError(memoKey, L"first attempt failed");
    StoreGrid(reg, memoKey);
    reg.UnregisterTopic(207);
    reg.ClearNonMemoized();
    out.clear(); err = L"sentinel";
    Check(reg.TryGet(memoKey, &out, &err) == LK::kResult,
          "(g) a completed grid stored over a transient error must regain memoize retention");
    Check(out.size() == sizeof(kGridBytes),
          "(g) the promoted entry must hold the completed grid bytes");
    Check(err == L"sentinel",
          "(g) a kResult lookup must not overwrite the caller's error out-param");

    // (h) An error stored over a completed grid must WIN and drop the payload:
    //     the handler re-ran, failed, and the cell must show the failure rather
    //     than a stale grid from a previous cycle.
    reg.RegisterTopic(208, memoKey);
    StoreGrid(reg, memoKey);
    reg.StoreError(memoKey, L"second attempt failed");
    out.assign(3, 0x11);
    Check(reg.TryGet(memoKey, &out, &err) == LK::kError,
          "(h) an error stored over a completed grid must be reported as kError");
    Check(out.empty(),
          "(h) an error stored over a completed grid must drop the stale payload");
    reg.UnregisterTopic(208);
    reg.ClearNonMemoized();

    if (g_fail != 0) {
        std::printf("%d assertion(s) failed\n", g_fail);
        return 1;
    }
    std::printf("OK\n");
    return 0;
}
`

// TestRtdOnceGridRegistryErrorBehavior is the BEHAVIORAL regression gate for the
// grid-once error path (AGENTS.md §19.3, "Error values on a scalar rtd-once
// topic" / grid follow-up). It compiles the embedded xll_rtd_once_grid.h
// together with a driver and RUNS it.
//
// The gap it closes: before this, an is_error RtdUpdate for a grid/numgrid
// rtd-once topic was DROPPED (ProcessRtdUpdate skipped the scalar StoreResult
// for grid-once topics and the grid registry had no error kind). The grid-once
// wrapper reads only RtdOnceGridRegistry, so a failed handler left the cell
// showing the loading placeholder (grid) or an empty 0x0 FP12 (numgrid)
// FOREVER — every recalc missed the cache, re-issued xlfRtd against the STILL
// CONNECTED topic, ConnectData never re-fired, and the one-shot handler never
// re-ran. No error text, no self-heal.
//
// The contract asserted here:
//
//	(a) a transient error IS stored and read back as kError (a HIT) while its
//	    topic is live — the wrapper returns on that hit, which is what drops the
//	    cell's RTD reference and lets Excel disconnect the topic;
//	(b) once the topic disconnects, ClearNonMemoized erases it EVEN FOR
//	    memoize:true / memoize_ttl — so the next recalc re-runs the handler;
//	(c) the read-side miss, the memoize/ttl/once controls, and both promotion
//	    directions (grid over error, error over grid).
//
// Requires g++ (MinGW/MSYS2). Skipped (not failed) when it is absent, and under
// -short, like the heavier cmake gates. Unlike the scalar twin this needs NO
// types checkout: xll_rtd_once_grid.h includes only <windows.h> + the STL.
func TestRtdOnceGridRegistryErrorBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll_rtd_once_grid.h is Windows-only (windows.h / GetTickCount64)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping xll_rtd_once_grid.h compile+run gate")
	}

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_rtd_once_grid.h"]
	if !ok {
		t.Fatalf("embedded include/xll_rtd_once_grid.h not found in assets")
	}

	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "xll_rtd_once_grid.h"), []byte(hdr), 0o644); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(dir, "driver.cpp")
	if err := os.WriteFile(srcPath, []byte(rtdOnceGridErrorDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	exePath := filepath.Join(dir, "driver.exe")

	// gnu++17 (not c++17) to match the real build (CMAKE_CXX_STANDARD 17 with
	// CMAKE_CXX_EXTENSIONS default ON) and the scalar twin's gate.
	build := exec.Command(gxx,
		"-std=gnu++17", "-DXLL_RTD_ENABLED",
		"-I", incDir,
		"-o", exePath, srcPath,
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("xll_rtd_once_grid.h driver failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("RtdOnceGridRegistry transient/error contract violated: %v\n%s", err, out)
	}
}
