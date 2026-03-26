// Package ctrlc provides utilities for detecting Ctrl+C in interactive readers.
package ctrlc

import "errors"

// ErrCanceled is returned when the user presses Ctrl+C.
var ErrCanceled = errors.New("user canceled")
