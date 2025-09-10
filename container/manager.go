package container

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Config holds the configuration for the container manager
type Config struct {
	// Containerd addresses - list of CONTAINERD_ADDRESS values for containerd daemons
	// Empty string means local daemon
	ContainerdAddresses []string `json:"containerd_addresses"`

	// Default resource limits
	DefaultCPURequest    string `json:"default_cpu_request"`
	DefaultMemoryRequest string `json:"default_memory_request"`
	DefaultStorageSize   string `json:"default_storage_size"`

	// Optional OCI/Kata annotations to attach to created containers.
	// Keys and values are passed verbatim. Use this to enable Kata/CLH
	// snapshot-restore or other runtime-specific behaviors when supported.
	KataAnnotations map[string]string `json:"kata_annotations"`

	// DataSubdir is the subdirectory under /data for container isolation
	DataSubdir string `json:"data_subdir"`
}

// DefaultConfig returns a sensible default configuration
func DefaultConfig() *Config {
	return &Config{
		ContainerdAddresses:  []string{""}, // Default to local daemon
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "256Mi",
		DefaultStorageSize:   "1Gi",
	}
}

// validateConfig ensures all required fields are present
func validateConfig(cfg *Config) error {
	if len(cfg.ContainerdAddresses) == 0 {
		return fmt.Errorf("at least one containerd address is required")
	}
	return nil
}

// CleanupTestContainers removes containers with names containing substring.
// Designed for cleaning up test containers; best effort only.
// DO NOT USE for prod.
func CleanupTestContainers(ctx context.Context, manager *NerdctlManager, substring string) error {
	containers, err := manager.ListAllContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	for _, container := range containers {
		if !strings.Contains(container.Name, substring) {
			continue
		}
		if err := manager.DeleteContainer(ctx, container.AllocID, container.ID); err != nil {
			slog.Warn("failed to delete container", "name", container.Name, "error", err)
		} else {
			slog.Info("deleted container", "name", container.Name)
		}
	}

	return nil
}
