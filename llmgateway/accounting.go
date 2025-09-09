package llmgateway

// This file contains functions, types and constants to help exed check and track
// how many tokens i.e. credits a client has consumed with each request that exed
// proxies to anthropic.
//
// Note: The code in this package is mostly copied from bold.git/skaband.
import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	Claude35Sonnet = "claude-3-5-sonnet-20241022"
	Claude35Haiku  = "claude-3-5-haiku-20241022"
	Claude37Sonnet = "claude-3-7-sonnet-20250219"
	Claude4Sonnet  = "claude-sonnet-4-20250514"
	Claude4Opus    = "claude-opus-4-20250514"
)

type UsageDebit struct {
	Usage
	Model            string    `json:"model"`
	MessageID        string    `json:"message_id"`
	BillingAccountID string    `json:"billing_account_id"`
	Created          time.Time `json:"created"`
}

// UsageCredit represents a credit purchase by a user
type UsageCredit struct {
	ID               int64     `json:"id,omitempty"`
	BillingAccountID string    `json:"billing_account_id"`
	Amount           float64   `json:"amount"`
	Created          time.Time `json:"created"`
	PaymentMethod    string    `json:"payment_method"`
	PaymentID        string    `json:"payment_id"`
	Status           string    `json:"status"`
	Data             any       `json:"data,omitempty"` // Additional data specific to payment method
}

// Usage represents billing and rate-limit usage.
type Usage struct {
	InputTokens              uint64  `json:"input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}

// Accountant handles credit balance checking and usage debiting
type Accountant interface {
	// GetUserBalance retrieves the current credit balance for a user
	GetUserBalance(ctx context.Context, billingAccountID string) (float64, error)

	// DebitUsage records a usage debit for a user
	DebitUsage(ctx context.Context, debit UsageDebit) error

	// DebitUsage records a usage credit for a user
	CreditUsage(ctx context.Context, credit UsageCredit) error

	HasNewUserCredits(ctx context.Context, billingAccountID string) (bool, any)

	ApplyNewUserCredits(ctx context.Context, billingAccountID string) any
}

// Prometheus metrics for accounting
var (
	// Single counter for all token types with token_type label
	tokensCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_tokens_total",
			Help: "Total number of tokens by type, model and API type",
		},
		[]string{"token_type", "model", "api_type"},
	)

	// Counter for cost in USD by model
	costUSDCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_cost_usd_total",
			Help: "Total cost in USD by model",
		},
		[]string{"model", "api_type"},
	)

	// Counter for requests proxied
	requestsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_requests_total",
			Help: "Total number of requests proxied",
		},
		[]string{"status", "api_type"},
	)

	// Histogram for request latencies
	requestLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llm_request_duration_seconds",
			Help:    "Request latency distribution",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10), // Start at 100ms, double 10 times
		},
		[]string{"model", "api_type"},
	)

	// Gauge for Anthropic rate limits
	anthropicRateLimitGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "anthropic_rate_limit_remaining",
			Help: "Remaining Anthropic rate limits by type",
		},
		[]string{"type"},
	)
)

// RegisterAccountingMetrics registers all accounting metrics with the provided registry
func RegisterAccountingMetrics(registry *prometheus.Registry) {
	registry.MustRegister(
		tokensCounter,
		costUSDCounter,
		requestsCounter,
		requestLatency,
		anthropicRateLimitGauge,
	)
}

// accountingTransport wraps http transactions to check and track the client's credit usage
type accountingTransport struct {
	http.RoundTripper
	accountant       Accountant
	BillingAccountID string
	baseURL          string
	apiType          string    // "antmsgs" or "gemmsgs"
	testDebitDone    chan bool // for testing -- if non-nil, best effort send every time a debit occurs
}

func (a *accountingTransport) checkCredits(ctx context.Context, billingAccountID string) error {
	// Get the current balance for the user
	balance, err := a.accountant.GetUserBalance(ctx, billingAccountID)
	if err != nil {
		slog.Error("accountingTransport.checkCredits: GetUserBalance failed", "error", err)
		// Fallback to allowing the request if we can't check balance
		return nil
	}

	// If balance is negative, reject the request
	if balance <= 0 {
		return fmt.Errorf("your account balance of $%.2f is insufficient - please purchase more credits at %s, and then ask the agent to continue", balance, a.baseURL+"/buy")
	}
	return nil
}

// RoundTrip enforces credit usage limits and records some metrics.
func (a *accountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// TODO: restore latency measurements
	if err := a.checkCredits(r.Context(), a.BillingAccountID); err != nil {
		slog.Error("accountingTransport.RoundTrip: checkCredits failed", "error", err)
		// Increment the requests counter with status="payment_required"
		requestsCounter.WithLabelValues("payment_required", a.apiType).Inc()
		return &http.Response{
			StatusCode:    http.StatusPaymentRequired,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewBufferString(err.Error())),
			ContentLength: int64(len(err.Error())),
			Request:       r,
		}, nil
	}
	// Increment the requests counter with status="attempted"
	requestsCounter.WithLabelValues("attempted", a.apiType).Inc()
	ret, err := a.RoundTripper.RoundTrip(r)

	if ret != nil {
		// Increment counter with actual status
		status := "error"
		if err == nil {
			status = fmt.Sprintf("%d", ret.StatusCode)
		}
		requestsCounter.WithLabelValues(status, a.apiType).Inc()
	}

	// Extract and record Anthropic rate limit headers if present
	if ret != nil && err == nil && a.apiType == "antmsgs" {
		// Extract the rate limit headers and publish as gauge metrics
		setRateLimitGauge(ret.Header, "Anthropic-Ratelimit-Input-Tokens-Remaining", "input_tokens")
		setRateLimitGauge(ret.Header, "Anthropic-Ratelimit-Output-Tokens-Remaining", "output_tokens")
		setRateLimitGauge(ret.Header, "Anthropic-Ratelimit-Requests-Remaining", "requests")
		setRateLimitGauge(ret.Header, "Anthropic-Ratelimit-Tokens-Remaining", "tokens")
	}

	return ret, err
}

func (a *accountingTransport) modifyResponse(resp *http.Response) error {
	if resp == nil || resp.StatusCode != http.StatusOK {
		return nil
	}

	buf, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("couldn't read response body: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(buf))

	contentEncoding := resp.Header["Content-Encoding"]
	if slices.Contains(contentEncoding, "gzip") {
		gzReader, err := gzip.NewReader(bytes.NewReader(buf))
		if err != nil {
			return fmt.Errorf("couldn't create new gzip reader: %w", err)
		}
		decoded, err := io.ReadAll(gzReader)
		if err != nil {
			return fmt.Errorf("couldn't read gzip reader: %w", err)
		}
		gzReader.Close()
		buf = decoded
	}

	usageDebit := UsageDebit{BillingAccountID: a.BillingAccountID, Created: time.Now()}
	// Usage, Model, MessageID to be filled in below

	switch a.apiType {
	case "antmsgs":
		var ui anthropicResponseUsageInfo
		if err := json.Unmarshal(buf, &ui); err != nil {
			return fmt.Errorf("anthropic json decode error: %v", err)
		}
		usageDebit.Usage = ui.Usage
		usageDebit.Model = ui.Model
		usageDebit.MessageID = ui.ID

	case "oaimsgs":
		if len(buf) == 0 {
			return fmt.Errorf("empty openai response, skipping accounting")
		}

		var oi openaiResponseUsageInfo
		if err := json.Unmarshal(buf, &oi); err != nil {
			return fmt.Errorf("openai json decode error: %v, content: %s", err, string(buf))
		}
		if oi.Usage.TotalTokens == 0 {
			return fmt.Errorf("openai response missing usage data, skipping accounting")
		}

		// Convert OpenAI usage to Usage format for accounting
		promptTokens := oi.Usage.PromptTokens
		completionTokens := oi.Usage.CompletionTokens

		// If token counts are zero, set a minimal token count to avoid accounting errors
		if promptTokens == 0 && completionTokens == 0 {
			slog.Debug("openai response has zero token counts, using defaults")
			promptTokens = 1
			completionTokens = 1
		}

		usage := Usage{
			InputTokens:  uint64(promptTokens),
			OutputTokens: uint64(completionTokens),
		}

		// Use the model from the response, or unknown if not provided
		model := cmp.Or(oi.Model, "oai-unknown")
		usageDebit.Usage = usage
		usageDebit.Model = model
		usageDebit.MessageID = oi.ID

	case "gemmsgs":
		if len(buf) == 0 {
			return fmt.Errorf("empty gemini response, skipping accounting")
		}

		var gi geminiResponseUsageInfo
		if err := json.Unmarshal(buf, &gi); err != nil {
			return fmt.Errorf("gemini json decode error: %v, content: %s", err, string(buf))
		}
		if gi.UsageMetadata.TotalTokenCount == 0 && len(gi.Candidates) == 0 {
			return fmt.Errorf("gemini response missing usage data, skipping accounting")
		}

		// Convert Gemini usage to Usage format for accounting
		// Handle the case where UsageMetadata might not be fully populated
		promptTokens := gi.UsageMetadata.PromptTokenCount + gi.UsageMetadata.CachedContentTokenCount
		candidatesTokens := gi.UsageMetadata.CandidatesTokenCount

		// If token counts are zero, set a minimal token count to avoid accounting errors
		if promptTokens == 0 && candidatesTokens == 0 {
			slog.Debug("gemini response has zero token counts, using defaults")
			promptTokens = 1
			candidatesTokens = 1
		}

		usage := Usage{
			InputTokens:  uint64(promptTokens),
			OutputTokens: uint64(candidatesTokens),
		}

		// Response modelVersion is in a format like "gemini-1.5-pro-001".
		// Map to our pricing table keys.
		model := "gemini-1.5-pro" // default to pro if we can't determine otherwise
		if strings.Contains(gi.ModelVersion, "flash") {
			model = "gemini-1.5-flash"
		}

		usageDebit.Usage = usage
		usageDebit.Model = model
		usageDebit.MessageID = fmt.Sprintf("gem-%d", time.Now().UnixNano()) // Gemini doesn't provide one

	default:
		return fmt.Errorf("unknown API type: %s", a.apiType)
	}

	uc := UsageCost(usageDebit.Model, usageDebit.Usage)
	usageDebit.Usage.CostUSD = uc.USD()
	resp.Header.Add("Skaband-Cost-Microcents", fmt.Sprint(uc))

	// Do DB+metrics work in a separate goroutine to avoid blocking the HTTP response
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := a.accountant.DebitUsage(ctx, usageDebit); err != nil {
			slog.Error("accountingTransport.modifyResponse: couldn't debit usage", "error", err)
		}
		if a.testDebitDone != nil {
			a.testDebitDone <- true
		}

		// Update Prometheus metrics
		usage := usageDebit.Usage
		model := usageDebit.Model
		tokensCounter.WithLabelValues("input", model, a.apiType).Add(float64(usage.InputTokens))
		tokensCounter.WithLabelValues("cache_creation", model, a.apiType).Add(float64(usage.CacheCreationInputTokens))
		tokensCounter.WithLabelValues("cache_read", model, a.apiType).Add(float64(usage.CacheReadInputTokens))
		tokensCounter.WithLabelValues("output", model, a.apiType).Add(float64(usage.OutputTokens))
		costUSDCounter.WithLabelValues(model, a.apiType).Add(usage.CostUSD)
	}()

	return nil
}

// anthropicResponseUsageInfo extracts usage-relevant information from an Anthropic response.
type anthropicResponseUsageInfo struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage Usage  `json:"usage"`
}

// openaiResponseUsageInfo extracts usage-relevant information from an openai-compatible response.
type openaiResponseUsageInfo struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Type for Gemini response usage information
type geminiResponseUsageInfo struct {
	Candidates    []any `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion string `json:"modelVersion"`
}

// Helper function to extract rate limit values from headers and set gauge metrics
func setRateLimitGauge(header http.Header, headerName, labelValue string) {
	if headerValue := header.Get(headerName); headerValue != "" {
		if val, err := strconv.Atoi(headerValue); err == nil {
			anthropicRateLimitGauge.WithLabelValues(labelValue).Set(float64(val))
		}
	}
}
