package xshelley

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGetShelley(t *testing.T) {
	ctx := context.Background()

	// Clean up any existing cache to ensure fresh download
	cacheDirPath, err := getCacheDir()
	if err != nil {
		t.Fatalf("failed to get cache dir: %v", err)
	}
	if err := os.RemoveAll(cacheDirPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to clean cache: %v", err)
	}

	// First call - should download (always use Linux binary)
	path, cached, err := GetShelley(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatalf("GetShelley failed: %v", err)
	}
	if cached {
		t.Error("expected first call to not be cached")
	}

	// Verify the path exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shelley binary not found at %s: %v", path, err)
	}

	// Verify the file is in the expected cache location
	if !strings.HasPrefix(path, cacheDirPath+string(filepath.Separator)) {
		t.Errorf("Expected path to be in cache dir %s, got %s", cacheDirPath, path)
	}

	// Second call - should use cache
	path2, cached2, err := GetShelley(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatalf("GetShelley (cached) failed: %v", err)
	}
	if !cached2 {
		t.Error("expected second call to be cached")
	}

	if path != path2 {
		t.Errorf("Expected same path from cache, got %s vs %s", path, path2)
	}
}

func TestGetShelleyMultipleArchitectures(t *testing.T) {
	ctx := context.Background()

	// Clean cache
	cacheDirPath, err := getCacheDir()
	if err != nil {
		t.Fatalf("failed to get cache dir: %v", err)
	}
	if err := os.RemoveAll(cacheDirPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to clean cache: %v", err)
	}

	// Test amd64
	pathAmd64, _, err := GetShelley(ctx, "amd64")
	if err != nil {
		t.Fatalf("GetShelley(amd64) failed: %v", err)
	}

	if _, err := os.Stat(pathAmd64); err != nil {
		t.Fatalf("amd64 binary not found: %v", err)
	}

	// Test arm64
	pathArm64, _, err := GetShelley(ctx, "arm64")
	if err != nil {
		t.Fatalf("GetShelley(arm64) failed: %v", err)
	}

	if _, err := os.Stat(pathArm64); err != nil {
		t.Fatalf("arm64 binary not found: %v", err)
	}

	// Paths should be different
	if pathAmd64 == pathArm64 {
		t.Errorf("Expected different paths for different architectures")
	}

	// Both should be in cache
	if !strings.HasPrefix(pathAmd64, cacheDirPath+string(filepath.Separator)) {
		t.Errorf("amd64 path not in cache dir")
	}
	if !strings.HasPrefix(pathArm64, cacheDirPath+string(filepath.Separator)) {
		t.Errorf("arm64 path not in cache dir")
	}
}

func TestCacheRefresh(t *testing.T) {
	ctx := context.Background()

	// Clean cache
	cacheDirPath, err := getCacheDir()
	if err != nil {
		t.Fatalf("failed to get cache dir: %v", err)
	}
	if err := os.RemoveAll(cacheDirPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to clean cache: %v", err)
	}

	// First download
	path, _, err := GetShelley(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatalf("GetShelley failed: %v", err)
	}

	// Get the metadata file
	platformDir := filepath.Dir(path)
	metadataPath := filepath.Join(platformDir, metadataFileName)

	// Verify metadata was created
	metaData, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}

	if len(metaData) == 0 {
		t.Fatal("metadata file is empty")
	}

	// Manually set old timestamp by modifying the file
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(metadataPath, oldTime, oldTime); err != nil {
		t.Logf("Warning: failed to change metadata time: %v", err)
	}

	// Call again - should check for updates but likely use cache if tag matches
	path2, _, err := GetShelley(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatalf("GetShelley (refresh check) failed: %v", err)
	}

	if path != path2 {
		t.Errorf("Expected same path after refresh check")
	}

	// Verify binary still exists and is executable
	if _, err := os.Stat(path2); err != nil {
		t.Fatalf("binary disappeared after refresh: %v", err)
	}
}

func TestCacheInvalidation(t *testing.T) {
	ctx := context.Background()

	// Clean cache
	cacheDirPath, err := getCacheDir()
	if err != nil {
		t.Fatalf("failed to get cache dir: %v", err)
	}
	if err := os.RemoveAll(cacheDirPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to clean cache: %v", err)
	}

	// First download
	path, _, err := GetShelley(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatalf("GetShelley failed: %v", err)
	}

	// Delete the binary but keep metadata
	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to remove binary: %v", err)
	}

	// Call again - should re-download since binary is missing
	path2, _, err := GetShelley(ctx, runtime.GOARCH)
	if err != nil {
		t.Fatalf("GetShelley (after deletion) failed: %v", err)
	}

	if path != path2 {
		t.Errorf("Expected same path after re-download")
	}

	// Verify binary was re-downloaded
	if _, err := os.Stat(path2); err != nil {
		t.Fatalf("binary not re-downloaded: %v", err)
	}
}

func TestInvalidMetadata(t *testing.T) {
	ctx := context.Background()

	// Clean cache
	cacheDirPath, err := getCacheDir()
	if err != nil {
		t.Fatalf("failed to get cache dir: %v", err)
	}
	if err := os.RemoveAll(cacheDirPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("failed to clean cache: %v", err)
	}

	// Create cache dir with invalid metadata
	platformDir := filepath.Join(cacheDirPath, "linux-amd64")
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatalf("failed to create platform dir: %v", err)
	}

	metadataPath := filepath.Join(platformDir, metadataFileName)
	if err := os.WriteFile(metadataPath, []byte("invalid json"), 0o644); err != nil {
		t.Fatalf("failed to write invalid metadata: %v", err)
	}

	// Should handle invalid metadata gracefully and re-download
	path, _, err := GetShelley(ctx, "amd64")
	if err != nil {
		t.Fatalf("GetShelley with invalid metadata failed: %v", err)
	}

	// Verify binary exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("binary not found after invalid metadata: %v", err)
	}

	// Verify metadata was fixed
	metaData, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("failed to read fixed metadata: %v", err)
	}

	if string(metaData) == "invalid json" {
		t.Error("metadata was not fixed")
	}
}
