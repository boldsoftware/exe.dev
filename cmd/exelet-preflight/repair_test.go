package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"exe.dev/pkg/ipam"
)

func readLeases(t *testing.T, path string) *ipam.LeaseDB {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read leases: %v", err)
	}
	db := &ipam.LeaseDB{}
	if err := json.Unmarshal(data, db); err != nil {
		t.Fatalf("unmarshal leases: %v", err)
	}
	return db
}

func TestRepairAddsMissingLeases(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeInstance(t, "vm000002-b", "10.42.0.4/16", "aa:bb:cc:00:00:04")
	// Only one lease on disk — inst b is missing its lease.
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
	)

	report, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(report.MissingLeases) != 1 {
		t.Fatalf("expected 1 missing lease, got %+v", report.MissingLeases)
	}

	res, err := repairMissingLeases(f.ipamDir, report, false)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0] != "10.42.0.4 aa:bb:cc:00:00:04" {
		t.Errorf("unexpected added list: %+v", res.Added)
	}

	db := readLeases(t, filepath.Join(f.ipamDir, "leases.json"))
	if _, ok := db.IPs["10.42.0.4"]; !ok {
		t.Errorf("repaired IP 10.42.0.4 missing from leases.json IPs map")
	}
	if _, ok := db.Hosts["aa:bb:cc:00:00:04"]; !ok {
		t.Errorf("repaired MAC aa:bb:cc:00:00:04 missing from leases.json Hosts map")
	}
	if _, ok := db.IPs["10.42.0.3"]; !ok {
		t.Errorf("pre-existing lease 10.42.0.3 was lost — repair must merge")
	}

	// Verify a rescan shows zero missing leases.
	report2, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(report2.MissingLeases) != 0 {
		t.Errorf("rescan still shows missing leases: %+v", report2.MissingLeases)
	}

	// Backup file should exist.
	if _, err := os.Stat(res.Backup); err != nil {
		t.Errorf("expected backup at %s, stat error: %v", res.Backup, err)
	}
}

func TestRepairDryRunDoesNotMutate(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeInstance(t, "vm000002-b", "10.42.0.4/16", "aa:bb:cc:00:00:04")
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
	)

	leasesPath := filepath.Join(f.ipamDir, "leases.json")
	before, err := os.ReadFile(leasesPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	report, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	res, err := repairMissingLeases(f.ipamDir, report, true)
	if err != nil {
		t.Fatalf("repair dry-run: %v", err)
	}
	if !res.DryRun {
		t.Errorf("expected DryRun=true in result")
	}
	if len(res.Added) != 1 {
		t.Errorf("dry-run should still report the would-add, got %+v", res.Added)
	}

	after, err := os.ReadFile(leasesPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("dry-run modified leases.json:\nbefore=%s\nafter=%s", before, after)
	}
	if _, err := os.Stat(leasesPath + ".bak"); !os.IsNotExist(err) {
		t.Errorf("dry-run should not create backup; stat err=%v", err)
	}
}

func TestRepairSkipsExistingMACCollision(t *testing.T) {
	f := newFixture(t)
	// Scenario: two instances claim the same MAC — config corruption. First one
	// already has its lease. The second should be skipped, not force-overwrite.
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeInstance(t, "vm000002-b", "10.42.0.4/16", "aa:bb:cc:00:00:03")
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
	)

	report, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	res, err := repairMissingLeases(f.ipamDir, report, false)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(res.Added) != 0 {
		t.Errorf("expected 0 additions on MAC collision, got %+v", res.Added)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("expected 1 skipped, got %+v", res.Skipped)
	}
}

func TestRepairNoop(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
	)

	report, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	res, err := repairMissingLeases(f.ipamDir, report, false)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(res.Added) != 0 || len(res.Skipped) != 0 {
		t.Errorf("expected no-op, got added=%+v skipped=%+v", res.Added, res.Skipped)
	}
	if _, err := os.Stat(filepath.Join(f.ipamDir, "leases.json.bak")); !os.IsNotExist(err) {
		t.Errorf("no-op should not create backup; stat err=%v", err)
	}
}
