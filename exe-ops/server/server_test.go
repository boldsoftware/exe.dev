package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exe.dev/exe-ops/apitype"
)

func testServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := NewStore(db)
	log := slog.Default()
	hub := NewHub(log)
	handler := New(store, hub, "test-token", nil, log, nil, nil, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, store
}

func TestPostReportAndGetServers(t *testing.T) {
	ts, _ := testServer(t)

	report := apitype.Report{
		Name:      "exelet-nyc-prod-01",
		Tags:      []string{"web"},
		Timestamp: time.Now().UTC(),
		CPU:       55.5,
		MemTotal:  8 * 1024 * 1024 * 1024,
		MemUsed:   4 * 1024 * 1024 * 1024,
		MemFree:   4 * 1024 * 1024 * 1024,
		DiskTotal: 100 * 1024 * 1024 * 1024,
		DiskUsed:  50 * 1024 * 1024 * 1024,
		DiskFree:  50 * 1024 * 1024 * 1024,
		NetSend:   1000,
		NetRecv:   2000,
	}

	// POST report.
	body, _ := json.Marshal(report)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post report: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	// GET servers.
	resp, err = http.Get(ts.URL + "/api/v1/servers")
	if err != nil {
		t.Fatalf("get servers: %v", err)
	}
	defer resp.Body.Close()

	var servers []apitype.ServerSummary
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Name != "exelet-nyc-prod-01" {
		t.Errorf("name = %q", servers[0].Name)
	}
	if servers[0].CPU != 55.5 {
		t.Errorf("cpu = %f, want 55.5", servers[0].CPU)
	}

	// GET server detail.
	resp, err = http.Get(ts.URL + "/api/v1/servers/exelet-nyc-prod-01")
	if err != nil {
		t.Fatalf("get server detail: %v", err)
	}
	defer resp.Body.Close()

	var detail apitype.ServerDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Region != "nyc" {
		t.Errorf("region = %q, want nyc", detail.Region)
	}
}

func TestPostReportUnauthorized(t *testing.T) {
	ts, _ := testServer(t)

	body := []byte(`{"name":"test"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHealthEndpoint(t *testing.T) {
	ts, _ := testServer(t)

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("status = %q, want ok", result["status"])
	}
}
