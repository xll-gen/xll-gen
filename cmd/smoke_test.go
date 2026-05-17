//go:build windows && xll_smoke

// Real-Excel end-to-end smoke test. Opt in with:
//
//	go test -tags=xll_smoke -run TestSmoke ./cmd/...
//
// Requires: Excel installed, cmake on PATH, a C++ toolchain (MSVC or MinGW),
// and Go on PATH. Sets/overwrites SHM "xll_smoke" — do not run concurrently
// with another xll-gen instance using that name. First run does a full
// FetchContent (flatbuffers/shm/types/phmap), which can take a minute on a
// cold machine; subsequent runs can be accelerated by setting:
//
//	XLL_SMOKE_FETCHCACHE=<dir>   # forwarded as -DFETCHCONTENT_BASE_DIR=
//	XLL_SMOKE_KEEP_DIR=<dir>     # reuse a project workdir across runs
package cmd

import (
	"os"
	"testing"

	"github.com/xll-gen/xll-gen/internal/smoketest"
)

func TestSmoke_All(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping XLL smoke test in short mode")
	}

	res := smoketest.Run(smoketest.Options{
		FetchContentCache: os.Getenv("XLL_SMOKE_FETCHCACHE"),
	})

	if res.ProjectDir != "" {
		t.Logf("project dir: %s", res.ProjectDir)
	}
	if res.XLLPath != "" {
		t.Logf("xll: %s", res.XLLPath)
	}
	if res.ServerExe != "" {
		t.Logf("server: %s", res.ServerExe)
	}
	for _, c := range res.Cases {
		if c.Err != nil {
			t.Errorf("[%s] %s: err=%v (after %s)", c.Label, c.Formula, c.Err, c.Duration)
			continue
		}
		t.Logf("[%s] %s = %v (%T) in %s (want %d)", c.Label, c.Formula, c.Got, c.Got, c.Duration, c.Want)
	}

	if res.Err != nil {
		t.Fatalf("smoke run failed: %v", res.Err)
	}
	if res.HasFailure() {
		t.Fatal("one or more cases failed (see logs)")
	}
}
