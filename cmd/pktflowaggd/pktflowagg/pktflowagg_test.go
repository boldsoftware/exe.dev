package pktflowagg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"

	api "exe.dev/pkg/api/exe/pktflow/v1"
)

// --- fake gRPC server ---

type fakePktFlowServer struct {
	api.UnimplementedPktFlowServiceServer
	reportCh    chan *api.FlowStatsReport
	streamReady chan struct{}
}

func (s *fakePktFlowServer) StreamFlowStats(_ *api.StreamFlowStatsRequest, stream api.PktFlowService_StreamFlowStatsServer) error {
	select {
	case s.streamReady <- struct{}{}:
	default:
	}
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case report := <-s.reportCh:
			if err := stream.Send(report); err != nil {
				return err
			}
		}
	}
}

// --- test harness ---

type testHarness struct {
	t          *testing.T
	store      *Store
	registry   *prometheus.Registry
	httpServer *httptest.Server
	reportCh   chan *api.FlowStatsReport
	ingestDone chan struct{}
	cancel     context.CancelFunc
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	registry := prometheus.NewRegistry()
	st := NewStore(60, registry)

	ingestDone := make(chan struct{}, 1)
	st.SetOnIngest(func() {
		select {
		case ingestDone <- struct{}{}:
		default:
		}
	})

	reportCh := make(chan *api.FlowStatsReport, 100)

	fake := &fakePktFlowServer{
		reportCh:    reportCh,
		streamReady: make(chan struct{}, 1),
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	api.RegisterPktFlowServiceServer(grpcServer, fake)
	go grpcServer.Serve(lis)
	t.Cleanup(grpcServer.Stop)

	exeletAddr := lis.Addr().String()
	st.RegisterExelet(exeletAddr)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go ConsumeExelet(ctx, exeletAddr, st)

	// Wait for the stream to be established.
	select {
	case <-fake.streamReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream to be established")
	}

	// Also need to wait for the gRPC client to actually start receiving.
	// Give a tiny bit of time for the ConsumeExelet goroutine to enter Recv().
	// We send a dummy report and wait for ingest to confirm the pipeline is ready.
	reportCh <- &api.FlowStatsReport{
		HostID:     "__probe__",
		TsUnixMs:   1,
		IntervalMs: 1000,
	}
	select {
	case <-ingestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for probe ingest")
	}

	httpSrv := httptest.NewServer(NewMux(registry, st))
	t.Cleanup(httpSrv.Close)

	return &testHarness{
		t:          t,
		store:      st,
		registry:   registry,
		httpServer: httpSrv,
		reportCh:   reportCh,
		ingestDone: ingestDone,
		cancel:     cancel,
	}
}

func (h *testHarness) ingest(report *api.FlowStatsReport) {
	h.t.Helper()
	h.reportCh <- report
	select {
	case <-h.ingestDone:
	case <-time.After(5 * time.Second):
		h.t.Fatal("timed out waiting for ingest")
	}
}

// metric scrapes the /metrics endpoint and returns the value of a metric
// with the given name and label set. Returns 0 if the metric is not found.
func (h *testHarness) metric(name string, labels map[string]string) float64 {
	h.t.Helper()
	families, err := h.registry.Gather()
	if err != nil {
		h.t.Fatalf("gather metrics: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
		for _, m := range fam.GetMetric() {
			if !matchLabels(m, labels) {
				continue
			}
			switch fam.GetType() {
			case dto.MetricType_COUNTER:
				return m.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				return m.GetGauge().GetValue()
			}
		}
	}
	return 0
}

// metricEndpointHas verifies the /metrics HTTP endpoint is reachable and
// contains the named metric string.
func (h *testHarness) metricEndpointHas(substr string) bool {
	h.t.Helper()
	resp, err := http.Get(h.httpServer.URL + "/metrics")
	if err != nil {
		h.t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return bytes.Contains(body, []byte(substr))
}

func matchLabels(m *dto.Metric, want map[string]string) bool {
	labels := make(map[string]string, len(m.GetLabel()))
	for _, lp := range m.GetLabel() {
		labels[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// --- flow builders ---

func makeReport(hostID string, intervalMs int64, vms ...*api.VMFlowStats) *api.FlowStatsReport {
	return &api.FlowStatsReport{
		HostID:     hostID,
		TsUnixMs:   1000000,
		IntervalMs: intervalMs,
		SampleRate: 1024,
		Vms:        vms,
	}
}

func makeVM(vmID, userID, tap string, flows ...*api.FlowRecord) *api.VMFlowStats {
	return &api.VMFlowStats{
		VmID:   vmID,
		UserID: userID,
		Tap:    tap,
		Flows:  flows,
	}
}

func udpFlow(dstIP string, dstPort uint32, packets, bytes uint64) *api.FlowRecord {
	return &api.FlowRecord{
		DstIP:     dstIP,
		IpVersion: 4,
		IpProto:   17,
		DstPort:   dstPort,
		Packets:   packets,
		Bytes:     bytes,
	}
}

func tcpFlow(dstIP string, dstPort uint32, flags uint32, packets, bytes uint64) *api.FlowRecord {
	return &api.FlowRecord{
		DstIP:     dstIP,
		IpVersion: 4,
		IpProto:   6,
		DstPort:   dstPort,
		TcpFlags:  flags,
		Packets:   packets,
		Bytes:     bytes,
	}
}

func icmpFlow(dstIP string, icmpType uint32, packets, bytes uint64) *api.FlowRecord {
	return &api.FlowRecord{
		DstIP:     dstIP,
		IpVersion: 4,
		IpProto:   1,
		IcmpType:  icmpType,
		Packets:   packets,
		Bytes:     bytes,
	}
}

func fragFlow(dstIP string, proto uint32, packets, bytes uint64) *api.FlowRecord {
	return &api.FlowRecord{
		DstIP:     dstIP,
		IpVersion: 4,
		IpProto:   proto,
		Fragment:  true,
		Packets:   packets,
		Bytes:     bytes,
	}
}

func hasAlert(alerts []Alert, attack, vmID, userID, targetIP string) bool {
	for _, a := range alerts {
		if a.Attack == attack && a.VMID == vmID && a.UserID == userID && a.TargetIP == targetIP {
			return true
		}
	}
	return false
}

func hasAlertTarget(alerts []Alert, attack, targetIP string) bool {
	for _, a := range alerts {
		if a.Attack == attack && a.TargetIP == targetIP {
			return true
		}
	}
	return false
}

// --- Per-VM detection unit tests (detectVM) ---

func TestDetectVM_UDPFloodAboveThreshold(t *testing.T) {
	// 1 VM sends 600k UDP packets to one target in a 2s interval.
	// udp_flood threshold is 500k per 2s. 600k > 500k → alert.
	// ip_flood threshold is 1M per 2s. 600k < 1M → no ip_flood alert.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 600_000, 600_000*100),
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if !hasAlert(alerts, "udp_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected udp_flood alert, got %+v", alerts)
	}
	if hasAlert(alerts, "ip_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("did not expect ip_flood alert at 600k packets")
	}
}

func TestDetectVM_UDPFloodBelowThreshold(t *testing.T) {
	// 1 VM sends 100k UDP packets. 100k < 500k threshold → no alert.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 100_000, 100_000*100),
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected no alerts, got %+v", alerts)
	}
}

func TestDetectVM_ICMPFlood(t *testing.T) {
	// 1 VM sends 600k ICMP echo-request packets. 600k > 500k → icmp_flood alert.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			icmpFlow("10.0.0.1", 8, 600_000, 600_000*64),
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if !hasAlert(alerts, "icmp_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected icmp_flood alert, got %+v", alerts)
	}
}

func TestDetectVM_TCPFlood(t *testing.T) {
	// 1 VM sends 600k TCP SYN packets to port 80. 600k > 500k → tcp_flood alert.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			tcpFlow("10.0.0.1", 80, 0x02, 600_000, 600_000*60),
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if !hasAlert(alerts, "tcp_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected tcp_flood alert, got %+v", alerts)
	}
}

func TestDetectVM_UDPAmplification(t *testing.T) {
	// 1 VM sends 600k UDP packets with a well-known src_port (e.g. DNS 53).
	// 600k > 500k udp_amplification threshold → alert keyed on src_port.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			&api.FlowRecord{
				DstIP: "10.0.0.1", IpVersion: 4, IpProto: 17,
				SrcPort: 53, DstPort: 80,
				Packets: 600_000, Bytes: 600_000 * 512,
			},
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if !hasAlert(alerts, "udp_amplification", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected udp_amplification alert, got %+v", alerts)
	}
}

func TestDetectVM_TCPAmplification(t *testing.T) {
	// 1 VM sends 600k TCP SYN-ACK (flags 0x12) packets with src_port 80.
	// 600k > 500k tcp_amplification threshold → alert keyed on src_port.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			&api.FlowRecord{
				DstIP: "10.0.0.1", IpVersion: 4, IpProto: 6,
				SrcPort: 80, DstPort: 12345,
				TcpFlags: 0x12,
				Packets:  600_000, Bytes: 600_000 * 60,
			},
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if !hasAlert(alerts, "tcp_amplification", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected tcp_amplification alert, got %+v", alerts)
	}
}

func TestDetectVM_IPFragmentation(t *testing.T) {
	// 1 VM sends 600k fragmented UDP packets. 600k > 500k → ip_fragmentation alert.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			fragFlow("10.0.0.1", 17, 600_000, 600_000*1400),
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if !hasAlert(alerts, "ip_fragmentation", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected ip_fragmentation alert, got %+v", alerts)
	}
}

func TestDetectVM_IntervalScaling(t *testing.T) {
	// 10s interval (5x flowSeconds=2) scales thresholds up 5x.
	// udp_flood base=500k → scaled=2.5M. 600k < 2.5M → no alert.
	// Then 3M > 2.5M → alert.
	st := NewStore(60, nil)
	report := makeReport("host1", 10_000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 600_000, 600_000*100),
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if hasAlert(alerts, "udp_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("600k packets should not trigger at 10s interval (threshold 2.5M)")
	}

	// Create a new store to reset state, then test with 3M packets.
	st2 := NewStore(60, nil)
	report.Vms[0].Flows[0].Packets = 3_000_000
	st2.Ingest(report)
	alerts = st2.ActiveAlerts()
	if !hasAlert(alerts, "udp_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("3M packets should trigger at 10s interval")
	}
}

func TestDetectVM_NoFlows(t *testing.T) {
	// VM with counter data but no sampled flows → no alert.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0"),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if len(alerts) != 0 {
		t.Errorf("expected no alerts for VM with no flows")
	}
}

func TestDetectVM_MultipleAttackTypes(t *testing.T) {
	// 1 VM sends 600k UDP + 600k ICMP simultaneously → both udp_flood and icmp_flood alerts.
	st := NewStore(60, nil)
	report := makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 600_000, 600_000*100),
			icmpFlow("10.0.0.1", 8, 600_000, 600_000*64),
		),
	)
	st.Ingest(report)
	alerts := st.ActiveAlerts()
	if !hasAlert(alerts, "udp_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected udp_flood alert")
	}
	if !hasAlert(alerts, "icmp_flood", "vm1", "user1", "10.0.0.1") {
		t.Errorf("expected icmp_flood alert")
	}
}

// --- Harness: single-VM DDoS via metrics ---

func TestHarness_SingleVMFlood(t *testing.T) {
	// 1 VM sends 600k UDP packets (above 500k threshold).
	// Verify via prometheus metrics: alerts_total increments, active_attacks > 0,
	// flagged_vms > 0, and /metrics HTTP endpoint serves the data.
	h := newTestHarness(t)

	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 600_000, 600_000*100),
		),
	))

	// ingest_reports_total is 2 because of the probe report.
	if v := h.metric("pktflow_ingest_reports_total", nil); v != 2 {
		t.Errorf("expected ingest_reports_total=2, got %v", v)
	}
	if v := h.metric("pktflow_network_alerts_total", map[string]string{"attack": "udp_flood"}); v == 0 {
		t.Error("expected pktflow_network_alerts_total{attack=udp_flood} > 0")
	}
	if v := h.metric("pktflow_network_active_attacks", nil); v == 0 {
		t.Error("expected active_attacks > 0")
	}
	if v := h.metric("pktflow_network_flagged_vms", nil); v == 0 {
		t.Error("expected flagged_vms > 0")
	}
	if !h.metricEndpointHas("pktflow_network_alerts_total") {
		t.Error("expected /metrics endpoint to contain pktflow_network_alerts_total")
	}
}

func TestHarness_NormalTrafficNoAlert(t *testing.T) {
	// Normal traffic: 1k UDP + 5k TCP packets, both well below all thresholds.
	// Verify: ingest_reports_total increments but no active attacks or flagged VMs.
	h := newTestHarness(t)

	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 1000, 1000*100),
			tcpFlow("10.0.0.2", 443, 0x10, 5000, 5000*1400),
		),
	))

	if v := h.metric("pktflow_ingest_reports_total", nil); v != 2 {
		t.Errorf("expected ingest_reports_total=2, got %v", v)
	}
	if v := h.metric("pktflow_network_active_attacks", nil); v != 0 {
		t.Errorf("expected no active attacks, got %v", v)
	}
	if v := h.metric("pktflow_network_flagged_vms", nil); v != 0 {
		t.Errorf("expected no flagged VMs, got %v", v)
	}
}

// --- Harness: cross-VM same-user aggregation via metrics ---

func TestHarness_MultiVMSameUser(t *testing.T) {
	h := newTestHarness(t)

	// 25 VMs owned by user1, each sending 30k UDP packets to the same target.
	// Per-VM: 30k is far below 500k threshold.
	// Aggregate: 25 * 30k = 750k, which exceeds 500k.
	var vms []*api.VMFlowStats
	for i := 0; i < 25; i++ {
		vms = append(vms, makeVM(
			fmt.Sprintf("vm%d", i), "user1", fmt.Sprintf("tap%d", i),
			udpFlow("10.0.0.1", 53, 30_000, 30_000*100),
		))
	}
	h.ingest(makeReport("host1", 2000, vms...))

	if v := h.metric("pktflow_network_alerts_total", map[string]string{"attack": "udp_flood"}); v == 0 {
		t.Error("expected network alert for udp_flood from 25 VMs aggregating to 750k")
	}
	if v := h.metric("pktflow_network_flagged_vms", nil); v != 25 {
		t.Errorf("expected 25 flagged VMs (each contributing 30k > minContrib), got %v", v)
	}
}

func TestHarness_MultiVMBelowAggregate(t *testing.T) {
	h := newTestHarness(t)

	// 5 VMs each sending 10k packets — aggregate 50k, still below 500k.
	var vms []*api.VMFlowStats
	for i := 0; i < 5; i++ {
		vms = append(vms, makeVM(
			fmt.Sprintf("vm%d", i), "user1", fmt.Sprintf("tap%d", i),
			udpFlow("10.0.0.1", 53, 10_000, 10_000*100),
		))
	}
	h.ingest(makeReport("host1", 2000, vms...))

	if v := h.metric("pktflow_network_active_attacks", nil); v != 0 {
		t.Errorf("50k aggregate should not trigger, got active_attacks=%v", v)
	}
}

// --- Harness: cross-user (inter-user) network-level DDoS ---

func TestHarness_MultipleUsersAttackSameTarget(t *testing.T) {
	// 25 different users, 1 VM each, all attacking 10.0.0.1:53 with 30k packets.
	// Per-VM: 30k < 500k threshold. Aggregate: 25*30k = 750k > 500k → network alert.
	// All 25 users should be flagged (each 30k > minContrib 5k).
	h := newTestHarness(t)

	var vms []*api.VMFlowStats
	for i := 0; i < 25; i++ {
		vms = append(vms, makeVM(
			fmt.Sprintf("vm%d", i),
			fmt.Sprintf("user%d", i),
			fmt.Sprintf("tap%d", i),
			udpFlow("10.0.0.1", 53, 30_000, 30_000*100),
		))
	}
	h.ingest(makeReport("host1", 2000, vms...))

	if v := h.metric("pktflow_network_alerts_total", map[string]string{"attack": "udp_flood"}); v == 0 {
		t.Error("expected network-level udp_flood alert for 25 users * 30k = 750k")
	}

	// All 25 users should be flagged (each contributing 30k > minContrib=5k).
	alerts := h.store.ActiveAlerts()
	for i := 0; i < 25; i++ {
		userID := fmt.Sprintf("user%d", i)
		found := false
		for _, a := range alerts {
			if a.UserID == userID && a.TargetIP == "10.0.0.1" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected alert for contributing user %s", userID)
		}
	}
}

func TestHarness_LargeScaleMultiUserDDoS(t *testing.T) {
	// 50 compromised accounts, 1 VM each, 20k UDP packets each → aggregate 1M > 500k.
	// Plus 10 innocent users sending 50 packets each to the same target.
	// Bad users (20k > minContrib 5k) should be flagged.
	// Innocent users (50 < minContrib 5k) should NOT be flagged.
	h := newTestHarness(t)

	var vms []*api.VMFlowStats
	for i := 0; i < 50; i++ {
		vms = append(vms, makeVM(
			fmt.Sprintf("vm-bad-%d", i),
			fmt.Sprintf("bad-user%d", i),
			fmt.Sprintf("tap-b%d", i),
			udpFlow("10.0.0.1", 53, 20_000, 20_000*100),
		))
	}
	for i := 0; i < 10; i++ {
		vms = append(vms, makeVM(
			fmt.Sprintf("vm-good-%d", i),
			fmt.Sprintf("good-user%d", i),
			fmt.Sprintf("tap-g%d", i),
			udpFlow("10.0.0.1", 53, 50, 50*100),
		))
	}
	h.ingest(makeReport("host1", 2000, vms...))

	if v := h.metric("pktflow_network_alerts_total", map[string]string{"attack": "udp_flood"}); v == 0 {
		t.Error("expected alert for 50*20k = 1M packets to 10.0.0.1")
	}

	alerts := h.store.ActiveAlerts()

	// Each bad user should be flagged.
	for i := 0; i < 50; i++ {
		userID := fmt.Sprintf("bad-user%d", i)
		found := false
		for _, a := range alerts {
			if a.UserID == userID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected alert for attacking user %s", userID)
		}
	}

	// Innocent users sending 50 packets should NOT be flagged.
	for i := 0; i < 10; i++ {
		userID := fmt.Sprintf("good-user%d", i)
		for _, a := range alerts {
			if a.UserID == userID {
				t.Errorf("innocent user %s should not be flagged (only 50 packets)", userID)
			}
		}
	}
}

// --- Harness: cross-host aggregation ---

func TestHarness_CrossHostAggregation(t *testing.T) {
	// 2 hosts each report 300k UDP packets to the same target.
	// After host1: 300k < 500k threshold → no alert.
	// After host2: 300k + 300k = 600k > 500k → network alert, 2 flagged VMs.
	// Verifies that pktflowaggd aggregates flows arriving from different hosts.
	h := newTestHarness(t)
	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 300_000, 300_000*100),
		),
	))

	// After first ingest, 300k < 500k threshold — no alert.
	if v := h.metric("pktflow_network_active_attacks", nil); v != 0 {
		t.Errorf("300k alone should not trigger, got active_attacks=%v", v)
	}

	h.ingest(makeReport("host2", 2000,
		makeVM("vm2", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 300_000, 300_000*100),
		),
	))

	// After second ingest, 300k + 300k = 600k > 500k — alert.
	if v := h.metric("pktflow_network_alerts_total", map[string]string{"attack": "udp_flood"}); v == 0 {
		t.Error("expected cross-host aggregate alert for 600k packets")
	}
	if v := h.metric("pktflow_network_flagged_vms", nil); v != 2 {
		t.Errorf("expected 2 flagged VMs (one per host), got %v", v)
	}
}

// --- Harness: different targets should NOT aggregate ---

func TestHarness_DifferentTargetsNoAggregation(t *testing.T) {
	// 25 VMs each send 30k UDP packets to a DIFFERENT target IP.
	// Per-target: 30k < 500k → no alert for any target.
	// Verifies that flows to different targets are not aggregated together.
	h := newTestHarness(t)
	var vms []*api.VMFlowStats
	for i := 0; i < 25; i++ {
		vms = append(vms, makeVM(
			fmt.Sprintf("vm%d", i),
			fmt.Sprintf("user%d", i),
			fmt.Sprintf("tap%d", i),
			udpFlow(fmt.Sprintf("10.0.0.%d", i+1), 53, 30_000, 30_000*100),
		))
	}
	h.ingest(makeReport("host1", 2000, vms...))

	if v := h.metric("pktflow_network_active_attacks", nil); v != 0 {
		t.Errorf("different targets should not aggregate, got active_attacks=%v", v)
	}
}

// --- Harness: mixed attack types via metrics ---

func TestHarness_MultipleAttackTypes(t *testing.T) {
	// 1 VM sends 600k UDP + 600k ICMP packets to the same target simultaneously.
	// Both exceed 500k threshold → both udp_flood and icmp_flood alerts fire.
	// Verifies independent detection of concurrent attack types via metrics.
	h := newTestHarness(t)

	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 600_000, 600_000*100),
			icmpFlow("10.0.0.1", 8, 600_000, 600_000*64),
		),
	))

	if v := h.metric("pktflow_network_alerts_total", map[string]string{"attack": "udp_flood"}); v == 0 {
		t.Error("expected udp_flood alert")
	}
	if v := h.metric("pktflow_network_alerts_total", map[string]string{"attack": "icmp_flood"}); v == 0 {
		t.Error("expected icmp_flood alert")
	}
}

// --- Harness: attack clears when traffic drops ---

func TestHarness_AttackClearsWhenTrafficDrops(t *testing.T) {
	// Interval 1: VM sends 600k UDP packets → active_attacks=1, flagged_vms=1.
	// Interval 2: same VM sends 100 packets (normal traffic).
	// active_attacks and flagged_vms gauges should reset to 0.
	// Verifies that detection is based on latest data, not cumulative history.
	h := newTestHarness(t)

	// First interval: flood.
	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 600_000, 600_000*100),
		),
	))
	if v := h.metric("pktflow_network_active_attacks", nil); v == 0 {
		t.Fatal("expected active attack during flood")
	}

	// Second interval: traffic drops to normal.
	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 100, 100*100),
		),
	))
	if v := h.metric("pktflow_network_active_attacks", nil); v != 0 {
		t.Errorf("expected attack to clear after traffic drops, got active_attacks=%v", v)
	}
	if v := h.metric("pktflow_network_flagged_vms", nil); v != 0 {
		t.Errorf("expected flagged_vms=0 after traffic drops, got %v", v)
	}
}

// --- Harness: validation ---

func TestHarness_ValidationErrors(t *testing.T) {
	// Reports missing required fields should return errors from ingest.
	st := NewStore(60, nil)

	cases := []struct {
		name   string
		report *api.FlowStatsReport
	}{
		{"missing host_id", &api.FlowStatsReport{TsUnixMs: 1, IntervalMs: 2000}},
		{"missing timestamp", &api.FlowStatsReport{HostID: "h", IntervalMs: 2000}},
		{"missing interval", &api.FlowStatsReport{HostID: "h", TsUnixMs: 1}},
	}
	for _, tc := range cases {
		if err := st.Ingest(tc.report); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

func TestHarness_MetricsEndpoint(t *testing.T) {
	// GET /metrics should return HTTP 200 with Prometheus exposition format.
	// Verifies the metrics endpoint is wired up and serving.
	h := newTestHarness(t)

	resp, err := http.Get(h.httpServer.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from /metrics, got %d", resp.StatusCode)
	}
}

// --- Exelet status tracking ---

func TestExeletStatus_RegisterAndRecord(t *testing.T) {
	st := NewStore(60, nil)

	st.RegisterExelet("10.0.0.1:9090")
	st.RegisterExelet("10.0.0.2:9090")

	statuses := st.ExeletStatuses()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 exelets, got %d", len(statuses))
	}

	// Before any reports, both should be disconnected.
	for _, s := range statuses {
		if s.Connected {
			t.Errorf("%s should not be connected before any reports", s.Addr)
		}
	}

	st.RecordReport("10.0.0.1:9090", "host1")
	st.RecordReport("10.0.0.1:9090", "host1")
	st.RecordReport("10.0.0.1:9090", "host1")

	statuses = st.ExeletStatuses()
	var s1, s2 ExeletStatus
	for _, s := range statuses {
		switch s.Addr {
		case "10.0.0.1:9090":
			s1 = s
		case "10.0.0.2:9090":
			s2 = s
		}
	}

	if !s1.Connected {
		t.Error("10.0.0.1:9090 should be connected after reports")
	}
	if s1.ReportsTotal != 3 {
		t.Errorf("expected 3 reports, got %d", s1.ReportsTotal)
	}
	if s1.LastReport.IsZero() {
		t.Error("expected LastReport to be set")
	}

	if s2.Connected {
		t.Error("10.0.0.2:9090 should not be connected")
	}
	if s2.ReportsTotal != 0 {
		t.Errorf("expected 0 reports for 10.0.0.2, got %d", s2.ReportsTotal)
	}
}

func TestExeletStatus_RecordError(t *testing.T) {
	st := NewStore(60, nil)
	st.RegisterExelet("10.0.0.1:9090")

	// Simulate: received a report (connected), then error.
	st.RecordReport("10.0.0.1:9090", "host1")
	st.RecordError("10.0.0.1:9090", fmt.Errorf("connection refused"))

	statuses := st.ExeletStatuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 exelet, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Connected {
		t.Error("should be disconnected after error")
	}
	if s.ReportsTotal != 1 {
		t.Errorf("expected 1 report, got %d", s.ReportsTotal)
	}
	if s.LastErrorMsg != "connection refused" {
		t.Errorf("expected error msg 'connection refused', got %q", s.LastErrorMsg)
	}
	if s.LastError.IsZero() {
		t.Error("expected LastError to be set")
	}
}

// --- Debug pages ---

func TestHarness_RootRedirect(t *testing.T) {
	h := newTestHarness(t)

	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(h.httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected 302, got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/debug/" {
		t.Errorf("expected redirect to /debug/, got %q", loc)
	}
}

func TestHarness_DebugIndex(t *testing.T) {
	h := newTestHarness(t)

	resp, err := http.Get(h.httpServer.URL + "/debug/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"/metrics", "/healthz", "/debug/ddos"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("debug index missing link to %s", want)
		}
	}
}

func TestHarness_DebugDDoS(t *testing.T) {
	h := newTestHarness(t)

	// Ingest a report so the exelet has a LastReport.
	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 1000, 1000*100),
		),
	))

	resp, err := http.Get(h.httpServer.URL + "/debug/ddos")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !bytes.Contains(body, []byte("exelets")) {
		t.Error("debug/ddos page missing 'exelets' heading")
	}
	if !bytes.Contains(body, []byte("<table")) {
		t.Error("debug/ddos page missing table")
	}
	// The harness registers the exelet address; check it appears.
	statuses := h.store.ExeletStatuses()
	if len(statuses) == 0 {
		t.Fatal("expected at least one exelet status")
	}
	for _, es := range statuses {
		if !bytes.Contains(body, []byte(es.Addr)) {
			t.Errorf("debug/ddos page missing exelet addr %s; body: %s", es.Addr, bodyStr)
		}
	}
}

func TestHarness_ExeletLastReportMetric(t *testing.T) {
	h := newTestHarness(t)

	// The probe ingest sets LastReport via RecordReport (after Ingest returns),
	// but the gauge is updated inside Ingest. A second ingest is needed so
	// Ingest sees the non-zero LastReport and updates the gauge.
	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 100, 100*100),
		),
	))

	if !h.metricEndpointHas("pktflow_exelet_last_report_seconds") {
		t.Error("expected /metrics to contain pktflow_exelet_last_report_seconds")
	}
}

// --- HostSummaries ---

func TestHostSummaries(t *testing.T) {
	st := NewStore(60, nil)
	report := &api.FlowStatsReport{
		HostID:     "host1",
		TsUnixMs:   1000000,
		IntervalMs: 2000,
		SampleRate: 1024,
		Vms: []*api.VMFlowStats{
			{
				VmID: "vm1", UserID: "user1", Tap: "tap0",
				TxBytes: 1000, TxPackets: 10, RxBytes: 2000, RxPackets: 20,
				Flows: []*api.FlowRecord{
					udpFlow("10.0.0.1", 53, 100, 100*100),
				},
			},
			{
				VmID: "vm2", UserID: "user2", Tap: "tap1",
				TxBytes: 3000, TxPackets: 30, RxBytes: 4000, RxPackets: 40,
			},
		},
	}
	if err := st.Ingest(report); err != nil {
		t.Fatal(err)
	}

	summaries := st.HostSummaries()
	if len(summaries) != 1 {
		t.Fatalf("expected 1 host summary, got %d", len(summaries))
	}
	hs := summaries[0]
	if hs.HostID != "host1" {
		t.Errorf("expected host1, got %s", hs.HostID)
	}
	if hs.Timestamp != 1000000 {
		t.Errorf("expected timestamp 1000000, got %d", hs.Timestamp)
	}
	if hs.IntervalMs != 2000 {
		t.Errorf("expected intervalMs 2000, got %d", hs.IntervalMs)
	}
	if len(hs.VMs) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(hs.VMs))
	}

	vmByID := make(map[string]VMSummary)
	for _, vm := range hs.VMs {
		vmByID[vm.VMID] = vm
	}

	vm1 := vmByID["vm1"]
	if vm1.UserID != "user1" {
		t.Errorf("vm1: expected user1, got %s", vm1.UserID)
	}
	if vm1.TxBytes != 1000 || vm1.TxPackets != 10 || vm1.RxBytes != 2000 || vm1.RxPackets != 20 {
		t.Errorf("vm1: unexpected counters: tx=%d/%d rx=%d/%d", vm1.TxBytes, vm1.TxPackets, vm1.RxBytes, vm1.RxPackets)
	}
	if vm1.FlowCount != 1 {
		t.Errorf("vm1: expected 1 flow, got %d", vm1.FlowCount)
	}

	vm2 := vmByID["vm2"]
	if vm2.TxBytes != 3000 || vm2.TxPackets != 30 || vm2.RxBytes != 4000 || vm2.RxPackets != 40 {
		t.Errorf("vm2: unexpected counters: tx=%d/%d rx=%d/%d", vm2.TxBytes, vm2.TxPackets, vm2.RxBytes, vm2.RxPackets)
	}
	if vm2.FlowCount != 0 {
		t.Errorf("vm2: expected 0 flows, got %d", vm2.FlowCount)
	}
}

// --- UserSummaries ---

func TestUserSummaries(t *testing.T) {
	st := NewStore(60, nil)

	// user1 has 2 VMs across 2 hosts; user2 has 1 VM.
	st.Ingest(&api.FlowStatsReport{
		HostID: "host1", TsUnixMs: 1000000, IntervalMs: 2000,
		Vms: []*api.VMFlowStats{
			{VmID: "vm1", UserID: "user1", Tap: "tap0", TxBytes: 1000, TxPackets: 10, RxBytes: 2000, RxPackets: 20},
			{VmID: "vm2", UserID: "user2", Tap: "tap1", TxBytes: 500, TxPackets: 5, RxBytes: 600, RxPackets: 6},
		},
	})
	st.Ingest(&api.FlowStatsReport{
		HostID: "host2", TsUnixMs: 1000000, IntervalMs: 2000,
		Vms: []*api.VMFlowStats{
			{VmID: "vm3", UserID: "user1", Tap: "tap0", TxBytes: 3000, TxPackets: 30, RxBytes: 4000, RxPackets: 40},
		},
	})

	users := st.UserSummaries()
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}

	// Sorted by TxBytes desc, so user1 (4000) first, user2 (500) second.
	if users[0].UserID != "user1" {
		t.Errorf("expected user1 first, got %s", users[0].UserID)
	}
	if users[0].VMCount != 2 {
		t.Errorf("user1: expected 2 VMs, got %d", users[0].VMCount)
	}
	if users[0].TxBytes != 4000 {
		t.Errorf("user1: expected TxBytes=4000, got %d", users[0].TxBytes)
	}
	if users[0].RxBytes != 6000 {
		t.Errorf("user1: expected RxBytes=6000, got %d", users[0].RxBytes)
	}
	if users[1].UserID != "user2" {
		t.Errorf("expected user2 second, got %s", users[1].UserID)
	}
}

// --- TopFlows ---

func TestTopFlows(t *testing.T) {
	st := NewStore(60, nil)
	st.Ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 100_000, 100_000*100),
			udpFlow("10.0.0.2", 80, 50_000, 50_000*100),
		),
		makeVM("vm2", "user2", "tap1",
			udpFlow("10.0.0.1", 53, 200_000, 200_000*100),
		),
	))

	flows := st.TopFlows(10)
	if len(flows) == 0 {
		t.Fatal("expected top flows")
	}

	// The top flow by packets should be the aggregated 10.0.0.1:53 udp_flood
	// (100k + 200k = 300k packets, 2 VMs contributing).
	found := false
	for _, f := range flows {
		if f.Attack == "udp_flood" && f.TargetIP == "10.0.0.1" && f.Port == 53 {
			found = true
			if f.Packets != 300_000 {
				t.Errorf("expected 300k packets, got %d", f.Packets)
			}
			if f.VMCount != 2 {
				t.Errorf("expected 2 VMs, got %d", f.VMCount)
			}
			if f.Threshold == 0 {
				t.Error("expected non-zero threshold")
			}
			break
		}
	}
	if !found {
		t.Error("expected udp_flood flow for 10.0.0.1:53")
	}

	// TopFlows(1) should return only the top flow.
	flows1 := st.TopFlows(1)
	if len(flows1) != 1 {
		t.Errorf("expected 1 flow with k=1, got %d", len(flows1))
	}
}

// --- Debug page HTTP tests ---

func TestHarness_DebugDDoSOverview(t *testing.T) {
	h := newTestHarness(t)

	h.ingest(&api.FlowStatsReport{
		HostID: "host1", TsUnixMs: 1000000, IntervalMs: 2000, SampleRate: 1024,
		Vms: []*api.VMFlowStats{
			{
				VmID: "vm1", UserID: "user1", Tap: "tap0",
				TxBytes: 5000, TxPackets: 50, RxBytes: 6000, RxPackets: 60,
				Flows: []*api.FlowRecord{udpFlow("10.0.0.1", 53, 100, 100*100)},
			},
		},
	})

	resp, err := http.Get(h.httpServer.URL + "/debug/ddos")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Overview page should have exelets, top flows, and users sections.
	for _, want := range []string{"exelets", "top flows", "users", "user1"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("debug/ddos page missing %q; body:\n%s", want, bodyStr)
		}
	}
	// Should link to hosts page.
	if !bytes.Contains(body, []byte("/debug/ddos/hosts")) {
		t.Errorf("debug/ddos page missing link to /debug/ddos/hosts; body:\n%s", bodyStr)
	}
}

func TestHarness_DebugDDoSActiveAlerts(t *testing.T) {
	h := newTestHarness(t)

	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 600_000, 600_000*100),
		),
	))

	alerts := h.store.ActiveAlerts()
	if len(alerts) == 0 {
		t.Fatal("expected active alerts")
	}

	resp, err := http.Get(h.httpServer.URL + "/debug/ddos")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{"active alerts", "udp_flood", "vm1", "user1", "10.0.0.1"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("debug/ddos page missing %q; body:\n%s", want, bodyStr)
		}
	}
}

func TestHarness_DebugDDoSHosts(t *testing.T) {
	h := newTestHarness(t)

	h.ingest(&api.FlowStatsReport{
		HostID: "host1", TsUnixMs: 1000000, IntervalMs: 2000, SampleRate: 1024,
		Vms: []*api.VMFlowStats{
			{
				VmID: "vm1", UserID: "user1", Tap: "tap0",
				TxBytes: 5000, TxPackets: 50, RxBytes: 6000, RxPackets: 60,
				Flows: []*api.FlowRecord{udpFlow("10.0.0.1", 53, 100, 100*100)},
			},
		},
	})

	resp, err := http.Get(h.httpServer.URL + "/debug/ddos/hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{"host1", "vm1", "user1"} {
		if !bytes.Contains(body, []byte(want)) {
			t.Errorf("debug/ddos/hosts page missing %q; body:\n%s", want, bodyStr)
		}
	}
}

func TestHarness_DebugDDoSExeletHostID(t *testing.T) {
	h := newTestHarness(t)

	h.ingest(makeReport("host1", 2000,
		makeVM("vm1", "user1", "tap0",
			udpFlow("10.0.0.1", 53, 100, 100*100),
		),
	))

	resp, err := http.Get(h.httpServer.URL + "/debug/ddos")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if !bytes.Contains(body, []byte("<th>host</th>")) {
		t.Errorf("exelet table missing host column; body:\n%s", bodyStr)
	}
}
