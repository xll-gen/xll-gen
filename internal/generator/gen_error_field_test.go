package generator

import (
	"regexp"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/templates"
)

// templateSource returns the raw (unrendered) text of an embedded template, for
// assertions that must hold for EVERY config rather than for one rendered
// instance.
func templateSource(t *testing.T, name string) string {
	t.Helper()
	src, err := templates.Get(name)
	if err != nil {
		t.Fatalf("templates.Get(%q): %v", name, err)
	}
	return src
}

// errFieldCfg builds a config exercising every return type the empty-error
// defect could reach: `string` (the crash), `int`/`float`/`bool` (the silent
// 0/0.0/FALSE), `any` (the blank cell) and `grid` (the one type that was already
// defended). A cache-enabled variant is included because the cache-HIT path
// re-uses the very same returnConversion template block.
func errFieldCfg(cacheEnabled bool) *config.Config {
	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "RetStr", Return: "string", Args: []config.Arg{}},
			{Name: "RetInt", Return: "int", Args: []config.Arg{}},
			{Name: "RetFloat", Return: "float", Args: []config.Arg{}},
			{Name: "RetBool", Return: "bool", Args: []config.Arg{}},
			{Name: "RetAny", Return: "any", Args: []config.Arg{}},
			{Name: "RetGrid", Return: "grid", Args: []config.Arg{}},
			{Name: "RetNumGrid", Return: "numgrid", Args: []config.Arg{}},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	*cfg.Server.Launch.Enabled = true
	if cacheEnabled {
		cfg.Cache = config.CacheConfig{Enabled: true, TTL: "1s", Jitter: "0s"}
	}
	return cfg
}

// TestGenCpp_EmptyErrorMessageDoesNotDereferenceNullResult pins the C++ half of
// the empty-error fix (HIGH, 2026-07-29).
//
// THE DEFECT. The generated server writes Response.result and Response.error in
// an EXCLUSIVE if/else, so an errored response carries no result field at all —
// and flatc's accessor answers nullptr for an absent field. But
// `b.CreateString("")` returns a NON-ZERO offset, so an error whose Error() is
// the empty string still PRODUCED the error field, with size 0. The wrapper's
// guard required `size() > 0`, so that response fell through to the result path:
//
//	std::wstring wres = StringToWString(resp->result()->str());   // nullptr->str()
//
// which read length_ off offset 0 and took the Excel process down. A generated
// sync UDF is outside XLL_SAFE_BLOCK/__try, so nothing contained the access
// violation and the user lost unsaved workbooks. int/float/bool silently painted
// the FlatBuffers default 0/0.0/FALSE instead — a wrong answer with no error.
//
// The fix is presence-only (`if (resp->error())`) plus a null-result check on the
// string path. `return "", errors.New("")` was enough to trigger the crash.
func TestGenCpp_EmptyErrorMessageDoesNotDereferenceNullResult(t *testing.T) {
	t.Parallel()

	for _, cached := range []bool{false, true} {
		name := "cache_off"
		if cached {
			name = "cache_on"
		}
		t.Run(name, func(t *testing.T) {
			content := renderCppMain(t, errFieldCfg(cached))

			// 1. The guard is PRESENCE-only. The rejected shape must not appear
			//    anywhere: it is what routed an empty message to the result path.
			if strings.Contains(content, "resp->error()->size() > 0") {
				t.Errorf("the error guard still requires size() > 0; an empty handler message " +
					"would fall through to resp->result(), which is nullptr for an errored response")
			}
			if !strings.Contains(content, "if (resp->error()) {") {
				t.Errorf("missing the presence-only error guard `if (resp->error()) {`:\n%s", content)
			}

			// 2. The string return path must refuse a null result instead of
			//    dereferencing it. Assert the guard sits BEFORE the deref.
			guard := strings.Index(content, "if (!resp->result()) {")
			deref := strings.Index(content, "StringToWString(resp->result()->str())")
			if guard < 0 {
				t.Errorf("string return path has no `if (!resp->result())` guard:\n%s", content)
			}
			if deref < 0 {
				t.Fatalf("string return path no longer dereferences resp->result(); update this test")
			}
			if guard >= 0 && guard > deref {
				t.Errorf("the null-result guard (offset %d) must come BEFORE the deref (offset %d)", guard, deref)
			}

			// 3. Both the guard and the deref must appear once per string-returning
			//    function per emission site. With cache ON the returnConversion
			//    block is emitted TWICE (cache-hit path + post-send path), and the
			//    original report noted the cache-hit path reproduces identically —
			//    so the count is what proves the fix covers both.
			wantSites := 1
			if cached {
				wantSites = 2
			}
			if got := strings.Count(content, "if (!resp->result()) {"); got != wantSites {
				t.Errorf("null-result guard emitted %d times, want %d (one per returnConversion site)", got, wantSites)
			}
			if got := strings.Count(content, "StringToWString(resp->result()->str())"); got != wantSites {
				t.Errorf("string result deref emitted %d times, want %d", got, wantSites)
			}

			// 4. numgrid is still excluded from the error branch (FP12*/K% cannot
			//    carry a string). Its wrapper must not contain the error guard.
			numGridBody := funcBody(t, content, "NumGridToFP12(resp->result())")
			if strings.Contains(numGridBody, "if (resp->error()) {") {
				t.Errorf("numgrid wrapper must not emit the string error branch")
			}
		})
	}
}

// funcBody returns a window of `content` around `marker`, back to the previous
// exported-function opener. Used to assert something is ABSENT from one specific
// wrapper rather than from the whole file.
func funcBody(t *testing.T, content, marker string) string {
	t.Helper()
	i := strings.Index(content, marker)
	if i < 0 {
		t.Fatalf("marker %q not found in generated source", marker)
	}
	start := strings.LastIndex(content[:i], `extern "C" __declspec(dllexport)`)
	if start < 0 {
		start = 0
	}
	return content[start : i+len(marker)]
}

// TestGenServer_EmptyErrorMessageIsNormalized pins the Go half. Fixing only one
// side leaves a live failure mode: the C++ guard alone paints an EMPTY cell
// against an older server, and this alone leaves the nullptr dereference in an
// XLL built before the guard changed.
func TestGenServer_EmptyErrorMessageIsNormalized(t *testing.T) {
	t.Parallel()
	srv := renderTemplate(t, "server.go.tmpl", serverDataFor(errFieldCfgWithAsync()))
	assertParses(t, "server.go", srv)

	// No raw err.Error()/verr.Error() may reach an error FIELD any more; every
	// such site must route through server.ErrorMessage.
	for _, banned := range []string{
		"b.CreateString(err.Error())",
		"protocol.AnyValue(0), err.Error())",
		"protocol.AnyValue(0), verr.Error())",
	} {
		if strings.Contains(srv, banned) {
			t.Errorf("server.go still writes a raw error message via %q; an empty message "+
				"produces a present-but-size-0 error field (sync) or is mistaken for a "+
				"SUCCESS (async, where `res.Err != \"\"` selects the error branch)", banned)
		}
	}

	if !strings.Contains(srv, "b.CreateString(server.ErrorMessage(err))") {
		t.Errorf("sync response builder does not normalize the error message:\n%s", srv)
	}
	if !strings.Contains(srv, "protocol.AnyValue(0), server.ErrorMessage(err))") {
		t.Errorf("async result queue does not normalize the error message:\n%s", srv)
	}

	// A "Server Busy" fast-fail is a literal, not an error value; it must stay.
	if !strings.Contains(srv, `"Server Busy"`) {
		t.Errorf("the async worker-pool-full fast-fail message was lost")
	}
}

// errFieldCfgWithAsync adds an async function so the QueueResult error sites are
// rendered too (they only exist on the async branch).
func errFieldCfgWithAsync() *config.Config {
	cfg := errFieldCfg(false)
	cfg.Functions = append(cfg.Functions,
		config.Function{Name: "AsyncStr", Mode: "async", Async: true, Return: "string", Args: []config.Arg{}},
		config.Function{Name: "AsyncGrid", Mode: "async", Async: true, Return: "grid", Args: []config.Arg{}},
		config.Function{Name: "AsyncNumGrid", Mode: "async", Async: true, Return: "numgrid", Args: []config.Arg{}},
	)
	return cfg
}

// TestServerErrorMessageIsUsedForEveryErrorField is a belt-and-braces scan of
// the TEMPLATE SOURCE (not one rendered instance): a future return type that
// adds another error-field site would otherwise slip past the rendered-output
// assertions above if this test's config does not happen to include it.
func TestServerErrorMessageIsUsedForEveryErrorField(t *testing.T) {
	t.Parallel()
	src := templateSource(t, "server.go.tmpl")

	// Any *.Error() feeding an error field is the banned shape. Logging calls
	// ("error", err) are untouched and use a different syntax, so a plain
	// `.Error()` scan is precise enough here.
	re := regexp.MustCompile(`(?:CreateString|AnyValue\(0\)),?\s*\)?\s*\w*\.Error\(\)`)
	if loc := re.FindString(src); loc != "" {
		t.Errorf("server.go.tmpl still feeds a raw .Error() into an error field: %q", loc)
	}
	if n := strings.Count(src, "server.ErrorMessage("); n < 4 {
		t.Errorf("server.go.tmpl calls server.ErrorMessage only %d times; the sync builder, "+
			"the async queue and both grid validators are expected to use it", n)
	}
}
