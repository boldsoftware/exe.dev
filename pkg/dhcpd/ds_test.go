package dhcpd

import (
	"os"
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

	assert.NoError(t, ds.Reserve(mac, ip, leaseTTL))

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

	assert.NoError(t, ds.Reserve(mac, ip, leaseTTL))

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
