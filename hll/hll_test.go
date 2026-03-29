package hll

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestTrackerBasic(t *testing.T) {
	tracker := NewTracker(nil)
	defer tracker.Close()

	// Add some users
	tracker.NoteEvent("test-event", "user1")
	tracker.NoteEvent("test-event", "user2")
	tracker.NoteEvent("test-event", "user3")
	tracker.NoteEvent("test-event", "user1") // duplicate

	daily := tracker.GetCurrentDailyCount("test-event")
	weekly := tracker.GetCurrentWeeklyCount("test-event")

	// HLL is probabilistic, but for small sets should be exact
	if daily != 3 {
		t.Errorf("daily count = %d, want 3", daily)
	}
	if weekly != 3 {
		t.Errorf("weekly count = %d, want 3", weekly)
	}
}

func TestTrackerMultipleEvents(t *testing.T) {
	tracker := NewTracker(nil)
	defer tracker.Close()

	tracker.NoteEvent("proxy", "user1")
	tracker.NoteEvent("proxy", "user2")
	tracker.NoteEvent("web-visit", "user1")
	tracker.NoteEvent("web-visit", "user3")
	tracker.NoteEvent("web-visit", "user4")

	proxyCount := tracker.GetCurrentDailyCount("proxy")
	webCount := tracker.GetCurrentDailyCount("web-visit")

	if proxyCount != 2 {
		t.Errorf("proxy daily count = %d, want 2", proxyCount)
	}
	if webCount != 3 {
		t.Errorf("web-visit daily count = %d, want 3", webCount)
	}
}

func TestTrackerPersistence(t *testing.T) {
	storage := NewMemoryStorage()

	// Create tracker and add data
	tracker1 := NewTracker(storage)

	tracker1.NoteEvent("persist-test", "user1")
	tracker1.NoteEvent("persist-test", "user2")
	tracker1.NoteEvent("persist-test", "user3")

	// Close to save
	if err := tracker1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify data was saved (should have day and week keys)
	ctx := context.Background()
	dayData, err := storage.Load(ctx, "persist-test:day")
	if err != nil {
		t.Fatalf("Load day: %v", err)
	}
	if dayData == nil {
		t.Error("expected day data to be saved")
	}

	weekData, err := storage.Load(ctx, "persist-test:week")
	if err != nil {
		t.Fatalf("Load week: %v", err)
	}
	if weekData == nil {
		t.Error("expected week data to be saved")
	}

	// Create new tracker and verify data was loaded on demand
	tracker2 := NewTracker(storage)
	defer tracker2.Close()

	// Need to add an event to trigger loading from storage
	tracker2.NoteEvent("persist-test", "user4")

	daily := tracker2.GetCurrentDailyCount("persist-test")
	weekly := tracker2.GetCurrentWeeklyCount("persist-test")

	if daily != 4 {
		t.Errorf("reloaded daily count = %d, want 4", daily)
	}
	if weekly != 4 {
		t.Errorf("reloaded weekly count = %d, want 4", weekly)
	}
}

func TestTrackerLargeScale(t *testing.T) {
	tracker := NewTracker(nil)
	defer tracker.Close()

	// Add 10000 unique users
	for i := range 10000 {
		tracker.NoteEvent("scale-test", string(rune(i)))
	}

	count := tracker.GetCurrentDailyCount("scale-test")
	// HLL with default precision should be within ~2% for this size
	if count < 9700 || count > 10300 {
		t.Errorf("daily count = %d, expected ~10000 (within 3%%)", count)
	}
}

func TestPrometheusCollector(t *testing.T) {
	tracker := NewTracker(nil)
	defer tracker.Close()

	tracker.NoteEvent("proxy", "user1")
	tracker.NoteEvent("proxy", "user2")
	tracker.NoteEvent("web-visit", "user1")

	collector := NewCollector(tracker, []string{"proxy", "web-visit"})

	registry := prometheus.NewRegistry()
	if err := collector.Register(registry); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Gather metrics
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Verify we got the expected metric with period labels
	var found bool
	for _, fam := range families {
		if fam.GetName() == "unique_users" {
			found = true
			// Should have 4 metrics: 2 events x 2 periods (daily, weekly)
			if len(fam.GetMetric()) != 4 {
				t.Errorf("expected 4 unique_users metrics, got %d", len(fam.GetMetric()))
			}
		}
	}
	if !found {
		t.Error("missing unique_users metric")
	}
}

func TestMemoryStorage(t *testing.T) {
	ctx := context.Background()
	storage := NewMemoryStorage()

	// Test Save and Load
	if err := storage.Save(ctx, "key1", []byte("data1")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := storage.Load(ctx, "key1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "data1" {
		t.Errorf("Load = %q, want %q", data, "data1")
	}

	// Test Load non-existent
	data, err = storage.Load(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	if data != nil {
		t.Errorf("Load nonexistent = %v, want nil", data)
	}
}

func TestMarshalUnmarshalSketchData(t *testing.T) {
	period := "2026-01-07"
	hllData := []byte("fake-hll-data")

	data := marshalSketchData(period, hllData)
	gotPeriod, gotHLLData, err := unmarshalSketchData(data)
	if err != nil {
		t.Fatalf("unmarshalSketchData: %v", err)
	}

	if gotPeriod != period {
		t.Errorf("period = %q, want %q", gotPeriod, period)
	}
	if string(gotHLLData) != string(hllData) {
		t.Errorf("hllData = %q, want %q", gotHLLData, hllData)
	}
}

func TestMarshalUnmarshalWeekPeriod(t *testing.T) {
	period := "2026-W02"
	hllData := []byte("fake-hll-data-for-week")

	data := marshalSketchData(period, hllData)
	gotPeriod, gotHLLData, err := unmarshalSketchData(data)
	if err != nil {
		t.Fatalf("unmarshalSketchData: %v", err)
	}

	if gotPeriod != period {
		t.Errorf("period = %q, want %q", gotPeriod, period)
	}
	if string(gotHLLData) != string(hllData) {
		t.Errorf("hllData = %q, want %q", gotHLLData, hllData)
	}
}
