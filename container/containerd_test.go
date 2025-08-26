package container

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestContainerdManagerCreation(t *testing.T) {
	// Skip Kata check for tests
	oldSkipKata := os.Getenv("SKIP_KATA_CHECK")
	os.Setenv("SKIP_KATA_CHECK", "true")
	defer func() {
		if oldSkipKata == "" {
			os.Unsetenv("SKIP_KATA_CHECK")
		} else {
			os.Setenv("SKIP_KATA_CHECK", oldSkipKata)
		}
	}()

	cfg := &Config{
		Backend:              "containerd",
		DockerHosts:          []string{""},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create containerd manager: %v", err)
	}

	if _, ok := manager.(*NerdctlManager); !ok {
		t.Fatal("Expected NerdctlManager instance")
	}
}

func TestDockerManagerCreation(t *testing.T) {
	cfg := &Config{
		Backend:              "docker",
		DockerHosts:          []string{""},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create docker manager: %v", err)
	}

	if _, ok := manager.(*DockerManager); !ok {
		t.Fatal("Expected DockerManager instance")
	}
}

func TestBackendValidation(t *testing.T) {
	// Skip Kata check for tests
	oldSkipKata := os.Getenv("SKIP_KATA_CHECK")
	os.Setenv("SKIP_KATA_CHECK", "true")
	defer func() {
		if oldSkipKata == "" {
			os.Unsetenv("SKIP_KATA_CHECK")
		} else {
			os.Setenv("SKIP_KATA_CHECK", oldSkipKata)
		}
	}()

	tests := []struct {
		name    string
		backend string
		wantErr bool
	}{
		{"Docker backend", "docker", false},
		{"Containerd backend", "containerd", false},
		{"Invalid backend", "kubernetes", true},
		{"Empty backend defaults to docker", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Backend:     tt.backend,
				DockerHosts: []string{""},
			}

			_, err := NewManager(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewManager() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestContainerdIntegration tests basic containerd operations if containerd is available
func TestContainerdIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skip Kata check for tests
	oldSkipKata := os.Getenv("SKIP_KATA_CHECK")
	os.Setenv("SKIP_KATA_CHECK", "true")
	defer func() {
		if oldSkipKata == "" {
			os.Unsetenv("SKIP_KATA_CHECK")
		} else {
			os.Setenv("SKIP_KATA_CHECK", oldSkipKata)
		}
	}()

	// Check if containerd is available
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := checkContainerdAvailable(ctx); err != nil {
		t.Skipf("Containerd not available: %v", err)
	}

	cfg := &Config{
		Backend:              "containerd",
		DockerHosts:          []string{""},
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
	}

	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("Failed to create containerd manager: %v", err)
	}
	defer manager.Close()

	// Test listing containers (should not fail even if empty)
	containers, err := manager.ListContainers(ctx, "test-alloc")
	if err != nil {
		t.Fatalf("Failed to list containers: %v", err)
	}

	t.Logf("Found %d containers", len(containers))
}

func checkContainerdAvailable(ctx context.Context) error {
	// Try to run ctr version to check if containerd is available
	cmd := exec.CommandContext(ctx, "ctr", "version")
	return cmd.Run()
}