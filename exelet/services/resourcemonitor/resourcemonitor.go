package resourcemonitor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	"exe.dev/exelet/utils"
	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

type counterState struct {
	totalSeconds float64
	name         string
}

type networkState struct {
	tapName string
	rxBytes uint64
	txBytes uint64
	name    string
}

// ResourceMonitor provides periodic resource monitoring for VMs managed by exelet.
type ResourceMonitor struct {
	config        *config.ExeletConfig
	context       *services.ServiceContext
	log           *slog.Logger
	pollInterval  time.Duration
	now           func() time.Time
	clockTicks    float64
	runtimeScheme string
	runtimePath   string
	procRoot      string
	sysRoot       string
	zfsDataset    string

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup

	counter     *prometheus.CounterVec
	totalsMu    sync.Mutex
	totalsState map[string]*counterState

	netRxCounter *prometheus.CounterVec
	netTxCounter *prometheus.CounterVec
	netStateMu   sync.Mutex
	netState     map[string]*networkState

	diskGauge   *prometheus.GaugeVec
	diskStateMu sync.Mutex
	diskState   map[string]string // vm_id -> vm_name for cleanup

	diskPollCount int
}

// New returns a new resource monitor service.
func New(cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.RuntimeAddress == "" {
		return nil, fmt.Errorf("runtime address is required")
	}
	if cfg.ResourceMonitorInterval <= 0 {
		return nil, fmt.Errorf("resource monitor interval must be greater than zero")
	}

	runtimeURL, err := url.Parse(cfg.RuntimeAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to parse runtime address: %w", err)
	}
	if runtimeURL.Scheme != "cloudhypervisor" {
		return nil, fmt.Errorf("unsupported runtime scheme %q", runtimeURL.Scheme)
	}
	if runtimeURL.Path == "" {
		return nil, fmt.Errorf("runtime path cannot be blank for cloudhypervisor runtime")
	}

	// Parse storage address for ZFS dataset name
	var zfsDataset string
	if cfg.StorageManagerAddress != "" {
		storageURL, err := url.Parse(cfg.StorageManagerAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to parse storage address: %w", err)
		}
		if storageURL.Scheme == "zfs" {
			zfsDataset = storageURL.Query().Get("dataset")
		}
	}

	return &ResourceMonitor{
		config:        cfg,
		log:           log,
		pollInterval:  cfg.ResourceMonitorInterval,
		now:           time.Now,
		clockTicks:    100.0,
		runtimeScheme: runtimeURL.Scheme,
		runtimePath:   runtimeURL.Path,
		procRoot:      "/proc",
		sysRoot:       "/sys",
		zfsDataset:    zfsDataset,
		totalsState:   make(map[string]*counterState),
		netState:      make(map[string]*networkState),
		diskState:     make(map[string]string),
	}, nil
}

// Register is called from the server to register with the GRPC server.
func (m *ResourceMonitor) Register(ctx *services.ServiceContext, _ *grpc.Server) error {
	if ctx == nil {
		return fmt.Errorf("service context is required")
	}
	if ctx.MetricsRegistry == nil {
		return fmt.Errorf("metrics registry is required")
	}

	counter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "cpu_seconds_total",
			Help:      "Total CPU seconds consumed by the VM process.",
		},
		[]string{"vm_id", "vm_name"},
	)
	ctx.MetricsRegistry.MustRegister(counter)

	netRxCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "net_rx_bytes_total",
			Help:      "Total network bytes received by the VM.",
		},
		[]string{"vm_id", "vm_name"},
	)
	ctx.MetricsRegistry.MustRegister(netRxCounter)

	netTxCounter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "net_tx_bytes_total",
			Help:      "Total network bytes transmitted by the VM.",
		},
		[]string{"vm_id", "vm_name"},
	)
	ctx.MetricsRegistry.MustRegister(netTxCounter)

	diskGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "exelet",
			Subsystem: "vm",
			Name:      "disk_used_bytes",
			Help:      "Disk space used by the VM in bytes.",
		},
		[]string{"vm_id", "vm_name"},
	)
	ctx.MetricsRegistry.MustRegister(diskGauge)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.context = ctx
	m.counter = counter
	m.netRxCounter = netRxCounter
	m.netTxCounter = netTxCounter
	m.diskGauge = diskGauge

	return nil
}

// Type is the type of service.
func (m *ResourceMonitor) Type() services.Type {
	return services.ResourceMonitorService
}

// Requires defines what other services on which this service depends.
func (m *ResourceMonitor) Requires() []services.Type {
	return nil
}

// Start runs the service.
func (m *ResourceMonitor) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cancel != nil {
		return nil
	}
	if m.counter == nil {
		return fmt.Errorf("metrics not initialised")
	}
	if m.context == nil || m.context.ComputeService == nil {
		return fmt.Errorf("compute service is not initialised")
	}

	monitorCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(monitorCtx)
	}()

	return nil
}

func (m *ResourceMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

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

func (m *ResourceMonitor) poll(ctx context.Context) {
	instances, err := m.context.ComputeService.Instances(ctx)
	if err != nil {
		m.log.ErrorContext(ctx, "resource monitor: failed to list instances", "error", err)
		return
	}

	// Disk stats are collected less frequently since they change slowly.
	// With the default 1-minute poll interval, collect every 10 polls (~10 minutes).
	// With shorter intervals (e.g. 5s for testing), collect every poll.
	// Always poll on the first cycle (diskPollCount starts at 0).
	diskPollInterval := 10
	if m.pollInterval < 30*time.Second {
		diskPollInterval = 1
	}

	pollDisk := m.diskPollCount == 0
	m.diskPollCount++
	m.diskPollCount %= diskPollInterval

	seen := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		seen[inst.GetID()] = struct{}{}
		if inst.GetState() != api.VMState_RUNNING {
			m.forgetTotals(inst.GetID())
			m.forgetNetState(inst.GetID())
			m.forgetDiskState(inst.GetID())
			continue
		}
		if err := m.observeInstance(ctx, inst); err != nil {
			m.log.DebugContext(ctx, "resource monitor: failed to observe instance", "instance", inst.GetID(), "error", err)
			m.forgetTotals(inst.GetID())
		}
		if err := m.observeNetworkStats(ctx, inst); err != nil {
			m.log.DebugContext(ctx, "resource monitor: failed to observe network stats", "instance", inst.GetID(), "error", err)
			m.forgetNetState(inst.GetID())
		}
		if pollDisk {
			if err := m.observeDiskStats(ctx, inst); err != nil {
				m.log.DebugContext(ctx, "resource monitor: failed to observe disk stats", "instance", inst.GetID(), "error", err)
				m.forgetDiskState(inst.GetID())
			}
		}
	}

	m.cleanupMissing(seen)
}

func (m *ResourceMonitor) observeInstance(ctx context.Context, inst *api.Instance) error {
	pid, err := m.vmPID(ctx, inst.GetID())
	if err != nil {
		return fmt.Errorf("determine vm pid: %w", err)
	}

	totalTicks, err := m.readProcessTotalTicks(pid)
	if err != nil {
		return fmt.Errorf("read process stat: %w", err)
	}

	totalSeconds := float64(totalTicks) / m.clockTicks
	delta := m.recordTotal(inst.GetID(), inst.GetName(), totalSeconds)
	if delta <= 0 {
		return nil
	}

	m.counter.WithLabelValues(inst.GetID(), inst.GetName()).Add(delta)
	return nil
}

func (m *ResourceMonitor) recordTotal(id, name string, totalSeconds float64) float64 {
	m.totalsMu.Lock()
	defer m.totalsMu.Unlock()

	state, ok := m.totalsState[id]
	if !ok || state.name != name {
		if ok && m.counter != nil {
			m.counter.DeleteLabelValues(id, state.name)
		}
		m.totalsState[id] = &counterState{
			totalSeconds: totalSeconds,
			name:         name,
		}
		return totalSeconds
	}

	delta := totalSeconds - state.totalSeconds
	if delta < 0 {
		delta = totalSeconds
	}
	state.totalSeconds = totalSeconds
	state.name = name
	return delta
}

func (m *ResourceMonitor) forgetTotals(id string) {
	m.totalsMu.Lock()
	defer m.totalsMu.Unlock()
	if state, ok := m.totalsState[id]; ok {
		if m.counter != nil {
			m.counter.DeleteLabelValues(id, state.name)
		}
		delete(m.totalsState, id)
	}
}

func (m *ResourceMonitor) forgetNetState(id string) {
	m.netStateMu.Lock()
	defer m.netStateMu.Unlock()
	if state, ok := m.netState[id]; ok {
		if m.netRxCounter != nil {
			m.netRxCounter.DeleteLabelValues(id, state.name)
		}
		if m.netTxCounter != nil {
			m.netTxCounter.DeleteLabelValues(id, state.name)
		}
		delete(m.netState, id)
	}
}

func (m *ResourceMonitor) forgetDiskState(id string) {
	m.diskStateMu.Lock()
	defer m.diskStateMu.Unlock()
	if name, ok := m.diskState[id]; ok {
		if m.diskGauge != nil {
			m.diskGauge.DeleteLabelValues(id, name)
		}
		delete(m.diskState, id)
	}
}

func (m *ResourceMonitor) cleanupMissing(seen map[string]struct{}) {
	m.totalsMu.Lock()
	for id, state := range m.totalsState {
		if _, ok := seen[id]; !ok {
			if m.counter != nil {
				m.counter.DeleteLabelValues(id, state.name)
			}
			delete(m.totalsState, id)
		}
	}
	m.totalsMu.Unlock()

	m.netStateMu.Lock()
	for id, state := range m.netState {
		if _, ok := seen[id]; !ok {
			if m.netRxCounter != nil {
				m.netRxCounter.DeleteLabelValues(id, state.name)
			}
			if m.netTxCounter != nil {
				m.netTxCounter.DeleteLabelValues(id, state.name)
			}
			delete(m.netState, id)
		}
	}
	m.netStateMu.Unlock()

	m.diskStateMu.Lock()
	for id, name := range m.diskState {
		if _, ok := seen[id]; !ok {
			if m.diskGauge != nil {
				m.diskGauge.DeleteLabelValues(id, name)
			}
			delete(m.diskState, id)
		}
	}
	m.diskStateMu.Unlock()
}

func (m *ResourceMonitor) vmPID(ctx context.Context, id string) (int, error) {
	if m.runtimeScheme != "cloudhypervisor" {
		return 0, fmt.Errorf("unsupported runtime scheme %q", m.runtimeScheme)
	}

	// TODO(philip): This is the same join as "func (v *VMM) apiSocketPath(id string) string" in cloudhypervisor.go;
	// should be shared.
	socketPath := filepath.Join(m.runtimePath, id, "chh.sock")

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Use retry=false - fail fast for monitoring queries
	cl, err := client.NewCloudHypervisorClient(reqCtx, socketPath, false, m.log)
	if err != nil {
		return 0, err
	}
	defer cl.Close()

	resp, err := cl.GetVmmPingWithResponse(reqCtx)
	if err != nil {
		return 0, err
	}
	if resp.JSON200 == nil || resp.JSON200.Pid == nil {
		return 0, fmt.Errorf("cloudhypervisor did not report pid")
	}
	return int(*resp.JSON200.Pid), nil
}

func (m *ResourceMonitor) readProcessTotalTicks(pid int) (uint64, error) {
	statPath := filepath.Join(m.procRoot, strconv.Itoa(pid), "stat")
	data, err := os.ReadFile(statPath)
	if err != nil {
		return 0, err
	}
	return parseProcessTotalTicks(data)
}

func parseProcessTotalTicks(data []byte) (uint64, error) {
	closing := bytes.LastIndexByte(data, ')')
	if closing == -1 {
		return 0, fmt.Errorf("malformed stat data: missing ')'")
	}

	fields := strings.Fields(strings.TrimSpace(string(data[closing+1:])))
	const requiredFields = 14
	if len(fields) < requiredFields {
		return 0, fmt.Errorf("malformed stat data: insufficient fields")
	}

	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse utime: %w", err)
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse stime: %w", err)
	}

	return utime + stime, nil
}

// Stop stops the service.
func (m *ResourceMonitor) Stop(ctx context.Context) error {
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

// observeNetworkStats reads network TX/RX bytes from the tap device.
// The tap device name is derived from the VM ID using the same logic as network/nat.
// Note: tap TX = VM RX and tap RX = VM TX (perspective inversion).
func (m *ResourceMonitor) observeNetworkStats(ctx context.Context, inst *api.Instance) error {
	tapName := m.getOrCacheTapName(inst.GetID())

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Read RX bytes from tap (this is TX from VM perspective)
	tapRxBytes, err := m.readNetStat(readCtx, tapName, "rx_bytes")
	if err != nil {
		return fmt.Errorf("read tap rx_bytes: %w", err)
	}

	// Read TX bytes from tap (this is RX from VM perspective)
	tapTxBytes, err := m.readNetStat(readCtx, tapName, "tx_bytes")
	if err != nil {
		return fmt.Errorf("read tap tx_bytes: %w", err)
	}

	// Invert perspective: tap RX = VM TX, tap TX = VM RX
	vmRxBytes := tapTxBytes
	vmTxBytes := tapRxBytes

	rxDelta, txDelta := m.recordNetStats(inst.GetID(), inst.GetName(), tapName, vmRxBytes, vmTxBytes)

	if rxDelta > 0 {
		m.netRxCounter.WithLabelValues(inst.GetID(), inst.GetName()).Add(float64(rxDelta))
	}
	if txDelta > 0 {
		m.netTxCounter.WithLabelValues(inst.GetID(), inst.GetName()).Add(float64(txDelta))
	}

	return nil
}

// getOrCacheTapName returns the cached tap name for the VM ID, or computes and caches it.
func (m *ResourceMonitor) getOrCacheTapName(id string) string {
	m.netStateMu.Lock()
	defer m.netStateMu.Unlock()

	if state, ok := m.netState[id]; ok && state.tapName != "" {
		return state.tapName
	}
	return utils.GetTapName(id)
}

func (m *ResourceMonitor) readNetStat(ctx context.Context, ifaceName, stat string) (uint64, error) {
	statPath := filepath.Join(m.sysRoot, "class", "net", ifaceName, "statistics", stat)

	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := os.ReadFile(statPath)
		ch <- result{data, err}
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return 0, r.err
		}
		return strconv.ParseUint(strings.TrimSpace(string(r.data)), 10, 64)
	}
}

func (m *ResourceMonitor) recordNetStats(id, name, tapName string, rxBytes, txBytes uint64) (rxDelta, txDelta uint64) {
	m.netStateMu.Lock()
	defer m.netStateMu.Unlock()

	state, ok := m.netState[id]
	if !ok || state.name != name {
		// First observation or name changed - delete old metric labels if name changed
		if ok && state.name != name {
			if m.netRxCounter != nil {
				m.netRxCounter.DeleteLabelValues(id, state.name)
			}
			if m.netTxCounter != nil {
				m.netTxCounter.DeleteLabelValues(id, state.name)
			}
		}
		m.netState[id] = &networkState{
			tapName: tapName,
			rxBytes: rxBytes,
			txBytes: txBytes,
			name:    name,
		}
		return rxBytes, txBytes
	}

	// Calculate deltas
	if rxBytes >= state.rxBytes {
		rxDelta = rxBytes - state.rxBytes
	} else {
		// Counter wrapped or reset
		rxDelta = rxBytes
	}
	if txBytes >= state.txBytes {
		txDelta = txBytes - state.txBytes
	} else {
		// Counter wrapped or reset
		txDelta = txBytes
	}

	state.rxBytes = rxBytes
	state.txBytes = txBytes
	state.name = name
	return rxDelta, txDelta
}

// observeDiskStats reads disk usage from ZFS for the VM's volume.
func (m *ResourceMonitor) observeDiskStats(ctx context.Context, inst *api.Instance) error {
	if m.zfsDataset == "" {
		return nil // ZFS not configured
	}

	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	dsName := filepath.Join(m.zfsDataset, inst.GetID())
	usedBytes, err := m.readZFSUsed(readCtx, dsName)
	if err != nil {
		return fmt.Errorf("read zfs used: %w", err)
	}

	m.recordDiskState(inst.GetID(), inst.GetName())
	m.diskGauge.WithLabelValues(inst.GetID(), inst.GetName()).Set(float64(usedBytes))

	return nil
}

// readZFSUsed reads the "used" property from a ZFS dataset.
func (m *ResourceMonitor) readZFSUsed(ctx context.Context, dsName string) (uint64, error) {
	cmd := exec.CommandContext(ctx, "zfs", "get", "-Hp", "-o", "value", "used", dsName)
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
}

func (m *ResourceMonitor) recordDiskState(id, name string) {
	m.diskStateMu.Lock()
	defer m.diskStateMu.Unlock()

	oldName, ok := m.diskState[id]
	if ok && oldName != name {
		// Name changed - delete old metric labels
		if m.diskGauge != nil {
			m.diskGauge.DeleteLabelValues(id, oldName)
		}
	}
	m.diskState[id] = name
}
