package generator

import (
	"strconv"
	"time"
)

// parseDurationToMs parses a duration string (e.g. "2s", "500ms") and returns milliseconds as int.
// Returns defaultVal if parsing fails or string is empty.
func parseDurationToMs(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}

	// Handle raw numbers as seconds (backward compatibility if needed, though Go usually uses strings)
	if _, err := strconv.Atoi(s); err == nil {
		// If it's just a number, assume seconds? Or ms?
		// Standard xll.yaml uses "10s", so pure number is ambiguous.
		// Let's assume input must be valid duration string.
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return defaultVal
	}
	return int(d.Milliseconds())
}
