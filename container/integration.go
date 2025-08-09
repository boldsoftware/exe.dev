package container

import (
	"context"
	"fmt"
	"os"
)

// NewManagerFromEnv creates a container manager using environment variables
// This is a convenience function for easy integration with the main exe server
func NewManagerFromEnv(ctx context.Context) (Manager, error) {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT environment variable required")
	}

	config := DefaultConfig(projectID)

	// Override defaults with environment variables if set
	if cluster := os.Getenv("EXE_GKE_CLUSTER_NAME"); cluster != "" {
		config.ClusterName = cluster
	}
	if location := os.Getenv("EXE_GKE_LOCATION"); location != "" {
		config.ClusterLocation = location
	}
	if registry := os.Getenv("EXE_CONTAINER_REGISTRY"); registry != "" {
		config.RegistryHost = registry
	}
	if prefix := os.Getenv("EXE_NAMESPACE_PREFIX"); prefix != "" {
		config.NamespacePrefix = prefix
	}

	return NewGKEManager(ctx, config)
}

// ContainerCommand represents a command that can be executed in SSH sessions
type ContainerCommand struct {
	Name        string
	Description string
	Handler     func(userID string, args []string) (string, error)
}

// GetAvailableCommands returns the container management commands
// that can be integrated into the SSH shell
func GetAvailableCommands(manager Manager) []ContainerCommand {
	return []ContainerCommand{
		{
			Name:        "containers",
			Description: "List your containers",
			Handler: func(userID string, args []string) (string, error) {
				ctx := context.Background()
				containers, err := manager.ListContainers(ctx, userID)
				if err != nil {
					return "", fmt.Errorf("failed to list containers: %w", err)
				}

				if len(containers) == 0 {
					return "No containers found. Use 'create-container' to create one.", nil
				}

				result := "Your containers:\n"
				for _, c := range containers {
					status := string(c.Status)
					result += fmt.Sprintf("  %s (%s) - %s\n", c.Name, c.ID, status)
				}
				return result, nil
			},
		},
		{
			Name:        "create-container",
			Description: "Create a new container",
			Handler: func(userID string, args []string) (string, error) {
				name := "default"
				if len(args) > 0 {
					name = args[0]
				}

				req := &CreateContainerRequest{
					UserID: userID,
					Name:   name,
				}

				ctx := context.Background()
				container, err := manager.CreateContainer(ctx, req)
				if err != nil {
					return "", fmt.Errorf("failed to create container: %w", err)
				}

				return fmt.Sprintf("Created container %s (ID: %s)\nStatus: %s", 
					container.Name, container.ID, container.Status), nil
			},
		},
		{
			Name:        "container-status",
			Description: "Get container status",
			Handler: func(userID string, args []string) (string, error) {
				if len(args) == 0 {
					return "", fmt.Errorf("container ID required")
				}

				containerID := args[0]
				ctx := context.Background()
				container, err := manager.GetContainer(ctx, userID, containerID)
				if err != nil {
					return "", fmt.Errorf("failed to get container: %w", err)
				}

				result := fmt.Sprintf("Container: %s\n", container.Name)
				result += fmt.Sprintf("ID: %s\n", container.ID)
				result += fmt.Sprintf("Status: %s\n", container.Status)
				result += fmt.Sprintf("Image: %s\n", container.Image)
				result += fmt.Sprintf("Created: %s\n", container.CreatedAt.Format("2006-01-02 15:04:05"))

				if container.StartedAt != nil {
					result += fmt.Sprintf("Started: %s\n", container.StartedAt.Format("2006-01-02 15:04:05"))
				}

				return result, nil
			},
		},
		{
			Name:        "container-logs",
			Description: "Get container logs",
			Handler: func(userID string, args []string) (string, error) {
				if len(args) == 0 {
					return "", fmt.Errorf("container ID required")
				}

				containerID := args[0]
				lines := 50 // default
				
				ctx := context.Background()
				logs, err := manager.GetContainerLogs(ctx, userID, containerID, lines)
				if err != nil {
					return "", fmt.Errorf("failed to get logs: %w", err)
				}

				if len(logs) == 0 {
					return "No logs available.", nil
				}

				result := fmt.Sprintf("Last %d lines of logs for container %s:\n", len(logs), containerID)
				for _, line := range logs {
					result += line + "\n"
				}
				return result, nil
			},
		},
	}
}