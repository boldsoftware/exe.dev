package email

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPostmarkStatsCollector(t *testing.T) {
	// Mock API responses
	outboundResp := postmarkOutboundResponse{
		Sent:               22070,
		Bounced:            2098,
		SMTPApiErrors:      5,
		SpamComplaints:     1,
		Opens:              100,
		UniqueOpens:        80,
		TotalClicks:        50,
		UniqueLinksClicked: 30,
	}
	bounceResp := postmarkBounceResponse{
		HardBounce:       1935,
		SoftBounce:       21,
		Transient:        104,
		Blocked:          16,
		DnsError:         12,
		AutoResponder:    9,
		SpamNotification: 1,
		SMTPApiError:     0,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Postmark-Server-Token") != "test-api-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/stats/outbound":
			json.NewEncoder(w).Encode(outboundResp)
		case "/stats/outbound/bounces":
			json.NewEncoder(w).Encode(bounceResp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// Create collector with mock server
	logger := slog.Default()
	collector := NewPostmarkStatsCollector("test-api-key", logger)
	collector.httpClient = &http.Client{
		Transport: &mockTransport{baseURL: server.URL},
	}

	// Poll to populate the cache
	collector.poll()

	// Create a fresh registry and register the collector
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	// Verify the metrics are reported as counters with correct values
	expected := `
# HELP postmark_bounces_total Postmark bounce statistics by type (cumulative).
# TYPE postmark_bounces_total counter
postmark_bounces_total{type="auto_responder"} 9
postmark_bounces_total{type="blocked"} 16
postmark_bounces_total{type="dns_error"} 12
postmark_bounces_total{type="hard_bounce"} 1935
postmark_bounces_total{type="smtp_api_error"} 0
postmark_bounces_total{type="soft_bounce"} 21
postmark_bounces_total{type="spam_notification"} 1
postmark_bounces_total{type="transient"} 104
# HELP postmark_outbound_total Postmark outbound email statistics (cumulative).
# TYPE postmark_outbound_total counter
postmark_outbound_total{type="bounced"} 2098
postmark_outbound_total{type="opens"} 100
postmark_outbound_total{type="sent"} 22070
postmark_outbound_total{type="smtp_api_errors"} 5
postmark_outbound_total{type="spam_complaints"} 1
postmark_outbound_total{type="total_clicks"} 50
postmark_outbound_total{type="unique_links_clicked"} 30
postmark_outbound_total{type="unique_opens"} 80
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected)); err != nil {
		t.Errorf("unexpected metrics:\n%s", err)
	}

	// Test Start/Stop
	collector.Start()
	time.Sleep(10 * time.Millisecond)
	collector.Stop()
	// Calling Stop again should be safe
	collector.Stop()
}

type mockTransport struct {
	baseURL string
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect requests to mock server
	req.URL.Scheme = "http"
	req.URL.Host = t.baseURL[7:] // Strip "http://"
	return http.DefaultTransport.RoundTrip(req)
}
