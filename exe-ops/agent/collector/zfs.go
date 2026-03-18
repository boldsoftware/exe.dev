package collector

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	"exe.dev/exe-ops/apitype"
)

// ZFS collects ZFS pool metrics. Gracefully absent if ZFS is not available.
type ZFS struct {
	Used *int64
	Free *int64

	BackupUsed *int64
	BackupFree *int64

	PoolHealth *string

	Pools []apitype.ZFSPool
}

func NewZFS() *ZFS { return &ZFS{} }

func (z *ZFS) Name() string { return "zfs" }

func (z *ZFS) Collect(ctx context.Context) error {
	z.Used = nil
	z.Free = nil
	z.BackupUsed = nil
	z.BackupFree = nil
	z.PoolHealth = nil
	z.Pools = nil

	// Check if zpool command exists.
	zpoolPath, err := exec.LookPath("zpool")
	if err != nil {
		return nil // ZFS not installed
	}

	// Get pool sizes, health, fragmentation, and capacity.
	cmd := exec.CommandContext(ctx, zpoolPath, "list", "-Hp", "-o", "name,size,alloc,free,health,frag,cap")
	out, err := cmd.Output()
	if err != nil {
		return nil // No pools or command failed
	}

	pools := parseZpoolList(string(out))

	// Get per-pool error counts from zpool status.
	cmd = exec.CommandContext(ctx, zpoolPath, "status", "-p")
	statusOut, err := cmd.Output()
	if err == nil {
		errors := parseZpoolStatus(string(statusOut))
		for i := range pools {
			if errs, ok := errors[pools[i].Name]; ok {
				pools[i].ReadErrors = errs.Read
				pools[i].WriteErrors = errs.Write
				pools[i].CksumErrors = errs.Cksum
			}
		}
	}

	z.Pools = pools

	// Populate legacy fields for backward compat.
	var tankUsed, tankFree int64
	var hasTank bool
	var worstHealth string
	for _, p := range pools {
		switch p.Name {
		case "backup":
			z.BackupUsed = &p.Used
			z.BackupFree = &p.Free
		default:
			tankUsed += p.Used
			tankFree += p.Free
			hasTank = true
			if zfsHealthWorse(p.Health, worstHealth) {
				worstHealth = p.Health
			}
		}
	}

	if hasTank {
		z.Used = &tankUsed
		z.Free = &tankFree
		z.PoolHealth = &worstHealth
	}
	return nil
}

// parseZpoolList parses output from: zpool list -Hp -o name,size,alloc,free,health,frag,cap
func parseZpoolList(output string) []apitype.ZFSPool {
	var pools []apitype.ZFSPool
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		alloc, err1 := strconv.ParseInt(fields[2], 10, 64)
		free, err2 := strconv.ParseInt(fields[3], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}

		frag := -1
		// Frag field may be "-" for pools without fragmentation (e.g. raidz).
		if s := strings.TrimSuffix(fields[5], "%"); s != "-" {
			if v, err := strconv.Atoi(s); err == nil {
				frag = v
			}
		}

		cap := 0
		if s := strings.TrimSuffix(fields[6], "%"); s != "-" {
			if v, err := strconv.Atoi(s); err == nil {
				cap = v
			}
		}

		pools = append(pools, apitype.ZFSPool{
			Name:    fields[0],
			Health:  fields[4],
			Used:    alloc,
			Free:    free,
			FragPct: frag,
			CapPct:  cap,
		})
	}
	return pools
}

// poolErrors holds aggregated error counts for a pool.
type poolErrors struct {
	Read  int64
	Write int64
	Cksum int64
}

// parseZpoolStatus parses output from: zpool status -p
// It returns per-pool aggregated error counts (summed across all vdevs).
func parseZpoolStatus(output string) map[string]poolErrors {
	result := make(map[string]poolErrors)
	var currentPool string
	inConfig := false

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)

		// Detect pool name from "pool: <name>" lines.
		if strings.HasPrefix(trimmed, "pool:") {
			currentPool = strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:"))
			inConfig = false
			continue
		}

		// Detect the config/vdev table section.
		if strings.HasPrefix(trimmed, "config:") {
			inConfig = true
			continue
		}

		// End of config section markers.
		if strings.HasPrefix(trimmed, "errors:") {
			inConfig = false
			continue
		}

		if !inConfig || currentPool == "" {
			continue
		}

		// Skip the header line (NAME STATE READ WRITE CKSUM).
		if strings.HasPrefix(trimmed, "NAME") {
			continue
		}

		// Parse vdev lines: NAME STATE READ WRITE CKSUM
		fields := strings.Fields(trimmed)
		if len(fields) < 5 {
			continue
		}

		// Skip the pool-level summary line (same name as pool) — we aggregate from children.
		// Actually, include all lines including pool-level; for single-disk pools the
		// pool line IS the only device. But for multi-vdev pools, the pool line is
		// the aggregate. To avoid double-counting, only count leaf devices.
		// However, the simplest correct approach: just use the pool-level line which
		// ZFS already aggregates.
		if fields[0] != currentPool {
			continue
		}

		read, err1 := strconv.ParseInt(fields[2], 10, 64)
		write, err2 := strconv.ParseInt(fields[3], 10, 64)
		cksum, err3 := strconv.ParseInt(fields[4], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		result[currentPool] = poolErrors{
			Read:  read,
			Write: write,
			Cksum: cksum,
		}
	}
	return result
}

// zfsHealthWorse returns true if candidate is worse than current.
// Severity order: FAULTED > DEGRADED > ONLINE (and anything else).
func zfsHealthWorse(candidate, current string) bool {
	rank := func(h string) int {
		switch h {
		case "FAULTED":
			return 3
		case "DEGRADED":
			return 2
		case "ONLINE":
			return 1
		default:
			return 0
		}
	}
	return rank(candidate) > rank(current)
}
