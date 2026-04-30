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
// Resolution order: user-specific override > plan tier cap > stage default.
// Pass tierMaxUserVMs=0 when no plan/tier context is available.
func GetMaxBoxes(userLimits *UserLimits, tierMaxUserVMs int) int {
	if userLimits != nil && userLimits.MaxBoxes > 0 {
		return userLimits.MaxBoxes
	}
	if tierMaxUserVMs > 0 {
		return tierMaxUserVMs
	}
	return stage.DefaultMaxBoxes
}

// GetMaxTeamBoxes returns the effective max number of VMs for a team.
// Resolution order: team-specific override > plan tier cap > stage default.
// Pass tierMaxTeamVMs=0 when no plan/tier context is available.
func GetMaxTeamBoxes(teamLimits *UserLimits, tierMaxTeamVMs int) int {
	if teamLimits != nil && teamLimits.MaxBoxes > 0 {
		return teamLimits.MaxBoxes
	}
	if tierMaxTeamVMs > 0 {
		return tierMaxTeamVMs
	}
	return stage.DefaultMaxTeamBoxes
}

// GetMaxMemory returns the effective max memory for a user.
// Resolution order: user-specific override > plan tier cap > stage default.
// Pass tierMaxMemory=0 when no plan/tier context is available.
func GetMaxMemory(env stage.Env, userLimits *UserLimits, tierMaxMemory uint64) uint64 {
	if userLimits != nil && userLimits.MaxMemory > 0 {
		return userLimits.MaxMemory
	}
	if tierMaxMemory > 0 {
		return tierMaxMemory
	}
	return max(env.DefaultMemory, stage.MinMemory)
}

// GetMaxCPUs returns the effective max CPUs for a user.
// Resolution order: user-specific override > plan tier cap > stage default.
// Pass tierMaxCPUs=0 when no plan/tier context is available.
func GetMaxCPUs(env stage.Env, userLimits *UserLimits, tierMaxCPUs uint64) uint64 {
	if userLimits != nil && userLimits.MaxCPUs > 0 {
		return userLimits.MaxCPUs
	}
	if tierMaxCPUs > 0 {
		return tierMaxCPUs
	}
	return max(env.DefaultCPUs, uint64(stage.MinCPUs))
}
