package execore

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// liveMigrationState represents the current phase of a VM-to-VM live migration.
type liveMigrationState string

const (
	liveMigrationTransferring  liveMigrationState = "transferring"
	liveMigrationReconfiguring liveMigrationState = "reconfiguring"
	liveMigrationFinalizing    liveMigrationState = "finalizing"
)

// liveMigrationEntry tracks a single in-flight VM migration between exelets.
type liveMigrationEntry struct {
	BoxName   string
	Source    string // source exelet address
	Target    string // target exelet address
	Live      bool   // true for live migration, false for cold
	State     liveMigrationState
	BytesSent int64
	StartedAt time.Time

	// internal fields — not included in snapshots
	cancel    context.CancelFunc
	cancelled bool
}

// liveMigrationTracker tracks in-flight VM-to-VM migrations.
type liveMigrationTracker struct {
	mu         sync.Mutex
	migrations map[string]*liveMigrationEntry // keyed by box name

	// batchCancels tracks cancel funcs for user-level batch migrations,
	// keyed by user ID. Cancelling a batch stops the current migration
	// and prevents further VMs from being processed.
	batchCancels map[string]context.CancelFunc
}

func newLiveMigrationTracker() *liveMigrationTracker {
	return &liveMigrationTracker{
		migrations:   make(map[string]*liveMigrationEntry),
		batchCancels: make(map[string]context.CancelFunc),
	}
}

func (t *liveMigrationTracker) start(ctx context.Context, boxName, source, target string, live bool) context.Context {
	ctx, cancelFunc := context.WithCancel(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.migrations[boxName] = &liveMigrationEntry{
		BoxName:   boxName,
		Source:    source,
		Target:    target,
		Live:      live,
		State:     liveMigrationTransferring,
		StartedAt: time.Now(),
		cancel:    cancelFunc,
	}
	return ctx
}

// cancel cancels the in-flight migration for the given box, if any.
// It returns true if a migration was found and cancelled.
func (t *liveMigrationTracker) cancel(boxName string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.migrations[boxName]
	if !ok {
		return false
	}
	m.cancel()
	m.cancelled = true
	return true
}

// cancelled reports whether the migration for the given box was cancelled.
func (t *liveMigrationTracker) cancelled(boxName string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.migrations[boxName]
	if !ok {
		return false
	}
	return m.cancelled
}

// startBatch registers a cancel func for a user-level batch migration.
// The returned context is derived from the parent and will be cancelled
// when cancelBatch is called. The caller must call finishBatch when done.
func (t *liveMigrationTracker) startBatch(ctx context.Context, userID string) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.batchCancels[userID] = cancel
	return ctx
}

// cancelBatch cancels the in-flight batch migration for the given user.
// Returns true if a batch was found and cancelled.
func (t *liveMigrationTracker) cancelBatch(userID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cancel, ok := t.batchCancels[userID]
	if !ok {
		return false
	}
	cancel()
	return true
}

// finishBatch removes the batch cancel entry for the given user.
func (t *liveMigrationTracker) finishBatch(userID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cancel, ok := t.batchCancels[userID]; ok {
		cancel()
		delete(t.batchCancels, userID)
	}
}

func (t *liveMigrationTracker) updateBytes(boxName string, bytesSent int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m, ok := t.migrations[boxName]; ok {
		m.BytesSent = bytesSent
	}
}

func (t *liveMigrationTracker) updateState(boxName string, state liveMigrationState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m, ok := t.migrations[boxName]; ok {
		m.State = state
	}
}

func (t *liveMigrationTracker) finish(boxName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if m, ok := t.migrations[boxName]; ok {
		if m.cancel != nil {
			m.cancel()
		}
		delete(t.migrations, boxName)
	}
}

// snapshot returns a copy of all in-flight migrations.
func (t *liveMigrationTracker) snapshot() []liveMigrationEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries := make([]liveMigrationEntry, 0, len(t.migrations))
	for _, m := range t.migrations {
		entries = append(entries, liveMigrationEntry{
			BoxName:   m.BoxName,
			Source:    m.Source,
			Target:    m.Target,
			Live:      m.Live,
			State:     m.State,
			BytesSent: m.BytesSent,
			StartedAt: m.StartedAt,
		})
	}
	return entries
}

// snapshotForExelet returns in-flight migrations involving the given exelet address
// (as source or target).
func (t *liveMigrationTracker) snapshotForExelet(addr string) []liveMigrationEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	var entries []liveMigrationEntry
	for _, m := range t.migrations {
		if m.Source == addr || m.Target == addr {
			entries = append(entries, liveMigrationEntry{
				BoxName:   m.BoxName,
				Source:    m.Source,
				Target:    m.Target,
				Live:      m.Live,
				State:     m.State,
				BytesSent: m.BytesSent,
				StartedAt: m.StartedAt,
			})
		}
	}
	return entries
}

// liveMigrationInfo is the display-friendly representation of an in-flight migration.
type liveMigrationInfo struct {
	BoxName      string
	Source       string
	Target       string
	Direction    string // "outbound" or "inbound" relative to the exelet being viewed
	Live         bool
	State        string
	Transferred  string
	TransferRate string
	Duration     string
}

// liveMigrationInfoForExelet returns display-ready info for in-flight migrations
// involving the given exelet address.
func (s *Server) liveMigrationInfoForExelet(addr string) []liveMigrationInfo {
	entries := s.liveMigrations.snapshotForExelet(addr)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StartedAt.Before(entries[j].StartedAt)
	})
	infos := make([]liveMigrationInfo, len(entries))
	now := time.Now()
	for i, e := range entries {
		dur := now.Sub(e.StartedAt)
		var rate string
		if dur > time.Second && e.BytesSent > 0 {
			mbps := float64(e.BytesSent) * 8 / dur.Seconds() / 1_000_000
			rate = fmt.Sprintf("%.1f Mbit/s", mbps)
		}
		direction := "outbound"
		if e.Target == addr {
			direction = "inbound"
		}
		infos[i] = liveMigrationInfo{
			BoxName:      e.BoxName,
			Source:       e.Source,
			Target:       e.Target,
			Direction:    direction,
			Live:         e.Live,
			State:        string(e.State),
			Transferred:  formatMigrationBytes(e.BytesSent),
			TransferRate: rate,
			Duration:     formatDuration(dur),
		}
	}
	return infos
}

func formatMigrationBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(b)/float64(1<<20))
	case b > 0:
		return fmt.Sprintf("%.0f KiB", float64(b)/float64(1<<10))
	default:
		return "—"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// allLiveMigrationInfo returns display-ready info for all in-flight VM migrations.
func (s *Server) allLiveMigrationInfo() []liveMigrationInfo {
	entries := s.liveMigrations.snapshot()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StartedAt.Before(entries[j].StartedAt)
	})
	infos := make([]liveMigrationInfo, len(entries))
	now := time.Now()
	for i, e := range entries {
		dur := now.Sub(e.StartedAt)
		var rate string
		if dur > time.Second && e.BytesSent > 0 {
			mbps := float64(e.BytesSent) * 8 / dur.Seconds() / 1_000_000
			rate = fmt.Sprintf("%.1f Mbit/s", mbps)
		}
		infos[i] = liveMigrationInfo{
			BoxName:      e.BoxName,
			Source:       e.Source,
			Target:       e.Target,
			Live:         e.Live,
			State:        string(e.State),
			Transferred:  formatMigrationBytes(e.BytesSent),
			TransferRate: rate,
			Duration:     formatDuration(dur),
		}
	}
	return infos
}
