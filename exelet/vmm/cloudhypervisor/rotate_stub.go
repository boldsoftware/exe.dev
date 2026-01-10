//go:build !linux

package cloudhypervisor

import (
	"context"
	"time"
)

// StartLogRotation is a stub for non-Linux platforms.
// Log rotation uses Linux-specific fallocate syscalls.
func (v *VMM) StartLogRotation(ctx context.Context, interval time.Duration, maxBytes int64) func() {
	return func() {}
}

// RotateBootLog is a stub for non-Linux platforms.
func (v *VMM) RotateBootLog(ctx context.Context, id string, maxBytes int64) error {
	return nil
}
