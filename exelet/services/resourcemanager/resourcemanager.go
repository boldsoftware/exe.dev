package resourcemanager

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"google.golang.org/grpc"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	api "exe.dev/pkg/api/exe/resource/v1"
)

const (
	// DefaultPollInterval is the default polling interval for the resource manager
	DefaultPollInterval = 30 * time.Second
)

// ResourceManager provides resource tracking and priority management for VMs.
type ResourceManager struct {
	api.UnimplementedResourceManagerServiceServer

	config  *config.ExeletConfig
	context *services.ServiceContext
	log     *slog.Logger

	// Capacity detection
	capacity     *Capacity
	zfsPool      string
	capacityOnce sync.Once

	// VM usage tracking
	usageMu    sync.Mutex
	usageState map[string]*vmUsageState

	// Host machine usage
	machineUsageMu        sync.Mutex
	machineAvailable      bool
	machineSetUsage       *api.MachineUsage
	machineUsageCache     *api.MachineUsage
	machineUsageCacheTime time.Time

	// Prometheus metrics
	metrics *prometheusMetrics

	// Priority management
	priorityMu       sync.Mutex
	priorityOverride map[string]api.VMPriority // manual overrides (cleared when set to auto)
	cgroupRoot       string

	// Test hooks (nil in production; overridden in tests)
	collectUsageFn func(ctx context.Context, id, name, groupID string) (*usageData, error)
	applyPriorityFn func(ctx context.Context, id, groupID string, priority api.VMPriority, allocatedMemoryBytes uint64) error

	// Memory reclaim
	reclaimInflight    sync.Map      // tracks in-flight memory.reclaim writes by path
	readMemAvailableFn func() uint64 // overridden in tests; nil uses /proc/meminfo

	// Polling
	pollInterval time.Duration

	// Metrics daemon reporter
	metricsReporter *MetricsDaemonReporter

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// vmUsageState tracks per-VM usage and activity
type vmUsageState struct {
	name                 string
	groupID              string // Group ID for per-account cgroup grouping
	cpuSeconds           float64
	cpuPercent           float64 // CPU usage percentage from last poll interval
	memoryBytes          uint64
	swapBytes            uint64 // Per-VM swap usage from /proc/<pid>/status VmSwap
	allocatedMemoryBytes uint64 // VM's allocated memory from config (for memory.high calculation)
	diskVolsizeBytes     uint64 // ZFS volsize (provisioned size)
	diskBytes            uint64 // ZFS used (actual compressed bytes on disk)
	diskLogicalBytes     uint64 // ZFS logicalused (uncompressed)
	netRxBytes           uint64
	netTxBytes           uint64
	ioReadBytes          uint64
	ioWriteBytes         uint64
	priority             api.VMPriority
	cgroupApplied        bool // true after applyPriority succeeds at least once

	// Previous poll values for delta calculation
	prevCPUSeconds float64
	prevPollTime   time.Time
}

// New creates a new ResourceManager service.
func New(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	// Parse ZFS pool from storage address
	var zfsPool string
	if cfg.StorageManagerAddress != "" {
		storageURL, err := url.Parse(cfg.StorageManagerAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to parse storage address: %w", err)
		}
		if storageURL.Scheme == "zfs" {
			dataset := storageURL.Query().Get("dataset")
			if dataset != "" {
				// Extract pool name (first component of dataset path)
				zfsPool = dataset
				for i, c := range dataset {
					if c == '/' {
						zfsPool = dataset[:i]
						break
					}
				}
			}
		}
	}

	pollInterval := DefaultPollInterval
	if cfg.ResourceManagerInterval > 0 {
		pollInterval = cfg.ResourceManagerInterval
	}

	return &ResourceManager{
		config:           cfg,
		log:              log,
		machineAvailable: true,
		zfsPool:          zfsPool,
		usageState:       make(map[string]*vmUsageState),
		priorityOverride: make(map[string]api.VMPriority),
		cgroupRoot:       "/sys/fs/cgroup",
		pollInterval:     pollInterval,
	}, nil
}

// Type returns the service type.
func (m *ResourceManager) Type() services.Type {
	return services.ResourceManagerService
}

// Requires returns dependencies for this service.
func (m *ResourceManager) Requires() []services.Type {
	return []services.Type{services.ComputeService}
}

// Register registers the service with the gRPC server.
func (m *ResourceManager) Register(ctx *services.ServiceContext, server *grpc.Server) error {
	if ctx == nil {
		return fmt.Errorf("service context is required")
	}
	if ctx.MetricsRegistry == nil {
		return fmt.Errorf("metrics registry is required")
	}
	m.context = ctx
	m.metrics = newPrometheusMetrics(ctx.MetricsRegistry)
	api.RegisterResourceManagerServiceServer(server, m)

	// Register as the memory reclaimer so other services (compute)
	// can request proactive memory reclamation during live migration.
	ctx.MemoryReclaimer = m

	// Initialize metrics daemon reporter if configured
	if m.config.MetricsDaemonURL != "" {
		interval := m.config.MetricsDaemonInterval
		if interval == 0 {
			interval = config.DefaultMetricsDaemonInterval
		}
		m.metricsReporter = NewMetricsDaemonReporter(
			m.config.MetricsDaemonURL,
			m.config.Name,
			interval,
			m.log,
			m.collectMetricsFromRM,
		)
	}

	return nil
}

// Start starts the resource manager polling loop.
func (m *ResourceManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cancel != nil {
		return nil
	}

	// Initialize capacity detection
	m.capacityOnce.Do(func() {
		m.capacity = NewCapacity(m.zfsPool, m.log)
	})

	// Initialize cgroup controllers at root and slice level
	m.initControllers(ctx)

	pollCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(pollCtx)
	}()

	m.log.InfoContext(ctx, "resource manager started",
		"poll_interval", m.pollInterval,
		"zfs_pool", m.zfsPool)

	// Start metrics daemon reporter if configured
	if m.metricsReporter != nil {
		m.metricsReporter.Start(ctx)
	}

	return nil
}

// Stop stops the resource manager.
func (m *ResourceManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()

	if cancel == nil {
		return nil
	}

	// Stop metrics daemon reporter
	if m.metricsReporter != nil {
		m.metricsReporter.Stop()
	}

	cancel()
	m.wg.Wait()
	return nil
}

func (m *ResourceManager) run(ctx context.Context) {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	// Initial poll
	m.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

func (m *ResourceManager) poll(ctx context.Context) {
	if m.context == nil || m.context.ComputeService == nil {
		return
	}

	instances, err := m.context.ComputeService.Instances(ctx)
	if err != nil {
		m.log.ErrorContext(ctx, "resource manager: failed to list instances", "error", err)
		return
	}

	now := time.Now()
	seen := make(map[string]struct{}, len(instances))

	for _, inst := range instances {
		seen[inst.GetID()] = struct{}{}
		m.pollInstance(ctx, inst.GetID(), inst.GetName(), inst.GetGroupID(), inst.GetVMConfig(), inst.GetState(), now)
	}

	// Cleanup state for removed instances
	m.cleanupMissing(ctx, seen)
}

func (m *ResourceManager) pollInstance(ctx context.Context, id, name, groupID string, vmCfg interface{}, vmState computeapi.VMState, now time.Time) {
	// Collect usage metrics
	var usage *usageData
	var err error

	if vmState == computeapi.VMState_STOPPED {
		// Stopped: only collect disk (ZFS volume still exists), zero runtime metrics.
		// The VM process, cgroups, and tap device don't exist so skip collectUsage.
		usage = &usageData{}
		zfsInfo, zfsErr := m.readZFSVolumeInfo(ctx, id)
		if zfsErr != nil {
			m.log.DebugContext(ctx, "resource manager: failed to read ZFS info for stopped instance", "id", id, "error", zfsErr)
		} else if zfsInfo != nil {
			usage.diskVolsizeBytes = zfsInfo.Volsize
			usage.diskBytes = zfsInfo.Used
			usage.diskLogicalBytes = zfsInfo.LogicalUsed
		}
	} else {
		// Running, starting, paused, stopping: VM process still exists, collect full usage.
		collectFn := m.collectUsage
		if m.collectUsageFn != nil {
			collectFn = m.collectUsageFn
		}
		usage, err = collectFn(ctx, id, name, groupID)
		if err != nil {
			m.log.DebugContext(ctx, "resource manager: failed to collect usage", "id", id, "error", err)
			return
		}
	}

	m.usageMu.Lock()
	state, exists := m.usageState[id]
	groupChanged := false
	oldGroupID := ""
	if !exists {
		// Get allocated memory from VM config for memory.high calculation
		var allocatedMemory uint64
		if cfg, ok := vmCfg.(*computeapi.VMConfig); ok && cfg != nil {
			allocatedMemory = cfg.GetMemory()
		}
		state = &vmUsageState{
			name:                 name,
			groupID:              groupID,
			priority:             api.VMPriority_PRIORITY_NORMAL,
			prevPollTime:         now,
			allocatedMemoryBytes: allocatedMemory,
		}
		m.usageState[id] = state
	} else {
		// Update allocatedMemoryBytes on each poll in case VM was resized
		if cfg, ok := vmCfg.(*computeapi.VMConfig); ok && cfg != nil {
			if newMemory := cfg.GetMemory(); newMemory != state.allocatedMemoryBytes {
				state.allocatedMemoryBytes = newMemory
			}
		}
	}

	if state.groupID != groupID {
		// Group ID changed (via SetInstanceGroup), update state so cgroup moves on next applyPriority
		m.log.InfoContext(ctx, "resource manager: group ID changed", "id", id, "old_group", state.groupID, "new_group", groupID)
		oldGroupID = state.groupID
		state.groupID = groupID
		groupChanged = true
	}

	// Compute CPU percentage for Prometheus metrics
	var cpuPercent float64
	if exists && !state.prevPollTime.IsZero() {
		elapsed := now.Sub(state.prevPollTime).Seconds()
		if elapsed > 0 {
			cpuDelta := usage.cpuSeconds - state.prevCPUSeconds
			if cpuDelta > 0 {
				cpuPercent = (cpuDelta / elapsed) * 100.0
			}
		}
	}

	// Update state
	state.name = name
	state.cpuSeconds = usage.cpuSeconds
	state.cpuPercent = cpuPercent
	state.memoryBytes = usage.memoryBytes
	state.swapBytes = usage.swapBytes
	state.diskVolsizeBytes = usage.diskVolsizeBytes
	state.diskBytes = usage.diskBytes
	state.diskLogicalBytes = usage.diskLogicalBytes
	state.netRxBytes = usage.netRxBytes
	state.netTxBytes = usage.netTxBytes
	state.ioReadBytes = usage.ioReadBytes
	state.ioWriteBytes = usage.ioWriteBytes
	state.prevCPUSeconds = usage.cpuSeconds
	state.prevPollTime = now

	// Determine priority — use manual override if set, otherwise default to normal
	m.priorityMu.Lock()
	override, hasOverride := m.priorityOverride[id]
	m.priorityMu.Unlock()

	var newPriority api.VMPriority
	if hasOverride {
		newPriority = override
	} else {
		newPriority = api.VMPriority_PRIORITY_NORMAL
	}

	oldPriority := state.priority
	state.priority = newPriority
	cgroupApplied := state.cgroupApplied
	allocatedMemoryBytes := state.allocatedMemoryBytes
	stateGroupID := state.groupID
	m.usageMu.Unlock()

	// Update Prometheus metrics
	if m.metrics != nil {
		m.metrics.update(id, name, state)
	}

	// Apply priority on first observation (to create cgroup), when cgroup setup
	// hasn't succeeded yet, when priority changes, or when group changes.
	// Skip for stopped VMs since there is no process/socket to configure.
	needsApply := !exists || !cgroupApplied || oldPriority != newPriority || groupChanged
	if needsApply && vmState != computeapi.VMState_STOPPED {
		if groupChanged {
			m.log.InfoContext(ctx, "resource manager: moving VM to new group cgroup",
				"id", id,
				"name", name,
				"group_id", stateGroupID)
		} else if oldPriority != newPriority {
			m.log.InfoContext(ctx, "resource manager: priority changed",
				"id", id,
				"name", name,
				"old_priority", oldPriority,
				"new_priority", newPriority)
		} else {
			m.log.InfoContext(ctx, "resource manager: initializing cgroup for existing VM",
				"id", id,
				"name", name,
				"priority", newPriority)
		}
		applyFn := m.applyPriority
		if m.applyPriorityFn != nil {
			applyFn = m.applyPriorityFn
		}
		if err := applyFn(ctx, id, stateGroupID, newPriority, allocatedMemoryBytes); err != nil {
			m.log.WarnContext(ctx, "resource manager: failed to apply priority", "id", id, "error", err)
		} else {
			m.usageMu.Lock()
			state.cgroupApplied = true
			m.usageMu.Unlock()
			if groupChanged {
				// Clean up old cgroup after successfully moving to new group
				if err := m.removeCgroup(ctx, id, oldGroupID); err != nil {
					m.log.DebugContext(ctx, "resource manager: failed to remove old cgroup", "id", id, "old_group", oldGroupID, "error", err)
				}
			}
		}
	}
}

func (m *ResourceManager) cleanupMissing(ctx context.Context, seen map[string]struct{}) {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()

	for id, state := range m.usageState {
		if _, ok := seen[id]; !ok {
			groupID := state.groupID
			delete(m.usageState, id)
			// Clean up Prometheus metrics
			if m.metrics != nil {
				m.metrics.delete(id)
			}
			// Clean up cgroup
			if err := m.removeCgroup(ctx, id, groupID); err != nil {
				m.log.DebugContext(ctx, "resource manager: failed to remove cgroup", "id", id, "error", err)
			}
		}
	}

	m.priorityMu.Lock()
	for id := range m.priorityOverride {
		if _, ok := seen[id]; !ok {
			delete(m.priorityOverride, id)
		}
	}
	m.priorityMu.Unlock()
}
