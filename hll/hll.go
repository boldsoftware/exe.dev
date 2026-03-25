// Package hll provides HyperLogLog-based unique user tracking.
package hll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/axiomhq/hyperloglog"
)

// Storage is the interface for persisting HLL sketches.
type Storage interface {
	// Load retrieves a sketch by key. Returns nil, nil if not found.
	Load(ctx context.Context, key string) ([]byte, error)
	// Save stores a sketch by key.
	Save(ctx context.Context, key string, data []byte) error
}

// MemoryStorage is an in-memory Storage implementation for testing.
type MemoryStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemoryStorage creates a new in-memory storage.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{data: make(map[string][]byte)}
}

func (m *MemoryStorage) Load(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if data, ok := m.data[key]; ok {
		// Return a copy to prevent mutation
		cp := make([]byte, len(data))
		copy(cp, data)
		return cp, nil
	}
	return nil, nil
}

func (m *MemoryStorage) Save(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.data[key] = cp
	return nil
}

// Tracker manages HyperLogLog sketches for counting unique users per event type.
// It maintains separate sketches for daily and weekly time buckets.
type Tracker struct {
	mu       sync.RWMutex
	storage  Storage
	sketches map[string]*sketch // key: "event:day" or "event:week"

	saveInterval time.Duration

	stopped atomic.Bool
	stop    chan struct{}
	wg      sync.WaitGroup
}

type sketch struct {
	hll       *hyperloglog.Sketch
	period    string // e.g., "2026-01-07" for day, "2026-W02" for week
	modified  bool
	lastSaved []byte // marshaled data from last successful save
}

// NewTracker creates a new HLL tracker with the given storage backend.
// If storage is nil, sketches are kept in memory only (no persistence).
func NewTracker(storage Storage) *Tracker {
	t := &Tracker{
		storage:      storage,
		sketches:     make(map[string]*sketch),
		saveInterval: 1 * time.Minute,
		stop:         make(chan struct{}),
	}

	// Start background save goroutine
	t.wg.Go(t.saveLoop)
	return t
}

// NoteEvent records that userID performed the given event.
// This adds the user to both the daily and weekly sketches for the current time.
func (t *Tracker) NoteEvent(event, userID string) {
	now := time.Now().UTC()
	dayKey := event + ":day"
	dayPeriod := now.Format("2006-01-02")
	weekKey := event + ":week"
	year, week := now.ISOWeek()
	weekPeriod := fmt.Sprintf("%d-W%02d", year, week)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.addToSketch(dayKey, dayPeriod, userID)
	t.addToSketch(weekKey, weekPeriod, userID)
}

// GetCurrentDailyCount returns the estimated unique user count for today.
func (t *Tracker) GetCurrentDailyCount(event string) uint64 {
	now := time.Now().UTC()
	key := event + ":day"
	period := now.Format("2006-01-02")

	t.mu.RLock()
	defer t.mu.RUnlock()

	if s, ok := t.sketches[key]; ok && s.period == period {
		return s.hll.Estimate()
	}
	return 0
}

// GetCurrentWeeklyCount returns the estimated unique user count for this week.
func (t *Tracker) GetCurrentWeeklyCount(event string) uint64 {
	now := time.Now().UTC()
	key := event + ":week"
	year, week := now.ISOWeek()
	period := fmt.Sprintf("%d-W%02d", year, week)

	t.mu.RLock()
	defer t.mu.RUnlock()

	if s, ok := t.sketches[key]; ok && s.period == period {
		return s.hll.Estimate()
	}
	return 0
}

// Close stops the background save loop and saves all modified sketches.
func (t *Tracker) Close() error {
	if !t.stopped.Swap(true) {
		close(t.stop)
	}
	t.wg.Wait()
	return t.saveAllModified()
}

// addToSketch adds a user to the sketch with the given key, creating it if needed.
// If the stored sketch is for a different period, it resets and starts fresh.
// Caller must hold t.mu lock.
func (t *Tracker) addToSketch(key, period, userID string) {
	s, ok := t.sketches[key]
	if !ok {
		// Try loading from storage
		s = t.loadSketch(key, period)
		t.sketches[key] = s
	} else if s.period != period {
		// Period changed (new day/week), start fresh
		s = &sketch{hll: hyperloglog.New(), period: period}
		t.sketches[key] = s
	}
	s.hll.Insert([]byte(userID))
	s.modified = true
}

// loadSketch loads a sketch from storage, returning a fresh one if not found or stale.
func (t *Tracker) loadSketch(key, currentPeriod string) *sketch {
	if t.storage == nil {
		return &sketch{hll: hyperloglog.New(), period: currentPeriod}
	}

	ctx := context.Background()
	data, err := t.storage.Load(ctx, key)
	if err != nil || data == nil {
		return &sketch{hll: hyperloglog.New(), period: currentPeriod}
	}

	period, hllData, err := unmarshalSketchData(data)
	if err != nil || period != currentPeriod {
		// Stale or corrupted data, start fresh
		return &sketch{hll: hyperloglog.New(), period: currentPeriod}
	}

	hll := hyperloglog.New()
	if err := hll.UnmarshalBinary(hllData); err != nil {
		return &sketch{hll: hyperloglog.New(), period: currentPeriod}
	}

	return &sketch{hll: hll, period: period}
}

func (t *Tracker) saveLoop() {
	ticker := time.NewTicker(t.saveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = t.saveAllModified()
		case <-t.stop:
			return
		}
	}
}

func (t *Tracker) saveAllModified() error {
	if t.storage == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	ctx := context.Background()
	for key, s := range t.sketches {
		if !s.modified {
			continue
		}
		hllData, err := s.hll.MarshalBinary()
		if err != nil {
			return fmt.Errorf("marshal sketch %s: %w", key, err)
		}
		data := marshalSketchData(s.period, hllData)
		if bytes.Equal(data, s.lastSaved) {
			s.modified = false
			continue
		}
		if err := t.storage.Save(ctx, key, data); err != nil {
			return fmt.Errorf("save sketch %s: %w", key, err)
		}
		s.lastSaved = data
		s.modified = false
	}
	return nil
}

// sketchData is the JSON-serializable format for stored sketches.
type sketchData struct {
	Period string `json:"period"`
	HLL    string `json:"hll"` // base64-encoded HLL sketch
}

// marshalSketchData encodes period and HLL data as JSON.
func marshalSketchData(period string, hllData []byte) []byte {
	sd := sketchData{
		Period: period,
		HLL:    base64.StdEncoding.EncodeToString(hllData),
	}
	data, _ := json.Marshal(sd)
	return data
}

// unmarshalSketchData decodes period and HLL data from JSON.
func unmarshalSketchData(data []byte) (period string, hllData []byte, err error) {
	var sd sketchData
	if err := json.Unmarshal(data, &sd); err != nil {
		return "", nil, fmt.Errorf("unmarshal JSON: %w", err)
	}
	hllData, err = base64.StdEncoding.DecodeString(sd.HLL)
	if err != nil {
		return "", nil, fmt.Errorf("decode base64: %w", err)
	}
	return sd.Period, hllData, nil
}
