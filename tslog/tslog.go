// Package tslog provides log/slog support for testing.
//
// The ts- prefix does not mean Tailscale. Or TypeScript.
// It's not even a ts- prefix, it's a t- prefix and an s- prefix.
package tslog

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
)

// Slogger is short for SloggerLevel(t, slog.LevelDebug).
func Slogger(t testing.TB) *slog.Logger {
	return SloggerLevel(t, slog.LevelDebug)
}

// SloggerLevel returns a [*slog.Logger] that writes each message
// using t.Output() at the given level.
// Logs are automatically silenced when the test ends via t.Cleanup,
// preventing panics from background goroutines that log after test completion.
func SloggerLevel(t testing.TB, level slog.Level) *slog.Logger {
	h := &silenceableHandler{
		inner: slog.NewTextHandler(t.Output(), &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == "time" {
					return slog.Attr{}
				}
				return a
			},
		}),
		silenced: new(atomic.Bool),
	}
	t.Cleanup(func() { h.silenced.Store(true) })
	return slog.New(h)
}

type silenceableHandler struct {
	inner    slog.Handler
	silenced *atomic.Bool
}

func (h *silenceableHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.silenced.Load() {
		return false
	}
	return h.inner.Enabled(ctx, level)
}

func (h *silenceableHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.silenced.Load() {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h *silenceableHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &silenceableHandler{inner: h.inner.WithAttrs(attrs), silenced: h.silenced}
}

func (h *silenceableHandler) WithGroup(name string) slog.Handler {
	return &silenceableHandler{inner: h.inner.WithGroup(name), silenced: h.silenced}
}
