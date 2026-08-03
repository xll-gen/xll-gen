package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRegisterOfficeAddinKeyLoadBehaviorPolicy compiles the EMBEDDED
// include/rtd/registry.h offline against
// internal/assets/testdata/registry_addin_key_native_test.cpp and RUNS it.
//
// WHY A REAL-REGISTRY TEST AND NOT A SOURCE GREP. The unit is three Win32 calls
// with no seam to fake, and the defect it pins is an ORDER between two of them:
// RegisterOfficeAddinKey wrote LoadBehavior=0 unconditionally on every
// xlAutoOpen. That was harmless while the graceful teardown deleted
// HKCU\...\Excel\Addins\<progId> each session (there was never a prior value),
// and became a live defect the moment v0.8.50 stopped deleting it — Office
// records a user's re-tick in the COM Add-ins dialog as LoadBehavior=3, and the
// next load reset it to 0 behind their back. A grep can pin the SHAPE of the
// fix; only execution can show the value survives. The structural test below is
// the backstop for hosts where this one is skipped, not a substitute.
//
// BLAST RADIUS. The harness remaps HKEY_CURRENT_USER for its own process with
// RegOverridePredefKey, so the real HKCU\Software\Microsoft\Office\Excel\Addins
// is never opened; it aborts (exit 2) rather than run if the remap did not take,
// and its scratch ProgID carries the harness PID so even a failed remap could
// only create — and then delete — a uniquely named row. See the file header.
//
// Needs g++ only: registry.h pulls in nothing but <windows.h>/<string>/<vector>,
// so unlike the cache/gridarg harnesses there is no FetchContent cache and no
// types checkout involved. Skipped (not failed) without a toolchain, off
// Windows, and under -short.
func TestRegisterOfficeAddinKeyLoadBehaviorPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("rtd/registry.h is Windows-only (windows.h / RegCreateKeyExW)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping registry compile+run gate")
	}

	_, thisFile, ok := callerFile()
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/rtd/registry.h"]
	if !ok {
		t.Fatalf("embedded asset include/rtd/registry.h not found")
	}

	dir := t.TempDir()
	incDir := filepath.Join(dir, "include", "rtd")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "registry.h"), []byte(hdr), 0o644); err != nil {
		t.Fatal(err)
	}

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", "registry_addin_key_native_test.cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}
	exePath := filepath.Join(dir, "registry_addin_key_native_test.exe")

	// gnu++17 + -static mirror the other native harnesses (and the real build):
	// the MS spellings in the Windows headers need the non-strict dialect, and a
	// statically linked harness cannot die at load on a MinGW runtime DLL that
	// happens not to be first on PATH.
	args := []string{
		"-std=gnu++17", "-O1", "-DUNICODE", "-D_UNICODE", "-fexceptions",
		"-static", "-static-libgcc", "-static-libstdc++",
		"-I", filepath.Join(dir, "include"),
		"-o", exePath,
		harness,
		"-ladvapi32", "-lole32",
	}
	if out, err := exec.Command(gxx, args...).CombinedOutput(); err != nil {
		t.Fatalf("registry addin-key native harness failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	t.Logf("registry_addin_key_native_test output:\n%s", out)
	if err != nil {
		t.Fatalf("registry addin-key native harness reported failures (or crashed): %v", err)
	}
	if strings.Contains(string(out), "ABORT:") {
		t.Fatalf("registry addin-key native harness refused to run (registry sandbox not established)")
	}
	if !strings.Contains(string(out), "0 failures") {
		t.Fatalf("registry addin-key native harness did not report 0 failures")
	}
}

// TestRegisterOfficeAddinKeyReadsBeforeWriting is the ALWAYS-ON backstop for the
// test above, which skips without g++ / off Windows. It pins the two things the
// read-before-write shape cannot exist without: KEY_QUERY_VALUE in the access
// mask (RegCreateKeyExW asked for KEY_SET_VALUE alone, so the probe would fail
// with ERROR_ACCESS_DENIED and — depending on how the branch is written — either
// clobber every time or never write at all), and a LoadBehavior QUERY that
// precedes the LoadBehavior SET.
//
// It is deliberately NOT a substring check for "RegQueryValueExW": that would
// stay green if the query were moved after the write, which is the exact defect.
func TestRegisterOfficeAddinKeyReadsBeforeWriting(t *testing.T) {
	t.Parallel()

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/rtd/registry.h"]
	if !ok {
		t.Fatalf("embedded asset include/rtd/registry.h not found")
	}
	code := stripCppCommentsAsset(hdr)

	i := strings.Index(code, "inline HRESULT RegisterOfficeAddinKey(")
	if i < 0 {
		t.Fatalf("RegisterOfficeAddinKey not found in include/rtd/registry.h")
	}
	body := code[i:]
	if e := strings.Index(body, "inline HRESULT UnregisterOfficeAddinKey("); e > 0 {
		body = body[:e]
	}

	createIdx := strings.Index(body, "RegCreateKeyExW(")
	if createIdx < 0 {
		t.Fatalf("RegisterOfficeAddinKey must still create the key\n---\n%s", body)
	}
	createCall := body[createIdx:]
	if e := strings.Index(createCall, ";"); e > 0 {
		createCall = createCall[:e]
	}
	if !strings.Contains(createCall, "KEY_QUERY_VALUE") {
		t.Errorf("RegCreateKeyExW must request KEY_QUERY_VALUE as well as KEY_SET_VALUE: the "+
			"LoadBehavior existence probe reads through this very handle, and a KEY_SET_VALUE-only "+
			"handle makes it fail with ERROR_ACCESS_DENIED regardless of what is in the "+
			"registry\n---\n%s", createCall)
	}
	if !strings.Contains(createCall, "KEY_SET_VALUE") {
		t.Errorf("RegCreateKeyExW must still request KEY_SET_VALUE\n---\n%s", createCall)
	}

	queryIdx := strings.Index(body, "RegQueryValueExW(hKey, L\"LoadBehavior\"")
	setIdx := strings.Index(body, "RegSetValueExW(hKey, L\"LoadBehavior\"")
	if setIdx < 0 {
		t.Fatalf("RegisterOfficeAddinKey must still be able to write LoadBehavior (the fresh-install "+
			"case: the surviving Addins key must autoload nothing at Excel startup)\n---\n%s", body)
	}
	if queryIdx < 0 {
		t.Fatalf("RegisterOfficeAddinKey must PROBE LoadBehavior before writing it. It runs on every "+
			"xlAutoOpen, and since v0.8.50 the Addins key SURVIVES the session — so an unconditional "+
			"write silently resets the LoadBehavior=3 that Office records when the user re-ticks the "+
			"box in the COM Add-ins dialog\n---\n%s", body)
	}
	if queryIdx > setIdx {
		t.Errorf("the LoadBehavior probe must precede the LoadBehavior write (query@%d set@%d): a probe "+
			"after the write measures our own value and preserves nothing\n---\n%s",
			queryIdx, setIdx, body)
	}
}
