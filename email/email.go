// Package email provides an interface for sending emails with multiple provider implementations.
package email

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mailgun/mailgun-go/v4"
	"github.com/mrz1836/postmark"
	"github.com/prometheus/client_golang/prometheus"
)

// Type represents the type of email being sent.
type Type string

const (
	TypeNewUserVerification      Type = "new_user_verification"
	TypeDeviceVerification       Type = "device_verification"
	TypeWebAuthVerification      Type = "web_auth_verification"
	TypeMobileAuthVerification   Type = "mobile_auth_verification"
	TypeLoginWithExeVerification Type = "login_with_exe_verification"
	TypeShareInvitation          Type = "share_invitation"
	TypeBoxCreated               Type = "box_created"
	TypeDebugTest                Type = "debug_test"
	TypeInvitesAllocated         Type = "invites_allocated"
)

// postmarkMessageStreams maps email types to Postmark message stream IDs.
var postmarkMessageStreams = map[Type]string{}

var emailsSentTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "emails_sent_total",
		Help: "Total number of emails sent.",
	},
	[]string{"provider", "type"},
)

// RegisterMetrics registers email metrics with the given prometheus registry.
// If a PostmarkStatsCollector is provided, it will also be registered.
func RegisterMetrics(registry *prometheus.Registry, postmarkCollector *PostmarkStatsCollector) {
	registry.MustRegister(emailsSentTotal)
	if postmarkCollector != nil {
		registry.MustRegister(postmarkCollector)
	}
}

// Sender is an interface for sending emails.
type Sender interface {
	// Send sends an email with the given parameters.
	// emailType identifies the type of email being sent.
	// from should be in the format "Name <email@example.com>"
	Send(ctx context.Context, emailType Type, from, to, subject, body string) error
}

// Senders holds multiple email provider implementations.
// Use Postmark or Mailgun fields directly to choose which provider to use per-email.
type Senders struct {
	Postmark      Sender
	Mailgun       Sender
	preferMailgun bool
}

// NewSendersFromEnv creates Senders from environment variables.
// For Postmark: requires POSTMARK_API_KEY
// For Mailgun: requires MAILGUN_API_KEY; domain is passed as parameter
// EMAIL_PROVIDER controls which provider Any() prefers: "mailgun" or "postmark" (default)
// Either or both may be nil if not configured.
func NewSendersFromEnv(mailgunDomain string) *Senders {
	s := &Senders{}

	if apiKey := os.Getenv("POSTMARK_API_KEY"); apiKey != "" {
		s.Postmark = NewPostmarkSender(apiKey)
	}

	if apiKey := os.Getenv("MAILGUN_API_KEY"); apiKey != "" && mailgunDomain != "" {
		s.Mailgun = NewMailgunSender(mailgunDomain, apiKey)
	}

	s.preferMailgun = os.Getenv("EMAIL_PROVIDER") == "mailgun"

	return s
}

// Any returns a configured sender based on EMAIL_PROVIDER preference.
// Returns nil if no sender is configured.
func (s *Senders) Any() Sender {
	if s.preferMailgun {
		if s.Mailgun != nil {
			return s.Mailgun
		}
		return s.Postmark
	}
	if s.Postmark != nil {
		return s.Postmark
	}
	return s.Mailgun
}

// PostmarkSender implements Sender using Postmark.
type PostmarkSender struct {
	client *postmark.Client
}

// NewPostmarkSender creates a new Postmark sender.
func NewPostmarkSender(apiKey string) *PostmarkSender {
	client := postmark.NewClient(apiKey, "")
	// Under load, we see HTTP/2 GOAWAY errors from Postmark.
	// Instead of attempting to cache bodies and enable retries, disable HTTP/2 entirely.
	client.HTTPClient.Transport = &http.Transport{
		ForceAttemptHTTP2: false,
		TLSNextProto:      make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	}
	return &PostmarkSender{client: client}
}

// Send sends an email via Postmark.
func (s *PostmarkSender) Send(ctx context.Context, emailType Type, from, to, subject, body string) error {
	email := postmark.Email{
		From:          from,
		To:            to,
		Subject:       subject,
		TextBody:      body,
		MessageStream: postmarkMessageStreams[emailType],
	}
	_, err := s.client.SendEmail(context.WithoutCancel(ctx), email)
	if err == nil {
		emailsSentTotal.WithLabelValues("postmark", string(emailType)).Inc()
		slog.InfoContext(ctx, "email sent", "provider", "postmark", "type", emailType, "to", to, "subject", subject)
	}
	return err
}

// MailgunSender implements Sender using Mailgun.
type MailgunSender struct {
	mg     *mailgun.MailgunImpl
	domain string
}

// NewMailgunSender creates a new Mailgun sender.
func NewMailgunSender(domain, apiKey string) *MailgunSender {
	return &MailgunSender{
		mg:     mailgun.NewMailgun(domain, apiKey),
		domain: domain,
	}
}

// Send sends an email via Mailgun.
func (s *MailgunSender) Send(ctx context.Context, emailType Type, from, to, subject, body string) error {
	msg := s.mg.NewMessage(from, subject, body, to)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, _, err := s.mg.Send(ctx, msg)
	if err != nil {
		return fmt.Errorf("mailgun send failed: %w", err)
	}
	emailsSentTotal.WithLabelValues("mailgun", string(emailType)).Inc()
	slog.InfoContext(ctx, "email sent", "provider", "mailgun", "type", emailType, "to", to, "subject", subject)
	return nil
}

// Prometheus metric descriptors for Postmark stats (reported as counters since they're cumulative totals).
var (
	postmarkOutboundDesc = prometheus.NewDesc(
		"postmark_outbound_total",
		"Postmark outbound email statistics (cumulative).",
		[]string{"type"}, nil,
	)
	postmarkBounceDesc = prometheus.NewDesc(
		"postmark_bounces_total",
		"Postmark bounce statistics by type (cumulative).",
		[]string{"type"}, nil,
	)
)

// postmarkOutboundResponse matches the /stats/outbound API response.
type postmarkOutboundResponse struct {
	Sent               int64   `json:"Sent"`
	Bounced            int64   `json:"Bounced"`
	SMTPApiErrors      int64   `json:"SMTPApiErrors"`
	BounceRate         float64 `json:"BounceRate"`
	SpamComplaints     int64   `json:"SpamComplaints"`
	SpamComplaintsRate float64 `json:"SpamComplaintsRate"`
	Opens              int64   `json:"Opens"`
	UniqueOpens        int64   `json:"UniqueOpens"`
	TotalClicks        int64   `json:"TotalClicks"`
	UniqueLinksClicked int64   `json:"UniqueLinksClicked"`
}

// postmarkBounceResponse matches the /stats/outbound/bounces API response.
type postmarkBounceResponse struct {
	HardBounce       int64 `json:"HardBounce"`
	SoftBounce       int64 `json:"SoftBounce"`
	Transient        int64 `json:"Transient"`
	Blocked          int64 `json:"Blocked"`
	DnsError         int64 `json:"DnsError"`
	AutoResponder    int64 `json:"AutoResponder"`
	SpamNotification int64 `json:"SpamNotification"`
	SMTPApiError     int64 `json:"SMTPApiError"`
}

// PostmarkStatsCollector polls Postmark stats API and exposes them as Prometheus counters.
// It implements prometheus.Collector.
type PostmarkStatsCollector struct {
	apiKey     string
	httpClient *http.Client
	logger     *slog.Logger
	stopOnce   sync.Once
	stop       chan struct{}

	mu       sync.RWMutex
	outbound *postmarkOutboundResponse
	bounces  *postmarkBounceResponse
}

// NewPostmarkStatsCollector creates a new stats collector.
func NewPostmarkStatsCollector(apiKey string, logger *slog.Logger) *PostmarkStatsCollector {
	return &PostmarkStatsCollector{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
		stop:   make(chan struct{}),
	}
}

// Start begins polling Postmark stats every 10 minutes.
func (c *PostmarkStatsCollector) Start() {
	c.poll() // Poll immediately on start
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.poll()
			case <-c.stop:
				return
			}
		}
	}()
}

// Stop stops the stats collector.
func (c *PostmarkStatsCollector) Stop() {
	c.stopOnce.Do(func() {
		close(c.stop)
	})
}

// Describe implements prometheus.Collector.
func (c *PostmarkStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- postmarkOutboundDesc
	ch <- postmarkBounceDesc
}

// Collect implements prometheus.Collector.
func (c *PostmarkStatsCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	outbound := c.outbound
	bounces := c.bounces
	c.mu.RUnlock()

	if outbound != nil {
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.Sent), "sent")
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.Bounced), "bounced")
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.SMTPApiErrors), "smtp_api_errors")
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.SpamComplaints), "spam_complaints")
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.Opens), "opens")
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.UniqueOpens), "unique_opens")
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.TotalClicks), "total_clicks")
		ch <- prometheus.MustNewConstMetric(postmarkOutboundDesc, prometheus.CounterValue, float64(outbound.UniqueLinksClicked), "unique_links_clicked")
	}

	if bounces != nil {
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.HardBounce), "hard_bounce")
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.SoftBounce), "soft_bounce")
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.Transient), "transient")
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.Blocked), "blocked")
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.DnsError), "dns_error")
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.AutoResponder), "auto_responder")
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.SpamNotification), "spam_notification")
		ch <- prometheus.MustNewConstMetric(postmarkBounceDesc, prometheus.CounterValue, float64(bounces.SMTPApiError), "smtp_api_error")
	}
}

func (c *PostmarkStatsCollector) poll() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outbound, err := c.fetchOutboundStats(ctx)
	if err != nil {
		c.logger.ErrorContext(ctx, "failed to fetch postmark outbound stats", "error", err)
	}

	bounces, err := c.fetchBounceStats(ctx)
	if err != nil {
		c.logger.ErrorContext(ctx, "failed to fetch postmark bounce stats", "error", err)
	}

	c.mu.Lock()
	if outbound != nil {
		c.outbound = outbound
	}
	if bounces != nil {
		c.bounces = bounces
	}
	c.mu.Unlock()
}

func (c *PostmarkStatsCollector) fetchOutboundStats(ctx context.Context) (*postmarkOutboundResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.postmarkapp.com/stats/outbound", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Postmark-Server-Token", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("postmark API returned status %d", resp.StatusCode)
	}

	var result postmarkOutboundResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *PostmarkStatsCollector) fetchBounceStats(ctx context.Context) (*postmarkBounceResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.postmarkapp.com/stats/outbound/bounces", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Postmark-Server-Token", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("postmark API returned status %d", resp.StatusCode)
	}

	var result postmarkBounceResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
