package ctrhosttest

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"time"
)

const (
	defaultHost         = "lima-exe-ctr"
	defaultHostForTests = "lima-exe-ctr-tests"
)

// Detect returns a usable CTR_HOST value for tests and local dev.
// The CTR_HOST env var wins, but otherwise it will try lima-exe-ctr{,-tests}
// (depening if its being called from a unit test or not).
func Detect(ctx context.Context) string {
	if v := os.Getenv("CTR_HOST"); v != "" {
		return v
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	host := defaultHost
	if flag.Lookup("test.v") != nil {
		host = defaultHostForTests
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "ssh",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		host, "true",
	)
	if err := cmd.Run(); err == nil {
		return "ssh://" + host
	}
	return ""
}
