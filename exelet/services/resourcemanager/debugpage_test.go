package resourcemanager

import (
	"net/http/httptest"
	"strings"
	"testing"

	"exe.dev/exelet/guestmetrics"
)

// TestGuestMetricsPageEscapesVMName ensures the debug page renders VM
// metadata through html/template (which auto-escapes), so a malicious
// VM name cannot inject script tags.
func TestGuestMetricsPageEscapesVMName(t *testing.T) {
	pool := guestmetrics.NewPool(guestmetrics.PoolConfig{})
	pool.Add(guestmetrics.VMInfo{
		ID:   "<script>alert('id')</script>",
		Name: "<script>alert('name')</script>",
	})
	hp := newHostPressure()
	m := &ResourceManager{guestPool: pool, hostPressure: hp}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/debug/vms/guest-metrics", nil)
	m.handleGuestMetricsPage(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "<script>alert") {
		t.Fatalf("raw <script> leaked into HTML output:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert") {
		t.Fatalf("expected escaped script tag in HTML output:\n%s", body)
	}
}
