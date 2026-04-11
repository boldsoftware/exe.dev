package execore

import (
	"testing"
)

func TestCgroupUnitCapacityFromMem(t *testing.T) {
	tests := []struct {
		name        string
		memTotalKiB int64
		want        int32
	}{
		{"tiny dev host", 4 * 1024 * 1024, 25},
		{"384 GiB host", 384 * 1024 * 1024, 75},
		{"384 GiB host (kernel reserved)", 370 * 1024 * 1024, 75},
		{"768 GiB host", 768 * 1024 * 1024, 150},
		{"768 GiB host (kernel reserved)", 740 * 1024 * 1024, 150},
		{"1536 GiB host", 1536 * 1024 * 1024, 300},
		{"1536 GiB host (kernel reserved)", 1500 * 1024 * 1024, 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cgroupUnitCapacityFromMem(tt.memTotalKiB)
			if got != tt.want {
				t.Errorf("cgroupUnitCapacityFromMem(%d) = %d, want %d", tt.memTotalKiB, got, tt.want)
			}
		})
	}
}

func TestUserCgroupUnits(t *testing.T) {
	tests := []struct {
		name   string
		limits *UserLimits
		want   int
	}{
		{"nil limits (default 8GB)", nil, 1},
		{"zero max_memory (default 8GB)", &UserLimits{MaxMemory: 0}, 1},
		{"8GB plan", &UserLimits{MaxMemory: 8 * 1024 * 1024 * 1024}, 1},
		{"16GB plan", &UserLimits{MaxMemory: 16 * 1024 * 1024 * 1024}, 2},
		{"32GB plan", &UserLimits{MaxMemory: 32 * 1024 * 1024 * 1024}, 4},
		{"64GB plan", &UserLimits{MaxMemory: 64 * 1024 * 1024 * 1024}, 8},
		{"small custom (< 8GB)", &UserLimits{MaxMemory: 4 * 1024 * 1024 * 1024}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := userCgroupUnits(tt.limits)
			if got != tt.want {
				t.Errorf("userCgroupUnits(%v) = %d, want %d", tt.limits, got, tt.want)
			}
		})
	}
}
