package dhcpd

import (
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
