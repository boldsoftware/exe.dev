package resourcemanager

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capacity handles node capacity detection.
type Capacity struct {
	zfsPools []string // all pool names (primary + tiers)
	log      *slog.Logger

	mu           sync.RWMutex
	cpus         uint64
	memoryBytes  uint64
	diskBytes    uint64
	lastRefresh  time.Time
	refreshError error

	// For testing
	procMeminfo string
}

// NewCapacity creates a new Capacity detector.
func NewCapacity(zfsPool string, log *slog.Logger) *Capacity {
	var pools []string
	if zfsPool != "" {
		pools = []string{zfsPool}
	}
	c := &Capacity{
		zfsPools:    pools,
		log:         log,
		procMeminfo: "/proc/meminfo",
	}
	return c
}

// NewCapacityWithPools creates a Capacity detector for multiple ZFS pools.
func NewCapacityWithPools(pools []string, log *slog.Logger) *Capacity {
	return &Capacity{
		zfsPools:    pools,
		log:         log,
		procMeminfo: "/proc/meminfo",
	}
}

// Get returns the current node capacity, refreshing if needed.
func (c *Capacity) Get(ctx context.Context) (cpus, memoryBytes, diskBytes uint64, err error) {
	c.mu.RLock()
	// Cache capacity for 1 minute
	if time.Since(c.lastRefresh) < time.Minute && c.refreshError == nil {
		cpus, memoryBytes, diskBytes = c.cpus, c.memoryBytes, c.diskBytes
		c.mu.RUnlock()
		return cpus, memoryBytes, diskBytes, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if time.Since(c.lastRefresh) < time.Minute && c.refreshError == nil {
		return c.cpus, c.memoryBytes, c.diskBytes, nil
	}

	c.cpus = uint64(runtime.NumCPU())

	c.memoryBytes, err = c.detectMemory(ctx)
	if err != nil {
		c.refreshError = fmt.Errorf("failed to detect memory: %w", err)
		return 0, 0, 0, c.refreshError
	}

	if len(c.zfsPools) > 0 {
		c.diskBytes, err = c.detectZFSPoolSize(ctx)
		if err != nil {
			c.refreshError = fmt.Errorf("failed to detect ZFS pool size: %w", err)
			return 0, 0, 0, c.refreshError
		}
	}

	c.lastRefresh = time.Now()
	c.refreshError = nil

	c.log.DebugContext(ctx, "capacity detected",
		"cpus", c.cpus,
		"memory_bytes", c.memoryBytes,
		"disk_bytes", c.diskBytes)

	return c.cpus, c.memoryBytes, c.diskBytes, nil
}

// detectMemory reads total memory from /proc/meminfo.
func (c *Capacity) detectMemory(ctx context.Context) (uint64, error) {
	info, err := readMemInfoFile(ctx, c.procMeminfo)
	if err != nil {
		return 0, err
	}
	return uint64(info.memTotal) * 1024, nil
}

// detectZFSPoolSize returns the aggregate size of all ZFS pools in bytes.
func (c *Capacity) detectZFSPoolSize(ctx context.Context) (uint64, error) {
	if len(c.zfsPools) == 0 {
		return 0, nil
	}

	var totalSize uint64
	seen := make(map[string]struct{})
	for _, pool := range c.zfsPools {
		if _, ok := seen[pool]; ok {
			continue // skip duplicate pool names
		}
		seen[pool] = struct{}{}

		size, err := c.getPoolSize(ctx, pool)
		if err != nil {
			return 0, err
		}
		totalSize += size
	}

	return totalSize, nil
}

// getPoolSize returns the size of a single ZFS pool in bytes.
func (c *Capacity) getPoolSize(ctx context.Context, pool string) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// zpool get -Hp size <pool> returns: <pool>\tsize\t<bytes>\t-
	cmd := exec.CommandContext(ctx, "zpool", "get", "-Hp", "size", pool)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("zpool get size failed for %s: %w", pool, err)
	}

	fields := strings.Fields(string(output))
	if len(fields) < 3 {
		return 0, fmt.Errorf("unexpected zpool output for %s: %s", pool, string(output))
	}

	size, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse pool size for %s: %w", pool, err)
	}

	return size, nil
}
