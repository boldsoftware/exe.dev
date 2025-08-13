package container

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// WarmPoolManager manages pre-warmed container pools for fast startup
type WarmPoolManager struct {
	k8sClient kubernetes.Interface
	config    *Config
	mu        sync.RWMutex
	pools     map[string]*WarmPool // key: size-image combination
}

// WarmPool represents a pool of pre-warmed containers for a specific size/image combo
type WarmPool struct {
	Size           string
	Image          string
	TargetReplicas int32
	StatefulSetName string
	Namespace      string
	CreatedAt      time.Time
}

// WarmPoolConfig defines configuration for warm pools
type WarmPoolConfig struct {
	// Default number of warm pods to maintain per size
	DefaultReplicas map[string]int32
	// Images to pre-warm (in addition to ubuntu)
	PreWarmImages []string
	// Namespace for warm pools
	Namespace string
}

// NewWarmPoolManager creates a new warm pool manager
func NewWarmPoolManager(k8sClient kubernetes.Interface, config *Config) *WarmPoolManager {
	return &WarmPoolManager{
		k8sClient: k8sClient,
		config:    config,
		pools:     make(map[string]*WarmPool),
	}
}

// Initialize sets up the warm pools with default configuration
func (wpm *WarmPoolManager) Initialize(ctx context.Context) error {
	log.Printf("Initializing warm pool manager...")
	
	// Default configuration - start small and expand based on usage patterns
	poolConfig := &WarmPoolConfig{
		DefaultReplicas: map[string]int32{
			"micro":  0, // Don't pre-warm micro
			"small":  2, // Keep 2 small containers warm (most common size)
			"medium": 0, // Don't pre-warm medium
			"large":  0, // Don't pre-warm large (too expensive)
			"xlarge": 0, // Don't pre-warm xlarge (too expensive)
		},
		PreWarmImages: []string{
			"mirror.gcr.io/library/ubuntu:22.04", // Most common image
			"gcr.io/exe-dev-468515/exeuntu",      // Exeuntu development image
		},
		Namespace: "exe-containers", // Use same namespace as user containers
	}

	// Ensure warm pool namespace exists
	if err := wpm.ensureNamespace(ctx, poolConfig.Namespace); err != nil {
		return fmt.Errorf("failed to create warm pool namespace: %w", err)
	}

	// Create headless service for StatefulSets
	if err := wpm.createHeadlessService(ctx, poolConfig.Namespace); err != nil {
		return fmt.Errorf("failed to create headless service: %w", err)
	}

	// Create warm pools for each size/image combination
	for size, replicas := range poolConfig.DefaultReplicas {
		if replicas == 0 {
			continue // Skip sizes with 0 replicas
		}
		
		containerSize, exists := ContainerSizes[size]
		if !exists {
			log.Printf("Warning: unknown container size %s", size)
			continue
		}

		for _, image := range poolConfig.PreWarmImages {
			poolKey := fmt.Sprintf("%s-%s", size, wpm.imageToPoolKey(image))
			
			pool := &WarmPool{
				Size:           size,
				Image:          image,
				TargetReplicas: replicas,
				StatefulSetName: fmt.Sprintf("warm-pool-%s", poolKey),
				Namespace:      poolConfig.Namespace,
				CreatedAt:      time.Now(),
			}

			if err := wpm.createStatefulSet(ctx, pool, containerSize); err != nil {
				// Check if the StatefulSet already exists
				if strings.Contains(err.Error(), "already exists") {
					// StatefulSet exists, we can still use it
					log.Printf("Warm pool %s already exists, will use existing StatefulSet", poolKey)
				} else {
					// Real error, skip this pool
					log.Printf("Warning: failed to create warm pool %s: %v", poolKey, err)
					continue
				}
			} else {
				log.Printf("Created warm pool: %s (replicas: %d)", poolKey, replicas)
			}

			wpm.mu.Lock()
			wpm.pools[poolKey] = pool
			wpm.mu.Unlock()
		}
	}

	log.Printf("Warm pool manager initialized with %d pools", len(wpm.pools))
	return nil
}

// ClaimPod claims a pre-warmed pod from the pool and converts it for user use
func (wpm *WarmPoolManager) ClaimPod(ctx context.Context, req *CreateContainerRequest) (*Container, error) {
	// Determine the image to use
	image := req.Image
	if image == "" {
		image = "mirror.gcr.io/library/ubuntu:22.04"
	}
	
	// Map common images to their mirror versions
	image = wpm.getMirrorImage(image)
	
	// Determine the size
	size := req.Size
	if size == "" {
		size = "small" // default
	}

	poolKey := fmt.Sprintf("%s-%s", size, wpm.imageToPoolKey(image))
	
	wpm.mu.RLock()
	pool, exists := wpm.pools[poolKey]
	wpm.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("no warm pool exists for size %s, image %s", size, image)
	}

	// Find an available pod in the StatefulSet
	pods, err := wpm.k8sClient.CoreV1().Pods(pool.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"app":               "exe-warmpool",
			"warmpool-size":     size,
			"warmpool-image":    wpm.imageToPoolKey(image),
			"claimed":           "false",
		}).String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list warm pool pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no available pods in warm pool %s", poolKey)
	}

	// Claim the first available pod
	pod := &pods.Items[0]
	
	// Mark as claimed by updating labels
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	
	// Generate the container ID once and use consistently
	containerID := generateContainerID(req.UserID, req.Name)
	
	pod.Labels["claimed"] = "true"
	pod.Labels["user-id"] = wpm.shortenForLabel(req.UserID)
	pod.Labels["container-name"] = req.Name
	pod.Labels["container-id"] = wpm.shortenForLabel(containerID)
	if req.TeamName != "" {
		pod.Labels["team-name"] = req.TeamName
	}

	// Update the pod
	_, err = wpm.k8sClient.CoreV1().Pods(pool.Namespace).Update(ctx, pod, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to claim pod: %w", err)
	}

	// Create container object
	container := &Container{
		ID:            containerID,
		UserID:        req.UserID,
		Name:          req.Name,
		TeamName:      req.TeamName,
		Image:         image,
		Status:        StatusRunning, // Pod is already running
		CreatedAt:     time.Now(),
		StartedAt:     &[]time.Time{time.Now()}[0],
		Namespace:     "exe-containers", // All containers in same namespace
		PodName:       pod.Name,
		CPURequest:    ContainerSizes[size].CPURequest,
		MemoryRequest: ContainerSizes[size].MemoryRequest,
		StorageSize:   ContainerSizes[size].StorageSize,
		PVCName:       pod.Name + "-storage", // Warm pool PVC name
	}

	// Scale up the StatefulSet to maintain warm pool size
	go wpm.maintainPoolSize(context.Background(), poolKey)

	log.Printf("Claimed warm pod %s for user %s", pod.Name, req.UserID)
	return container, nil
}

// ReleasePod releases a pod back to the warm pool (or creates a new warm pod)
func (wpm *WarmPoolManager) ReleasePod(ctx context.Context, container *Container) error {
	// For now, just delete the pod - the StatefulSet will recreate it
	err := wpm.k8sClient.CoreV1().Pods(container.Namespace).Delete(ctx, container.PodName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to release pod: %w", err)
	}

	log.Printf("Released pod %s back to warm pool", container.PodName)
	return nil
}

// createStatefulSet creates a StatefulSet for a warm pool
func (wpm *WarmPoolManager) createStatefulSet(ctx context.Context, pool *WarmPool, containerSize ContainerSize) error {
	cpuQuantity := resource.MustParse(containerSize.CPURequest)
	memoryQuantity := resource.MustParse(containerSize.MemoryRequest)
	storageQuantity := resource.MustParse(containerSize.StorageSize)

	// No init containers - each pod only needs its specific image
	// The main container will pull its image when it starts

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pool.StatefulSetName,
			Namespace: pool.Namespace,
			Labels: map[string]string{
				"app":            "exe-warmpool",
				"warmpool-size":  pool.Size,
				"warmpool-image": wpm.imageToPoolKey(pool.Image),
				"managed-by":     "exe.dev",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &pool.TargetReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":            "exe-warmpool",
					"warmpool-size":  pool.Size,
					"warmpool-image": wpm.imageToPoolKey(pool.Image),
				},
			},
			ServiceName: "warmpool-headless", // We'll create this service
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":            "exe-warmpool",
						"warmpool-size":  pool.Size,
						"warmpool-image": wpm.imageToPoolKey(pool.Image),
						"claimed":        "false",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "warm-container",
							Image:   pool.Image,
							Command: []string{"/bin/bash"},
							Args:    []string{"-c", "while true; do sleep 30; done"},
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
									Name:      "warm-storage",
									MountPath: "/workspace",
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "warm-storage",
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
						StorageClassName: stringPtr(wpm.config.StorageClassName),
					},
				},
			},
		},
	}

	// Add sandbox configuration if enabled
	if wpm.config.EnableSandbox {
		runtimeClassName := "gvisor"
		statefulSet.Spec.Template.Spec.RuntimeClassName = &runtimeClassName
		statefulSet.Spec.Template.Spec.NodeSelector = map[string]string{
			"sandbox.gke.io/runtime": "gvisor",
		}
		statefulSet.Spec.Template.Spec.Tolerations = []corev1.Toleration{
			{
				Key:      "sandbox.gke.io/runtime",
				Operator: corev1.TolerationOpEqual,
				Value:    "gvisor",
				Effect:   corev1.TaintEffectNoSchedule,
			},
		}
	}

	_, err := wpm.k8sClient.AppsV1().StatefulSets(pool.Namespace).Create(ctx, statefulSet, metav1.CreateOptions{})
	return err
}


// maintainPoolSize ensures the warm pool maintains its target size
func (wpm *WarmPoolManager) maintainPoolSize(ctx context.Context, poolKey string) {
	wpm.mu.RLock()
	pool, exists := wpm.pools[poolKey]
	wpm.mu.RUnlock()

	if !exists {
		return
	}

	// Get current StatefulSet
	ss, err := wpm.k8sClient.AppsV1().StatefulSets(pool.Namespace).Get(ctx, pool.StatefulSetName, metav1.GetOptions{})
	if err != nil {
		log.Printf("Failed to get StatefulSet %s: %v", pool.StatefulSetName, err)
		return
	}

	// Scale up if needed (StatefulSet controller will handle this automatically)
	if *ss.Spec.Replicas < pool.TargetReplicas {
		ss.Spec.Replicas = &pool.TargetReplicas
		_, err := wpm.k8sClient.AppsV1().StatefulSets(pool.Namespace).Update(ctx, ss, metav1.UpdateOptions{})
		if err != nil {
			log.Printf("Failed to scale StatefulSet %s: %v", pool.StatefulSetName, err)
		} else {
			log.Printf("Scaled StatefulSet %s to %d replicas", pool.StatefulSetName, pool.TargetReplicas)
		}
	}
}

// ensureNamespace creates a namespace if it doesn't exist
func (wpm *WarmPoolManager) ensureNamespace(ctx context.Context, namespace string) error {
	_, err := wpm.k8sClient.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil // Namespace already exists
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"managed-by": "exe.dev",
				"purpose":    "warmpool",
			},
		},
	}

	_, err = wpm.k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

// imageToPoolKey converts an image name to a safe key for pool identification
func (wpm *WarmPoolManager) imageToPoolKey(image string) string {
	// Use SHA256 hash to ensure consistent, unique keys for Kubernetes labels
	h := sha256.Sum256([]byte(image))
	return fmt.Sprintf("%x", h)[:16] // Use first 16 chars of hex hash
}

// shortenForLabel creates a shortened version of a string suitable for Kubernetes labels
func (wpm *WarmPoolManager) shortenForLabel(s string) string {
	if len(s) <= 63 {
		return s
	}
	
	// Create a short hash of the value to stay within Kubernetes 63-character limit for labels
	h := sha256.Sum256([]byte(s))
	hash := fmt.Sprintf("%x", h)[:16] // Take first 16 chars of hex
	
	// Keep a short prefix for readability if possible
	maxPrefix := 63 - 17 // Reserve 16 chars for hash + 1 for separator
	prefix := s
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	
	return fmt.Sprintf("%s-%s", prefix, hash)
}

// getMirrorImage maps common Docker Hub images to Google's mirror for better performance
func (wpm *WarmPoolManager) getMirrorImage(image string) string {
	// Reuse the mirror mapping from GKEManager
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
		"node:16":          "mirror.gcr.io/library/node:16",
		"node:18":          "mirror.gcr.io/library/node:18",
		"node:20":          "mirror.gcr.io/library/node:20",
		"node:22":          "mirror.gcr.io/library/node:22",
		"node:latest":      "mirror.gcr.io/library/node:latest",
		"alpine:latest":    "mirror.gcr.io/library/alpine:latest",
	}
	
	if mirrorImage, exists := mirrorMap[image]; exists {
		return mirrorImage
	}
	
	return image
}

// GetPoolStats returns statistics about warm pools
func (wpm *WarmPoolManager) GetPoolStats(ctx context.Context) (map[string]interface{}, error) {
	wpm.mu.RLock()
	defer wpm.mu.RUnlock()

	stats := make(map[string]interface{})
	
	for poolKey, pool := range wpm.pools {
		// Get pod counts
		pods, err := wpm.k8sClient.CoreV1().Pods(pool.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(map[string]string{
				"app":            "exe-warmpool",
				"warmpool-size":  pool.Size,
				"warmpool-image": wpm.imageToPoolKey(pool.Image),
			}).String(),
		})
		
		var available, claimed int
		if err == nil {
			for _, pod := range pods.Items {
				if pod.Labels["claimed"] == "true" {
					claimed++
				} else {
					available++
				}
			}
		}

		stats[poolKey] = map[string]interface{}{
			"size":        pool.Size,
			"image":       pool.Image,
			"target":      pool.TargetReplicas,
			"available":   available,
			"claimed":     claimed,
			"total":       available + claimed,
			"created_at":  pool.CreatedAt,
		}
	}

	return stats, nil
}

// createHeadlessService creates a headless service for StatefulSets
func (wpm *WarmPoolManager) createHeadlessService(ctx context.Context, namespace string) error {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "warmpool-headless",
			Namespace: namespace,
			Labels: map[string]string{
				"app":        "exe-warmpool",
				"managed-by": "exe.dev",
			},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None", // Headless service
			Selector: map[string]string{
				"app": "exe-warmpool",
			},
		},
	}

	_, err := wpm.k8sClient.CoreV1().Services(namespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("failed to create headless service: %w", err)
	}
	
	return nil
}

// Cleanup removes all warm pools and associated resources
func (wpm *WarmPoolManager) Cleanup(ctx context.Context) error {
	wpm.mu.Lock()
	defer wpm.mu.Unlock()

	for poolKey, pool := range wpm.pools {
		log.Printf("Cleaning up warm pool: %s", poolKey)
		
		// Delete StatefulSet
		err := wpm.k8sClient.AppsV1().StatefulSets(pool.Namespace).Delete(ctx, pool.StatefulSetName, metav1.DeleteOptions{})
		if err != nil {
			log.Printf("Warning: failed to delete StatefulSet %s: %v", pool.StatefulSetName, err)
		}
		
		delete(wpm.pools, poolKey)
	}

	return nil
}