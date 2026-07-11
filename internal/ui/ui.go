package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ANSI color escape codes. These are the "enabled" values; SetColorEnabled(false)
// blanks the exported ColorX variables so piped/CI output stays free of escapes.
const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiBold   = "\033[1m"
)

var (
	// ANSI Colors. Populated at package init based on terminal detection
	// (disabled when stdout is not a TTY or NO_COLOR is set). Callers read
	// these directly, so the API shape is unchanged — they simply become
	// empty strings when color is disabled.
	ColorReset  = ansiReset
	ColorRed    = ansiRed
	ColorGreen  = ansiGreen
	ColorYellow = ansiYellow
	ColorCyan   = ansiCyan
	ColorBold   = ansiBold
)

func init() {
	SetColorEnabled(detectColor())
}

// SetColorEnabled turns ANSI coloring on or off by (re)setting the exported
// ColorX variables. When off they become empty strings, so every format string
// that interpolates them emits no escape sequences.
func SetColorEnabled(on bool) {
	if on {
		ColorReset, ColorRed, ColorGreen, ColorYellow, ColorCyan, ColorBold =
			ansiReset, ansiRed, ansiGreen, ansiYellow, ansiCyan, ansiBold
		return
	}
	ColorReset, ColorRed, ColorGreen, ColorYellow, ColorCyan, ColorBold =
		"", "", "", "", "", ""
}

// detectColor reports whether ANSI colors should be emitted: NO_COLOR must be
// unset (any value disables color, per https://no-color.org) AND stdout must be
// a character device (a real terminal, not a pipe/file/CI capture).
func detectColor() bool {
	_, noColor := os.LookupEnv("NO_COLOR")
	return colorEnabledFor(os.Stdout, noColor)
}

// colorEnabledFor is the pure decision function behind detectColor, split out so
// it can be unit-tested without touching the process environment or real stdout.
func colorEnabledFor(f *os.File, noColorSet bool) bool {
	if noColorSet {
		return false
	}
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func PrintHeader(msg string) {
	fmt.Printf("\n%s%s%s\n", ColorBold, msg, ColorReset)
}

func PrintSuccess(label, detail string) {
	fmt.Printf("  %s✔%s %-15s %s%s\n", ColorGreen, ColorReset, label, ColorGreen, detail+ColorReset)
}

func PrintError(label, detail string) {
	fmt.Printf("  %s✘%s %-15s %s%s\n", ColorRed, ColorReset, label, ColorRed, detail+ColorReset)
}

func PrintWarning(label, detail string) {
	fmt.Printf("  %s!%s %-15s %s%s\n", ColorYellow, ColorReset, label, ColorYellow, detail+ColorReset)
}

// Spinner represents a loading indicator
type Spinner struct {
	msg      string
	stopChan chan struct{}
	doneChan chan struct{}
	stopOnce sync.Once
}

// StartSpinner starts a new spinner with the given message
func StartSpinner(msg string) *Spinner {
	s := &Spinner{
		msg:      msg,
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.doneChan)
	chars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	for {
		select {
		case <-s.stopChan:
			return
		default:
			fmt.Printf("\r%s%s%s %s", ColorCyan, chars[i], ColorReset, s.msg)
			time.Sleep(100 * time.Millisecond)
			i = (i + 1) % len(chars)
		}
	}
}

// Stop stops the spinner and clears the line. Safe to call multiple times.
func (s *Spinner) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopChan)
	})
	<-s.doneChan // Wait for goroutine to finish
	// Clear line
	fmt.Printf("\r%s\r", strings.Repeat(" ", len(s.msg)+10))
}

// RunSpinner executes the given action while showing a spinner.
func RunSpinner(msg string, action func() error) error {
	s := StartSpinner(msg)
	defer s.Stop()
	return action()
}
