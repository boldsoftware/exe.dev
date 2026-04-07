package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"exe.dev/exelet/client"
	api "exe.dev/pkg/api/exe/compute/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type exeletTarget struct {
	addr   string
	client *client.Client
}

type instanceInfo struct {
	id          string
	currentPool string
	exeletAddr  string
}

type activeOp struct {
	OperationID string
	InstanceID  string
	Exelet      string
	SourcePool  string
	TargetPool  string
	Progress    float32
	StartedAt   time.Time
}

type Migrator struct {
	targets       []exeletTarget
	pools         []string
	live          bool
	cooldown      time.Duration
	maxMigrations int

	collector *ReportCollector

	mu        sync.Mutex
	inventory []instanceInfo
	activeOps map[string]*activeOp // operation_id -> op
}

func NewMigrator(targets []exeletTarget, pools []string, live bool, cooldown time.Duration, maxMigrations int, collector *ReportCollector) *Migrator {
	return &Migrator{
		targets:       targets,
		pools:         pools,
		live:          live,
		cooldown:      cooldown,
		maxMigrations: maxMigrations,
		collector:     collector,
		activeOps:     make(map[string]*activeOp),
	}
}

func (m *Migrator) BuildInventory(ctx context.Context) error {
	var allInstances []instanceInfo

	for _, t := range m.targets {
		tiers, err := t.client.ListStorageTiers(ctx, &api.ListStorageTiersRequest{})
		if err != nil {
			return fmt.Errorf("list tiers on %s: %w", t.addr, err)
		}

		tierNames := make(map[string]bool)
		for _, tier := range tiers.Tiers {
			tierNames[tier.Name] = true
		}

		for _, pool := range m.pools {
			if !tierNames[pool] {
				return fmt.Errorf("pool %q not found on exelet %s (available: %v)", pool, t.addr, tierNames)
			}
		}

		stream, err := t.client.ListInstances(ctx, &api.ListInstancesRequest{})
		if err != nil {
			return fmt.Errorf("list instances on %s: %w", t.addr, err)
		}

		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("recv instance from %s: %w", t.addr, err)
			}
			if resp.Instance == nil {
				continue
			}
			allInstances = append(allInstances, instanceInfo{
				id:         resp.Instance.ID,
				exeletAddr: t.addr,
			})
		}
	}

	if len(allInstances) == 0 {
		return fmt.Errorf("no instances found across %d exelet(s)", len(m.targets))
	}

	// Resolve current pools for each instance
	for i := range allInstances {
		inst := &allInstances[i]
		t := m.targetForAddr(inst.exeletAddr)
		pool, err := m.resolveCurrentPool(ctx, t, inst.id)
		if err != nil {
			slog.WarnContext(ctx, "could not resolve pool for instance, skipping", "instance", inst.id, "error", err)
			continue
		}
		inst.currentPool = pool
	}

	// Filter out instances with unknown pools
	var filtered []instanceInfo
	for _, inst := range allInstances {
		if inst.currentPool != "" {
			filtered = append(filtered, inst)
		}
	}

	if len(filtered) == 0 {
		return fmt.Errorf("no instances with resolvable pools found")
	}

	m.mu.Lock()
	m.inventory = filtered
	m.mu.Unlock()

	slog.InfoContext(ctx, "inventory built", "instances", len(filtered), "exelets", len(m.targets))
	return nil
}

func (m *Migrator) resolveCurrentPool(ctx context.Context, t *exeletTarget, instanceID string) (string, error) {
	// We can't cheaply determine which pool an instance is on without
	// triggering a real migration. Assign the primary pool as a best guess;
	// runMigration will self-correct via parseAlreadyOnPool if wrong.
	tiers, err := t.client.ListStorageTiers(ctx, &api.ListStorageTiersRequest{})
	if err != nil {
		return "", err
	}
	for _, tier := range tiers.Tiers {
		if tier.Primary {
			return tier.Name, nil
		}
	}
	if len(tiers.Tiers) > 0 {
		return tiers.Tiers[0].Name, nil
	}
	return "", fmt.Errorf("no tiers available")
}

func (m *Migrator) targetForAddr(addr string) *exeletTarget {
	for i := range m.targets {
		if m.targets[i].addr == addr {
			return &m.targets[i]
		}
	}
	return nil
}

func (m *Migrator) ActiveOps() []*activeOp {
	m.mu.Lock()
	defer m.mu.Unlock()
	ops := make([]*activeOp, 0, len(m.activeOps))
	for _, op := range m.activeOps {
		cpy := *op
		ops = append(ops, &cpy)
	}
	return ops
}

func (m *Migrator) Run(ctx context.Context) {
	migrationCount := 0

	for {
		if ctx.Err() != nil {
			return
		}
		if m.maxMigrations > 0 && migrationCount >= m.maxMigrations {
			slog.InfoContext(ctx, "max migrations reached", "count", migrationCount)
			return
		}

		inst, targetPool := m.pickMigration()
		if inst == nil {
			slog.WarnContext(ctx, "no eligible instance found, retrying after cooldown")
			select {
			case <-ctx.Done():
				return
			case <-time.After(m.cooldown):
				continue
			}
		}

		t := m.targetForAddr(inst.exeletAddr)
		if t == nil {
			slog.ErrorContext(ctx, "target not found for addr", "addr", inst.exeletAddr)
			continue
		}

		m.runMigration(ctx, t, inst, targetPool)
		migrationCount++

		select {
		case <-ctx.Done():
			return
		case <-time.After(m.cooldown):
		}
	}
}

func (m *Migrator) pickMigration() (*instanceInfo, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.inventory) == 0 {
		return nil, ""
	}

	// Shuffle and pick first instance where we can find a different target pool
	perm := rand.Perm(len(m.inventory))
	for _, idx := range perm {
		inst := m.inventory[idx]
		pool := m.pickDifferentPool(inst.currentPool)
		if pool != "" {
			return &inst, pool
		}
	}

	// Fallback: pick any instance and any pool
	inst := m.inventory[rand.IntN(len(m.inventory))]
	return &inst, m.pools[rand.IntN(len(m.pools))]
}

func (m *Migrator) pickDifferentPool(currentPool string) string {
	var candidates []string
	for _, p := range m.pools {
		if p != currentPool {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[rand.IntN(len(candidates))]
}

func (m *Migrator) runMigration(ctx context.Context, t *exeletTarget, inst *instanceInfo, targetPool string) {
	startTime := time.Now()

	slog.DebugContext(ctx, "starting migration", "instance", inst.id, "from", inst.currentPool, "to", targetPool, "exelet", t.addr)

	resp, err := t.client.MigrateStorageTier(ctx, &api.MigrateStorageTierRequest{
		InstanceID: inst.id,
		TargetPool: targetPool,
		Live:       m.live,
	})
	if err != nil {
		// If the exelet is shutting down (or otherwise unavailable),
		// skip this attempt and let the next interval retry.
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unavailable {
			slog.WarnContext(ctx, "exelet unavailable, skipping migration until next interval",
				"instance", inst.id, "exelet", t.addr, "error", st.Message())
			m.collector.Add(MigrationResult{
				InstanceID:  inst.id,
				Exelet:      t.addr,
				SourcePool:  inst.currentPool,
				TargetPool:  targetPool,
				State:       "skipped",
				Error:       st.Message(),
				StartedAt:   startTime,
				CompletedAt: time.Now(),
				Duration:    time.Since(startTime),
				DurationStr: time.Since(startTime).Truncate(time.Millisecond).String(),
			})
			return
		}

		// If the circuit breaker is tripped, report and let the caller
		// handle cooldown — don't spam the exelet.
		if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
			slog.ErrorContext(ctx, "circuit breaker tripped on exelet, skipping",
				"instance", inst.id, "exelet", t.addr, "error", st.Message())
			m.collector.Add(MigrationResult{
				InstanceID:  inst.id,
				Exelet:      t.addr,
				SourcePool:  inst.currentPool,
				TargetPool:  targetPool,
				State:       "failed",
				Error:       st.Message(),
				StartedAt:   startTime,
				CompletedAt: time.Now(),
				Duration:    time.Since(startTime),
				DurationStr: time.Since(startTime).Truncate(time.Millisecond).String(),
			})
			return
		}

		// If the instance is already on the target pool, update inventory
		// with the actual pool and retry with a different target.
		if actualPool, ok := parseAlreadyOnPool(err); ok {
			slog.DebugContext(ctx, "instance already on target pool, correcting inventory",
				"instance", inst.id, "actual_pool", actualPool)
			m.mu.Lock()
			for i := range m.inventory {
				if m.inventory[i].id == inst.id {
					m.inventory[i].currentPool = actualPool
					break
				}
			}
			m.mu.Unlock()

			// Retry with a different pool
			retryPool := m.pickDifferentPool(actualPool)
			if retryPool != "" {
				slog.DebugContext(ctx, "retrying migration with corrected pool",
					"instance", inst.id, "from", actualPool, "to", retryPool)
				inst.currentPool = actualPool
				m.runMigration(ctx, t, inst, retryPool)
				return
			}
			// No other pool available — skip silently
			return
		}

		m.collector.Add(MigrationResult{
			InstanceID:  inst.id,
			Exelet:      t.addr,
			SourcePool:  inst.currentPool,
			TargetPool:  targetPool,
			State:       "failed",
			Error:       err.Error(),
			StartedAt:   startTime,
			CompletedAt: time.Now(),
			Duration:    time.Since(startTime),
			DurationStr: time.Since(startTime).Truncate(time.Millisecond).String(),
		})
		return
	}

	op := &activeOp{
		OperationID: resp.OperationID,
		InstanceID:  inst.id,
		Exelet:      t.addr,
		SourcePool:  resp.SourcePool,
		TargetPool:  resp.TargetPool,
		StartedAt:   startTime,
	}

	// Update source pool from response (authoritative)
	m.mu.Lock()
	m.activeOps[resp.OperationID] = op
	m.mu.Unlock()

	result := m.pollMigration(ctx, t, op)

	m.mu.Lock()
	delete(m.activeOps, resp.OperationID)
	// Update inventory with new pool
	if result.State == "completed" {
		for i := range m.inventory {
			if m.inventory[i].id == inst.id {
				m.inventory[i].currentPool = targetPool
				break
			}
		}
	}
	m.mu.Unlock()

	m.collector.Add(result)

	// Clean up completed migrations
	_, _ = t.client.ClearTierMigrations(ctx, &api.ClearTierMigrationsRequest{})
}

func (m *Migrator) pollMigration(ctx context.Context, t *exeletTarget, op *activeOp) MigrationResult {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Track consecutive poll failures to detect exelet restarts.
	// After a restart the operation is lost — poll errors will persist
	// and we should not spin forever.
	var consecutivePollErrors int
	const maxConsecutivePollErrors = 10 // ~20 seconds of failures

	for {
		select {
		case <-ctx.Done():
			// Best-effort cancel
			_, _ = t.client.CancelTierMigration(ctx, &api.CancelTierMigrationRequest{
				OperationID: op.OperationID,
			})
			return MigrationResult{
				OperationID: op.OperationID,
				InstanceID:  op.InstanceID,
				Exelet:      op.Exelet,
				SourcePool:  op.SourcePool,
				TargetPool:  op.TargetPool,
				State:       "cancelled",
				StartedAt:   op.StartedAt,
				CompletedAt: time.Now(),
				Duration:    time.Since(op.StartedAt),
				DurationStr: time.Since(op.StartedAt).Truncate(time.Millisecond).String(),
			}
		case <-ticker.C:
			statusResp, err := t.client.GetTierMigrationStatus(ctx, &api.GetTierMigrationStatusRequest{})
			if err != nil {
				consecutivePollErrors++
				slog.WarnContext(ctx, "poll status failed", "op", op.OperationID, "error", err,
					"consecutive_errors", consecutivePollErrors)
				if consecutivePollErrors >= maxConsecutivePollErrors {
					slog.ErrorContext(ctx, "too many consecutive poll failures, exelet likely restarted",
						"op", op.OperationID, "instance", op.InstanceID)
					return MigrationResult{
						OperationID: op.OperationID,
						InstanceID:  op.InstanceID,
						Exelet:      op.Exelet,
						SourcePool:  op.SourcePool,
						TargetPool:  op.TargetPool,
						State:       "failed",
						Error:       fmt.Sprintf("lost connection to exelet after %d poll failures: %v", consecutivePollErrors, err),
						StartedAt:   op.StartedAt,
						CompletedAt: time.Now(),
						Duration:    time.Since(op.StartedAt),
						DurationStr: time.Since(op.StartedAt).Truncate(time.Millisecond).String(),
					}
				}
				continue
			}
			consecutivePollErrors = 0 // reset on success

			// If the status call succeeded but our operation is missing,
			// the exelet has restarted and lost track of it.
			found := false
			for _, migration := range statusResp.Operations {
				if migration.OperationID != op.OperationID {
					continue
				}
				found = true

				m.mu.Lock()
				if tracked, ok := m.activeOps[op.OperationID]; ok {
					tracked.Progress = migration.Progress
				}
				m.mu.Unlock()

				switch migration.State {
				case "completed":
					completedAt := time.Now()
					if migration.CompletedAt > 0 {
						completedAt = time.Unix(migration.CompletedAt, 0)
					}
					return MigrationResult{
						OperationID: op.OperationID,
						InstanceID:  op.InstanceID,
						Exelet:      op.Exelet,
						SourcePool:  op.SourcePool,
						TargetPool:  op.TargetPool,
						State:       "completed",
						StartedAt:   op.StartedAt,
						CompletedAt: completedAt,
						Duration:    completedAt.Sub(op.StartedAt),
						DurationStr: completedAt.Sub(op.StartedAt).Truncate(time.Millisecond).String(),
					}
				case "failed":
					return MigrationResult{
						OperationID: op.OperationID,
						InstanceID:  op.InstanceID,
						Exelet:      op.Exelet,
						SourcePool:  op.SourcePool,
						TargetPool:  op.TargetPool,
						State:       "failed",
						Error:       migration.Error,
						StartedAt:   op.StartedAt,
						CompletedAt: time.Now(),
						Duration:    time.Since(op.StartedAt),
						DurationStr: time.Since(op.StartedAt).Truncate(time.Millisecond).String(),
					}
				case "cancelled":
					return MigrationResult{
						OperationID: op.OperationID,
						InstanceID:  op.InstanceID,
						Exelet:      op.Exelet,
						SourcePool:  op.SourcePool,
						TargetPool:  op.TargetPool,
						State:       "cancelled",
						StartedAt:   op.StartedAt,
						CompletedAt: time.Now(),
						Duration:    time.Since(op.StartedAt),
						DurationStr: time.Since(op.StartedAt).Truncate(time.Millisecond).String(),
					}
				}
			}

			if !found {
				slog.ErrorContext(ctx, "operation not found on exelet, likely restarted",
					"op", op.OperationID, "instance", op.InstanceID)
				return MigrationResult{
					OperationID: op.OperationID,
					InstanceID:  op.InstanceID,
					Exelet:      op.Exelet,
					SourcePool:  op.SourcePool,
					TargetPool:  op.TargetPool,
					State:       "failed",
					Error:       "operation lost: exelet restarted during migration",
					StartedAt:   op.StartedAt,
					CompletedAt: time.Now(),
					Duration:    time.Since(op.StartedAt),
					DurationStr: time.Since(op.StartedAt).Truncate(time.Millisecond).String(),
				}
			}
		}
	}
}

// parseAlreadyOnPool extracts the pool name from an "already on pool X"
// InvalidArgument gRPC error. Returns the pool name and true if matched.
func parseAlreadyOnPool(err error) (string, bool) {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		return "", false
	}
	// Error format: "instance <id> is already on pool <pool>"
	const marker = "is already on pool "
	msg := st.Message()
	idx := strings.Index(msg, marker)
	if idx < 0 {
		return "", false
	}
	pool := msg[idx+len(marker):]
	if pool == "" {
		return "", false
	}
	return pool, true
}
