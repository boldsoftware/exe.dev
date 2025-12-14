// Package ctrlc provides utilities for detecting Ctrl+C in interactive readers.
package ctrlc

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
)

// ErrCanceled is returned when the user presses Ctrl+C.
var ErrCanceled = errors.New("user canceled")

// WithReader wraps r to detect Ctrl+C (byte 0x03). It returns a context that
// cancels when Ctrl+C is detected, a reader that preserves all non-Ctrl+C
// bytes, and a stop function to call when done waiting.
//
// The returned reader should be used for all subsequent reads. When stop is
// called, any byte read by the internal goroutine is preserved in the returned
// reader before it resumes reading directly from r.
//
// Known issue: the first byte after stop is consistently lost in practice
// (e.g. after exiting a shell session started through the REPL, the first
// keystroke is swallowed). Unit tests with synctest have not reproduced this,
// so the bug may be elsewhere, but this is a likely suspect.
func WithReader(parent context.Context, r io.Reader) (context.Context, io.Reader, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	var stopped atomic.Bool
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		var b [1]byte
		for {
			n, err := r.Read(b[:])
			if stopped.Load() {
				if n > 0 {
					pw.Write(b[:n])
				}
				return
			}
			if err != nil {
				cancel(err)
				return
			}
			if b[0] == 3 { // Ctrl+C
				cancel(ErrCanceled)
				pw.CloseWithError(ErrCanceled)
				return
			}
			if n > 0 {
				pw.Write(b[:n])
			}
		}
	}()
	return ctx, io.MultiReader(pr, r), func() { stopped.Store(true) }
}
