package e1e

import (
	"testing"
	"time"
)

// TestMetricsDaemonE2E tests that VM metrics flow from exelet to metricsd.
// It just verifies that some metrics exist - other tests likely ran before this
// and generated metrics data.
func TestMetricsDaemonE2E(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Skip if metricsd is not configured in the test environment
	if Env.servers.Metricsd == nil {
		t.Skip("metricsd not configured")
	}

	// Wait for any metrics to appear (from this or other tests).
	// The exelet sends metrics every 20s in tests.
	ctx := Env.context(t)
	deadline := time.Now().Add(30 * time.Second)
	var count int
	for time.Now().Before(deadline) {
		metrics, err := Env.servers.Metricsd.QueryMetrics(ctx, "") // query all VMs
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if len(metrics) > 0 {
			count = len(metrics)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if count == 0 {
		t.Fatal("no metrics found within timeout")
	}

	t.Logf("Found %d metrics in metricsd", count)
}
