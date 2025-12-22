package cmd

import (
	"github.com/xll-gen/xll-gen/internal/ui"
)

var (
	// ANSI Colors
	colorReset  = ui.ColorReset
	colorRed    = ui.ColorRed
	colorGreen  = ui.ColorGreen
	colorYellow = ui.ColorYellow
	colorCyan   = ui.ColorCyan
	colorBold   = ui.ColorBold
)

func printHeader(msg string) {
	ui.PrintHeader(msg)
}

func printSuccess(label, detail string) {
	ui.PrintSuccess(label, detail)
}

func printError(label, detail string) {
	ui.PrintError(label, detail)
}

func printWarning(label, detail string) {
	ui.PrintWarning(label, detail)
}

func prompt(label, defaultValue string) string {
	return ui.Prompt(label, defaultValue)
}
