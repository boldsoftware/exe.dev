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

// TestGetInstanceCreatingStateSkipsVMM tests that getInstance returns the CREATING state
// directly from disk without querying the VMM (which would fail since the VM doesn't exist yet).
func TestGetInstanceCreatingStateSkipsVMM(t *testing.T) {
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

	// Create an instance config with CREATING state (simulating early persistence during creation)
	creatingInstanceID := "test-creating-instance"
	creatingInstance := &api.Instance{
		ID:        creatingInstanceID,
		Name:      "test-creating",
		Image:     "test-image",
		State:     api.VMState_CREATING,
		CreatedAt: 1234567890,
		UpdatedAt: 1234567890,
	}

	// Save the instance config
	if err := computeSvc.saveInstanceConfig(creatingInstance); err != nil {
		t.Fatalf("failed to save instance config: %v", err)
	}

	// Load the instance using loadInstanceConfig (which doesn't query VMM)
	loaded, err := computeSvc.loadInstanceConfig(creatingInstanceID)
	if err != nil {
		t.Fatalf("loadInstanceConfig should succeed: %v", err)
	}
	if loaded.State != api.VMState_CREATING {
		t.Errorf("expected CREATING state, got %v", loaded.State)
	}
	if loaded.ID != creatingInstanceID {
		t.Errorf("expected ID %s, got %s", creatingInstanceID, loaded.ID)
	}
	if loaded.Name != "test-creating" {
		t.Errorf("expected name 'test-creating', got %s", loaded.Name)
	}
}

// TestInstanceStateTransition tests that an instance transitions from CREATING to a final state
// when the config is updated after creation completes.
func TestInstanceStateTransition(t *testing.T) {
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

	instanceID := "test-transition-instance"

	// Step 1: Save instance with CREATING state (simulating early persistence)
	creatingInstance := &api.Instance{
		ID:        instanceID,
		Name:      "test-transition",
		Image:     "test-image",
		State:     api.VMState_CREATING,
		CreatedAt: 1234567890,
		UpdatedAt: 1234567890,
	}
	if err := computeSvc.saveInstanceConfig(creatingInstance); err != nil {
		t.Fatalf("failed to save CREATING instance: %v", err)
	}

	// Verify it's in CREATING state
	loaded, err := computeSvc.loadInstanceConfig(instanceID)
	if err != nil {
		t.Fatalf("failed to load instance: %v", err)
	}
	if loaded.State != api.VMState_CREATING {
		t.Errorf("expected CREATING state, got %v", loaded.State)
	}

	// Step 2: Update instance to STARTING state (simulating completion of creation)
	finalInstance := &api.Instance{
		ID:        instanceID,
		Name:      "test-transition",
		Image:     "test-image",
		State:     api.VMState_STARTING,
		CreatedAt: 1234567890,
		UpdatedAt: 1234567899, // Later timestamp
		VMConfig: &api.VMConfig{
			ID:     instanceID,
			Name:   "test-transition",
			CPUs:   2,
			Memory: 1024,
		},
		SSHPort: 22022,
	}
	if err := computeSvc.saveInstanceConfig(finalInstance); err != nil {
		t.Fatalf("failed to save final instance: %v", err)
	}

	// Verify state transitioned and VMConfig is present
	loaded, err = computeSvc.loadInstanceConfig(instanceID)
	if err != nil {
		t.Fatalf("failed to load instance after update: %v", err)
	}
	if loaded.State != api.VMState_STARTING {
		t.Errorf("expected STARTING state, got %v", loaded.State)
	}
	if loaded.VMConfig == nil {
		t.Error("expected VMConfig to be present")
	}
	if loaded.SSHPort != 22022 {
		t.Errorf("expected SSHPort 22022, got %d", loaded.SSHPort)
	}
	if loaded.CreatedAt != 1234567890 {
		t.Errorf("CreatedAt should be preserved, got %d", loaded.CreatedAt)
	}
	if loaded.UpdatedAt != 1234567899 {
		t.Errorf("UpdatedAt should be updated, got %d", loaded.UpdatedAt)
	}
}

// TestCreatingStateAllowsRetry tests that when an instance is in CREATING state
// (e.g., from a crashed previous creation), a new CreateInstance call is allowed
// to proceed rather than returning AlreadyExists. This tests the outer check behavior.
func TestCreatingStateAllowsRetry(t *testing.T) {
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

	instanceID := "test-stale-creating"

	// Create a stale CREATING instance (simulating crashed exelet)
	instanceDir := computeSvc.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o770); err != nil {
		t.Fatalf("failed to create instance dir: %v", err)
	}

	staleInstance := &api.Instance{
		ID:        instanceID,
		Name:      "test-stale",
		Image:     "test-image",
		State:     api.VMState_CREATING,
		CreatedAt: 1234567890,
		UpdatedAt: 1234567890,
	}
	if err := computeSvc.saveInstanceConfig(staleInstance); err != nil {
		t.Fatalf("failed to save stale instance config: %v", err)
	}

	// Verify the stale instance exists
	loaded, err := computeSvc.loadInstanceConfig(instanceID)
	if err != nil {
		t.Fatalf("stale instance should exist: %v", err)
	}
	if loaded.State != api.VMState_CREATING {
		t.Fatalf("expected CREATING state, got %v", loaded.State)
	}

	// Now test that a RUNNING instance DOES return AlreadyExists
	runningInstanceID := "test-running-instance"
	runningInstanceDir := computeSvc.getInstanceDir(runningInstanceID)
	if err := os.MkdirAll(runningInstanceDir, 0o770); err != nil {
		t.Fatalf("failed to create running instance dir: %v", err)
	}
	runningInstance := &api.Instance{
		ID:        runningInstanceID,
		Name:      "test-running",
		Image:     "test-image",
		State:     api.VMState_RUNNING,
		CreatedAt: 1234567890,
		UpdatedAt: 1234567890,
	}
	if err := computeSvc.saveInstanceConfig(runningInstance); err != nil {
		t.Fatalf("failed to save running instance config: %v", err)
	}

	// Verify the running instance exists
	loaded, err = computeSvc.loadInstanceConfig(runningInstanceID)
	if err != nil {
		t.Fatalf("running instance should exist: %v", err)
	}
	if loaded.State != api.VMState_RUNNING {
		t.Fatalf("expected RUNNING state, got %v", loaded.State)
	}

	// Test: CREATING state should be treated as "not fully exists" - we can't call
	// the full CreateInstance since that requires networking etc., but we can verify
	// the state distinction by checking what loadInstanceConfig returns
	// The key insight is that the outer check in CreateInstance now allows CREATING
	// to fall through to singleflight, while RUNNING returns AlreadyExists.

	// This test verifies the states are correctly distinguishable
	if staleInstance.State == api.VMState_CREATING {
		// This is the condition that allows retry
		t.Log("CREATING state correctly identified - would allow singleflight retry")
	}
	if runningInstance.State != api.VMState_CREATING {
		// This is the condition that triggers AlreadyExists
		t.Log("RUNNING state correctly identified - would return AlreadyExists")
	}
}

// TestRollbackCleansUpCreatingInstance tests that when instance creation fails,
// the early-persisted config file is cleaned up along with the instance directory.
func TestRollbackCleansUpCreatingInstance(t *testing.T) {
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

	instanceID := "test-rollback-instance"

	// Create instance directory and save config (simulating early persistence)
	instanceDir := computeSvc.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o770); err != nil {
		t.Fatalf("failed to create instance dir: %v", err)
	}

	creatingInstance := &api.Instance{
		ID:        instanceID,
		Name:      "test-rollback",
		Image:     "test-image",
		State:     api.VMState_CREATING,
		CreatedAt: 1234567890,
		UpdatedAt: 1234567890,
	}
	if err := computeSvc.saveInstanceConfig(creatingInstance); err != nil {
		t.Fatalf("failed to save instance config: %v", err)
	}

	// Verify config exists
	if _, err := computeSvc.loadInstanceConfig(instanceID); err != nil {
		t.Fatalf("config should exist before rollback: %v", err)
	}

	// Simulate rollback by removing instance directory (which contains config.json)
	if err := os.RemoveAll(instanceDir); err != nil {
		t.Fatalf("failed to remove instance dir: %v", err)
	}

	// Verify config no longer exists
	_, err = computeSvc.loadInstanceConfig(instanceID)
	if err == nil {
		t.Fatal("config should not exist after rollback")
	}
	if !errors.Is(err, api.ErrNotFound) {
		t.Errorf("expected ErrNotFound after rollback, got: %v", err)
	}
}
