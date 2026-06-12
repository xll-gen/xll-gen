package generator

import (
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/pkg/server"
)

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
// terminate or corrupt the generated literal.
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
			b.WriteRune(r)
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
		"anyRtdOnce": func(fns []config.Function) bool {
			for _, fn := range fns {
				if fn.Mode == "rtd-once" {
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
	}
}
