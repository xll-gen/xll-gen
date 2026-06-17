package generator

import (
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/pkg/server"
)

// cppWideLiteral renders s as a C++ wide-string literal (L"...") that is safe
// regardless of the generated file's source encoding or the compiler's wide
// execution charset: every non-printable-ASCII rune is emitted as a universal
// character name (\uXXXX / \UXXXXXXXX), and the C escapes are applied to quotes,
// backslashes, and the common control characters.
func cppWideLiteral(s string) string {
	var b strings.Builder
	b.WriteString(`L"`)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			switch {
			case r < 0x20 || r > 0x7e:
				if r > 0xffff {
					fmt.Fprintf(&b, `\U%08X`, r)
				} else {
					fmt.Fprintf(&b, `\u%04X`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// anyRtdOnceGrid reports whether the project declares at least one rtd-once
// function whose return is a grid/numgrid. Used to gate emission of the
// rtd-once grid-spill C++/Go codegen (the once-result-to-array machinery),
// mirroring how anyRtdOnce gates the scalar rtd-once machinery.
func anyRtdOnceGrid(fns []config.Function) bool {
	for _, f := range fns {
		if f.Mode == "rtd-once" && (f.Return == "grid" || f.Return == "numgrid") {
			return true
		}
	}
	return false
}

// anyDateType reports whether any function takes a date argument or returns a
// date. Used to gate the `"time"` import in interface.go.tmpl: the handler
// interface references time.Time only when a date appears, so importing time
// unconditionally would be an unused import in date-free projects.
func anyDateType(fns []config.Function) bool {
	for _, f := range fns {
		if f.Return == "date" {
			return true
		}
		for _, a := range f.Args {
			if a.Type == "date" {
				return true
			}
		}
	}
	return false
}

// Helper to check if a specific event type is registered
func hasEvent(eventType string, events []config.Event) bool {
	for _, e := range events {
		if e.Type == eventType {
			return true
		}
	}
	return false
}

// Helper to get the handler name for a specific event type
func getEventHandler(eventType string, events []config.Event, defaultHandler string) string {
	for _, e := range events {
		if e.Type == eventType {
			return e.Handler
		}
	}
	return defaultHandler
}

// escapeCppString escapes a config-supplied free-text string for emission
// inside a C++ (wide) string literal. Names/shortcuts are charset-validated
// at config time, but descriptions, categories and help topics accept
// arbitrary text — an interior quote, backslash or newline would otherwise
// terminate or corrupt the generated literal. Other control characters
// (notably an embedded NUL, reachable via adversarial/pasted YAML) are
// escaped as \uXXXX so they cannot truncate the emitted C string, mirroring
// the sibling cppWideLiteral helper.
func escapeCppString(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// Helper to parse duration string to milliseconds
func parseDurationToMs(s string, defaultMs int) int {
	if s == "" {
		return defaultMs
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultMs
	}
	return int(d.Milliseconds())
}

// GetCommonFuncMap returns a map of common template functions used across different generators.
// This centralization ensures consistency and avoids code duplication.
func GetCommonFuncMap() template.FuncMap {
	return template.FuncMap{
		"hasEvent":        hasEvent,
		"anyDateType":     anyDateType,
		"getEventHandler": getEventHandler,
		"escapeCppString": escapeCppString,
		"derefBool": func(b *bool) bool {
			if b == nil {
				return false
			}
			return *b
		},
		"capitalize": func(s string) string {
			if len(s) == 0 {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		// Type lookup helpers
		"lookupSchemaType": func(t string) string {
			return LookupSchemaType(t)
		},
		"lookupGoType": func(t string) string {
			return LookupGoType(t)
		},
		"lookupRetGoType": func(t string) string {
			return LookupRetGoType(t)
		},
		"lookupCppType": func(t string) string {
			return LookupCppType(t)
		},
		"lookupXllType": func(t string) string {
			return LookupXllType(t)
		},
		"lookupArgXllType": func(t string) string {
			return LookupArgXllType(t)
		},
		"lookupCppArgType": func(t string) string {
			return LookupArgCppType(t)
		},
		"lookupEventCode": func(t string) string {
			// Map event types to xlEvent... constants
			switch t {
			case "CalculationEnded":
				return "xleventCalculationEnded"
			case "CalculationCanceled":
				return "xleventCalculationCanceled"
			default:
				return "0"
			}
		},
		"lookupEventId": func(t string) string {
			// Map event types to system MsgIDs?
			// The server needs to know MsgIDs for events if it receives them.
			// Currently events like CalculationEnded are MsgID 131.
			switch t {
			case "CalculationEnded":
				return strconv.Itoa(server.MsgCalculationEnded)
			case "CalculationCanceled":
				return strconv.Itoa(server.MsgCalculationCanceled)
			default:
				return "0"
			}
		},
		// Arithmetic helpers
		"add": func(a, b int) int {
			return a + b
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"boolToInt": func(b bool) int {
			if b {
				return 1
			}
			return 0
		},
		// Timeout parsing helper
		"parseTimeout": func(s string, defaultMs int) int {
			return parseDurationToMs(s, defaultMs)
		},
		"parseDurationToMs": func(s string) int {
			return parseDurationToMs(s, 0)
		},
		// parseDurationToNs emits the nanosecond count for a Go time.Duration
		// literal. Used by server.go.tmpl when threading server.chunk.*
		// durations into ChunkManagerConfig (the runtime expects
		// time.Duration, not strings).
		"parseDurationToNs": func(s string) int64 {
			if s == "" {
				return 0
			}
			d, err := time.ParseDuration(s)
			if err != nil {
				return 0
			}
			return int64(d)
		},
		"MsgUserStart": func() int {
			return server.MsgUserStart
		},
		// isRtdLike reports whether a mode routes through the RTD topic
		// lifecycle (xlfRtd wrapper, RTD push results). Both "rtd" and
		// "rtd-once" share the C++ wrapper shape and the server-side skip of
		// the sync/async handler glue.
		"isRtdLike": func(mode string) bool {
			return mode == "rtd" || mode == "rtd-once"
		},
		// anyRtdOnce reports whether the project declares at least one
		// rtd-once function. Used to gate emission of the C++ RtdOnceResults
		// machinery and the once-set initializer.
		"anyRtdOnceGrid": anyRtdOnceGrid,
		"anyRtdOnce": func(fns []config.Function) bool {
			for _, fn := range fns {
				if fn.Mode == "rtd-once" {
					return true
				}
			}
			return false
		},
		// anyRtd reports whether the project declares at least one plain
		// (streaming) rtd function. Used to gate the RtdPlaceholderRegistry
		// include + xlAutoOpen population (plain rtd's ConnectData initial-value
		// placeholder lives in that registry).
		"anyRtd": func(fns []config.Function) bool {
			for _, fn := range fns {
				if fn.Mode == "rtd" {
					return true
				}
			}
			return false
		},
		// durationMillis parses a Go duration string and returns its whole
		// milliseconds as a string, for embedding a memoize_ttl into the
		// generated C++ SetFunctionNames(...) call. Config validation has
		// already guaranteed the string parses to a positive duration; if it
		// somehow does not, fall back to "0" (treated as no TTL by the C++
		// registry, which only stores TTLs > 0).
		"durationMillis": func(s string) string {
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				return "0"
			}
			return strconv.FormatInt(d.Milliseconds(), 10)
		},
		// rtdPlaceholderReturn emits the C++ `return ...;` statement for an
		// rtd-once cell's first paint (cache miss) on the SCALAR/GRID path,
		// honoring the resolved loading_placeholder (per-function override, then
		// global, then the #GETTING_DATA default). It is never used on the
		// numgrid path, which cannot carry an error or string and always returns
		// an empty FP12. Custom text is shipped through NewExcelString (a
		// DLL-owned xltypeStr that xlAutoFree12 reclaims); the two reserved
		// keywords resolve to the static error sentinels.
		"rtdPlaceholderReturn": func(fn config.Function, rtd config.RtdConfig) string {
			ph := config.ResolveRtdPlaceholder(fn, rtd)
			switch ph.Kind {
			case config.PlaceholderNA:
				return "return &g_xlErrNA;"
			case config.PlaceholderText:
				return "return NewExcelString(" + cppWideLiteral(ph.Text) + ");"
			default:
				return "return &g_xlErrGettingData;"
			}
		},
		// rtdPlaceholderEntry emits one `{L"Name", {kind, L"text"}}` initializer
		// for the RtdPlaceholderRegistry::Set call at xlAutoOpen — the plain-rtd
		// ConnectData initial-value placeholder, resolved (per-function override
		// over the global default). Unlike rtdPlaceholderReturn this targets a
		// COM VARIANT path (ConnectData), so the keywords carry the kind and the
		// verbatim text is escaped for the C++ wide literal.
		"rtdPlaceholderEntry": func(fn config.Function, rtd config.RtdConfig) string {
			ph := config.ResolveRtdPlaceholder(fn, rtd)
			kind := "xll::RtdPlaceholderKind::GettingData"
			text := `L""`
			switch ph.Kind {
			case config.PlaceholderNA:
				kind = "xll::RtdPlaceholderKind::NA"
			case config.PlaceholderText:
				kind = "xll::RtdPlaceholderKind::Text"
				text = cppWideLiteral(ph.Text)
			}
			return fmt.Sprintf(`{L"%s", {%s, %s}}`, fn.Name, kind, text)
		},
	}
}
