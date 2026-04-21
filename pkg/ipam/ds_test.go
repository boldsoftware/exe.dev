package ipam

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatastoreReserveRelease(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	ds, err := NewDatastore(tmpDir)
	assert.NoError(t, err)

	mac := "00:11:22:33:44:55"
	ip := "127.1.2.3"

	assert.NoError(t, ds.Reserve(mac, ip))

	l, err := ds.Get(&Query{MACAddress: mac})
	assert.NoError(t, err)

	assert.Equal(t, l.MACAddress, mac)
	assert.Equal(t, l.IP, ip)

	assert.NoError(t, ds.Release(l.MACAddress, l.IP))

	_, gErr := ds.Get(&Query{MACAddress: l.MACAddress})
	assert.Error(t, gErr)
}

// TestDatastoreReleaseMismatchedMAC verifies that Release is a safe no-op when
// another MAC owns the IP. This guards against the bug where rapid VM churn
// causes a stale release call to evict a newer, valid lease, leaving two VMs
// with the same IP.
func TestDatastoreReleaseMismatchedMAC(t *testing.T) {
	ds, err := NewDatastore(t.TempDir())
	assert.NoError(t, err)

	macA := "00:11:22:33:44:01"
	macB := "00:11:22:33:44:02"
	ip := "127.1.2.3"

	assert.NoError(t, ds.Reserve(macA, ip))

	// A release call tagged with a different MAC must not disturb the lease.
	assert.NoError(t, ds.Release(macB, ip))

	l, err := ds.Get(&Query{IP: ip})
	assert.NoError(t, err)
	assert.Equal(t, macA, l.MACAddress)
	assert.Equal(t, ip, l.IP)
}

// TestDatastoreReleaseMismatchedIP verifies that Release is a no-op when the
// caller's recorded IP no longer matches the lease's current IP — e.g. a
// release arrives referencing an IP that was freed and rebound before the
// call landed.
func TestDatastoreReleaseMismatchedIP(t *testing.T) {
	ds, err := NewDatastore(t.TempDir())
	assert.NoError(t, err)

	mac := "00:11:22:33:44:55"
	ip := "127.1.2.3"
	wrongIP := "127.9.9.9"

	assert.NoError(t, ds.Reserve(mac, ip))
	assert.NoError(t, ds.Release(mac, wrongIP))

	l, err := ds.Get(&Query{MACAddress: mac})
	assert.NoError(t, err)
	assert.Equal(t, ip, l.IP)
}

// TestDatastoreReleaseUnknownMAC verifies that releasing a MAC that was never
// registered returns without error.
func TestDatastoreReleaseUnknownMAC(t *testing.T) {
	ds, err := NewDatastore(t.TempDir())
	assert.NoError(t, err)

	assert.NoError(t, ds.Release("00:11:22:33:44:55", "127.1.2.3"))
}

// TestDatastoreRapidDuplicateRegression reproduces the timeline from
// rapid-duplicate.md: under high VM churn, a delayed release for an old VM
// must not evict the lease for a newer VM that has since been allocated the
// same IP. Before the MAC-guarded Release, step 4 below would silently
// remove vmB's lease, allowing step 5 to hand the same IP to a third VM —
// producing the duplicate-IP conflict observed on exelet-dal-prod-02.
func TestDatastoreRapidDuplicateRegression(t *testing.T) {
	ds, err := NewDatastore(t.TempDir())
	assert.NoError(t, err)

	ip := "10.42.2.205"
	macA := "02:8e:fe:18:ef:11" // vmA (the VM being deleted)
	macB := "02:74:44:0e:87:22" // vmB (new VM that reuses the IP)
	macC := "02:11:22:33:44:33" // vmC (would collide if vmB's lease were evicted)

	// 1. vmA holds the IP.
	assert.NoError(t, ds.Reserve(macA, ip))

	// 2. vmA's delete path reads the IP from its config. Simulate that by
	//    capturing (macA, ip) here — the delete path will replay these later.
	staleMAC, staleIP := macA, ip

	// 3. vmA's lease is released (by the first delete or by reconciliation).
	assert.NoError(t, ds.Release(macA, ip))

	// 4. vmB allocates and takes the same IP.
	assert.NoError(t, ds.Reserve(macB, ip))

	// 5. vmA's second/delayed release finally lands. Under the old
	//    IP-keyed Release this evicted vmB's lease; the MAC guard must
	//    make it a safe no-op.
	assert.NoError(t, ds.Release(staleMAC, staleIP))

	// 6. vmB must still own the IP.
	l, err := ds.Get(&Query{IP: ip})
	assert.NoError(t, err)
	assert.Equal(t, macB, l.MACAddress, "vmB's lease must survive the stale release")

	// 7. A third VM attempting to reserve the same IP must be rejected —
	//    proving the IP is still tracked as held by vmB. Before the fix,
	//    the lease table was empty at this point and vmC would succeed,
	//    producing the duplicate-IP conflict.
	err = ds.Reserve(macC, ip)
	assert.ErrorIs(t, err, ErrExists)
}

func TestDatastoreLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	ds, err := NewDatastore(tmpDir)
	assert.NoError(t, err)

	mac := "00:11:22:33:44:55"
	ip := "127.1.2.3"

	assert.NoError(t, ds.Reserve(mac, ip))

	l, err := ds.Get(&Query{MACAddress: mac})
	assert.NoError(t, err)

	assert.Equal(t, l.MACAddress, mac)
	assert.Equal(t, l.IP, ip)

	ds2, err := NewDatastore(tmpDir)
	assert.NoError(t, err)

	l2, err := ds2.Get(&Query{MACAddress: mac})
	assert.NoError(t, err)

	assert.Equal(t, l2.MACAddress, mac)
	assert.Equal(t, l2.IP, ip)
}

func TestDatastoreListConcurrentAccess(t *testing.T) {
	// This test ensures that calling List() concurrently with Reserve() is race-free.
	ds, err := NewDatastore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create datastore: %v", err)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		ds.Reserve("00:11:22:33:44:55", "192.0.2.0")
	})
	wg.Go(func() {
		ds.List()
	})
	wg.Wait()
}

func TestDatastoreReserveExistingIsIdempotent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	ds, err := NewDatastore(tmpDir)
	assert.NoError(t, err)

	mac := "00:11:22:33:44:55"
	ip := "127.1.2.3"

	// Reserve the first time
	assert.NoError(t, ds.Reserve(mac, ip))

	l1, err := ds.Get(&Query{MACAddress: mac})
	assert.NoError(t, err)
	assert.Equal(t, mac, l1.MACAddress)
	assert.Equal(t, ip, l1.IP)

	// Reserve again with the same MAC/IP - should succeed
	// This simulates an existing instance sending another DHCP DISCOVER
	assert.NoError(t, ds.Reserve(mac, ip))

	l2, err := ds.Get(&Query{MACAddress: mac})
	assert.NoError(t, err)
	assert.Equal(t, mac, l2.MACAddress)
	assert.Equal(t, ip, l2.IP)

	// Reservation should remain valid
	assert.Equal(t, l1.IP, l2.IP)
	assert.Equal(t, l1.MACAddress, l2.MACAddress)
}

func TestDatastoreReserveIPCollision(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	ds, err := NewDatastore(tmpDir)
	assert.NoError(t, err)

	mac1 := "00:11:22:33:44:55"
	mac2 := "00:11:22:33:44:66"
	ip := "127.1.2.3"

	// First MAC reserves the IP
	assert.NoError(t, ds.Reserve(mac1, ip))

	// Second MAC tries to reserve the same IP - should fail
	err = ds.Reserve(mac2, ip)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrExists)
	assert.Contains(t, err.Error(), ip)
	assert.Contains(t, err.Error(), mac1)

	// Original reservation should still be intact
	l, err := ds.Get(&Query{IP: ip})
	assert.NoError(t, err)
	assert.Equal(t, mac1, l.MACAddress)
}

func TestDatastoreReserveConcurrentSameIP(t *testing.T) {
	// Test that concurrent reservations of the same IP result in
	// exactly one success and the rest getting ErrExists
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	ds, err := NewDatastore(tmpDir)
	assert.NoError(t, err)

	ip := "127.1.2.3"
	numGoroutines := 10

	var wg sync.WaitGroup
	results := make(chan error, numGoroutines)

	for i := range numGoroutines {
		mac := fmt.Sprintf("00:11:22:33:44:%02x", i)
		wg.Add(1)
		go func(mac string) {
			defer wg.Done()
			results <- ds.Reserve(mac, ip)
		}(mac)
	}

	wg.Wait()
	close(results)

	successCount := 0
	errorCount := 0
	for err := range results {
		if err == nil {
			successCount++
		} else {
			assert.ErrorIs(t, err, ErrExists)
			errorCount++
		}
	}

	// Exactly one goroutine should succeed
	assert.Equal(t, 1, successCount, "exactly one reservation should succeed")
	assert.Equal(t, numGoroutines-1, errorCount, "all other reservations should fail with ErrExists")

	// Verify exactly one lease exists for that IP
	l, err := ds.Get(&Query{IP: ip})
	assert.NoError(t, err)
	assert.Equal(t, ip, l.IP)
}
