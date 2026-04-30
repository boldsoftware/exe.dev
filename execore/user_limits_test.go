package execore

import (
	"testing"

	"exe.dev/billing/plan"
	"exe.dev/stage"
)

func TestGetMaxMemory(t *testing.T) {
	const gb = 1024 * 1024 * 1024
	tests := []struct {
		name          string
		defMem        uint64
		userLimits    *UserLimits
		tierMaxMemory uint64
		want          uint64
	}{
		{"user override", 1 * gb, &UserLimits{MaxMemory: 16 * gb}, 0, 16 * gb},
		{"no overrides", 8 * gb, nil, 0, 8 * gb},
		{"env below min", 512 * 1024 * 1024, nil, 0, stage.MinMemory},
		{"tier quota used when no user override", 1 * gb, nil, 8 * gb, 8 * gb},
		{"user override beats tier", 1 * gb, &UserLimits{MaxMemory: 16 * gb}, 8 * gb, 16 * gb},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := stage.Test()
			env.DefaultMemory = tt.defMem
			got := GetMaxMemory(env, tt.userLimits, tt.tierMaxMemory)
			if got != tt.want {
				t.Errorf("GetMaxMemory() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetMaxCPUs(t *testing.T) {
	tests := []struct {
		name        string
		defCPUs     uint64
		userLimits  *UserLimits
		tierMaxCPUs uint64
		want        uint64
	}{
		{"no overrides", 2, nil, 0, 2},
		{"tier quota", 2, nil, 4, 4},
		{"user override beats tier", 2, &UserLimits{MaxCPUs: 8}, 4, 8},
		{"env below min", 0, nil, 0, uint64(stage.MinCPUs)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := stage.Test()
			env.DefaultCPUs = tt.defCPUs
			got := GetMaxCPUs(env, tt.userLimits, tt.tierMaxCPUs)
			if got != tt.want {
				t.Errorf("GetMaxCPUs() = %d, want %d", got, tt.want)
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
