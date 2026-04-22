package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/pkg/ipam"
)

type fixture struct {
	dataDir string
	ipamDir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		dataDir: filepath.Join(root, "data"),
		ipamDir: filepath.Join(root, "data", "network"),
	}
	if err := os.MkdirAll(filepath.Join(f.dataDir, "instances"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(f.ipamDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return f
}

func (f *fixture) writeInstance(t *testing.T, id, ip, mac string) {
	t.Helper()
	dir := filepath.Join(f.dataDir, "instances", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inst := &api.Instance{
		ID:    id,
		State: api.VMState_RUNNING,
		VMConfig: &api.VMConfig{
			NetworkInterface: &api.NetworkInterface{
				MACAddress: mac,
				IP:         &api.IPAddress{IPV4: ip},
			},
		},
	}
	data, err := inst.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o660); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func (f *fixture) writeBrokenInstance(t *testing.T, id string) {
	t.Helper()
	// config.json as a directory guarantees os.ReadFile returns a non-IsNotExist error.
	if err := os.MkdirAll(filepath.Join(f.dataDir, "instances", id, "config.json"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
}

func (f *fixture) writeLeases(t *testing.T, leases ...ipam.Lease) {
	t.Helper()
	db := &ipam.LeaseDB{
		Hosts: map[string]*ipam.Lease{},
		IPs:   map[string]*ipam.Lease{},
	}
	for i := range leases {
		l := leases[i]
		db.Hosts[l.MACAddress] = &l
		db.IPs[l.IP] = &l
	}
	data, err := json.Marshal(db)
	if err != nil {
		t.Fatalf("marshal leases: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.ipamDir, "leases.json"), data, 0o660); err != nil {
		t.Fatalf("write leases: %v", err)
	}
}

func TestScanCleanHost(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeInstance(t, "vm000002-b", "10.42.0.4/16", "aa:bb:cc:00:00:04")
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
		ipam.Lease{IP: "10.42.0.4", MACAddress: "aa:bb:cc:00:00:04"},
	)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if r.ExitCode() != 0 {
		t.Fatalf("expected exit 0, got %d: %+v", r.ExitCode(), r)
	}
	if r.InstancesReadable != 2 || r.LeasesTotal != 2 {
		t.Errorf("counts off: %+v", r)
	}
}

func TestScanUnreadableConfig(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeBrokenInstance(t, "vm000002-b")
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
	)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if r.InstancesUnreadable != 1 {
		t.Errorf("expected 1 unreadable, got %d", r.InstancesUnreadable)
	}
	if r.ExitCode() != 2 {
		t.Errorf("expected exit 2, got %d", r.ExitCode())
	}
}

func TestScanOrphanLease(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	// Lease for 10.42.0.99 with no matching instance — looks orphaned.
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
		ipam.Lease{IP: "10.42.0.99", MACAddress: "aa:bb:cc:00:00:99"},
	)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(r.OrphanLeases) != 1 || r.OrphanLeases[0].IP != "10.42.0.99" {
		t.Errorf("expected one orphan at 10.42.0.99, got %+v", r.OrphanLeases)
	}
	// 2 leases, 1 orphan → safety bound does NOT trip: both rules require >10 leases.
	if r.SafetyBound.WouldTrip {
		t.Errorf("safety bound should not trip with 2 leases: %+v", r.SafetyBound)
	}
	if r.ExitCode() != 3 {
		t.Errorf("expected exit 3 (unprotected orphan release), got %d", r.ExitCode())
	}
}

func TestScanSafetyBoundTripsOnZeroValidIPs(t *testing.T) {
	f := newFixture(t)
	// No instances at all, but many leases exist. The bound requires
	// >10 leases so a lone orphan after all VMs are gone still reconciles.
	var leases []ipam.Lease
	for i := 10; i < 21; i++ {
		leases = append(leases, ipam.Lease{
			IP:         "10.42.0." + strconv.Itoa(i),
			MACAddress: "aa:bb:cc:00:00:" + strconv.Itoa(i),
		})
	}
	f.writeLeases(t, leases...)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !r.SafetyBound.WouldTrip {
		t.Errorf("safety bound should trip with 0 valid IPs and >10 leases: %+v", r.SafetyBound)
	}
	if r.ExitCode() != 1 {
		t.Errorf("expected exit 1 (safety bound trip, informational), got %d", r.ExitCode())
	}
}

func TestScanSafetyBoundTripsOnMajorityOrphan(t *testing.T) {
	f := newFixture(t)
	// 2 surviving instances, 11 leases — 9 orphans out of 11 (> 50%) and > 10 leases total.
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeInstance(t, "vm000002-b", "10.42.0.4/16", "aa:bb:cc:00:00:04")
	leases := []ipam.Lease{
		{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
		{IP: "10.42.0.4", MACAddress: "aa:bb:cc:00:00:04"},
	}
	for i := 10; i < 19; i++ {
		leases = append(leases, ipam.Lease{
			IP:         "10.42.0." + strconv.Itoa(i),
			MACAddress: "aa:bb:cc:00:00:" + strconv.Itoa(i),
		})
	}
	f.writeLeases(t, leases...)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !r.SafetyBound.WouldTrip {
		t.Errorf("safety bound should trip on >50%% orphan: %+v", r.SafetyBound)
	}
	if r.ExitCode() != 1 {
		t.Errorf("expected exit 1, got %d", r.ExitCode())
	}
}

func TestScanDuplicateIPs(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.20/16", "aa:bb:cc:00:00:20")
	f.writeInstance(t, "vm000002-b", "10.42.0.20/16", "aa:bb:cc:00:00:21")
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.20", MACAddress: "aa:bb:cc:00:00:20"},
	)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(r.DuplicateIPs) != 1 {
		t.Fatalf("expected 1 duplicate IP finding, got %+v", r.DuplicateIPs)
	}
	d := r.DuplicateIPs[0]
	if d.LeaseMAC != "aa:bb:cc:00:00:20" {
		t.Errorf("expected lease MAC on duplicate finding, got %q", d.LeaseMAC)
	}
	// Exactly one claimant should own the lease (the one with the matching MAC).
	owners := 0
	var owner, squatter DuplicateClaimant
	for _, c := range d.Claimants {
		if c.OwnsLease {
			owners++
			owner = c
		} else {
			squatter = c
		}
	}
	if owners != 1 {
		t.Fatalf("expected exactly one owner, got %d (claimants=%+v)", owners, d.Claimants)
	}
	if owner.InstanceID != "vm000001-a" {
		t.Errorf("expected vm000001-a to own the lease, got %s", owner.InstanceID)
	}
	if squatter.InstanceID != "vm000002-b" {
		t.Errorf("expected vm000002-b to be the squatter, got %s", squatter.InstanceID)
	}
	if r.ExitCode() != 4 {
		t.Errorf("expected exit 4 (duplicate IPs), got %d", r.ExitCode())
	}
}

func TestScanDuplicateIPsNoLease(t *testing.T) {
	f := newFixture(t)
	// Two instances claim the same IP and no lease exists at all — both
	// are squatters as far as IPAM is concerned.
	f.writeInstance(t, "vm000001-a", "10.42.0.20/16", "aa:bb:cc:00:00:20")
	f.writeInstance(t, "vm000002-b", "10.42.0.20/16", "aa:bb:cc:00:00:21")
	f.writeLeases(t)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(r.DuplicateIPs) != 1 {
		t.Fatalf("expected 1 duplicate IP finding, got %+v", r.DuplicateIPs)
	}
	d := r.DuplicateIPs[0]
	if d.LeaseMAC != "" {
		t.Errorf("expected empty lease MAC when no lease exists, got %q", d.LeaseMAC)
	}
	for _, c := range d.Claimants {
		if c.OwnsLease {
			t.Errorf("no claimant should own a nonexistent lease, got %+v", c)
		}
	}
}

func TestScanMissingLease(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeInstance(t, "vm000002-b", "10.42.0.4/16", "aa:bb:cc:00:00:04")
	// Only one lease — inst b is missing its lease.
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:00:00:03"},
	)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(r.MissingLeases) != 1 || r.MissingLeases[0].IP != "10.42.0.4" {
		t.Errorf("expected one missing lease at 10.42.0.4, got %+v", r.MissingLeases)
	}
}

func TestScanMACMismatch(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	f.writeLeases(t,
		ipam.Lease{IP: "10.42.0.3", MACAddress: "aa:bb:cc:de:ad:be"},
	)

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(r.MACMismatches) != 1 {
		t.Fatalf("expected 1 mac mismatch, got %+v", r.MACMismatches)
	}
}

func TestScanMissingLeasesFile(t *testing.T) {
	f := newFixture(t)
	f.writeInstance(t, "vm000001-a", "10.42.0.3/16", "aa:bb:cc:00:00:03")
	// Do not write leases.json — fresh host.

	r, err := scan(f.dataDir, f.ipamDir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if r.LeasesTotal != 0 {
		t.Errorf("expected 0 leases on fresh host, got %d", r.LeasesTotal)
	}
	if len(r.MissingLeases) != 1 {
		t.Errorf("expected 1 missing lease (inst has IP, no DB), got %+v", r.MissingLeases)
	}
}
