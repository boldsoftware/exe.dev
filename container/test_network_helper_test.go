package container

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/ctrhosttest"
)

// findFreeSubnetOnRemote inspects existing nerdctl networks on the remote host
// and returns a free /24 subnet in the 10.42.0.0/16..10.99.255.0/24 range.
var usedTestSubnets = map[string]bool{}

func findFreeSubnetOnRemote(t *testing.T, host string) (string, error) {
	t.Helper()
	h := strings.TrimPrefix(host, "ssh://")
	if h == "" || strings.HasPrefix(h, "/") {
		return "", fmt.Errorf("invalid SSH host: %q", host)
	}

	sshBase := []string{"ssh", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", h}

	// List network names
	listCmd := exec.Command(sshBase[0], append(sshBase[1:], "sudo", "nerdctl", "--namespace", "exe", "network", "ls", "--format", "{{.Name}}")...)
	out, err := listCmd.Output()
	if err != nil {
		return "", fmt.Errorf("list networks failed: %v", err)
	}
	used := map[string]bool{}
	names := strings.Fields(string(out))
	for _, name := range names {
		inspCmd := exec.Command(sshBase[0], append(sshBase[1:], "sudo", "nerdctl", "--namespace", "exe", "network", "inspect", name, "--format", "{{range .IPAM.Config}}{{.Subnet}} {{end}}")...)
		o2, _ := inspCmd.Output()
		for _, s := range strings.Fields(string(o2)) {
			used[s] = true
		}
	}

	// Merge in-process used cache to avoid reusing ranges within the same test run
	for k := range usedTestSubnets {
		used[k] = true
	}

	// Probe-create strategy: attempt to create a temporary network with a candidate subnet.
	// If nerdctl reports overlap, move to the next candidate. Remove the probe network immediately on success.
	probeName := fmt.Sprintf("exe-probe-%d", time.Now().UnixNano())

	tryProbe := func(cand string) bool {
		create := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", h,
			"sudo", "nerdctl", "--namespace", "exe", "network", "create", probeName, "--subnet", cand, "--driver", "bridge")
		_, err := create.CombinedOutput()
		if err != nil {
			// Overlap or other error: skip this candidate
			return false
		}
		// Remove probe network
		_ = exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", h,
			"sudo", "nerdctl", "--namespace", "exe", "network", "rm", probeName).Run()
		return true
	}

	// Prefer a high-range 10.250.0.0/16 to avoid common defaults, then fall back to 10.42.0.0/16..10.99.0.0/16
	// Pass 1: 10.250.X.0/24
	for x := 0; x <= 255; x++ {
		cand := fmt.Sprintf("10.250.%d.0/24", x)
		if used[cand] || usedTestSubnets[cand] {
			continue
		}
		if tryProbe(cand) {
			usedTestSubnets[cand] = true
			return cand, nil
		}
	}
	// Pass 2: 10.42-10.99 ranges
	for x := 42; x <= 99; x++ {
		for y := 0; y <= 255; y++ {
			cand := fmt.Sprintf("10.%d.%d.0/24", x, y)
			if used[cand] || usedTestSubnets[cand] {
				continue
			}
			if tryProbe(cand) {
				usedTestSubnets[cand] = true
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("no free subnet available on remote host (probe)\nstdout: %s", "")
}

// networkNameForAlloc returns the nerdctl network name used for an allocation.
func networkNameForAlloc(allocID string) string {
	nameLen := len(allocID)
	if nameLen > 12 {
		nameLen = 12
	}
	return "exe-" + allocID[:nameLen]
}

// cleanupAllocNetwork removes the per-alloc network on the remote host.
func cleanupAllocNetwork(t *testing.T, host, allocID string) {
	t.Helper()
	h := strings.TrimPrefix(host, "ssh://")
	if h == "" || strings.HasPrefix(h, "/") {
		return
	}
	nn := networkNameForAlloc(allocID)
	_ = exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", h,
		"sudo", "nerdctl", "--namespace", "exe", "network", "rm", nn).Run()
}

// WithAllocIPRange determines and assigns a per-alloc IP range for remote CTR_HOST tests.
// It inspects existing remote networks and returns a free /24. If CTR_HOST is not set
// or is a local path, it returns an empty string and does nothing.
// It also registers cleanup to remove the per-alloc network after the test.
func WithAllocIPRange(t *testing.T, allocID string) string {
	t.Helper()
	host := os.Getenv("CTR_HOST")
	if host == "" {
		// Attempt auto-detection for local dev convenience
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if detected := ctrhosttest.Detect(ctx); detected != "" {
			host = detected
		}
	}
	if host == "" || strings.HasPrefix(host, "/") {
		return "" // local/non-remote: leave empty; prod path not under test
	}
	ipRange, err := findFreeSubnetOnRemote(t, host)
	if err != nil {
		t.Skipf("Cannot determine free subnet on remote host %s: %v", host, err)
		return ""
	}
	t.Logf("Using test IP range %s for alloc %s", ipRange, allocID)
	t.Cleanup(func() { cleanupAllocNetwork(t, host, allocID) })
	return ipRange
}
