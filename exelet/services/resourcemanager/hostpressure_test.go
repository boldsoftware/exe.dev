package resourcemanager

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHostPressureRetainsCacheOnReadError ensures that when /proc/meminfo
// becomes unreadable, hostPressure keeps the previous good sample
// instead of overwriting it with a zeroed one (which would make the
// tier classifier silently flip to "calm" on a broken /proc).
func TestHostPressureRetainsCacheOnReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meminfo"),
		[]byte("MemTotal:       16384 kB\nMemAvailable:    8192 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hp := &hostPressure{procRoot: dir, cacheTTL: 0}

	good := hp.Sample()
	if good.MemTotalBytes == 0 || good.MemAvailableBytes == 0 {
		t.Fatalf("expected a populated first sample, got %+v", good)
	}

	// Break /proc/meminfo and force a re-read by clearing cacheTime.
	if err := os.Remove(filepath.Join(dir, "meminfo")); err != nil {
		t.Fatal(err)
	}
	hp.cacheTime = time.Time{}

	after := hp.Sample()
	if after.MemTotalBytes != good.MemTotalBytes || after.MemAvailableBytes != good.MemAvailableBytes {
		t.Fatalf("expected previous good sample to be retained on read error; got %+v want %+v", after, good)
	}
	if af := after.AvailFraction(); af != good.AvailFraction() {
		t.Fatalf("AvailFraction flipped on read error: got %v want %v", af, good.AvailFraction())
	}
}

// TestHostPressureZeroWhenNoPriorGood verifies that without any
// previously cached good sample, a read error yields a zero sample
// (the only safe fallback — nothing to retain).
func TestHostPressureZeroWhenNoPriorGood(t *testing.T) {
	dir := t.TempDir()
	hp := &hostPressure{procRoot: dir, cacheTTL: 0}
	s := hp.Sample()
	if s.MemTotalBytes != 0 {
		t.Fatalf("expected zero sample, got %+v", s)
	}
}
