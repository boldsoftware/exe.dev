package execore

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// commandLogKey is used to pass command log info via context.
type commandLogKey struct{}

// CommandLog accumulates log attributes during command execution,
// similar to sloghttp for HTTP requests. It allows handlers to add
// custom attributes that will be included in the final command completion log.
type CommandLog struct {
	mu        sync.Mutex
	attrs     []slog.Attr
	start     time.Time
	durations map[string]time.Duration
}

// NewCommandLog creates a new CommandLog with the given start time.
func NewCommandLog(start time.Time) *CommandLog {
	return &CommandLog{
		start:     start,
		attrs:     make([]slog.Attr, 0),
		durations: make(map[string]time.Duration),
	}
}

// WithCommandLog attaches a CommandLog to the context.
func WithCommandLog(ctx context.Context, cl *CommandLog) context.Context {
	return context.WithValue(ctx, commandLogKey{}, cl)
}

// GetCommandLog retrieves the CommandLog from context, if present.
func GetCommandLog(ctx context.Context) *CommandLog {
	if v := ctx.Value(commandLogKey{}); v != nil {
		if cl, ok := v.(*CommandLog); ok {
			return cl
		}
	}
	return nil
}

// AddAttr adds a log attribute to be included in the final log.
func (cl *CommandLog) AddAttr(attr slog.Attr) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.attrs = append(cl.attrs, attr)
}

// AddDuration records a named duration (e.g., "dns", "exelet_rpc").
func (cl *CommandLog) AddDuration(name string, d time.Duration) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.durations[name] = d
}

// Attrs returns a copy of all accumulated attributes, including durations.
func (cl *CommandLog) Attrs() []slog.Attr {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	// Start with user-added attrs
	result := make([]slog.Attr, len(cl.attrs))
	copy(result, cl.attrs)

	// Add durations as attributes
	for name, d := range cl.durations {
		result = append(result, slog.Duration(name+"_duration", d))
	}

	return result
}

// Duration returns the total duration since the CommandLog was created.
func (cl *CommandLog) Duration() time.Duration {
	return time.Since(cl.start)
}

// CommandLogAddAttr is a convenience function to add an attribute to the
// CommandLog in the context. It's a no-op if no CommandLog is present.
func CommandLogAddAttr(ctx context.Context, attr slog.Attr) {
	if cl := GetCommandLog(ctx); cl != nil {
		cl.AddAttr(attr)
	}
}

// CommandLogAddDuration is a convenience function to add a named duration
// to the CommandLog in the context. It's a no-op if no CommandLog is present.
func CommandLogAddDuration(ctx context.Context, name string, d time.Duration) {
	if cl := GetCommandLog(ctx); cl != nil {
		cl.AddDuration(name, d)
	}
}
