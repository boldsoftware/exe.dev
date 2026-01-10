//go:build linux

package cloudhypervisor

import (
	"context"
	"io"
	"os"
	"syscall"
	"time"

	"exe.dev/exelet/config"
)

// StartLogRotation starts the background log rotation goroutine.
// It returns a function that can be called to stop the rotation.
func (v *VMM) StartLogRotation(ctx context.Context, interval time.Duration, maxBytes int64) func() {
	if interval <= 0 {
		interval = config.DefaultBootLogRotationInterval
	}
	if maxBytes <= 0 {
		maxBytes = config.DefaultBootLogMaxBytes
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				v.rotateAllBootLogs(ctx, maxBytes)
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}

// rotateAllBootLogs iterates through all VM instances and rotates their boot logs.
func (v *VMM) rotateAllBootLogs(ctx context.Context, maxBytes int64) {
	entries, err := os.ReadDir(v.dataDir)
	if err != nil {
		if !os.IsNotExist(err) {
			v.log.WarnContext(ctx, "failed to read VMM data directory for log rotation", "error", err)
		}
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		instanceID := entry.Name()
		if err := v.RotateBootLog(ctx, instanceID, maxBytes); err != nil {
			v.log.WarnContext(ctx, "failed to rotate boot log", "instance", instanceID, "error", err)
		}
	}
}

// RotateBootLog rotates the boot log for the given instance, keeping the last
// maxBytes of content. Since boot.log is opened with O_APPEND, cloud-hypervisor
// will automatically write to the new end of file after truncation.
func (v *VMM) RotateBootLog(ctx context.Context, id string, maxBytes int64) error {
	bootLogPath := v.bootLogPath(id)

	// Check file size
	info, err := os.Stat(bootLogPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	size := info.Size()
	if size <= maxBytes {
		return nil
	}

	// Open file for reading and writing
	f, err := os.OpenFile(bootLogPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	// Acquire exclusive lock to prevent concurrent rotation
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Another rotation is in progress, skip
		return nil
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	// Re-check size after acquiring lock (may have changed)
	info, err = f.Stat()
	if err != nil {
		return err
	}
	size = info.Size()
	if size <= maxBytes {
		return nil
	}

	// Read last maxBytes from the file
	offset := size - maxBytes
	tail := make([]byte, maxBytes)
	n, err := f.ReadAt(tail, offset)
	if err != nil && err != io.EOF {
		return err
	}
	tail = tail[:n]

	// Truncate the file to 0
	if err := f.Truncate(0); err != nil {
		return err
	}

	// Write the tail back - cloud-hypervisor will continue appending
	// after this since boot.log was opened with O_APPEND
	if _, err := f.WriteAt(tail, 0); err != nil {
		return err
	}

	v.log.DebugContext(ctx, "rotated boot log", "instance", id, "old_size", size, "new_size", n)
	return nil
}
