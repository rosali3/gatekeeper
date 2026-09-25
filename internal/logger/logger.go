// Package logger configures gatekeeper's structured (JSON) logging.
package logger

import (
	"io"
	"log/slog"
	"strings"
)

// New returns a JSON slog.Logger writing to w at the given level.
// Unrecognized level strings fall back to info.
func New(w io.Writer, level string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: ParseLevel(level)})
	return slog.New(h)
}

// ParseLevel maps a config/CLI level string to a slog.Level, defaulting to
// info for anything unrecognized.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
