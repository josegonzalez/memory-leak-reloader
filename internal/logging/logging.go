// Package logging builds a logr.Logger backed by log/slog, supporting logfmt
// (default, for local development) and Datadog-compatible JSON output.
package logging

import (
	"log/slog"
	"os"
	"strings"

	"github.com/go-logr/logr"
)

// New returns a logr.Logger for the given format ("logfmt" or "json") and level
// ("debug", "info", "warn", "error").
func New(format, level string) logr.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var handler slog.Handler
	switch strings.ToLower(format) {
	case "json":
		opts.ReplaceAttr = datadogReplace
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default: // logfmt
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return logr.FromSlogHandler(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
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

// datadogReplace maps slog's default keys to Datadog's reserved attributes so
// logs are parsed correctly by the Datadog log pipeline.
func datadogReplace(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.MessageKey: // "msg" -> "message"
		a.Key = "message"
	case slog.LevelKey: // "level" -> "status"
		a.Key = "status"
	case slog.TimeKey: // "time" -> "timestamp"
		a.Key = "timestamp"
	}
	return a
}
