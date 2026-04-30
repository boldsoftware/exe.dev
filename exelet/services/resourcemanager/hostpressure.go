package resourcemanager

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/exelet/guestmetrics"
)

// hostPressure reads host /proc/meminfo and /proc/pressure/memory and
// caches them briefly so a poll cycle that asks repeatedly does not
// re-read /proc.
//
// The reader is owned by ResourceManager. It is the input to the
// guestmetrics tier classifier (which selects scrape cadence) and is
// exposed via the debug page. It does *not* drive any actuation in v0.
type hostPressure struct {
	mu        sync.Mutex
	cacheTime time.Time
	haveGood  bool
	cache     guestmetrics.HostSample

	// procRoot is overridable in tests; "" means /proc.
	procRoot string
	cacheTTL time.Duration

	// metrics is optional; when set, read errors increment a counter.
	metrics *guestmetrics.Metrics
}

// newHostPressure returns a reader with default cache TTL.
func newHostPressure() *hostPressure {
	return &hostPressure{cacheTTL: 2 * time.Second}
}

// withMetrics wires the read-error counter. nil-safe.
func (h *hostPressure) withMetrics(m *guestmetrics.Metrics) *hostPressure {
	h.metrics = m
	return h
}

func (h *hostPressure) procPath(p string) string {
	root := h.procRoot
	if root == "" {
		root = "/proc"
	}
	return root + p
}

// Sample returns the most recent host pressure reading. Stale entries are
// re-read; on read errors the previous cached value is retained so the
// classifier does not silently flip to "calm" when /proc reads fail.
// When no good sample has ever been observed, a zero HostSample is
// returned (which the classifier treats as calm) — there is no signal
// to do better with.
//
// PSI is treated as best-effort: kernels without /proc/pressure/memory
// return a zero PSI inside an otherwise-good sample. Only meminfo
// failure (or both failing) is counted as an error.
func (h *hostPressure) Sample() guestmetrics.HostSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.cacheTime.IsZero() && time.Since(h.cacheTime) < h.cacheTTL {
		return h.cache
	}
	var s guestmetrics.HostSample
	miBytes, miErr := os.ReadFile(h.procPath("/meminfo"))
	if miErr == nil {
		s.MemTotalBytes, s.MemAvailableBytes = parseHostMeminfo(string(miBytes))
	}
	if b, err := os.ReadFile(h.procPath("/pressure/memory")); err == nil {
		s.PSISomeAvg60, s.PSIFullAvg60 = parseHostPSI(string(b))
	}
	if miErr != nil || s.MemTotalBytes == 0 {
		if h.metrics != nil && h.metrics.HostPressureReadErrors != nil {
			h.metrics.HostPressureReadErrors.Inc()
		}
		if h.haveGood {
			// Retain previous good cache; refresh cacheTime so we
			// don't hammer /proc on every call while it's broken.
			h.cacheTime = time.Now()
			return h.cache
		}
		// No prior good sample — leave cache zeroed.
		h.cache = s
		h.cacheTime = time.Now()
		return s
	}
	h.cache = s
	h.cacheTime = time.Now()
	h.haveGood = true
	return s
}

func parseHostMeminfo(text string) (memTotalBytes, memAvailBytes uint64) {
	for _, line := range strings.Split(text, "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := line[:colon]
		rest := strings.TrimSpace(line[colon+1:])
		rest = strings.TrimSuffix(rest, " kB")
		v, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			memTotalBytes = v * 1024
		case "MemAvailable":
			memAvailBytes = v * 1024
		}
	}
	return memTotalBytes, memAvailBytes
}

func parseHostPSI(text string) (someAvg60, fullAvg60 float64) {
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) < 2 {
			continue
		}
		kind := parts[0]
		if kind != "some" && kind != "full" {
			continue
		}
		for _, kv := range parts[1:] {
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			if kv[:eq] != "avg60" {
				continue
			}
			v, err := strconv.ParseFloat(kv[eq+1:], 64)
			if err != nil {
				continue
			}
			if kind == "some" {
				someAvg60 = v
			} else {
				fullAvg60 = v
			}
		}
	}
	return someAvg60, fullAvg60
}
