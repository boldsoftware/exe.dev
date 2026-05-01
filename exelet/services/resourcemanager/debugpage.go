package resourcemanager

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"
)

//go:embed static/guest_metrics.html static/guest_metrics.css
var guestMetricsFS embed.FS

var guestMetricsTmpl = template.Must(template.ParseFS(guestMetricsFS, "static/guest_metrics.html"))

// guestMetricsCSS is loaded once at init from the embedded FS so the
// template can drop it inline without HTML-escaping.
var guestMetricsCSS = func() template.CSS {
	b, err := guestMetricsFS.ReadFile("static/guest_metrics.css")
	if err != nil {
		panic("resourcemanager: missing embedded guest_metrics.css: " + err.Error())
	}
	return template.CSS(b)
}()

// RegisterDebugHandlers registers the /debug/vms/guest-metrics page and a
// JSON variant against the given mux.
func (m *ResourceManager) RegisterDebugHandlers(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/debug/vms/guest-metrics", m.handleGuestMetricsPage)
	mux.HandleFunc("/debug/vms/guest-metrics.json", m.handleGuestMetricsJSON)
}

func (m *ResourceManager) handleGuestMetricsJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if m.guestPool == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false})
		return
	}
	snap := m.guestPool.Snapshot()
	out := struct {
		Enabled    bool   `json:"enabled"`
		HostTier   string `json:"host_tier"`
		HostSample any    `json:"host_sample"`
		Entries    any    `json:"entries"`
	}{
		Enabled:  true,
		HostTier: snap.Tier.String(),
		Entries:  snap.Entries,
	}
	if m.hostPressure != nil {
		out.HostSample = m.hostPressure.Sample()
	}
	_ = json.NewEncoder(w).Encode(out)
}

// guestMetricsRow is a flattened, pre-formatted view model for the
// debug page template. Strings are rendered through html/template, which
// HTML-escapes them — so a VM whose name contains markup cannot inject.
type guestMetricsRow struct {
	ID, Name, Age  string
	VMTier         string
	Stale          bool
	HaveLatest     bool
	MemTotal       string
	MemAvail       string
	Cached         string
	Reclaim        string
	Dirty          string
	Mlocked        string
	PSISome60      float64
	PSIFull60      float64
	RefaultRate    float64
	LastCPUPct     float64
	IdleFor        string
	FrozenFor      string
	LastWakeReason string
	NumSamples     int
}

type guestMetricsPageData struct {
	CSS           template.CSS
	Enabled       bool
	HostTier      string
	HostAvailPct  float64
	HostPSISome60 float64
	HostPSIFull60 float64
	Rows          []guestMetricsRow
}

func (m *ResourceManager) handleGuestMetricsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := guestMetricsPageData{CSS: guestMetricsCSS}
	if m.guestPool == nil {
		if err := guestMetricsTmpl.Execute(w, data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	data.Enabled = true
	snap := m.guestPool.Snapshot()
	data.HostTier = snap.Tier.String()
	if m.hostPressure != nil {
		host := m.hostPressure.Sample()
		data.HostAvailPct = host.AvailFraction() * 100
		data.HostPSISome60 = host.PSISomeAvg60
		data.HostPSIFull60 = host.PSIFullAvg60
	}
	now := time.Now()
	sort.Slice(snap.Entries, func(i, j int) bool { return snap.Entries[i].Name < snap.Entries[j].Name })
	data.Rows = make([]guestMetricsRow, 0, len(snap.Entries))
	for _, e := range snap.Entries {
		row := guestMetricsRow{
			ID:             e.ID,
			Name:           e.Name,
			Age:            "\u2014",
			VMTier:         e.VMTier.String(),
			HaveLatest:     e.HaveLatest,
			LastCPUPct:     e.LastCPUPct,
			LastWakeReason: e.LastWakeReason,
		}
		if e.IdleFor > 0 {
			row.IdleFor = e.IdleFor.Truncate(time.Second).String()
		}
		if e.FrozenFor > 0 {
			row.FrozenFor = e.FrozenFor.Truncate(time.Second).String()
		}
		if e.HaveLatest {
			d := now.Sub(e.Latest.FetchedAt)
			row.Age = d.Truncate(time.Second).String()
			row.Stale = d > 60*time.Second
			row.MemTotal = humanBytes(e.Latest.MemTotalBytes)
			row.MemAvail = humanBytes(e.Latest.MemAvailableBytes)
			row.Cached = humanBytes(e.Latest.CachedBytes)
			row.Reclaim = humanBytes(e.Latest.ReclaimableBytes())
			row.Dirty = humanBytes(e.Latest.DirtyBytes)
			row.Mlocked = humanBytes(e.Latest.MlockedBytes)
			row.PSISome60 = e.Latest.PSISome.Avg60
			row.PSIFull60 = e.Latest.PSIFull.Avg60
			row.RefaultRate = e.RefaultRate
			row.NumSamples = e.NumSamples
		}
		data.Rows = append(data.Rows, row)
	}
	if err := guestMetricsTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func humanBytes(n uint64) string {
	const (
		K = 1 << 10
		M = 1 << 20
		G = 1 << 30
	)
	switch {
	case n >= G:
		return fmt.Sprintf("%.2f GiB", float64(n)/G)
	case n >= M:
		return fmt.Sprintf("%.1f MiB", float64(n)/M)
	case n >= K:
		return fmt.Sprintf("%.1f KiB", float64(n)/K)
	}
	return fmt.Sprintf("%d B", n)
}
