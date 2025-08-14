package container

import (
	"testing"
)

func TestContainerSizes(t *testing.T) {
	// Test that all expected sizes exist
	expectedSizes := []string{"micro", "small", "medium", "large", "xlarge"}

	for _, size := range expectedSizes {
		preset, exists := ContainerSizes[size]
		if !exists {
			t.Errorf("Expected size %s to exist", size)
			continue
		}

		// Verify all fields are populated
		if preset.Name == "" {
			t.Errorf("Size %s has empty Name", size)
		}
		if preset.DisplayName == "" {
			t.Errorf("Size %s has empty DisplayName", size)
		}
		if preset.CPURequest == "" {
			t.Errorf("Size %s has empty CPURequest", size)
		}
		if preset.MemoryRequest == "" {
			t.Errorf("Size %s has empty MemoryRequest", size)
		}
		if preset.StorageSize == "" {
			t.Errorf("Size %s has empty StorageSize", size)
		}
		if preset.Description == "" {
			t.Errorf("Size %s has empty Description", size)
		}
	}

	// Test specific size values
	micro := ContainerSizes["micro"]
	if micro.CPURequest != "250m" {
		t.Errorf("Expected micro CPU to be 250m, got %s", micro.CPURequest)
	}
	if micro.MemoryRequest != "512Mi" {
		t.Errorf("Expected micro memory to be 512Mi, got %s", micro.MemoryRequest)
	}
	if micro.StorageSize != "5Gi" {
		t.Errorf("Expected micro storage to be 5Gi, got %s", micro.StorageSize)
	}

	xlarge := ContainerSizes["xlarge"]
	if xlarge.CPURequest != "4000m" {
		t.Errorf("Expected xlarge CPU to be 4000m, got %s", xlarge.CPURequest)
	}
	if xlarge.MemoryRequest != "16Gi" {
		t.Errorf("Expected xlarge memory to be 16Gi, got %s", xlarge.MemoryRequest)
	}
	if xlarge.StorageSize != "100Gi" {
		t.Errorf("Expected xlarge storage to be 100Gi, got %s", xlarge.StorageSize)
	}
}

func TestCreateContainerRequestDefaults(t *testing.T) {
	req := CreateContainerRequest{
		UserID: "test-user",
		Name:   "test-container",
	}

	// Verify the fields exist and can be set
	req.Size = "medium"
	req.CPURequest = "1000m"
	req.MemoryRequest = "4Gi"
	req.StorageSize = "20Gi"
	req.Ephemeral = true

	if req.Size != "medium" {
		t.Errorf("Expected Size to be medium, got %s", req.Size)
	}
	if !req.Ephemeral {
		t.Error("Expected Ephemeral to be true")
	}
}
