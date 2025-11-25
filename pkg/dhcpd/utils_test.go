package dhcpd

import (
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUtilsGetServerIP(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-dhcpd-test-")
	assert.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv, err := NewDHCPServer(&Config{
		DataDir:   tmpDir,
		Interface: "br-exe",
		Network:   "192.168.64.0/24",
		Port:      67,
	}, log)
	assert.NoError(t, err)

	expectedIP := net.ParseIP("192.168.64.1")

	serverIP, err := srv.getServerIP()
	assert.NoError(t, err)

	assert.Equal(t, expectedIP.String(), serverIP.String())
}

func TestUtilsGetNextIP(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "exe-dhcpd-test-")
	assert.NoError(t, err)

	defer os.RemoveAll(tmpDir)

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv, err := NewDHCPServer(&Config{
		DataDir:   tmpDir,
		Interface: "br-exe",
		Network:   "192.168.64.0/24",
		Port:      67,
	}, log)
	assert.NoError(t, err)

	assert.NoError(t, srv.ds.Reserve("server", "192.168.64.1"))

	nextIP, err := srv.getNextIP()
	assert.NoError(t, err)

	assert.Equal(t, nextIP.String(), "192.168.64.2")
}
