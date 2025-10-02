//go:build !xll_debug

package debug

// no-op
func Debug(msg string, args ...any) {}
