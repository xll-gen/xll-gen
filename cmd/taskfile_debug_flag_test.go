package cmd

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/templates"
)

// cmakeConfigureLine is one `cmake -S ... -B ...` invocation found in the
// generated Taskfile, reduced to the three things that decide whether a debug
// configure can contaminate a later release build.
type cmakeConfigureLine struct {
	raw       string
	buildDir  string // the -B argument
	buildType string // the -DCMAKE_BUILD_TYPE argument
	debugFlag string // "" when -DXLL_DEBUG is not passed at all
}

// parseCmakeConfigureLines extracts every cmake CONFIGURE command (i.e. a
// `cmake -S`, not a `cmake --build`) from the raw Taskfile template. Working on
// the template rather than a rendered project is deliberate: both the
// `singlefile: xll` branch and the plain branch are present in the same text,
// so one pass covers every mode the generator can emit.
func parseCmakeConfigureLines(t *testing.T, tmpl string) []cmakeConfigureLine {
	t.Helper()
	var out []cmakeConfigureLine
	for _, ln := range strings.Split(tmpl, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.Contains(ln, "cmake ") || !strings.Contains(ln, "-S ") {
			continue
		}
		fields := strings.Fields(ln)
		c := cmakeConfigureLine{raw: ln}
		for i, f := range fields {
			switch {
			case f == "-B" && i+1 < len(fields):
				c.buildDir = fields[i+1]
			case strings.HasPrefix(f, "-DCMAKE_BUILD_TYPE="):
				c.buildType = strings.TrimPrefix(f, "-DCMAKE_BUILD_TYPE=")
			case strings.HasPrefix(f, "-DXLL_DEBUG="):
				c.debugFlag = strings.TrimPrefix(f, "-DXLL_DEBUG=")
			}
		}
		out = append(out, c)
	}
	return out
}

// TestTaskfileReleaseConfigureClearsXllDebug pins the fix for the one-way debug
// contamination bug.
//
// The regression (MED, DX/artifact defect): `configure-cpp-debug` /
// `build-cpp-debug` pass `-DXLL_DEBUG=ON`, and BOTH build types configure into
// the SAME tree, `build/cpp`. CMake keeps every cache variable that a later run
// does not set, so the `ON` written by one `xll-gen build --debug` survives into
// every subsequent release configure. From then on each "release" build emits an
// XLL compiled with SHM_DEBUG + XLL_DEBUG_LOGGING (CMakeLists.txt.tmpl), while
// the Go server exe — built by a separate `go build` that has no such sticky
// state — stays a normal release binary. Nothing in the output says so, and the
// only escape is `task clean`. That is a shipped-artifact defect, not just a
// slow build: the developer believes they are distributing a release XLL.
//
// The fix is to make the release configure state the flag explicitly. The test
// is written against the HAZARD rather than the literal fix, so it stays honest
// if someone later splits the build trees instead: a release configure only has
// to clear XLL_DEBUG when it shares a build directory with a debug configure.
func TestTaskfileReleaseConfigureClearsXllDebug(t *testing.T) {
	tmpl, err := templates.Get("Taskfile.yml.tmpl")
	if err != nil {
		t.Fatalf("templates.Get(Taskfile.yml.tmpl): %v", err)
	}

	lines := parseCmakeConfigureLines(t, tmpl)
	if len(lines) == 0 {
		t.Fatal("no `cmake -S ... -B ...` configure commands found in Taskfile.yml.tmpl; " +
			"the parser (or the template) changed shape — update this gate rather than deleting it")
	}

	// Build dirs that some DEBUG configure writes -DXLL_DEBUG=ON into.
	debugPoisoned := map[string]bool{}
	sawDebug := false
	for _, c := range lines {
		if c.buildType == "Debug" {
			sawDebug = true
			if c.debugFlag != "ON" {
				t.Errorf("debug configure no longer passes -DXLL_DEBUG=ON:\n  %s", c.raw)
			}
			if c.debugFlag == "ON" {
				debugPoisoned[c.buildDir] = true
			}
		}
	}
	if !sawDebug {
		t.Fatal("Taskfile.yml.tmpl has no Debug cmake configure; this gate assumes the " +
			"release/debug pair exists")
	}

	sawRelease := false
	for _, c := range lines {
		if c.buildType != "Release" {
			continue
		}
		sawRelease = true
		if !debugPoisoned[c.buildDir] {
			// Separate trees: no sticky cache to inherit.
			continue
		}
		if c.debugFlag != "OFF" {
			t.Errorf("release configure shares the build tree %q with a -DXLL_DEBUG=ON debug "+
				"configure but does not pass -DXLL_DEBUG=OFF, so one `xll-gen build --debug` "+
				"leaves the cache entry ON and every later release build silently produces a "+
				"debug-instrumented XLL (SHM_DEBUG + XLL_DEBUG_LOGGING) until `task clean`:\n  %s",
				c.buildDir, c.raw)
		}
	}
	if !sawRelease {
		t.Fatal("Taskfile.yml.tmpl has no Release cmake configure")
	}
}
