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
	}
}
