package cloudhypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// processMetadata is the JSON structure persisted to disk for tracking cloud-hypervisor and virtiofsd processes
type processMetadata struct {
	PID       int       `json:"pid"`
	Name      string    `json:"name"` // "cloud-hypervisor" or "virtiofsd-<tag>"
	StartedAt time.Time `json:"started_at"`
}

// saveProcessMetadata persists process metadata to disk
func (v *VMM) saveProcessMetadata(id string, pid int, name string) error {
	metadata := processMetadata{
		PID:       pid,
		Name:      name,
		StartedAt: time.Now(),
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal process metadata: %w", err)
	}

	metadataPath := v.processMetadataPath(id, name)
	if err := os.WriteFile(metadataPath, data, 0o644); err != nil {
		return fmt.Errorf("failed to write process metadata: %w", err)
	}

	return nil
}

// loadProcessMetadata loads process metadata from disk
func (v *VMM) loadProcessMetadata(id, name string) (*processMetadata, error) {
	metadataPath := v.processMetadataPath(id, name)

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read process metadata: %w", err)
	}

	var metadata processMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal process metadata: %w", err)
	}

	return &metadata, nil
}

// removeProcessMetadata removes process metadata from disk
func (v *VMM) removeProcessMetadata(id, name string) error {
	metadataPath := v.processMetadataPath(id, name)
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove process metadata: %w", err)
	}
	return nil
}

// processMetadataPath returns the path to the process metadata file
func (v *VMM) processMetadataPath(id, name string) string {
	return filepath.Join(v.getDataPath(id), fmt.Sprintf("process-%s.json", name))
}

// isProcessRunning checks if a process is still alive
func isProcessRunning(pid int) bool {
	if pid == 0 {
		return false
	}

	// Send signal 0 to check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// killProcess attempts to kill a process gracefully, then forcefully
func killProcess(pid int) error {
	if pid == 0 {
		return nil
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", pid, err)
	}

	// Try SIGTERM first
	if err := process.Signal(syscall.SIGTERM); err != nil {
		// Process might already be dead
		if err.Error() == "os: process already finished" {
			return nil
		}
		// Try SIGKILL
		if killErr := process.Signal(syscall.SIGKILL); killErr != nil {
			return fmt.Errorf("failed to kill process %d: %w", pid, killErr)
		}
	}

	// Wait for the process to be reaped to prevent zombie processes
	// Use a goroutine with timeout to avoid blocking forever
	done := make(chan error, 1)
	go func() {
		_, waitErr := process.Wait()
		done <- waitErr
	}()

	// Wait up to 5 seconds for the process to exit
	select {
	case waitErr := <-done:
		if waitErr != nil {
			// Process might have already been reaped by init
			return nil
		}
		return nil
	case <-time.After(5 * time.Second):
		// Timeout - process didn't exit, but we tried
		return nil
	}
}

// cleanupOrphanedProcesses kills any processes for this instance and removes their metadata
func (v *VMM) cleanupOrphanedProcesses(ctx context.Context, id string) error {
	dataPath := v.getDataPath(id)

	// Find all process metadata files
	entries, err := os.ReadDir(dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read data directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Check if this is a process metadata file
		name := entry.Name()
		if len(name) < 13 || name[:8] != "process-" || name[len(name)-5:] != ".json" {
			continue
		}

		// Extract process name from filename (process-<name>.json)
		processName := name[8 : len(name)-5]

		// Load metadata
		metadata, err := v.loadProcessMetadata(id, processName)
		if err != nil {
			v.log.WarnContext(ctx, "failed to load process metadata for cleanup", "id", id, "process", processName, "error", err)
			continue
		}

		if metadata == nil {
			continue
		}

		// Check if process is still running
		if isProcessRunning(metadata.PID) {
			v.log.InfoContext(ctx, "killing orphaned process", "id", id, "process", processName, "pid", metadata.PID)
			if err := killProcess(metadata.PID); err != nil {
				v.log.WarnContext(ctx, "failed to kill orphaned process", "id", id, "process", processName, "pid", metadata.PID, "error", err)
			}
		}

		// Remove metadata file
		if err := v.removeProcessMetadata(id, processName); err != nil {
			v.log.WarnContext(ctx, "failed to remove orphaned process metadata", "id", id, "process", processName, "error", err)
		}
	}

	return nil
}

// RecoverProcesses adopts any still-running processes and cleans up stale metadata on startup
// This is called when the exelet starts to recover state from a previous exelet restart
func (v *VMM) RecoverProcesses(ctx context.Context) error {
	// Find all instance directories
	entries, err := os.ReadDir(v.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read VMM data directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		instanceID := entry.Name()

		// Find all process metadata files for this instance
		instancePath := v.getDataPath(instanceID)
		instanceEntries, err := os.ReadDir(instancePath)
		if err != nil {
			v.log.WarnContext(ctx, "failed to read instance directory during recovery", "id", instanceID, "error", err)
			continue
		}

		for _, instanceEntry := range instanceEntries {
			if instanceEntry.IsDir() {
				continue
			}

			// Check if this is a process metadata file
			name := instanceEntry.Name()
			if len(name) < 13 || name[:8] != "process-" || name[len(name)-5:] != ".json" {
				continue
			}

			// Extract process name from filename (process-<name>.json)
			processName := name[8 : len(name)-5]

			// Load metadata
			metadata, err := v.loadProcessMetadata(instanceID, processName)
			if err != nil {
				v.log.WarnContext(ctx, "failed to load process metadata during recovery", "id", instanceID, "process", processName, "error", err)
				continue
			}

			if metadata == nil {
				continue
			}

			// Check if process is still running
			if isProcessRunning(metadata.PID) {
				// Process is still running - adopt it
				v.log.InfoContext(ctx, "adopted running process", "id", instanceID, "process", processName, "pid", metadata.PID)
			} else {
				// Process is not running - clean up stale metadata
				v.log.InfoContext(ctx, "cleaning up stale process metadata", "id", instanceID, "process", processName, "pid", metadata.PID)
				if err := v.removeProcessMetadata(instanceID, processName); err != nil {
					v.log.WarnContext(ctx, "failed to remove stale process metadata", "id", instanceID, "process", processName, "error", err)
				}
			}
		}
	}

	return nil
}
