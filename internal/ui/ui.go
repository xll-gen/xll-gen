package ui

import (
	"fmt"
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
