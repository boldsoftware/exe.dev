package compute

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// mockNetworkManager implements network.NetworkManager for testing reconciliation.
type mockNetworkManager struct {
	mu               sync.Mutex
	reconcileCalls   []map[string]struct{}
	reconcileRet     []string
	reconcileErr     error
	reconcileBlock   chan struct{} // if non-nil, ReconcileLeases blocks until closed
	reconcileEntered chan struct{} // if non-nil, signalled when ReconcileLeases is entered
}

func (m *mockNetworkManager) Start(ctx context.Context) error { return nil }
func (m *mockNetworkManager) Stop(ctx context.Context) error  { return nil }
func (m *mockNetworkManager) Config(ctx context.Context) any  { return nil }
func (m *mockNetworkManager) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	return nil, nil
}
func (m *mockNetworkManager) DeleteInterface(ctx context.Context, id, ip string) error { return nil }
func (m *mockNetworkManager) ApplyConnectionLimit(ctx context.Context, ip string) error {
	return nil
}

func (m *mockNetworkManager) ApplyBandwidthLimit(ctx context.Context, id string) error {
	return nil
}

func (m *mockNetworkManager) ReconcileLeases(ctx context.Context, validIPs map[string]struct{}) ([]string, error) {
	if m.reconcileEntered != nil {
		select {
		case m.reconcileEntered <- struct{}{}:
		default:
		}
	}
	if m.reconcileBlock != nil {
		<-m.reconcileBlock
	}
	cp := make(map[string]struct{}, len(validIPs))
	for k, v := range validIPs {
		cp[k] = v
	}
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
	cfg := &config.ExeletConfig{
		Name:         "test",
		DataDir:      dataDir,
		ProxyPortMin: 20000,
		ProxyPortMax: 30000,
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

	validIPs := nm.reconcileCalls[0]
	if _, ok := validIPs["10.42.0.3"]; !ok {
		t.Error("expected 10.42.0.3 in valid IPs")
	}
	if _, ok := validIPs["10.42.0.4"]; !ok {
		t.Error("expected 10.42.0.4 in valid IPs")
	}
	if len(validIPs) != 2 {
		t.Errorf("expected 2 valid IPs, got %d", len(validIPs))
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

func TestReconcileIPLeasesAbortsOnParseError(t *testing.T) {
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

	if len(nm.reconcileCalls) != 0 {
		t.Fatalf("expected 0 reconcile calls on parse error, got %d", len(nm.reconcileCalls))
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

	validIPs := nm.reconcileCalls[0]
	if len(validIPs) != 1 {
		t.Errorf("expected 1 valid IP (stopped instance has no network), got %d", len(validIPs))
	}
	if _, ok := validIPs["10.42.0.3"]; !ok {
		t.Error("expected 10.42.0.3 in valid IPs")
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

	validIPs := nm.reconcileCalls[0]
	if _, ok := validIPs["10.42.0.9"]; !ok {
		t.Error("stopped instance's persisted IP must be in validIPs to prevent release")
	}
	if len(validIPs) != 1 {
		t.Errorf("expected 1 valid IP, got %d", len(validIPs))
	}
}

func TestReconcileIPLeasesEmptyInstances(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	svc.reconcileIPLeasesFromInstances(context.Background(), nil)

	if len(nm.reconcileCalls) != 1 {
		t.Fatalf("expected 1 reconcile call with empty valid IPs, got %d", len(nm.reconcileCalls))
	}
	if len(nm.reconcileCalls[0]) != 0 {
		t.Errorf("expected 0 valid IPs, got %d", len(nm.reconcileCalls[0]))
	}
}

func TestReconcileIPLeasesSingleflightDedup(t *testing.T) {
	t.Parallel()
	svc, nm := newTestService(t)

	// Block ReconcileLeases until we've confirmed the first goroutine has entered.
	// Once inside Do, the singleflight key is set and all other callers coalesce.
	nm.reconcileBlock = make(chan struct{})
	nm.reconcileEntered = make(chan struct{}, 1)

	// Write an instance config to disk so listInstances finds something
	inst := &api.Instance{
		ID:    "inst-1",
		State: api.VMState_RUNNING,
		VMConfig: &api.VMConfig{
			NetworkInterface: &api.NetworkInterface{
				IP: &api.IPAddress{IPV4: "10.42.0.3/16"},
			},
		},
	}
	if err := svc.saveInstanceConfig(inst); err != nil {
		t.Fatalf("failed to save instance config: %v", err)
	}

	// Launch goroutines that all call reconcileIPLeases concurrently.
	// The first enters ReconcileLeases and blocks; the rest queue on singleflight.
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			svc.reconcileIPLeases()
		}()
	}
	close(ready)

	// Wait for the first goroutine to enter ReconcileLeases inside Do.
	// At that point the singleflight key is set and all other callers coalesce.
	<-nm.reconcileEntered

	// Unblock the mock — all waiters return from the single flight.
	close(nm.reconcileBlock)
	wg.Wait()

	// Verify dedup occurred: 10 concurrent callers should produce significantly
	// fewer than 10 ReconcileLeases calls. We allow up to 2 since a goroutine
	// may be scheduled after the first flight completes, starting a second flight.
	if calls := nm.callCount(); calls == 0 || calls > 2 {
		t.Fatalf("expected singleflight dedup (1-2 calls for 10 goroutines), got %d", calls)
	}
}
