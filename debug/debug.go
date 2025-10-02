//go:build xll_debug

package debug

import (
	"io"
	"log/slog"
	"os"

	"log"
)

var Writer io.Writer
var Logger *slog.Logger

func init() {
	if Writer == nil {
		// Use xllgen.log in the current directory as the default log output.
		// If the file cannot be opened, fall back to os.Stderr to avoid losing logs.
		file, err := os.OpenFile("xll-gen.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			log.Printf("Failed to open log file: %v. Falling back to stderr.", err)
			Writer = os.Stderr
		} else {
			Writer = file
		}
	}

	if Logger == nil {
		Logger = slog.New(slog.NewTextHandler(Writer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
}

// Debug logs a debug message. with optional key-value pairs for context.
func Debug(msg string, args ...any) {
	Logger.Debug(msg, args...)
}
