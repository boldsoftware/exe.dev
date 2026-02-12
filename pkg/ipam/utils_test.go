package ipam

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUtilsGetServerIP(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mgr, err := NewManager(&Config{
		DataDir: tmpDir,
		Network: "192.168.64.0/24",
	}, log)
	assert.NoError(t, err)

	expectedIP := net.ParseIP("192.168.64.1")

	serverIP, err := mgr.getServerIP()
	assert.NoError(t, err)

	assert.Equal(t, expectedIP.String(), serverIP.String())
}

func TestUtilsGetNextIP(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mgr, err := NewManager(&Config{
		DataDir: tmpDir,
		Network: "192.168.64.0/24",
	}, log)
	assert.NoError(t, err)

	assert.NoError(t, mgr.ds.Reserve("server", "192.168.64.1"))

	nextIP, err := mgr.getNextIP()
	assert.NoError(t, err)

	assert.Equal(t, nextIP.String(), "192.168.64.2")
}

func TestManagerReserveConcurrent(t *testing.T) {
	// Test that concurrent Reserve() calls each get a unique IP
	tmpDir, err := os.MkdirTemp("", "exe-ipam-test-")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mgr, err := NewManager(&Config{
		DataDir: tmpDir,
		Network: "192.168.64.0/24",
	}, log)
	assert.NoError(t, err)

	numGoroutines := 20

	var wg sync.WaitGroup
	type result struct {
		mac string
		ip  net.IP
		err error
	}
	results := make(chan result, numGoroutines)

	for i := range numGoroutines {
		mac := fmt.Sprintf("00:11:22:33:44:%02x", i)
		wg.Add(1)
		go func(mac string) {
			defer wg.Done()
			ip, err := mgr.Reserve(mac)
			results <- result{mac: mac, ip: ip, err: err}
		}(mac)
	}

	wg.Wait()
	close(results)

	// Collect all results
	ips := make(map[string]string) // IP -> MAC
	for r := range results {
		assert.NoError(t, r.err, "Reserve failed for MAC %s", r.mac)
		if r.err == nil {
			ipStr := r.ip.String()
			if existingMAC, exists := ips[ipStr]; exists {
				t.Errorf("IP %s was assigned to both %s and %s", ipStr, existingMAC, r.mac)
			}
			ips[ipStr] = r.mac
		}
	}

	// Verify all goroutines got unique IPs
	assert.Equal(t, numGoroutines, len(ips), "each goroutine should get a unique IP")
}
