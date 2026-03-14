package compute

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestCreateSSHProxy tests that CreateProxy correctly creates a SSH proxy for an instance.
func TestCreateSSHProxy(t *testing.T) {
	testCreateSSHProxy(t, "")
}

func TestCreateSSHProxyExepipe(t *testing.T) {
	testCreateSSHProxy(t, exepipe.UnixAddr)
}

func testCreateSSHProxy(t *testing.T, exepipeAddress string) {
	// Skip test if socat is not installed
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:           "test",
		ListenAddress:  "127.0.0.1:0",
		DataDir:        dataDir,
		ProxyPortMin:   20000, // Use different range to avoid conflicts with dev
		ProxyPortMax:   30000,
		ExepipeAddress: exepipeAddress,
	}

	// Create a service instance
	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Start a mock TCP server to simulate the VM's SSH service
	vmIP := "127.0.0.1"
	listener, err := net.Listen("tcp", vmIP+":0")
	if err != nil {
		t.Fatalf("failed to start mock VM SSH server: %v", err)
	}
	defer listener.Close()

	// Accept and close connections (simple mock)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Create instance directory
	instanceID := "test-instance-123"
	// Allocate a dynamic port for the SSH proxy to avoid hardcoded port conflicts.
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate proxy port: %v", err)
	}
	sshPort := proxyLn.Addr().(*net.TCPAddr).Port
	proxyLn.Close()
	instanceDir := computeSvc.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("failed to create instance directory: %v", err)
	}

	// Create SSH proxy using CreateProxy
	if err := computeSvc.proxyManager.CreateProxy(t.Context(), instanceID, vmIP, sshPort, instanceDir); err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	// Verify that a SSH proxy was created
	proxyPort, exists := computeSvc.proxyManager.GetPort(t.Context(), instanceID)
	if !exists {
		t.Fatalf("SSH proxy should exist after CreateProxy")
	}

	if proxyPort != sshPort {
		t.Errorf("proxy port mismatch: expected %d, got %d", sshPort, proxyPort)
	}

	// Test that we can actually connect to the proxy
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", sshPort)
	var conn net.Conn
	var connErr error
	for range 10 {
		conn, connErr = net.DialTimeout("tcp", proxyAddr, 100*time.Millisecond)
		if connErr == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if connErr != nil {
		t.Errorf("failed to connect to SSH proxy at %s: %v", proxyAddr, connErr)
	}

	// Test that calling CreateProxy again is idempotent (stops old, creates new)
	if err := computeSvc.proxyManager.CreateProxy(t.Context(), instanceID, vmIP, sshPort, instanceDir); err != nil {
		t.Errorf("CreateProxy should be idempotent: %v", err)
	}

	// Verify proxy still exists after idempotent call
	if _, exists := computeSvc.proxyManager.GetPort(t.Context(), instanceID); !exists {
		t.Errorf("proxy should still exist after idempotent CreateProxy call")
	}

	// Cleanup
	if _, err := computeSvc.proxyManager.StopProxy(t.Context(), instanceID); err != nil {
		t.Errorf("failed to stop proxy: %v", err)
	}
}

// TestRecoverProxiesStopsProxyForStoppedInstance verifies that RecoverProxies
// stops proxies for instances that are in STOPPED state.
func TestRecoverProxiesStopsProxyForStoppedInstance(t *testing.T) {
	testRecoverProxiesStopsProxyForStoppedInstance(t, "")
}

func TestRecoverProxiesStopsProxyForStoppedInstanceExepipe(t *testing.T) {
	testRecoverProxiesStopsProxyForStoppedInstance(t, exepipe.UnixAddr)
}

func testRecoverProxiesStopsProxyForStoppedInstance(t *testing.T, exepipeAddress string) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:           "test",
		ListenAddress:  "127.0.0.1:0",
		DataDir:        dataDir,
		ProxyPortMin:   20000, // Use different range to avoid conflicts with dev
		ProxyPortMax:   30000,
		ExepipeAddress: exepipeAddress,
	}

	// Create a service instance
	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Create a mock STOPPED instance (no network interface)
	instanceID := "test-instance-stopped"
	sshPort := int32(20023) // Use port in test range (20000-30000)
	instance := &api.Instance{
		ID:      instanceID,
		Name:    "test-instance-stopped",
		Image:   "test-image",
		State:   api.VMState_STOPPED,
		SSHPort: sshPort,
		VMConfig: &api.VMConfig{
			ID:     instanceID,
			Name:   "test-instance-stopped",
			CPUs:   1,
			Memory: 1024,
		},
	}

	// Verify that NO SSH proxy exists initially
	_, exists := computeSvc.proxyManager.GetPort(t.Context(), instanceID)
	if exists {
		t.Errorf("SSH proxy should NOT exist for STOPPED instance initially")
	}

	// Call RecoverProxies with a STOPPED instance - it should not create a proxy
	instances := []*api.Instance{instance}
	if err := computeSvc.proxyManager.RecoverProxies(t.Context(), instances); err != nil {
		t.Errorf("RecoverProxies failed: %v", err)
	}

	// Verify that still NO SSH proxy exists
	_, exists = computeSvc.proxyManager.GetPort(t.Context(), instanceID)
	if exists {
		t.Errorf("SSH proxy should NOT be created for STOPPED instance")
	}
}

// TestRegisterRequiresImageLoader verifies that Register fails with a clear error
// if ImageLoader is not set in ServiceContext.
func TestRegisterRequiresImageLoader(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	cfg := &config.ExeletConfig{
		Name:    "test",
		DataDir: t.TempDir(),
	}

	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Try to register with nil ImageLoader
	ctx := &services.ServiceContext{
		// ImageLoader is nil
	}

	err = computeSvc.Register(ctx, nil)
	if err == nil {
		t.Fatal("Register should fail when ImageLoader is nil")
	}

	expectedMsg := "compute service requires ImageLoader"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("error should mention ImageLoader requirement, got: %v", err)
	}
}
