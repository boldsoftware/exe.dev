package inventory

import (
	"context"
	"fmt"
	"os/exec"
)

// execCommand runs a command and returns its stdout.
func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w", name, args, err)
	}
	return out, nil
}
