package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
	"github.com/xll-gen/xll-gen/internal/config"
)

// serverDataWithEvents builds the template input for server.go.tmpl with the
// given events, mirroring the anonymous struct generateServer passes.
func serverDataWithEvents(events []config.Event) interface{} {
	return struct {
		Package       string
		ModName       string
		ProjectName   string
		Functions     []config.Function
		Events        []config.Event
		Commands      []config.Command
		ServerTimeout string
		ServerWorkers int
		Version       string
		Logging       config.LoggingConfig
		Rtd           config.RtdConfig
		Chunk         *config.ChunkConfig
	}{
		Package:     "generated",
		ModName:     "testmod",
		ProjectName: "TestProj",
		Events:      events,
		Version:     "test",
		Logging:     config.LoggingConfig{Level: "info", Dir: "logs"},
	}
}

// TestGenServer_BuiltinEventHandlerName verifies the built-in calculation
// events dispatch to the configured custom handler name.
func TestGenServer_BuiltinEventHandlerName(t *testing.T) {
	t.Parallel()

	srv := renderTemplate(t, "server.go.tmpl", serverDataWithEvents([]config.Event{
		{Type: "CalculationEnded", Handler: "OnRecalc"},
	}))
	assertParses(t, "server.go", srv)

	if !strings.Contains(srv, "sysHandler.HandleCalculationEnded(respBuf, builder, handler.OnRecalc)") {
		t.Errorf("server.go: CalculationEnded must dispatch to the configured handler:\n%s", srv)
	}
}

// TestGenCpp_UserCalculationEndedDrainsSystemWork is the regression for the
// showcase YDH "raw serials instead of dates" bug.
//
// When a project declares a user CalculationEnded event handler (e.g. renamed
// to OnRecalc), the generator emitted ONLY a named-event stub that logged
// "Event CalculationEnded triggered" and did NOT call HandleCalculationEnded().
// The built-in CalculationEnded() macro that DOES call HandleCalculationEnded()
// is suppressed (`{{if hasEvent "CalculationEnded"}}`) whenever a user handler
// exists. Net effect: the date auto-format drain (DrainAndApplyDateFormats),
// the RefCache clear, the rtd-once ClearNonMemoized, AND the MSG_CALCULATION_ENDED
// round-trip that invokes the user's Go handler all silently stop running.
// That is exactly why the minimal smoketest (NO user CalculationEnded event)
// formats dates correctly while the full showcase (HAS one) leaves raw serials.
//
// The fix: the named-event handler, when its type is CalculationEnded, must
// route through HandleCalculationEnded() like the built-in macro does.
func TestGenCpp_UserCalculationEndedDrainsSystemWork(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "MakeDateGrid", Return: "grid", Args: []config.Arg{}},
		},
		Events: []config.Event{
			{Type: "CalculationEnded", Handler: "OnRecalc"},
		},
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
	cpp := renderCppMain(t, cfg)

	// The user-renamed handler must be the registered CalculationEnded callback.
	if !strings.Contains(cpp, `xll::CallExcel(xlEventRegister, nullptr, L"OnRecalc", xleventCalculationEnded);`) {
		t.Errorf("render must register the user handler OnRecalc as the CalculationEnded callback:\n%s", cpp)
	}
	// And that handler MUST call HandleCalculationEnded() — otherwise the
	// date-format drain (and all other calc-end work) never runs.
	if !strings.Contains(cpp, "HandleCalculationEnded();") {
		t.Errorf("user CalculationEnded handler (OnRecalc) must call HandleCalculationEnded() so the date-format drain runs:\n%s", cpp)
	}
	// Belt-and-suspenders: the named-event stub must NOT be a log-only no-op for
	// the CalculationEnded type. Find the OnRecalc handler body and assert it
	// contains the drain call rather than only the placeholder log.
	const sig = "void __stdcall OnRecalc()"
	start := strings.Index(cpp, sig)
	if start < 0 {
		t.Fatalf("OnRecalc handler not emitted:\n%s", cpp)
	}
	body := cpp[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "HandleCalculationEnded();") {
		t.Errorf("OnRecalc body must call HandleCalculationEnded(), got:\n%s", body)
	}
}

// cancelEventCfg builds a minimal config, optionally declaring the
// CalculationCanceled event with the given handler name.
func cancelEventCfg(events ...config.Event) *config.Config {
	return &config.Config{
		Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
		Functions: []config.Function{
			{Name: "EchoInt", Return: "int", Args: []config.Arg{{Name: "v", Type: "int"}}},
		},
		Events: events,
		Server: config.ServerConfig{
			Timeout: "2s",
			Launch:  &config.LaunchConfig{Enabled: new(bool)},
		},
	}
}

// handlerBody extracts the emitted body of an exported __stdcall proc.
func handlerBody(t *testing.T, cpp, name string) string {
	t.Helper()
	sig := "void __stdcall " + name + "()"
	start := strings.Index(cpp, sig)
	if start < 0 {
		t.Fatalf("handler %s not emitted:\n%s", name, cpp)
	}
	body := cpp[start:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	return body
}

// TestGenCpp_CalculationCanceledForwardsToServer is the regression for the
// named-event stub that made a user-declared CalculationCanceled a total no-op.
//
// The `{{range .Events}}` registration loop always emitted the xlEventRegister
// call, so Excel DID invoke the exported handler — but the stub's `{{else}}`
// branch ("Currently not fully implemented for named events") only logged. It
// never sent MSG_CALCULATION_CANCELED (132), so the entire Go chain
// (HandleCalculationCanceled -> OnCalculationCanceled) was dead code in every
// generated project.
//
// FAIL-before: on the parent template the body contains only the placeholder
// log, so the HandleCalculationCanceled() assertion fails.
func TestGenCpp_CalculationCanceledForwardsToServer(t *testing.T) {
	t.Parallel()

	for _, handler := range []string{"OnCalculationCanceled", "OnEsc"} {
		t.Run(handler, func(t *testing.T) {
			t.Parallel()

			cpp := renderCppMain(t, cancelEventCfg(config.Event{
				Type: "CalculationCanceled", Handler: handler,
			}))

			// Registered against Excel's cancel event...
			want := `xll::CallExcel(xlEventRegister, nullptr, L"` + handler + `", xleventCalculationCanceled);`
			if !strings.Contains(cpp, want) {
				t.Errorf("missing cancel-event registration %q:\n%s", want, cpp)
			}

			// ...and the exported proc must actually forward it.
			body := handlerBody(t, cpp, handler)
			if !strings.Contains(body, "HandleCalculationCanceled();") {
				t.Errorf("%s body must call HandleCalculationCanceled() (sends MSG_CALCULATION_CANCELED); got:\n%s", handler, body)
			}
			// The dead placeholder must be gone for this type.
			if strings.Contains(body, "not fully implemented") || strings.Contains(body, "not forwarded to the server") {
				t.Errorf("%s body still carries the unimplemented named-event stub:\n%s", handler, body)
			}
			// Constraint: the cancel path must NOT do calc-end work. Both events
			// fire for one interrupted cycle (AGENTS.md §19.4) and splitting the
			// clears desynchronizes g_sentRefCache from the Go RefCache.
			if strings.Contains(body, "HandleCalculationEnded();") {
				t.Errorf("%s must not invoke the calc-END path:\n%s", handler, body)
			}
		})
	}
}

// TestGenCpp_CalculationCanceledNotEmittedWhenUndeclared pins the opt-in half:
// a project that does not declare the event must neither register a cancel
// callback with Excel nor emit the round-trip, so it pays nothing.
func TestGenCpp_CalculationCanceledNotEmittedWhenUndeclared(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		events []config.Event
	}{
		{"no events", nil},
		{"only CalculationEnded", []config.Event{{Type: "CalculationEnded", Handler: "OnRecalc"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cpp := renderCppMain(t, cancelEventCfg(tc.events...))

			if strings.Contains(cpp, "xleventCalculationCanceled") {
				t.Errorf("undeclared CalculationCanceled must not be registered with Excel:\n%s", cpp)
			}
			if strings.Contains(cpp, "HandleCalculationCanceled()") {
				t.Errorf("undeclared CalculationCanceled must not emit the MSG_CALCULATION_CANCELED round-trip:\n%s", cpp)
			}
		})
	}
}

// TestCalcCanceled_AssetIsNotificationOnly pins the shipped runtime asset: the
// cancel round-trip must do NO cache work.
//
// Excel fires CalculationCanceled and then CalculationEnded 2-6 ms later for
// one interrupted cycle (measured, AGENTS.md §19.4), so the Ended path already
// owns every per-cycle clear. Clearing here would additionally desynchronize
// the C++ g_sentRefCache from the Go RefCache — they stay consistent only
// because a SINGLE event clears both.
func TestCalcCanceled_AssetIsNotificationOnly(t *testing.T) {
	t.Parallel()

	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	src, ok := m["src/xll_events.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_events.cpp not found in assets")
	}

	const sig = "void HandleCalculationCanceled()"
	start := strings.Index(src, sig)
	if start < 0 {
		t.Fatalf("HandleCalculationCanceled not found in xll_events.cpp:\n%s", src)
	}
	body := src[start:]
	if end := strings.Index(body[len(sig):], "\n    void "); end >= 0 {
		body = body[:len(sig)+end]
	}
	// Strip comments: the rationale text names the very calls we forbid.
	var code strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	stripped := code.String()

	for _, forbidden := range []string{
		"g_sentRefCache",
		"ClearRefCache",
		"ClearNonMemoized",
		"DeferCalcEndCommands",
		"RefreshIterativeCalcMode",
	} {
		if strings.Contains(stripped, forbidden) {
			t.Errorf("HandleCalculationCanceled must not touch %s — the CalculationEnded "+
				"that follows 2-6 ms later owns all per-cycle clears (AGENTS.md 19.4):\n%s", forbidden, stripped)
		}
	}
	// It must send the message, synchronously (the blocking send is what keeps
	// the Canceled -> Ended handler ordering).
	if !strings.Contains(stripped, "MSG_CALCULATION_CANCELED") {
		t.Errorf("HandleCalculationCanceled must send MSG_CALCULATION_CANCELED:\n%s", stripped)
	}
	if !strings.Contains(stripped, "g_host.Send(") {
		t.Errorf("HandleCalculationCanceled must use the synchronous g_host.Send round-trip:\n%s", stripped)
	}
}

// TestGenServer_CalculationCanceledDispatch pins the guest-side dispatch: the
// cancel message must route to HandleCalculationCanceled with the CONFIGURED
// handler name (not just the default), otherwise a renamed handler silently
// never runs.
func TestGenServer_CalculationCanceledDispatch(t *testing.T) {
	t.Parallel()

	srv := renderTemplate(t, "server.go.tmpl", serverDataWithEvents([]config.Event{
		{Type: "CalculationCanceled", Handler: "OnEsc"},
	}))
	assertParses(t, "server.go", srv)

	if !strings.Contains(srv, "sysHandler.HandleCalculationCanceled(handler.OnEsc)") {
		t.Errorf("server.go: CalculationCanceled must dispatch to the configured handler:\n%s", srv)
	}
	// The cancel must NOT be routed through the async jobQueue block: that would
	// let OnCalculationCanceled land after OnCalculationEnded.
	if strings.Contains(srv, `log.Warn("Event dropped due to server load", "event", "CalculationCanceled")`) {
		t.Errorf("server.go: CalculationCanceled must not ride the fire-and-forget jobQueue path:\n%s", srv)
	}
}

// TestGenServer_NonBuiltinEventUsesHandlerField is the regression for the
// template referencing `handler.{{.Name}}` in the non-builtin event dispatch
// block: config.Event has no Name field (only Type/Handler), so rendering a
// config with any event type other than CalculationEnded/CalculationCanceled
// failed with "can't evaluate field Name". The dispatch must use .Handler.
//
// Note: config.Validate rejects unknown event types at config-load time, so
// this path is only reachable if the supported-event whitelist grows — this
// test pins the template/schema consistency for that day.
func TestGenServer_NonBuiltinEventUsesHandlerField(t *testing.T) {
	t.Parallel()

	srv := renderTemplate(t, "server.go.tmpl", serverDataWithEvents([]config.Event{
		{Type: "SheetActivated", Handler: "OnSheetActivated"},
	}))
	assertParses(t, "server.go", srv)

	if !strings.Contains(srv, "handler.OnSheetActivated(ctx)") {
		t.Errorf("server.go: non-builtin event must dispatch to .Handler:\n%s", srv)
	}
}
