//go:build !linux

package seccomp

// BlockKillSelf is a no-op on non-Linux systems.
// Seccomp is a Linux-specific feature.
func BlockKillSelf() error {
	return nil
}
