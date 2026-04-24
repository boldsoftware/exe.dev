package ipam

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestManager(t *testing.T, network string) *Manager {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := NewManager(&Config{
		DataDir: t.TempDir(),
		Network: network,
	}, log)
	require.NoError(t, err)
	return m
}

// TestCursorFreshHostStartsAtFirstHost verifies that a host with no existing
// leases and no persisted cursor hands out the first host address after the
// server IP, matching the previous allocator's behavior.
func TestCursorFreshHostStartsAtFirstHost(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/24")

	ip, err := m.Reserve("00:11:22:33:44:55")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.2", ip.String())
}

// TestCursorDoesNotReuseJustReleasedIP is the headline property: releasing a
// lease must NOT cause the next Reserve to hand out the same IP. This is the
// behavior change that mitigates the rapid-duplicate-IP incident.
func TestCursorDoesNotReuseJustReleasedIP(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/24")

	ip1, err := m.Reserve("00:11:22:33:44:01")
	require.NoError(t, err)

	require.NoError(t, m.Release("00:11:22:33:44:01", ip1.String()))

	ip2, err := m.Reserve("00:11:22:33:44:02")
	require.NoError(t, err)
	assert.NotEqual(t, ip1.String(), ip2.String(),
		"cursor must not hand out a just-released IP on the next Reserve")
}

// TestCursorWrapsAtSubnetEnd verifies that after exhausting the forward range,
// the allocator wraps and consumes previously-released IPs — so a lease CAN
// be reused eventually, just not immediately. Uses a /30 (2 usable addresses)
// so the test runs fast.
func TestCursorWrapsAtSubnetEnd(t *testing.T) {
	// 192.168.64.0/30: network .0, server .1, usable .2, broadcast .3.
	// FirstAddress=.1 (server), LastAddress=.2.
	m := newTestManager(t, "192.168.64.0/30")

	ip1, err := m.Reserve("00:11:22:33:44:01")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.2", ip1.String())

	// Only address .2 exists; releasing and re-reserving should wrap back to it.
	require.NoError(t, m.Release("00:11:22:33:44:01", ip1.String()))

	ip2, err := m.Reserve("00:11:22:33:44:02")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.2", ip2.String(),
		"wrap-around must reuse the only free address")
}

// TestCursorWrapsAndFindsFreedIPAtBeginning verifies the full lifecycle: once
// the allocator has walked through the pool and wrapped the cursor back to
// the beginning, it correctly locates a freed IP sitting at the start of the
// range. This is the property the rolling cursor ultimately trades on — a
// released IP does eventually get reused, just not until the cursor has
// cycled past the rest of the pool.
func TestCursorWrapsAndFindsFreedIPAtBeginning(t *testing.T) {
	// /29: FirstAddress=.1 (server), LastAddress=.6 → .2..6 usable (5 IPs).
	m := newTestManager(t, "192.168.64.0/29")

	// Fill the whole pool. After the last allocation the cursor has
	// advanced past .6 and wrapped to .1 (FirstAddress).
	macs := []string{
		"02:00:00:00:00:02",
		"02:00:00:00:00:03",
		"02:00:00:00:00:04",
		"02:00:00:00:00:05",
		"02:00:00:00:00:06",
	}
	wantIPs := []string{"192.168.64.2", "192.168.64.3", "192.168.64.4", "192.168.64.5", "192.168.64.6"}
	for i, mac := range macs {
		ip, err := m.Reserve(mac)
		require.NoError(t, err)
		assert.Equal(t, wantIPs[i], ip.String())
	}
	// Cursor should have wrapped to the first host address (server IP); the
	// allocator will skip over the server on the next Reserve.
	assert.Equal(t, "192.168.64.1", m.ds.db.NextIP,
		"cursor should wrap to FirstAddress after allocating the last host")

	// Free a mid-pool IP.
	require.NoError(t, m.Release("02:00:00:00:00:04", "192.168.64.4"))

	// Next Reserve must wrap past server (.1), skip held .2 / .3, and return
	// the freed .4 — proving the wrap-around scan correctly locates free
	// slots at the beginning of the pool.
	ip, err := m.Reserve("02:00:00:00:00:99")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.4", ip.String())

	// Cursor now advances past .4 to .5.
	assert.Equal(t, "192.168.64.5", m.ds.db.NextIP)
}

// TestCursorWrapsAndFindsFreedIPNearEnd covers the complementary case: after
// a wrap, if every low-range IP is still held, the scan must keep walking
// and find a freed IP later in the range rather than erroring.
func TestCursorWrapsAndFindsFreedIPNearEnd(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/29")

	macs := []string{
		"02:00:00:00:00:02",
		"02:00:00:00:00:03",
		"02:00:00:00:00:04",
		"02:00:00:00:00:05",
		"02:00:00:00:00:06",
	}
	for _, mac := range macs {
		_, err := m.Reserve(mac)
		require.NoError(t, err)
	}
	require.Equal(t, "192.168.64.1", m.ds.db.NextIP)

	// Free only the highest IP in the range.
	require.NoError(t, m.Release("02:00:00:00:00:06", "192.168.64.6"))

	ip, err := m.Reserve("02:00:00:00:00:99")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.6", ip.String(),
		"wrap-around scan must walk all the way to the last freed IP")

	// Cursor advances past .6 and wraps back to .1 (FirstAddress).
	assert.Equal(t, "192.168.64.1", m.ds.db.NextIP)
}

// TestCursorPoolExhausted verifies that Reserve returns an error when all
// addresses in the subnet are held.
func TestCursorPoolExhausted(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/30")

	_, err := m.Reserve("00:11:22:33:44:01")
	require.NoError(t, err)

	_, err = m.Reserve("00:11:22:33:44:02")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no IPs available")
}

// TestCursorSurvivesReopen verifies that the persisted cursor is loaded from
// disk and resumed: re-opening the datastore must not rewind the cursor and
// cause a just-released IP to be re-handed on the first post-restart Reserve.
func TestCursorSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m1, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/24"}, log)
	require.NoError(t, err)

	ip1, err := m1.Reserve("00:11:22:33:44:01")
	require.NoError(t, err)

	// Release before reopening so the IP is eligible again by the old
	// allocator's rules — the cursor is what should keep it out of reach.
	require.NoError(t, m1.Release("00:11:22:33:44:01", ip1.String()))

	m2, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/24"}, log)
	require.NoError(t, err)

	ip2, err := m2.Reserve("00:11:22:33:44:02")
	require.NoError(t, err)
	assert.NotEqual(t, ip1.String(), ip2.String(),
		"cursor must persist across restart so a just-released IP is not re-handed")
}

// TestCursorSeedMigratesExistingLeases verifies the upgrade path: a
// pre-existing leases.json with populated IPs but no cursor must trigger the
// migration in NewManager, seeding the cursor past the highest existing
// lease. Otherwise the first post-upgrade Reserve would walk from the subnet
// base and hand out whatever IP sits in the lowest gap — exactly the
// "just-released IP" case the cursor is meant to prevent.
func TestCursorSeedMigratesExistingLeases(t *testing.T) {
	dir := t.TempDir()
	// Synthesize a legacy lease DB: three leases with a gap at .4.
	legacy := &LeaseDB{
		Hosts: map[string]*Lease{
			"aa:aa:aa:aa:aa:02": {IP: "192.168.64.2", MACAddress: "aa:aa:aa:aa:aa:02"},
			"aa:aa:aa:aa:aa:03": {IP: "192.168.64.3", MACAddress: "aa:aa:aa:aa:aa:03"},
			"aa:aa:aa:aa:aa:05": {IP: "192.168.64.5", MACAddress: "aa:aa:aa:aa:aa:05"},
		},
		IPs: map[string]*Lease{
			"192.168.64.2": {IP: "192.168.64.2", MACAddress: "aa:aa:aa:aa:aa:02"},
			"192.168.64.3": {IP: "192.168.64.3", MACAddress: "aa:aa:aa:aa:aa:03"},
			"192.168.64.5": {IP: "192.168.64.5", MACAddress: "aa:aa:aa:aa:aa:05"},
		},
	}
	writeLegacyDB(t, dir, legacy)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/24"}, log)
	require.NoError(t, err)

	// The seed must have advanced the cursor past .5. First Reserve should
	// NOT hand out .4 (the tempting gap) — it should land at .6 or later.
	ip, err := m.Reserve("bb:bb:bb:bb:bb:01")
	require.NoError(t, err)
	assert.NotEqual(t, "192.168.64.4", ip.String(),
		"seeded cursor must not hand out the gap that looks like a just-released IP")
	assert.Equal(t, "192.168.64.6", ip.String())
}

// TestCursorSeedWrapsWhenMaxIsLastAddress verifies that if the highest
// existing lease is at the subnet's last host address, the seed wraps the
// cursor back to the first host. The next allocation then walks forward
// from there, skipping over existing leases.
func TestCursorSeedWrapsWhenMaxIsLastAddress(t *testing.T) {
	dir := t.TempDir()
	// /30: FirstAddress=.1 (server), LastAddress=.2. Max existing at .2
	// means the seeded cursor should wrap to .1 (server), and the allocator
	// will skip server on first Reserve.
	legacy := &LeaseDB{
		Hosts: map[string]*Lease{
			"aa:aa:aa:aa:aa:02": {IP: "192.168.64.2", MACAddress: "aa:aa:aa:aa:aa:02"},
		},
		IPs: map[string]*Lease{
			"192.168.64.2": {IP: "192.168.64.2", MACAddress: "aa:aa:aa:aa:aa:02"},
		},
	}
	writeLegacyDB(t, dir, legacy)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/30"}, log)
	require.NoError(t, err)

	// Only .2 exists and it's taken. Next Reserve must fail cleanly.
	_, err = m.Reserve("bb:bb:bb:bb:bb:01")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no IPs available")

	// Releasing .2 should free it; the wrapped cursor finds it.
	require.NoError(t, m.Release("aa:aa:aa:aa:aa:02", "192.168.64.2"))
	ip, err := m.Reserve("bb:bb:bb:bb:bb:01")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.2", ip.String())
}

// TestCursorOutsideSubnetResets verifies that a stale cursor pointing at an
// IP outside the currently-configured subnet (e.g. operator changed the
// CIDR) falls back to the subnet base rather than erroring or looping.
func TestCursorOutsideSubnetResets(t *testing.T) {
	dir := t.TempDir()
	stale := &LeaseDB{
		Hosts:  map[string]*Lease{},
		IPs:    map[string]*Lease{},
		NextIP: "10.200.0.50", // not inside 192.168.64.0/24
	}
	writeLegacyDB(t, dir, stale)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/24"}, log)
	require.NoError(t, err)

	ip, err := m.Reserve("00:11:22:33:44:01")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.2", ip.String(),
		"stale out-of-subnet cursor must reset to the subnet base")
}

// TestCursorUnparseableCursorResets verifies that a persisted cursor string
// that fails to parse as an IP is treated as missing and the allocator falls
// back to the subnet base rather than erroring.
func TestCursorUnparseableCursorResets(t *testing.T) {
	dir := t.TempDir()
	stale := &LeaseDB{
		Hosts:  map[string]*Lease{},
		IPs:    map[string]*Lease{},
		NextIP: "not-an-ip",
	}
	writeLegacyDB(t, dir, stale)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/24"}, log)
	require.NoError(t, err)

	ip, err := m.Reserve("00:11:22:33:44:01")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.2", ip.String(),
		"unparseable cursor must reset to the subnet base")
}

// TestCursorSeedTolleratesUnparseableLeaseIP verifies that a corrupt lease
// entry (unparseable IP in the map) does not abort the seed — the other
// leases should still contribute to the computed cursor.
func TestCursorSeedTolleratesUnparseableLeaseIP(t *testing.T) {
	dir := t.TempDir()
	legacy := &LeaseDB{
		Hosts: map[string]*Lease{
			"aa:aa:aa:aa:aa:07": {IP: "192.168.64.7", MACAddress: "aa:aa:aa:aa:aa:07"},
		},
		IPs: map[string]*Lease{
			"192.168.64.7": {IP: "192.168.64.7", MACAddress: "aa:aa:aa:aa:aa:07"},
			"garbage":      {IP: "garbage", MACAddress: "aa:aa:aa:aa:aa:99"},
		},
	}
	writeLegacyDB(t, dir, legacy)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/24"}, log)
	require.NoError(t, err)

	ip, err := m.Reserve("bb:bb:bb:bb:bb:01")
	require.NoError(t, err)
	assert.Equal(t, "192.168.64.8", ip.String(),
		"seed should still land past .7 even with a corrupt entry in IPs")
}

// TestCursorSeedIgnoresLeasesOutsideSubnet verifies that lease entries
// outside the configured subnet (e.g. after a CIDR change) do not influence
// the seeded cursor — only in-subnet entries matter.
func TestCursorSeedIgnoresLeasesOutsideSubnet(t *testing.T) {
	dir := t.TempDir()
	legacy := &LeaseDB{
		Hosts: map[string]*Lease{
			"aa:aa:aa:aa:aa:01": {IP: "10.10.10.10", MACAddress: "aa:aa:aa:aa:aa:01"},
			"aa:aa:aa:aa:aa:05": {IP: "192.168.64.5", MACAddress: "aa:aa:aa:aa:aa:05"},
		},
		IPs: map[string]*Lease{
			"10.10.10.10":  {IP: "10.10.10.10", MACAddress: "aa:aa:aa:aa:aa:01"},
			"192.168.64.5": {IP: "192.168.64.5", MACAddress: "aa:aa:aa:aa:aa:05"},
		},
	}
	writeLegacyDB(t, dir, legacy)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m, err := NewManager(&Config{DataDir: dir, Network: "192.168.64.0/24"}, log)
	require.NoError(t, err)

	ip, err := m.Reserve("bb:bb:bb:bb:bb:01")
	require.NoError(t, err)
	// Seed picks max in-subnet IP (.5) and advances to .6.
	assert.Equal(t, "192.168.64.6", ip.String())
}

// TestCursorFreshHostNoSeed verifies that a host with no existing leases does
// not have a cursor persisted after startup — the default start behavior
// (cursor starts at subnet first host) applies.
func TestCursorFreshHostNoSeed(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/24")

	// On a fresh host, no seed should have been written.
	assert.Empty(t, m.ds.db.NextIP, "fresh host should not have a seeded cursor")

	// After a Reserve, the cursor advances past the allocation.
	ip, err := m.Reserve("00:11:22:33:44:01")
	require.NoError(t, err)
	require.Equal(t, "192.168.64.2", ip.String())
	assert.Equal(t, "192.168.64.3", m.ds.db.NextIP)
}

// TestCursorAdvancesAcrossMultipleReserves sanity-checks that the cursor
// advances monotonically and each Reserve returns the next sequential IP.
func TestCursorAdvancesAcrossMultipleReserves(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/24")

	wantIPs := []string{"192.168.64.2", "192.168.64.3", "192.168.64.4", "192.168.64.5"}
	for i, want := range wantIPs {
		ip, err := m.Reserve(macFromByte(byte(i + 1)))
		require.NoError(t, err)
		assert.Equal(t, want, ip.String())
	}
}

// TestCursorConcurrentReservesAdvanceMonotonically verifies that under
// concurrent Reserve calls the cursor is not corrupted: all goroutines
// receive distinct IPs from a contiguous range starting at the pre-test
// cursor position, and the final persisted cursor equals
// start + N (where N is the number of Reserves), proving each advancement
// is serialized through the datastore mutex.
func TestCursorConcurrentReservesAdvanceMonotonically(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/24")

	const n = 50

	type result struct {
		mac string
		ip  string
		err error
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mac := fmt.Sprintf("02:00:00:00:%02x:%02x", byte(i>>8), byte(i))
			ip, err := m.Reserve(mac)
			if err != nil {
				results <- result{mac: mac, err: err}
				return
			}
			results <- result{mac: mac, ip: ip.String()}
		}(i)
	}
	wg.Wait()
	close(results)

	seen := map[string]string{}
	for r := range results {
		require.NoError(t, r.err, "Reserve failed for %s", r.mac)
		if prev, dup := seen[r.ip]; dup {
			t.Fatalf("duplicate IP %s handed to %s and %s", r.ip, prev, r.mac)
		}
		seen[r.ip] = r.mac
	}
	require.Len(t, seen, n)

	// The allocated IPs must form the exact contiguous block .2..(n+1) — no
	// gaps, no reuse — which is the property the cursor mutex guards.
	for i := range n {
		want := fmt.Sprintf("192.168.64.%d", i+2)
		if _, ok := seen[want]; !ok {
			t.Errorf("expected %s to have been allocated; gap in cursor advancement", want)
		}
	}

	// Cursor must reflect the next candidate past the final allocation.
	assert.Equal(t, fmt.Sprintf("192.168.64.%d", n+2), m.ds.db.NextIP,
		"cursor should advance by exactly N under concurrent Reserves")
}

// TestCursorConcurrentReservesAndReleases layers concurrent Releases on top
// of concurrent Reserves and verifies: (a) no duplicate IPs are ever issued
// while the churn runs, and (b) the cursor continues to advance past any
// in-flight releases rather than rewinding to reuse them. This is the
// closest analog in-test to the production pattern that triggered the
// incident (rapid create/delete of VMs under the same user).
func TestCursorConcurrentReservesAndReleases(t *testing.T) {
	m := newTestManager(t, "192.168.64.0/24")

	// Pre-allocate a batch so there are leases to release concurrently.
	const preN = 20
	preMACs := make([]string, preN)
	preIPs := make([]string, preN)
	for i := range preN {
		mac := fmt.Sprintf("02:aa:00:00:00:%02x", i)
		ip, err := m.Reserve(mac)
		require.NoError(t, err)
		preMACs[i] = mac
		preIPs[i] = ip.String()
	}
	cursorBefore := m.ds.db.NextIP
	require.Equal(t, "192.168.64.22", cursorBefore)

	// Churn: release the pre-allocated leases while new Reserves run.
	const newN = 30
	var wg sync.WaitGroup
	newResults := make(chan string, newN)
	newErrs := make(chan error, newN)

	for i := range preN {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = m.Release(preMACs[i], preIPs[i])
		}(i)
	}
	for i := range newN {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mac := fmt.Sprintf("02:bb:00:00:00:%02x", i)
			ip, err := m.Reserve(mac)
			if err != nil {
				newErrs <- err
				return
			}
			newResults <- ip.String()
		}(i)
	}
	wg.Wait()
	close(newResults)
	close(newErrs)

	for err := range newErrs {
		t.Fatalf("concurrent Reserve failed: %v", err)
	}

	// Collect allocated-during-churn IPs. They must all be distinct and
	// none of them may overlap with the IPs that were just released — that
	// would mean the cursor rewound under concurrent release.
	releasedSet := make(map[string]struct{}, preN)
	for _, ip := range preIPs {
		releasedSet[ip] = struct{}{}
	}
	newSet := make(map[string]struct{}, newN)
	for ip := range newResults {
		if _, dup := newSet[ip]; dup {
			t.Fatalf("duplicate IP %s handed out during concurrent churn", ip)
		}
		newSet[ip] = struct{}{}
		if _, reused := releasedSet[ip]; reused {
			t.Fatalf("cursor handed out just-released IP %s under concurrent churn", ip)
		}
	}
	require.Len(t, newSet, newN)

	// Cursor advanced by exactly newN regardless of the interleaved releases.
	assert.Equal(t, fmt.Sprintf("192.168.64.%d", 22+newN), m.ds.db.NextIP,
		"cursor must advance past N new Reserves even with concurrent Releases")
}

func macFromByte(b byte) string {
	mac := net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, b}
	return mac.String()
}

func writeLegacyDB(t *testing.T, dir string, db *LeaseDB) {
	t.Helper()
	data, err := json.Marshal(db)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, configName), data, 0o600))
}
