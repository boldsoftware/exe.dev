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
		image = "ubuntu:latest"
	}
	
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
		if err := m.createKubernetesResources(ctx, container); err != nil {
			return nil, fmt.Errorf("failed to create Kubernetes resources: %w", err)
		}
	}

	return container, nil
}

// createKubernetesResources creates the namespace, PVC, and pod for a container
func (m *GKEManager) createKubernetesResources(ctx context.Context, container *Container) error {
	// Ensure namespace exists
	if err := m.ensureNamespace(ctx, container.Namespace); err != nil {
		return fmt.Errorf("failed to ensure namespace: %w", err)
	}

	// Create PVC
	if err := m.createPVC(ctx, container); err != nil {
		return fmt.Errorf("failed to create PVC: %w", err)
	}

	// Create Pod
	if err := m.createPod(ctx, container); err != nil {
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
				"app":         "exe-container",
				"container-id": m.shortenForLabel(container.ID),
				"user-id":     m.shortenForLabel(container.UserID),
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
func (m *GKEManager) createPod(ctx context.Context, container *Container) error {
	cpuQuantity := resource.MustParse(container.CPURequest)
	memoryQuantity := resource.MustParse(container.MemoryRequest)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      container.PodName,
			Namespace: container.Namespace,
			Labels: map[string]string{
				"app":         "exe-container",
				"container-id": m.shortenForLabel(container.ID),
				"user-id":     m.shortenForLabel(container.UserID),
			},
		},
		Spec: corev1.PodSpec{
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
			Volumes: []corev1.Volume{
				{
					Name: "storage",
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: container.PVCName,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyAlways,
		},
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

// generateContainerID creates a unique container ID
func generateContainerID(userID, name string) string {
	// Simple implementation - in production you might want UUIDs
	sanitized := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	timestamp := time.Now().Unix()
	return fmt.Sprintf("%s-%s-%d", userID, sanitized, timestamp)
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

// Placeholder implementations for remaining interface methods
func (m *GKEManager) ListContainers(ctx context.Context, userID string) ([]*Container, error) {
	// TODO: Implement listing containers for a user
	return nil, fmt.Errorf("not implemented yet")
}

func (m *GKEManager) StartContainer(ctx context.Context, userID, containerID string) error {
	// TODO: Implement starting a stopped container
	return fmt.Errorf("not implemented yet")
}

func (m *GKEManager) StopContainer(ctx context.Context, userID, containerID string) error {
	// TODO: Implement stopping a running container
	return fmt.Errorf("not implemented yet")
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
		Command: cmd,
		Stdin:   stdin != nil,
		Stdout:  stdout != nil,
		Stderr:  stderr != nil,
		TTY:     true,
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