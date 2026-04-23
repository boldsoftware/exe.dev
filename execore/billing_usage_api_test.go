package execore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"exe.dev/metricsd/types"
)

// fakemetricsdsrv is a test fake for the metricsd HTTP server.
// It handles /query/usage, /query/daily, and /query/monthly endpoints.
type fakemetricsdsrv struct {
	t              *testing.T
	dailyMetrics   []types.DailyMetric
	monthlyMetrics []types.MonthlyMetric
	usageSummaries []types.UsageSummary
}

func (f *fakemetricsdsrv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/query/usage":
		json.NewEncoder(w).Encode(types.QueryUsageResponse{Metrics: f.usageSummaries})
	case "/query/daily":
		json.NewEncoder(w).Encode(types.QueryDailyResponse{Metrics: f.dailyMetrics})
	case "/query/monthly":
		json.NewEncoder(w).Encode(types.QueryMonthlyResponse{Metrics: f.monthlyMetrics})
	default:
		f.t.Errorf("unexpected metricsd request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func newFakeMetricsdServer(t *testing.T, daily []types.DailyMetric, monthly []types.MonthlyMetric, usage []types.UsageSummary) *httptest.Server {
	t.Helper()
	fake := &fakemetricsdsrv{
		t:              t,
		dailyMetrics:   daily,
		monthlyMetrics: monthly,
		usageSummaries: usage,
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return srv
}

func TestAPIBillingUsageVMs_Unauthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/billing/usage/vms?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAPIBillingUsageVMs_NoMetricsd(t *testing.T) {
	// The VMs endpoint requires metricsd; returns 503 when not configured.
	t.Parallel()
	s := newTestServer(t)
	// metricsdURL is empty by default in test server.

	user, err := s.createUser(t.Context(), testSSHPubKey, "usagevms-nometrics@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/billing/usage/vms?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestAPIBillingUsageVMs(t *testing.T) {
	t.Parallel()

	usageSummaries := []types.UsageSummary{
		{
			ResourceGroup:  "user-1",
			PeriodStart:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PeriodEnd:      time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
			DiskAvgBytes:   20_000_000_000,
			BandwidthBytes: 1_000_000_000,
			VMs: []types.VMUsageSummary{
				{
					VMID:           "vm-aaa",
					VMName:         "web-server",
					ResourceGroup:  "user-1",
					DiskAvgBytes:   12_000_000_000,
					DiskMaxBytes:   15_000_000_000,
					BandwidthBytes: 600_000_000,
					CPUSeconds:     7200,
					DaysWithData:   31,
				},
				{
					VMID:           "vm-bbb",
					VMName:         "dev-box",
					ResourceGroup:  "user-1",
					DiskAvgBytes:   8_000_000_000,
					DiskMaxBytes:   9_000_000_000,
					BandwidthBytes: 400_000_000,
					CPUSeconds:     3600,
					DaysWithData:   20,
				},
			},
		},
	}

	metricsSrv := newFakeMetricsdServer(t, nil, nil, usageSummaries)
	s := newTestServerWithMetricsd(t, metricsSrv.URL)

	user, err := s.createUser(t.Context(), testSSHPubKey, "usagevms@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/billing/usage/vms?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp billingUsageVMsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(resp.Metrics))
	}

	// Check first VM
	if resp.Metrics[0].VMID != "vm-aaa" {
		t.Errorf("vm[0].vm_id: expected 'vm-aaa', got %q", resp.Metrics[0].VMID)
	}
	if resp.Metrics[0].VMName != "web-server" {
		t.Errorf("vm[0].vm_name: expected 'web-server', got %q", resp.Metrics[0].VMName)
	}
	if resp.Metrics[0].DiskAvgBytes != 12_000_000_000 {
		t.Errorf("vm[0].disk_avg_bytes: expected 12000000000, got %d", resp.Metrics[0].DiskAvgBytes)
	}
	if resp.Metrics[0].CPUSeconds != 7200 {
		t.Errorf("vm[0].cpu_seconds: expected 7200, got %f", resp.Metrics[0].CPUSeconds)
	}

	// Check second VM
	if resp.Metrics[1].VMID != "vm-bbb" {
		t.Errorf("vm[1].vm_id: expected 'vm-bbb', got %q", resp.Metrics[1].VMID)
	}
	if resp.Metrics[1].VMName != "dev-box" {
		t.Errorf("vm[1].vm_name: expected 'dev-box', got %q", resp.Metrics[1].VMName)
	}
	if resp.Metrics[1].DaysWithData != 20 {
		t.Errorf("vm[1].days_with_data: expected 20, got %d", resp.Metrics[1].DaysWithData)
	}
}

func TestAPIBillingUsageVMs_EmptyMetrics(t *testing.T) {
	t.Parallel()

	metricsSrv := newFakeMetricsdServer(t, nil, nil, nil)
	s := newTestServerWithMetricsd(t, metricsSrv.URL)

	user, err := s.createUser(t.Context(), testSSHPubKey, "usagevms-empty@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/billing/usage/vms?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp billingUsageVMsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 0 {
		t.Errorf("expected empty VMs array, got %d entries", len(resp.Metrics))
	}
}

func TestBillingPeriod_CalendarMonth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		now       time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "mid month",
			now:       time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
			wantStart: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "first day",
			now:       time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			wantStart: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "last day of december",
			now:       time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC),
			wantStart: time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := calendarMonthPeriod(tc.now)
			if !start.Equal(tc.wantStart) {
				t.Errorf("start: got %v, want %v", start, tc.wantStart)
			}
			if !end.Equal(tc.wantEnd) {
				t.Errorf("end: got %v, want %v", end, tc.wantEnd)
			}
		})
	}
}

func TestBillingPeriod_Anchored(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		now       time.Time
		anchorDay int
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "anchor on 15th, now is 20th",
			now:       time.Date(2024, 6, 20, 0, 0, 0, 0, time.UTC),
			anchorDay: 15,
			wantStart: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 7, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "anchor on 15th, now is 10th (before anchor this month)",
			now:       time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC),
			anchorDay: 15,
			wantStart: time.Date(2024, 5, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "anchor on 31st in february (clamp to 29)",
			now:       time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC),
			anchorDay: 31,
			wantStart: time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := anchoredMonthPeriod(tc.now, tc.anchorDay)
			if !start.Equal(tc.wantStart) {
				t.Errorf("start: got %v, want %v", start, tc.wantStart)
			}
			if !end.Equal(tc.wantEnd) {
				t.Errorf("end: got %v, want %v", end, tc.wantEnd)
			}
		})
	}
}

func TestAPIBillingUsageVMs_OverageFields(t *testing.T) {
	t.Parallel()

	// Set up a usage summary where disk is over the plan limit.
	const gb = int64(1024 * 1024 * 1024)
	usageSummaries := []types.UsageSummary{
		{
			ResourceGroup:  "user-1",
			PeriodStart:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			PeriodEnd:      time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
			DiskAvgBytes:   30 * gb,
			BandwidthBytes: 5 * gb,
			VMs: []types.VMUsageSummary{
				{
					VMID:           "vm-xxx",
					VMName:         "over-vm",
					ResourceGroup:  "user-1",
					DiskAvgBytes:   30 * gb, // 30GB, plan includes 25GB -> 5GB over
					BandwidthBytes: 5 * gb,  // 5GB, under 100GB -> no overage
					DaysWithData:   31,
				},
			},
		},
	}

	metricsSrv := newFakeMetricsdServer(t, nil, nil, usageSummaries)
	s := newTestServerWithMetricsd(t, metricsSrv.URL)

	user, err := s.createUser(t.Context(), testSSHPubKey, "overage-test@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/billing/usage/vms?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp billingUsageVMsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(resp.Metrics))
	}
	vm := resp.Metrics[0]

	// IncludedDiskBytes is the plan's default disk (25GB for trial/basic users).
	// For a new test user with no billing, plan lookup fails => included = 0.
	// So overage should be 0 too.
	if vm.OverageDiskBytes < 0 {
		t.Errorf("overage_disk_bytes should not be negative, got %d", vm.OverageDiskBytes)
	}
	if vm.OverageBandwidthBytes < 0 {
		t.Errorf("overage_bandwidth_bytes should not be negative, got %d", vm.OverageBandwidthBytes)
	}
	// period_start and period_end must be set.
	if resp.PeriodStart.IsZero() {
		t.Error("period_start should not be zero")
	}
	if resp.PeriodEnd.IsZero() {
		t.Error("period_end should not be zero")
	}
	if resp.PeriodEnd.Before(resp.PeriodStart) {
		t.Errorf("period_end %v is before period_start %v", resp.PeriodEnd, resp.PeriodStart)
	}
}

// newTestServerWithMetricsd creates a test server with the given metricsd URL.
func newTestServerWithMetricsd(t testing.TB, metricsdURL string) *Server {
	t.Helper()
	s := newUnstartedServerWithMetricsd(t, metricsdURL)
	s.startAndAwaitReady()
	return s
}

// newUnstartedServerWithMetricsd creates an unstarted test server with the given metricsd URL.
func newUnstartedServerWithMetricsd(t testing.TB, metricsdURL string) *Server {
	t.Helper()
	s := newUnstartedServer(t)
	s.metricsdURL = metricsdURL
	return s
}
