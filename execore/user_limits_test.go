package execore

import (
	"testing"

	"exe.dev/billing/plan"
	"exe.dev/stage"
)

func TestGetMaxMemory(t *testing.T) {
	tests := []struct {
		name       string
		defMem     uint64
		userLimits *UserLimits
		want       uint64
	}{
		{
			name:       "user limit set",
			defMem:     1 * 1024 * 1024 * 1024,
			userLimits: &UserLimits{MaxMemory: 16 * 1024 * 1024 * 1024},
			want:       16 * 1024 * 1024 * 1024,
		},
		{
			name:       "no user limit",
			defMem:     8 * 1024 * 1024 * 1024,
			userLimits: nil,
			want:       8 * 1024 * 1024 * 1024,
		},
		{
			name:       "env below min",
			defMem:     512 * 1024 * 1024,
			userLimits: nil,
			want:       stage.MinMemory,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := stage.Test()
			env.DefaultMemory = tt.defMem
			got := GetMaxMemory(env, tt.userLimits)
			if got != tt.want {
				t.Errorf("GetMaxMemory() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetMaxBoxes(t *testing.T) {
	tests := []struct {
		name           string
		userLimits     *UserLimits
		tierMaxUserVMs int
		want           int
	}{
		{"nil limits, tier unset", nil, 0, stage.DefaultMaxBoxes},
		{"nil limits, tier set", nil, 10, 10},
		{"override wins over tier", &UserLimits{MaxBoxes: 7}, 10, 7},
		{"override wins over default", &UserLimits{MaxBoxes: 7}, 0, 7},
		{"zero override falls through to tier", &UserLimits{MaxBoxes: 0}, 10, 10},
		{"zero override and tier falls through to default", &UserLimits{MaxBoxes: 0}, 0, stage.DefaultMaxBoxes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetMaxBoxes(tt.userLimits, tt.tierMaxUserVMs)
			if got != tt.want {
				t.Errorf("GetMaxBoxes(%+v, %d) = %d, want %d", tt.userLimits, tt.tierMaxUserVMs, got, tt.want)
			}
		})
	}
}

func TestGetMaxTeamBoxes(t *testing.T) {
	tests := []struct {
		name           string
		teamLimits     *UserLimits
		tierMaxTeamVMs int
		want           int
	}{
		{"nil limits, tier unset", nil, 0, stage.DefaultMaxTeamBoxes},
		{"nil limits, tier set", nil, 200, 200},
		{"override wins over tier", &UserLimits{MaxBoxes: 17}, 200, 17},
		{"override wins over default", &UserLimits{MaxBoxes: 17}, 0, 17},
		{"zero override falls through to tier", &UserLimits{MaxBoxes: 0}, 200, 200},
		{"zero override and tier falls through to default", &UserLimits{MaxBoxes: 0}, 0, stage.DefaultMaxTeamBoxes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetMaxTeamBoxes(tt.teamLimits, tt.tierMaxTeamVMs)
			if got != tt.want {
				t.Errorf("GetMaxTeamBoxes(%+v, %d) = %d, want %d", tt.teamLimits, tt.tierMaxTeamVMs, got, tt.want)
			}
		})
	}
}

// TestIncludedDiskIntegration tests that plan.IncludedDisk works correctly
// with stage.Env.DefaultDisk values from real stage configurations.
func TestIncludedDiskIntegration(t *testing.T) {
	const gb = 1024 * 1024 * 1024

	tests := []struct {
		name       string
		tierID     string
		envDefault uint64
		want       uint64
	}{
		// Prod: env.DefaultDisk=0 → tier value (25GB).
		{"individual prod", "individual:small:monthly:20260106", 0, 25 * gb},
		{"trial prod", "trial", 0, 25 * gb},
		{"basic prod", "basic", 0, 25 * gb},

		// Local: env.DefaultDisk=10GB → capped.
		{"individual local", "individual:small:monthly:20260106", 10 * gb, 10 * gb},
		{"trial local", "trial", 10 * gb, 10 * gb},

		// Test: env.DefaultDisk=11GB → capped.
		{"individual test", "individual:small:monthly:20260106", 11 * gb, 11 * gb},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plan.IncludedDisk(tt.tierID, tt.envDefault)
			if got != tt.want {
				t.Errorf("plan.IncludedDisk(%q, %d) = %d, want %d",
					tt.tierID, tt.envDefault, got, tt.want)
			}
		})
	}
}
