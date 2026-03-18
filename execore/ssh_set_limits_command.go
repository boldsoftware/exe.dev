package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strconv"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/stage"
)

func setLimitsFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("set-limits", flag.ContinueOnError)
	fs.String("max-boxes", "", "maximum number of VMs (e.g. '50')")
	fs.String("max-memory", "", "maximum memory per VM (e.g. '8GB', '16GB')")
	fs.String("max-disk", "", "maximum disk per VM (e.g. '40GB', '100GB')")
	fs.String("max-cpus", "", "maximum CPUs per VM (e.g. '4')")
	fs.Bool("show", false, "show current limits without changing anything")
	fs.Bool("clear", false, "clear all limit overrides")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

func (ss *SSHServer) handleSetLimitsCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: sudo-exe set-limits <userid-or-email> [--max-boxes=N] [--max-memory=SIZE] [--max-disk=SIZE] [--max-cpus=N] [--show] [--clear]")
	}

	user, err := ss.lookupUserByRef(ctx, cc.Args[0])
	if err != nil {
		return cc.Errorf("%v", err)
	}

	show, _ := strconv.ParseBool(cc.FlagSet.Lookup("show").Value.String())
	clear, _ := strconv.ParseBool(cc.FlagSet.Lookup("clear").Value.String())

	if show {
		return ss.showUserLimits(ctx, cc, user)
	}

	if clear {
		err := withTx1(ss.server, ctx, (*exedb.Queries).SetUserLimits, exedb.SetUserLimitsParams{
			Limits: nil,
			UserID: user.UserID,
		})
		if err != nil {
			return cc.Errorf("failed to clear limits: %v", err)
		}
		cc.Writeln("Cleared all limit overrides for %s (%s)", user.Email, user.UserID)
		// Show actual effective limit after clearing (may come from team)
		team, _ := ss.server.GetTeamForUser(ctx, user.UserID)
		var effectiveLimits *UserLimits
		if team != nil && team.Limits != nil {
			effectiveLimits = ParseUserLimitsFromJSON(*team.Limits)
		}
		var effectiveMaxBoxes int
		if team != nil {
			effectiveMaxBoxes = GetMaxTeamBoxes(effectiveLimits)
			cc.Writeln("Effective max VMs: %d (team: %s)", effectiveMaxBoxes, team.TeamID)
		} else {
			effectiveMaxBoxes = GetMaxBoxes(effectiveLimits)
			cc.Writeln("Effective max VMs: %d (default)", effectiveMaxBoxes)
		}
		return nil
	}

	// Parse flags into a limits struct, merging with existing
	existing := ParseUserLimits(user)
	if existing == nil {
		existing = &UserLimits{}
	}

	changed := false

	if s := cc.FlagSet.Lookup("max-boxes").Value.String(); s != "" {
		v, parseErr := strconv.Atoi(s)
		if parseErr != nil || v < 1 {
			return cc.Errorf("invalid --max-boxes: must be a positive integer")
		}
		// Shards > 25 require GLB (nXXX CNAMEs). Without it, DNS for
		// sXXX shards beyond s025 won't resolve and boxes are unreachable.
		if v > stage.DefaultMaxBoxes {
			if err := ss.requireGLB(ctx, user); err != nil {
				return cc.Errorf("%v", err)
			}
		}
		existing.MaxBoxes = v
		changed = true
	}
	if s := cc.FlagSet.Lookup("max-cpus").Value.String(); s != "" {
		v, parseErr := strconv.Atoi(s)
		if parseErr != nil || v < 1 {
			return cc.Errorf("invalid --max-cpus: must be a positive integer")
		}
		existing.MaxCPUs = uint64(v)
		changed = true
	}
	if s := cc.FlagSet.Lookup("max-memory").Value.String(); s != "" {
		parsed, parseErr := parseSize(s)
		if parseErr != nil {
			return cc.Errorf("invalid --max-memory: %v", parseErr)
		}
		existing.MaxMemory = parsed
		changed = true
	}
	if s := cc.FlagSet.Lookup("max-disk").Value.String(); s != "" {
		parsed, parseErr := parseSize(s)
		if parseErr != nil {
			return cc.Errorf("invalid --max-disk: %v", parseErr)
		}
		existing.MaxDisk = parsed
		changed = true
	}

	if !changed {
		return cc.Errorf("at least one of --max-boxes, --max-memory, --max-disk, --max-cpus, --show, or --clear is required")
	}

	// Serialize and store
	limitsJSON, err := json.Marshal(existing)
	if err != nil {
		return cc.Errorf("failed to serialize limits: %v", err)
	}
	limitsStr := string(limitsJSON)

	err = withTx1(ss.server, ctx, (*exedb.Queries).SetUserLimits, exedb.SetUserLimitsParams{
		Limits: &limitsStr,
		UserID: user.UserID,
	})
	if err != nil {
		return cc.Errorf("failed to set limits: %v", err)
	}

	cc.Writeln("Updated limits for %s (%s)", user.Email, user.UserID)
	printLimits(cc, existing)
	return nil
}

func (ss *SSHServer) showUserLimits(ctx context.Context, cc *exemenu.CommandContext, user *exedb.User) error {
	limits := ParseUserLimits(user)

	// Also get team context
	team, _ := ss.server.GetTeamForUser(ctx, user.UserID)

	// Count current boxes
	boxCount, _ := ss.server.CountBoxesForLimitCheck(ctx, user.UserID)

	// Compute effective limits: team limits override user limits (matches enforcement in allocateIPShard)
	effectiveLimits := limits
	if team != nil && team.Limits != nil {
		effectiveLimits = ParseUserLimitsFromJSON(*team.Limits)
	}
	var effectiveMaxBoxes int
	if team != nil {
		effectiveMaxBoxes = GetMaxTeamBoxes(effectiveLimits)
	} else {
		effectiveMaxBoxes = GetMaxBoxes(effectiveLimits)
	}
	glbStatus := ss.userGLBStatus(ctx, user.UserID)

	if cc.WantJSON() {
		result := map[string]any{
			"user_id":             user.UserID,
			"email":               user.Email,
			"current_boxes":       boxCount,
			"effective_max_boxes": effectiveMaxBoxes,
			"default_max_boxes":   stage.DefaultMaxBoxes,
			"glb":                 glbStatus,
		}
		if limits != nil {
			result["overrides"] = limits
		}
		if team != nil {
			result["team_id"] = team.TeamID
			if team.Limits != nil {
				teamLimits := ParseUserLimitsFromJSON(*team.Limits)
				if teamLimits != nil {
					result["team_limits"] = teamLimits
				}
			}
		}
		cc.WriteJSON(result)
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1mUser:\033[0m %s (%s)", user.Email, user.UserID)
	cc.Writeln("\033[1mCurrent VMs:\033[0m %d / %d", boxCount, effectiveMaxBoxes)
	cc.Writeln("\033[1mGLB:\033[0m %s", glbStatus)
	cc.Writeln("")

	if limits != nil && (limits.MaxBoxes > 0 || limits.MaxMemory > 0 || limits.MaxDisk > 0 || limits.MaxCPUs > 0) {
		cc.Writeln("\033[1;33m── User Limit Overrides ──\033[0m")
		printLimits(cc, limits)
	} else {
		cc.Writeln("\033[2mNo user limit overrides (using defaults)\033[0m")
	}

	if team != nil {
		cc.Writeln("")
		cc.Writeln("\033[1;33m── Team ──\033[0m")
		cc.Writeln("  Team ID: %s", team.TeamID)
		if team.Limits != nil {
			teamLimits := ParseUserLimitsFromJSON(*team.Limits)
			if teamLimits != nil {
				cc.Writeln("  Team limits:")
				printLimitsIndented(cc, teamLimits, "    ")
			}
		} else {
			cc.Writeln("  \033[2mNo team limit overrides\033[0m")
		}
	}

	cc.Writeln("")
	return nil
}

func printLimits(cc *exemenu.CommandContext, limits *UserLimits) {
	printLimitsIndented(cc, limits, "  ")
}

func printLimitsIndented(cc *exemenu.CommandContext, limits *UserLimits, indent string) {
	if limits.MaxBoxes > 0 {
		cc.Writeln("%smax_boxes:  %d", indent, limits.MaxBoxes)
	}
	if limits.MaxMemory > 0 {
		cc.Writeln("%smax_memory: %s", indent, formatBytes(limits.MaxMemory))
	}
	if limits.MaxDisk > 0 {
		cc.Writeln("%smax_disk:   %s", indent, formatBytes(limits.MaxDisk))
	}
	if limits.MaxCPUs > 0 {
		cc.Writeln("%smax_cpus:   %d", indent, limits.MaxCPUs)
	}
}

// requireGLB checks that the target user has GLB (global load balancer) enabled.
// Without GLB, shards > 25 CNAME to sXXX.exe.xyz which only exists for s001–s025,
// so boxes with higher shard numbers would be unreachable.
func (ss *SSHServer) requireGLB(ctx context.Context, user *exedb.User) error {
	defaults, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserDefaults, user.UserID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (defaults.GlobalLoadBalancer == nil || *defaults.GlobalLoadBalancer != 1)) {
		return fmt.Errorf("user %s does not have GLB enabled; shards > %d require it.\n"+
			"Enable with: defaults write dev.exe global-load-balancer on\n"+
			"(or use the debug page to migrate the user's region)\n"+
			"Note: users enabled via GLB rollout prefix still need the explicit flag for >%d boxes",
			user.Email, stage.DefaultMaxBoxes, stage.DefaultMaxBoxes)
	}
	if err != nil {
		return fmt.Errorf("failed to check GLB status: %v", err)
	}
	return nil
}

// userGLBStatus returns a human-readable GLB status for the user.
func (ss *SSHServer) userGLBStatus(ctx context.Context, userID string) string {
	defaults, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserDefaults, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "unset"
	}
	if err != nil {
		return "error"
	}
	if defaults.GlobalLoadBalancer == nil {
		return "unset"
	}
	if *defaults.GlobalLoadBalancer == 1 {
		return "on"
	}
	return "off"
}

// formatBytes formats bytes as a human-readable string (e.g., "8 GB").
func formatBytes(b uint64) string {
	const (
		gb = 1000 * 1000 * 1000
		mb = 1000 * 1000
	)
	switch {
	case b >= gb:
		val := float64(b) / float64(gb)
		if val == float64(int(val)) {
			return fmt.Sprintf("%d GB", int(val))
		}
		return fmt.Sprintf("%.1f GB", val)
	case b >= mb:
		return fmt.Sprintf("%d MB", b/mb)
	default:
		return fmt.Sprintf("%d bytes", b)
	}
}
