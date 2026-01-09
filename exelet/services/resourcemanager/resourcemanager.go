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
	// DefaultIdleThreshold is the default duration after which a VM is considered idle
	DefaultIdleThreshold = 1 * time.Minute
	// DefaultPollInterval is the default polling interval for the resource manager
	DefaultPollInterval = 30 * time.Second
	// DefaultCPUIdleThresholdPercent is the CPU usage percentage below which a VM is considered idle
	// The VMM process will use some CPU even when the guest is idle (typically ~2% for virtio,
	// timers, etc.), so we use a percentage of wall-clock time rather than absolute CPU seconds.
	DefaultCPUIdleThresholdPercent = 3.0
	// DefaultNetActivityThreshold is the minimum network bytes delta to consider a VM active
	// Set high enough to ignore background traffic like ARP, DHCP renewals, etc.
	DefaultNetActivityThreshold = 10240 // 10KB
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

	// Usage tracking
	usageMu    sync.Mutex
	usageState map[string]*vmUsageState

	// Prometheus metrics
	metrics *prometheusMetrics

	// Priority management
	priorityMu       sync.Mutex
	priorityOverride map[string]api.VMPriority // manual overrides (cleared when set to auto)
	cgroupRoot       string

	// Polling
	pollInterval  time.Duration
	idleThreshold time.Duration

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
	allocatedMemoryBytes uint64 // VM's allocated memory from config (for memory.high calculation)
	diskBytes            uint64
	netRxBytes           uint64
	netTxBytes           uint64
	lastActivity         time.Time
	priority             api.VMPriority

	// Previous poll values for delta calculation
	prevCPUSeconds float64
	prevNetRxBytes uint64
	prevNetTxBytes uint64
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

	idleThreshold := DefaultIdleThreshold
	if cfg.IdleThreshold > 0 {
		idleThreshold = cfg.IdleThreshold
	}

	return &ResourceManager{
		config:           cfg,
		log:              log,
		zfsPool:          zfsPool,
		usageState:       make(map[string]*vmUsageState),
		priorityOverride: make(map[string]api.VMPriority),
		cgroupRoot:       "/sys/fs/cgroup",
		pollInterval:     pollInterval,
		idleThreshold:    idleThreshold,
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
		"idle_threshold", m.idleThreshold,
		"zfs_pool", m.zfsPool)

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
		m.pollInstance(ctx, inst.GetID(), inst.GetName(), inst.GetGroupID(), inst.GetVMConfig(), now)
	}

	// Cleanup state for removed instances
	m.cleanupMissing(ctx, seen)
}

func (m *ResourceManager) pollInstance(ctx context.Context, id, name, groupID string, vmCfg interface{}, now time.Time) {
	// Collect usage metrics
	usage, err := m.collectUsage(ctx, id, name)
	if err != nil {
		m.log.DebugContext(ctx, "resource manager: failed to collect usage", "id", id, "error", err)
		return
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
			lastActivity:         now,
			priority:             api.VMPriority_PRIORITY_NORMAL,
			prevPollTime:         now,
			allocatedMemoryBytes: allocatedMemory,
		}
		m.usageState[id] = state
	} else if state.groupID != groupID {
		// Group ID changed (via SetInstanceGroup), update state so cgroup moves on next applyPriority
		m.log.InfoContext(ctx, "resource manager: group ID changed", "id", id, "old_group", state.groupID, "new_group", groupID)
		oldGroupID = state.groupID
		state.groupID = groupID
		groupChanged = true
	}

	// Check for activity based on CPU percentage and network delta
	isActive := false
	var cpuPercent float64
	if exists && !state.prevPollTime.IsZero() {
		elapsed := now.Sub(state.prevPollTime).Seconds()
		if elapsed > 0 {
			cpuDelta := usage.cpuSeconds - state.prevCPUSeconds
			cpuPercent = (cpuDelta / elapsed) * 100.0

			netDelta := (usage.netRxBytes - state.prevNetRxBytes) + (usage.netTxBytes - state.prevNetTxBytes)

			m.log.DebugContext(ctx, "resource manager: activity check",
				"id", id,
				"cpu_seconds", usage.cpuSeconds,
				"prev_cpu_seconds", state.prevCPUSeconds,
				"cpu_delta", cpuDelta,
				"elapsed", elapsed,
				"cpu_percent", cpuPercent,
				"net_delta", netDelta)

			// VM is active if CPU usage > threshold% OR significant network activity
			if cpuPercent > DefaultCPUIdleThresholdPercent || netDelta > DefaultNetActivityThreshold {
				isActive = true
				state.lastActivity = now
			}
		}
	} else {
		// First observation - consider active
		isActive = true
		state.lastActivity = now
	}

	// Update state
	state.name = name
	state.cpuSeconds = usage.cpuSeconds
	state.cpuPercent = cpuPercent
	state.memoryBytes = usage.memoryBytes
	state.diskBytes = usage.diskBytes
	state.netRxBytes = usage.netRxBytes
	state.netTxBytes = usage.netTxBytes
	state.prevCPUSeconds = usage.cpuSeconds
	state.prevNetRxBytes = usage.netRxBytes
	state.prevNetTxBytes = usage.netTxBytes
	state.prevPollTime = now

	// Determine priority - use manual override if set, otherwise auto-detect based on activity
	m.priorityMu.Lock()
	override, hasOverride := m.priorityOverride[id]
	m.priorityMu.Unlock()

	var newPriority api.VMPriority
	if hasOverride {
		newPriority = override
	} else if now.Sub(state.lastActivity) > m.idleThreshold {
		newPriority = api.VMPriority_PRIORITY_LOW
	} else {
		newPriority = api.VMPriority_PRIORITY_NORMAL
	}

	oldPriority := state.priority
	state.priority = newPriority
	allocatedMemoryBytes := state.allocatedMemoryBytes
	stateGroupID := state.groupID
	m.usageMu.Unlock()

	// Update Prometheus metrics
	if m.metrics != nil {
		m.metrics.update(id, name, state)
	}

	// Apply priority on first observation (to create cgroup), when priority changes, or when group changes
	if !exists || oldPriority != newPriority || groupChanged {
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
				"new_priority", newPriority,
				"is_active", isActive)
		} else {
			m.log.InfoContext(ctx, "resource manager: initializing cgroup for existing VM",
				"id", id,
				"name", name,
				"priority", newPriority)
		}
		if err := m.applyPriority(ctx, id, stateGroupID, newPriority, allocatedMemoryBytes); err != nil {
			m.log.WarnContext(ctx, "resource manager: failed to apply priority", "id", id, "error", err)
		} else if groupChanged {
			// Clean up old cgroup after successfully moving to new group
			if err := m.removeCgroup(ctx, id, oldGroupID); err != nil {
				m.log.DebugContext(ctx, "resource manager: failed to remove old cgroup", "id", id, "old_group", oldGroupID, "error", err)
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
