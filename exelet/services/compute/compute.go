package compute

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"tailscale.com/util/singleflight"

	"exe.dev/exelet/config"
	"exe.dev/exelet/network"
	"exe.dev/exelet/services"
	"exe.dev/exelet/sshproxy"
	"exe.dev/exelet/vmm"
	api "exe.dev/pkg/api/exe/compute/v1"
)

const (
	instanceDataDir = "instances"
)

type Service struct {
	api.UnimplementedComputeServiceServer
	config              *config.ExeletConfig
	context             *services.ServiceContext
	vmm                 vmm.VMM
	mu                  *sync.Mutex
	log                 *slog.Logger
	portAllocator       *PortAllocator
	proxyManager        sshproxy.Manager
	instanceCreateGroup singleflight.Group[string, *api.Instance]
	instanceDeleteGroup singleflight.Group[string, *api.DeleteInstanceResponse]
	stopLogRotation     func()
	migratingInstances  sync.Map                 // map[instanceID]struct{} - instances currently being migrated
	instanceOpMu        sync.Mutex               // protects instanceOpLocks
	instanceOpLocks     map[string]*instanceLock // per-instance operation lock
	reconcileGroup      singleflight.Group[string, struct{}]
	// reconcileCtx is stored in the struct (rather than passed per-call) because
	// background reconcile goroutines outlive the gRPC request that triggers them.
	// Cancelled in Stop to unblock stuck IPAM writes during shutdown.
	reconcileCtx          context.Context
	reconcileCancel       context.CancelFunc
	tierMigrationSem      chan struct{}   // semaphore limiting concurrent tier migrations
	tierMigrationFailures []time.Time     // timestamps of recent migration failures
	tierMigrationDisabled bool            // true if circuit breaker tripped
	tierMigrationMu       sync.Mutex      // protects tierMigrationFailures and tierMigrationDisabled
	tierMigrationWg       sync.WaitGroup  // tracks in-flight migration goroutines
	tierMigrationCtx      context.Context // parent context for all migrations; cancelled on Stop
	tierMigrationCancel   context.CancelFunc

	// sidebandFaultAfterBytes, when > 0, causes sideband TCP connections to
	// be closed after this many bytes are copied. Used by e1e tests to
	// exercise resumable transfer without iptables. Resets to 0 after firing.
	// sidebandFaultSkipCount, when > 0, causes that many sideband connections
	// to pass through unfaulted before the fault fires.
	// sidebandFaultKillGRPC, when true, also kills the gRPC stream to the
	// target when the sideband fault fires, simulating a full network outage.
	sidebandFaultAfterBytes atomic.Int64
	sidebandFaultSkipCount  atomic.Int64
	sidebandFaultKillGRPC   atomic.Bool

	// receiveFaultCrashAfterData, when true, causes ReceiveVM to return an
	// error after ZFS data is received but before config is saved, WITHOUT
	// triggering rollback — simulating an exelet crash. Fires once.
	receiveFaultCrashAfterData atomic.Bool

	receiveVMSessions *receiveVMSessionManager
	sendVMSessions    *sendVMSessionManager
}

func newProxyManager(ctx context.Context, cfg *config.ExeletConfig, log *slog.Logger) sshproxy.Manager {
	var opts sshproxy.ProxyOpts
	if cfg.ProxyBindDevFunc != nil {
		opts.BindDev = sshproxy.BindDevFunc(cfg.ProxyBindDevFunc)
	}
	if cfg.ProxyNetnsFunc != nil {
		opts.NetnsFunc = sshproxy.NetnsFunc(cfg.ProxyNetnsFunc)
	}
	return sshproxy.NewManager(ctx, cfg.DataDir, cfg.ProxyBindIP, cfg.ExepipeAddress, log, opts)
}

// New returns a new service.
func New(ctx context.Context, cfg *config.ExeletConfig, log *slog.Logger) (services.Service, error) {
	// Use configured port range, or defaults if not set
	minPort := cfg.ProxyPortMin
	maxPort := cfg.ProxyPortMax
	var portAllocator *PortAllocator
	if minPort > 0 && maxPort > 0 {
		portAllocator = NewPortAllocatorWithRange(minPort, maxPort)
	} else {
		portAllocator = NewPortAllocator()
	}

	migrationWorkers := cfg.StorageTierMigrationWorkers
	if migrationWorkers <= 0 {
		migrationWorkers = config.DefaultStorageTierMigrationWorkers
	}

	svc := &Service{
		config:           cfg,
		mu:               &sync.Mutex{},
		log:              log,
		portAllocator:    portAllocator,
		proxyManager:     newProxyManager(ctx, cfg, log),
		instanceOpLocks:  make(map[string]*instanceLock),
		tierMigrationSem: make(chan struct{}, migrationWorkers),
	}
	svc.receiveVMSessions = newReceiveVMSessionManager(log, svc)
	svc.sendVMSessions = newSendVMSessionManager(log, svc)
	return svc, nil
}

// Register is called from the server to register with the GRPC server.
// VMM is created here (not in Start) because other services may call into
// compute methods concurrently once services start, and s.vmm must be
// non-nil before that happens.
func (s *Service) Register(ctx *services.ServiceContext, server *grpc.Server) error {
	if ctx.ImageLoader == nil {
		return errors.New("compute service requires ImageLoader to be set in ServiceContext")
	}
	v, err := vmm.NewVMM(s.config.RuntimeAddress, ctx.NetworkManager, s.config.EnableHugepages, s.config.InstanceDomain, s.log)
	if err != nil {
		return fmt.Errorf("failed to create VMM: %w", err)
	}
	s.vmm = v
	api.RegisterComputeServiceServer(server, s)
	s.context = ctx
	s.tierMigrationCtx, s.tierMigrationCancel = context.WithCancel(context.Background())
	return nil
}

// RegisterTestHandlers registers HTTP handlers for test fault injection.
// These are only used by e1e tests to simulate failures.
func (s *Service) RegisterTestHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /debug/fault/sideband-disconnect", func(w http.ResponseWriter, r *http.Request) {
		afterBytes, err := strconv.ParseInt(r.FormValue("after_bytes"), 10, 64)
		if err != nil || afterBytes <= 0 {
			http.Error(w, "after_bytes must be a positive integer", http.StatusBadRequest)
			return
		}
		var skipCount int64
		if v := r.FormValue("skip_count"); v != "" {
			skipCount, _ = strconv.ParseInt(v, 10, 64)
		}
		killGRPC := r.FormValue("kill_grpc") == "true"
		s.sidebandFaultSkipCount.Store(skipCount)
		s.sidebandFaultAfterBytes.Store(afterBytes)
		s.sidebandFaultKillGRPC.Store(killGRPC)
		s.log.Warn("fault injection: sideband disconnect armed", "after_bytes", afterBytes, "skip_count", skipCount, "kill_grpc", killGRPC)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "armed: disconnect after %d bytes (skip %d connections, kill_grpc=%v)\n", afterBytes, skipCount, killGRPC)
	})
	mux.HandleFunc("POST /debug/fault/receive-crash-after-data", func(w http.ResponseWriter, r *http.Request) {
		s.receiveFaultCrashAfterData.Store(true)
		s.log.Warn("fault injection: receive crash-after-data armed")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "armed: will crash after ZFS data received")
	})
}

// Type is the type of service.
func (s *Service) Type() services.Type {
	return services.ComputeService
}

// Requires defines what other services on which this service depends.
func (s *Service) Requires() []services.Type {
	return nil
}

// Start runs the service.
func (s *Service) Start(ctx context.Context) error {
	s.reconcileCtx, s.reconcileCancel = context.WithCancel(context.Background())

	instances, listErr, err := s.initServiceState(ctx)
	if err != nil {
		s.reconcileCancel()
		return err
	}

	// Ensure log rotation is stopped if Start fails after initServiceState succeeds
	startSucceeded := false
	defer func() {
		if !startSucceeded && s.stopLogRotation != nil {
			s.stopLogRotation()
			s.stopLogRotation = nil
		}
	}()

	// Reconcile IPAM leases against known instances to release any orphaned IPs
	// from previous crashes, failed migrations, or incomplete deletions.
	// Runs outside s.mu since it only touches NetworkManager/IPAM (their own locks).
	// Must run before startInstance loop below — starting instances allocates new
	// IPs, which would be seen as orphans if reconciliation ran after.
	// Uses the Start ctx directly (not s.reconcileCtx) since this is synchronous
	// and should respect the startup context's lifecycle.
	//
	// Skip reconciliation if the instance list is known to be partial:
	// reconciling against a partial list would release leases for VMs
	// whose configs we simply failed to read, and those IPs could then
	// be reassigned — producing duplicate-IP conflicts.
	if listErr != nil {
		s.log.ErrorContext(ctx, "skipping IPAM reconciliation: instance list is incomplete", "error", listErr)
	} else {
		s.reconcileIPLeasesFromInstances(ctx, instances)
	}

	// start stopped instances if enabled
	if s.config.EnableInstanceBootOnStartup {
		s.log.InfoContext(ctx, "booting stopped instances")
		for _, i := range instances {
			if i.State == api.VMState_STOPPED {
				if err := s.startInstance(ctx, i.ID); err != nil {
					return err
				}
			}
		}
	}

	if s.receiveVMSessions != nil {
		go s.receiveVMSessions.janitor(s.reconcileCtx)
	}
	if s.sendVMSessions != nil {
		go s.sendVMSessions.janitor(s.reconcileCtx)
	}

	startSucceeded = true
	return nil
}

// initServiceState loads instances, recovers processes, and initializes service
// state. Acquires s.mu for the duration to serialize with concurrent operations.
//
// Returns (instances, listErr, fatalErr):
//   - fatalErr: unrecoverable — Start should abort.
//   - listErr: some instance configs failed to load; `instances` is a
//     partial list. Startup should continue (keep gRPC up, log the error)
//     but callers MUST NOT reconcile durable state (IPAM) against a partial
//     list — doing so releases leases for VMs whose configs temporarily
//     failed to read, causing duplicate-IP conflicts when those IPs get
//     reassigned.
func (s *Service) initServiceState(ctx context.Context) ([]*api.Instance, error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load existing instances. A listErr with a non-nil `instances` slice
	// is a partial-load signal: startup continues on the partial list,
	// but IPAM reconciliation is skipped by the caller.
	instances, listErr := s.listInstances(ctx)
	if listErr != nil && instances == nil {
		return nil, nil, listErr
	}
	if listErr != nil {
		s.log.ErrorContext(ctx, "failed to load some instance configs; continuing startup with partial list", "error", listErr)
	}

	// Mark SSH ports as allocated in the port allocator
	for _, i := range instances {
		if i.SSHPort > 0 {
			s.portAllocator.MarkAllocated(int(i.SSHPort))
			s.log.DebugContext(ctx, "marked port as allocated", "instance", i.ID, "port", i.SSHPort)
		}
	}

	// Recover ext IP mappings (netns mode). Must happen before the metadata
	// service starts handling requests so GetInstanceByExtIP can identify VMs.
	if recoverer, ok := s.context.NetworkManager.(network.ExtIPRecoverer); ok {
		var runningIDs []string
		for _, i := range instances {
			if i.State == api.VMState_RUNNING || i.State == api.VMState_STARTING {
				runningIDs = append(runningIDs, i.ID)
			}
		}
		if err := recoverer.RecoverExtIPs(ctx, runningIDs); err != nil {
			s.log.WarnContext(ctx, "failed to recover ext IPs", "error", err)
		}
	}

	// Apply connection limits, bandwidth limits, and recover proxies/processes
	// in the background. These are all defensive measures — existing VMs and
	// their proxies continue running across exelet restarts. Doing this in the
	// background lets the gRPC server start immediately instead of blocking
	// for minutes at scale (800 VMs × ~700ms per bandwidth setup = ~9 min).
	go s.applyStartupNetworkLimits(ctx, instances)
	go func() {
		if err := s.proxyManager.RecoverProxies(ctx, instances); err != nil {
			s.log.WarnContext(ctx, "failed to recover SSH proxies", "error", err)
		}
	}()
	go func() {
		if err := s.vmm.RecoverProcesses(ctx); err != nil {
			s.log.WarnContext(ctx, "failed to recover VMM processes", "error", err)
		}
	}()

	// Start boot log rotation
	interval := s.config.BootLogRotationInterval
	if interval == 0 {
		interval = config.DefaultBootLogRotationInterval
	}
	maxBytes := s.config.BootLogMaxBytes
	if maxBytes == 0 {
		maxBytes = config.DefaultBootLogMaxBytes
	}
	keepBytes := s.config.BootLogKeepBytes
	if keepBytes == 0 {
		keepBytes = config.DefaultBootLogKeepBytes
	}
	s.stopLogRotation = s.vmm.StartLogRotation(ctx, interval, maxBytes, keepBytes)

	return instances, listErr, nil
}

// applyStartupNetworkLimits applies connection limits and bandwidth limits to
// all running VMs. Runs serially to minimize contention on the kernel's RTNL
// lock and xtables lock. Each iptables/tc/ip-link invocation acquires these
// global locks, and running them in parallel causes sustained lock contention
// that stalls other kernel subsystems (e.g. node_exporter reading /proc/net/dev
// hangs for 30-45s, causing false "host down" alerts from Prometheus).
//
// In practice this is fast: bandwidth setup checks if the IFB is already
// configured (kernel state survives exelet restarts) and skips the ~8 netlink
// operations when nothing changed. Connection limit checks use iptables -C.
func (s *Service) applyStartupNetworkLimits(ctx context.Context, instances []*api.Instance) {
	for _, inst := range instances {
		if inst.State == api.VMState_STOPPED || inst.State == api.VMState_CREATING {
			continue
		}

		if err := s.context.NetworkManager.ApplyConnectionLimit(ctx, inst); err != nil {
			s.log.WarnContext(ctx, "failed to apply connection limit", "instance", inst.ID, "error", err)
		}
		if err := s.context.NetworkManager.ApplyBandwidthLimit(ctx, inst.ID); err != nil {
			s.log.WarnContext(ctx, "failed to apply bandwidth limit", "instance", inst.ID, "error", err)
		}
	}

	s.log.InfoContext(ctx, "background network limits applied", "count", len(instances))
}

// reconcileIPLeases loads current instances and releases any orphaned IPAM leases.
// Uses singleflight to deduplicate calls that arrive while a reconcile is already
// in progress. Sequential callers after completion each run a new pass.
func (s *Service) reconcileIPLeases() {
	s.reconcileGroup.Do("reconcile", func() (struct{}, error) {
		ctx := s.reconcileCtx
		// Skip if shutdown is in progress: filesystem reads become unreliable
		// as other services stop, and reconciliation is destructive.
		if ctx.Err() != nil {
			return struct{}{}, nil
		}
		instances, err := s.listInstances(ctx)
		if err != nil {
			// Partial or failed load. Skip reconciliation: acting on a
			// partial list can release leases for VMs whose configs
			// failed to read, causing duplicate-IP conflicts.
			s.log.ErrorContext(ctx, "skipping IPAM reconciliation: failed to list instances", "error", err)
			return struct{}{}, nil
		}
		s.reconcileIPLeasesFromInstances(ctx, instances)
		return struct{}{}, nil
	})
}

// reconcileIPLeasesFromInstances compares network resources against the given
// instances and releases any orphans. For the NAT manager this means IPAM leases;
// for the netns manager this means kernel namespaces, bridges, and veth pairs.
// Skips reconciliation if any instance is in a transient state (CREATING/STARTING)
// to avoid racing with in-flight allocations.
func (s *Service) reconcileIPLeasesFromInstances(ctx context.Context, instances []*api.Instance) {
	// Skip reconciliation entirely if any instance is mid-creation/start,
	// since its resources may be allocated but not yet persisted to config.
	for _, inst := range instances {
		switch inst.State {
		case api.VMState_CREATING, api.VMState_STARTING:
			s.log.DebugContext(ctx, "aborting network reconciliation: instance in transient state", "instance", inst.ID, "state", inst.State)
			return
		case api.VMState_RUNNING, api.VMState_PAUSED, api.VMState_STOPPING, api.VMState_STOPPED,
			api.VMState_ERROR, api.VMState_CREATED, api.VMState_DELETED, api.VMState_UPDATING, api.VMState_UNKNOWN:
			// OK — resources are persisted to config.
		default:
			s.log.WarnContext(ctx, "aborting network reconciliation: unknown instance state", "instance", inst.ID, "state", inst.State)
			return
		}
	}

	released, err := s.context.NetworkManager.ReconcileLeases(ctx, instances)
	if err != nil {
		s.log.WarnContext(ctx, "network reconciliation failed", "error", err)
		return
	}
	if len(released) > 0 {
		s.log.InfoContext(ctx, "released orphaned network resources", "count", len(released), "released", released)
	}
}

// Stop stops the service.
func (s *Service) Stop(ctx context.Context) error {
	// NOTE: We intentionally do NOT stop SSH proxies here.
	// Socat processes run in their own process group and survive exelet restarts.
	// On next startup, RecoverProxies will adopt still-running proxies seamlessly,
	// avoiding any SSH connectivity gap during restarts.

	if s.reconcileCancel != nil {
		s.reconcileCancel()
	}

	// Signal in-flight tier migrations to stop accepting new work, then
	// wait for any that are past the point-of-no-return to finish.
	if s.tierMigrationCancel != nil {
		s.tierMigrationCancel()
	}
	migDone := make(chan struct{})
	go func() {
		s.tierMigrationWg.Wait()
		close(migDone)
	}()
	select {
	case <-migDone:
	case <-ctx.Done():
		s.log.WarnContext(ctx, "shutdown context expired while waiting for tier migrations to drain")
	}

	if s.receiveVMSessions != nil {
		s.receiveVMSessions.abortAll()
	}
	if s.sendVMSessions != nil {
		s.sendVMSessions.abortAll()
	}

	if s.stopLogRotation != nil {
		s.stopLogRotation()
	}

	return nil
}

// lockForMigration marks an instance as migrating, preventing lifecycle operations.
// It briefly acquires the per-instance lock to ensure atomicity with checkNotMigrating
// (which is called under the same lock by lifecycle ops). The per-instance lock is
// released after setting the flag so lifecycle ops fail fast rather than blocking
// for the entire migration.
func (s *Service) lockForMigration(id string) error {
	unlock := s.lockInstance(id)
	defer unlock()
	if _, loaded := s.migratingInstances.LoadOrStore(id, struct{}{}); loaded {
		return api.ErrMigrating
	}
	return nil
}

// unlockMigration unlocks an instance after migration completes or fails.
func (s *Service) unlockMigration(id string) {
	s.migratingInstances.Delete(id)
}

// checkNotMigrating returns an error if the instance is currently being migrated.
// Must be called while holding the per-instance lock (via lockInstance) to prevent
// TOCTOU races with lockForMigration.
func (s *Service) checkNotMigrating(id string) error {
	if _, ok := s.migratingInstances.Load(id); ok {
		return api.ErrMigrating
	}
	return nil
}

// instanceLock is a refcounted mutex. When the last waiter releases,
// the entry is removed from instanceOpLocks to prevent unbounded growth.
type instanceLock struct {
	mu   sync.Mutex
	refs int // protected by s.instanceOpMu
}

// lockInstance acquires a per-instance mutex to serialize lifecycle operations
// (start, stop, delete) on the same instance. Returns a function to release the lock.
func (s *Service) lockInstance(id string) func() {
	s.instanceOpMu.Lock()
	il, ok := s.instanceOpLocks[id]
	if !ok {
		il = &instanceLock{}
		s.instanceOpLocks[id] = il
	}
	il.refs++
	s.instanceOpMu.Unlock()

	il.mu.Lock()
	return func() {
		il.mu.Unlock()

		s.instanceOpMu.Lock()
		il.refs--
		if il.refs == 0 {
			delete(s.instanceOpLocks, id)
		}
		s.instanceOpMu.Unlock()
	}
}
