package imageunpack

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnpackPublicImage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	destDir := t.TempDir()

	// Use a small public image for testing
	imageRef := "docker.io/library/alpine:latest"

	cfg := &Config{
		Concurrency: 10,
		ChunkSize:   8 * 1024 * 1024,
		NoSameOwner: true,
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	unpacker := NewUnpacker(cfg, log)

	result, err := unpacker.UnpackWithPlatform(ctx, imageRef, "linux/amd64", destDir)
	if err != nil {
		t.Fatalf("Unpack failed: %v", err)
	}

	// Verify result
	if result.Digest == "" {
		t.Error("expected non-empty digest")
	}
	if !strings.HasPrefix(result.Digest, "sha256:") {
		t.Errorf("expected sha256 digest prefix, got %s", result.Digest)
	}
	if result.LayerCount == 0 {
		t.Error("expected at least one layer")
	}
	if result.UnpackedSize == 0 {
		t.Error("expected non-zero unpacked size")
	}
	if result.Config == nil {
		t.Error("expected non-nil config")
	}

	// Verify some files were extracted
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("failed to read destination directory: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected files to be extracted")
	}

	// Check for common alpine files
	expectedFiles := []string{"bin", "etc", "lib", "usr"}
	for _, expected := range expectedFiles {
		path := filepath.Join(destDir, expected)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected %s to exist", expected)
		}
	}

	t.Logf("Digest: %s", result.Digest)
	t.Logf("Layers: %d", result.LayerCount)
	t.Logf("Compressed: %d bytes", result.CompressedSize)
	t.Logf("Unpacked: %d bytes", result.UnpackedSize)
}

func TestUnpackExeuntu(t *testing.T) {
	t.Skip("downloads large image")
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	destDir := t.TempDir()

	// The specified image from the task
	imageRef := "ghcr.io/boldsoftware/exeuntu:main-c1b77a6"

	cfg := &Config{
		Concurrency: 10,
		ChunkSize:   8 * 1024 * 1024,
		NoSameOwner: true,
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	unpacker := NewUnpacker(cfg, log)

	start := time.Now()
	result, err := unpacker.UnpackWithPlatform(ctx, imageRef, "linux/amd64", destDir)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Unpack failed: %v", err)
	}

	// Verify result
	if result.Digest == "" {
		t.Error("expected non-empty digest")
	}
	if result.LayerCount == 0 {
		t.Error("expected at least one layer")
	}

	// Verify files were extracted
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("failed to read destination directory: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected files to be extracted")
	}

	t.Logf("Digest: %s", result.Digest)
	t.Logf("Layers: %d", result.LayerCount)
	t.Logf("Compressed: %d bytes (%.2f MB)", result.CompressedSize, float64(result.CompressedSize)/(1024*1024))
	t.Logf("Unpacked: %d bytes (%.2f GB)", result.UnpackedSize, float64(result.UnpackedSize)/(1024*1024*1024))
	t.Logf("Time: %s", elapsed)
	if elapsed.Seconds() > 0 {
		speed := float64(result.CompressedSize) / elapsed.Seconds() / (1024 * 1024)
		t.Logf("Download speed: %.2f MB/s", speed)
	}
}

func TestUnpackWithDifferentConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	imageRef := "docker.io/library/alpine:latest"

	concurrencies := []int{1, 5, 10, 20}

	for _, c := range concurrencies {
		t.Run(fmt.Sprintf("concurrency=%d", c), func(t *testing.T) {
			destDir := t.TempDir()

			cfg := &Config{
				Concurrency: c,
				ChunkSize:   4 * 1024 * 1024,
				NoSameOwner: true,
			}

			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			unpacker := NewUnpacker(cfg, log)

			start := time.Now()
			result, err := unpacker.UnpackWithPlatform(ctx, imageRef, "linux/amd64", destDir)
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("Unpack failed with concurrency %d: %v", c, err)
			}

			t.Logf("Concurrency %d: %s (%d bytes)", c, elapsed, result.CompressedSize)
		})
	}
}

func TestConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Concurrency != 10 {
		t.Errorf("expected default concurrency 10, got %d", cfg.Concurrency)
	}
	if cfg.ChunkSize != 8*1024*1024 {
		t.Errorf("expected default chunk size 8MB, got %d", cfg.ChunkSize)
	}
}

func TestPlatform(t *testing.T) {
	platform := Platform()
	if platform == "" {
		t.Error("expected non-empty platform")
	}
	// Should be in format "os/arch"
	if platform != "linux/amd64" && platform != "linux/arm64" && platform != "darwin/amd64" && platform != "darwin/arm64" {
		t.Logf("platform: %s (unusual but might be valid)", platform)
	}
}
