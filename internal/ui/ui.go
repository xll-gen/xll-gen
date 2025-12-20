package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	// ANSI Colors
	ColorReset  = "\033[0m"
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorCyan   = "\033[36m"
	ColorBold   = "\033[1m"
)

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

// Prompt asks the user for input.
func Prompt(label string) string {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s?%s %s ", ColorCyan, ColorReset, label)
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("\nError reading input: %v\n", err)
			os.Exit(1)
		}
		input = strings.TrimSpace(input)
		if input != "" {
			return input
		}
	}
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
