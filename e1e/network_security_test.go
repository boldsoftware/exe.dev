// This file contains tests for network security functionality.

package e1e

import (
	"testing"
	"time"
)

// TestCarrierNATBlock verifies that guests cannot access the carrier-grade NAT range (100.64.0.0/10).
// This protects host infrastructure on CGNAT from guest access while allowing exeletd to connect.
func TestCarrierNATBlock(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()

	waitForSSH(t, boxName, keyFile)

	// Try to reach an IP in the carrier NAT range that is NOT the guest's gateway.
	// We use 100.127.0.1 which is at the far end of the 100.64.0.0/10 range.
	// The connection should timeout/fail due to iptables DROP rule.
	t.Run("cgnat_blocked", func(t *testing.T) {
		// Use nc (netcat) to try to connect to a CGNAT IP with a short timeout.
		// We expect this to fail (timeout or connection refused).
		// The -w flag sets a timeout in seconds.
		cmd := boxSSHCommand(t, boxName, keyFile, "timeout", "3", "nc", "-z", "-w", "2", "100.127.0.1", "80")
		cmd.Stdout = t.Output()
		cmd.Stderr = t.Output()

		start := time.Now()
		err := cmd.Run()
		elapsed := time.Since(start)

		// The command should fail (non-zero exit) because the connection is blocked
		if err == nil {
			t.Error("expected connection to 100.127.0.1 to fail (CGNAT should be blocked), but it succeeded")
		}

		// Verify it didn't take too long (iptables DROP should cause relatively quick timeout)
		if elapsed > 5*time.Second {
			t.Errorf("connection attempt took %v, expected faster timeout with DROP rule", elapsed)
		}

		t.Logf("CGNAT connection correctly blocked (elapsed: %v, error: %v)", elapsed, err)
	})

	// Verify guest can still reach the internet (not broken by our blocking rule)
	t.Run("internet_reachable", func(t *testing.T) {
		// Try to reach a well-known public IP (Cloudflare DNS)
		cmd := boxSSHCommand(t, boxName, keyFile, "timeout", "10", "nc", "-z", "-w", "5", "1.1.1.1", "53")
		cmd.Stdout = t.Output()
		cmd.Stderr = t.Output()

		if err := cmd.Run(); err != nil {
			t.Errorf("expected connection to 1.1.1.1:53 to succeed (internet should be reachable), but got: %v", err)
		} else {
			t.Log("Internet connectivity confirmed")
		}
	})

	// Clean up
	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(boxName)
	pty.disconnect()
}
