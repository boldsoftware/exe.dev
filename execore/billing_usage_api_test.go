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

func TestAPIBillingUsage_Unauthenticated(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/billing/usage?granularity=monthly&start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
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

func TestAPIBillingUsage_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "usage-method@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/billing/usage?granularity=monthly&start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestAPIBillingUsage_MissingParams(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "usage-missing@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	cases := []struct {
		name string
		url  string
	}{
		{"missing granularity", "/api/billing/usage?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z"},
		{"missing start", "/api/billing/usage?granularity=monthly&end=2024-02-01T00:00:00Z"},
		{"missing end", "/api/billing/usage?granularity=monthly&start=2024-01-01T00:00:00Z"},
		{"invalid granularity", "/api/billing/usage?granularity=hourly&start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			req.Host = s.env.WebHost
			req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d", tc.name, w.Code)
			}
		})
	}
}

func TestAPIBillingUsageVMs_MissingParams(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	user, err := s.createUser(t.Context(), testSSHPubKey, "usagevms-missing@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	cases := []struct {
		name string
		url  string
	}{
		{"missing start", "/api/billing/usage/vms?end=2024-02-01T00:00:00Z"},
		{"missing end", "/api/billing/usage/vms?start=2024-01-01T00:00:00Z"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			req.Host = s.env.WebHost
			req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d", tc.name, w.Code)
			}
		})
	}
}

func TestAPIBillingUsage_NoMetricsd(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	// metricsdURL is empty by default in test server

	user, err := s.createUser(t.Context(), testSSHPubKey, "usage-nometrics@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/billing/usage?granularity=monthly&start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestAPIBillingUsage_Monthly(t *testing.T) {
	t.Parallel()

	month1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	monthlyMetrics := []types.MonthlyMetric{
		{
			MonthStart:          month1,
			VMID:                "vm-abc",
			VMName:              "my-vm",
			ResourceGroup:       "user-1",
			DiskLogicalAvgBytes: 10_000_000_000,
			NetworkTXBytes:      300_000_000,
			NetworkRXBytes:      200_000_000,
			DaysWithData:        31,
		},
	}

	metricsSrv := newFakeMetricsdServer(t, nil, monthlyMetrics, nil)
	s := newTestServerWithMetricsd(t, metricsSrv.URL)

	user, err := s.createUser(t.Context(), testSSHPubKey, "usage-monthly@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/billing/usage?granularity=monthly&start=2024-01-01T00:00:00Z&end=2024-03-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Metrics []billingUsageMonthlyMetric `json:"metrics"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(resp.Metrics))
	}
	if resp.Metrics[0].Date != "2024-01-01" {
		t.Errorf("expected date '2024-01-01', got %q", resp.Metrics[0].Date)
	}
	if resp.Metrics[0].DiskAvgBytes != 10_000_000_000 {
		t.Errorf("expected disk_avg_bytes 10000000000, got %d", resp.Metrics[0].DiskAvgBytes)
	}
	// bandwidth = tx + rx
	wantBandwidth := int64(300_000_000 + 200_000_000)
	if resp.Metrics[0].BandwidthBytes != wantBandwidth {
		t.Errorf("expected bandwidth_bytes %d, got %d", wantBandwidth, resp.Metrics[0].BandwidthBytes)
	}
}

func TestAPIBillingUsage_Daily(t *testing.T) {
	t.Parallel()

	day1 := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2024, 1, 16, 0, 0, 0, 0, time.UTC)
	dailyMetrics := []types.DailyMetric{
		{
			DayStart:            day1,
			VMID:                "vm-def",
			VMName:              "my-vm",
			ResourceGroup:       "user-1",
			DiskLogicalAvgBytes: 5_000_000_000,
			NetworkTXBytes:      60_000_000,
			NetworkRXBytes:      40_000_000,
			HoursWithData:       24,
		},
		{
			DayStart:            day2,
			VMID:                "vm-def",
			VMName:              "my-vm",
			ResourceGroup:       "user-1",
			DiskLogicalAvgBytes: 5_100_000_000,
			NetworkTXBytes:      70_000_000,
			NetworkRXBytes:      30_000_000,
			HoursWithData:       24,
		},
	}

	metricsSrv := newFakeMetricsdServer(t, dailyMetrics, nil, nil)
	s := newTestServerWithMetricsd(t, metricsSrv.URL)

	user, err := s.createUser(t.Context(), testSSHPubKey, "usage-daily@example.com", "", AllQualityChecks)
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	cookieValue, err := s.createAuthCookie(t.Context(), user.UserID, s.env.WebHost)
	if err != nil {
		t.Fatalf("createAuthCookie: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/billing/usage?granularity=daily&start=2024-01-15T00:00:00Z&end=2024-01-17T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Metrics []billingUsageDailyMetric `json:"metrics"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(resp.Metrics))
	}
	if resp.Metrics[0].Date != "2024-01-15" {
		t.Errorf("expected date '2024-01-15', got %q", resp.Metrics[0].Date)
	}
	if resp.Metrics[1].Date != "2024-01-16" {
		t.Errorf("expected date '2024-01-16', got %q", resp.Metrics[1].Date)
	}
	// bandwidth = tx + rx for day 1
	wantBandwidth1 := int64(60_000_000 + 40_000_000)
	if resp.Metrics[0].BandwidthBytes != wantBandwidth1 {
		t.Errorf("day1 bandwidth_bytes: expected %d, got %d", wantBandwidth1, resp.Metrics[0].BandwidthBytes)
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

	req := httptest.NewRequest(http.MethodGet,
		"/api/billing/usage/vms?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Metrics []billingUsageVMEntry `json:"metrics"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 2 {
		t.Fatalf("expected 2 VM metrics, got %d", len(resp.Metrics))
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

	req := httptest.NewRequest(http.MethodGet,
		"/api/billing/usage/vms?start=2024-01-01T00:00:00Z&end=2024-02-01T00:00:00Z", nil)
	req.Host = s.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Metrics []any `json:"metrics"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Metrics) != 0 {
		t.Errorf("expected empty metrics array, got %d entries", len(resp.Metrics))
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
