package ui

import (
	"os"
	"testing"
)

// TestColorEnabledFor pins the color-decision logic: NO_COLOR (any value) always
// disables color, and a non-terminal stdout (pipe/file) disables it too.
func TestColorEnabledFor(t *testing.T) {
	// NO_COLOR set -> disabled regardless of the file.
	if colorEnabledFor(os.Stdout, true) {
		t.Error("colorEnabledFor: NO_COLOR set must disable color")
	}

	// A regular file (not a char device) -> disabled even without NO_COLOR.
	f, err := os.CreateTemp(t.TempDir(), "uicolor")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if colorEnabledFor(f, false) {
		t.Error("colorEnabledFor: a regular file is not a terminal, color must be disabled")
	}

	// nil file -> disabled (defensive).
	if colorEnabledFor(nil, false) {
		t.Error("colorEnabledFor: nil file must disable color")
	}
}

// TestSetColorEnabled verifies the exported ColorX variables are blanked when
// color is disabled and restored to their ANSI values when enabled.
func TestSetColorEnabled(t *testing.T) {
	// Restore whatever the init() detection chose once the test finishes.
	orig := ColorReset
	t.Cleanup(func() { SetColorEnabled(orig != "") })

	SetColorEnabled(false)
	for name, v := range map[string]string{
		"ColorReset":  ColorReset,
		"ColorRed":    ColorRed,
		"ColorGreen":  ColorGreen,
		"ColorYellow": ColorYellow,
		"ColorCyan":   ColorCyan,
		"ColorBold":   ColorBold,
	} {
		if v != "" {
			t.Errorf("SetColorEnabled(false): %s = %q, want empty", name, v)
		}
	}

	SetColorEnabled(true)
	if ColorReset != ansiReset || ColorRed != ansiRed || ColorGreen != ansiGreen ||
		ColorYellow != ansiYellow || ColorCyan != ansiCyan || ColorBold != ansiBold {
		t.Error("SetColorEnabled(true): ANSI codes not restored")
	}
}
