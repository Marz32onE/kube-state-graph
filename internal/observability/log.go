package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger returns a slog.Logger backed by a JSON handler writing to stdout
// at the requested level (debug | info | warn | error).
func NewLogger(level string) *slog.Logger {
	return slog.New(NewLogHandler(level))
}

// NewLogHandler returns the JSON handler used by NewLogger so callers (e.g.
// the telemetry package) can wrap it in additional handlers before installing
// the global logger.
func NewLogHandler(level string) slog.Handler {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
}
