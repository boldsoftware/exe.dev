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
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"exe.dev/accounting"
)

// accountingTransport wraps http transactions to check and track the client's credit usage
type accountingTransport struct {
	http.RoundTripper
	accountant       accounting.Accountant
	billingAccountID string
	baseURL          string
	apiType          string
	testDebitDone    chan bool // for testing -- if non-nil, best effort send every time a debit occurs
}

func (a *accountingTransport) checkCredits(ctx context.Context, billingAccountID string) error {
	// Get the current balance for the user
	balance, err := a.accountant.GetBalance(ctx, billingAccountID)
	if err != nil {
		slog.Error("accountingTransport.checkCredits", "error", err)
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

	if err := a.checkCredits(r.Context(), a.billingAccountID); err != nil {
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

	ctx := context.Background()

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
			slog.Error("couldn't read unary response body", "error", err)
			return err
		}
		resp.Body = io.NopCloser(bytes.NewReader(data))
		contentEncoding := resp.Header["Content-Encoding"]

		if slices.Contains(contentEncoding, "gzip") {
			gzReader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				slog.Error("accountingTransport couldn't create new gzip reader for unary response body", "error", err)
				return err
			}
			decoded, err := io.ReadAll(gzReader)
			if err != nil {
				slog.Error("accountingTransport couldn't read gzip reader for unary response body", "error", err)
				return err
			}
			gzReader.Close()
			data = decoded
		}
		if err := a.processResponseData(ctx, data); err != nil {
			slog.Error("accountingTransport couldn't process unary JSON response", "processResponseData error", err)
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
		go func() {
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data:") {
					data := strings.TrimPrefix(line, "data:")
					// Process the event data, which may include details for accounting.
					if err := a.processResponseData(ctx, []byte(data)); err != nil {
						slog.Error("Proxy SSE scanner", "processResponseData error", err)
					}
				}
				fmt.Fprintln(bodyWriter, line)
			}
			if err := scanner.Err(); err != nil {
				slog.Error("Proxy SSE scanner", "error", err)
			}
			bodyWriter.Close()
		}()
	default:
		// We just log this rather than return an error, so that the request still gets
		// proxied. We just don't have a way to debit any charges based on usage data that
		// may have been included in the response.
		slog.Error("accountingTransport.modifyResponse", "unrecognized content type", contentType)
	}

	if a.testDebitDone != nil {
		a.testDebitDone <- true
	}

	return nil
}

func (m *accountingTransport) processResponseData(ctx context.Context, data []byte) error {
	usageDebit := accounting.UsageDebit{BillingAccountID: m.billingAccountID, Created: time.Now()}

	switch m.apiType {
	case "anthropic":
		var ui anthropicResponseUsageInfo
		if err := json.Unmarshal(data, &ui); err != nil {
			slog.Error("anthropic json decode", "data", string(data), "error", err)
			return fmt.Errorf("json decode error: %w", err)
		}
		if ui.Usage == nil {
			// Nothing to bill for here.
			return nil
		}
		usageDebit.Usage = *ui.Usage
		usageDebit.Model = ui.Model
		usageDebit.MessageID = ui.ID
		slog.Info("debitResponse", "anthropicResponseUsageInfo", ui)
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
			slog.Debug("openai response has zero token counts, using defaults")
			promptTokens = 1
			completionTokens = 1
		}

		usage := accounting.Usage{
			InputTokens:  uint64(promptTokens),
			OutputTokens: uint64(completionTokens),
		}

		// Use the model from the response, or unknown if not provided
		model := cmp.Or(oi.Model, "oai-unknown")
		usageDebit.Usage = usage
		usageDebit.Model = model
		usageDebit.MessageID = oi.ID
		slog.Info("debitResponse", "openaiResponseUsageInfo", oi)

	default:
		slog.Error("accountingTransport.processResponseData: unknown API type", "apiType", m.apiType)
	}

	uc := accounting.UsageCost(usageDebit.Model, usageDebit.Usage)
	usageDebit.Usage.CostUSD = uc.USD()

	if err := m.accountant.DebitUsage(ctx, usageDebit); err != nil {
		slog.Error("accountingTransport.debitResponse: couldn't debit usage", "error", err)
	}

	// Update Prometheus metrics
	usage := usageDebit.Usage
	model := usageDebit.Model
	tokensCounter.WithLabelValues("input", model, m.apiType).Add(float64(usage.InputTokens))
	tokensCounter.WithLabelValues("cache_creation", model, m.apiType).Add(float64(usage.CacheCreationInputTokens))
	tokensCounter.WithLabelValues("cache_read", model, m.apiType).Add(float64(usage.CacheReadInputTokens))
	tokensCounter.WithLabelValues("output", model, m.apiType).Add(float64(usage.OutputTokens))
	costUSDCounter.WithLabelValues(model, m.apiType).Add(usage.CostUSD)
	return nil
}

// anthropicResponseUsageInfo extracts usage-relevant information from an Anthropic response.
type anthropicResponseUsageInfo struct {
	ID    string            `json:"id"`
	Model string            `json:"model"`
	Usage *accounting.Usage `json:"usage"`
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
