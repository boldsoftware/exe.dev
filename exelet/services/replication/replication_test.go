package replication

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"testing"

	"exe.dev/exelet/services"
	"exe.dev/exelet/storage"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
)

// mockCompute implements services.InstanceLookup for testing.
type mockCompute struct {
	instanceByID map[string]*computeapi.Instance
	// errByID overrides the default ErrNotFound for specific IDs.
	// Use nil value to simulate (nil, nil) read-race returns.
	errByID map[string]error
}

func (m *mockCompute) Instances(context.Context) ([]*computeapi.Instance, error) {
	panic("not called by cleanOrphanedVMDatasets")
}

func (m *mockCompute) GetInstanceByID(_ context.Context, id string) (*computeapi.Instance, error) {
	if inst, ok := m.instanceByID[id]; ok {
		return inst, nil
	}
	if err, ok := m.errByID[id]; ok {
		return nil, err
	}
	return nil, fmt.Errorf("%w: instance %s", computeapi.ErrNotFound, id)
}

func (m *mockCompute) GetInstanceByIP(context.Context, string) (string, string, error) {
	panic("not called")
}

func (m *mockCompute) StopInstanceByID(context.Context, string) error { panic("not called") }
func (m *mockCompute) StartInstanceByID(context.Context, string) error {
	panic("not called")
}

// mockStorage implements storage.StorageManager for testing.
// Only Delete is functional; embedded interface satisfies the rest.
type mockStorage struct {
	storage.StorageManager
	deleted []string
}

func (m *mockStorage) Delete(_ context.Context, id string) error {
	m.deleted = append(m.deleted, id)
	return nil
}

func TestIsVMInstanceID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"vm000123-blue-falcon", true},
		{"vm999999-x", true},
		{"vm000000-a", true},
		{"vm00012-blue-falcon", false},   // only 5 digits
		{"vm0001234-blue-falcon", false}, // 7 digits (digit at position 8, not dash)
		{"data", false},
		{"tank", false},
		{"", false},
		{"vmabcdef-x", false}, // letters instead of digits
		{"VM000123-x", false}, // uppercase
		{"vm000123", false},   // no dash after digits
		{"xm000123-x", false}, // wrong first char
		{"vx000123-x", false}, // wrong second char
		{"v", false},          // too short
		{"vm", false},         // too short
		{"vm000123x", false},  // char at position 8 is not dash
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got := isVMInstanceID(tt.id)
			if got != tt.want {
				t.Errorf("isVMInstanceID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func newTestService(compute services.InstanceLookup, store storage.StorageManager) *Service {
	return &Service{
		context: &services.ServiceContext{
			ComputeService: compute,
			StorageManager: store,
		},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestCleanOrphanedVMDatasets(t *testing.T) {
	ctx := context.Background()
	datasets := []string{"vm000123-blue-falcon", "vm000456-red-eagle", "data"}

	t.Run("single cycle does not delete", func(t *testing.T) {
		store := &mockStorage{}
		svc := newTestService(&mockCompute{}, store)

		svc.cleanOrphanedVMDatasets(ctx, datasets, map[string]string{}, true)

		if len(store.deleted) != 0 {
			t.Fatalf("expected no deletions after one cycle, got %v", store.deleted)
		}
	})

	t.Run("empty instance list deletes all VM datasets after two cycles", func(t *testing.T) {
		store := &mockStorage{}
		svc := newTestService(&mockCompute{}, store)

		nameByID := map[string]string{}

		// Cycle 1: candidates recorded, nothing deleted.
		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)
		if len(store.deleted) != 0 {
			t.Fatalf("expected no deletions after first cycle, got %v", store.deleted)
		}

		// Cycle 2: confirmed orphans deleted.
		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)
		sort.Strings(store.deleted)
		want := []string{"vm000123-blue-falcon", "vm000456-red-eagle"}
		if !slices.Equal(store.deleted, want) {
			t.Fatalf("deleted = %v, want %v", store.deleted, want)
		}
	})

	t.Run("only missing instance is deleted", func(t *testing.T) {
		store := &mockStorage{}
		svc := newTestService(&mockCompute{}, store)

		nameByID := map[string]string{"vm000123-blue-falcon": "my-vm"}

		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)
		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)

		if !slices.Equal(store.deleted, []string{"vm000456-red-eagle"}) {
			t.Fatalf("deleted = %v, want [vm000456-red-eagle]", store.deleted)
		}
	})

	t.Run("instances call failure resets candidates", func(t *testing.T) {
		store := &mockStorage{}
		svc := newTestService(&mockCompute{}, store)

		vmOnly := []string{"vm000123-blue-falcon"}

		// Cycle 1: successful, records candidates.
		svc.cleanOrphanedVMDatasets(ctx, vmOnly, map[string]string{}, true)
		// Cycle 2: Instances() failed — resets candidates.
		svc.cleanOrphanedVMDatasets(ctx, vmOnly, map[string]string{}, false)
		// Cycle 3: successful, but only first cycle since reset — no deletion.
		svc.cleanOrphanedVMDatasets(ctx, vmOnly, map[string]string{}, true)

		if len(store.deleted) != 0 {
			t.Fatalf("expected no deletions after failed-cycle reset, got %v", store.deleted)
		}
	})

	t.Run("point check finds instance skips deletion", func(t *testing.T) {
		store := &mockStorage{}
		compute := &mockCompute{
			instanceByID: map[string]*computeapi.Instance{
				"vm000123-blue-falcon": {ID: "vm000123-blue-falcon"},
			},
		}
		svc := newTestService(compute, store)

		nameByID := map[string]string{} // both appear orphaned by list

		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)
		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)

		// vm000123 survived the point check; only vm000456 is deleted.
		if !slices.Equal(store.deleted, []string{"vm000456-red-eagle"}) {
			t.Fatalf("deleted = %v, want [vm000456-red-eagle]", store.deleted)
		}
	})

	t.Run("transient GetInstanceByID error skips deletion", func(t *testing.T) {
		store := &mockStorage{}
		compute := &mockCompute{
			errByID: map[string]error{
				// Simulate read-race (nil, nil) and VMM error — both transient.
				"vm000123-blue-falcon": nil,
				"vm000456-red-eagle":   fmt.Errorf("VMM connection refused"),
			},
		}
		svc := newTestService(compute, store)

		nameByID := map[string]string{}

		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)
		svc.cleanOrphanedVMDatasets(ctx, datasets, nameByID, true)

		// Neither should be deleted — errors are not definitive ErrNotFound.
		if len(store.deleted) != 0 {
			t.Fatalf("expected no deletions on transient errors, got %v", store.deleted)
		}
	})
}

func TestRemoteVolumeID(t *testing.T) {
	tests := []struct {
		localID  string
		nodeName string
		want     string
	}{
		// VM instance IDs are unchanged
		{"vm000123-blue-falcon", "node1", "vm000123-blue-falcon"},
		{"vm999999-x", "node1", "vm999999-x"},

		// Non-VM datasets get node suffix
		{"data", "node1", "data-node1"},
		{"tank", "exelet-us-east-1", "tank-exelet-us-east-1"},

		// Already suffixed - don't double-suffix
		{"data-node1", "node1", "data-node1"},
		{"myvolume-exelet-us-east-1", "exelet-us-east-1", "myvolume-exelet-us-east-1"},

		// Partial suffix match should still add suffix
		{"data-node", "node1", "data-node-node1"},
	}

	for _, tt := range tests {
		t.Run(tt.localID+"/"+tt.nodeName, func(t *testing.T) {
			got := remoteVolumeID(tt.localID, tt.nodeName)
			if got != tt.want {
				t.Errorf("remoteVolumeID(%q, %q) = %q, want %q", tt.localID, tt.nodeName, got, tt.want)
			}
		})
	}
}
