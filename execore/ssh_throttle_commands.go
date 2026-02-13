package execore

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"exe.dev/desiredstate"
	"exe.dev/exedb"
	"exe.dev/exemenu"
)

func throttleVMFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("throttle-vm", flag.ContinueOnError)
	fs.String("cpu", "", "CPU limit as fraction of 1 core (e.g. 0.1 = 10% of 1 CPU, 2.5 = 2.5 cores)")
	fs.String("io", "", "symmetric IO bandwidth limit (e.g. '10M', '1G', '500K', or bytes; 0 or 'clear' to remove)")
	fs.String("io-read", "", "IO read bandwidth limit (e.g. '10M', '1G', '500K', or bytes)")
	fs.String("io-write", "", "IO write bandwidth limit (e.g. '10M', '1G', '500K', or bytes)")
	fs.String("raw", "", "raw cgroup setting as path:value (e.g. 'cpu.max:10000 100000')")
	fs.Bool("show", false, "show current cgroup overrides")
	fs.Bool("clear", false, "clear all cgroup overrides")
	return fs
}

func throttleUserFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("throttle-user", flag.ContinueOnError)
	fs.String("cpu", "", "CPU limit as fraction of 1 core (e.g. 0.1 = 10% of 1 CPU, 2.5 = 2.5 cores)")
	fs.String("io", "", "symmetric IO bandwidth limit (e.g. '10M', '1G', '500K', or bytes; 0 or 'clear' to remove)")
	fs.String("io-read", "", "IO read bandwidth limit (e.g. '10M', '1G', '500K', or bytes)")
	fs.String("io-write", "", "IO write bandwidth limit (e.g. '10M', '1G', '500K', or bytes)")
	fs.String("raw", "", "raw cgroup setting as path:value (e.g. 'cpu.max:10000 100000')")
	fs.Bool("show", false, "show current cgroup overrides")
	fs.Bool("clear", false, "clear all cgroup overrides")
	return fs
}

// parseThrottleFlags extracts cgroup settings from the common --cpu / --io / --raw flags.
// Returns the settings to apply and whether "show" or "clear" was requested.
func parseThrottleFlags(fs *flag.FlagSet) (settings []desiredstate.CgroupSetting, show, clear bool, err error) {
	show, _ = strconv.ParseBool(fs.Lookup("show").Value.String())
	clear, _ = strconv.ParseBool(fs.Lookup("clear").Value.String())

	cpuStr := fs.Lookup("cpu").Value.String()
	ioStr := fs.Lookup("io").Value.String()
	ioReadStr := fs.Lookup("io-read").Value.String()
	ioWriteStr := fs.Lookup("io-write").Value.String()
	rawStr := fs.Lookup("raw").Value.String()

	hasValues := cpuStr != "" || ioStr != "" || ioReadStr != "" || ioWriteStr != "" || rawStr != ""

	if show || clear {
		if hasValues {
			return nil, false, false, fmt.Errorf("--show and --clear cannot be combined with --cpu, --io, --io-read, --io-write, or --raw")
		}
		return nil, show, clear, nil
	}

	if cpuStr != "" {
		if cpuStr == "clear" {
			// --cpu=clear removes the cpu.max override
			settings = append(settings, desiredstate.CgroupSetting{Path: "cpu.max", Value: ""})
		} else {
			fraction, parseErr := strconv.ParseFloat(cpuStr, 64)
			if parseErr != nil || fraction < 0 {
				return nil, false, false, fmt.Errorf("invalid --cpu value %q: must be a positive number (e.g. 0.1, 1.0, 2.5)", cpuStr)
			}
			if fraction == 0 {
				// --cpu=0 clears the override
				settings = append(settings, desiredstate.CgroupSetting{Path: "cpu.max", Value: ""})
			} else {
				settings = append(settings, desiredstate.CgroupSetting{
					Path:  "cpu.max",
					Value: desiredstate.CPUFractionToMax(fraction),
				})
			}
		}
	}

	// Handle --io (symmetric read+write) or --io-read / --io-write (asymmetric)
	if ioStr != "" {
		if ioReadStr != "" || ioWriteStr != "" {
			return nil, false, false, fmt.Errorf("--io cannot be combined with --io-read or --io-write")
		}
		ioSetting, parseErr := buildIOMaxSetting(ioStr, ioStr)
		if parseErr != nil {
			return nil, false, false, parseErr
		}
		settings = append(settings, ioSetting)
	} else if ioReadStr != "" || ioWriteStr != "" {
		ioSetting, parseErr := buildIOMaxSetting(ioReadStr, ioWriteStr)
		if parseErr != nil {
			return nil, false, false, parseErr
		}
		settings = append(settings, ioSetting)
	}

	if rawStr != "" {
		path, value, ok := strings.Cut(rawStr, ":")
		if !ok {
			return nil, false, false, fmt.Errorf("invalid --raw format: expected 'path:value' (e.g. 'cpu.max:10000 100000')")
		}
		settings = append(settings, desiredstate.CgroupSetting{Path: path, Value: value})
	}

	if len(settings) == 0 {
		return nil, false, false, fmt.Errorf("at least one of --cpu, --io, --io-read, --io-write, --raw, --show, or --clear is required")
	}

	return settings, false, false, nil
}

// buildIOMaxSetting builds a single io.max CgroupSetting with the ~ device
// placeholder. readBW and writeBW are bandwidth strings ("10M", "1G", etc.);
// empty string means "don't set that direction"; "clear" or "0" means "max".
//
// Examples:
//
//	buildIOMaxSetting("10M", "10M")   → io.max: ~ rbps=10485760 wbps=10485760
//	buildIOMaxSetting("50M", "")      → io.max: ~ rbps=52428800
//	buildIOMaxSetting("clear", "clear") → io.max: ~ rbps=max wbps=max
func buildIOMaxSetting(readBW, writeBW string) (desiredstate.CgroupSetting, error) {
	var parts []string
	parts = append(parts, desiredstate.IOMaxDevicePlaceholder)

	if readBW != "" {
		val, err := bwToIOMaxVal(readBW)
		if err != nil {
			return desiredstate.CgroupSetting{}, fmt.Errorf("invalid --io-read value: %w", err)
		}
		parts = append(parts, "rbps="+val)
	}
	if writeBW != "" {
		val, err := bwToIOMaxVal(writeBW)
		if err != nil {
			return desiredstate.CgroupSetting{}, fmt.Errorf("invalid --io-write value: %w", err)
		}
		parts = append(parts, "wbps="+val)
	}

	return desiredstate.CgroupSetting{
		Path:  "io.max",
		Value: strings.Join(parts, " "),
	}, nil
}

// bwToIOMaxVal converts a bandwidth flag value to the io.max value string.
// "clear" and "0" become "max"; otherwise parses as a bandwidth with K/M/G suffix.
func bwToIOMaxVal(s string) (string, error) {
	if s == "clear" || s == "0" {
		return "max", nil
	}
	bytes, err := parseBandwidth(s)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(bytes, 10), nil
}

// parseBandwidth parses a bandwidth string like "10M", "1G", "500K", or plain bytes.
// Supported suffixes: K/k (1024), M/m (1024^2), G/g (1024^3).
func parseBandwidth(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty bandwidth value")
	}

	var multiplier uint64 = 1
	lastChar := s[len(s)-1]

	switch lastChar {
	case 'k', 'K':
		multiplier = 1024
		s = s[:len(s)-1]
	case 'm', 'M':
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case 'g', 'G':
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}

	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid bandwidth value: %s", s)
	}

	const maxUint64 = ^uint64(0)
	if val > maxUint64/multiplier {
		return 0, fmt.Errorf("bandwidth value too large: %s (would overflow)", s)
	}

	return val * multiplier, nil
}

func (ss *SSHServer) handleThrottleVMCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if !ss.server.UserHasExeSudo(ctx, cc.User.ID) {
		return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", cc.User.Email)
	}

	if len(cc.Args) != 1 {
		return cc.Errorf("usage: throttle-vm <vmname> [--cpu=<fraction>] [--io=<bw>] [--io-read=<bw>] [--io-write=<bw>] [--raw=<path:value>] [--show] [--clear]\n\n" +
			"Manage cgroup overrides for a VM. These overrides are published via\n" +
			"/exelet-desired and applied by the exelet's desired-state sync loop.\n\n" +
			"--cpu sets cpu.max as a fraction of 1 CPU core:\n" +
			"  --cpu=0.1    10%% of 1 core\n" +
			"  --cpu=1.0    1 full core\n" +
			"  --cpu=2.5    2.5 cores\n" +
			"  --cpu=0      clear the cpu.max override\n" +
			"  --cpu=clear  clear the cpu.max override\n\n" +
			"--io sets symmetric read+write IO bandwidth limit:\n" +
			"  --io=10M     10 MB/s read and write\n" +
			"  --io=1G      1 GB/s read and write\n" +
			"  --io=500K    500 KB/s read and write\n" +
			"  --io=0       clear IO overrides\n" +
			"  --io=clear   clear IO overrides\n\n" +
			"--io-read / --io-write set asymmetric IO bandwidth limits:\n" +
			"  --io-read=50M   50 MB/s read\n" +
			"  --io-write=1G   1 GB/s write\n\n" +
			"--raw sets an arbitrary cgroup file (empty value clears it):\n" +
			"  --raw='cpu.max:10000 100000'\n" +
			"  --raw='memory.high:1073741824'\n" +
			"  --raw='cpu.max:'              (clears the cpu.max override)\n\n" +
			"--show displays current overrides, --clear removes all overrides.")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found", boxName)
	}
	if err != nil {
		return cc.Errorf("failed to look up VM: %v", err)
	}

	settings, show, clear, err := parseThrottleFlags(cc.FlagSet)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	if show {
		current := desiredstate.ParseOverrides(derefStr(box.CgroupOverrides))
		if len(current) == 0 {
			fmt.Fprintf(cc.Output, "No cgroup overrides for VM %s\r\n", boxName)
		} else {
			fmt.Fprintf(cc.Output, "Cgroup overrides for VM %s:\r\n", boxName)
			for _, s := range current {
				fmt.Fprintf(cc.Output, "  %s: %s\r\n", s.Path, s.Value)
			}
		}
		return nil
	}

	if clear {
		err := withTx1(ss.server, ctx, (*exedb.Queries).SetBoxCgroupOverrides, exedb.SetBoxCgroupOverridesParams{
			CgroupOverrides: nil,
			ID:              box.ID,
		})
		if err != nil {
			return cc.Errorf("failed to clear overrides: %v", err)
		}
		fmt.Fprintf(cc.Output, "Cleared all cgroup overrides for VM %s\r\n", boxName)
		return nil
	}

	// Merge new settings with existing
	existing := desiredstate.ParseOverrides(derefStr(box.CgroupOverrides))
	merged := desiredstate.MergeOverrides(existing, settings)
	var dbVal *string
	if s := desiredstate.FormatOverrides(merged); s != "" {
		dbVal = &s
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).SetBoxCgroupOverrides, exedb.SetBoxCgroupOverridesParams{
		CgroupOverrides: dbVal,
		ID:              box.ID,
	})
	if err != nil {
		return cc.Errorf("failed to set overrides: %v", err)
	}

	if len(merged) == 0 {
		fmt.Fprintf(cc.Output, "Cleared all cgroup overrides for VM %s\r\n", boxName)
	} else {
		fmt.Fprintf(cc.Output, "Updated cgroup overrides for VM %s:\r\n", boxName)
		for _, s := range merged {
			fmt.Fprintf(cc.Output, "  %s: %s\r\n", s.Path, s.Value)
		}
	}
	return nil
}

func (ss *SSHServer) handleThrottleUserCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if !ss.server.UserHasExeSudo(ctx, cc.User.ID) {
		return cc.Errorf("%s is not in the sudoers file. This incident will be reported.", cc.User.Email)
	}

	if len(cc.Args) != 1 {
		return cc.Errorf("usage: throttle-user <email-or-userid> [--cpu=<fraction>] [--io=<bw>] [--io-read=<bw>] [--io-write=<bw>] [--raw=<path:value>] [--show] [--clear]\n\n" +
			"Manage cgroup overrides for a user (applies to all their VMs at the group level).\n" +
			"These overrides are published via /exelet-desired and applied by exelet.\n\n" +
			"--cpu sets cpu.max as a fraction of 1 CPU core:\n" +
			"  --cpu=0.1    10%% of 1 core (shared across all user's VMs)\n" +
			"  --cpu=1.0    1 full core\n" +
			"  --cpu=0      clear the cpu.max override\n" +
			"  --cpu=clear  clear the cpu.max override\n\n" +
			"--io sets symmetric read+write IO bandwidth limit:\n" +
			"  --io=10M     10 MB/s read and write\n" +
			"  --io=1G      1 GB/s read and write\n" +
			"  --io=0       clear IO overrides\n" +
			"  --io=clear   clear IO overrides\n\n" +
			"--io-read / --io-write set asymmetric IO bandwidth limits:\n" +
			"  --io-read=50M   50 MB/s read\n" +
			"  --io-write=1G   1 GB/s write\n\n" +
			"--raw sets an arbitrary cgroup file (empty value clears it):\n" +
			"  --raw='cpu.max:10000 100000'\n" +
			"  --raw='cpu.max:'              (clears the override)\n\n" +
			"--show displays current overrides, --clear removes all overrides.")
	}

	userRef := cc.Args[0]

	// Try to look up user by email first, then by user ID
	user, err := ss.lookupUserByRef(ctx, userRef)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	settings, show, clear, err := parseThrottleFlags(cc.FlagSet)
	if err != nil {
		return cc.Errorf("%v", err)
	}

	if show {
		current := desiredstate.ParseOverrides(derefStr(user.CgroupOverrides))
		if len(current) == 0 {
			fmt.Fprintf(cc.Output, "No cgroup overrides for user %s (%s)\r\n", user.Email, user.UserID)
		} else {
			fmt.Fprintf(cc.Output, "Cgroup overrides for user %s (%s):\r\n", user.Email, user.UserID)
			for _, s := range current {
				fmt.Fprintf(cc.Output, "  %s: %s\r\n", s.Path, s.Value)
			}
		}
		return nil
	}

	if clear {
		err := withTx1(ss.server, ctx, (*exedb.Queries).SetUserCgroupOverrides, exedb.SetUserCgroupOverridesParams{
			CgroupOverrides: nil,
			UserID:          user.UserID,
		})
		if err != nil {
			return cc.Errorf("failed to clear overrides: %v", err)
		}
		fmt.Fprintf(cc.Output, "Cleared all cgroup overrides for user %s (%s)\r\n", user.Email, user.UserID)
		return nil
	}

	// Merge new settings with existing
	existing := desiredstate.ParseOverrides(derefStr(user.CgroupOverrides))
	merged := desiredstate.MergeOverrides(existing, settings)
	var dbVal *string
	if s := desiredstate.FormatOverrides(merged); s != "" {
		dbVal = &s
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).SetUserCgroupOverrides, exedb.SetUserCgroupOverridesParams{
		CgroupOverrides: dbVal,
		UserID:          user.UserID,
	})
	if err != nil {
		return cc.Errorf("failed to set overrides: %v", err)
	}

	if len(merged) == 0 {
		fmt.Fprintf(cc.Output, "Cleared all cgroup overrides for user %s (%s)\r\n", user.Email, user.UserID)
	} else {
		fmt.Fprintf(cc.Output, "Updated cgroup overrides for user %s (%s):\r\n", user.Email, user.UserID)
		for _, s := range merged {
			fmt.Fprintf(cc.Output, "  %s: %s\r\n", s.Path, s.Value)
		}
	}
	return nil
}

// lookupUserByRef looks up a user by email or user ID.
func (ss *SSHServer) lookupUserByRef(ctx context.Context, ref string) (*exedb.User, error) {
	// If it contains @, treat as email
	if strings.Contains(ref, "@") {
		canonical := strings.ToLower(strings.TrimSpace(ref))
		user, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserByEmail, &canonical)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("user with email %q not found", ref)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to look up user: %v", err)
		}
		return &user, nil
	}
	// Otherwise treat as user ID
	user, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserWithDetails, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user with ID %q not found", ref)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to look up user: %v", err)
	}
	return &user, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
