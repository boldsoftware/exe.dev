package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestBillingUsageAPI tests the /api/billing/usage and /api/billing/usage/vms
// endpoints end-to-end with a real metricsd and exed.
func TestBillingUsageAPI(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	if Env.servers.Metricsd == nil {
		t.Skip("metricsd not configured")
	}

	// Register a user and create a box so metrics get generated.
	pty, cookies, _, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	defer pty.Disconnect()
	defer pty.deleteBox(boxName)

	// Wait for metrics to flow from exelet → metricsd.
	ctx := Env.context(t)
	_, err := Env.servers.Metricsd.WaitForMetrics(ctx, boxName, 60*time.Second)
	if err != nil {
		t.Fatalf("waiting for metrics: %v", err)
	}

	// Use a 30-day window around "now" — metricsd caps daily/monthly at 366 days.
	now := time.Now().UTC()
	windowStart := now.AddDate(0, 0, -30).Format(time.RFC3339)
	windowEnd := now.AddDate(0, 0, 1).Format(time.RFC3339)

	client := newClientWithCookies(t, cookies)

	t.Run("unauthenticated_usage_401", func(t *testing.T) {
		noGolden(t)

		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/api/billing/usage?granularity=monthly&start=%s&end=%s", Env.HTTPPort(), windowStart, windowEnd))
		if err != nil {
			t.Fatalf("GET /api/billing/usage failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("unauthenticated_vms_401", func(t *testing.T) {
		noGolden(t)

		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/api/billing/usage/vms?start=%s&end=%s", Env.HTTPPort(), windowStart, windowEnd))
		if err != nil {
			t.Fatalf("GET /api/billing/usage/vms failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("missing_params_400", func(t *testing.T) {
		noGolden(t)

		cases := []struct {
			name string
			url  string
		}{
			{"no granularity", "/api/billing/usage?start=2024-01-01T00:00:00Z&end=2025-01-01T00:00:00Z"},
			{"no start", "/api/billing/usage?granularity=monthly&end=2025-01-01T00:00:00Z"},
			{"no end", "/api/billing/usage?granularity=monthly&start=2024-01-01T00:00:00Z"},
			{"bad granularity", "/api/billing/usage?granularity=hourly&start=2024-01-01T00:00:00Z&end=2025-01-01T00:00:00Z"},
			{"vms no start", "/api/billing/usage/vms?end=2025-01-01T00:00:00Z"},
			{"vms no end", "/api/billing/usage/vms?start=2024-01-01T00:00:00Z"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				resp, err := client.Get(fmt.Sprintf("http://localhost:%d%s", Env.HTTPPort(), tc.url))
				if err != nil {
					t.Fatalf("GET %s failed: %v", tc.url, err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusBadRequest {
					body, _ := io.ReadAll(resp.Body)
					t.Errorf("%s: expected 400, got %d: %s", tc.name, resp.StatusCode, body)
				}
			})
		}
	})

	t.Run("monthly_returns_json", func(t *testing.T) {
		noGolden(t)

		url := fmt.Sprintf("http://localhost:%d/api/billing/usage?granularity=monthly&start=%s&end=%s", Env.HTTPPort(), windowStart, windowEnd)
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		var result struct {
			PeriodStart string `json:"period_start"`
			PeriodEnd   string `json:"period_end"`
			Metrics     []struct {
				Date           string `json:"date"`
				DiskAvgBytes   int64  `json:"disk_avg_bytes"`
				BandwidthBytes int64  `json:"bandwidth_bytes"`
			} `json:"metrics"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result.PeriodStart == "" || result.PeriodEnd == "" {
			t.Error("expected period_start and period_end in response")
		}
		// Metrics may or may not have data yet depending on rollup timing,
		// but the response shape must be correct (array, not null).
		if result.Metrics == nil {
			t.Error("expected metrics array (possibly empty), got null")
		}
	})

	t.Run("daily_returns_json", func(t *testing.T) {
		noGolden(t)

		url := fmt.Sprintf("http://localhost:%d/api/billing/usage?granularity=daily&start=%s&end=%s", Env.HTTPPort(), windowStart, windowEnd)
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		var result struct {
			PeriodStart string `json:"period_start"`
			PeriodEnd   string `json:"period_end"`
			Metrics     []struct {
				Date           string `json:"date"`
				DiskAvgBytes   int64  `json:"disk_avg_bytes"`
				BandwidthBytes int64  `json:"bandwidth_bytes"`
			} `json:"metrics"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result.Metrics == nil {
			t.Error("expected metrics array (possibly empty), got null")
		}
	})

	t.Run("vms_returns_json", func(t *testing.T) {
		noGolden(t)

		url := fmt.Sprintf("http://localhost:%d/api/billing/usage/vms?start=%s&end=%s", Env.HTTPPort(), windowStart, windowEnd)
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		var result struct {
			PeriodStart string `json:"period_start"`
			PeriodEnd   string `json:"period_end"`
			Metrics     []struct {
				VMID           string  `json:"vm_id"`
				VMName         string  `json:"vm_name"`
				DiskAvgBytes   int64   `json:"disk_avg_bytes"`
				BandwidthBytes int64   `json:"bandwidth_bytes"`
				CPUSeconds     float64 `json:"cpu_seconds"`
				IOReadBytes    int64   `json:"io_read_bytes"`
				IOWriteBytes   int64   `json:"io_write_bytes"`
				DaysWithData   int     `json:"days_with_data"`
			} `json:"metrics"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if result.Metrics == nil {
			t.Error("expected metrics array (possibly empty), got null")
		}
	})

	t.Run("post_not_allowed", func(t *testing.T) {
		noGolden(t)

		url := fmt.Sprintf("http://localhost:%d/api/billing/usage?granularity=monthly&start=2024-01-01T00:00:00Z&end=2025-01-01T00:00:00Z", Env.HTTPPort())
		resp, err := client.Post(url, "application/json", nil)
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", resp.StatusCode)
		}
	})

	t.Run("cross_user_isolation", func(t *testing.T) {
		noGolden(t)

		// Register a second user (user B) — no VM needed.
		ptyB, cookiesB, _, _ := registerForExeDev(t)
		ptyB.Disconnect()

		clientB := newClientWithCookies(t, cookiesB)

		// User B queries /api/billing/usage/vms for the same period.
		url := fmt.Sprintf("http://localhost:%d/api/billing/usage/vms?start=%s&end=%s", Env.HTTPPort(), windowStart, windowEnd)
		resp, err := clientB.Get(url)
		if err != nil {
			t.Fatalf("GET as user B failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Metrics []struct {
				VMName string `json:"vm_name"`
			} `json:"metrics"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// User B should not see user A's box.
		for _, vm := range result.Metrics {
			if vm.VMName == boxName {
				t.Errorf("user B should not see user A's box %q in /api/billing/usage/vms", boxName)
			}
		}
	})
}
