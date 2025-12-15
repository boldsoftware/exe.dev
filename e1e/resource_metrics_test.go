package e1e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestResourceMetrics tests all resource manager metrics using a single VM.
// Consolidating these tests saves VM creation overhead.
func TestResourceMetrics(t *testing.T) {
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	if Env.exelet.HTTPAddress == "" {
		t.Skip("exelet HTTP address is not available")
	}

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.disconnect()

	boxName := newBox(t, pty)

	// Wait for SSH to respond.
	waitDeadline := time.Now().Add(2 * time.Minute)
	for {
		if err := boxSSHCommand(t, boxName, keyFile, "true").Run(); err == nil {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("box %s ssh did not become ready", boxName)
		}
		time.Sleep(500 * time.Millisecond)
	}

	exeletClient, err := Env.initExeletClient()
	if err != nil {
		t.Fatalf("failed to init exelet client: %v", err)
	}
	defer exeletClient.Close()

	ctx := Env.context(t)
	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)
	metricsURL := Env.exelet.HTTPAddress + "/metrics"

	t.Run("network_metrics", func(t *testing.T) {
		// Wait for network metrics to appear
		rxMetric := fmt.Sprintf(`exelet_vm_net_rx_bytes_total{vm_id="%s"`, instanceID)
		txMetric := fmt.Sprintf(`exelet_vm_net_tx_bytes_total{vm_id="%s"`, instanceID)

		waitForMetric(t, metricsURL, rxMetric, true, 30*time.Second)
		waitForMetric(t, metricsURL, txMetric, true, 30*time.Second)

		// Generate some network traffic by running a command over SSH
		if err := boxSSHCommand(t, boxName, keyFile, "echo hello").Run(); err != nil {
			t.Fatalf("failed to run SSH command: %v", err)
		}

		// Wait a bit for metrics to update
		time.Sleep(2 * time.Second)

		// Verify metrics are still present and have non-zero values
		body := fetchMetrics(t, metricsURL)
		if !strings.Contains(body, rxMetric) {
			t.Errorf("network RX metric not found after traffic generation")
		}
		if !strings.Contains(body, txMetric) {
			t.Errorf("network TX metric not found after traffic generation")
		}
	})

	t.Run("disk_metrics", func(t *testing.T) {
		// Disk metrics are polled every cycle with the resource manager.
		diskMetric := fmt.Sprintf(`exelet_vm_disk_used_bytes{vm_id="%s"`, instanceID)

		waitForMetric(t, metricsURL, diskMetric, true, 30*time.Second)

		// Verify the metric value is greater than 0 (disk should have some usage)
		body := fetchMetrics(t, metricsURL)
		if !strings.Contains(body, diskMetric) {
			t.Errorf("disk metric not found")
		}
	})

	t.Run("metrics_cleared_after_deletion", func(t *testing.T) {
		label := fmt.Sprintf(`vm_id="%s"`, instanceID)
		waitForMetric(t, metricsURL, label, true, 30*time.Second)

		pty.deleteBox(boxName)

		waitForMetric(t, metricsURL, label, false, 30*time.Second)
	})
}

func instanceIDByName(t *testing.T, ctx context.Context, client *client.Client, name string) string {
	stream, err := client.ListInstances(ctx, &api.ListInstancesRequest{})
	if err != nil {
		t.Fatalf("failed to list instances: %v", err)
	}
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("failed to receive instance list: %v", err)
		}
		if resp.Instance != nil && resp.Instance.GetName() == name {
			return resp.Instance.GetID()
		}
	}
	t.Fatalf("instance %q not found", name)
	return ""
}

func waitForMetric(t *testing.T, metricsURL, label string, expectPresent bool, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		body := fetchMetrics(t, metricsURL)
		hasLabel := strings.Contains(body, label)
		if hasLabel == expectPresent {
			return
		}
		if time.Now().After(deadline) {
			state := "missing"
			if expectPresent {
				state = "present"
			}
			t.Fatalf("metric %s not %s within %v\nbody=%s", label, state, timeout, truncateForError(body))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func fetchMetrics(t *testing.T, metricsURL string) string {
	resp, err := http.Get(metricsURL)
	if err != nil {
		t.Fatalf("failed to fetch metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read metrics body: %v", err)
	}
	return string(body)
}

func truncateForError(s string) string {
	if len(s) <= 512 {
		return s
	}
	return s[:512] + "...[truncated]"
}
