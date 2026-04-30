package guestmetrics

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRingPushAndLatest(t *testing.T) {
	r := NewRing()
	if _, ok := r.Latest(); ok {
		t.Fatalf("empty ring should report not-ok")
	}
	for i := 0; i < RingCapacity+5; i++ {
		r.Push(Sample{
			CapturedAt:            time.Unix(int64(i), 0),
			WorkingsetRefaultFile: uint64(i * 10),
		})
	}
	last, ok := r.Latest()
	if !ok || last.WorkingsetRefaultFile != uint64((RingCapacity+4)*10) {
		t.Fatalf("latest=%+v ok=%v", last, ok)
	}
	snap := r.Snapshot()
	if len(snap) != RingCapacity {
		t.Fatalf("len=%d", len(snap))
	}
	if snap[0].WorkingsetRefaultFile >= snap[len(snap)-1].WorkingsetRefaultFile {
		t.Fatalf("snapshot not chronological")
	}
}

func TestRingRefaultRate(t *testing.T) {
	r := NewRing()
	t0 := time.Unix(1000, 0)
	for i := 0; i < 10; i++ {
		r.Push(Sample{
			CapturedAt:            t0.Add(time.Duration(i) * time.Second),
			WorkingsetRefaultFile: uint64(i * 50),
		})
	}
	rate := r.RefaultRate(60 * time.Second)
	if rate < 49 || rate > 51 {
		t.Fatalf("rate=%v", rate)
	}

	// counter reset → 0
	r.Push(Sample{CapturedAt: t0.Add(11 * time.Second), WorkingsetRefaultFile: 0})
	if rate := r.RefaultRate(60 * time.Second); rate != 0 {
		t.Fatalf("expected 0 on counter reset, got %v", rate)
	}
}

func TestSampleReclaimable(t *testing.T) {
	s := Sample{
		ActiveFileBytes:   100,
		InactiveFileBytes: 200,
		MlockedBytes:      50,
		DirtyBytes:        30,
	}
	if got := s.ReclaimableBytes(); got != 220 {
		t.Errorf("want 220, got %d", got)
	}
	s.MlockedBytes = 1000
	if got := s.ReclaimableBytes(); got != 0 {
		t.Errorf("want 0, got %d", got)
	}
}

func TestClassifierHysteresis(t *testing.T) {
	c := NewClassifier(DefaultTierThresholds)
	cases := []struct {
		availFrac, psiFull float64
		want               Tier
	}{
		{0.5, 0.0, TierCalm},
		{0.15, 0.0, TierNormal},
		{0.05, 0.0, TierPressured},
		{0.15, 0.0, TierPressured}, // hysteresis: stays
		{0.21, 0.5, TierNormal},    // exit triggered (avail>0.20 AND full<1.0)
		{0.50, 0.0, TierCalm},
		{0.50, 6.0, TierPressured}, // PSI alone triggers
	}
	for i, c2 := range cases {
		h := HostSample{MemTotalBytes: 100, MemAvailableBytes: uint64(c2.availFrac * 100), PSIFullAvg60: c2.psiFull}
		got := c.Update(h)
		if got != c2.want {
			t.Errorf("case %d (avail=%v full=%v): got %v want %v", i, c2.availFrac, c2.psiFull, got, c2.want)
		}
	}
}

func TestPoolScrapesViaPipe(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	var scrapes atomic.Int64
	dial := func(ctx context.Context, vmID string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			buf := make([]byte, 64)
			n, _ := b.Read(buf)
			if string(buf[:n]) != memdRequest {
				return
			}
			scrapes.Add(1)
			raw := RawSample{
				Version:    1,
				CapturedAt: time.Now(),
				Meminfo: map[string]uint64{
					"MemTotal":       1024 * 1024,
					"MemAvailable":   512 * 1024,
					"Cached":         256 * 1024,
					"Active(file)":   128 * 1024,
					"Inactive(file)": 64 * 1024,
				},
				Vmstat: map[string]uint64{"workingset_refault_file": 100},
			}
			data, _ := json.Marshal(&raw)
			data = append(data, '\n')
			_, _ = b.Write(data)
		}()
		return a, nil
	}
	hostSample := HostSample{MemTotalBytes: 1 << 30, MemAvailableBytes: 1 << 29}
	p := NewPool(PoolConfig{
		Cadences:      Cadences{Calm: 100 * time.Millisecond, Normal: 100 * time.Millisecond, Pressured: 100 * time.Millisecond},
		DialFunc:      dial,
		HostSampler:   func() HostSample { return hostSample },
		Metrics:       metrics,
		StaleAfter:    time.Second,
		Workers:       4,
		ScrapeTimeout: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	p.Add(VMInfo{ID: "vm1", Name: "alpha"})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if scrapes.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if scrapes.Load() == 0 {
		t.Fatalf("no scrapes happened")
	}
	s, ok := p.Latest("vm1")
	if !ok {
		t.Fatalf("no latest sample")
	}
	if s.MemTotalBytes != 1024*1024*1024 {
		t.Errorf("MemTotalBytes=%d", s.MemTotalBytes)
	}
}

func TestPoolReadsThroughDialFunc(t *testing.T) {
	// Sanity: ensure the buffered handshake-style reply is parsed correctly.
	dial := func(ctx context.Context, vmID string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			buf := make([]byte, 32)
			_, _ = io.ReadFull(b, buf[:len(memdRequest)])
			_, _ = b.Write([]byte(`{"version":1,"meminfo":{"MemTotal":2048},"vmstat":{}}` + "\n"))
		}()
		return a, nil
	}
	p := NewPool(PoolConfig{DialFunc: dial})
	raw, err := p.scrapeOnce(context.Background(), VMInfo{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if raw.Meminfo["MemTotal"] != 2048 {
		t.Errorf("meminfo=%+v", raw.Meminfo)
	}
}

// TestRingRefaultRateTwoSampleWindow regression-tests an earlier bug
// where the loop's exit guard compared a pointer into r.buf against the
// address of a stack-local Sample copy. Because those addresses are
// always different, the guard never fired and the function relied on the
// elapsed=0 escape hatch to avoid bogus rates. With proper index-based
// detection a window that holds only the latest sample must return 0.
func TestRingRefaultRateTwoSampleWindow(t *testing.T) {
	r := NewRing()
	t0 := time.Unix(1000, 0)
	r.Push(Sample{CapturedAt: t0, WorkingsetRefaultFile: 0})
	r.Push(Sample{CapturedAt: t0.Add(10 * time.Second), WorkingsetRefaultFile: 1000})

	// 60s window: both samples in window, expect 1000/10 = 100.
	if got := r.RefaultRate(60 * time.Second); got < 99 || got > 101 {
		t.Fatalf("60s window: got %v want ~100", got)
	}
	// 1ns window: only latest is in-window; oldestIdx must equal latestIdx
	// so the function returns 0.
	if got := r.RefaultRate(time.Nanosecond); got != 0 {
		t.Fatalf("1ns window: got %v want 0", got)
	}
}

// TestRingRefaultRateCounterReset (port of opus46) makes sure a snapshot
// + restore that resets the kernel's workingset_refault_file counter is
// treated as no signal rather than producing a hugely negative rate.
func TestRingRefaultRateCounterReset(t *testing.T) {
	r := NewRing()
	t0 := time.Unix(2000, 0)
	r.Push(Sample{CapturedAt: t0, WorkingsetRefaultFile: 5_000_000})
	r.Push(Sample{CapturedAt: t0.Add(15 * time.Second), WorkingsetRefaultFile: 100})
	if got := r.RefaultRate(60 * time.Second); got != 0 {
		t.Fatalf("counter reset: got %v want 0", got)
	}
}

func TestRingPrev(t *testing.T) {
	r := NewRing()
	if _, ok := r.Prev(); ok {
		t.Fatalf("empty ring Prev should be !ok")
	}
	r.Push(Sample{WorkingsetRefaultFile: 1})
	if _, ok := r.Prev(); ok {
		t.Fatalf("single-sample Prev should be !ok")
	}
	r.Push(Sample{WorkingsetRefaultFile: 2})
	prev, ok := r.Prev()
	if !ok || prev.WorkingsetRefaultFile != 1 {
		t.Fatalf("prev=%+v ok=%v", prev, ok)
	}
}
