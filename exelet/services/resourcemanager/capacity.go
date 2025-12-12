package resourcemanager

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capacity handles node capacity detection.
type Capacity struct {
	zfsPool string
	log     *slog.Logger

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
	c := &Capacity{
		zfsPool:     zfsPool,
		log:         log,
		procMeminfo: "/proc/meminfo",
	}
	return c
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

	c.memoryBytes, err = c.detectMemory()
	if err != nil {
		c.refreshError = fmt.Errorf("failed to detect memory: %w", err)
		return 0, 0, 0, c.refreshError
	}

	if c.zfsPool != "" {
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
func (c *Capacity) detectMemory() (uint64, error) {
	f, err := os.Open(c.procMeminfo)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("malformed MemTotal line: %s", line)
			}
			// Value is in kB
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("failed to parse MemTotal: %w", err)
			}
			return kb * 1024, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, err
	}

	return 0, fmt.Errorf("MemTotal not found in %s", c.procMeminfo)
}

// detectZFSPoolSize returns the total size of the ZFS pool in bytes.
func (c *Capacity) detectZFSPoolSize(ctx context.Context) (uint64, error) {
	if c.zfsPool == "" {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// zpool get -Hp size <pool> returns: <pool>\tsize\t<bytes>\t-
	cmd := exec.CommandContext(ctx, "zpool", "get", "-Hp", "size", c.zfsPool)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("zpool get size failed: %w", err)
	}

	fields := strings.Fields(string(output))
	if len(fields) < 3 {
		return 0, fmt.Errorf("unexpected zpool output: %s", string(output))
	}

	size, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse pool size: %w", err)
	}

	return size, nil
}
