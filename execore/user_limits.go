package execore

import (
	"encoding/json"

	"exe.dev/billing/plan"
	"exe.dev/exedb"
	"exe.dev/stage"
)

// UserLimits is a type alias for backward compatibility.
// New code should use plan.UserLimits directly.
type UserLimits = plan.UserLimits

// ParseUserLimits parses the limits JSON from a user record.
// Returns nil if the user has no limits override set.
func ParseUserLimits(user *exedb.User) *UserLimits {
	if user == nil || user.Limits == nil || *user.Limits == "" {
		return nil
	}
	return ParseUserLimitsFromJSON(*user.Limits)
}

// ParseTeamLimits parses the limits JSON from a team record.
// Returns nil if the team has no limits override set.
func ParseTeamLimits(team *exedb.Team) *UserLimits {
	if team == nil || team.Limits == nil || *team.Limits == "" {
		return nil
	}
	return ParseUserLimitsFromJSON(*team.Limits)
}

// ParseUserLimitsFromJSON parses the limits JSON string.
// Returns nil if the string is empty or invalid JSON.
func ParseUserLimitsFromJSON(limitsJSON string) *UserLimits {
	if limitsJSON == "" {
		return nil
	}
	var limits UserLimits
	if err := json.Unmarshal([]byte(limitsJSON), &limits); err != nil {
		return nil // Invalid JSON treated as no limits
	}
	return &limits
}

// GetMaxBoxes returns the effective max number of VMs for a user.
// Uses user-specific limit if set, otherwise falls back to DefaultMaxBoxes.
func GetMaxBoxes(userLimits *UserLimits) int {
	if userLimits != nil && userLimits.MaxBoxes > 0 {
		return userLimits.MaxBoxes
	}
	return stage.DefaultMaxBoxes
}

// GetMaxTeamBoxes returns the effective max number of VMs for a team.
// Uses team-specific limit if set, otherwise falls back to DefaultMaxTeamBoxes.
func GetMaxTeamBoxes(teamLimits *UserLimits) int {
	if teamLimits != nil && teamLimits.MaxBoxes > 0 {
		return teamLimits.MaxBoxes
	}
	return stage.DefaultMaxTeamBoxes
}

// GetMaxMemory returns the effective max memory for a user.
// Uses user-specific limit if set, otherwise falls back to environment default.
func GetMaxMemory(env stage.Env, userLimits *UserLimits) uint64 {
	// Use user-specific limit if set
	if userLimits != nil && userLimits.MaxMemory > 0 {
		return userLimits.MaxMemory
	}
	// Fall back to environment default (but at least the minimum)
	return max(env.DefaultMemory, stage.MinMemory)
}

// GetMaxDisk returns the effective max disk for a user.
// Uses user-specific limit if set, otherwise falls back to environment default.
func GetMaxDisk(env stage.Env, userLimits *UserLimits) uint64 {
	// Use user-specific limit if set
	if userLimits != nil && userLimits.MaxDisk > 0 {
		return userLimits.MaxDisk
	}
	// Fall back to environment default (but at least the minimum)
	return max(env.DefaultDisk, stage.MinDisk)
}

// GetMaxCPUs returns the effective max CPUs for a user.
// Uses user-specific limit if set, otherwise falls back to environment default.
func GetMaxCPUs(env stage.Env, userLimits *UserLimits) uint64 {
	// Use user-specific limit if set
	if userLimits != nil && userLimits.MaxCPUs > 0 {
		return userLimits.MaxCPUs
	}
	// Fall back to environment default (but at least the minimum)
	return max(env.DefaultCPUs, uint64(stage.MinCPUs))
}
