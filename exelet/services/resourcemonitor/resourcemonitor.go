package resourcemonitor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	"exe.dev/exelet/vmm/cloudhypervisor/client"
	api "exe.dev/pkg/api/exe/compute/v1"
)

type counterState struct {
	totalSeconds float64
	name         string
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

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup

	counter     *prometheus.CounterVec
	totalsMu    sync.Mutex
	totalsState map[string]*counterState
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

	return &ResourceMonitor{
		config:        cfg,
		log:           log,
		pollInterval:  cfg.ResourceMonitorInterval,
		now:           time.Now,
		clockTicks:    100.0,
		runtimeScheme: runtimeURL.Scheme,
		runtimePath:   runtimeURL.Path,
		procRoot:      "/proc",
		totalsState:   make(map[string]*counterState),
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

	m.mu.Lock()
	defer m.mu.Unlock()
	m.context = ctx
	m.counter = counter

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

	seen := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		seen[inst.GetID()] = struct{}{}
		if inst.GetState() != api.VMState_RUNNING {
			m.forgetTotals(inst.GetID())
			continue
		}
		if err := m.observeInstance(ctx, inst); err != nil {
			m.log.DebugContext(ctx, "resource monitor: failed to observe instance", "instance", inst.GetID(), "error", err)
			m.forgetTotals(inst.GetID())
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
}

func (m *ResourceMonitor) vmPID(ctx context.Context, id string) (int, error) {
	if m.runtimeScheme != "cloudhypervisor" {
		return 0, fmt.Errorf("unsupported runtime scheme %q", m.runtimeScheme)
	}

	// TODO(philip): This is the same join as "func (v *VMM) apiSocketPath(id string) string" in cloudhypervisor.go;
	// should be shared.
	socketPath := filepath.Join(m.runtimePath, id, "chh.sock")
	cl, err := client.NewCloudHypervisorClient(socketPath, m.log)
	if err != nil {
		return 0, err
	}
	defer cl.Close()

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

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
