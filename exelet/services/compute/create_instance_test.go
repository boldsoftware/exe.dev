package compute

import (
	"errors"
	"log/slog"
	"os"
	"testing"

	"exe.dev/exelet/config"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestLoadInstanceConfigAlreadyExists tests that loadInstanceConfig properly returns
// instance data when an instance config exists on disk, which is the foundation for
// the AlreadyExists check in CreateInstance.
func TestLoadInstanceConfigAlreadyExists(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:          "test",
		ListenAddress: "127.0.0.1:0",
		DataDir:       dataDir,
		ProxyPortMin:  20000,
		ProxyPortMax:  30000,
	}

	svc, err := New(cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Create an existing instance config on disk
	existingInstanceID := "test-existing-instance"
	existingInstance := &api.Instance{
		ID:    existingInstanceID,
		Name:  "test-existing",
		Image: "test-image",
		State: api.VMState_RUNNING,
		VMConfig: &api.VMConfig{
			ID:   existingInstanceID,
			Name: "test-existing",
		},
	}

	// Save the instance config to simulate a pre-existing instance
	if err := computeSvc.saveInstanceConfig(existingInstance); err != nil {
		t.Fatalf("failed to save instance config: %v", err)
	}

	// Test that loadInstanceConfig succeeds for the existing instance
	// This is the underlying check that CreateInstance uses via getInstance
	instance, err := computeSvc.loadInstanceConfig(existingInstanceID)
	if err != nil {
		t.Fatalf("loadInstanceConfig should succeed for existing instance: %v", err)
	}
	if instance.ID != existingInstanceID {
		t.Errorf("expected instance ID %s, got %s", existingInstanceID, instance.ID)
	}

	// Verify that loadInstanceConfig returns ErrNotFound for non-existent instances
	_, err = computeSvc.loadInstanceConfig("non-existent-instance")
	if err == nil {
		t.Fatal("loadInstanceConfig should return error for non-existent instance")
	}
	if !errors.Is(err, api.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// TestCreateInstanceSingleflightGroupExists tests that the instanceCreateGroup
// singleflight group is properly initialized on the service.
func TestCreateInstanceSingleflightGroupExists(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:          "test",
		ListenAddress: "127.0.0.1:0",
		DataDir:       dataDir,
		ProxyPortMin:  20000,
		ProxyPortMax:  30000,
	}

	svc, err := New(cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Verify that the singleflight groups exist and are usable
	// instanceCreateGroup should be zero-valued but functional
	// We test by calling Do with a simple function
	result, err, shared := computeSvc.instanceCreateGroup.Do("test-key", func() (*api.Instance, error) {
		return &api.Instance{ID: "test"}, nil
	})
	if err != nil {
		t.Fatalf("instanceCreateGroup.Do should work: %v", err)
	}
	if result == nil || result.ID != "test" {
		t.Errorf("unexpected result from singleflight: %v", result)
	}
	if shared {
		t.Error("first call should not be shared")
	}
}
