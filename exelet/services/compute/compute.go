package compute

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"

	"google.golang.org/grpc"
	"tailscale.com/util/singleflight"

	"exe.dev/exelet/config"
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
	reconcileCtx    context.Context
	reconcileCancel context.CancelFunc
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

	return &Service{
		config:          cfg,
		mu:              &sync.Mutex{},
		log:             log,
		portAllocator:   portAllocator,
		proxyManager:    sshproxy.NewManager(ctx, cfg.DataDir, cfg.ProxyBindIP, cfg.ExepipeAddress, log),
		instanceOpLocks: make(map[string]*instanceLock),
	}, nil
}

// Register is called from the server to register with the GRPC server.
func (s *Service) Register(ctx *services.ServiceContext, server *grpc.Server) error {
	if ctx.ImageLoader == nil {
		return errors.New("compute service requires ImageLoader to be set in ServiceContext")
	}
	api.RegisterComputeServiceServer(server, s)
	s.context = ctx
	return nil
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

	instances, err := s.initServiceState(ctx)
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
	s.reconcileIPLeasesFromInstances(ctx, instances)

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

	startSucceeded = true
	return nil
}

// initServiceState loads instances, recovers processes, and initializes service
// state. Acquires s.mu for the duration to serialize with concurrent operations.
func (s *Service) initServiceState(ctx context.Context) ([]*api.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load existing instances
	instances, err := s.listInstances(ctx)
	if err != nil {
		return nil, err
	}

	// Mark SSH ports as allocated in the port allocator
	for _, i := range instances {
		if i.SSHPort > 0 {
			s.portAllocator.MarkAllocated(int(i.SSHPort))
			s.log.DebugContext(ctx, "marked port as allocated", "instance", i.ID, "port", i.SSHPort)
		}
	}

	// Apply connection limits and bandwidth limits in the background.
	// This allows the gRPC server to start faster.
	go func() {
		for _, i := range instances {
			// Skip stopped instances - they don't have TAP devices
			if i.State == api.VMState_STOPPED || i.State == api.VMState_CREATING {
				continue
			}

			if i.VMConfig != nil && i.VMConfig.NetworkInterface != nil && i.VMConfig.NetworkInterface.IP != nil {
				ipStr := i.VMConfig.NetworkInterface.IP.IPV4
				ip, _, err := net.ParseCIDR(ipStr)
				if err != nil {
					s.log.WarnContext(ctx, "failed to parse instance IP", "instance", i.ID, "ip", ipStr, "error", err)
					continue
				}
				if err := s.context.NetworkManager.ApplyConnectionLimit(ctx, ip.String()); err != nil {
					s.log.WarnContext(ctx, "failed to apply connection limit", "instance", i.ID, "ip", ip.String(), "error", err)
				}
			}
			// Apply bandwidth limit to existing TAP device
			if err := s.context.NetworkManager.ApplyBandwidthLimit(ctx, i.ID); err != nil {
				s.log.WarnContext(ctx, "failed to apply bandwidth limit", "instance", i.ID, "error", err)
			}
		}
		s.log.InfoContext(ctx, "background network limits applied", "count", len(instances))
	}()

	// Recover existing SSH proxies from disk
	// This will find existing socat processes and adopt them, or restart dead ones
	if err := s.proxyManager.RecoverProxies(ctx, instances); err != nil {
		s.log.WarnContext(ctx, "failed to recover SSH proxies", "error", err)
		// Don't fail startup, continue
	}

	// Recover existing VMM processes (cloud-hypervisor and virtiofsd)
	// This will adopt any still-running processes and clean up stale metadata
	vmm, err := vmm.NewVMM(s.config.RuntimeAddress, s.context.NetworkManager, s.config.EnableHugepages, s.log)
	if err != nil {
		return nil, err
	}
	if err := vmm.RecoverProcesses(ctx); err != nil {
		s.log.WarnContext(ctx, "failed to recover VMM processes", "error", err)
		// Don't fail startup, continue
	}

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
	s.stopLogRotation = vmm.StartLogRotation(ctx, interval, maxBytes, keepBytes)

	return instances, nil
}

// reconcileIPLeases loads current instances and releases any orphaned IPAM leases.
// Uses singleflight to deduplicate calls that arrive while a reconcile is already
// in progress. Sequential callers after completion each run a new pass.
func (s *Service) reconcileIPLeases() {
	s.reconcileGroup.Do("reconcile", func() (struct{}, error) {
		ctx := s.reconcileCtx
		instances, err := s.listInstances(ctx)
		if err != nil {
			s.log.WarnContext(ctx, "failed to list instances for IP reconciliation", "error", err)
			return struct{}{}, nil
		}
		s.reconcileIPLeasesFromInstances(ctx, instances)
		return struct{}{}, nil
	})
}

// reconcileIPLeasesFromInstances compares IPAM leases against the given instances
// and releases any orphaned leases. Skips reconciliation if any instance is in a
// transient state (CREATING/STARTING) to avoid racing with in-flight IP allocations.
func (s *Service) reconcileIPLeasesFromInstances(ctx context.Context, instances []*api.Instance) {
	// Build set of IPs that belong to known instances.
	// Skip reconciliation entirely if any instance is mid-creation/start,
	// since its IP may be allocated in IPAM but not yet persisted to config.
	validIPs := make(map[string]struct{})
	for _, inst := range instances {
		switch inst.State {
		case api.VMState_CREATING, api.VMState_STARTING:
			s.log.DebugContext(ctx, "aborting IP reconciliation: instance in transient state", "instance", inst.ID, "state", inst.State)
			return
		case api.VMState_RUNNING, api.VMState_PAUSED, api.VMState_STOPPING, api.VMState_STOPPED,
			api.VMState_ERROR, api.VMState_CREATED, api.VMState_DELETED, api.VMState_UPDATING, api.VMState_UNKNOWN:
			// IP (if any) is persisted to config; handled below.
			// STOPPED instances may retain a persisted IP after a failed DeleteInterface.
		default:
			s.log.WarnContext(ctx, "aborting IP reconciliation: unknown instance state", "instance", inst.ID, "state", inst.State)
			return
		}
		if inst.VMConfig != nil && inst.VMConfig.NetworkInterface != nil && inst.VMConfig.NetworkInterface.IP != nil {
			ipStr := inst.VMConfig.NetworkInterface.IP.IPV4
			ip, _, err := net.ParseCIDR(ipStr)
			if err != nil {
				s.log.ErrorContext(ctx, "aborting IP reconciliation: failed to parse instance IP", "instance", inst.ID, "ip", ipStr, "error", err)
				return
			}
			validIPs[ip.String()] = struct{}{}
		}
	}

	released, err := s.context.NetworkManager.ReconcileLeases(ctx, validIPs)
	if err != nil {
		s.log.WarnContext(ctx, "IP lease reconciliation failed", "error", err)
		return
	}
	if len(released) > 0 {
		s.log.InfoContext(ctx, "released orphaned IP leases", "count", len(released), "ips", released)
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
