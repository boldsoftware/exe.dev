package replication

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sort"
	"testing"
	"time"
)

// mockTarget implements Target and VolumeDeleter for pruner tests.
type mockTarget struct {
	Target            // embed to satisfy interface; unused methods will panic
	volumes           []string
	snapshotsByVolume map[string][]SnapshotMetadata
	deletedVolumes    []string
	deletedSnapshots  []string
}

func (m *mockTarget) ListVolumes(context.Context) ([]string, error) {
	return m.volumes, nil
}

func (m *mockTarget) ListSnapshotsWithMetadata(_ context.Context, volumeID string) ([]SnapshotMetadata, error) {
	return m.snapshotsByVolume[volumeID], nil
}

func (m *mockTarget) DeleteVolume(_ context.Context, volumeID string) error {
	m.deletedVolumes = append(m.deletedVolumes, volumeID)
	return nil
}

func newTestPruner(target Target, nodeName string, retention time.Duration, now func() time.Time) *Pruner {
	p := NewPruner(target, true, nodeName, retention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if now != nil {
		p.now = now
	}
	return p
}

func TestPrune_RetentionBlocksDeletion(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	recentSnapshot := nowTime.Add(-3 * 24 * time.Hour) // 3 days ago

	target := &mockTarget{
		volumes: []string{"vm000123-blue-falcon"},
		snapshotsByVolume: map[string][]SnapshotMetadata{
			"vm000123-blue-falcon": {
				{Name: "repl-20250112T120000Z", CreatedAt: recentSnapshot.Unix()},
			},
		},
	}

	pruner := newTestPruner(target, "node1", 7*24*time.Hour, func() time.Time { return nowTime })

	// Volume is orphaned (not in local set)
	err := pruner.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	if len(target.deletedVolumes) != 0 {
		t.Fatalf("expected no deletions (snapshot within retention), got %v", target.deletedVolumes)
	}
}

func TestPrune_RetentionAllowsDeletionAfterExpiry(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	oldSnapshot := nowTime.Add(-10 * 24 * time.Hour) // 10 days ago

	target := &mockTarget{
		volumes: []string{"vm000123-blue-falcon"},
		snapshotsByVolume: map[string][]SnapshotMetadata{
			"vm000123-blue-falcon": {
				{Name: "repl-20250105T120000Z", CreatedAt: oldSnapshot.Unix()},
			},
		},
	}

	pruner := newTestPruner(target, "node1", 7*24*time.Hour, func() time.Time { return nowTime })

	err := pruner.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(target.deletedVolumes, []string{"vm000123-blue-falcon"}) {
		t.Fatalf("expected vm000123-blue-falcon to be deleted, got %v", target.deletedVolumes)
	}
}

func TestPrune_ZeroRetentionDeletesImmediately(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	recentSnapshot := nowTime.Add(-1 * time.Hour) // 1 hour ago

	target := &mockTarget{
		volumes: []string{"vm000123-blue-falcon"},
		snapshotsByVolume: map[string][]SnapshotMetadata{
			"vm000123-blue-falcon": {
				{Name: "repl-20250115T110000Z", CreatedAt: recentSnapshot.Unix()},
			},
		},
	}

	// Zero retention = delete immediately (old behavior)
	pruner := newTestPruner(target, "node1", 0, func() time.Time { return nowTime })

	err := pruner.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(target.deletedVolumes, []string{"vm000123-blue-falcon"}) {
		t.Fatalf("expected immediate deletion with zero retention, got %v", target.deletedVolumes)
	}
}

func TestPrune_NewestSnapshotDeterminesRetention(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	oldSnapshot := nowTime.Add(-10 * 24 * time.Hour)
	recentSnapshot := nowTime.Add(-2 * 24 * time.Hour)

	target := &mockTarget{
		volumes: []string{"vm000123-blue-falcon"},
		snapshotsByVolume: map[string][]SnapshotMetadata{
			"vm000123-blue-falcon": {
				{Name: "repl-old", CreatedAt: oldSnapshot.Unix()},
				{Name: "repl-recent", CreatedAt: recentSnapshot.Unix()},
			},
		},
	}

	pruner := newTestPruner(target, "node1", 7*24*time.Hour, func() time.Time { return nowTime })

	err := pruner.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	// Most recent snapshot is 2 days old, within 7-day retention
	if len(target.deletedVolumes) != 0 {
		t.Fatalf("expected no deletions (newest snapshot within retention), got %v", target.deletedVolumes)
	}
}

func TestPrune_MixedOrphansRetentionFiltering(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	oldSnapshot := nowTime.Add(-10 * 24 * time.Hour)
	recentSnapshot := nowTime.Add(-2 * 24 * time.Hour)

	target := &mockTarget{
		volumes: []string{"vm000123-blue-falcon", "vm000456-red-eagle"},
		snapshotsByVolume: map[string][]SnapshotMetadata{
			"vm000123-blue-falcon": {
				{Name: "repl-old", CreatedAt: oldSnapshot.Unix()},
			},
			"vm000456-red-eagle": {
				{Name: "repl-recent", CreatedAt: recentSnapshot.Unix()},
			},
		},
	}

	pruner := newTestPruner(target, "node1", 7*24*time.Hour, func() time.Time { return nowTime })

	err := pruner.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	// Only the old one should be deleted
	if !slices.Equal(target.deletedVolumes, []string{"vm000123-blue-falcon"}) {
		t.Fatalf("expected only vm000123-blue-falcon deleted, got %v", target.deletedVolumes)
	}
}

func TestPrune_LocalVolumeNotPruned(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	oldSnapshot := nowTime.Add(-10 * 24 * time.Hour)

	target := &mockTarget{
		volumes: []string{"vm000123-blue-falcon"},
		snapshotsByVolume: map[string][]SnapshotMetadata{
			"vm000123-blue-falcon": {
				{Name: "repl-old", CreatedAt: oldSnapshot.Unix()},
			},
		},
	}

	pruner := newTestPruner(target, "node1", 7*24*time.Hour, func() time.Time { return nowTime })

	// Volume exists locally — should not be pruned
	local := map[string]struct{}{"vm000123-blue-falcon": {}}
	err := pruner.Prune(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}

	if len(target.deletedVolumes) != 0 {
		t.Fatalf("expected no deletions for local volume, got %v", target.deletedVolumes)
	}
}

func TestPrune_DisabledNoOp(t *testing.T) {
	target := &mockTarget{
		volumes: []string{"vm000123-blue-falcon"},
	}

	p := NewPruner(target, false, "node1", 7*24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err := p.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	if len(target.deletedVolumes) != 0 {
		t.Fatalf("expected no deletions when disabled, got %v", target.deletedVolumes)
	}
}

func TestPrune_NodeNamespacing(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	oldSnapshot := nowTime.Add(-10 * 24 * time.Hour)

	target := &mockTarget{
		volumes: []string{"data-node1", "data-node2"},
		snapshotsByVolume: map[string][]SnapshotMetadata{
			"data-node1": {{Name: "repl-old", CreatedAt: oldSnapshot.Unix()}},
			"data-node2": {{Name: "repl-old", CreatedAt: oldSnapshot.Unix()}},
		},
	}

	pruner := newTestPruner(target, "node1", 7*24*time.Hour, func() time.Time { return nowTime })

	err := pruner.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	// Only data-node1 belongs to node1; data-node2 should be skipped
	sort.Strings(target.deletedVolumes)
	if !slices.Equal(target.deletedVolumes, []string{"data-node1"}) {
		t.Fatalf("expected only data-node1 deleted, got %v", target.deletedVolumes)
	}
}

func TestPrune_EmptySnapshotsDeletesImmediately(t *testing.T) {
	nowTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	target := &mockTarget{
		volumes:           []string{"vm000123-blue-falcon"},
		snapshotsByVolume: map[string][]SnapshotMetadata{},
	}

	pruner := newTestPruner(target, "node1", 7*24*time.Hour, func() time.Time { return nowTime })

	err := pruner.Prune(context.Background(), map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	// No snapshots means newest=0 → retention check passes (volume is empty, safe to delete)
	if !slices.Equal(target.deletedVolumes, []string{"vm000123-blue-falcon"}) {
		t.Fatalf("expected empty volume to be deleted, got %v", target.deletedVolumes)
	}
}
