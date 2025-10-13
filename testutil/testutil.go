// Package testutil provides utilities for testing.
package testutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Slogger is short for SloggerLevel(t, slog.LevelDebug).
func Slogger(t testing.TB) *slog.Logger {
	return SloggerLevel(t, slog.LevelDebug)
}

type slogWriter struct {
	ctx   context.Context
	s     *slog.Logger
	level slog.Level
}

func (sw slogWriter) Write(p []byte) (n int, err error) {
	sw.s.LogAttrs(sw.ctx, sw.level, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func SlogWriter(t testing.TB, level slog.Level) io.Writer {
	return slogWriter{
		ctx:   t.Context(),
		s:     SloggerLevel(t, level),
		level: level,
	}
}

// SwapLogger sets the global logger to log and registers a cleanup
// function to restore the previous logger.
func SwapLogger(t testing.TB, log *slog.Logger) {
	t.Chdir(".") // prevents parallel tests changing the global logger
	oldLog := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(oldLog) })
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

// SlogBuffer returns a [*slog.Logger] that writes each message to out.
func SlogBuffer() (lg *slog.Logger, out *bytes.Buffer) {
	var buf bytes.Buffer
	lg = slog.New(slog.NewTextHandler(&buf, nil))
	return lg, &buf
}

type OutputFailer interface {
	Output() io.Writer
	Fail()
}

// Failer returns a "failf" function that works like t.Errorf,
// but writes the message using t.Output(),
// to avoid the file:line prefix.
//
// It then calls t.Fail before returning.
func Failer(t OutputFailer) func(format string, args ...any) {
	return func(format string, args ...any) {
		if !strings.HasSuffix(format, "\n") {
			format += "\n"
		}
		fmt.Fprintf(t.Output(), format, args...)
		t.Fail()
	}
}

// Check calls t.Fatal(err) if err is not nil.
func Check(t *testing.T, err error) {
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
}

// CheckFunc exists so other packages do not need to invent their own type for
// taking a Check function.
type CheckFunc func(err error)

// Checker returns a check function that
// calls t.Fatal if err is not nil.
func Checker(t *testing.T) (check func(err error)) {
	return func(err error) {
		if err != nil {
			t.Helper()
			t.Fatal(err)
		}
	}
}

// StopPanic runs f but silently recovers from any panic f causes.
// The normal usage is:
//
//	testutil.StopPanic(func() {
//		callThatShouldPanic()
//		t.Errorf("callThatShouldPanic did not panic")
//	})
func StopPanic(f func()) {
	defer func() { recover() }()
	f()
}

// CheckTime calls t.Fatalf if got != want. Included in the error message is
// want.Sub(got) to help diagnose the difference, along with their values in
// UTC.
func CheckTime(t *testing.T, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v (%v)", got.UTC(), want.UTC(), want.Sub(got))
	}
}

// WriteFile writes data to a file named name. It makes the directory if it
// doesn't exist and sets the file mode to perm.
//
// The name must be a relative path and must not contain .. or start with a /;
// otherwise WriteFile will panic.
func WriteFile[S []byte | string](t testing.TB, name string, data S) {
	t.Helper()

	if filepath.IsAbs(name) {
		t.Fatalf("WriteFile: name must be a relative path, got %q", name)
	}
	name = filepath.Clean(name)
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
