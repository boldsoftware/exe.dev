package llmgateway

// This file contains functions, types and constants to help exed check and track
// how many tokens i.e. credits a client has consumed with each request that exed
// proxies to anthropic.
//
// Note: The code in this package is mostly copied from bold.git/skaband.

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"exe.dev/errorz"
	"exe.dev/llmpricing"
	"exe.dev/sqlite"
	sloghttp "github.com/samber/slog-http"
	"golang.org/x/net/http2"
)

const sseScannerBufSize = 256 * 1024

var sseScannerBufPool = sync.Pool{
	New: func() any {
		return new([sseScannerBufSize]byte)
	},
}

// errBodyNotReplayable is returned when HTTP/2 tries to retry a request but the body has already been consumed.
var errBodyNotReplayable = errors.New("request body not replayable; caller should retry")

// accountingTransport wraps http transactions to check and track the client's credit usage
type accountingTransport struct {
	http.RoundTripper
	db            *sqlite.DB
	provider      llmpricing.Provider
	testDebitDone chan bool // for testing -- if non-nil, best effort send every time a debit occurs
	log           *slog.Logger
	creditMgr     *CreditManager

	// Request context for adding slog attributes
	incomingReq *http.Request
	boxName     string
	userID      string

	// For SSE responses, we store the usage data and add attributes after the stream completes
	sseDone  chan struct{}
	sseUsage *UsageDebit
}

// getBodyCannotReplay is an http.Request.GetBody implementation that returns a sentinel error.
// It is useful for communicating to someone upstream than a replay was requested and rejected.
// It's not worth buffering all requests to handle this transparently,
// but treating this situation as a full error is just log noise.
func getBodyCannotReplay() (io.ReadCloser, error) {
	return nil, errBodyNotReplayable
}

// RoundTrip enforces credit usage limits and records some metrics.
func (a *accountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// TODO: restore latency measurements

	if r.Body != nil && r.GetBody == nil {
		r.GetBody = getBodyCannotReplay
	}

	// Increment the requests counter with status="attempted"
	requestsCounter.WithLabelValues("attempted", string(a.provider)).Inc()
	ret, err := a.RoundTripper.RoundTrip(r)

	if ret != nil {
		// Increment counter with actual status
		status := "error"
		if err == nil {
			status = fmt.Sprintf("%d", ret.StatusCode)
		}
		requestsCounter.WithLabelValues(status, string(a.provider)).Inc()
	}

	// Extract and record Anthropic rate limit headers if present
	if ret != nil && err == nil && string(a.provider) == "anthropic" {
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

	ctx := a.incomingReq.Context()
	contentType := resp.Header.Get("Content-Type")

	// Note that the response may be a unary HTTP response body, or it may be a stream of
	// SSE events. So this goroutine may handle either a unary req/response transaction,
	// or a long-lived streaming response (SSE).

	// Just check the first part of contentType, if it's something like "text/event-stream; charset=utf-8"
	parts := strings.Split(contentType, ";")
	contentType = parts[0]

	// Handle vanilla unary HTTP json responses by reading the entire body
	// and creating a new reder for those same bytes after we've read them.
	// This is basically resetting the response body reader for the downstream
	// http handler logic after we've made our copy of it.
	switch contentType {
	case "application/json":
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if !errorz.HasType[http2.StreamError](err) {
				a.log.ErrorContext(ctx, "couldn't read unary response body", "error", err)
			}
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(data))

		// Check if the data is gzip-compressed. We check both the Content-Encoding
		// header and the gzip magic bytes because some proxies/servers may send
		// gzip data with non-standard header values (different casing, multiple
		// values like "gzip, br", etc.) or even missing headers.
		if isGzipped(resp.Header.Get("Content-Encoding"), data) {
			gzReader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				a.log.ErrorContext(ctx, "accountingTransport couldn't create new gzip reader for unary response body", "error", err)
				return err
			}
			decoded, err := io.ReadAll(gzReader)
			if err != nil {
				a.log.ErrorContext(ctx, "accountingTransport couldn't read gzip reader for unary response body", "error", err)
				return err
			}
			gzReader.Close()
			data = decoded
		}
		costInfo, err := a.processResponseData(data)
		if err != nil {
			truncated := data[:min(len(data), 256)]
			a.log.ErrorContext(ctx, "accountingTransport couldn't process unary JSON response", "error", err, "data", fmt.Sprintf("%q", truncated))
			return err
		}

		// Add cost header to response
		if costInfo != nil {
			resp.Header.Set("Exedev-Gateway-Cost", fmt.Sprintf("%.6f", costInfo.CostUSD))
		}

	// Handle SSE streams by scanning messages, parsing, and re-writing as we go.
	// We run the scan-and-re-write loop in a goroutine so this method can return
	// before the response body's event stream is closed (because that can be a long time
	// from now).
	case "text/event-stream":
		// TODO(banksean): Figure out if we need to check for "gzip" Content-Encoding headers
		// here as well. I have no idea how (or if) gzipping works for SSE response streams, though.
		body := resp.Body
		bodyReader, bodyWriter := io.Pipe()
		resp.Body = bodyReader
		scanner := bufio.NewScanner(body)
		bufp := sseScannerBufPool.Get().(*[sseScannerBufSize]byte)
		scanner.Buffer(bufp[:], sseScannerBufSize)
		// Set up channel to signal when SSE processing is complete
		a.sseDone = make(chan struct{})
		go func() {
			defer sseScannerBufPool.Put(bufp)
			defer close(a.sseDone)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data:") {
					data := strings.TrimPrefix(line, "data:")
					// Process the event data, which may include details for accounting.
					// For SSE, we use processResponseDataSSE which stores usage for later
					if err := a.processResponseDataSSE([]byte(data)); err != nil {
						a.log.ErrorContext(ctx, "Proxy SSE scanner", "processResponseData error", err)
					}
				}
				fmt.Fprintln(bodyWriter, line)
			}
			if err := scanner.Err(); err != nil {
				switch {
				case errors.Is(err, context.Canceled), errorz.HasType[http2.StreamError](err):
					// common, uninteresting error, ignore
				default:
					a.log.ErrorContext(ctx, "Proxy SSE scanner", "error", err)
				}
			}
			bodyWriter.Close()
		}()
	default:
		// We just log this rather than return an error, so that the request still gets
		// proxied. We just don't have a way to debit any charges based on usage data that
		// may have been included in the response.
		a.log.ErrorContext(ctx, "accountingTransport.modifyResponse", "unrecognized content type", contentType)
	}

	if a.testDebitDone != nil {
		a.testDebitDone <- true
	}

	return nil
}

type Usage struct {
	InputTokens              uint64  `json:"input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}

// CostInfo holds cost information to be added to response headers.
type CostInfo struct {
	CostUSD          float64
	RemainingCredit  float64
	InputTokens      uint64
	OutputTokens     uint64
	CacheReadTokens  uint64
	CacheWriteTokens uint64
}

type UsageDebit struct {
	Usage Usage `json:"usage"`

	Model     string    `json:"model"`
	MessageID string    `json:"message_id"`
	Created   time.Time `json:"created"`
}

func (m *accountingTransport) processResponseData(data []byte) (*CostInfo, error) {
	usageDebit := UsageDebit{Created: time.Now()}
	ctx := m.incomingReq.Context()

	switch m.provider {
	case llmpricing.ProviderAnthropic:
		var ui anthropicResponseUsageInfo
		if err := json.Unmarshal(data, &ui); err != nil {
			return nil, fmt.Errorf("anthropic json decode error: %w", err)
		}
		if ui.Usage == nil {
			// Nothing to bill for here.
			return nil, nil
		}
		usageDebit.Usage = *ui.Usage
		usageDebit.Model = ui.Model
		usageDebit.MessageID = ui.ID
		m.log.InfoContext(ctx, "debitResponse",
			"message_id", ui.ID,
			"model", ui.Model,
			"input_tokens", ui.Usage.InputTokens,
			"output_tokens", ui.Usage.OutputTokens,
			"cache_creation_tokens", ui.Usage.CacheCreationInputTokens,
			"cache_read_tokens", ui.Usage.CacheReadInputTokens,
		)
	case llmpricing.ProviderOpenAI, llmpricing.ProviderFireworks:
		if len(data) == 0 {
			// Empty response, nothing to account for
			return nil, nil
		}

		var oi openaiResponseUsageInfo
		if err := json.Unmarshal(data, &oi); err != nil {
			return nil, fmt.Errorf("openai json decode error: %v", err)
		}
		if oi.Usage.TotalTokens == 0 {
			// Check if this is a free endpoint that doesn't return usage data
			path := m.incomingReq.URL.Path
			if isFreeEndpoint(path) {
				return nil, nil
			}
			m.log.WarnContext(ctx, "openai response missing usage data",
				"path", path,
				"method", m.incomingReq.Method,
				"box", m.boxName,
				"user_id", m.userID,
			)
			return nil, fmt.Errorf("openai response missing usage data for path %s", path)
		}

		// Convert OpenAI usage to Usage format for accounting
		promptTokens := oi.Usage.PromptTokens
		completionTokens := oi.Usage.CompletionTokens

		// If token counts are zero, set a minimal token count to avoid accounting errors
		if promptTokens == 0 && completionTokens == 0 {
			m.log.DebugContext(ctx, "openai response has zero token counts, using defaults")
			promptTokens = 1
			completionTokens = 1
		}

		// Extract cached tokens if available
		var cachedTokens uint64
		if oi.Usage.PromptTokensDetails != nil {
			cachedTokens = uint64(oi.Usage.PromptTokensDetails.CachedTokens)
		}

		usage := Usage{
			InputTokens:          uint64(promptTokens),
			OutputTokens:         uint64(completionTokens),
			CacheReadInputTokens: cachedTokens,
		}

		// Use the model from the response, or unknown if not provided
		model := cmp.Or(oi.Model, "oai-unknown")
		usageDebit.Usage = usage
		usageDebit.Model = model
		usageDebit.MessageID = oi.ID
		m.log.InfoContext(ctx, "debitResponse",
			"message_id", oi.ID,
			"model", model,
			"input_tokens", promptTokens,
			"output_tokens", completionTokens,
			"cache_read_tokens", cachedTokens,
		)

	default:
		m.log.ErrorContext(ctx, "accountingTransport.processResponseData: unknown provider", "provider", m.provider)
	}

	// Calculate cost based on model pricing
	usage := usageDebit.Usage
	model := usageDebit.Model
	providerStr := string(m.provider)
	costUSD := llmpricing.CalculateCost(m.provider, model, llmpricing.Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	})

	// Update Prometheus metrics
	tokensCounter.WithLabelValues("input", model, providerStr, m.boxName, m.userID).Add(float64(usage.InputTokens))
	tokensCounter.WithLabelValues("cache_creation", model, providerStr, m.boxName, m.userID).Add(float64(usage.CacheCreationInputTokens))
	tokensCounter.WithLabelValues("cache_read", model, providerStr, m.boxName, m.userID).Add(float64(usage.CacheReadInputTokens))
	tokensCounter.WithLabelValues("output", model, providerStr, m.boxName, m.userID).Add(float64(usage.OutputTokens))
	costUSDCounter.WithLabelValues(model, providerStr, m.boxName, m.userID).Add(costUSD)

	// Debit credit from user's balance
	var remainingCredit float64 = -1
	if m.creditMgr != nil && m.userID != "" {
		if creditInfo, err := m.creditMgr.DebitCredit(m.incomingReq.Context(), m.userID, costUSD); err != nil {
			m.log.ErrorContext(m.incomingReq.Context(), "failed to debit LLM credit", "user_id", m.userID, "cost_usd", costUSD, "error", err)
		} else if creditInfo != nil {
			remainingCredit = creditInfo.Available
		}
	}

	// Add slog attributes to the incoming request for HTTP logging
	if m.incomingReq != nil {
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("llm_model", model))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("vm_name", m.boxName))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("user_id", m.userID))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("input_tokens", usage.InputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("output_tokens", usage.OutputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("cache_creation_tokens", usage.CacheCreationInputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("cache_read_tokens", usage.CacheReadInputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Float64("cost_usd", costUSD))
		if remainingCredit >= 0 {
			sloghttp.AddCustomAttributes(m.incomingReq, slog.Float64("remaining_credit_usd", remainingCredit))
		}
	}

	return &CostInfo{
		CostUSD:          costUSD,
		RemainingCredit:  remainingCredit,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
	}, nil
}

// processResponseDataSSE is like processResponseData but stores usage for later attribute addition.
// This is needed for SSE responses where we can't add slog attributes from the goroutine.
func (m *accountingTransport) processResponseDataSSE(data []byte) error {
	usageDebit := UsageDebit{Created: time.Now()}
	ctx := m.incomingReq.Context()

	switch m.provider {
	case llmpricing.ProviderAnthropic:
		var ui anthropicResponseUsageInfo
		if err := json.Unmarshal(data, &ui); err != nil {
			// SSE events that aren't JSON are common (empty lines, etc.)
			return nil
		}
		if ui.Usage == nil {
			// Nothing to bill for here.
			return nil
		}
		usageDebit.Usage = *ui.Usage
		usageDebit.Model = ui.Model
		usageDebit.MessageID = ui.ID
		m.log.InfoContext(ctx, "debitResponse",
			"message_id", ui.ID,
			"model", ui.Model,
			"input_tokens", ui.Usage.InputTokens,
			"output_tokens", ui.Usage.OutputTokens,
			"cache_creation_tokens", ui.Usage.CacheCreationInputTokens,
			"cache_read_tokens", ui.Usage.CacheReadInputTokens,
		)
	case llmpricing.ProviderOpenAI, llmpricing.ProviderFireworks:
		if len(data) == 0 {
			return nil
		}

		var oi openaiResponseUsageInfo
		if err := json.Unmarshal(data, &oi); err != nil {
			return nil
		}
		if oi.Usage.TotalTokens == 0 {
			return nil
		}

		promptTokens := oi.Usage.PromptTokens
		completionTokens := oi.Usage.CompletionTokens
		if promptTokens == 0 && completionTokens == 0 {
			promptTokens = 1
			completionTokens = 1
		}

		// Extract cached tokens if available
		var cachedTokens uint64
		if oi.Usage.PromptTokensDetails != nil {
			cachedTokens = uint64(oi.Usage.PromptTokensDetails.CachedTokens)
		}

		usage := Usage{
			InputTokens:          uint64(promptTokens),
			OutputTokens:         uint64(completionTokens),
			CacheReadInputTokens: cachedTokens,
		}

		model := cmp.Or(oi.Model, "oai-unknown")
		usageDebit.Usage = usage
		usageDebit.Model = model
		usageDebit.MessageID = oi.ID
		m.log.InfoContext(ctx, "debitResponse",
			"message_id", oi.ID,
			"model", model,
			"input_tokens", promptTokens,
			"output_tokens", completionTokens,
			"cache_read_tokens", cachedTokens,
		)

	default:
		return nil
	}

	// Calculate cost based on model pricing
	usage := usageDebit.Usage
	model := usageDebit.Model
	providerStr := string(m.provider)
	costUSD := llmpricing.CalculateCost(m.provider, model, llmpricing.Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	})

	// Update Prometheus metrics immediately
	tokensCounter.WithLabelValues("input", model, providerStr, m.boxName, m.userID).Add(float64(usage.InputTokens))
	tokensCounter.WithLabelValues("cache_creation", model, providerStr, m.boxName, m.userID).Add(float64(usage.CacheCreationInputTokens))
	tokensCounter.WithLabelValues("cache_read", model, providerStr, m.boxName, m.userID).Add(float64(usage.CacheReadInputTokens))
	tokensCounter.WithLabelValues("output", model, providerStr, m.boxName, m.userID).Add(float64(usage.OutputTokens))
	costUSDCounter.WithLabelValues(model, providerStr, m.boxName, m.userID).Add(costUSD)

	// Store for later attribute addition (only keep the last one with usage data)
	usageDebit.Usage.CostUSD = costUSD
	m.sseUsage = &usageDebit

	return nil
}

// WaitAndAddSSEAttributes waits for SSE processing to complete and adds slog attributes.
// Call this after proxy.ServeHTTP returns for SSE responses.
func (m *accountingTransport) WaitAndAddSSEAttributes() {
	if m.sseDone == nil {
		return
	}
	<-m.sseDone

	if m.sseUsage != nil && m.incomingReq != nil {
		usage := m.sseUsage.Usage
		model := m.sseUsage.Model

		// Debit credit from user's balance
		var remainingCredit float64 = -1
		if m.creditMgr != nil && m.userID != "" {
			if creditInfo, err := m.creditMgr.DebitCredit(m.incomingReq.Context(), m.userID, usage.CostUSD); err != nil {
				m.log.ErrorContext(m.incomingReq.Context(), "failed to debit LLM credit (SSE)", "user_id", m.userID, "cost_usd", usage.CostUSD, "error", err)
			} else if creditInfo != nil {
				remainingCredit = creditInfo.Available
			}
		}

		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("llm_model", model))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("vm_name", m.boxName))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("user_id", m.userID))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("input_tokens", usage.InputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("output_tokens", usage.OutputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("cache_creation_tokens", usage.CacheCreationInputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("cache_read_tokens", usage.CacheReadInputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Float64("cost_usd", usage.CostUSD))
		if remainingCredit >= 0 {
			sloghttp.AddCustomAttributes(m.incomingReq, slog.Float64("remaining_credit_usd", remainingCredit))
		}
	}
}

// anthropicResponseUsageInfo extracts usage-relevant information from an Anthropic response.
type anthropicResponseUsageInfo struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage *Usage `json:"usage"`
}

// openaiResponseUsageInfo extracts usage-relevant information from an openai-compatible response.
type openaiResponseUsageInfo struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details,omitempty"`
	} `json:"usage"`
}

// Helper function to extract rate limit values from headers and set gauge metrics
func setRateLimitGauge(header http.Header, headerName, labelValue string) {
	if headerValue := header.Get(headerName); headerValue != "" {
		if val, err := strconv.Atoi(headerValue); err == nil {
			anthropicRateLimitGauge.WithLabelValues(labelValue).Set(float64(val))
		}
	}
}

// isFreeEndpoint returns true if the path is a free endpoint that doesn't
// return usage data. These are endpoints like /models that just list
// available models without consuming any tokens.
func isFreeEndpoint(path string) bool {
	// Exact matches
	switch path {
	case "/v1/models", "/inference/v1/models":
		return true
	}
	// Prefix matches for model details (e.g., /v1/models/gpt-4)
	if strings.HasPrefix(path, "/v1/models/") || strings.HasPrefix(path, "/inference/v1/models/") {
		return true
	}
	return false
}

// isGzipped returns true if the data appears to be gzip-compressed.
// It checks both the Content-Encoding header (case-insensitive, handles multiple
// values) and the gzip magic bytes (0x1f 0x8b) as a fallback.
func isGzipped(contentEncoding string, data []byte) bool {
	// Check header first (case-insensitive, handles "gzip", "GZIP", "gzip, br", etc.)
	if strings.Contains(strings.ToLower(contentEncoding), "gzip") {
		return true
	}
	// Fallback: check gzip magic bytes
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}
