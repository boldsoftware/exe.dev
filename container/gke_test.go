package container

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Integration tests require Google Cloud credentials
// Run with: go test -tags=integration

func TestGKEManagerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Skip unless explicitly requested with RUN_GKE_TESTS=true
	if os.Getenv("RUN_GKE_TESTS") != "true" {
		t.Skip("Skipping GKE integration test. Set RUN_GKE_TESTS=true to run")
	}

	// Check for required environment variables
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		t.Skip("GOOGLE_CLOUD_PROJECT not set, skipping integration test. Run: export GOOGLE_CLOUD_PROJECT=\"your-project-id\"")
	}

	// Using Application Default Credentials
	// If this fails, run: gcloud auth application-default login

	ctx := context.Background()

	// Create test configuration
	config := DefaultConfig(projectID)
	// Use test prefix to isolate from production
	config.NamespacePrefix = "exe-test-"
	
	// Override with environment variables if set
	if cluster := os.Getenv("EXE_GKE_CLUSTER_NAME"); cluster != "" {
		config.ClusterName = cluster
	}
	if location := os.Getenv("EXE_GKE_LOCATION"); location != "" {
		config.ClusterLocation = location
	}

	// Create manager
	manager, err := NewGKEManager(ctx, config)
	if err != nil {
		t.Skipf("Failed to create GKE manager with Application Default Credentials: %v\nRun: gcloud auth application-default login", err)
	}
	defer manager.Close()

	t.Run("CreateUbuntuContainer", func(t *testing.T) {
		testCreateContainer(t, ctx, manager, "")
	})

	t.Run("CreateCustomContainer", func(t *testing.T) {
		dockerfile := `FROM ubuntu:latest
RUN apt-get update && apt-get install -y curl
RUN useradd -m testuser
USER testuser
WORKDIR /home/testuser`
		
		testCreateContainer(t, ctx, manager, dockerfile)
	})
}

func testCreateContainer(t *testing.T, ctx context.Context, manager *GKEManager, dockerfile string) {
	userID := "test-user-" + time.Now().Format("20060102-150405")
	containerName := "test-container"

	req := &CreateContainerRequest{
		UserID:   userID,
		Name:     containerName,
		TeamName: "test-team",
	}

	if dockerfile != "" {
		req.Dockerfile = dockerfile
	}

	// Create container
	container, err := manager.CreateContainer(ctx, req)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	// Verify container properties
	if container.UserID != userID {
		t.Errorf("Expected UserID %s, got %s", userID, container.UserID)
	}

	if container.Name != containerName {
		t.Errorf("Expected Name %s, got %s", containerName, container.Name)
	}

	if dockerfile != "" {
		if !container.HasCustomImage {
			t.Error("Expected HasCustomImage to be true for custom Dockerfile")
		}
		if container.Status != StatusBuilding {
			t.Errorf("Expected status %s for custom build, got %s", StatusBuilding, container.Status)
		}
	} else {
		if container.HasCustomImage {
			t.Error("Expected HasCustomImage to be false for default image")
		}
		if container.Status != StatusPending {
			t.Errorf("Expected status %s for default image, got %s", StatusPending, container.Status)
		}
	}

	// For custom containers, skip retrieval since Kubernetes resources aren't created until build completes
	if dockerfile == "" {
		// Wait a bit for resources to be created
		time.Sleep(10 * time.Second)

		// Try to get the container back
		retrieved, err := manager.GetContainer(ctx, userID, container.ID)
		if err != nil {
			t.Fatalf("Failed to retrieve container: %v", err)
		}

		if retrieved.ID != container.ID {
			t.Errorf("Expected ID %s, got %s", container.ID, retrieved.ID)
		}
	} else {
		t.Log("Skipping retrieval for custom container - Kubernetes resources not created until build completes")
	}

	// Clean up - delete the namespace (which deletes all resources)
	t.Logf("Created container %s in namespace %s", container.ID, container.Namespace)
	
	// Only clean up namespace if Kubernetes resources were actually created
	if dockerfile == "" {
		t.Cleanup(func() {
			err := manager.k8sClient.CoreV1().Namespaces().Delete(context.Background(), container.Namespace, metav1.DeleteOptions{})
			if err != nil {
				t.Logf("Warning: failed to cleanup namespace %s: %v", container.Namespace, err)
			} else {
				t.Logf("Cleaned up namespace %s", container.Namespace)
			}
		})
	} else {
		t.Log("No Kubernetes resources to clean up for building container")
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "missing project ID",
			config:  &Config{},
			wantErr: true,
		},
		{
			name: "missing cluster name",
			config: &Config{
				ProjectID: "test-project",
			},
			wantErr: true,
		},
		{
			name: "missing cluster location",
			config: &Config{
				ProjectID:   "test-project",
				ClusterName: "test-cluster",
			},
			wantErr: true,
		},
		{
			name: "valid config",
			config: &Config{
				ProjectID:       "test-project",
				ClusterName:     "test-cluster",
				ClusterLocation: "us-central1",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	projectID := "test-project"
	config := DefaultConfig(projectID)

	if config.ProjectID != projectID {
		t.Errorf("Expected ProjectID %s, got %s", projectID, config.ProjectID)
	}

	// Verify defaults are set
	if config.Region == "" {
		t.Error("Expected Region to be set")
	}
	if config.ClusterName == "" {
		t.Error("Expected ClusterName to be set")
	}
	if config.DefaultCPURequest == "" {
		t.Error("Expected DefaultCPURequest to be set")
	}
}

func TestGenerateContainerID(t *testing.T) {
	userID := "test-user"
	name := "my container"

	id1 := generateContainerID(userID, name)
	time.Sleep(1 * time.Second) // Ensure different timestamps
	id2 := generateContainerID(userID, name)

	// Should be different (due to timestamp)
	if id1 == id2 {
		t.Error("Expected different container IDs")
	}

	// Should contain sanitized name
	if !strings.Contains(id1, "my-container") {
		t.Error("Expected ID to contain sanitized container name")
	}
}

func TestPodStatusConversion(t *testing.T) {
	manager := &GKEManager{}

	tests := []struct {
		phase    corev1.PodPhase
		expected ContainerStatus
	}{
		{corev1.PodPending, StatusPending},
		{corev1.PodRunning, StatusRunning},
		{corev1.PodSucceeded, StatusStopped},
		{corev1.PodFailed, StatusStopped},
	}

	for _, tt := range tests {
		result := manager.podStatusToContainerStatus(tt.phase)
		if result != tt.expected {
			t.Errorf("podStatusToContainerStatus(%s) = %s, want %s", 
				tt.phase, result, tt.expected)
		}
	}
}