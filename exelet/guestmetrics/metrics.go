package guestmetrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds per-VM gauges and channel-level counters for the guest
// memory scraper.
type Metrics struct {
	CachedBytes       *prometheus.GaugeVec
	MemAvailableBytes *prometheus.GaugeVec
	MemTotalBytes     *prometheus.GaugeVec
	ReclaimableBytes  *prometheus.GaugeVec
	DirtyBytes        *prometheus.GaugeVec
	MlockedBytes      *prometheus.GaugeVec
	PSISomeAvg60      *prometheus.GaugeVec
	PSIFullAvg60      *prometheus.GaugeVec
	RefaultRate       *prometheus.GaugeVec
	LastFetchTSSecs   *prometheus.GaugeVec

	// Channel-level
	ScrapesTotal   *prometheus.CounterVec
	ScrapeFailures *prometheus.CounterVec
	ScrapeDuration prometheus.Histogram

	// PoolScrapeDropped counts scrapes the dispatcher skipped because the
	// worker pool was saturated. Backpressure visibility for the worker
	// budget; non-zero values mean the pool is undersized for the fleet.
	PoolScrapeDropped prometheus.Counter

	// Host-pressure tier as a numeric gauge: 0=calm, 1=normal, 2=pressured.
	HostTier prometheus.Gauge

	// Whether memwatch is enabled (kill switch). 1=on, 0=off.
	Enabled prometheus.Gauge

	// HostPressureReadErrors counts failed reads of host /proc/meminfo or
	// /proc/pressure/memory. The reader retains the previous good sample
	// on error, so a non-zero counter without a fresh sample means the
	// classifier is operating on a stale view.
	HostPressureReadErrors prometheus.Counter

	mu         sync.Mutex
	registered map[string]struct{}
}

// NewMetrics registers gauges with the given registry.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	labels := []string{"vm_id", "vm_name"}
	mkGauge := func(name, help string) *prometheus.GaugeVec {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "exelet", Subsystem: "guestmem", Name: name, Help: help,
		}, labels)
		reg.MustRegister(g)
		return g
	}
	mkCounter := func(name, help string) *prometheus.CounterVec {
		c := prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "exelet", Subsystem: "guestmem", Name: name, Help: help,
		}, labels)
		reg.MustRegister(c)
		return c
	}
	hist := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "exelet", Subsystem: "guestmem", Name: "scrape_duration_seconds",
		Help:    "Time spent dialling memd and reading one sample.",
		Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
	})
	reg.MustRegister(hist)
	dropped := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "exelet", Subsystem: "guestmem", Name: "pool_scrape_dropped_total",
		Help: "Scrapes skipped because the worker pool was saturated.",
	})
	reg.MustRegister(dropped)
	hostTier := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "exelet", Subsystem: "guestmem", Name: "host_tier",
		Help: "Host-pressure tier classifier output: 0=calm, 1=normal, 2=pressured.",
	})
	reg.MustRegister(hostTier)
	enabled := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "exelet", Subsystem: "guestmem", Name: "enabled",
		Help: "1 when guest memory observability is enabled, 0 when killed via env.",
	})
	reg.MustRegister(enabled)
	hpReadErrs := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "exelet", Subsystem: "guestmem", Name: "host_pressure_read_errors_total",
		Help: "Failed reads of host /proc/meminfo or /proc/pressure/memory; previous cached sample is retained.",
	})
	reg.MustRegister(hpReadErrs)
	return &Metrics{
		CachedBytes:            mkGauge("cached_bytes", "Guest /proc/meminfo Cached."),
		MemAvailableBytes:      mkGauge("mem_available_bytes", "Guest /proc/meminfo MemAvailable."),
		MemTotalBytes:          mkGauge("mem_total_bytes", "Guest /proc/meminfo MemTotal."),
		ReclaimableBytes:       mkGauge("reclaimable_bytes", "Active(file)+Inactive(file)−Mlocked−Dirty."),
		DirtyBytes:             mkGauge("dirty_bytes", "Guest /proc/meminfo Dirty."),
		MlockedBytes:           mkGauge("mlocked_bytes", "Guest /proc/meminfo Mlocked."),
		PSISomeAvg60:           mkGauge("psi_some_avg60_pct", "Guest PSI memory some.avg60."),
		PSIFullAvg60:           mkGauge("psi_full_avg60_pct", "Guest PSI memory full.avg60."),
		RefaultRate:            mkGauge("workingset_refault_file_rate", "Refaults/s over the recent window."),
		LastFetchTSSecs:        mkGauge("last_fetch_unix_seconds", "Unix time of the last successful scrape."),
		ScrapesTotal:           mkCounter("scrapes_total", "Total scrape attempts."),
		ScrapeFailures:         mkCounter("scrape_failures_total", "Failed scrape attempts."),
		ScrapeDuration:         hist,
		PoolScrapeDropped:      dropped,
		HostTier:               hostTier,
		Enabled:                enabled,
		HostPressureReadErrors: hpReadErrs,
		registered:             make(map[string]struct{}),
	}
}

// Update writes per-VM gauges from a sample.
func (m *Metrics) Update(id, name string, s Sample, refaultRate float64) {
	if m == nil {
		return
	}
	lbl := prometheus.Labels{"vm_id": id, "vm_name": name}
	m.CachedBytes.With(lbl).Set(float64(s.CachedBytes))
	m.MemAvailableBytes.With(lbl).Set(float64(s.MemAvailableBytes))
	m.MemTotalBytes.With(lbl).Set(float64(s.MemTotalBytes))
	m.ReclaimableBytes.With(lbl).Set(float64(s.ReclaimableBytes()))
	m.DirtyBytes.With(lbl).Set(float64(s.DirtyBytes))
	m.MlockedBytes.With(lbl).Set(float64(s.MlockedBytes))
	if s.PSIAvailable {
		m.PSISomeAvg60.With(lbl).Set(s.PSISome.Avg60)
		m.PSIFullAvg60.With(lbl).Set(s.PSIFull.Avg60)
	}
	m.RefaultRate.With(lbl).Set(refaultRate)
	m.LastFetchTSSecs.With(lbl).Set(float64(s.FetchedAt.Unix()))

	m.mu.Lock()
	m.registered[id+"\x00"+name] = struct{}{}
	m.mu.Unlock()
}

// Delete removes per-VM labelsets to keep the cardinality bounded as VMs
// come and go.
func (m *Metrics) Delete(id, name string) {
	if m == nil {
		return
	}
	lbl := prometheus.Labels{"vm_id": id, "vm_name": name}
	m.CachedBytes.Delete(lbl)
	m.MemAvailableBytes.Delete(lbl)
	m.MemTotalBytes.Delete(lbl)
	m.ReclaimableBytes.Delete(lbl)
	m.DirtyBytes.Delete(lbl)
	m.MlockedBytes.Delete(lbl)
	m.PSISomeAvg60.Delete(lbl)
	m.PSIFullAvg60.Delete(lbl)
	m.RefaultRate.Delete(lbl)
	m.LastFetchTSSecs.Delete(lbl)
	m.ScrapesTotal.Delete(lbl)
	m.ScrapeFailures.Delete(lbl)
	m.mu.Lock()
	delete(m.registered, id+"\x00"+name)
	m.mu.Unlock()
}

// SetTier publishes the host-pressure tier as a gauge.
func (m *Metrics) SetTier(t Tier) {
	if m == nil {
		return
	}
	m.HostTier.Set(float64(t))
}
