package server

import (
	"context"
	"testing"
	"time"

	"exe.dev/exe-ops/apitype"
)

func testDB(t *testing.T) *Store {
	t.Helper()
	db, err := OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestUpsertAndListServers(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	report := &apitype.Report{
		Name:      "exelet-nyc-prod-01",
		Tags:      []string{"web", "primary"},
		Timestamp: time.Now().UTC(),
		CPU:       45.5,
		MemTotal:  8 * 1024 * 1024 * 1024,
		MemUsed:   4 * 1024 * 1024 * 1024,
		MemFree:   4 * 1024 * 1024 * 1024,
		DiskTotal: 100 * 1024 * 1024 * 1024,
		DiskUsed:  50 * 1024 * 1024 * 1024,
		DiskFree:  50 * 1024 * 1024 * 1024,
		NetSend:   1000,
		NetRecv:   2000,
	}
	parts := apitype.HostnameParts{Role: "exelet", Region: "nyc", Env: "prod", Instance: "01"}

	serverID, err := store.UpsertServer(ctx, report, parts)
	if err != nil {
		t.Fatalf("upsert server: %v", err)
	}
	if serverID == 0 {
		t.Fatal("expected non-zero server ID")
	}

	if err := store.InsertReport(ctx, serverID, report); err != nil {
		t.Fatalf("insert report: %v", err)
	}

	servers, err := store.ListServers(ctx)
	if err != nil {
		t.Fatalf("list servers: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}

	s := servers[0]
	if s.Name != "exelet-nyc-prod-01" {
		t.Errorf("name = %q, want %q", s.Name, "exelet-nyc-prod-01")
	}
	if s.Region != "nyc" {
		t.Errorf("region = %q, want %q", s.Region, "nyc")
	}
	if s.CPU != 45.5 {
		t.Errorf("cpu = %f, want 45.5", s.CPU)
	}
}

func TestGetServer(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()

	// Non-existent server.
	sd, err := store.GetServer(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if sd != nil {
		t.Fatal("expected nil for non-existent server")
	}

	// Insert server + reports.
	report := &apitype.Report{
		Name:       "exeprox-lax-staging-01",
		Tags:       []string{"proxy"},
		Timestamp:  time.Now().UTC(),
		CPU:        12.3,
		MemTotal:   16 * 1024 * 1024 * 1024,
		MemUsed:    8 * 1024 * 1024 * 1024,
		MemFree:    8 * 1024 * 1024 * 1024,
		DiskTotal:  200 * 1024 * 1024 * 1024,
		DiskUsed:   100 * 1024 * 1024 * 1024,
		DiskFree:   100 * 1024 * 1024 * 1024,
		NetSend:    500,
		NetRecv:    1500,
		UptimeSecs: 86400,
		Components: []apitype.Component{{Name: "exeprox", Version: "1.2.3", Status: "active"}},
		Updates:    []string{"libssl3/1.1.1-2"},
	}
	parts := apitype.HostnameParts{Role: "exeprox", Region: "lax", Env: "staging", Instance: "01"}

	serverID, err := store.UpsertServer(ctx, report, parts)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.InsertReport(ctx, serverID, report); err != nil {
		t.Fatalf("insert: %v", err)
	}

	sd, err = store.GetServer(ctx, "exeprox-lax-staging-01")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if sd == nil {
		t.Fatal("expected server detail")
	}
	if sd.Role != "exeprox" {
		t.Errorf("role = %q, want %q", sd.Role, "exeprox")
	}
	if len(sd.Components) != 1 || sd.Components[0].Name != "exeprox" {
		t.Errorf("components = %+v, want 1 exeprox component", sd.Components)
	}
	if len(sd.History) != 1 {
		t.Errorf("history len = %d, want 1", len(sd.History))
	}
}

func TestUpsertServerUpdatesOnConflict(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()
	parts := apitype.HostnameParts{Role: "exelet", Region: "nyc", Env: "prod", Instance: "01"}

	report1 := &apitype.Report{
		Name:      "exelet-nyc-prod-01",
		Tags:      []string{"v1"},
		Timestamp: time.Now().UTC(),
	}
	id1, err := store.UpsertServer(ctx, report1, parts)
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}

	report2 := &apitype.Report{
		Name:      "exelet-nyc-prod-01",
		Tags:      []string{"v2"},
		Timestamp: time.Now().UTC(),
	}
	id2, err := store.UpsertServer(ctx, report2, parts)
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	if id1 != id2 {
		t.Errorf("expected same server ID, got %d and %d", id1, id2)
	}
}

func TestPurgeOldReports(t *testing.T) {
	store := testDB(t)
	ctx := context.Background()
	parts := apitype.HostnameParts{}

	report := &apitype.Report{
		Name:      "test-server-prod-01",
		Tags:      []string{},
		Timestamp: time.Now().UTC().Add(-8 * 24 * time.Hour), // 8 days ago
	}

	serverID, err := store.UpsertServer(ctx, report, parts)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.InsertReport(ctx, serverID, report); err != nil {
		t.Fatalf("insert: %v", err)
	}

	deleted, err := store.PurgeOldReports(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
}
