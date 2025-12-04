package dhcpd

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatastoreReserveRelease(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-dhcpd-test-")
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

	assert.NoError(t, ds.Release(l.IP))

	_, gErr := ds.Get(&Query{MACAddress: l.MACAddress})
	assert.Error(t, gErr)
}

func TestDatastoreLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-dhcpd-test-")
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
	tmpDir, err := os.MkdirTemp("", "exe-dhcpd-test-")
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
	tmpDir, err := os.MkdirTemp("", "exe-dhcpd-test-")
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
	tmpDir, err := os.MkdirTemp("", "exe-dhcpd-test-")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	ds, err := NewDatastore(tmpDir)
	assert.NoError(t, err)

	ip := "127.1.2.3"
	numGoroutines := 10

	var wg sync.WaitGroup
	results := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
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
