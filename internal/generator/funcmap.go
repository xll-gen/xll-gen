package generator

import (
	"strings"
	"text/template"
	"time"

	"github.com/xll-gen/xll-gen/internal/config"
)

// Helper to check if a slice of functions contains any async functions
func hasAsync(funcs []config.Function) bool {
	for _, f := range funcs {
		if f.Async {
			return true
		}
	}
	return false
}

// Helper to check if a slice of functions contains any resizable functions
func hasResizable(funcs []config.Function) bool {
	for _, f := range funcs {
		if f.Resizable {
			return true
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
		"hasAsync":     hasAsync,
		"hasResizable": hasResizable,
		"hasEvent":     hasEvent,
		"derefBool": func(b *bool) bool {
			if b == nil {
				return false
			}
			return *b
		},
		"derefString": func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		},
		// Case conversion helpers
		"Title": strings.Title,
		"Lower": strings.ToLower,
		"Upper": strings.ToUpper,
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
		"lookupCppType": func(t string) string {
			return LookupCppType(t)
		},
		"lookupXllType": func(t string) string {
			return LookupXllType(t)
		},
		"lookupCppArgType": func(t string) string {
			return LookupArgCppType(t)
		},
		"defaultErrorVal": func(t string) string {
			return DefaultErrorVal(t)
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
				return "131"
			case "CalculationCanceled":
				return "132"
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
		"MsgUserStart": func() int {
			return 133
		},
	}
}
