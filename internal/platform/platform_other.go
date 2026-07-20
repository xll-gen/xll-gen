//go:build !windows

package platform

const exeSuffix = ""

// excelPath has no meaning off Windows; the caller falls back to a default.
func excelPath() (string, bool) { return "", false }

// detectExcelSecurityAddins has no meaning off Windows (no registry).
func detectExcelSecurityAddins() []string { return nil }
