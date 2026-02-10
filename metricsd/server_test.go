package metricsd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServer(t *testing.T) {
	ctx := context.Background()

	// Use in-memory database
	connector, db, err := OpenDB(ctx, "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	t.Run("index", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/")
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		// Check content type
		ct := resp.Header.Get("Content-Type")
		if ct != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
		}
	})

	t.Run("health", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/health")
		if err != nil {
			t.Fatalf("GET /health: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
	})

	t.Run("post_empty_batch", func(t *testing.T) {
		body := `{"metrics": []}`
		resp, err := http.Post(ts.URL+"/write", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST /write: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	})

	t.Run("post_invalid_json", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/write", "application/json", bytes.NewBufferString("not json"))
		if err != nil {
			t.Fatalf("POST /write: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	})

	t.Run("post_and_get_metrics", func(t *testing.T) {
		now := time.Now().Truncate(time.Microsecond)
		batch := MetricsBatch{
			Metrics: []Metric{
				{
					Timestamp:             now,
					Host:                  "ctr-host-1",
					VMName:                "test-vm-1",
					ResourceGroup:         "acct-alice",
					DiskSizeBytes:         100_000_000_000,
					DiskUsedBytes:         50_000_000_000,
					DiskLogicalUsedBytes:  75_000_000_000,
					MemoryNominalBytes:    8_000_000_000,
					MemoryRSSBytes:        4_000_000_000,
					MemorySwapBytes:       100_000_000,
					CPUUsedCumulativeSecs: 3600.5,
					CPUNominal:            4.0,
					NetworkTXBytes:        1_000_000_000,
					NetworkRXBytes:        2_000_000_000,
				},
				{
					Timestamp:             now.Add(-time.Minute),
					Host:                  "ctr-host-2",
					VMName:                "test-vm-2",
					ResourceGroup:         "acct-bob",
					DiskSizeBytes:         200_000_000_000,
					DiskUsedBytes:         75_000_000_000,
					DiskLogicalUsedBytes:  150_000_000_000,
					MemoryNominalBytes:    16_000_000_000,
					MemoryRSSBytes:        8_000_000_000,
					MemorySwapBytes:       0,
					CPUUsedCumulativeSecs: 7200.0,
					CPUNominal:            8.0,
					NetworkTXBytes:        5_000_000_000,
					NetworkRXBytes:        10_000_000_000,
				},
			},
		}

		body, _ := json.Marshal(batch)
		resp, err := http.Post(ts.URL+"/write", "application/json", bytes.NewBuffer(body))
		if err != nil {
			t.Fatalf("POST /write: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("POST status = %d, want %d", resp.StatusCode, http.StatusCreated)
		}

		// Get all metrics
		resp, err = http.Get(ts.URL + "/query")
		if err != nil {
			t.Fatalf("GET /query: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		var result MetricsBatch
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(result.Metrics) != 2 {
			t.Errorf("got %d metrics, want 2", len(result.Metrics))
		}

		// Results should be ordered by timestamp DESC
		if result.Metrics[0].VMName != "test-vm-1" {
			t.Errorf("first metric vm_name = %q, want %q", result.Metrics[0].VMName, "test-vm-1")
		}
		if result.Metrics[0].DiskSizeBytes != 100_000_000_000 {
			t.Errorf("disk_size_bytes = %d, want %d", result.Metrics[0].DiskSizeBytes, 100_000_000_000)
		}
		if result.Metrics[0].CPUNominal != 4.0 {
			t.Errorf("cpu_nominal = %f, want %f", result.Metrics[0].CPUNominal, 4.0)
		}
		if result.Metrics[0].ResourceGroup != "acct-alice" {
			t.Errorf("resource_group = %q, want %q", result.Metrics[0].ResourceGroup, "acct-alice")
		}
	})

	t.Run("filter_by_vm_name", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/query?vm_name=test-vm-1")
		if err != nil {
			t.Fatalf("GET /query?vm_name=test-vm-1: %v", err)
		}
		defer resp.Body.Close()

		var result MetricsBatch
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(result.Metrics) != 1 {
			t.Errorf("got %d metrics, want 1", len(result.Metrics))
		}
		if len(result.Metrics) > 0 && result.Metrics[0].VMName != "test-vm-1" {
			t.Errorf("vm_name = %q, want %q", result.Metrics[0].VMName, "test-vm-1")
		}
	})

	t.Run("limit", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/query?limit=1")
		if err != nil {
			t.Fatalf("GET /query?limit=1: %v", err)
		}
		defer resp.Body.Close()

		var result MetricsBatch
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(result.Metrics) != 1 {
			t.Errorf("got %d metrics, want 1", len(result.Metrics))
		}
	})
}

func TestSparklines(t *testing.T) {
	ctx := context.Background()

	connector, db, err := OpenDB(ctx, "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Insert test data: two VMs, two data points each
	now := time.Now().Truncate(time.Microsecond)
	batch := MetricsBatch{
		Metrics: []Metric{
			{
				Timestamp:             now.Add(-10 * time.Minute),
				Host:                  "host-1",
				VMName:                "vm-alpha",
				ResourceGroup:         "acct-alice",
				DiskSizeBytes:         100_000_000_000,
				DiskUsedBytes:         50_000_000_000,
				DiskLogicalUsedBytes:  75_000_000_000,
				MemoryNominalBytes:    8_000_000_000,
				MemoryRSSBytes:        4_000_000_000,
				MemorySwapBytes:       100_000_000,
				CPUUsedCumulativeSecs: 1000.0,
				CPUNominal:            4.0,
				NetworkTXBytes:        1_000_000,
				NetworkRXBytes:        2_000_000,
			},
			{
				Timestamp:             now,
				Host:                  "host-1",
				VMName:                "vm-alpha",
				ResourceGroup:         "acct-alice",
				DiskSizeBytes:         100_000_000_000,
				DiskUsedBytes:         55_000_000_000,
				DiskLogicalUsedBytes:  80_000_000_000,
				MemoryNominalBytes:    8_000_000_000,
				MemoryRSSBytes:        5_000_000_000,
				MemorySwapBytes:       200_000_000,
				CPUUsedCumulativeSecs: 1600.0,
				CPUNominal:            4.0,
				NetworkTXBytes:        2_000_000,
				NetworkRXBytes:        4_000_000,
			},
			{
				Timestamp:             now,
				Host:                  "host-2",
				VMName:                "vm-beta",
				ResourceGroup:         "acct-bob",
				DiskSizeBytes:         200_000_000_000,
				DiskUsedBytes:         100_000_000_000,
				DiskLogicalUsedBytes:  150_000_000_000,
				MemoryNominalBytes:    16_000_000_000,
				MemoryRSSBytes:        8_000_000_000,
				MemorySwapBytes:       0,
				CPUUsedCumulativeSecs: 5000.0,
				CPUNominal:            8.0,
				NetworkTXBytes:        10_000_000,
				NetworkRXBytes:        20_000_000,
			},
		},
	}

	body, _ := json.Marshal(batch)
	resp, err := http.Post(ts.URL+"/write", "application/json", bytes.NewBuffer(body))
	if err != nil {
		t.Fatalf("POST /write: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	t.Run("sparklines_page", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/sparklines")
		if err != nil {
			t.Fatalf("GET /sparklines: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		ct := resp.Header.Get("Content-Type")
		if ct != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
		}
	})

	t.Run("sparkline_data", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/query/sparkline")
		if err != nil {
			t.Fatalf("GET /query/sparkline: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		var result SparklineResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(result.Metrics) != 3 {
			t.Errorf("got %d metrics, want 3", len(result.Metrics))
		}
		if len(result.Counters) != 3 {
			t.Errorf("got %d counters, want 3", len(result.Counters))
		}
		// Verify counter field names
		wantCounters := map[string]bool{
			"cpu_used_cumulative_seconds": true,
			"network_tx_bytes":            true,
			"network_rx_bytes":            true,
		}
		for _, c := range result.Counters {
			if !wantCounters[c] {
				t.Errorf("unexpected counter %q", c)
			}
		}

		// Verify ordering: vm-alpha rows before vm-beta (ORDER BY vm_name, timestamp ASC)
		if len(result.Metrics) == 3 {
			if result.Metrics[0].VMName != "vm-alpha" {
				t.Errorf("first metric vm_name = %q, want %q", result.Metrics[0].VMName, "vm-alpha")
			}
			if result.Metrics[2].VMName != "vm-beta" {
				t.Errorf("last metric vm_name = %q, want %q", result.Metrics[2].VMName, "vm-beta")
			}
			// Within vm-alpha, should be timestamp ascending
			if !result.Metrics[0].Timestamp.Before(result.Metrics[1].Timestamp) {
				t.Error("vm-alpha metrics not in ascending timestamp order")
			}
		}
	})

	t.Run("sparkline_data_hours_param", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/query/sparkline?hours=1")
		if err != nil {
			t.Fatalf("GET /query/sparkline?hours=1: %v", err)
		}
		defer resp.Body.Close()

		var result SparklineResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		// All 3 metrics were inserted within last hour, so should all be returned
		if len(result.Metrics) != 3 {
			t.Errorf("got %d metrics with hours=1, want 3", len(result.Metrics))
		}
	})

	t.Run("sparkline_data_invalid_hours", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/query/sparkline?hours=abc")
		if err != nil {
			t.Fatalf("GET /query/sparkline?hours=abc: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	})
}

func TestInsertMetrics_DefaultTimestamp(t *testing.T) {
	ctx := context.Background()

	connector, db, err := OpenDB(ctx, "")
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	defer connector.Close()

	srv := NewServer(connector, db, false)
	defer srv.Close()

	// Insert metric without timestamp
	err = srv.InsertMetrics(ctx, []Metric{{
		Host:          "test-host",
		VMName:        "no-timestamp-vm",
		DiskSizeBytes: 1000,
	}})
	if err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	// Verify it was inserted with a timestamp
	metrics, err := srv.QueryMetrics(ctx, "no-timestamp-vm", "1")
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("got %d metrics, want 1", len(metrics))
	}
	if metrics[0].Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
	// Timestamp should be recent (within last minute)
	if time.Since(metrics[0].Timestamp) > time.Minute {
		t.Errorf("timestamp %v is too old", metrics[0].Timestamp)
	}
}
