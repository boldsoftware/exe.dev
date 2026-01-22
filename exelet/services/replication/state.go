package replication

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// MaxHistoryEntries is the maximum number of history entries to keep
	MaxHistoryEntries = 1024
)

// State manages replication state persistence
type State struct {
	dir     string
	mu      sync.RWMutex
	history []HistoryEntry
	queue   []QueueItem
}

// HistoryEntry represents a completed replication operation
type HistoryEntry struct {
	VolumeID         string    `json:"volume_id"`
	VolumeName       string    `json:"volume_name"`
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `json:"completed_at"`
	DurationMS       int64     `json:"duration_ms"`
	BytesTransferred int64     `json:"bytes_transferred"`
	Success          bool      `json:"success"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	SnapshotName     string    `json:"snapshot_name"`
	Incremental      bool      `json:"incremental"`
}

// QueueItem represents a volume queued for replication
type QueueItem struct {
	VolumeID   string    `json:"volume_id"`
	VolumeName string    `json:"volume_name"`
	QueuedAt   time.Time `json:"queued_at"`
}

// NewState creates a new state manager
func NewState(dataDir string) (*State, error) {
	dir := filepath.Join(dataDir, "replication")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	s := &State{
		dir:     dir,
		history: make([]HistoryEntry, 0),
		queue:   make([]QueueItem, 0),
	}

	// Load existing state
	if err := s.load(); err != nil {
		// Non-fatal: start fresh if state is corrupted
		s.history = make([]HistoryEntry, 0)
		s.queue = make([]QueueItem, 0)
	}

	return s, nil
}

// historyPath returns the path to the history file
func (s *State) historyPath() string {
	return filepath.Join(s.dir, "history.json")
}

// queuePath returns the path to the queue file
func (s *State) queuePath() string {
	return filepath.Join(s.dir, "queue.json")
}

// load reads state from disk
func (s *State) load() error {
	// Load history
	historyData, err := os.ReadFile(s.historyPath())
	if err == nil {
		if err := json.Unmarshal(historyData, &s.history); err != nil {
			s.history = make([]HistoryEntry, 0)
		}
	}

	// Load queue
	queueData, err := os.ReadFile(s.queuePath())
	if err == nil {
		if err := json.Unmarshal(queueData, &s.queue); err != nil {
			s.queue = make([]QueueItem, 0)
		}
	}

	return nil
}

// saveHistory persists history to disk
func (s *State) saveHistory() error {
	data, err := json.MarshalIndent(s.history, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.historyPath(), data, 0o644)
}

// saveQueue persists queue to disk
func (s *State) saveQueue() error {
	data, err := json.MarshalIndent(s.queue, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.queuePath(), data, 0o644)
}

// AddHistory adds a history entry
func (s *State) AddHistory(entry HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Add entry at the beginning (most recent first)
	s.history = append([]HistoryEntry{entry}, s.history...)

	// Trim to max entries
	if len(s.history) > MaxHistoryEntries {
		s.history = s.history[:MaxHistoryEntries]
	}

	return s.saveHistory()
}

// GetHistory returns recent history entries
func (s *State) GetHistory(limit int) []HistoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.history) {
		limit = len(s.history)
	}

	result := make([]HistoryEntry, limit)
	copy(result, s.history[:limit])
	return result
}

// SetQueue sets the current queue
func (s *State) SetQueue(items []QueueItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.queue = items
	return s.saveQueue()
}

// GetQueue returns the current queue
func (s *State) GetQueue() []QueueItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]QueueItem, len(s.queue))
	copy(result, s.queue)
	return result
}

// ClearQueue clears the queue
func (s *State) ClearQueue() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.queue = make([]QueueItem, 0)
	return s.saveQueue()
}
