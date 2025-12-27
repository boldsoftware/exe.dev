package storage

import (
	"log/slog"
	"os"
	"testing"

	"exe.dev/exelet/config"
)

// TestStorageServiceSingleflightGroupExists verifies that the singleflight group
// is properly initialized and can be used for image loading coordination.
func TestStorageServiceSingleflightGroupExists(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	cfg := &config.ExeletConfig{
		Name:    "test",
		DataDir: t.TempDir(),
	}

	svc, err := New(cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	storageSvc := svc.(*Service)

	// Verify singleflight group exists and is usable
	result, err, shared := storageSvc.imageLoadGroup.Do("test-key", func() (string, error) {
		return "test-digest", nil
	})
	if err != nil {
		t.Fatalf("imageLoadGroup.Do should work: %v", err)
	}
	if result != "test-digest" {
		t.Errorf("unexpected result: got %v, want test-digest", result)
	}
	if shared {
		t.Error("first call should not be shared")
	}
}

// TestStorageServiceImplementsImageLoader verifies that the storage service
// implements the ImageLoader interface.
func TestStorageServiceImplementsImageLoader(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	cfg := &config.ExeletConfig{
		Name:    "test",
		DataDir: t.TempDir(),
	}

	svc, err := New(cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	storageSvc := svc.(*Service)

	// Verify LoadImage method exists (compile-time check via interface)
	// The actual functionality requires ImageManager and StorageManager,
	// which are set during Register(), so we just verify the method signature here.
	_ = storageSvc.LoadImage
}
