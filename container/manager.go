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

	// DataSubdir is the subdirectory under /data for container isolation
	// By having a separate subdir, multiple exed instances can co-exist on
	// the same ctr-host, which is very useful for testing in parallel.
	// We could also have used the nerdctl "namespace" mechanism for this, but we don't.
	DataSubdir string `json:"data_subdir"`

	// IsProduction indicates whether this is a production environment.
	// When true, shelley.json will use "exe.dev" as the gateway.
	// When false, shelley.json will use the actual gateway IP.
	IsProduction bool `json:"is_production"`

	// For IsProduction=false, we need the port to build out URLs for the LLM gateway (and xterm)
	// For production, it's the default SSL port, so we don't use this.
	ExedListeningPort int `json:"exed_listening_port"`
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
