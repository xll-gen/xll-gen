package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu sync.Mutex
	// currentFile holds the log file opened by the most recent Init call (nil
	// when logging to stdout). On a subsequent Init we close it before opening
	// a new one so re-initialization does not leak file handles. Guarded by mu.
	currentFile *os.File
)

// Init initializes the global logger.
// It configures the default slog logger to write to the specified path (or stdout)
// at the specified level.
//
// path: Log file path. If empty, logs to stdout.
// level: Log level ("debug", "info", "warn", "error"). Defaults to "info".
func Init(path string, level string) error {
	mu.Lock()
	defer mu.Unlock()

	var w io.Writer = os.Stdout
	var newFile *os.File
	if path != "" {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("log: mkdir %q: %w", dir, err)
			}
		}

		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			// Leave the previously-installed logger (and its file) intact on
			// failure; we have not swapped anything yet.
			return fmt.Errorf("log: open %q: %w", path, err)
		}
		w = f
		newFile = f
	}

	// Close the file from a prior Init (if any) now that the new destination
	// has been opened successfully. Without this, repeated Init calls leak the
	// previous handle. Closing only after the new open succeeds means a failed
	// re-Init does not tear down a working logger.
	if currentFile != nil {
		_ = currentFile.Close()
	}
	currentFile = newFile

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

// Debug logs at LevelDebug.
func Debug(msg string, args ...any) {
	slog.Debug(msg, args...)
}

// Info logs at LevelInfo.
func Info(msg string, args ...any) {
	slog.Info(msg, args...)
}

// Warn logs at LevelWarn.
func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

// Error logs at LevelError.
func Error(msg string, args ...any) {
	slog.Error(msg, args...)
}

// Default returns the default logger.
func Default() *slog.Logger {
	return slog.Default()
}

// DebugContext logs at LevelDebug with context.
func DebugContext(ctx context.Context, msg string, args ...any) {
	slog.DebugContext(ctx, msg, args...)
}

// InfoContext logs at LevelInfo with context.
func InfoContext(ctx context.Context, msg string, args ...any) {
	slog.InfoContext(ctx, msg, args...)
}

// WarnContext logs at LevelWarn with context.
func WarnContext(ctx context.Context, msg string, args ...any) {
	slog.WarnContext(ctx, msg, args...)
}

// ErrorContext logs at LevelError with context.
func ErrorContext(ctx context.Context, msg string, args ...any) {
	slog.ErrorContext(ctx, msg, args...)
}
