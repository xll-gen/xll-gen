package log

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu sync.Mutex
)

// Init initializes the global logger.
// path: Log file path. If empty, logs to stdout.
// level: Log level ("debug", "info", "warn", "error"). Defaults to "info".
func Init(path string, level string) error {
	mu.Lock()
	defer mu.Unlock()

	var w io.Writer = os.Stdout
	if path != "" {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		}

		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		w = f
	}

	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{
		Level: lvl,
	}

	// User requested Text format
	handler := slog.NewTextHandler(w, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Debug logs at Debug level.
func Debug(msg string, args ...any) {
	slog.Debug(msg, args...)
}

// Info logs at Info level.
func Info(msg string, args ...any) {
	slog.Info(msg, args...)
}

// Warn logs at Warn level.
func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

// Error logs at Error level.
func Error(msg string, args ...any) {
	slog.Error(msg, args...)
}
