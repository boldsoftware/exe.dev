package container

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWarmPoolManager_Initialize(t *testing.T) {
	// Create a fake Kubernetes client
	fakeClient := fake.NewSimpleClientset()
	
	config := &Config{
		ProjectID:         "test-project",
		ClusterName:      "test-cluster",
		ClusterLocation:  "us-west2-a",
		StorageClassName: "standard-rwo",
		EnableSandbox:    true,
	}

	wpm := NewWarmPoolManager(fakeClient, config)

	ctx := context.Background()
	err := wpm.Initialize(ctx)
	if err != nil {
		t.Fatalf("Failed to initialize warm pool manager: %v", err)
	}

	// Check that namespace was created
	_, err = fakeClient.CoreV1().Namespaces().Get(ctx, "exe-warmpool", metav1.GetOptions{})
	if err != nil {
		t.Errorf("Expected warmpool namespace to be created, got error: %v", err)
	}

	// Check that headless service was created
	_, err = fakeClient.CoreV1().Services("exe-warmpool").Get(ctx, "warmpool-headless", metav1.GetOptions{})
	if err != nil {
		t.Errorf("Expected headless service to be created, got error: %v", err)
	}

	// Check that StatefulSets were created
	statefulSets, err := fakeClient.AppsV1().StatefulSets("exe-warmpool").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Failed to list StatefulSets: %v", err)
	}

	if len(statefulSets.Items) == 0 {
		t.Error("Expected at least one StatefulSet to be created")
	}

	// Verify StatefulSet has correct labels and configuration
	for _, ss := range statefulSets.Items {
		if ss.Labels["app"] != "exe-warmpool" {
			t.Errorf("Expected StatefulSet to have app=exe-warmpool label, got: %s", ss.Labels["app"])
		}
		if ss.Spec.ServiceName != "warmpool-headless" {
			t.Errorf("Expected StatefulSet to use warmpool-headless service, got: %s", ss.Spec.ServiceName)
		}
		if *ss.Spec.Replicas == 0 {
			t.Error("Expected StatefulSet to have non-zero replicas")
		}
	}
}

func TestWarmPoolManager_ClaimPod(t *testing.T) {
	// Create a fake Kubernetes client
	fakeClient := fake.NewSimpleClientset()
	
	config := &Config{
		ProjectID:         "test-project",
		ClusterName:      "test-cluster", 
		ClusterLocation:  "us-west2-a",
		StorageClassName: "standard-rwo",
		EnableSandbox:    false, // Disable sandbox for easier testing
	}

	wpm := NewWarmPoolManager(fakeClient, config)

	// Create the warm pool manually for testing
	poolKey := "small-6d6972726f722e67" // This matches the imageToPoolKey output for mirror.gcr.io/library/ubuntu:22.04
	pool := &WarmPool{
		Size:            "small",
		Image:           "mirror.gcr.io/library/ubuntu:22.04",
		TargetReplicas:  1,
		StatefulSetName: "warm-pool-small-test",
		Namespace:       "exe-warmpool",
		CreatedAt:       time.Now(),
	}
	wpm.pools[poolKey] = pool

	// Create a fake warm pod
	poolImageKey := wpm.imageToPoolKey("mirror.gcr.io/library/ubuntu:22.04")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "warm-pool-test-0",
			Namespace: "exe-warmpool",
			Labels: map[string]string{
				"app":            "exe-warmpool",
				"warmpool-size":  "small",
				"warmpool-image": poolImageKey,
				"claimed":        "false",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "warm-container",
					Image: "mirror.gcr.io/library/ubuntu:22.04",
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	_, err := fakeClient.CoreV1().Pods("exe-warmpool").Create(context.Background(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create fake warm pod: %v", err)
	}

	// Try to claim the pod
	req := &CreateContainerRequest{
		UserID:   "test-user",
		Name:     "test-container",
		Size:     "small",
		Image:    "ubuntu:22.04", // This should be mapped to mirror
	}

	container, err := wpm.ClaimPod(context.Background(), req)
	if err != nil {
		t.Fatalf("Failed to claim pod: %v", err)
	}

	// Verify container details
	if container.UserID != req.UserID {
		t.Errorf("Expected UserID %s, got %s", req.UserID, container.UserID)
	}
	if container.Name != req.Name {
		t.Errorf("Expected Name %s, got %s", req.Name, container.Name)
	}
	if container.Status != StatusRunning {
		t.Errorf("Expected Status %s, got %s", StatusRunning, container.Status)
	}
	if container.Namespace != "exe-warmpool" {
		t.Errorf("Expected Namespace exe-warmpool, got %s", container.Namespace)
	}

	// Verify the pod was marked as claimed
	updatedPod, err := fakeClient.CoreV1().Pods("exe-warmpool").Get(context.Background(), pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get updated pod: %v", err)
	}
	if updatedPod.Labels["claimed"] != "true" {
		t.Errorf("Expected pod to be marked as claimed=true, got: %s", updatedPod.Labels["claimed"])
	}
	if updatedPod.Labels["user-id"] != req.UserID {
		t.Errorf("Expected pod to have user-id=%s, got: %s", req.UserID, updatedPod.Labels["user-id"])
	}
}

func TestWarmPoolManager_ClaimPod_NoAvailable(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	
	config := &Config{
		ProjectID:        "test-project",
		ClusterName:     "test-cluster",
		ClusterLocation: "us-west2-a",
		StorageClassName: "standard-rwo",
	}

	wpm := NewWarmPoolManager(fakeClient, config)

	// Create a warm pool but with no pods available
	poolKey := "small-6d6972726f722e67"
	pool := &WarmPool{
		Size:            "small",
		Image:           "mirror.gcr.io/library/ubuntu:22.04",
		TargetReplicas:  1,
		StatefulSetName: "warm-pool-small-test",
		Namespace:       "exe-warmpool",
		CreatedAt:       time.Now(),
	}
	wpm.pools[poolKey] = pool

	req := &CreateContainerRequest{
		UserID: "test-user",
		Name:   "test-container",
		Size:   "small",
		Image:  "ubuntu:22.04",
	}

	// Try to claim a pod when none are available
	_, err := wpm.ClaimPod(context.Background(), req)
	if err == nil {
		t.Error("Expected error when no warm pods are available, but got nil")
	}
	if !strings.Contains(err.Error(), "no available pods") {
		t.Errorf("Expected error message about no available pods, got: %v", err)
	}
}

func TestWarmPoolManager_GetPoolStats(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	
	config := &Config{
		ProjectID:        "test-project",
		ClusterName:     "test-cluster",
		ClusterLocation: "us-west2-a",
		StorageClassName: "standard-rwo",
	}

	wpm := NewWarmPoolManager(fakeClient, config)

	// Create a pool manually for testing
	poolKey := "small-test"
	pool := &WarmPool{
		Size:            "small",
		Image:           "test-image",
		TargetReplicas:  2,
		StatefulSetName: "warm-pool-small-test",
		Namespace:       "exe-warmpool",
		CreatedAt:       time.Now(),
	}
	wpm.pools[poolKey] = pool

	// Create some fake pods
	availablePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-0",
			Namespace: "exe-warmpool",
			Labels: map[string]string{
				"app":            "exe-warmpool",
				"warmpool-size":  "small",
				"warmpool-image": wpm.imageToPoolKey("test-image"),
				"claimed":        "false",
			},
		},
	}

	claimedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "exe-warmpool",
			Labels: map[string]string{
				"app":            "exe-warmpool",
				"warmpool-size":  "small",
				"warmpool-image": wpm.imageToPoolKey("test-image"),
				"claimed":        "true",
			},
		},
	}

	_, err := fakeClient.CoreV1().Pods("exe-warmpool").Create(context.Background(), availablePod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create available pod: %v", err)
	}

	_, err = fakeClient.CoreV1().Pods("exe-warmpool").Create(context.Background(), claimedPod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create claimed pod: %v", err)
	}

	stats, err := wpm.GetPoolStats(context.Background())
	if err != nil {
		t.Fatalf("Failed to get pool stats: %v", err)
	}

	if len(stats) != 1 {
		t.Errorf("Expected 1 pool in stats, got %d", len(stats))
	}

	poolStats, exists := stats[poolKey]
	if !exists {
		t.Error("Expected pool stats to contain the test pool")
	}

	statsMap := poolStats.(map[string]interface{})
	if statsMap["available"] != 1 {
		t.Errorf("Expected 1 available pod, got %v", statsMap["available"])
	}
	if statsMap["claimed"] != 1 {
		t.Errorf("Expected 1 claimed pod, got %v", statsMap["claimed"])
	}
	if statsMap["total"] != 2 {
		t.Errorf("Expected 2 total pods, got %v", statsMap["total"])
	}
}

func TestImageToPoolKey(t *testing.T) {
	wpm := &WarmPoolManager{}
	
	testCases := []struct {
		image    string
		expected string
	}{
		{"ubuntu:22.04", "7562756e74753a32"}, // First 16 chars of hex
		{"python:3.12", "707974686f6e3a33"}, // First 16 chars of hex  
		{"ghcr.io/boldsoftware/sketch", "676863722e696f2f"}, // First 16 chars of hex
	}

	for _, tc := range testCases {
		result := wpm.imageToPoolKey(tc.image)
		if result != tc.expected {
			t.Errorf("imageToPoolKey(%s) = %s, expected %s", tc.image, result, tc.expected)
		}
		if len(result) != 16 {
			t.Errorf("imageToPoolKey(%s) should return 16 chars, got %d", tc.image, len(result))
		}
	}
}

func TestWarmPoolManager_CreateStatefulSet(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	
	config := &Config{
		ProjectID:        "test-project",
		ClusterName:     "test-cluster",
		ClusterLocation: "us-west2-a",
		StorageClassName: "standard-rwo",
		EnableSandbox:    true,
	}

	wpm := NewWarmPoolManager(fakeClient, config)

	pool := &WarmPool{
		Size:            "small",
		Image:           "mirror.gcr.io/library/ubuntu:22.04",
		TargetReplicas:  2,
		StatefulSetName: "warm-pool-test",
		Namespace:       "exe-warmpool",
		CreatedAt:       time.Now(),
	}

	containerSize := ContainerSizes["small"]
	
	err := wpm.createStatefulSet(context.Background(), pool, containerSize)
	if err != nil {
		t.Fatalf("Failed to create StatefulSet: %v", err)
	}

	// Verify StatefulSet was created
	ss, err := fakeClient.AppsV1().StatefulSets(pool.Namespace).Get(context.Background(), pool.StatefulSetName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get created StatefulSet: %v", err)
	}

	// Check basic properties
	if *ss.Spec.Replicas != pool.TargetReplicas {
		t.Errorf("Expected %d replicas, got %d", pool.TargetReplicas, *ss.Spec.Replicas)
	}

	if ss.Spec.ServiceName != "warmpool-headless" {
		t.Errorf("Expected service name warmpool-headless, got %s", ss.Spec.ServiceName)
	}

	// Check labels
	if ss.Labels["app"] != "exe-warmpool" {
		t.Errorf("Expected app=exe-warmpool label, got %s", ss.Labels["app"])
	}

	// Check that sandbox configuration is applied
	if *ss.Spec.Template.Spec.RuntimeClassName != "gvisor" {
		t.Errorf("Expected gvisor runtime class, got %s", *ss.Spec.Template.Spec.RuntimeClassName)
	}

	// Check that init containers for image pre-pulling are present
	initContainers := ss.Spec.Template.Spec.InitContainers
	if len(initContainers) == 0 {
		t.Error("Expected init containers for image pre-pulling")
	}

	// Check for specific images in init containers
	foundSketch := false
	for _, initContainer := range initContainers {
		if initContainer.Image == "ghcr.io/boldsoftware/sketch" {
			foundSketch = true
			break
		}
	}
	if !foundSketch {
		t.Error("Expected ghcr.io/boldsoftware/sketch to be pre-pulled by init container")
	}

	// Check volume claim template
	vcts := ss.Spec.VolumeClaimTemplates
	if len(vcts) != 1 {
		t.Errorf("Expected 1 volume claim template, got %d", len(vcts))
	}
	if vcts[0].Name != "warm-storage" {
		t.Errorf("Expected volume claim template name warm-storage, got %s", vcts[0].Name)
	}
}