package execore

import (
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
}

// liveMigrationTracker tracks in-flight VM-to-VM migrations.
type liveMigrationTracker struct {
	mu         sync.Mutex
	migrations map[string]*liveMigrationEntry // keyed by box name
}

func newLiveMigrationTracker() *liveMigrationTracker {
	return &liveMigrationTracker{
		migrations: make(map[string]*liveMigrationEntry),
	}
}

func (t *liveMigrationTracker) start(boxName, source, target string, live bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.migrations[boxName] = &liveMigrationEntry{
		BoxName:   boxName,
		Source:    source,
		Target:    target,
		Live:      live,
		State:     liveMigrationTransferring,
		StartedAt: time.Now(),
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
	delete(t.migrations, boxName)
}

// snapshot returns a copy of all in-flight migrations.
func (t *liveMigrationTracker) snapshot() []liveMigrationEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries := make([]liveMigrationEntry, 0, len(t.migrations))
	for _, m := range t.migrations {
		entries = append(entries, *m)
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
			entries = append(entries, *m)
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
