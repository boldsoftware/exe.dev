//go:build linux

package nat

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"testing"
)

// TestBandwidthConstants verifies the default bandwidth constants are set correctly.
func TestBandwidthConstants(t *testing.T) {
	if DefaultBandwidthRate != "100mbit" {
		t.Errorf("expected DefaultBandwidthRate to be 100mbit, got %s", DefaultBandwidthRate)
	}
	if DefaultBandwidthBurst != "256k" {
		t.Errorf("expected DefaultBandwidthBurst to be 256k, got %s", DefaultBandwidthBurst)
	}
}

// TestNATBandwidthFieldsInitialized verifies the NAT struct has bandwidth fields initialized.
func TestNATBandwidthFieldsInitialized(t *testing.T) {
	// Create a minimal NAT struct for testing
	n := &NAT{
		bandwidthRate:  DefaultBandwidthRate,
		bandwidthBurst: DefaultBandwidthBurst,
	}

	if n.bandwidthRate != "100mbit" {
		t.Errorf("expected bandwidthRate to be 100mbit, got %s", n.bandwidthRate)
	}
	if n.bandwidthBurst != "256k" {
		t.Errorf("expected bandwidthBurst to be 256k, got %s", n.bandwidthBurst)
	}
}

// tcAvailable checks if the tc command is available.
func tcAvailable() bool {
	_, err := exec.LookPath("tc")
	return err == nil
}

// isRoot checks if the current process is running as root.
func isRoot() bool {
	return os.Getuid() == 0
}

// TestApplyBandwidthLimitRequiresTc tests that applyBandwidthLimit fails gracefully
// when tc command fails (e.g., interface doesn't exist).
func TestApplyBandwidthLimitRequiresTc(t *testing.T) {
	if !tcAvailable() {
		t.Skip("tc command not available")
	}
	if !isRoot() {
		t.Skip("tc commands require root")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	n := &NAT{
		log:            log,
		bandwidthRate:  DefaultBandwidthRate,
		bandwidthBurst: DefaultBandwidthBurst,
	}

	ctx := context.Background()
	tapName := "tap-nonexistent"

	// Ensure cleanup of any leftover state (IFB device) after test
	t.Cleanup(func() {
		_ = n.removeBandwidthLimit(ctx, tapName)
	})

	// Attempt to apply bandwidth limit to non-existent interface
	// This should fail because the interface doesn't exist
	// The function should clean up any partial state (IFB device) on failure
	err := n.applyBandwidthLimit(ctx, tapName)
	if err == nil {
		t.Error("expected error when applying bandwidth limit to non-existent interface")
	}
}

// TestRemoveBandwidthLimitNonexistent tests that removeBandwidthLimit doesn't fail
// when the interface doesn't exist or has no qdisc.
func TestRemoveBandwidthLimitNonexistent(t *testing.T) {
	if !tcAvailable() {
		t.Skip("tc command not available")
	}
	if !isRoot() {
		t.Skip("tc commands require root")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	n := &NAT{
		log: log,
	}

	ctx := context.Background()

	// removeBandwidthLimit should not return an error for non-existent interface
	// (it logs the error but continues)
	err := n.removeBandwidthLimit(ctx, "tap-nonexistent")
	if err != nil {
		t.Errorf("removeBandwidthLimit should not return error for non-existent interface: %v", err)
	}
}

// TestApplyBridgeFqCodelRequiresTc tests that applyBridgeFqCodel fails gracefully
// when tc command fails (e.g., interface doesn't exist).
func TestApplyBridgeFqCodelRequiresTc(t *testing.T) {
	if !tcAvailable() {
		t.Skip("tc command not available")
	}
	if !isRoot() {
		t.Skip("tc commands require root")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	n := &NAT{
		log: log,
	}

	ctx := context.Background()

	// Attempt to apply fq_codel to non-existent bridge
	err := n.applyBridgeFqCodel(ctx, "br-nonexistent")
	if err == nil {
		t.Error("expected error when applying fq_codel to non-existent bridge")
	}
}
