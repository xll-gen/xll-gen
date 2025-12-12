package cmd

import (
	"fmt"
)

var (
	// ANSI Colors
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

func printHeader(msg string) {
	fmt.Printf("\n%s%s%s\n", colorBold, msg, colorReset)
}

func printSuccess(label, detail string) {
	fmt.Printf("  %s✔%s %-15s %s%s\n", colorGreen, colorReset, label, colorGreen, detail+colorReset)
}

func printError(label, detail string) {
	fmt.Printf("  %s✘%s %-15s %s%s\n", colorRed, colorReset, label, colorRed, detail+colorReset)
}

func printWarning(label, detail string) {
	fmt.Printf("  %s!%s %-15s %s%s\n", colorYellow, colorReset, label, colorYellow, detail+colorReset)
}
