package llmgateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"exe.dev/llmpricing"
)

func TestEnsureOpenAIStreamOptions(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantChange bool     // should the body be modified?
		wantIncl   bool     // should include_usage be true in output?
		wantKeys   []string // extra keys that must survive in stream_options
	}{
		{
			name:       "streaming request gets stream_options injected",
			input:      `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			wantChange: true,
			wantIncl:   true,
		},
		{
			name:       "streaming request with stream_options already set",
			input:      `{"model":"gpt-4o-mini","stream":true,"stream_options":{"include_usage":true},"messages":[]}`,
			wantChange: false, // already correct, no modification needed
			wantIncl:   true,
		},
		{
			name:       "streaming request with include_usage false gets overridden",
			input:      `{"model":"gpt-4o-mini","stream":true,"stream_options":{"include_usage":false},"messages":[]}`,
			wantChange: true,
			wantIncl:   true,
		},
		{
			name:       "streaming request with stream_options null gets injected",
			input:      `{"model":"gpt-4o-mini","stream":true,"stream_options":null,"messages":[]}`,
			wantChange: true,
			wantIncl:   true,
		},
		{
			name:       "extra keys in stream_options survive round-trip",
			input:      `{"model":"gpt-4o-mini","stream":true,"stream_options":{"include_usage":false,"other_key":123},"messages":[]}`,
			wantChange: true,
			wantIncl:   true,
			wantKeys:   []string{"other_key"},
		},
		{
			name:       "non-streaming request left alone",
			input:      `{"model":"gpt-4o-mini","stream":false,"messages":[]}`,
			wantChange: false,
		},
		{
			name:       "no stream field left alone",
			input:      `{"model":"gpt-4o-mini","messages":[]}`,
			wantChange: false,
		},
		{
			name:       "empty body returns as-is",
			input:      ``,
			wantChange: false,
		},
		{
			name:       "invalid JSON returns as-is",
			input:      `not json at all`,
			wantChange: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ensureOpenAIStreamOptions([]byte(tt.input))
			if err != nil {
				t.Fatal(err)
			}

			changed := string(got) != tt.input
			if changed != tt.wantChange {
				t.Errorf("body changed=%v, want changed=%v\n  input:  %s\n  output: %s", changed, tt.wantChange, tt.input, got)
			}

			if tt.wantIncl {
				var parsed map[string]json.RawMessage
				if err := json.Unmarshal(got, &parsed); err != nil {
					t.Fatalf("output is not valid JSON: %v", err)
				}
				soRaw, ok := parsed["stream_options"]
				if !ok {
					t.Fatal("stream_options not present in output")
				}
				var so map[string]any
				if err := json.Unmarshal(soRaw, &so); err != nil {
					t.Fatalf("stream_options is not a JSON object: %v", err)
				}
				incl, _ := so["include_usage"].(bool)
				if !incl {
					t.Errorf("include_usage=%v, want true; stream_options=%s", so["include_usage"], soRaw)
				}
				for _, key := range tt.wantKeys {
					if _, ok := so[key]; !ok {
						t.Errorf("stream_options missing key %q after round-trip: %s", key, soRaw)
					}
				}
			}
		})
	}
}

// TestOpenAI_SSE_UsageWithStreamOptions verifies the full flow:
// a mock OpenAI backend that only includes usage when stream_options is set
// in the request body, proxied through accountingTransport, and usage is captured.
func TestOpenAI_SSE_UsageWithStreamOptions(t *testing.T) {
	logs := &logCapture{}
	logger := slog.New(logs.handler())

	var capturedBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		// Simulate OpenAI behavior: only include usage if stream_options.include_usage is true
		hasStreamOpts := strings.Contains(capturedBody, `"include_usage"`) &&
			strings.Contains(capturedBody, `"stream_options"`)

		fmt.Fprintln(w, `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Hello!"},"finish_reason":null}]}`)
		fmt.Fprintln(w)

		if hasStreamOpts {
			// OpenAI sends usage in the final chunk when stream_options.include_usage is true
			fmt.Fprintln(w, `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
			fmt.Fprintln(w)
		} else {
			// Without stream_options, the final chunk has no usage
			fmt.Fprintln(w, `data: {"id":"chatcmpl-test","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			fmt.Fprintln(w)
		}

		fmt.Fprintln(w, `data: [DONE]`)
		fmt.Fprintln(w)
	}))
	t.Cleanup(backend.Close)

	mockURL, _ := url.Parse(backend.URL)

	// Simulate what ServeHTTP does: read the body, inject stream_options, restore body.
	reqBody := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"say hi"}]}`
	bodyBytes, err := ensureOpenAIStreamOptions([]byte(reqBody))
	if err != nil {
		t.Fatal(err)
	}

	incomingReq := httptest.NewRequest("POST", "/_/gateway/openai/v1/chat/completions",
		strings.NewReader(string(bodyBytes)))
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
			r.Out.URL.Scheme = mockURL.Scheme
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
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	// The mock backend should have received stream_options in the request body
	if !strings.Contains(capturedBody, `"stream_options"`) {
		t.Errorf("backend did not receive stream_options in request body: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, `"include_usage"`) {
		t.Errorf("backend did not receive include_usage in request body: %s", capturedBody)
	}

	// Usage should have been captured from the SSE stream
	if transport.sseUsage == nil {
		t.Fatal("sseUsage is nil - no usage data captured from SSE stream")
	}
	if transport.sseUsage.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", transport.sseUsage.Usage.InputTokens)
	}
	if transport.sseUsage.Usage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", transport.sseUsage.Usage.OutputTokens)
	}

	// Verify debitResponse log was emitted
	debit := logs.findRecord("debitResponse")
	if debit == nil {
		t.Fatal("no debitResponse log found")
	}
	attrs := attrMap(debit)
	assertAttr(t, attrs, "model", "gpt-4o-mini")
}

// openaiSSEHelper sends a real streaming Chat Completions request to OpenAI
// and returns the transport (for usage inspection) and the raw SSE body.
func openaiSSEHelper(t *testing.T, apiKey string, body string) (*accountingTransport, string) {
	t.Helper()

	logs := &logCapture{}
	logger := slog.New(logs.handler())

	oaiURL, _ := url.Parse("https://api.openai.com")

	incomingReq := httptest.NewRequest("POST", "/_/gateway/openai/v1/chat/completions",
		strings.NewReader(body))
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
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = oaiURL.Host
			r.Out.URL.Path = "/v1/chat/completions"
			r.Out.Host = oaiURL.Host
			r.Out.Header.Set("Authorization", "Bearer "+apiKey)
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
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("got Content-Type %q, want text/event-stream; body: %s", ct, rr.Body.String())
	}

	return transport, rr.Body.String()
}

// TestOpenAI_ChatCompletions_SSE_Live_NoStreamOptions proves the bug:
// without stream_options, OpenAI SSE never includes usage data.
func TestOpenAI_ChatCompletions_SSE_Live_NoStreamOptions(t *testing.T) {
	apiKey := getOpenAIKey(t)

	// Send a streaming request WITHOUT stream_options — the old behavior.
	// gpt-4.1-nano: cheapest model; update if retired.
	reqBody := `{"model":"gpt-4.1-nano","stream":true,"messages":[{"role":"user","content":"Say hi."}],"max_tokens":5}`
	transport, body := openaiSSEHelper(t, apiKey, reqBody)

	t.Logf("Raw SSE body (no stream_options):\n%s", body)

	if transport.sseUsage != nil {
		t.Fatalf("expected sseUsage to be nil without stream_options, but got: input=%d output=%d",
			transport.sseUsage.Usage.InputTokens, transport.sseUsage.Usage.OutputTokens)
	}
	t.Log("Confirmed: without stream_options, OpenAI SSE has no usage data — accounting is blind.")
}

// TestOpenAI_ChatCompletions_SSE_Live_WithStreamOptions proves the fix:
// ensureOpenAIStreamOptions injects stream_options, and OpenAI returns usage.
func TestOpenAI_ChatCompletions_SSE_Live_WithStreamOptions(t *testing.T) {
	apiKey := getOpenAIKey(t)

	// Client sends a streaming request WITHOUT stream_options.
	// ensureOpenAIStreamOptions injects it — this is the fix.
	// gpt-4.1-nano: cheapest model; update if retired.
	reqBody := `{"model":"gpt-4.1-nano","stream":true,"messages":[{"role":"user","content":"Say hi."}],"max_tokens":5}`
	bodyBytes, err := ensureOpenAIStreamOptions([]byte(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bodyBytes), `"stream_options"`) {
		t.Fatalf("ensureOpenAIStreamOptions did not inject stream_options: %s", bodyBytes)
	}

	transport, body := openaiSSEHelper(t, apiKey, string(bodyBytes))

	t.Logf("Raw SSE body (with stream_options):\n%s", body)

	if transport.sseUsage == nil {
		t.Fatal("sseUsage is nil — OpenAI did not include usage in SSE stream")
	}

	t.Logf("Live usage: model=%s input=%d output=%d cost=%.10f",
		transport.sseUsage.Model,
		transport.sseUsage.Usage.InputTokens,
		transport.sseUsage.Usage.OutputTokens,
		transport.sseUsage.Usage.CostUSD)

	if transport.sseUsage.Usage.InputTokens == 0 {
		t.Error("InputTokens should not be 0")
	}
	if transport.sseUsage.Usage.OutputTokens == 0 {
		t.Error("OutputTokens should not be 0")
	}
}
