package llmgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"exe.dev/llmpricing"
)

func TestEffectiveTokens(t *testing.T) {
	tests := []struct {
		name           string
		json           string
		wantPrompt     int
		wantCompletion int
		wantCached     uint64
	}{
		{
			name:           "chat completions format",
			json:           `{"id":"abc","model":"gpt-4","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantCached:     0,
		},
		{
			name:           "chat completions with cache",
			json:           `{"id":"abc","model":"gpt-4","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":80}}}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantCached:     80,
		},
		{
			name:           "responses API format",
			json:           `{"id":"resp_abc","model":"gpt-5.3-codex","usage":{"input_tokens":200,"output_tokens":75,"total_tokens":275}}`,
			wantPrompt:     200,
			wantCompletion: 75,
			wantCached:     0,
		},
		{
			name:           "responses API with cache",
			json:           `{"id":"resp_abc","model":"gpt-5.3-codex","usage":{"input_tokens":200,"output_tokens":75,"total_tokens":275,"input_tokens_details":{"cached_tokens":150}}}`,
			wantPrompt:     200,
			wantCompletion: 75,
			wantCached:     150,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var oi openaiResponseUsageInfo
			if err := json.Unmarshal([]byte(tt.json), &oi); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			prompt, completion, cached := oi.effectiveTokens()
			if prompt != tt.wantPrompt {
				t.Errorf("prompt = %d, want %d", prompt, tt.wantPrompt)
			}
			if completion != tt.wantCompletion {
				t.Errorf("completion = %d, want %d", completion, tt.wantCompletion)
			}
			if cached != tt.wantCached {
				t.Errorf("cached = %d, want %d", cached, tt.wantCached)
			}
		})
	}
}

// logCapture captures slog records for inspection in tests.
type logCapture struct {
	records []slog.Record
}

func (l *logCapture) handler() slog.Handler {
	return &logCaptureHandler{capture: l}
}

type logCaptureHandler struct {
	capture *logCapture
	attrs   []slog.Attr
}

func (h *logCaptureHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *logCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	// Add pre-attached attrs to the record
	for _, a := range h.attrs {
		r.AddAttrs(a)
	}
	h.capture.records = append(h.capture.records, r)
	return nil
}

func (h *logCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &logCaptureHandler{capture: h.capture, attrs: newAttrs}
}

func (h *logCaptureHandler) WithGroup(_ string) slog.Handler {
	return h
}

// findRecord returns the first log record matching the given message.
func (l *logCapture) findRecord(msg string) *slog.Record {
	for i := range l.records {
		if l.records[i].Message == msg {
			return &l.records[i]
		}
	}
	return nil
}

// attrMap returns all attributes from a record as a map.
func attrMap(r *slog.Record) map[string]any {
	m := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	return m
}

// proxyThroughAccounting sets up an accountingTransport -> mock backend and
// returns the response recorder plus the transport for inspection.
func proxyThroughAccounting(
	t *testing.T,
	provider llmpricing.Provider,
	backendResponse string,
	requestPath string,
	requestBody string,
	logger *slog.Logger,
) (*httptest.ResponseRecorder, *accountingTransport) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(backendResponse))
	}))
	t.Cleanup(backend.Close)

	mockURL, _ := url.Parse(backend.URL)

	incomingReq := httptest.NewRequest("POST", requestPath,
		strings.NewReader(requestBody))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     provider,
		log:          logger,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user",
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = mockURL.Host
			r.Out.Host = mockURL.Host
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	return rr, transport
}

// TestAccountingTransport_OpenAI_ChatCompletions verifies accounting for
// standard Chat Completions API responses (prompt_tokens/completion_tokens).
func TestAccountingTransport_OpenAI_ChatCompletions(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	backend := `{
		"id": "chatcmpl-abc123",
		"model": "gpt-4.1-2025-04-14",
		"usage": {
			"prompt_tokens": 500,
			"completion_tokens": 120,
			"total_tokens": 620,
			"prompt_tokens_details": {
				"cached_tokens": 300
			}
		}
	}`

	rr, _ := proxyThroughAccounting(t,
		llmpricing.ProviderOpenAI,
		backend,
		"/_/gateway/openai/v1/chat/completions",
		`{"model":"gpt-4.1-2025-04-14","messages":[]}`,
		logger,
	)

	// Verify cost header is present and parseable
	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Fatal("missing Exedev-Gateway-Cost header")
	}
	costUSD, err := strconv.ParseFloat(costHeader, 64)
	if err != nil {
		t.Fatalf("bad Exedev-Gateway-Cost header %q: %v", costHeader, err)
	}
	if costUSD <= 0 {
		t.Fatalf("cost should be > 0, got %f", costUSD)
	}

	// Manually compute expected cost:
	// gpt-4.1: Input=200, Output=800, CacheRead=50 (cents per million tokens)
	// input_tokens=500, completion_tokens=120, cache_read=300
	expectedMicroCents := uint64(500)*200 + uint64(120)*800 + uint64(300)*50
	expectedUSD := float64(expectedMicroCents) / 100_000_000
	t.Logf("cost: got=%s expected=%.6f", costHeader, expectedUSD)
	if fmt.Sprintf("%.6f", costUSD) != fmt.Sprintf("%.6f", expectedUSD) {
		t.Errorf("cost mismatch: got %.6f, want %.6f", costUSD, expectedUSD)
	}

	// Check log record
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	t.Logf("debitResponse attrs: %v", attrs)

	assertAttr(t, attrs, "model", "gpt-4.1-2025-04-14")
	assertAttr(t, attrs, "message_id", "chatcmpl-abc123")
	assertAttrUint(t, attrs, "input_tokens", 500)
	assertAttrUint(t, attrs, "output_tokens", 120)
	assertAttrUint(t, attrs, "cache_read_tokens", 300)
}

// TestAccountingTransport_OpenAI_ResponsesAPI verifies accounting for the
// Responses API format (input_tokens/output_tokens) used by codex models.
func TestAccountingTransport_OpenAI_ResponsesAPI(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	backend := `{
		"id": "resp_abc123",
		"object": "response",
		"model": "gpt-5.3-codex",
		"status": "completed",
		"output": [{
			"type": "message",
			"role": "assistant",
			"content": [{"type": "output_text", "text": "hello"}]
		}],
		"usage": {
			"input_tokens": 1500,
			"output_tokens": 200,
			"total_tokens": 1700,
			"input_tokens_details": {
				"cached_tokens": 1200
			},
			"output_tokens_details": {
				"reasoning_tokens": 50
			}
		}
	}`

	rr, _ := proxyThroughAccounting(t,
		llmpricing.ProviderOpenAI,
		backend,
		"/_/gateway/openai/v1/responses",
		`{"model":"gpt-5.3-codex","input":[]}`,
		logger,
	)

	// Verify cost header
	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Fatal("missing Exedev-Gateway-Cost header")
	}
	costUSD, err := strconv.ParseFloat(costHeader, 64)
	if err != nil {
		t.Fatalf("bad Exedev-Gateway-Cost header %q: %v", costHeader, err)
	}
	if costUSD <= 0 {
		t.Fatalf("cost should be > 0, got %f", costUSD)
	}

	// gpt-5.3-codex: Input=175, Output=1400, CacheRead=17
	// input_tokens=1500, output_tokens=200, cache_read=1200
	expectedMicroCents := uint64(1500)*175 + uint64(200)*1400 + uint64(1200)*17
	expectedUSD := float64(expectedMicroCents) / 100_000_000
	t.Logf("cost: got=%s expected=%.6f", costHeader, expectedUSD)
	if fmt.Sprintf("%.6f", costUSD) != fmt.Sprintf("%.6f", expectedUSD) {
		t.Errorf("cost mismatch: got %.6f, want %.6f", costUSD, expectedUSD)
	}

	// Check log record
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	t.Logf("debitResponse attrs: %v", attrs)

	assertAttr(t, attrs, "model", "gpt-5.3-codex")
	assertAttr(t, attrs, "message_id", "resp_abc123")
	assertAttrUint(t, attrs, "input_tokens", 1500)
	assertAttrUint(t, attrs, "output_tokens", 200)
	assertAttrUint(t, attrs, "cache_read_tokens", 1200)

	// Verify the response body is passed through unmodified
	var respBody map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if respBody["id"] != "resp_abc123" {
		t.Errorf("response body id = %v, want resp_abc123", respBody["id"])
	}
}

// TestAccountingTransport_OpenAI_ResponsesAPI_NoCache verifies accounting
// when the Responses API returns no cache details.
func TestAccountingTransport_OpenAI_ResponsesAPI_NoCache(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	backend := `{
		"id": "resp_nocache",
		"model": "gpt-5.3-codex",
		"output": [{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],
		"usage": {
			"input_tokens": 800,
			"output_tokens": 100,
			"total_tokens": 900
		}
	}`

	rr, _ := proxyThroughAccounting(t,
		llmpricing.ProviderOpenAI,
		backend,
		"/_/gateway/openai/v1/responses",
		`{"model":"gpt-5.3-codex","input":[]}`,
		logger,
	)

	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Fatal("missing Exedev-Gateway-Cost header")
	}

	// gpt-5.3-codex: Input=175, Output=1400, CacheRead=17
	// no cache, so only input + output
	expectedMicroCents := uint64(800)*175 + uint64(100)*1400
	expectedUSD := float64(expectedMicroCents) / 100_000_000
	costUSD, _ := strconv.ParseFloat(costHeader, 64)
	if fmt.Sprintf("%.6f", costUSD) != fmt.Sprintf("%.6f", expectedUSD) {
		t.Errorf("cost mismatch: got %.6f, want %.6f", costUSD, expectedUSD)
	}

	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	assertAttr(t, attrs, "model", "gpt-5.3-codex")
	assertAttrUint(t, attrs, "input_tokens", 800)
	assertAttrUint(t, attrs, "output_tokens", 100)
	assertAttrUint(t, attrs, "cache_read_tokens", 0)
}

// TestAccountingTransport_Anthropic verifies accounting for Anthropic responses.
func TestAccountingTransport_Anthropic(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	backend := `{
		"id": "msg_ant123",
		"model": "claude-sonnet-4-20250514",
		"usage": {
			"input_tokens": 400,
			"output_tokens": 80,
			"cache_creation_input_tokens": 200,
			"cache_read_input_tokens": 150
		}
	}`

	rr, _ := proxyThroughAccounting(t,
		llmpricing.ProviderAnthropic,
		backend,
		"/_/gateway/anthropic/v1/messages",
		`{"model":"claude-sonnet-4-20250514","messages":[]}`,
		logger,
	)

	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Fatal("missing Exedev-Gateway-Cost header")
	}
	costUSD, _ := strconv.ParseFloat(costHeader, 64)
	if costUSD <= 0 {
		t.Fatalf("cost should be > 0, got %f", costUSD)
	}
	t.Logf("Anthropic cost: %s", costHeader)

	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	t.Logf("debitResponse attrs: %v", attrs)

	assertAttr(t, attrs, "model", "claude-sonnet-4-20250514")
	assertAttr(t, attrs, "message_id", "msg_ant123")
	assertAttrUint(t, attrs, "input_tokens", 400)
	assertAttrUint(t, attrs, "output_tokens", 80)
	assertAttrUint(t, attrs, "cache_creation_tokens", 200)
	assertAttrUint(t, attrs, "cache_read_tokens", 150)
}

// TestAccountingTransport_SSE_ResponsesAPI verifies accounting for SSE
// streaming with the Responses API format.
func TestAccountingTransport_SSE_ResponsesAPI(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	// Build an SSE stream that ends with a usage event
	var sseBuf bytes.Buffer
	sseBuf.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
	sseBuf.WriteString("data: {\"type\":\"response.completed\",\"id\":\"resp_sse123\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":2000,\"output_tokens\":300,\"total_tokens\":2300,\"input_tokens_details\":{\"cached_tokens\":1800}}}\n\n")
	sseData := sseBuf.String()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseData))
	}))
	defer backend.Close()

	mockURL, _ := url.Parse(backend.URL)

	incomingReq := httptest.NewRequest("POST", "/_/gateway/openai/v1/responses",
		strings.NewReader(`{"model":"gpt-5.3-codex","input":[],"stream":true}`))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderOpenAI,
		log:          logger,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user",
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = mockURL.Host
			r.Out.Host = mockURL.Host
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)
	transport.WaitAndAddSSEAttributes()

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify the SSE body was passed through
	body := rr.Body.String()
	if !strings.Contains(body, "hello") {
		t.Errorf("SSE body missing expected content: %s", body)
	}

	// Check log record
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found in SSE stream")
	}
	attrs := attrMap(debit)
	t.Logf("SSE debitResponse attrs: %v", attrs)

	assertAttr(t, attrs, "model", "gpt-5.3-codex")
	assertAttr(t, attrs, "message_id", "resp_sse123")
	assertAttrUint(t, attrs, "input_tokens", 2000)
	assertAttrUint(t, attrs, "output_tokens", 300)
	assertAttrUint(t, attrs, "cache_read_tokens", 1800)

	// Verify stored usage for WaitAndAddSSEAttributes
	if transport.sseUsage == nil {
		t.Fatal("sseUsage was not stored")
	}
	if transport.sseUsage.Model != "gpt-5.3-codex" {
		t.Errorf("sseUsage.Model = %s, want gpt-5.3-codex", transport.sseUsage.Model)
	}
	if transport.sseUsage.Usage.InputTokens != 2000 {
		t.Errorf("sseUsage.InputTokens = %d, want 2000", transport.sseUsage.Usage.InputTokens)
	}
	if transport.sseUsage.Usage.CacheReadInputTokens != 1800 {
		t.Errorf("sseUsage.CacheReadInputTokens = %d, want 1800", transport.sseUsage.Usage.CacheReadInputTokens)
	}
	if transport.sseUsage.Usage.CostUSD <= 0 {
		t.Errorf("sseUsage.CostUSD should be > 0, got %f", transport.sseUsage.Usage.CostUSD)
	}
}

// TestAccountingTransport_OpenAI_ResponsesAPI_LiveCodex makes a real request
// to the OpenAI Responses API and verifies the gateway would account for it correctly.
func TestAccountingTransport_OpenAI_ResponsesAPI_LiveCodex(t *testing.T) {
	apiKey := getOpenAIKey(t)

	logs := &logCapture{}
	logger := slog.New(logs.handler())

	// Build a real OpenAI Responses API request
	reqBody := `{"model":"gpt-5.3-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say hello in one word."}]}],"max_output_tokens":50}`

	// We'll proxy through the accounting transport to the real OpenAI API.
	openaiURL, _ := url.Parse("https://api.openai.com")

	incomingReq := httptest.NewRequest("POST", "/_/gateway/openai/v1/responses",
		strings.NewReader(reqBody))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderOpenAI,
		log:          logger,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user",
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = openaiURL.Host
			r.Out.URL.Path = "/v1/responses"
			r.Out.Host = openaiURL.Host
			r.Out.Header.Set("Authorization", "Bearer "+apiKey)
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Parse the response to get the raw usage
	var rawResp struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	respBody := rr.Body.Bytes()
	if err := json.Unmarshal(respBody, &rawResp); err != nil {
		t.Fatalf("failed to parse response: %v\nbody: %s", err, string(respBody))
	}

	t.Logf("Live response: id=%s model=%s", rawResp.ID, rawResp.Model)
	t.Logf("Raw usage: input_tokens=%d output_tokens=%d total_tokens=%d",
		rawResp.Usage.InputTokens, rawResp.Usage.OutputTokens, rawResp.Usage.TotalTokens)
	if rawResp.Usage.InputTokensDetails != nil {
		t.Logf("Raw cache: cached_tokens=%d", rawResp.Usage.InputTokensDetails.CachedTokens)
	}

	// Basic sanity: tokens should be non-zero
	if rawResp.Usage.InputTokens == 0 {
		t.Error("input_tokens should not be 0")
	}
	if rawResp.Usage.OutputTokens == 0 {
		t.Error("output_tokens should not be 0")
	}

	// Verify cost header
	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Fatal("missing Exedev-Gateway-Cost header")
	}
	costUSD, err := strconv.ParseFloat(costHeader, 64)
	if err != nil {
		t.Fatalf("bad cost header %q: %v", costHeader, err)
	}
	if costUSD <= 0 {
		t.Fatalf("cost should be > 0, got %f", costUSD)
	}
	t.Logf("Gateway cost header: %s USD", costHeader)

	// Verify the cost matches our pricing calculation
	var cachedTokens uint64
	if rawResp.Usage.InputTokensDetails != nil {
		cachedTokens = uint64(rawResp.Usage.InputTokensDetails.CachedTokens)
	}
	expectedCost := llmpricing.CalculateCost(llmpricing.ProviderOpenAI, rawResp.Model, llmpricing.Usage{
		InputTokens:          uint64(rawResp.Usage.InputTokens),
		OutputTokens:         uint64(rawResp.Usage.OutputTokens),
		CacheReadInputTokens: cachedTokens,
	})
	if fmt.Sprintf("%.6f", costUSD) != fmt.Sprintf("%.6f", expectedCost) {
		t.Errorf("cost mismatch: header=%.6f expected=%.6f", costUSD, expectedCost)
	}

	// Verify log record matches the raw response
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	t.Logf("debitResponse log attrs: %v", attrs)

	assertAttr(t, attrs, "message_id", rawResp.ID)
	// Model in logs should match the response model
	assertAttr(t, attrs, "model", rawResp.Model)
	assertAttrUint(t, attrs, "input_tokens", uint64(rawResp.Usage.InputTokens))
	assertAttrUint(t, attrs, "output_tokens", uint64(rawResp.Usage.OutputTokens))
	assertAttrUint(t, attrs, "cache_read_tokens", cachedTokens)
}

// TestAccountingTransport_OpenAI_ChatCompletions_Live makes a real request
// through the Chat Completions API and verifies accounting.
func TestAccountingTransport_OpenAI_ChatCompletions_Live(t *testing.T) {
	apiKey := getOpenAIKey(t)

	logs := &logCapture{}
	logger := slog.New(logs.handler())

	reqBody := `{"model":"gpt-4.1-nano-2025-04-14","messages":[{"role":"user","content":"Say hello."}],"max_tokens":10}`

	openaiURL, _ := url.Parse("https://api.openai.com")

	incomingReq := httptest.NewRequest("POST", "/_/gateway/openai/v1/chat/completions",
		strings.NewReader(reqBody))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderOpenAI,
		log:          logger,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user",
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = openaiURL.Host
			r.Out.URL.Path = "/v1/chat/completions"
			r.Out.Host = openaiURL.Host
			r.Out.Header.Set("Authorization", "Bearer "+apiKey)
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d; body: %s", rr.Code, rr.Body.String())
	}

	// Parse raw response
	var rawResp struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			TotalTokens         int `json:"total_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &rawResp); err != nil {
		t.Fatalf("failed to parse: %v\nbody: %s", err, rr.Body.String())
	}

	t.Logf("Live ChatCompletions: id=%s model=%s prompt=%d completion=%d total=%d",
		rawResp.ID, rawResp.Model, rawResp.Usage.PromptTokens,
		rawResp.Usage.CompletionTokens, rawResp.Usage.TotalTokens)

	// Verify cost header
	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Fatal("missing Exedev-Gateway-Cost header")
	}
	costUSD, _ := strconv.ParseFloat(costHeader, 64)
	if costUSD <= 0 {
		t.Fatalf("cost should be > 0, got %f", costUSD)
	}
	t.Logf("Gateway cost: %s USD", costHeader)

	// Compute expected cost
	var cachedTokens uint64
	if rawResp.Usage.PromptTokensDetails != nil {
		cachedTokens = uint64(rawResp.Usage.PromptTokensDetails.CachedTokens)
	}
	expectedCost := llmpricing.CalculateCost(llmpricing.ProviderOpenAI, rawResp.Model, llmpricing.Usage{
		InputTokens:          uint64(rawResp.Usage.PromptTokens),
		OutputTokens:         uint64(rawResp.Usage.CompletionTokens),
		CacheReadInputTokens: cachedTokens,
	})
	if fmt.Sprintf("%.6f", costUSD) != fmt.Sprintf("%.6f", expectedCost) {
		t.Errorf("cost mismatch: header=%.6f expected=%.6f", costUSD, expectedCost)
	}

	// Check logs
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	t.Logf("debitResponse attrs: %v", attrs)

	assertAttr(t, attrs, "model", rawResp.Model)
	assertAttr(t, attrs, "message_id", rawResp.ID)
	assertAttrUint(t, attrs, "input_tokens", uint64(rawResp.Usage.PromptTokens))
	assertAttrUint(t, attrs, "output_tokens", uint64(rawResp.Usage.CompletionTokens))
	assertAttrUint(t, attrs, "cache_read_tokens", cachedTokens)
}

// TestAccountingTransport_Anthropic_WebSearch verifies that server_tool_use
// (web_search_requests) adds per-search cost on top of token cost.
func TestAccountingTransport_Anthropic_WebSearch(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	backend := `{
		"id": "msg_websearch1",
		"model": "claude-sonnet-4-5",
		"usage": {
			"input_tokens": 10704,
			"output_tokens": 138,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens": 0,
			"server_tool_use": {
				"web_search_requests": 1,
				"web_fetch_requests": 0
			}
		}
	}`

	rr, _ := proxyThroughAccounting(t,
		llmpricing.ProviderAnthropic,
		backend,
		"/_/gateway/anthropic/v1/messages",
		`{"model":"claude-sonnet-4-5","messages":[]}`,
		logger,
	)

	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Fatal("missing Exedev-Gateway-Cost header")
	}
	costUSD, err := strconv.ParseFloat(costHeader, 64)
	if err != nil {
		t.Fatalf("bad Exedev-Gateway-Cost header %q: %v", costHeader, err)
	}

	// claude-sonnet-4-5: Input=300, Output=1500, CacheRead=30, CacheCreation=375
	// Token cost: 10704*300 + 138*1500 = 3,211,200 + 207,000 = 3,418,200 microCents
	tokenMicroCents := uint64(10704)*300 + uint64(138)*1500
	tokenCostUSD := float64(tokenMicroCents) / 100_000_000

	// Web search cost: 1 search * $0.01 = $0.01
	webSearchCostUSD := 0.01
	expectedUSD := tokenCostUSD + webSearchCostUSD

	t.Logf("cost: got=%s expected=%.6f (tokens=%.6f + websearch=%.6f)", costHeader, expectedUSD, tokenCostUSD, webSearchCostUSD)
	if fmt.Sprintf("%.6f", costUSD) != fmt.Sprintf("%.6f", expectedUSD) {
		t.Errorf("cost mismatch: got %.6f, want %.6f", costUSD, expectedUSD)
	}

	// Verify cost is strictly greater than token-only cost
	if costUSD <= tokenCostUSD {
		t.Errorf("cost %.6f should be greater than token-only cost %.6f", costUSD, tokenCostUSD)
	}

	// Check log record includes web_search_requests
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	t.Logf("debitResponse attrs: %v", attrs)

	assertAttr(t, attrs, "model", "claude-sonnet-4-5")
	assertAttr(t, attrs, "message_id", "msg_websearch1")
	assertAttrUint(t, attrs, "input_tokens", 10704)
	assertAttrUint(t, attrs, "output_tokens", 138)
	assertAttrUint(t, attrs, "web_search_requests", 1)
}

// TestAccountingTransport_Anthropic_WebSearch_SSE verifies that server_tool_use
// in SSE streaming adds per-search cost.
func TestAccountingTransport_Anthropic_WebSearch_SSE(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	// Build a realistic SSE stream: message_start has model/id nested inside "message",
	// message_delta has usage with server_tool_use at top level.
	var sseBuf bytes.Buffer
	sseBuf.WriteString(`data: {"type":"message_start","message":{"model":"claude-sonnet-4-5","id":"msg_sse_ws","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10698,"output_tokens":3}}}` + "\n\n")
	sseBuf.WriteString("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
	sseBuf.WriteString(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10698,"output_tokens":131,"server_tool_use":{"web_search_requests":1,"web_fetch_requests":0}}}` + "\n\n")
	sseData := sseBuf.String()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseData))
	}))
	defer backend.Close()

	mockURL, _ := url.Parse(backend.URL)

	incomingReq := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[],"stream":true}`))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderAnthropic,
		log:          logger,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user",
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = mockURL.Host
			r.Out.Host = mockURL.Host
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)
	transport.WaitAndAddSSEAttributes()

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify the SSE body was passed through
	body := rr.Body.String()
	if !strings.Contains(body, "hello") {
		t.Errorf("SSE body missing expected content: %s", body)
	}

	// Check log record includes web_search_requests
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found in SSE stream")
	}
	attrs := attrMap(debit)
	t.Logf("SSE debitResponse attrs: %v", attrs)

	assertAttrUint(t, attrs, "input_tokens", 10698)
	assertAttrUint(t, attrs, "output_tokens", 131)
	assertAttrUint(t, attrs, "web_search_requests", 1)

	// Verify stored usage for WaitAndAddSSEAttributes
	if transport.sseUsage == nil {
		t.Fatal("sseUsage was not stored")
	}

	// Verify cost includes web search charge
	// Token cost: 10698*300 + 131*1500 = 3,209,400 + 196,500 = 3,405,900 microCents
	tokenMicroCents := uint64(10698)*300 + uint64(131)*1500
	tokenCostUSD := float64(tokenMicroCents) / 100_000_000
	webSearchCostUSD := 0.01
	expectedCostUSD := tokenCostUSD + webSearchCostUSD

	gotCost := transport.sseUsage.Usage.CostUSD
	t.Logf("SSE cost: got=%.6f expected=%.6f (tokens=%.6f + websearch=%.6f)", gotCost, expectedCostUSD, tokenCostUSD, webSearchCostUSD)
	if fmt.Sprintf("%.6f", gotCost) != fmt.Sprintf("%.6f", expectedCostUSD) {
		t.Errorf("SSE cost mismatch: got %.6f, want %.6f", gotCost, expectedCostUSD)
	}

	// Verify ServerToolUse is tracked
	if transport.sseUsage.Usage.ServerToolUse == nil {
		t.Fatal("ServerToolUse should be non-nil")
	}
	if transport.sseUsage.Usage.ServerToolUse["web_search_requests"] != 1 {
		t.Errorf("ServerToolUse[web_search_requests] = %d, want 1",
			transport.sseUsage.Usage.ServerToolUse["web_search_requests"])
	}
}

func getOpenAIKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set, skipping live test")
	}
	return key
}

func assertAttr(t *testing.T, attrs map[string]any, key, want string) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Errorf("missing log attr %q", key)
		return
	}
	if fmt.Sprint(got) != want {
		t.Errorf("log attr %q = %v, want %s", key, got, want)
	}
}

func assertAttrUint(t *testing.T, attrs map[string]any, key string, want uint64) {
	t.Helper()
	got, ok := attrs[key]
	if !ok {
		t.Errorf("missing log attr %q", key)
		return
	}
	var gotUint uint64
	switch v := got.(type) {
	case uint64:
		gotUint = v
	case int64:
		gotUint = uint64(v)
	case int:
		gotUint = uint64(v)
	default:
		t.Errorf("log attr %q has type %T, want numeric", key, got)
		return
	}
	if gotUint != want {
		t.Errorf("log attr %q = %d, want %d", key, gotUint, want)
	}
}

// TestAccountingTransport_Anthropic_SSE verifies accounting for Anthropic SSE
// streaming with the real event format. Anthropic SSE has:
//   - message_start: model/id nested inside a "message" wrapper
//   - message_delta: usage at top level but NO model or id
//
// This test verifies that the gateway correctly extracts the model from
// message_start and pairs it with the final usage from message_delta.
func TestAccountingTransport_Anthropic_SSE(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	// Build a realistic Anthropic SSE stream (from actual curl output).
	// Key: message_start has model/id inside "message", message_delta has usage but NO model/id.
	var sseBuf bytes.Buffer
	sseBuf.WriteString("event: message_start\n")
	sseBuf.WriteString(`data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_017N87RsTpk77EtJoeyifnHh","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}` + "\n\n")
	sseBuf.WriteString("event: content_block_start\n")
	sseBuf.WriteString(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n")
	sseBuf.WriteString("event: ping\n")
	sseBuf.WriteString(`data: {"type": "ping"}` + "\n\n")
	sseBuf.WriteString("event: content_block_delta\n")
	sseBuf.WriteString(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello! How"}}` + "\n\n")
	sseBuf.WriteString("event: content_block_delta\n")
	sseBuf.WriteString(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" can I help you today?"}}` + "\n\n")
	sseBuf.WriteString("event: content_block_stop\n")
	sseBuf.WriteString(`data: {"type":"content_block_stop","index":0}` + "\n\n")
	sseBuf.WriteString("event: message_delta\n")
	sseBuf.WriteString(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":12}}` + "\n\n")
	sseBuf.WriteString("event: message_stop\n")
	sseBuf.WriteString(`data: {"type":"message_stop"}` + "\n\n")
	sseData := sseBuf.String()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseData))
	}))
	defer backend.Close()

	mockURL, _ := url.Parse(backend.URL)

	incomingReq := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-5-20250929","messages":[],"stream":true}`))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderAnthropic,
		log:          logger,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user",
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = mockURL.Host
			r.Out.Host = mockURL.Host
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)
	transport.WaitAndAddSSEAttributes()

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify the SSE body was passed through
	body := rr.Body.String()
	if !strings.Contains(body, "Hello! How") {
		t.Errorf("SSE body missing expected content: %s", body)
	}

	// Verify stored usage
	if transport.sseUsage == nil {
		t.Fatal("sseUsage was not stored")
	}
	if transport.sseUsage.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("sseUsage.Model = %q, want %q", transport.sseUsage.Model, "claude-sonnet-4-5-20250929")
	}
	if transport.sseUsage.MessageID != "msg_017N87RsTpk77EtJoeyifnHh" {
		t.Errorf("sseUsage.MessageID = %q, want %q", transport.sseUsage.MessageID, "msg_017N87RsTpk77EtJoeyifnHh")
	}
	if transport.sseUsage.Usage.InputTokens != 10 {
		t.Errorf("sseUsage.InputTokens = %d, want 10", transport.sseUsage.Usage.InputTokens)
	}
	if transport.sseUsage.Usage.OutputTokens != 12 {
		t.Errorf("sseUsage.OutputTokens = %d, want 12", transport.sseUsage.Usage.OutputTokens)
	}

	// Verify cost is correctly calculated (not zero!)
	// claude-sonnet-4-5-20250929: Input=300, Output=1500 (cents per million tokens)
	// Cost = (10 * 300 + 12 * 1500) / 100_000_000 = 21000 / 100_000_000
	expectedMicroCents := uint64(10)*300 + uint64(12)*1500
	expectedUSD := float64(expectedMicroCents) / 100_000_000
	gotCost := transport.sseUsage.Usage.CostUSD
	if gotCost <= 0 {
		t.Errorf("sseUsage.CostUSD should be > 0, got %f (model was likely empty)", gotCost)
	}
	if fmt.Sprintf("%.10f", gotCost) != fmt.Sprintf("%.10f", expectedUSD) {
		t.Errorf("cost mismatch: got %.10f, want %.10f", gotCost, expectedUSD)
	}
	t.Logf("SSE cost: got=%.10f expected=%.10f", gotCost, expectedUSD)

	// Check debitResponse log record. The last one (from message_delta) should have
	// the correct model and token counts.
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found in SSE stream")
	}
	attrs := attrMap(debit)
	t.Logf("SSE debitResponse attrs: %v", attrs)

	assertAttr(t, attrs, "model", "claude-sonnet-4-5-20250929")
	assertAttr(t, attrs, "message_id", "msg_017N87RsTpk77EtJoeyifnHh")
	assertAttrUint(t, attrs, "input_tokens", 10)
	assertAttrUint(t, attrs, "output_tokens", 12)
}

// TestAccountingTransport_Anthropic_SSE_Live makes a real streaming request
// to the Anthropic API and verifies the gateway accounts for it correctly.
func TestAccountingTransport_Anthropic_SSE_Live(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping live test")
	}

	logs := &logCapture{}
	logger := slog.New(logs.handler())

	reqBody := `{"model":"claude-haiku-4-5-20251001","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"Say hello in one word."}]}`

	anthrURL, _ := url.Parse("https://api.anthropic.com")

	incomingReq := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(reqBody))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderAnthropic,
		log:          logger,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user",
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = anthrURL.Host
			r.Out.URL.Path = "/v1/messages"
			r.Out.Host = anthrURL.Host
			r.Out.Header.Set("x-api-key", apiKey)
			r.Out.Header.Set("anthropic-version", "2023-06-01")
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)
	transport.WaitAndAddSSEAttributes()

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Log the raw SSE body for debugging
	t.Logf("Raw SSE body (first 2000 chars):\n%s", rr.Body.String()[:min(2000, rr.Body.Len())])

	// Verify stored usage
	if transport.sseUsage == nil {
		t.Fatal("sseUsage was not stored")
	}

	t.Logf("Live SSE usage: model=%s message_id=%s input=%d output=%d cost=%.10f",
		transport.sseUsage.Model,
		transport.sseUsage.MessageID,
		transport.sseUsage.Usage.InputTokens,
		transport.sseUsage.Usage.OutputTokens,
		transport.sseUsage.Usage.CostUSD)

	// Model should be set (not empty!) - this is the core bug we're testing
	if transport.sseUsage.Model == "" {
		t.Error("sseUsage.Model is empty - model not extracted from message_start")
	}
	// Should be some variant of claude-haiku-4-5
	if !strings.Contains(transport.sseUsage.Model, "claude-haiku-4-5") {
		t.Errorf("sseUsage.Model = %q, want something containing 'claude-haiku-4-5'", transport.sseUsage.Model)
	}

	// Message ID should be set
	if transport.sseUsage.MessageID == "" {
		t.Error("sseUsage.MessageID is empty - id not extracted from message_start")
	}
	if !strings.HasPrefix(transport.sseUsage.MessageID, "msg_") {
		t.Errorf("sseUsage.MessageID = %q, want prefix 'msg_'", transport.sseUsage.MessageID)
	}

	// Token counts should be non-zero
	if transport.sseUsage.Usage.InputTokens == 0 {
		t.Error("sseUsage.InputTokens should not be 0")
	}
	if transport.sseUsage.Usage.OutputTokens == 0 {
		t.Error("sseUsage.OutputTokens should not be 0")
	}

	// Cost should be > 0 (this is the billing bug - cost is 0 when model is empty)
	if transport.sseUsage.Usage.CostUSD <= 0 {
		t.Errorf("sseUsage.CostUSD should be > 0, got %f (billing bug: model was likely empty)", transport.sseUsage.Usage.CostUSD)
	}

	// Verify cost matches our pricing calculation
	expectedCost := llmpricing.CalculateCost(llmpricing.ProviderAnthropic, transport.sseUsage.Model, llmpricing.Usage{
		InputTokens:              transport.sseUsage.Usage.InputTokens,
		OutputTokens:             transport.sseUsage.Usage.OutputTokens,
		CacheCreationInputTokens: transport.sseUsage.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:     transport.sseUsage.Usage.CacheReadInputTokens,
	})
	if fmt.Sprintf("%.10f", transport.sseUsage.Usage.CostUSD) != fmt.Sprintf("%.10f", expectedCost) {
		t.Errorf("cost mismatch: got %.10f, want %.10f", transport.sseUsage.Usage.CostUSD, expectedCost)
	}

	// Verify log record
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	t.Logf("Live SSE debitResponse attrs: %v", attrs)

	// The model in logs should match
	if modelAttr, ok := attrs["model"]; !ok || fmt.Sprint(modelAttr) == "" {
		t.Errorf("debitResponse log missing or empty model attr: %v", attrs["model"])
	}
}
