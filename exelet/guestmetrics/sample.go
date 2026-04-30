// Package guestmetrics scrapes per-VM in-guest memory stats over the
// cloud-hypervisor hybrid-vsock socket and stores recent samples in a ring
// buffer for use by the resource manager and observability surfaces.
//
// This is the v0 (observability-only) implementation: the scraper runs
// regardless, the host-pressure tier classifier is exposed as a gauge, but
// nothing actually drops cache. Policy lands in v1.
package guestmetrics

import (
	"time"
)

// ProtocolVersion is the wire format version. Mirrors
// cmd/exe-init.MemdProtocolVersion. Incremented on incompatible changes.
const ProtocolVersion = 1

// PSILine is one line of /proc/pressure/memory.
type PSILine struct {
	Avg10  float64 `json:"avg10"`
	Avg60  float64 `json:"avg60"`
	Avg300 float64 `json:"avg300"`
	Total  uint64  `json:"total"`
}

// RawSample is the wire format memd returns. Field set must stay in sync
// with cmd/exe-init.MemdSample.
type RawSample struct {
	Version    int                `json:"version"`
	CapturedAt time.Time          `json:"captured_at"`
	UptimeSec  float64            `json:"uptime_sec"`
	Meminfo    map[string]uint64  `json:"meminfo"`
	Vmstat     map[string]uint64  `json:"vmstat"`
	PSI        map[string]PSILine `json:"psi"`
	Errors     []string           `json:"errors,omitempty"`
}

// Sample is the host-side normalised view of a single scrape. Fields are
// expressed in bytes (kB-based meminfo entries multiplied by 1024) so they
// compose with cgroup figures cleanly. Missing/absent fields stay at zero.
type Sample struct {
	CapturedAt time.Time
	FetchedAt  time.Time
	UptimeSec  float64

	MemTotalBytes      uint64
	MemFreeBytes       uint64
	MemAvailableBytes  uint64
	BuffersBytes       uint64
	CachedBytes        uint64
	ActiveFileBytes    uint64
	InactiveFileBytes  uint64
	MlockedBytes       uint64
	DirtyBytes         uint64
	WritebackBytes     uint64
	SwapTotalBytes     uint64
	SwapFreeBytes      uint64
	AnonHugePagesBytes uint64
	HugepagesTotalKB   uint64 // pages * Hugepagesize — unused for now; retained.
	SReclaimableBytes  uint64

	WorkingsetRefaultFile uint64
	WorkingsetRefaultAnon uint64
	Pgmajfault            uint64

	PSISome PSILine
	PSIFull PSILine

	PSIAvailable bool // true when /proc/pressure/memory was readable.
	Errors       []string
}

// FromRaw converts a wire sample to the normalised host-side view.
func FromRaw(r *RawSample, fetchedAt time.Time) Sample {
	kB := func(name string) uint64 { return r.Meminfo[name] * 1024 }
	s := Sample{
		CapturedAt:            r.CapturedAt,
		FetchedAt:             fetchedAt,
		UptimeSec:             r.UptimeSec,
		MemTotalBytes:         kB("MemTotal"),
		MemFreeBytes:          kB("MemFree"),
		MemAvailableBytes:     kB("MemAvailable"),
		BuffersBytes:          kB("Buffers"),
		CachedBytes:           kB("Cached"),
		ActiveFileBytes:       kB("Active(file)"),
		InactiveFileBytes:     kB("Inactive(file)"),
		MlockedBytes:          kB("Mlocked"),
		DirtyBytes:            kB("Dirty"),
		WritebackBytes:        kB("Writeback"),
		SwapTotalBytes:        kB("SwapTotal"),
		SwapFreeBytes:         kB("SwapFree"),
		AnonHugePagesBytes:    kB("AnonHugePages"),
		SReclaimableBytes:     kB("SReclaimable"),
		WorkingsetRefaultFile: r.Vmstat["workingset_refault_file"],
		WorkingsetRefaultAnon: r.Vmstat["workingset_refault_anon"],
		Pgmajfault:            r.Vmstat["pgmajfault"],
		Errors:                r.Errors,
	}
	if p, ok := r.PSI["some"]; ok {
		s.PSISome = p
		s.PSIAvailable = true
	}
	if p, ok := r.PSI["full"]; ok {
		s.PSIFull = p
		s.PSIAvailable = true
	}
	return s
}

// ReclaimableBytes returns the conservative "droppable on drop_caches=1"
// estimate: Active(file)+Inactive(file)−Mlocked−Dirty.
//
// Per the synthesis: =1 evicts the page cache only, so SReclaimable is
// excluded here even though it would be reclaimed under =3.
func (s Sample) ReclaimableBytes() uint64 {
	total := s.ActiveFileBytes + s.InactiveFileBytes
	if total <= s.MlockedBytes {
		return 0
	}
	total -= s.MlockedBytes
	if total <= s.DirtyBytes {
		return 0
	}
	return total - s.DirtyBytes
}
