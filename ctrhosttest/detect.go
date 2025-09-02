package ctrhosttest

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// Detect returns a usable CTR_HOST value for tests and local dev.
// Priority:
// 1) CTR_HOST env var if set
// 2) If unset, probes "ssh colima-exe-ctr" and returns ssh://colima-exe-ctr on success
// Returns empty string if nothing is found.
func Detect(ctx context.Context) string {
	if v := os.Getenv("CTR_HOST"); v != "" {
		return v
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ssh",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"colima-exe-ctr", "true",
	)
	if err := cmd.Run(); err == nil {
		return "ssh://colima-exe-ctr"
	}
	return ""
}
