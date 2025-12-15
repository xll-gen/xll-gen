package generator

import (
	"path/filepath"
	"strings"
	"text/template"

	"github.com/xll-gen/xll-gen/internal/config"
)

// GetCommonFuncMap returns a map of functions available to all templates.
// It aggregates type lookups, event lookups, and general utility functions.
func GetCommonFuncMap() template.FuncMap {
	return template.FuncMap{
		// Math
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },

		// Types
		"lookupSchemaType": LookupSchemaType,
		"lookupGoType":     LookupGoType,
		"lookupCppType":    LookupCppType,
		"lookupArgCppType": LookupArgCppType,
		"lookupXllType":    LookupXllType,
		"lookupArgXllType": LookupArgXllType,
		"defaultErrorVal":  DefaultErrorVal,

		// Events
		"lookupEventId": func(evtType string) int {
			// Returns offset from User Start
			if evtType == "CalculationEnded" {
				return 1
			}
			if evtType == "CalculationCanceled" {
				return 2
			}
			return 0
		},
		"lookupEventCode": func(evtType string) string {
			if evtType == "CalculationEnded" {
				return "xleventCalculationEnded"
			}
			if evtType == "CalculationCanceled" {
				return "xleventCalculationCanceled"
			}
			return "0"
		},
		"hasEvent": func(name string, events []config.Event) bool {
			for _, e := range events {
				if e.Type == name {
					return true
				}
			}
			return false
		},
		"hasAsync": func(funcs []config.Function) bool {
			for _, f := range funcs {
				if f.Async {
					return true
				}
			}
			return false
		},

		// String / Formatting
		"capitalize": func(s string) string {
			if len(s) == 0 {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"withDefault": func(val, def string) string {
			if val == "" {
				return def
			}
			return val
		},
		"boolToInt": func(b bool) int {
			if b {
				return 1
			}
			return 0
		},
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

		// Path Helpers
		"fileBase": func(s string) string {
			return strings.TrimSuffix(s, filepath.Ext(s))
		},
		"fileExt": func(s string) string {
			return filepath.Ext(s)
		},

		// Config Helpers
		"registerCount": func(f config.Function) int {
			// Async functions have an implicit handle argument, but we do not register a help string for it
			// because Excel hides it. Therefore, we do not increment the count for Async functions.
			c := 10 + len(f.Args)
			return c
		},
		"joinArgNames": func(f config.Function) string {
			var names []string
			for _, a := range f.Args {
				names = append(names, a.Name)
			}
			// Do not append "asyncHandle" to argument text, as it is implicit/hidden in Excel.
			return strings.Join(names, ",")
		},
		"parseTimeout": func(s string, defaultMs int) int {
			return parseDurationToMs(s, defaultMs)
		},
	}
}
