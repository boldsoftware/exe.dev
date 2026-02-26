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

// TestMetricsClearedOnDelete tests that exelet metrics are cleared when a VM is deleted.
// This test requires its own VM since it deletes the box.
// Network and disk metrics tests are in TestVanillaBox.
func TestMetricsClearedOnDelete(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	if len(Env.servers.Exelets) == 0 || Env.servers.Exelets[0].HTTPAddress == "" {
		t.Skip("exelet HTTP address is not available")
	}

	pty, _, _, _ := registerForExeDev(t)
	defer pty.Disconnect()

	boxName := newBox(t, pty)

	exeletClient := Env.servers.Exelets[0].Client()
	ctx := Env.context(t)
	instanceID := instanceIDByName(t, ctx, exeletClient, boxName)
	metricsURL := Env.servers.Exelets[0].HTTPAddress + "/metrics"

	label := fmt.Sprintf(`vm_id="%s"`, instanceID)
	waitForMetric(t, metricsURL, label, true, 30*time.Second)

	pty.deleteBox(boxName)

	waitForMetric(t, metricsURL, label, false, 30*time.Second)
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
