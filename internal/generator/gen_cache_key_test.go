package generator

// Regression for the cache-key collection ladder in xll_main.cpp.tmpl (the
// per-arg branch that fills `cacheArgs` before xll::MakeCacheKey). Before the
// fix the ladder handled only int/float/bool/string and sent every other type
// through a blanket `cacheArgs.push_back((LPXLOPER12){{.Name}})`:
//
//   - date (ArgCppType "double") -> `(LPXLOPER12)d` — an ill-formed cast from a
//     `double` to a pointer that FAILS TO COMPILE (GCC "invalid cast", MSVC
//     C2440) for any cache-enabled non-async function with a date arg.
//   - numgrid (FP12*)            -> `(LPXLOPER12)ng` — a reinterpret cast that
//     makes MakeCacheKey read the FP12 double payload as an XLOPER (silent
//     mis-key / possible AV).
//
// grid/range/any are genuine LPXLOPER12s and MakeCacheKey content-hashes them
// (SerializeXLOPER / GetOrComputeRefHash), so their blanket cast is correct and
// intentionally preserved. These asserts pin the fix independently of the big
// golden snapshot.

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// renderCacheCppMain renders xll_main.cpp for a cache-enabled project with the
// given functions, mirroring generateCppMain's render struct.
func renderCacheCppMain(t *testing.T, fns []config.Function) string {
	t.Helper()
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "CacheKeyProj", Version: "0.1.0"},
		Build:   config.BuildConfig{Singlefile: "xll", TempDir: "temp"},
		Logging: config.LoggingConfig{Level: "info", Dir: "logs"},
		Cache:   config.CacheConfig{Enabled: true, TTL: "10m"},
		Server: config.ServerConfig{
			Timeout: "2s", Workers: 4,
			Launch: &config.LaunchConfig{Enabled: boolPtr(true)},
		},
		Functions: fns,
	}
	config.ApplyDefaults(cfg)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("fixture failed config.Validate: %v", err)
	}

	cppData := struct {
		ProjectName     string
		Functions       []config.Function
		Events          []config.Event
		Server          config.ServerConfig
		Build           config.BuildConfig
		ShouldAppendPid bool
		Version         string
		Logging         config.LoggingConfig
		Cache           config.CacheConfig
		Rtd             config.RtdConfig
		Ribbon          config.RibbonConfig
		Commands        []config.Command
	}{
		ProjectName: cfg.Project.Name,
		Functions:   cfg.Functions,
		Server:      cfg.Server,
		Build:       cfg.Build,
		Version:     "test",
		Logging:     cfg.Logging,
		Cache:       cfg.Cache,
	}
	return renderTemplate(t, "xll_main.cpp.tmpl", cppData)
}

// TestCacheKeyLadder_NoBlanketPointerCast pins that a cache-enabled function
// with a date and a numgrid arg never emits the ill-formed / mis-keying blanket
// `(LPXLOPER12)<arg>` cast for those types, and that each is handled correctly:
// date via a temp xltypeNum XLOPER, numgrid via a ContentHashTokenFP12 folded
// into the cache key.
func TestCacheKeyLadder_NoBlanketPointerCast(t *testing.T) {
	out := renderCacheCppMain(t, []config.Function{
		{Name: "CacheDate", Mode: "sync", Return: "int",
			Args: []config.Arg{{Name: "d", Type: "date"}}},
		{Name: "CacheNumGrid", Mode: "sync", Return: "int",
			Args: []config.Arg{{Name: "ng", Type: "numgrid"}}},
	})

	// The two buggy casts must be gone.
	if strings.Contains(out, "(LPXLOPER12)d)") {
		t.Errorf("date arg still emits the ill-formed blanket cast `(LPXLOPER12)d)` (would not compile)")
	}
	if strings.Contains(out, "(LPXLOPER12)ng)") {
		t.Errorf("numgrid arg still emits the mis-keying blanket cast `(LPXLOPER12)ng)`")
	}

	// date must be wrapped in a temp xltypeNum XLOPER (same as float).
	if !strings.Contains(out, "xArg.val.num = d;") {
		t.Errorf("date arg not wrapped into a temp xltypeNum XLOPER (expected `xArg.val.num = d;`)")
	}

	// numgrid content must be folded into the cache key via ContentHashTokenFP12.
	if !strings.Contains(out, "xll::ContentHashTokenFP12(ng)") {
		t.Errorf("numgrid arg content hash not folded into the cache key (expected `xll::ContentHashTokenFP12(ng)`)")
	}
}

// TestCacheKeyLadder_CompositePointerArgsPreserved pins that grid/range/any
// args — genuine LPXLOPER12s that MakeCacheKey already content-hashes — keep
// their (correct, well-formed) blanket pointer push and are NOT rerouted.
func TestCacheKeyLadder_CompositePointerArgsPreserved(t *testing.T) {
	out := renderCacheCppMain(t, []config.Function{
		{Name: "CacheComposite", Mode: "sync", Return: "int",
			Args: []config.Arg{
				{Name: "g", Type: "grid"},
				{Name: "r", Type: "range"},
				{Name: "a", Type: "any"},
			}},
	})
	for _, want := range []string{
		"cacheArgs.push_back((LPXLOPER12)g);",
		"cacheArgs.push_back((LPXLOPER12)r);",
		"cacheArgs.push_back((LPXLOPER12)a);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("composite pointer arg push missing/altered: expected %q", want)
		}
	}
}

// TestCacheKeyLadder_StringArgNoUndefinedHelper pins that a cache-enabled
// function with a string arg no longer calls CreateStringXLOPER — a helper
// defined nowhere in xll-gen/types/assets, so the old branch never compiled for
// any cache-enabled function with a string arg. The string arg is registered
// LPXLOPER12, so it is now pushed directly (Excel-owned; MakeCacheKey's
// SerializeXLOPER content-hashes its xltypeStr value).
func TestCacheKeyLadder_StringArgNoUndefinedHelper(t *testing.T) {
	out := renderCacheCppMain(t, []config.Function{
		{Name: "CacheString", Mode: "sync", Return: "int",
			Args: []config.Arg{{Name: "s", Type: "string"}}},
	})

	// The undefined-helper CALL must be gone (a comment may still name it).
	if strings.Contains(out, "CreateStringXLOPER(s") {
		t.Errorf("string arg still calls the undefined helper CreateStringXLOPER (would not compile)")
	}
	// The string arg (already an LPXLOPER12) must be pushed directly.
	if !strings.Contains(out, "cacheArgs.push_back(s);") {
		t.Errorf("string arg not pushed directly into cacheArgs (expected `cacheArgs.push_back(s);`)")
	}
	// The stale per-string cleanup that delete[]'d a never-allocated buffer must
	// be gone (it would be a delete[] on uninitialized tempArgs memory now).
	if strings.Contains(out, "delete[] tempArgs") {
		t.Errorf("stale `delete[] tempArgs[..].val.str` cleanup still emitted (UB now that no temp string is allocated)")
	}
}
