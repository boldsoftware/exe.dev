package replication

import (
	"os"
	"testing"
	"time"
)

func TestState(t *testing.T) {
	// Create a temporary directory for state
	tmpDir, err := os.MkdirTemp("", "replication-state-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create state manager
	state, err := NewState(tmpDir)
	if err != nil {
		t.Fatalf("NewState() error: %v", err)
	}

	// Test history
	t.Run("history", func(t *testing.T) {
		// Initially empty
		history := state.GetHistory(10)
		if len(history) != 0 {
			t.Errorf("expected empty history, got %d entries", len(history))
		}

		// Add entries
		for i := 0; i < 5; i++ {
			entry := HistoryEntry{
				VolumeID:    "vol-" + string(rune('a'+i)),
				StartedAt:   time.Now().Add(-time.Duration(5-i) * time.Hour),
				CompletedAt: time.Now().Add(-time.Duration(5-i)*time.Hour + time.Minute),
				Success:     true,
			}
			if err := state.AddHistory(entry); err != nil {
				t.Errorf("AddHistory() error: %v", err)
			}
		}

		// Get all entries
		history = state.GetHistory(10)
		if len(history) != 5 {
			t.Errorf("expected 5 entries, got %d", len(history))
		}

		// Most recent should be first
		if history[0].VolumeID != "vol-e" {
			t.Errorf("expected most recent entry first, got %s", history[0].VolumeID)
		}

		// Test limit
		history = state.GetHistory(2)
		if len(history) != 2 {
			t.Errorf("expected 2 entries with limit, got %d", len(history))
		}
	})

	// Test queue
	t.Run("queue", func(t *testing.T) {
		// Initially empty
		queue := state.GetQueue()
		if len(queue) != 0 {
			t.Errorf("expected empty queue, got %d items", len(queue))
		}

		// Set queue
		items := []QueueItem{
			{VolumeID: "vol-1", VolumeName: "test-1", QueuedAt: time.Now()},
			{VolumeID: "vol-2", VolumeName: "test-2", QueuedAt: time.Now()},
		}
		if err := state.SetQueue(items); err != nil {
			t.Errorf("SetQueue() error: %v", err)
		}

		// Get queue
		queue = state.GetQueue()
		if len(queue) != 2 {
			t.Errorf("expected 2 items, got %d", len(queue))
		}
		if queue[0].VolumeID != "vol-1" {
			t.Errorf("expected vol-1, got %s", queue[0].VolumeID)
		}

		// Clear queue
		if err := state.ClearQueue(); err != nil {
			t.Errorf("ClearQueue() error: %v", err)
		}
		queue = state.GetQueue()
		if len(queue) != 0 {
			t.Errorf("expected empty queue after clear, got %d items", len(queue))
		}
	})

	// Test persistence
	t.Run("persistence", func(t *testing.T) {
		// Add some state
		entry := HistoryEntry{
			VolumeID:  "persist-vol",
			Success:   true,
			StartedAt: time.Now(),
		}
		if err := state.AddHistory(entry); err != nil {
			t.Errorf("AddHistory() error: %v", err)
		}

		// Create new state manager pointing to same directory
		state2, err := NewState(tmpDir)
		if err != nil {
			t.Fatalf("NewState() for reload error: %v", err)
		}

		// Should have the same history
		history := state2.GetHistory(10)
		found := false
		for _, h := range history {
			if h.VolumeID == "persist-vol" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("persisted history entry not found after reload")
		}
	})

	// Test history limit
	t.Run("history limit", func(t *testing.T) {
		// Create fresh state
		tmpDir2, _ := os.MkdirTemp("", "replication-state-limit-*")
		defer os.RemoveAll(tmpDir2)

		state, err := NewState(tmpDir2)
		if err != nil {
			t.Fatalf("NewState() error: %v", err)
		}

		// Add more than MaxHistoryEntries
		for i := 0; i < MaxHistoryEntries+10; i++ {
			entry := HistoryEntry{
				VolumeID:  "vol-" + string(rune('0'+i%10)),
				Success:   true,
				StartedAt: time.Now(),
			}
			if err := state.AddHistory(entry); err != nil {
				t.Errorf("AddHistory() error: %v", err)
			}
		}

		// Should be capped at MaxHistoryEntries
		history := state.GetHistory(MaxHistoryEntries + 100)
		if len(history) > MaxHistoryEntries {
			t.Errorf("history should be capped at %d, got %d", MaxHistoryEntries, len(history))
		}
	})
}
