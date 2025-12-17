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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"exe.dev/llmpricing"
	"exe.dev/sqlite"
	sloghttp "github.com/samber/slog-http"
)

// accountingTransport wraps http transactions to check and track the client's credit usage
type accountingTransport struct {
	http.RoundTripper
	db            *sqlite.DB
	apiType       string
	testDebitDone chan bool // for testing -- if non-nil, best effort send every time a debit occurs
	log           *slog.Logger

	// Request context for adding slog attributes
	incomingReq *http.Request
	boxName     string
	userID      string

	// For SSE responses, we store the usage data and add attributes after the stream completes
	sseDone  chan struct{}
	sseUsage *UsageDebit
}

// RoundTrip enforces credit usage limits and records some metrics.
func (a *accountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// TODO: restore latency measurements

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
	if ret != nil && err == nil && a.apiType == "anthropic" {
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
			a.log.Error("couldn't read unary response body", "error", err)
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(data))
		contentEncoding := resp.Header["Content-Encoding"]

		if slices.Contains(contentEncoding, "gzip") {
			gzReader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				a.log.Error("accountingTransport couldn't create new gzip reader for unary response body", "error", err)
				return err
			}
			decoded, err := io.ReadAll(gzReader)
			if err != nil {
				a.log.Error("accountingTransport couldn't read gzip reader for unary response body", "error", err)
				return err
			}
			gzReader.Close()
			data = decoded
		}
		if err := a.processResponseData(data); err != nil {
			a.log.Error("accountingTransport couldn't process unary JSON response", "processResponseData error", err)
			return err
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
		// Set up channel to signal when SSE processing is complete
		a.sseDone = make(chan struct{})
		go func() {
			defer close(a.sseDone)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data:") {
					data := strings.TrimPrefix(line, "data:")
					// Process the event data, which may include details for accounting.
					// For SSE, we use processResponseDataSSE which stores usage for later
					if err := a.processResponseDataSSE([]byte(data)); err != nil {
						a.log.Error("Proxy SSE scanner", "processResponseData error", err)
					}
				}
				fmt.Fprintln(bodyWriter, line)
			}
			if err := scanner.Err(); err != nil {
				a.log.Error("Proxy SSE scanner", "error", err)
			}
			bodyWriter.Close()
		}()
	default:
		// We just log this rather than return an error, so that the request still gets
		// proxied. We just don't have a way to debit any charges based on usage data that
		// may have been included in the response.
		a.log.Error("accountingTransport.modifyResponse", "unrecognized content type", contentType)
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

type UsageDebit struct {
	Usage Usage `json:"usage"`

	Model     string    `json:"model"`
	MessageID string    `json:"message_id"`
	Created   time.Time `json:"created"`
}

func (m *accountingTransport) processResponseData(data []byte) error {
	usageDebit := UsageDebit{Created: time.Now()}

	switch m.apiType {
	case "anthropic":
		var ui anthropicResponseUsageInfo
		if err := json.Unmarshal(data, &ui); err != nil {
			m.log.Error("anthropic json decode", "data", string(data), "error", err)
			return fmt.Errorf("json decode error: %w", err)
		}
		if ui.Usage == nil {
			// Nothing to bill for here.
			return nil
		}
		usageDebit.Usage = *ui.Usage
		usageDebit.Model = ui.Model
		usageDebit.MessageID = ui.ID
		m.log.Info("debitResponse", "anthropicResponseUsageInfo", ui)
	case "openai", "fireworks":
		if len(data) == 0 {
			return fmt.Errorf("empty openai response, skipping accounting")
		}

		var oi openaiResponseUsageInfo
		if err := json.Unmarshal(data, &oi); err != nil {
			return fmt.Errorf("openai json decode error: %v, content: %s", err, string(data))
		}
		if oi.Usage.TotalTokens == 0 {
			return fmt.Errorf("openai response missing usage data, skipping accounting")
		}

		// Convert OpenAI usage to Usage format for accounting
		promptTokens := oi.Usage.PromptTokens
		completionTokens := oi.Usage.CompletionTokens

		// If token counts are zero, set a minimal token count to avoid accounting errors
		if promptTokens == 0 && completionTokens == 0 {
			m.log.Debug("openai response has zero token counts, using defaults")
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
		m.log.Info("debitResponse", "openaiResponseUsageInfo", oi)

	default:
		m.log.Error("accountingTransport.processResponseData: unknown API type", "apiType", m.apiType)
	}

	// Calculate cost based on model pricing
	usage := usageDebit.Usage
	model := usageDebit.Model
	costUSD := llmpricing.CalculateCost(model, llmpricing.Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	})

	// Update Prometheus metrics
	tokensCounter.WithLabelValues("input", model, m.apiType, m.boxName, m.userID).Add(float64(usage.InputTokens))
	tokensCounter.WithLabelValues("cache_creation", model, m.apiType, m.boxName, m.userID).Add(float64(usage.CacheCreationInputTokens))
	tokensCounter.WithLabelValues("cache_read", model, m.apiType, m.boxName, m.userID).Add(float64(usage.CacheReadInputTokens))
	tokensCounter.WithLabelValues("output", model, m.apiType, m.boxName, m.userID).Add(float64(usage.OutputTokens))
	costUSDCounter.WithLabelValues(model, m.apiType, m.boxName, m.userID).Add(costUSD)

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
	}

	return nil
}

// processResponseDataSSE is like processResponseData but stores usage for later attribute addition.
// This is needed for SSE responses where we can't add slog attributes from the goroutine.
func (m *accountingTransport) processResponseDataSSE(data []byte) error {
	usageDebit := UsageDebit{Created: time.Now()}

	switch m.apiType {
	case "anthropic":
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
		m.log.Info("debitResponse", "anthropicResponseUsageInfo", ui)
	case "openai", "fireworks":
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

		usage := Usage{
			InputTokens:  uint64(promptTokens),
			OutputTokens: uint64(completionTokens),
		}

		model := cmp.Or(oi.Model, "oai-unknown")
		usageDebit.Usage = usage
		usageDebit.Model = model
		usageDebit.MessageID = oi.ID
		m.log.Info("debitResponse", "openaiResponseUsageInfo", oi)

	default:
		return nil
	}

	// Calculate cost based on model pricing
	usage := usageDebit.Usage
	model := usageDebit.Model
	costUSD := llmpricing.CalculateCost(model, llmpricing.Usage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	})

	// Update Prometheus metrics immediately
	tokensCounter.WithLabelValues("input", model, m.apiType, m.boxName, m.userID).Add(float64(usage.InputTokens))
	tokensCounter.WithLabelValues("cache_creation", model, m.apiType, m.boxName, m.userID).Add(float64(usage.CacheCreationInputTokens))
	tokensCounter.WithLabelValues("cache_read", model, m.apiType, m.boxName, m.userID).Add(float64(usage.CacheReadInputTokens))
	tokensCounter.WithLabelValues("output", model, m.apiType, m.boxName, m.userID).Add(float64(usage.OutputTokens))
	costUSDCounter.WithLabelValues(model, m.apiType, m.boxName, m.userID).Add(costUSD)

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
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("llm_model", model))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("vm_name", m.boxName))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.String("user_id", m.userID))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("input_tokens", usage.InputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("output_tokens", usage.OutputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("cache_creation_tokens", usage.CacheCreationInputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Uint64("cache_read_tokens", usage.CacheReadInputTokens))
		sloghttp.AddCustomAttributes(m.incomingReq, slog.Float64("cost_usd", usage.CostUSD))
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
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
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
