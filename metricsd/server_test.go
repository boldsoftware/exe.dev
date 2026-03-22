package metricsd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
					IOReadBytes:           500_000_000,
					IOWriteBytes:          250_000_000,
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
					IOReadBytes:           1_000_000_000,
					IOWriteBytes:          750_000_000,
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
		if result.Metrics[0].IOReadBytes != 500_000_000 {
			t.Errorf("io_read_bytes = %d, want %d", result.Metrics[0].IOReadBytes, 500_000_000)
		}
		if result.Metrics[0].IOWriteBytes != 250_000_000 {
			t.Errorf("io_write_bytes = %d, want %d", result.Metrics[0].IOWriteBytes, 250_000_000)
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

		// Verify Content-Type is octet-stream
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "octet-stream") {
			t.Errorf("Content-Type = %q, want containing %q", ct, "octet-stream")
		}

		// Read body and verify it's a non-empty parquet file
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) == 0 {
			t.Fatal("response body is empty")
		}
		// Parquet files start with magic bytes "PAR1"
		if len(body) < 4 || string(body[:4]) != "PAR1" {
			t.Errorf("body does not start with PAR1 magic bytes, got %q", body[:min(4, len(body))])
		}

		// Write parquet to temp file and read back with DuckDB to validate contents
		tmpFile, err := os.CreateTemp("", "test-sparkline-*.parquet")
		if err != nil {
			t.Fatalf("create temp file: %v", err)
		}
		defer os.Remove(tmpFile.Name())
		if _, err := tmpFile.Write(body); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		tmpFile.Close()

		// Query the parquet file for row count and columns
		var rowCount int
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM read_parquet('%s')`, tmpFile.Name())).Scan(&rowCount); err != nil {
			t.Fatalf("count parquet rows: %v", err)
		}
		if rowCount != 3 {
			t.Errorf("parquet row count = %d, want 3", rowCount)
		}

		// Verify column names
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM read_parquet('%s') LIMIT 0`, tmpFile.Name()))
		if err != nil {
			t.Fatalf("query parquet columns: %v", err)
		}
		cols, _ := rows.Columns()
		rows.Close()
		wantCols := []string{
			"timestamp", "host", "vm_name", "disk_size_bytes", "disk_used_bytes",
			"disk_logical_used_bytes", "memory_nominal_bytes", "memory_rss_bytes", "memory_swap_bytes",
			"cpu_used_cumulative_seconds", "cpu_nominal", "network_tx_bytes", "network_rx_bytes", "resource_group",
			"io_read_bytes", "io_write_bytes",
		}
		if len(cols) != len(wantCols) {
			t.Errorf("got %d columns, want %d", len(cols), len(wantCols))
		}
		for i, c := range cols {
			if i < len(wantCols) && c != wantCols[i] {
				t.Errorf("column %d = %q, want %q", i, c, wantCols[i])
			}
		}
	})

	t.Run("sparkline_data_hours_param", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/query/sparkline?hours=1")
		if err != nil {
			t.Fatalf("GET /query/sparkline?hours=1: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		// Verify it's a valid parquet response
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) < 4 || string(body[:4]) != "PAR1" {
			t.Errorf("body does not start with PAR1 magic bytes")
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

func TestQueryVMs(t *testing.T) {
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

	// Insert test data
	now := time.Now().UTC()
	metrics := []Metric{
		{Timestamp: now.Add(-2 * time.Hour), Host: "h1", VMName: "vm-a", DiskSizeBytes: 20e9, DiskLogicalUsedBytes: 5e9, CPUUsedCumulativeSecs: 100, CPUNominal: 2, NetworkTXBytes: 1000, NetworkRXBytes: 500},
		{Timestamp: now.Add(-1 * time.Hour), Host: "h1", VMName: "vm-a", DiskSizeBytes: 20e9, DiskLogicalUsedBytes: 5e9, CPUUsedCumulativeSecs: 200, CPUNominal: 2, NetworkTXBytes: 2000, NetworkRXBytes: 1500},
		{Timestamp: now, Host: "h1", VMName: "vm-a", DiskSizeBytes: 20e9, DiskLogicalUsedBytes: 6e9, CPUUsedCumulativeSecs: 350, CPUNominal: 2, NetworkTXBytes: 5000, NetworkRXBytes: 3000},
		{Timestamp: now.Add(-1 * time.Hour), Host: "h2", VMName: "vm-b", DiskSizeBytes: 10e9, DiskLogicalUsedBytes: 2e9, CPUUsedCumulativeSecs: 50, CPUNominal: 1, NetworkTXBytes: 100, NetworkRXBytes: 200},
		{Timestamp: now, Host: "h2", VMName: "vm-b", DiskSizeBytes: 10e9, DiskLogicalUsedBytes: 3e9, CPUUsedCumulativeSecs: 80, CPUNominal: 1, NetworkTXBytes: 300, NetworkRXBytes: 400},
	}
	if err := srv.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	t.Run("query both VMs", func(t *testing.T) {
		reqBody, _ := json.Marshal(QueryVMsRequest{VMNames: []string{"vm-a", "vm-b"}, Hours: 24})
		resp, err := http.Post(ts.URL+"/query/vms", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		var result QueryVMsResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if len(result.VMs) != 2 {
			t.Errorf("expected 2 VMs, got %d", len(result.VMs))
		}
		if len(result.VMs["vm-a"]) == 0 {
			t.Error("expected data for vm-a")
		}
		if len(result.VMs["vm-b"]) == 0 {
			t.Error("expected data for vm-b")
		}
	})

	t.Run("query single VM", func(t *testing.T) {
		reqBody, _ := json.Marshal(QueryVMsRequest{VMNames: []string{"vm-a"}, Hours: 24})
		resp, err := http.Post(ts.URL+"/query/vms", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result QueryVMsResponse
		json.NewDecoder(resp.Body).Decode(&result)
		if len(result.VMs) != 1 {
			t.Errorf("expected 1 VM, got %d", len(result.VMs))
		}
	})

	t.Run("empty vm_names", func(t *testing.T) {
		reqBody, _ := json.Marshal(QueryVMsRequest{VMNames: []string{}, Hours: 24})
		resp, err := http.Post(ts.URL+"/query/vms", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("invalid hours", func(t *testing.T) {
		reqBody, _ := json.Marshal(QueryVMsRequest{VMNames: []string{"vm-a"}, Hours: 0})
		resp, err := http.Post(ts.URL+"/query/vms", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("expected 400, got %d", resp.StatusCode)
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
