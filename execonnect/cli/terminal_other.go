//go:build !linux && !darwin

package cli

import (
	"context"

	"golang.org/x/term"
)

func readTerminalLine(_ context.Context, fd int, hidden bool) ([]byte, error) {
	if hidden {
		return term.ReadPassword(fd)
	}
	return nil, context.Canceled
}
