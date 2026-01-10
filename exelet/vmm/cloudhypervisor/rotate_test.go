//go:build linux

package cloudhypervisor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotateBootLog(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	vmm := &VMM{
		dataDir: dataDir,
		log:     log,
	}

	instanceID := "test-instance"
	instanceDir := filepath.Join(dataDir, instanceID)
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("failed to create instance dir: %v", err)
	}

	t.Run("no rotation needed when file is small", func(t *testing.T) {
		bootLogPath := vmm.bootLogPath(instanceID)
		content := []byte("small log content")
		if err := os.WriteFile(bootLogPath, content, 0o644); err != nil {
			t.Fatalf("failed to write boot log: %v", err)
		}

		if err := vmm.RotateBootLog(context.Background(), instanceID, 1024); err != nil {
			t.Fatalf("RotateBootLog failed: %v", err)
		}

		data, err := os.ReadFile(bootLogPath)
		if err != nil {
			t.Fatalf("failed to read boot log: %v", err)
		}
		if string(data) != string(content) {
			t.Errorf("expected content %q, got %q", content, data)
		}
	})

	t.Run("rotation keeps last N bytes", func(t *testing.T) {
		bootLogPath := vmm.bootLogPath(instanceID)

		content := "AAAAAAAAAA" + "BBBBBBBBBB" + "CCCCCCCCCC"
		if err := os.WriteFile(bootLogPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write boot log: %v", err)
		}

		// Rotate keeping last 10 bytes
		if err := vmm.RotateBootLog(context.Background(), instanceID, 10); err != nil {
			t.Fatalf("RotateBootLog failed: %v", err)
		}

		data, err := os.ReadFile(bootLogPath)
		if err != nil {
			t.Fatalf("failed to read boot log: %v", err)
		}
		if string(data) != "CCCCCCCCCC" {
			t.Errorf("expected %q, got %q", "CCCCCCCCCC", string(data))
		}
	})

	t.Run("rotation handles nonexistent file", func(t *testing.T) {
		if err := vmm.RotateBootLog(context.Background(), "nonexistent-instance", 1024); err != nil {
			t.Fatalf("RotateBootLog should not fail for nonexistent file: %v", err)
		}
	})

	t.Run("rotation with O_APPEND writer", func(t *testing.T) {
		bootLogPath := vmm.bootLogPath(instanceID)

		// Create initial content
		initial := strings.Repeat("X", 1000)
		if err := os.WriteFile(bootLogPath, []byte(initial), 0o644); err != nil {
			t.Fatalf("failed to write boot log: %v", err)
		}

		// Open file with O_APPEND (simulating cloud-hypervisor)
		appendFile, err := os.OpenFile(bootLogPath, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatalf("failed to open file for append: %v", err)
		}
		defer appendFile.Close()

		// Write some content
		appendFile.WriteString("BEFORE")

		// Rotate keeping last 100 bytes
		if err := vmm.RotateBootLog(context.Background(), instanceID, 100); err != nil {
			t.Fatalf("RotateBootLog failed: %v", err)
		}

		// Write more content after rotation
		appendFile.WriteString("AFTER")

		// Read the file
		data, err := os.ReadFile(bootLogPath)
		if err != nil {
			t.Fatalf("failed to read boot log: %v", err)
		}

		// With O_APPEND, "AFTER" should appear at the end of the file, not at a sparse offset
		if !strings.HasSuffix(string(data), "AFTER") {
			t.Errorf("expected file to end with AFTER, got %q", string(data))
		}

		// File size should be reasonable (100 bytes kept + "AFTER")
		if len(data) > 200 {
			t.Errorf("file too large after rotation: %d bytes", len(data))
		}
	})
}

func TestStartLogRotation(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	vmm := &VMM{
		dataDir: dataDir,
		log:     log,
	}

	instanceID := "rotation-test-instance"
	instanceDir := filepath.Join(dataDir, instanceID)
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("failed to create instance dir: %v", err)
	}

	bootLogPath := vmm.bootLogPath(instanceID)
	largeContent := strings.Repeat("Z", 500)
	if err := os.WriteFile(bootLogPath, []byte(largeContent), 0o644); err != nil {
		t.Fatalf("failed to write boot log: %v", err)
	}

	ctx := context.Background()
	stop := vmm.StartLogRotation(ctx, 10*time.Millisecond, 100)
	defer stop()

	// Poll until rotation happens or timeout
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(bootLogPath)
		if err != nil {
			t.Fatalf("failed to stat boot log: %v", err)
		}
		if info.Size() <= 100 {
			return // Success
		}
		time.Sleep(10 * time.Millisecond)
	}

	info, _ := os.Stat(bootLogPath)
	t.Fatalf("expected file to be rotated to <= 100 bytes, got %d bytes", info.Size())
}

func TestRotationWithConcurrentAppendWrites(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	vmm := &VMM{
		dataDir: dataDir,
		log:     log,
	}

	instanceID := "concurrent-test"
	instanceDir := filepath.Join(dataDir, instanceID)
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("failed to create instance dir: %v", err)
	}

	bootLogPath := vmm.bootLogPath(instanceID)

	// Create initial content
	if err := os.WriteFile(bootLogPath, []byte(strings.Repeat("I", 1000)), 0o644); err != nil {
		t.Fatalf("failed to write boot log: %v", err)
	}

	// Open with O_APPEND like cloud-hypervisor would
	appendFile, err := os.OpenFile(bootLogPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer appendFile.Close()

	// Write some content, rotate, write more - no sleeps needed
	for i := 0; i < 10; i++ {
		appendFile.WriteString("W")
	}

	// Rotate
	if err := vmm.RotateBootLog(context.Background(), instanceID, 200); err != nil {
		t.Fatalf("first rotation failed: %v", err)
	}

	// Write more after rotation
	for i := 0; i < 10; i++ {
		appendFile.WriteString("X")
	}

	// Rotate again
	if err := vmm.RotateBootLog(context.Background(), instanceID, 200); err != nil {
		t.Fatalf("second rotation failed: %v", err)
	}

	// Write more
	for i := 0; i < 10; i++ {
		appendFile.WriteString("Y")
	}

	// Check final file
	data, err := os.ReadFile(bootLogPath)
	if err != nil {
		t.Fatalf("failed to read boot log: %v", err)
	}

	// File should be bounded and contain writes from after rotation
	if len(data) > 300 {
		t.Errorf("file too large: %d bytes (expected <= 300)", len(data))
	}
	if !strings.Contains(string(data), "Y") {
		t.Error("file should contain content written after rotation")
	}
}
