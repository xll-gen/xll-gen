package regtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestGenerateSimMainSkipsRtdFunctions pins WHICH functions the `xll-gen
// regtest` simulation host probes.
//
// The host's probe is a request/response round trip: it fills an
// ipc::<Name>Request into a zero-copy slot, Sends it as MsgUserStart+i, and
// then reads ipc::<Name>Response back out of the SAME slot. That only means
// anything for a function the generated Go dispatch has a case for — and
// server.go.tmpl emits a `case MsgUserStart+i` only for non-rtd-like modes
// ({{if not (isRtdLike .Mode)}}). An rtd / rtd-once function has NO case: its
// results arrive out-of-band through the RTD push path, and its message ID
// falls into the dispatch's `default: return 0, 0`.
//
// So probing an rtd function sent a message nothing handles and then read a
// Response out of a buffer nothing wrote — the request bytes reinterpreted as a
// Response. That is worse than useless: the probe reports "Success" (or
// dereferences a garbage vtable offset) instead of reporting that it cannot
// test this function. On an all-rtd project EVERY probe was of that kind, so
// the regtest was a green light with no signal behind it.
//
// The index is deliberately still taken from the FULL function list, exactly as
// server.go.tmpl and xll_main.cpp.tmpl do, so skipping an rtd function must not
// shift the IDs of the functions after it — this test pins that too.
func TestGenerateSimMainSkipsRtdFunctions(t *testing.T) {
	t.Run("mixed", testSimMainMixed)
	t.Run("all-rtd", testSimMainAllRtd)
}

func testSimMainMixed(t *testing.T) {
	cfg := &config.Config{}
	cfg.Project.Name = "simproj"
	cfg.Functions = []config.Function{
		{Name: "Alpha", Mode: "sync", Return: "int"},
		{Name: "Streamer", Mode: "rtd", Return: "float"},
		{Name: "Oncer", Mode: "rtd-once", Return: "float"},
		{Name: "Omega", Mode: "async", Return: "int"},
	}

	dir := t.TempDir()
	if err := generateSimMain(cfg, dir); err != nil {
		t.Fatalf("generateSimMain: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, "NO PROBEABLE FUNCTIONS") {
		t.Error("mixed project rendered the no-probeable-functions bail-out; Alpha and Omega are probeable")
	}

	for _, name := range []string{"Streamer", "Oncer"} {
		if strings.Contains(content, "ipc::"+name+"Request") ||
			strings.Contains(content, "ipc::"+name+"Response") {
			t.Errorf("simulation host probes rtd-like function %s: the generated Go dispatch has no case for its message ID (server.go.tmpl gates cases on `not (isRtdLike .Mode)`), so the probe sends into `default: return 0,0` and then reads a Response out of a buffer nothing wrote", name)
		}
	}
	for _, name := range []string{"Alpha", "Omega"} {
		if !strings.Contains(content, "Testing "+name+"...") {
			t.Errorf("simulation host dropped non-rtd function %s", name)
		}
	}

	// Index stability: Omega is the 4th function (index 3), so it must still be
	// MsgUserStart+3 even though indices 1 and 2 are now skipped.
	matches := simSendRe.FindAllStringSubmatch(content, -1)
	if len(matches) != 2 {
		t.Fatalf("want 2 probes (Alpha, Omega), got %d:\n%s", len(matches), content)
	}
	wantIDs := []string{"140", "143"}
	for i, m := range matches {
		if m[1] != wantIDs[i] {
			t.Errorf("probe %d sends msgType %s, want %s (index must stay the position in the FULL function list)", i, m[1], wantIDs[i])
		}
	}
}

// testSimMainAllRtd covers the case the mixed config above can NEVER reach: a
// project whose functions are ALL rtd/rtd-once, so the per-function
// `isRtdLike` skip removes every probe block.
//
// Skipping the probes was only half a fix. With zero blocks rendered, the
// generated main() ran nothing, left `failures` at 0 and returned 0 — and
// internal/regtest/runner.go decides pass/fail on the child's EXIT CODE alone
// (`if exitCode == 0 { "Simulation PASSED" }`). So `xll-gen regtest` on an
// all-rtd project printed PASSED for a run that probed nothing: the identical
// "green with no signal" the skip was justified by, one level up.
//
// The host must therefore announce the condition and exit non-zero. This test
// pins BOTH halves: no probe blocks, and a diagnostic + non-zero return in
// their place.
func testSimMainAllRtd(t *testing.T) {
	cfg := &config.Config{}
	cfg.Project.Name = "rtdonlyproj"
	cfg.Functions = []config.Function{
		{Name: "Streamer", Mode: "rtd", Return: "float"},
		{Name: "Oncer", Mode: "rtd-once", Return: "float"},
	}

	dir := t.TempDir()
	if err := generateSimMain(cfg, dir); err != nil {
		t.Fatalf("generateSimMain: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Half 1: nothing is probed.
	if m := simSendRe.FindAllStringSubmatch(content, -1); len(m) != 0 {
		t.Errorf("all-rtd project rendered %d probe(s); rtd-like functions have no dispatch case to probe", len(m))
	}
	for _, name := range []string{"Streamer", "Oncer"} {
		if strings.Contains(content, "ipc::"+name+"Request") ||
			strings.Contains(content, "ipc::"+name+"Response") {
			t.Errorf("simulation host probes rtd-like function %s", name)
		}
	}

	// Half 2: the emptiness is REPORTED, not silently returned as success.
	if !strings.Contains(content, "NO PROBEABLE FUNCTIONS") {
		t.Error("all-rtd simulation host does not announce that it has nothing to probe; runner.go reads the exit code only, so a silent main() renders as \"Simulation PASSED\"")
	}
	if !strings.Contains(content, "return 2;") {
		t.Error("all-rtd simulation host does not exit non-zero; with failures==0 and `return 0` the regtest is a vacuous pass")
	}
	// And it must not ALSO fall through to the success return: a `return 0`
	// reachable after the announcement would restore the vacuous pass.
	if strings.Contains(content, "return 0;") {
		t.Error("all-rtd simulation host still contains the success `return 0;` path; the bail-out must replace it, not precede it")
	}
}
