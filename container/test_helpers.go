package container

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestBackend represents the container backend configuration for tests
type TestBackend struct {
	Backend string   // "docker" or "containerd"
	Hosts   []string // List of hosts (empty for local)
}

// GetTestBackend determines which container backend to use based on environment variables
// Priority:
// 1. CTR_HOST env var -> use containerd with specified host
// 2. DOCKER_HOST env var -> use docker with specified host  
// 3. Local ctr available -> use local containerd
// 4. Local docker available -> use local docker
// 5. None available -> skip test
func GetTestBackend(t *testing.T) *TestBackend {
	// Check for CTR_HOST first (highest priority)
	if ctrHost := os.Getenv("CTR_HOST"); ctrHost != "" {
		// Parse CTR_HOST - supports formats:
		// - ssh://user@host or ssh://host -> remote containerd via SSH
		// - user@host or host -> remote containerd via SSH
		// - /path/to/socket -> local containerd with custom socket
		// - local or empty -> local containerd
		
		host := ctrHost
		if strings.HasPrefix(ctrHost, "ssh://") {
			host = strings.TrimPrefix(ctrHost, "ssh://")
		}
		
		t.Logf("Using containerd backend from CTR_HOST: %s", ctrHost)
		
		hosts := []string{}
		if host != "" && host != "local" {
			hosts = []string{host}
		}
		
		return &TestBackend{
			Backend: "containerd",
			Hosts:   hosts,
		}
	}
	
	// Check for DOCKER_HOST next
	if dockerHost := os.Getenv("DOCKER_HOST"); dockerHost != "" {
		t.Logf("Using docker backend from DOCKER_HOST: %s", dockerHost)
		return &TestBackend{
			Backend: "docker",
			Hosts:   []string{dockerHost},
		}
	}
	
	// Check if local ctr is available
	if isCommandAvailable("ctr") {
		// Verify containerd is actually running
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		
		cmd := exec.CommandContext(ctx, "ctr", "version")
		if err := cmd.Run(); err == nil {
			t.Logf("Using local containerd backend")
			return &TestBackend{
				Backend: "containerd",
				Hosts:   []string{},
			}
		}
	}
	
	// Check if local docker is available
	if isCommandAvailable("docker") {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		
		cmd := exec.CommandContext(ctx, "docker", "version")
		if err := cmd.Run(); err == nil {
			t.Logf("Using local docker backend")
			return &TestBackend{
				Backend: "docker",
				Hosts:   []string{},
			}
		}
	}
	
	// Skip if SKIP_CONTAINER_TESTS is set
	if os.Getenv("SKIP_CONTAINER_TESTS") != "" {
		t.Skip("Skipping container tests (SKIP_CONTAINER_TESTS is set)")
	}
	
	// No backend available
	t.Skip("No container backend available (set CTR_HOST or DOCKER_HOST, or install docker/containerd locally)")
	return nil
}

// CreateTestManager creates a Manager instance based on the test backend configuration
func CreateTestManager(t *testing.T, backend *TestBackend) Manager {
	config := &Config{
		Backend:              backend.Backend,
		DockerHosts:          backend.Hosts,
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}
	
	if len(backend.Hosts) == 0 {
		// Local backend
		config.DockerHosts = []string{""}
	}
	
	manager, err := NewManager(config)
	if err != nil {
		t.Fatalf("Failed to create %s manager: %v", backend.Backend, err)
	}
	
	return manager
}

// isCommandAvailable checks if a command is available in PATH
func isCommandAvailable(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

// SkipIfShort skips the test if running in short mode
func SkipIfShort(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
}

// CleanupContainer ensures a container is deleted after a test
func CleanupContainer(t *testing.T, manager Manager, allocID, containerID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	if err := manager.DeleteContainer(ctx, allocID, containerID); err != nil {
		t.Logf("Warning: Failed to delete container %s: %v", containerID, err)
	}
}

// WaitForContainerReady waits for a container to be in running state
func WaitForContainerReady(t *testing.T, manager Manager, allocID, containerID string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	start := time.Now()
	for {
		container, err := manager.GetContainer(ctx, allocID, containerID)
		if err == nil && container.Status == StatusRunning {
			t.Logf("Container %s ready after %v", containerID, time.Since(start))
			return
		}
		
		if time.Since(start) > timeout {
			t.Fatalf("Container %s not ready after %v", containerID, timeout)
		}
		
		time.Sleep(100 * time.Millisecond)
	}
}