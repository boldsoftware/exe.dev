package pktflowagg

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	api "exe.dev/pkg/api/exe/pktflow/v1"
)

type vmInterval struct {
	Timestamp  int64
	IntervalMs int64
	Report     *api.VMFlowStats
}

// Alert represents a detected attack alert.
type Alert struct {
	Timestamp  int64  `json:"ts_unix_ms"`
	HostID     string `json:"host_id"`
	VMID       string `json:"vm_id"`
	UserID     string `json:"user_id"`
	Attack     string `json:"attack"`
	IPVersion  uint32 `json:"ip_version"`
	TargetIP   string `json:"target_ip"`
	Protocol   uint32 `json:"ip_proto"`
	Port       uint32 `json:"port,omitempty"`
	PortRole   string `json:"port_role,omitempty"`
	Packets    uint64 `json:"packets"`
	Bytes      uint64 `json:"bytes"`
	Threshold  uint64 `json:"threshold_packets"`
	IntervalMs int64  `json:"interval_ms"`
}

type vmHistory struct {
	max   int
	items []vmInterval
}

func (h *vmHistory) add(v vmInterval) {
	if h.max <= 0 {
		return
	}
	if len(h.items) < h.max {
		h.items = append(h.items, v)
		return
	}
	copy(h.items, h.items[1:])
	h.items[len(h.items)-1] = v
}

type storeMetrics struct {
	ingestTotal            prometheus.Counter
	alertsTotal            *prometheus.CounterVec
	activeAttacks          prometheus.Gauge
	flaggedVMs             prometheus.Gauge
	exeletLastReportSeconds *prometheus.GaugeVec
}

func newStoreMetrics(registry *prometheus.Registry) *storeMetrics {
	m := &storeMetrics{
		ingestTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "pktflow_ingest_reports_total",
			Help: "Total number of reports ingested.",
		}),
		alertsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pktflow_network_alerts_total",
			Help: "Total network-level alert events, by attack type.",
		}, []string{"attack"}),
		activeAttacks: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pktflow_network_active_attacks",
			Help: "Number of distinct attack keys currently active.",
		}),
		flaggedVMs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pktflow_network_flagged_vms",
			Help: "Number of VMs currently flagged by network detection.",
		}),
		exeletLastReportSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pktflow_exelet_last_report_seconds",
			Help: "Seconds since last report from each exelet.",
		}, []string{"addr"}),
	}
	if registry != nil {
		registry.MustRegister(m.ingestTotal, m.alertsTotal, m.activeAttacks, m.flaggedVMs, m.exeletLastReportSeconds)
	}
	return m
}

// ExeletStatus tracks the connection health of one exelet.
type ExeletStatus struct {
	Addr         string
	HostID       string
	LastReport   time.Time
	LastError    time.Time
	LastErrorMsg string
	ReportsTotal int64
	Connected    bool
}

// Store holds flow stats history and detection state.
type Store struct {
	mu       sync.Mutex
	max      int
	data     map[string]map[string]*vmHistory   // host -> vmID -> history
	alertMax int
	alerts   map[string]map[string]*alertHistory // host -> vmID -> per-VM alerts
	metrics  *storeMetrics
	exelets  map[string]*ExeletStatus // addr -> status

	// networkAlerts holds the latest network-level detection results.
	// Replaced on every ingest cycle with the current picture.
	networkAlerts []Alert

	onIngest func()
}

// NewStore creates a new Store with the given max intervals and prometheus registry.
func NewStore(max int, registry *prometheus.Registry) *Store {
	return &Store{
		max:      max,
		data:     make(map[string]map[string]*vmHistory),
		alertMax: max,
		alerts:   make(map[string]map[string]*alertHistory),
		metrics:  newStoreMetrics(registry),
		exelets:  make(map[string]*ExeletStatus),
	}
}

// RegisterExelet records an exelet address for health tracking.
func (s *Store) RegisterExelet(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exelets[addr] = &ExeletStatus{Addr: addr}
}

// RecordReport marks a successful report from an exelet.
func (s *Store) RecordReport(addr, hostID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if es, ok := s.exelets[addr]; ok {
		es.Connected = true
		es.HostID = hostID
		es.LastReport = time.Now()
		es.ReportsTotal++
	}
}

// RecordError records a connection error for an exelet.
func (s *Store) RecordError(addr string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if es, ok := s.exelets[addr]; ok {
		es.Connected = false
		es.LastError = time.Now()
		es.LastErrorMsg = err.Error()
	}
}

// ExeletStatuses returns a snapshot of all exelet statuses.
func (s *Store) ExeletStatuses() []ExeletStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ExeletStatus, 0, len(s.exelets))
	for _, es := range s.exelets {
		out = append(out, *es)
	}
	return out
}

// SetOnIngest sets a callback that is called after each successful ingest.
func (s *Store) SetOnIngest(fn func()) {
	s.onIngest = fn
}

// Ingest processes an incoming flow stats report.
func (s *Store) Ingest(report *api.FlowStatsReport) error {
	if report.HostID == "" {
		return fmt.Errorf("host_id is required")
	}
	if report.TsUnixMs == 0 {
		return fmt.Errorf("ts_unix_ms is required")
	}
	if report.IntervalMs <= 0 {
		return fmt.Errorf("interval_ms is required")
	}
	for i := range report.Vms {
		vm := report.Vms[i]
		if vm.VmID == "" {
			return fmt.Errorf("vm_id is required")
		}
		if vm.UserID == "" {
			return fmt.Errorf("user_id is required")
		}
		if vm.Tap == "" {
			return fmt.Errorf("tap is required")
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.metrics.ingestTotal.Inc()

	perHost, ok := s.data[report.HostID]
	if !ok {
		perHost = make(map[string]*vmHistory)
		s.data[report.HostID] = perHost
	}
	alertHost, ok := s.alerts[report.HostID]
	if !ok {
		alertHost = make(map[string]*alertHistory)
		s.alerts[report.HostID] = alertHost
	}
	for i := range report.Vms {
		vm := report.Vms[i]
		h, ok := perHost[vm.VmID]
		if !ok {
			h = &vmHistory{max: s.max}
			perHost[vm.VmID] = h
		}
		h.add(vmInterval{
			Timestamp:  report.TsUnixMs,
			IntervalMs: report.IntervalMs,
			Report:     vm,
		})

		vmAlerts := detectVM(report, vm)
		if len(vmAlerts) == 0 {
			continue
		}
		ah, ok := alertHost[vm.VmID]
		if !ok {
			ah = &alertHistory{max: s.alertMax}
			alertHost[vm.VmID] = ah
		}
		for _, a := range vmAlerts {
			ah.add(a)
			log.Printf("pktflow alert host=%s vm=%s user=%s attack=%s target=%s port=%d role=%s packets=%d bytes=%d",
				a.HostID, a.VMID, a.UserID, a.Attack, a.TargetIP, a.Port, a.PortRole, a.Packets, a.Bytes)
		}
	}

	// Run network-level detection across all VMs on all hosts.
	netAlerts := s.detectNetwork(report.IntervalMs)
	for _, a := range netAlerts {
		s.metrics.alertsTotal.WithLabelValues(a.Attack).Inc()
		log.Printf("pktflow network alert host=%s vm=%s user=%s attack=%s target=%s port=%d role=%s packets=%d bytes=%d",
			a.HostID, a.VMID, a.UserID, a.Attack, a.TargetIP, a.Port, a.PortRole, a.Packets, a.Bytes)
	}
	s.networkAlerts = netAlerts

	// Update gauges from the latest detection results.
	activeKeys := make(map[attackKey]struct{})
	for _, a := range netAlerts {
		activeKeys[attackKey{attack: a.Attack, target: a.TargetIP, ipVersion: a.IPVersion, ipProto: a.Protocol, port: a.Port, portRole: a.PortRole}] = struct{}{}
	}
	s.metrics.activeAttacks.Set(float64(len(activeKeys)))
	s.metrics.flaggedVMs.Set(float64(len(netAlerts)))

	now := time.Now()
	for addr, es := range s.exelets {
		if es.LastReport.IsZero() {
			continue
		}
		s.metrics.exeletLastReportSeconds.WithLabelValues(addr).Set(now.Sub(es.LastReport).Seconds())
	}

	if s.onIngest != nil {
		s.onIngest()
	}

	return nil
}

// VMSummary holds per-VM traffic counters from the latest interval.
type VMSummary struct {
	VMID      string
	UserID    string
	TxBytes   uint64
	TxPackets uint64
	RxBytes   uint64
	RxPackets uint64
	FlowCount int
}

// HostSummary holds per-host traffic data from the latest interval.
type HostSummary struct {
	HostID     string
	Timestamp  int64
	IntervalMs int64
	VMs        []VMSummary
}

// HostSummaries returns a snapshot of per-host, per-VM traffic from the latest interval.
func (s *Store) HostSummaries() []HostSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []HostSummary
	for hostID, perHost := range s.data {
		hs := HostSummary{HostID: hostID}
		for _, history := range perHost {
			if len(history.items) == 0 {
				continue
			}
			latest := history.items[len(history.items)-1]
			vm := latest.Report
			if latest.Timestamp > hs.Timestamp {
				hs.Timestamp = latest.Timestamp
				hs.IntervalMs = latest.IntervalMs
			}
			hs.VMs = append(hs.VMs, VMSummary{
				VMID:      vm.VmID,
				UserID:    vm.UserID,
				TxBytes:   vm.TxBytes,
				TxPackets: vm.TxPackets,
				RxBytes:   vm.RxBytes,
				RxPackets: vm.RxPackets,
				FlowCount: len(vm.Flows),
			})
		}
		if len(hs.VMs) > 0 {
			out = append(out, hs)
		}
	}
	return out
}

// UserSummary holds per-user aggregate traffic from the latest interval.
type UserSummary struct {
	UserID    string
	VMCount   int
	TxBytes   uint64
	TxPackets uint64
	RxBytes   uint64
	RxPackets uint64
	FlowCount int
}

// UserSummaries returns per-user aggregate traffic, sorted by TxBytes descending.
func (s *Store) UserSummaries() []UserSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	byUser := make(map[string]*UserSummary)
	for _, perHost := range s.data {
		for _, history := range perHost {
			if len(history.items) == 0 {
				continue
			}
			vm := history.items[len(history.items)-1].Report
			us, ok := byUser[vm.UserID]
			if !ok {
				us = &UserSummary{UserID: vm.UserID}
				byUser[vm.UserID] = us
			}
			us.VMCount++
			us.TxBytes += vm.TxBytes
			us.TxPackets += vm.TxPackets
			us.RxBytes += vm.RxBytes
			us.RxPackets += vm.RxPackets
			us.FlowCount += len(vm.Flows)
		}
	}
	out := make([]UserSummary, 0, len(byUser))
	for _, us := range byUser {
		out = append(out, *us)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TxBytes > out[j].TxBytes })
	return out
}

// TopFlow holds an aggregated attack-pattern flow across all VMs.
type TopFlow struct {
	Attack    string
	TargetIP  string
	Protocol  uint32
	Port      uint32
	PortRole  string
	Packets   uint64
	Bytes     uint64
	VMCount   int
	Threshold uint64
}

// TopFlows returns the top-k attack-pattern flows aggregated across all VMs,
// sorted by packet count descending. This shows what the detection engine sees
// before thresholds are applied.
func (s *Store) TopFlows(k int) []TopFlow {
	s.mu.Lock()
	defer s.mu.Unlock()

	type flowAgg struct {
		TopFlow
		vms map[string]struct{}
	}
	aggs := make(map[attackKey]*flowAgg)

	var latestIntervalMs int64
	for _, perHost := range s.data {
		for _, history := range perHost {
			if len(history.items) == 0 {
				continue
			}
			latest := history.items[len(history.items)-1]
			vm := latest.Report
			if latest.IntervalMs > latestIntervalMs {
				latestIntervalMs = latest.IntervalMs
			}
			classified := classifyFlows(vm.Flows)
			for key, agg := range classified {
				fa, ok := aggs[key]
				if !ok {
					fa = &flowAgg{
						TopFlow: TopFlow{
							Attack:   key.attack,
							TargetIP: key.target,
							Protocol: key.ipProto,
							Port:     key.port,
							PortRole: key.portRole,
						},
						vms: make(map[string]struct{}),
					}
					aggs[key] = fa
				}
				fa.Packets += agg.packets
				fa.Bytes += agg.bytes
				fa.vms[vm.VmID] = struct{}{}
			}
		}
	}

	intervalSeconds := uint64(latestIntervalMs) / 1000
	if intervalSeconds == 0 {
		intervalSeconds = 1
	}

	out := make([]TopFlow, 0, len(aggs))
	for _, fa := range aggs {
		fa.VMCount = len(fa.vms)
		base, ok := attackThresholds[fa.Attack]
		if ok {
			fa.Threshold = base * intervalSeconds / flowSeconds
			if fa.Threshold == 0 {
				fa.Threshold = base
			}
		}
		out = append(out, fa.TopFlow)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Packets > out[j].Packets })
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// ActiveAlerts returns the current network-level alerts.
func (s *Store) ActiveAlerts() []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Alert, len(s.networkAlerts))
	copy(out, s.networkAlerts)
	return out
}

type alertHistory struct {
	max   int
	items []Alert
}

func (h *alertHistory) add(v Alert) {
	if h.max <= 0 {
		return
	}
	if len(h.items) < h.max {
		h.items = append(h.items, v)
		return
	}
	copy(h.items, h.items[1:])
	h.items[len(h.items)-1] = v
}

const (
	flowSeconds = 2

	// minContributorPackets is the absolute floor for flagging a contributor.
	// Even if the threshold percentage is lower, we won't flag VMs with fewer
	// packets than this.
	minContributorPackets = 1000
)

var attackThresholds = map[string]uint64{
	"ip_flood":          1_000_000,
	"ip_fragmentation":  500_000,
	"icmp_flood":        500_000,
	"udp_amplification": 500_000,
	"udp_flood":         500_000,
	"tcp_amplification": 500_000,
	"tcp_flood":         500_000,
}

type attackKey struct {
	attack    string
	target    string
	ipVersion uint32
	ipProto   uint32
	port      uint32
	portRole  string
}

type attackAgg struct {
	packets uint64
	bytes   uint64
}

// classifyFlows extracts attack signals from flow records, aggregating
// by (attack type, target, protocol, port).
func classifyFlows(flows []*api.FlowRecord) map[attackKey]*attackAgg {
	aggs := make(map[attackKey]*attackAgg)
	add := func(key attackKey, packets, bytes uint64) {
		if packets == 0 {
			return
		}
		if agg, ok := aggs[key]; ok {
			agg.packets += packets
			agg.bytes += bytes
			return
		}
		aggs[key] = &attackAgg{packets: packets, bytes: bytes}
	}

	for _, f := range flows {
		if f.DstIP == "" || f.IpProto == 0 {
			continue
		}
		add(attackKey{
			attack:    "ip_flood",
			target:    f.DstIP,
			ipVersion: f.IpVersion,
			ipProto:   f.IpProto,
			port:      f.IpProto,
			portRole:  "proto",
		}, f.Packets, f.Bytes)
		if f.Fragment {
			add(attackKey{
				attack:    "ip_fragmentation",
				target:    f.DstIP,
				ipVersion: f.IpVersion,
				ipProto:   f.IpProto,
				port:      f.IpProto,
				portRole:  "proto",
			}, f.Packets, f.Bytes)
		}
		switch f.IpProto {
		case 1, 58:
			add(attackKey{
				attack:    "icmp_flood",
				target:    f.DstIP,
				ipVersion: f.IpVersion,
				ipProto:   f.IpProto,
				port:      f.IcmpType,
				portRole:  "icmp_type",
			}, f.Packets, f.Bytes)
		case 6:
			if f.DstPort != 0 {
				add(attackKey{
					attack:    "tcp_flood",
					target:    f.DstIP,
					ipVersion: f.IpVersion,
					ipProto:   f.IpProto,
					port:      f.DstPort,
					portRole:  "dst_port",
				}, f.Packets, f.Bytes)
			}
			if (f.TcpFlags&0x12) == 0x12 && f.SrcPort != 0 {
				add(attackKey{
					attack:    "tcp_amplification",
					target:    f.DstIP,
					ipVersion: f.IpVersion,
					ipProto:   f.IpProto,
					port:      f.SrcPort,
					portRole:  "src_port",
				}, f.Packets, f.Bytes)
			}
		case 17:
			if f.DstPort != 0 {
				add(attackKey{
					attack:    "udp_flood",
					target:    f.DstIP,
					ipVersion: f.IpVersion,
					ipProto:   f.IpProto,
					port:      f.DstPort,
					portRole:  "dst_port",
				}, f.Packets, f.Bytes)
			}
			if f.SrcPort != 0 {
				add(attackKey{
					attack:    "udp_amplification",
					target:    f.DstIP,
					ipVersion: f.IpVersion,
					ipProto:   f.IpProto,
					port:      f.SrcPort,
					portRole:  "src_port",
				}, f.Packets, f.Bytes)
			}
		}
	}
	return aggs
}

func detectVM(report *api.FlowStatsReport, vm *api.VMFlowStats) []Alert {
	if len(vm.Flows) == 0 {
		return nil
	}

	intervalSeconds := uint64(report.IntervalMs) / 1000
	if intervalSeconds == 0 {
		intervalSeconds = 1
	}

	classified := classifyFlows(vm.Flows)

	var alerts []Alert
	for key, agg := range classified {
		base, ok := attackThresholds[key.attack]
		if !ok {
			continue
		}
		threshold := base * intervalSeconds / flowSeconds
		if threshold == 0 {
			threshold = base
		}
		if agg.packets < threshold {
			continue
		}
		alerts = append(alerts, Alert{
			Timestamp:  report.TsUnixMs,
			HostID:     report.HostID,
			VMID:       vm.VmID,
			UserID:     vm.UserID,
			Attack:     key.attack,
			IPVersion:  key.ipVersion,
			TargetIP:   key.target,
			Protocol:   key.ipProto,
			Port:       key.port,
			PortRole:   key.portRole,
			Packets:    agg.packets,
			Bytes:      agg.bytes,
			Threshold:  threshold,
			IntervalMs: report.IntervalMs,
		})
	}
	return alerts
}

// networkContribution tracks one VM's contribution to network-level traffic
// toward a specific attack key.
type networkContribution struct {
	hostID  string
	vmID    string
	userID  string
	packets uint64
	bytes   uint64
}

type networkAgg struct {
	total        attackAgg
	contributors []networkContribution
}

// detectNetwork scans all VMs' latest flow data across all hosts, aggregates
// by target, and flags VMs/users contributing to attacks that exceed network-wide
// thresholds. Must be called with s.mu held.
func (s *Store) detectNetwork(intervalMs int64) []Alert {
	intervalSeconds := uint64(intervalMs) / 1000
	if intervalSeconds == 0 {
		intervalSeconds = 1
	}

	aggs := make(map[attackKey]*networkAgg)

	for hostID, perHost := range s.data {
		for _, history := range perHost {
			if len(history.items) == 0 {
				continue
			}
			latest := history.items[len(history.items)-1]
			vm := latest.Report

			classified := classifyFlows(vm.Flows)
			for key, agg := range classified {
				na, ok := aggs[key]
				if !ok {
					na = &networkAgg{}
					aggs[key] = na
				}
				na.total.packets += agg.packets
				na.total.bytes += agg.bytes
				na.contributors = append(na.contributors, networkContribution{
					hostID:  hostID,
					vmID:    vm.VmID,
					userID:  vm.UserID,
					packets: agg.packets,
					bytes:   agg.bytes,
				})
			}
		}
	}

	var alerts []Alert
	for key, na := range aggs {
		base, ok := attackThresholds[key.attack]
		if !ok {
			continue
		}
		threshold := base * intervalSeconds / flowSeconds
		if threshold == 0 {
			threshold = base
		}
		if na.total.packets < threshold {
			continue
		}

		// Attack detected network-wide. Flag meaningful contributors.
		minContrib := threshold / 100
		if minContrib < minContributorPackets {
			minContrib = minContributorPackets
		}

		for _, c := range na.contributors {
			if c.packets < minContrib {
				continue
			}
			alerts = append(alerts, Alert{
				Timestamp:  time.Now().UnixMilli(),
				HostID:     c.hostID,
				VMID:       c.vmID,
				UserID:     c.userID,
				Attack:     key.attack,
				IPVersion:  key.ipVersion,
				TargetIP:   key.target,
				Protocol:   key.ipProto,
				Port:       key.port,
				PortRole:   key.portRole,
				Packets:    c.packets,
				Bytes:      c.bytes,
				Threshold:  threshold,
				IntervalMs: intervalMs,
			})
		}
	}
	return alerts
}

const ddosCSS = `* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: 'SF Mono', 'Menlo', 'Consolas', monospace; font-size: 12px; background: #fff; color: #24292f; }
.topbar {
  display: flex; gap: 12px; align-items: center;
  padding: 6px 12px; background: #f6f8fa; border-bottom: 1px solid #d0d7de;
}
.topbar .title { font-weight: bold; color: #24292f; }
.topbar a { color: #0969da; text-decoration: none; }
.topbar a:hover { text-decoration: underline; }
h2 { padding: 8px 12px 4px; font-size: 13px; color: #24292f; }
table { width: 100%; border-collapse: collapse; white-space: nowrap; }
th {
  background: #f6f8fa; color: #57606a;
  text-align: right; padding: 3px 8px; border-bottom: 1px solid #d0d7de;
  font-weight: normal; font-size: 11px; position: sticky; top: 0; z-index: 2;
}
th:first-child { text-align: left; }
td { padding: 2px 8px; border-bottom: 1px solid #eaeef2; text-align: right; }
td:first-child { text-align: left; }
tr:hover td { background: #eaeef2; }
.zero { color: #8c959f; }
.alert-row { background: #ffebe9; }
.alert-row:hover td { background: #ffcecb; }
.stale { color: #cf222e; }
.ok { color: #1a7f37; }
.section { padding: 0 12px; margin-bottom: 16px; }
`

func fmtBytesHTML(b uint64) string {
	if b == 0 {
		return `<span class="zero">0</span>`
	}
	switch {
	case b < 1024:
		return fmt.Sprintf("%dB", b)
	case b < 1024*1024:
		return fmt.Sprintf("%.1fK", float64(b)/1024)
	case b < 1024*1024*1024:
		return fmt.Sprintf("%.1fM", float64(b)/(1024*1024))
	default:
		return fmt.Sprintf("%.1fG", float64(b)/(1024*1024*1024))
	}
}

func fmtCountHTML(n uint64) string {
	if n == 0 {
		return `<span class="zero">0</span>`
	}
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

func fmtIntHTML(n int) string {
	if n == 0 {
		return `<span class="zero">0</span>`
	}
	return fmt.Sprintf("%d", n)
}

func fmtPctHTML(packets, threshold uint64) string {
	if threshold == 0 {
		return `<span class="zero">-</span>`
	}
	pct := float64(packets) / float64(threshold) * 100
	if pct >= 100 {
		return fmt.Sprintf(`<b>%.0f%%</b>`, pct)
	}
	if pct >= 10 {
		return fmt.Sprintf("%.0f%%", pct)
	}
	if pct >= 1 {
		return fmt.Sprintf("%.1f%%", pct)
	}
	return `<span class="zero">&lt;1%%</span>`
}

// NewMux creates an HTTP mux with /healthz, /metrics, and debug endpoints.
func NewMux(registry *prometheus.Registry, st *Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if registry != nil {
		mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/debug/", http.StatusFound)
	})
	mux.HandleFunc("/debug/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html>
<html><head><title>pktflowaggd debug</title></head><body>
<h1>pktflowaggd debug</h1>
<ul>
    <li><a href="/metrics">metrics</a></li>
    <li><a href="/healthz">healthz</a></li>
    <li><a href="/debug/ddos">ddos</a></li>
</ul>
</body></html>
`)
	})
	mux.HandleFunc("/debug/ddos", handleDebugDDoS(st))
	mux.HandleFunc("/debug/ddos/hosts", handleDebugDDoSHosts(st))
	return mux
}

func handleDebugDDoS(st *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html>\n<html><head><title>pktflowaggd ddos</title>\n<style>%s</style>\n</head><body>\n", ddosCSS)
		fmt.Fprintf(w, `<div class="topbar"><span class="title">pktflowaggd</span> <a href="/debug/ddos">overview</a> <a href="/debug/ddos/hosts">hosts</a></div>`+"\n")

		// Exelets.
		statuses := st.ExeletStatuses()
		fmt.Fprintf(w, "<h2>exelets</h2>\n<div class=\"section\">\n")
		fmt.Fprintf(w, "<table><tr><th>addr</th><th>host</th><th>status</th><th>age</th><th>reports</th><th>error</th></tr>\n")
		now := time.Now()
		for _, es := range statuses {
			age := ""
			ageClass := ""
			if !es.LastReport.IsZero() {
				ageDur := now.Sub(es.LastReport).Truncate(time.Second)
				age = ageDur.String()
				if ageDur > 30*time.Second {
					ageClass = " stale"
				}
			}
			status := `<span class="ok">connected</span>`
			if !es.Connected {
				status = `<span class="stale">disconnected</span>`
			}
			lastErr := ""
			if es.LastErrorMsg != "" {
				lastErr = es.LastErrorMsg
			}
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td class=\"%s\">%s</td><td>%d</td><td>%s</td></tr>\n",
				es.Addr, es.HostID, status, ageClass, age, es.ReportsTotal, lastErr)
		}
		fmt.Fprintf(w, "</table>\n</div>\n")

		// Active alerts.
		alerts := st.ActiveAlerts()
		fmt.Fprintf(w, "<h2>active alerts (%d)</h2>\n<div class=\"section\">\n", len(alerts))
		if len(alerts) > 0 {
			fmt.Fprintf(w, "<table><tr><th>host</th><th>vm</th><th>user</th><th>attack</th><th>target</th><th>proto</th><th>port</th><th>packets</th><th>threshold</th><th>%%</th></tr>\n")
			for _, a := range alerts {
				fmt.Fprintf(w, "<tr class=\"alert-row\"><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
					a.HostID, a.VMID, a.UserID, a.Attack, a.TargetIP, a.Protocol, a.Port,
					fmtCountHTML(a.Packets), fmtCountHTML(a.Threshold), fmtPctHTML(a.Packets, a.Threshold))
			}
			fmt.Fprintf(w, "</table>\n")
		}
		fmt.Fprintf(w, "</div>\n")

		// Top flows.
		topFlows := st.TopFlows(50)
		fmt.Fprintf(w, "<h2>top flows</h2>\n<div class=\"section\">\n")
		if len(topFlows) > 0 {
			fmt.Fprintf(w, "<table><tr><th>attack</th><th>target</th><th>proto</th><th>port</th><th>role</th><th>packets</th><th>bytes</th><th>vms</th><th>threshold</th><th>%%</th></tr>\n")
			for _, f := range topFlows {
				rowClass := ""
				if f.Threshold > 0 && f.Packets >= f.Threshold {
					rowClass = ` class="alert-row"`
				}
				fmt.Fprintf(w, "<tr%s><td>%s</td><td>%s</td><td>%d</td><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%d</td><td>%s</td><td>%s</td></tr>\n",
					rowClass, f.Attack, f.TargetIP, f.Protocol, f.Port, f.PortRole,
					fmtCountHTML(f.Packets), fmtBytesHTML(f.Bytes), f.VMCount,
					fmtCountHTML(f.Threshold), fmtPctHTML(f.Packets, f.Threshold))
			}
			fmt.Fprintf(w, "</table>\n")
		}
		fmt.Fprintf(w, "</div>\n")

		// Per-user aggregate.
		users := st.UserSummaries()
		fmt.Fprintf(w, "<h2>users (%d)</h2>\n<div class=\"section\">\n", len(users))
		if len(users) > 0 {
			fmt.Fprintf(w, "<table><tr><th>user</th><th>vms</th><th>tx bytes</th><th>tx pkts</th><th>rx bytes</th><th>rx pkts</th><th>flows</th></tr>\n")
			for _, u := range users {
				fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
					u.UserID, fmtIntHTML(u.VMCount),
					fmtBytesHTML(u.TxBytes), fmtCountHTML(u.TxPackets),
					fmtBytesHTML(u.RxBytes), fmtCountHTML(u.RxPackets),
					fmtIntHTML(u.FlowCount))
			}
			fmt.Fprintf(w, "</table>\n")
		}
		fmt.Fprintf(w, "</div>\n")

		fmt.Fprintf(w, "</body></html>\n")
	}
}

func handleDebugDDoSHosts(st *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html>\n<html><head><title>pktflowaggd hosts</title>\n<style>%s</style>\n</head><body>\n", ddosCSS)
		fmt.Fprintf(w, `<div class="topbar"><span class="title">pktflowaggd</span> <a href="/debug/ddos">overview</a> <a href="/debug/ddos/hosts">hosts</a></div>`+"\n")

		now := time.Now()
		summaries := st.HostSummaries()
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].HostID < summaries[j].HostID })
		for _, hs := range summaries {
			reportAge := ""
			if hs.Timestamp > 0 {
				ageDur := now.Sub(time.UnixMilli(hs.Timestamp)).Truncate(time.Second)
				reportAge = ageDur.String()
			}
			fmt.Fprintf(w, "<h2>%s <span style=\"font-weight:normal;color:#57606a\">(age %s)</span></h2>\n<div class=\"section\">\n", hs.HostID, reportAge)
			fmt.Fprintf(w, "<table><tr><th>vm</th><th>user</th><th>tx bytes</th><th>tx pkts</th><th>rx bytes</th><th>rx pkts</th><th>flows</th></tr>\n")
			sort.Slice(hs.VMs, func(i, j int) bool { return hs.VMs[i].TxBytes > hs.VMs[j].TxBytes })
			for _, vm := range hs.VMs {
				fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
					vm.VMID, vm.UserID,
					fmtBytesHTML(vm.TxBytes), fmtCountHTML(vm.TxPackets),
					fmtBytesHTML(vm.RxBytes), fmtCountHTML(vm.RxPackets),
					fmtIntHTML(vm.FlowCount))
			}
			fmt.Fprintf(w, "</table>\n</div>\n")
		}

		if len(summaries) == 0 {
			fmt.Fprintf(w, "<div class=\"section\" style=\"padding:16px 12px;color:#57606a\">no host data yet</div>\n")
		}

		fmt.Fprintf(w, "</body></html>\n")
	}
}

// ConsumeExelet connects to an exelet and streams flow stats into the store,
// reconnecting on failure with exponential backoff.
func ConsumeExelet(ctx context.Context, addr string, st *Store) {
	backoff := time.Second
	for {
		err := StreamFromExelet(ctx, addr, st)
		if ctx.Err() != nil {
			return
		}
		st.RecordError(addr, err)
		log.Printf("pktflowaggd: stream from %s failed: %v; reconnecting in %v", addr, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = time.Duration(math.Min(float64(backoff)*2, float64(30*time.Second)))
	}
}

// StreamFromExelet opens a single gRPC stream to an exelet and ingests reports.
func StreamFromExelet(ctx context.Context, addr string, st *Store) error {
	// grpc.NewClient expects "host:port", not "tcp://host:port".
	dialAddr := strings.TrimPrefix(addr, "tcp://")
	conn, err := grpc.NewClient(dialAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	client := api.NewPktFlowServiceClient(conn)
	stream, err := client.StreamFlowStats(ctx, &api.StreamFlowStatsRequest{})
	if err != nil {
		return fmt.Errorf("stream from %s: %w", addr, err)
	}

	for {
		report, err := stream.Recv()
		if err == io.EOF {
			return fmt.Errorf("stream closed by %s", addr)
		}
		if err != nil {
			return fmt.Errorf("recv from %s: %w", addr, err)
		}
		if err := st.Ingest(report); err != nil {
			log.Printf("pktflowaggd: ingest error from %s: %v", addr, err)
		}
		st.RecordReport(addr, report.HostID)
	}
}
