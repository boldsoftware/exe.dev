package e1e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestVCPUPoolEnforcement tests that the account-level cgroup slice enforces
// the plan's MaxCPUs as the cpu.max limit. Individual Small has MaxCPUs=2,
// so the account slice should get cpu.max = "200000 100000" (2 CPUs × 100ms period).
//
// It also verifies that user-level cgroup overrides take priority over the
// plan-based limit (both expanding for VIPs and throttling for abuse).
func TestVCPUPoolEnforcement(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)
	e1eTestsOnlyRunOnce(t)

	// Register user with Individual billing (Small tier: MaxCPUs=2).
	pty, _, keyFile, email := registerForExeDevWithEmail(t, "vcpu-pool@test-vcpu-pool.example")
	defer pty.Disconnect()

	// Create a box and wait for SSH.
	bn := newBox(t, pty)
	defer pty.deleteBox(bn)
	waitForSSH(t, bn, keyFile)

	// Resolve the container ID and user ID.
	ctx := Env.context(t)
	exeletClient := Env.servers.Exelets[0].Client()
	containerID := instanceIDByName(t, ctx, exeletClient, bn)
	userID := getUserIDByEmail(t, email)
	exelet := Env.servers.Exelets[0]

	// Helper: read account slice cpu.max
	readAccountCPUMax := func() string {
		out, err := exelet.Exec(ctx, fmt.Sprintf(
			"cat /sys/fs/cgroup/exelet.slice/%s.slice/cpu.max 2>/dev/null",
			userID))
		if err != nil || len(out) == 0 {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	// Helper: wait for account slice cpu.max to match expected value.
	waitForAccountCPUMax := func(t *testing.T, expected string, timeout time.Duration) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		var cpuMax string
		for time.Now().Before(deadline) {
			cpuMax = readAccountCPUMax()
			if cpuMax == expected {
				t.Logf("account slice cpu.max = %q", cpuMax)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		if cpuMax == "" {
			t.Fatalf("account slice cpu.max cgroup file not found within %v", timeout)
		}
		t.Fatalf("account slice cpu.max = %q, want %q (waited %v)", cpuMax, expected, timeout)
	}

	// 1. Plan-based enforcement: account slice gets plan MaxCPUs (2).
	t.Run("plan_enforced", func(t *testing.T) {
		waitForAccountCPUMax(t, "200000 100000", 90*time.Second) // 2 CPUs
	})

	// 2. Per-VM scope gets allocated CPUs (2).
	t.Run("vm_scope", func(t *testing.T) {
		expected := "200000 100000" // 2 CPUs
		deadline := time.Now().Add(30 * time.Second)
		var cpuMax string
		for time.Now().Before(deadline) {
			out, err := exelet.Exec(ctx, fmt.Sprintf(
				"cat /sys/fs/cgroup/exelet.slice/%s.slice/vm-%s.scope/cpu.max 2>/dev/null",
				userID, containerID))
			if err == nil && len(out) > 0 {
				cpuMax = strings.TrimSpace(string(out))
				if cpuMax == expected {
					t.Logf("VM scope cpu.max = %q", cpuMax)
					return
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if cpuMax == "" {
			t.Fatal("VM scope cpu.max cgroup file not found within timeout")
		}
		t.Fatalf("VM scope cpu.max = %q, want %q", cpuMax, expected)
	})

	// 3. User override expands beyond plan (e.g. VIP boost to 4 CPUs).
	t.Run("override_expands", func(t *testing.T) {
		setUserCgroupOverride(t, userID, "4") // 4 CPUs
		waitForAccountCPUMax(t, "400000 100000", 90*time.Second)
	})

	// 4. User override throttles below plan (abuse: 1 CPU).
	t.Run("override_throttles", func(t *testing.T) {
		setUserCgroupOverride(t, userID, "1") // 1 CPU
		waitForAccountCPUMax(t, "100000 100000", 90*time.Second)
	})

	// 5. Clearing override restores plan-based limit.
	t.Run("override_cleared", func(t *testing.T) {
		clearUserCgroupOverride(t, userID)
		waitForAccountCPUMax(t, "200000 100000", 90*time.Second) // back to plan MaxCPUs=2
	})
}

// setUserCgroupOverride sets a CPU cgroup override for a user via the debug endpoint.
func setUserCgroupOverride(t *testing.T, userID, cpuCores string) {
	t.Helper()
	postCgroupOverride(t, url.Values{"user_id": {userID}, "cpu": {cpuCores}})
}

// clearUserCgroupOverride removes all cgroup overrides for a user.
func clearUserCgroupOverride(t *testing.T, userID string) {
	t.Helper()
	postCgroupOverride(t, url.Values{"user_id": {userID}, "clear": {"1"}})
}

func postCgroupOverride(t *testing.T, form url.Values) {
	t.Helper()
	// Don't follow redirects — the endpoint returns 303 on success.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.PostForm(
		fmt.Sprintf("http://localhost:%d/debug/users/set-cgroup-overrides", Env.servers.Exed.HTTPPort),
		form,
	)
	if err != nil {
		t.Fatalf("failed to post cgroup override: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post cgroup override: want 303, got %d", resp.StatusCode)
	}
}
