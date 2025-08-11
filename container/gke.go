package container

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/option"
	
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

// GKEManager implements the Manager interface using Google Kubernetes Engine
type GKEManager struct {
	config    *Config
	k8sClient kubernetes.Interface
	k8sConfig *rest.Config
}

// NewGKEManager creates a new GKE-based container manager
func NewGKEManager(ctx context.Context, config *Config, opts ...option.ClientOption) (*GKEManager, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Create Kubernetes client configuration
	// First try in-cluster config (for when running in GKE)
	k8sConfig, err := rest.InClusterConfig()
	if err != nil {
		// We're running outside the cluster, use kubeconfig
		// This uses the default kubeconfig location (~/.kube/config)
		// and the credentials from `gcloud container clusters get-credentials`
		k8sConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load kubeconfig (run 'gcloud container clusters get-credentials %s --location %s --project %s'): %w", 
				config.ClusterName, config.ClusterLocation, config.ProjectID, err)
		}
	}

	k8sClient, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return &GKEManager{
		config:    config,
		k8sClient: k8sClient,
		k8sConfig: k8sConfig,
	}, nil
}

// CreateContainer creates a new container instance
func (m *GKEManager) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*Container, error) {
	// Generate unique IDs
	containerID := generateContainerID(req.UserID, req.Name)
	namespace := m.getUserNamespace(req.UserID)
	
	// Set defaults
	image := req.Image
	if image == "" {
		image = "ubuntu:22.04"
	}
	
	// Map common images to Google's mirror for better performance
	image = m.getMirrorImage(image)
	
	// Use provided resource settings or defaults
	cpuRequest := req.CPURequest
	if cpuRequest == "" {
		cpuRequest = m.config.DefaultCPURequest
	}
	
	memoryRequest := req.MemoryRequest
	if memoryRequest == "" {
		memoryRequest = m.config.DefaultMemoryRequest
	}
	
	storageSize := req.StorageSize
	if storageSize == "" {
		storageSize = m.config.DefaultStorageSize
	}

	// Create container object
	container := &Container{
		ID:            containerID,
		UserID:        req.UserID,
		Name:          req.Name,
		TeamName:      req.TeamName,
		Image:         image,
		Status:        StatusPending,
		CreatedAt:     time.Now(),
		Namespace:     namespace,
		PodName:       containerID, // Pod name same as container ID
		PVCName:       containerID + "-storage",
		CPURequest:    cpuRequest,
		MemoryRequest: memoryRequest,
		StorageSize:   storageSize,
	}

	// If custom Dockerfile is provided, build custom image first
	if req.Dockerfile != "" {
		container.HasCustomImage = true
		container.Status = StatusBuilding
		
		buildReq := &BuildRequest{
			UserID:     req.UserID,
			Dockerfile: req.Dockerfile,
			BuildID:    containerID + "-build",
		}
		
		buildResult, err := m.BuildImage(ctx, buildReq)
		if err != nil {
			return nil, fmt.Errorf("failed to build custom image: %w", err)
		}
		
		container.BuildID = buildResult.BuildID
		// Image will be updated when build completes
	} else {
		// Create Kubernetes resources immediately for pre-built images
		if err := m.createKubernetesResources(ctx, container, req.Ephemeral); err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes resources: %w", err)
		}
	}

	return container, nil
}

// createKubernetesResources creates the namespace, PVC, and pod for a container
func (m *GKEManager) createKubernetesResources(ctx context.Context, container *Container, ephemeral bool) error {
	// Ensure namespace exists
	if err := m.ensureNamespace(ctx, container.Namespace); err != nil {
		return fmt.Errorf("failed to ensure namespace: %w", err)
	}

	// Create PVC only for persistent containers
	if !ephemeral {
		if err := m.createPVC(ctx, container); err != nil {
			return fmt.Errorf("failed to create PVC: %w", err)
		}
	}

	// Create Pod (with either PVC or emptyDir volume)
	if err := m.createPod(ctx, container, ephemeral); err != nil {
		return fmt.Errorf("failed to create pod: %w", err)
	}

	return nil
}

// ensureNamespace creates a namespace if it doesn't exist
func (m *GKEManager) ensureNamespace(ctx context.Context, namespace string) error {
	_, err := m.k8sClient.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil // Namespace already exists
	}

	// Create namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"managed-by": "exe.dev",
			},
		},
	}

	_, err = m.k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

// createPVC creates a persistent volume claim for the container
func (m *GKEManager) createPVC(ctx context.Context, container *Container) error {
	storageQuantity := resource.MustParse(container.StorageSize)
	
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      container.PVCName,
			Namespace: container.Namespace,
			Labels: map[string]string{
				"app":            "exe-container",
				"container-id":   m.shortenForLabel(container.ID),
				"user-id":        m.shortenForLabel(container.UserID),
				"container-name": container.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageQuantity,
				},
			},
			StorageClassName: stringPtr("standard-rwo"), // GKE Autopilot default
		},
	}

	_, err := m.k8sClient.CoreV1().PersistentVolumeClaims(container.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	return err
}

// createPod creates a Kubernetes pod for the container
func (m *GKEManager) createPod(ctx context.Context, container *Container, ephemeral bool) error {
	cpuQuantity := resource.MustParse(container.CPURequest)
	memoryQuantity := resource.MustParse(container.MemoryRequest)

	// For Kubernetes hostname field, use a version without dots (k8s limitation)
	var k8sHostname string
	if container.TeamName != "" {
		k8sHostname = fmt.Sprintf("%s-%s-exe-dev", container.Name, container.TeamName)
	} else {
		k8sHostname = fmt.Sprintf("%s-exe-dev", container.Name)
	}

	labels := map[string]string{
		"app":            "exe-container",
		"container-id":   m.shortenForLabel(container.ID),
		"user-id":        m.shortenForLabel(container.UserID),
		"container-name": container.Name,
		"team-name":      container.TeamName,
	}
	
	// Add ephemeral label if applicable
	if ephemeral {
		labels["ephemeral"] = "true"
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      container.PodName,
			Namespace: container.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Hostname: k8sHostname,
			Containers: []corev1.Container{
				{
					Name:  "main",
					Image: container.Image,
					Command: []string{"/bin/bash"},
					Args:    []string{"-c", "while true; do sleep 30; done"}, // Keep container running
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQuantity,
							corev1.ResourceMemory: memoryQuantity,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQuantity,
							corev1.ResourceMemory: memoryQuantity,
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "storage",
							MountPath: "/workspace",
						},
					},
					WorkingDir: "/workspace",
				},
			},
			RestartPolicy: corev1.RestartPolicyAlways,
		},
	}
	
	// Configure volumes based on ephemeral flag
	if ephemeral {
		// Use emptyDir for ephemeral storage
		storageQuantity := resource.MustParse(container.StorageSize)
		pod.Spec.Volumes = []corev1.Volume{
			{
				Name: "storage",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						SizeLimit: &storageQuantity,
					},
				},
			},
		}
	} else {
		// Use PVC for persistent storage
		pod.Spec.Volumes = []corev1.Volume{
			{
				Name: "storage",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: container.PVCName,
					},
				},
			},
		}
	}

	_, err := m.k8sClient.CoreV1().Pods(container.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

// getUserNamespace returns the Kubernetes namespace for a user
func (m *GKEManager) getUserNamespace(userID string) string {
	// Create a short hash of the userID to stay within Kubernetes 63-character limit
	// NamespacePrefix is "exe-" (4 chars) + hash (16 chars) = 20 chars total, well within limit
	hasher := sha256.New()
	hasher.Write([]byte(userID))
	hash := fmt.Sprintf("%x", hasher.Sum(nil))[:16] // Take first 16 chars of hex
	
	return m.config.NamespacePrefix + hash
}

// generateContainerID creates a unique container ID (shortened for Kubernetes)
func generateContainerID(userID, name string) string {
	// Create a short hash of userID to keep it under Kubernetes limits
	hasher := sha256.New()
	hasher.Write([]byte(userID))
	userHash := fmt.Sprintf("%x", hasher.Sum(nil))[:8] // First 8 chars of hash
	
	sanitized := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	timestamp := time.Now().Unix()
	
	// Format: userhash-name-timestamp (should be under 63 chars)
	return fmt.Sprintf("%s-%s-%d", userHash, sanitized, timestamp)
}

// extractContainerNameFromID extracts the container name from a container ID
// Container IDs have format: {userID}-{name}-{timestamp}
func extractContainerNameFromID(containerID string) string {
	parts := strings.Split(containerID, "-")
	if len(parts) < 3 {
		return containerID // Fallback if format is unexpected
	}
	
	// Remove the userID (first 64 chars typically) and timestamp (last part)
	// Everything in between is the container name
	lastPart := parts[len(parts)-1]
	
	// Find where the timestamp starts (should be all digits)
	isTimestamp := true
	for _, char := range lastPart {
		if char < '0' || char > '9' {
			isTimestamp = false
			break
		}
	}
	
	if !isTimestamp {
		// If last part isn't a timestamp, just return as-is
		return strings.Join(parts[1:], "-")
	}
	
	// Remove userID part and timestamp part
	nameParts := parts[1 : len(parts)-1]
	return strings.Join(nameParts, "-")
}

// shortenForLabel creates a short hash suitable for Kubernetes labels (max 63 chars)
func (m *GKEManager) shortenForLabel(value string) string {
	if len(value) <= 63 {
		return value
	}
	
	// Create a short hash of the value to stay within Kubernetes 63-character limit for labels
	hasher := sha256.New()
	hasher.Write([]byte(value))
	hash := fmt.Sprintf("%x", hasher.Sum(nil))[:16] // Take first 16 chars of hex
	
	// Keep a short prefix for readability if possible
	maxPrefix := 63 - 17 // Reserve 16 chars for hash + 1 for separator
	prefix := value
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	
	return fmt.Sprintf("%s-%s", prefix, hash)
}

// stringPtr returns a pointer to a string
func stringPtr(s string) *string {
	return &s
}

// getMirrorImage maps common Docker Hub images to Google's mirror for better performance
func (m *GKEManager) getMirrorImage(image string) string {
	// Map of common images to their mirror.gcr.io equivalents
	mirrorMap := map[string]string{
		"ubuntu:20.04":     "mirror.gcr.io/library/ubuntu:20.04",
		"ubuntu:22.04":     "mirror.gcr.io/library/ubuntu:22.04",
		"ubuntu:24.04":     "mirror.gcr.io/library/ubuntu:24.04",
		"ubuntu:latest":    "mirror.gcr.io/library/ubuntu:latest",
		"python:3.9":       "mirror.gcr.io/library/python:3.9",
		"python:3.10":      "mirror.gcr.io/library/python:3.10",
		"python:3.11":      "mirror.gcr.io/library/python:3.11",
		"python:3.12":      "mirror.gcr.io/library/python:3.12",
		"python:latest":    "mirror.gcr.io/library/python:latest",
		"python:3.9-slim":  "mirror.gcr.io/library/python:3.9-slim",
		"python:3.10-slim": "mirror.gcr.io/library/python:3.10-slim",
		"python:3.11-slim": "mirror.gcr.io/library/python:3.11-slim",
		"python:3.12-slim": "mirror.gcr.io/library/python:3.12-slim",
		"node:16":          "mirror.gcr.io/library/node:16",
		"node:18":          "mirror.gcr.io/library/node:18",
		"node:20":          "mirror.gcr.io/library/node:20",
		"node:22":          "mirror.gcr.io/library/node:22",
		"node:latest":      "mirror.gcr.io/library/node:latest",
		"node:16-alpine":   "mirror.gcr.io/library/node:16-alpine",
		"node:18-alpine":   "mirror.gcr.io/library/node:18-alpine",
		"node:20-alpine":   "mirror.gcr.io/library/node:20-alpine",
		"nginx:alpine":     "mirror.gcr.io/library/nginx:alpine",
		"nginx:latest":     "mirror.gcr.io/library/nginx:latest",
		"alpine:latest":    "mirror.gcr.io/library/alpine:latest",
		"alpine:3.18":      "mirror.gcr.io/library/alpine:3.18",
		"alpine:3.19":      "mirror.gcr.io/library/alpine:3.19",
		"alpine:3.20":      "mirror.gcr.io/library/alpine:3.20",
		"debian:bullseye":  "mirror.gcr.io/library/debian:bullseye",
		"debian:bookworm":  "mirror.gcr.io/library/debian:bookworm",
		"debian:latest":    "mirror.gcr.io/library/debian:latest",
		"redis:alpine":     "mirror.gcr.io/library/redis:alpine",
		"redis:latest":     "mirror.gcr.io/library/redis:latest",
		"postgres:13":      "mirror.gcr.io/library/postgres:13",
		"postgres:14":      "mirror.gcr.io/library/postgres:14",
		"postgres:15":      "mirror.gcr.io/library/postgres:15",
		"postgres:16":      "mirror.gcr.io/library/postgres:16",
		"postgres:latest":  "mirror.gcr.io/library/postgres:latest",
	}
	
	if mirrorImage, exists := mirrorMap[image]; exists {
		return mirrorImage
	}
	
	// Return original image if no mirror mapping exists
	return image
}

// GetDisplayImageName returns a user-friendly image name for UI display
func GetDisplayImageName(actualImage string) string {
	// Strip mirror.gcr.io prefix for display
	if strings.HasPrefix(actualImage, "mirror.gcr.io/library/") {
		return strings.TrimPrefix(actualImage, "mirror.gcr.io/library/")
	}
	
	return actualImage
}

// GetContainer retrieves a container by ID
func (m *GKEManager) GetContainer(ctx context.Context, userID, containerID string) (*Container, error) {
	namespace := m.getUserNamespace(userID)
	
	// Get pod to check current status
	pod, err := m.k8sClient.CoreV1().Pods(namespace).Get(ctx, containerID, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("container not found: %w", err)
	}

	// Convert pod status to our container status
	status := m.podStatusToContainerStatus(pod.Status.Phase)
	
	// Extract container information from pod labels and spec
	container := &Container{
		ID:        containerID,
		UserID:    userID,
		Name:      pod.Labels["container-name"], // Will need to add this label
		Image:     pod.Spec.Containers[0].Image,
		Status:    status,
		Namespace: namespace,
		PodName:   pod.Name,
		PVCName:   containerID + "-storage",
		CreatedAt: pod.CreationTimestamp.Time,
	}

	if pod.Status.StartTime != nil {
		container.StartedAt = &pod.Status.StartTime.Time
	}

	return container, nil
}

// podStatusToContainerStatus converts Kubernetes pod phase to our container status
func (m *GKEManager) podStatusToContainerStatus(phase corev1.PodPhase) ContainerStatus {
	switch phase {
	case corev1.PodPending:
		return StatusPending
	case corev1.PodRunning:
		return StatusRunning
	case corev1.PodSucceeded, corev1.PodFailed:
		return StatusStopped
	default:
		return StatusUnknown
	}
}

// Close cleans up resources
func (m *GKEManager) Close() error {
	return nil
}

// ListContainers returns all containers for a user
func (m *GKEManager) ListContainers(ctx context.Context, userID string) ([]*Container, error) {
	namespace := m.getUserNamespace(userID)
	
	// List all pods in the user's namespace with our app label
	pods, err := m.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=exe-container",
	})
	if err != nil {
		// If namespace doesn't exist, return empty list instead of error
		if strings.Contains(err.Error(), "not found") {
			return []*Container{}, nil
		}
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	
	var containers []*Container
	for _, pod := range pods.Items {
		// Extract container information from pod
		containerID := pod.Name // Pod name is the container ID
		status := m.podStatusToContainerStatus(pod.Status.Phase)
		
		container := &Container{
			ID:        containerID,
			UserID:    userID,
			Name:      pod.Labels["container-name"], // Get name from label
			Image:     pod.Spec.Containers[0].Image,
			Status:    status,
			Namespace: namespace,
			PodName:   pod.Name,
			PVCName:   containerID + "-storage",
			CreatedAt: pod.CreationTimestamp.Time,
		}
		
		if pod.Status.StartTime != nil {
			container.StartedAt = &pod.Status.StartTime.Time
		}
		
		// Extract resource requests
		if len(pod.Spec.Containers) > 0 {
			resources := pod.Spec.Containers[0].Resources.Requests
			if cpu, ok := resources[corev1.ResourceCPU]; ok {
				container.CPURequest = cpu.String()
			}
			if memory, ok := resources[corev1.ResourceMemory]; ok {
				container.MemoryRequest = memory.String()
			}
		}
		
		containers = append(containers, container)
	}
	
	return containers, nil
}

func (m *GKEManager) StartContainer(ctx context.Context, userID, containerID string) error {
	// TODO: Implement starting a stopped container
	return fmt.Errorf("not implemented yet")
}

func (m *GKEManager) StopContainer(ctx context.Context, userID, containerID string) error {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return fmt.Errorf("failed to get container: %w", err)
	}
	
	if container.Status != StatusRunning {
		return fmt.Errorf("container is not running (status: %s)", container.Status)
	}
	
	// Delete the pod to stop the container
	err = m.k8sClient.CoreV1().Pods(container.Namespace).Delete(ctx, container.PodName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete pod: %w", err)
	}
	
	// Wait for the pod to be fully terminated
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for container to stop")
		case <-time.After(1 * time.Second):
			// Check if pod still exists
			_, err := m.k8sClient.CoreV1().Pods(container.Namespace).Get(ctx, container.PodName, metav1.GetOptions{})
			if err != nil {
				// Pod no longer exists, it's stopped
				if strings.Contains(err.Error(), "not found") {
					return nil
				}
				// Some other error occurred
				return fmt.Errorf("error checking pod status: %w", err)
			}
			// Pod still exists, continue waiting
		}
	}
}

func (m *GKEManager) DeleteContainer(ctx context.Context, userID, containerID string) error {
	// TODO: Implement deleting a container and its resources
	return fmt.Errorf("not implemented yet")
}


func (m *GKEManager) GetContainerLogs(ctx context.Context, userID, containerID string, lines int) ([]string, error) {
	// TODO: Implement getting container logs
	return nil, fmt.Errorf("not implemented yet")
}

// ConnectToContainer establishes a port-forward connection to a container for SSH access
func (m *GKEManager) ConnectToContainer(ctx context.Context, userID, containerID string) (*ContainerConnection, error) {
	// Get the container first to verify it exists and is running
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get container: %w", err)
	}
	
	if container.Status != StatusRunning {
		return nil, fmt.Errorf("container is not running (status: %s)", container.Status)
	}
	
	// For now, return a basic connection that indicates SSH should be done via kubectl exec
	// In a full implementation, we'd set up port-forwarding to port 22 in the container
	// but since our containers don't necessarily have SSH servers, we'll use kubectl exec instead
	conn := &ContainerConnection{
		Container: container,
		LocalPort: 0, // Not using port-forwarding for kubectl exec
		StopFunc:  func() {}, // No cleanup needed for exec
	}
	
	return conn, nil
}

// ExecuteInContainer executes a command inside a running container
func (m *GKEManager) ExecuteInContainer(ctx context.Context, userID, containerID string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	container, err := m.GetContainer(ctx, userID, containerID)
	if err != nil {
		return fmt.Errorf("failed to get container: %w", err)
	}
	
	if container.Status != StatusRunning {
		return fmt.Errorf("container is not running (status: %s)", container.Status)
	}
	
	// Create the exec request
	req := m.k8sClient.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(container.PodName).
		Namespace(container.Namespace).
		SubResource("exec")
	
	option := &corev1.PodExecOptions{
		Container: "main", // Specify which container in the pod to exec into
		Command:   cmd,
		Stdin:     stdin != nil,
		Stdout:    stdout != nil,
		Stderr:    stderr != nil,
		TTY:       true,
	}
	
	req.VersionedParams(
		option,
		scheme.ParameterCodec,
	)
	
	// Create the remote command executor
	exec, err := remotecommand.NewSPDYExecutor(m.k8sConfig, http.MethodPost, req.URL())
	if err != nil {
		return fmt.Errorf("failed to create executor: %w", err)
	}
	
	// Execute the command with streaming
	return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    true,
	})
}

// GetContainerDiagnostics returns diagnostic information for a stuck container
func (m *GKEManager) GetContainerDiagnostics(ctx context.Context, userID, containerName string) (string, error) {
	namespace := m.getUserNamespace(userID)
	
	var diagnostics []string
	diagnostics = append(diagnostics, fmt.Sprintf("=== Diagnostics for container '%s' ===", containerName))
	
	// Get pods with the container-name label
	podList, err := m.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("container-name=%s", containerName),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list pods: %w", err)
	}
	
	if len(podList.Items) == 0 {
		diagnostics = append(diagnostics, "No pods found for this container")
		return strings.Join(diagnostics, "\n"), nil
	}
	
	for _, pod := range podList.Items {
		diagnostics = append(diagnostics, fmt.Sprintf("\nPod: %s", pod.Name))
		diagnostics = append(diagnostics, fmt.Sprintf("Status: %s", pod.Status.Phase))
		
		// Get pod events
		events, err := m.k8sClient.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s", pod.Name),
		})
		if err == nil && len(events.Items) > 0 {
			diagnostics = append(diagnostics, "\nRecent Events:")
			for _, event := range events.Items {
				if event.Type == "Warning" {
					diagnostics = append(diagnostics, fmt.Sprintf("  WARNING: %s - %s", event.Reason, event.Message))
				}
			}
		}
		
		// Check PVC if pod is stuck
		if pod.Status.Phase == "Pending" {
			for _, volume := range pod.Spec.Volumes {
				if volume.PersistentVolumeClaim != nil {
					pvcName := volume.PersistentVolumeClaim.ClaimName
					pvc, err := m.k8sClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
					if err == nil {
						diagnostics = append(diagnostics, fmt.Sprintf("\nPVC: %s", pvcName))
						diagnostics = append(diagnostics, fmt.Sprintf("PVC Status: %s", pvc.Status.Phase))
						
						// Get PVC events
						pvcEvents, err := m.k8sClient.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
							FieldSelector: fmt.Sprintf("involvedObject.name=%s", pvcName),
						})
						if err == nil && len(pvcEvents.Items) > 0 {
							diagnostics = append(diagnostics, "PVC Events:")
							// Only show the most recent warning of each type to avoid spam
							seenReasons := make(map[string]bool)
							// Process events in reverse order to get most recent first
							for i := len(pvcEvents.Items) - 1; i >= 0; i-- {
								event := pvcEvents.Items[i]
								if event.Type == "Warning" && !seenReasons[event.Reason] {
									diagnostics = append(diagnostics, fmt.Sprintf("  WARNING: %s - %s", event.Reason, event.Message))
									seenReasons[event.Reason] = true
								}
							}
						}
					}
				}
			}
		}
	}
	
	return strings.Join(diagnostics, "\n"), nil
}