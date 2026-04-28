package compute

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// mockNetworkManager implements network.NetworkManager for testing reconciliation.
type mockNetworkManager struct {
	mu             sync.Mutex
	reconcileCalls [][]*api.Instance
	reconcileRet   []string
	reconcileErr   error
}

func (m *mockNetworkManager) Start(ctx context.Context) error { return nil }
func (m *mockNetworkManager) Stop(ctx context.Context) error  { return nil }
func (m *mockNetworkManager) Config(ctx context.Context) any  { return nil }
func (m *mockNetworkManager) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	return nil, nil
}

func (m *mockNetworkManager) DeleteInterface(ctx context.Context, id, ip, mac string) error {
	return nil
}

func (m *mockNetworkManager) ApplyConnectionLimit(ctx context.Context, inst *api.Instance) error {
	return nil
}

func (m *mockNetworkManager) ApplyBandwidthLimit(ctx context.Context, id string) error {
	return nil
}

func (m *mockNetworkManager) ApplySourceIPFilter(ctx context.Context, inst *api.Instance) error {
	return nil
}

func (m *mockNetworkManager) ReconcileLeases(ctx context.Context, instances []*api.Instance) ([]string, error) {
	cp := make([]*api.Instance, len(instances))
	copy(cp, instances)
	m.mu.Lock()
	m.reconcileCalls = append(m.reconcileCalls, cp)
	m.mu.Unlock()
	return m.reconcileRet, m.reconcileErr
}

func (m *mockNetworkManager) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reconcileCalls)
}

func newTestService(t *testing.T) (*Service, *mockNetworkManager) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dataDir := t.TempDir()
	runtimeDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:           "test",
		DataDir:        dataDir,
		RuntimeAddress: "cloudhypervisor://" + runtimeDir,
		ProxyPortMin:   20000,
		ProxyPortMax:   30000,
	}
	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)
	nm := &mockNetworkManager{}
	computeSvc.context = &services.ServiceContext{NetworkManager: nm}
	computeSvc.reconcileCtx, computeSvc.reconcileCancel = context.WithCancel(context.Background())
	t.Cleanup(func() { computeSvc.reconcileCancel() })
	v, err := vmm.NewVMM(cfg.RuntimeAddress, nm, false, "", log)
	if err != nil {
		t.Fatalf("failed to create VMM: %v", err)
	}
	computeSvc.vmm = v
	return computeSvc, nm
}

func TestReconcileIPLeasesReleasesOrphans(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)
	nm.reconcileRet = []string{"10.42.0.5"}

	instances := []*api.Instance{
		{
			ID:    "inst-1",
			State: api.VMState_RUNNING,
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: "10.42.0.3/16"},
				},
			},
		},
		{
			ID:    "inst-2",
			State: api.VMState_RUNNING,
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: "10.42.0.4/16"},
				},
			},
		},
	}

	svc.reconcileIPLeasesFromInstances(context.Background(), instances)

	if len(nm.reconcileCalls) != 1 {
		t.Fatalf("expected 1 reconcile call, got %d", len(nm.reconcileCalls))
	}

	passed := nm.reconcileCalls[0]
	if len(passed) != 2 {
		t.Fatalf("expected 2 instances passed to ReconcileLeases, got %d", len(passed))
	}
	ids := map[string]struct{}{}
	for _, inst := range passed {
		ids[inst.ID] = struct{}{}
	}
	if _, ok := ids["inst-1"]; !ok {
		t.Error("expected inst-1 in passed instances")
	}
	if _, ok := ids["inst-2"]; !ok {
		t.Error("expected inst-2 in passed instances")
	}
}

func TestReconcileIPLeasesAbortsOnCreatingState(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	instances := []*api.Instance{
		{
			ID:    "inst-1",
			State: api.VMState_RUNNING,
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: "10.42.0.3/16"},
				},
			},
		},
		{
			ID:    "inst-2",
			State: api.VMState_CREATING,
		},
	}

	svc.reconcileIPLeasesFromInstances(context.Background(), instances)

	if len(nm.reconcileCalls) != 0 {
		t.Fatalf("expected 0 reconcile calls when CREATING instance exists, got %d", len(nm.reconcileCalls))
	}
}

func TestReconcileIPLeasesAbortsOnStartingState(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	instances := []*api.Instance{
		{
			ID:    "inst-1",
			State: api.VMState_RUNNING,
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: "10.42.0.3/16"},
				},
			},
		},
		{
			ID:    "inst-2",
			State: api.VMState_STARTING,
		},
	}

	svc.reconcileIPLeasesFromInstances(context.Background(), instances)

	if len(nm.reconcileCalls) != 0 {
		t.Fatalf("expected 0 reconcile calls when STARTING instance exists, got %d", len(nm.reconcileCalls))
	}
}

func TestReconcileIPLeasesPassesThroughBadIP(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	instances := []*api.Instance{
		{
			ID:    "inst-1",
			State: api.VMState_RUNNING,
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: "not-a-cidr"},
				},
			},
		},
	}

	svc.reconcileIPLeasesFromInstances(context.Background(), instances)

	// Instance is still passed through; the network manager handles the bad IP.
	if len(nm.reconcileCalls) != 1 {
		t.Fatalf("expected 1 reconcile call, got %d", len(nm.reconcileCalls))
	}
	if len(nm.reconcileCalls[0]) != 1 {
		t.Fatalf("expected 1 instance passed, got %d", len(nm.reconcileCalls[0]))
	}
}

func TestReconcileIPLeasesStoppedInstanceNoNetwork(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	instances := []*api.Instance{
		{
			ID:    "inst-running",
			State: api.VMState_RUNNING,
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: "10.42.0.3/16"},
				},
			},
		},
		{
			ID:       "inst-stopped",
			State:    api.VMState_STOPPED,
			VMConfig: &api.VMConfig{},
		},
	}

	svc.reconcileIPLeasesFromInstances(context.Background(), instances)

	if len(nm.reconcileCalls) != 1 {
		t.Fatalf("expected 1 reconcile call, got %d", len(nm.reconcileCalls))
	}

	passed := nm.reconcileCalls[0]
	if len(passed) != 2 {
		t.Fatalf("expected 2 instances passed to ReconcileLeases, got %d", len(passed))
	}
}

func TestReconcileIPLeasesStoppedInstanceWithPersistedIP(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	instances := []*api.Instance{
		{
			ID:    "inst-stopped-with-ip",
			State: api.VMState_STOPPED,
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: "10.42.0.9/16"},
				},
			},
		},
	}

	svc.reconcileIPLeasesFromInstances(context.Background(), instances)

	if len(nm.reconcileCalls) != 1 {
		t.Fatalf("expected 1 reconcile call, got %d", len(nm.reconcileCalls))
	}

	passed := nm.reconcileCalls[0]
	if len(passed) != 1 {
		t.Fatalf("expected 1 instance passed to ReconcileLeases, got %d", len(passed))
	}
	if passed[0].ID != "inst-stopped-with-ip" {
		t.Errorf("expected inst-stopped-with-ip, got %s", passed[0].ID)
	}
}

func TestReconcileIPLeasesEmptyInstances(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	svc.reconcileIPLeasesFromInstances(context.Background(), nil)

	if len(nm.reconcileCalls) != 1 {
		t.Fatalf("expected 1 reconcile call with empty instances, got %d", len(nm.reconcileCalls))
	}
	if len(nm.reconcileCalls[0]) != 0 {
		t.Errorf("expected 0 instances, got %d", len(nm.reconcileCalls[0]))
	}
}

func TestReconcileIPLeasesAbortsOnUnreadableConfig(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	// A readable instance config.
	inst := &api.Instance{
		ID:    "inst-ok",
		State: api.VMState_RUNNING,
		VMConfig: &api.VMConfig{
			NetworkInterface: &api.NetworkInterface{
				IP: &api.IPAddress{IPV4: "10.42.0.3/16"},
			},
		},
	}
	if err := svc.saveInstanceConfig(inst); err != nil {
		t.Fatalf("saveInstanceConfig: %v", err)
	}

	// A broken instance directory whose config.json is actually a directory,
	// so os.ReadFile returns a non-IsNotExist error. This simulates a
	// transient read failure (permission denied, I/O error, etc.).
	brokenDir := svc.getInstanceDir("inst-broken")
	if err := os.MkdirAll(filepath.Join(brokenDir, "config.json"), 0o700); err != nil {
		t.Fatalf("setup broken config: %v", err)
	}

	svc.reconcileIPLeases()

	if calls := nm.callCount(); calls != 0 {
		t.Fatalf("expected 0 ReconcileLeases calls when a config is unreadable, got %d", calls)
	}
}

func TestListInstancesSkipsRaceRemovedConfigs(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)

	// A readable instance config.
	inst := &api.Instance{
		ID:    "inst-ok",
		State: api.VMState_RUNNING,
		VMConfig: &api.VMConfig{
			NetworkInterface: &api.NetworkInterface{
				IP: &api.IPAddress{IPV4: "10.42.0.3/16"},
			},
		},
	}
	if err := svc.saveInstanceConfig(inst); err != nil {
		t.Fatalf("saveInstanceConfig: %v", err)
	}

	// An instance dir that exists but whose config.json is missing —
	// this is the exact shape of the race where filepath.Glob saw the
	// config, DeleteInstance ran os.RemoveAll, and GetInstance then
	// returns NotFound. listInstances must skip these without erroring.
	ghostDir := svc.getInstanceDir("inst-ghost")
	if err := os.MkdirAll(ghostDir, 0o700); err != nil {
		t.Fatalf("mkdir ghost: %v", err)
	}
	// Create a config.json so the glob finds it, then remove it to simulate
	// the race (the glob already happened).
	cfgPath := filepath.Join(ghostDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte{}, 0o660); err != nil {
		t.Fatalf("write ghost cfg: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil {
		t.Fatalf("remove ghost cfg: %v", err)
	}
	// Re-create the config.json path so filepath.Glob still matches it,
	// using a symlink to /nonexistent so os.ReadFile returns ENOENT.
	if err := os.Symlink("/nonexistent/config.json", cfgPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	instances, err := svc.listInstances(context.Background())
	if err != nil {
		t.Fatalf("listInstances should tolerate NotFound from racing delete, got %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "inst-ok" {
		t.Errorf("expected only inst-ok, got %+v", instances)
	}

	count, err := svc.countInstances(context.Background())
	if err != nil {
		t.Fatalf("countInstances failed: %v", err)
	}
	if count != 1 {
		t.Errorf("got %d instances, want 1", count)
	}
}

func TestReconcileIPLeasesAbortsAfterShutdown(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	// Simulate shutdown: reconcileCancel is invoked from Service.Stop.
	svc.reconcileCancel()

	svc.reconcileIPLeases()

	if calls := nm.callCount(); calls != 0 {
		t.Fatalf("expected 0 ReconcileLeases calls after shutdown, got %d", calls)
	}
}
