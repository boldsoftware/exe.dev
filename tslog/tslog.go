// Package tslog provides log/slog support for testing.
//
// The ts- prefix does not mean Tailscale. Or TypeScript.
// It's not even a ts- prefix, it's a t- prefix and an s- prefix.
package tslog

import (
	"log/slog"
	"testing"
)

// Slogger is short for SloggerLevel(t, slog.LevelDebug).
func Slogger(t testing.TB) *slog.Logger {
	return SloggerLevel(t, slog.LevelDebug)
}

// SloggerLevel returns a [*slog.Logger] that writes each message
// using t.Output() at the given level.
func SloggerLevel(t testing.TB, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == "time" {
				return slog.Attr{}
			}
			return a
		},
	}))
}
