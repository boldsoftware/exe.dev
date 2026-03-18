package aiagent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicProvider(t *testing.T) {
	// Mock Anthropic API server — test stream parsing via readStream.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer ts.Close()

	p := &anthropicProvider{apiKey: "test-key", model: "test-model"}
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	ch := make(chan StreamEvent, 16)
	go p.readStream(context.Background(), resp.Body, ch)

	var got string
	for event := range ch {
		switch event.Type {
		case "text":
			got += event.Text
		case "done":
			// expected
		case "error":
			t.Fatalf("unexpected error: %s", event.Error)
		}
	}

	if got != "Hello world" {
		t.Errorf("got %q, want %q", got, "Hello world")
	}
}

func TestAnthropicProviderToolCall(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"list_servers"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{}"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer ts.Close()

	p := &anthropicProvider{apiKey: "test-key", model: "test-model"}
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	ch := make(chan StreamEvent, 16)
	go p.readStream(context.Background(), resp.Body, ch)

	var toolCalls int
	for event := range ch {
		if event.Type == "tool_call" && event.ToolCall != nil {
			toolCalls++
			if event.ToolCall.Name != "list_servers" {
				t.Errorf("tool name = %q, want %q", event.ToolCall.Name, "list_servers")
			}
			if event.ToolCall.ID != "tool_1" {
				t.Errorf("tool id = %q, want %q", event.ToolCall.ID, "tool_1")
			}
		}
	}
	if toolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", toolCalls)
	}
}

func TestOpenAIProvider(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing or wrong auth: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hello"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":" there"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	p := &openaiProvider{apiKey: "test-key", model: "test-model", baseURL: ts.URL}
	ch, err := p.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var got string
	for event := range ch {
		switch event.Type {
		case "text":
			got += event.Text
		case "done":
			// expected
		case "error":
			t.Fatalf("unexpected error: %s", event.Error)
		}
	}

	if got != "Hello there" {
		t.Errorf("got %q, want %q", got, "Hello there")
	}
}

func TestOpenAIProviderToolCall(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_fleet_status","arguments":""}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`+"\n\n")
		reason := "tool_calls"
		fmt.Fprintf(w, `data: {"choices":[{"delta":{},"finish_reason":"%s"}]}`+"\n\n", reason)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	p := &openaiProvider{apiKey: "test-key", model: "test-model", baseURL: ts.URL}
	ch, err := p.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var toolCalls int
	for event := range ch {
		if event.Type == "tool_call" && event.ToolCall != nil {
			toolCalls++
			if event.ToolCall.Name != "get_fleet_status" {
				t.Errorf("tool name = %q, want %q", event.ToolCall.Name, "get_fleet_status")
			}
		}
	}
	if toolCalls != 1 {
		t.Errorf("tool_calls = %d, want 1", toolCalls)
	}
}

func TestOllamaProvider(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path: %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"message":{"content":"Hi "},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":"there"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":""},"done":true}`)
	}))
	defer ts.Close()

	p := &ollamaProvider{model: "test-model", baseURL: ts.URL}
	ch, err := p.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var got string
	for event := range ch {
		switch event.Type {
		case "text":
			got += event.Text
		case "done":
			// expected
		case "error":
			t.Fatalf("unexpected error: %s", event.Error)
		}
	}

	if got != "Hi there" {
		t.Errorf("got %q, want %q", got, "Hi there")
	}
}

func TestNewProviderValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "anthropic without key",
			cfg:     Config{Provider: "anthropic"},
			wantErr: true,
		},
		{
			name:    "anthropic with key",
			cfg:     Config{Provider: "anthropic", APIKey: "sk-test"},
			wantErr: false,
		},
		{
			name:    "openai without key",
			cfg:     Config{Provider: "openai"},
			wantErr: true,
		},
		{
			name:    "openai with key",
			cfg:     Config{Provider: "openai", APIKey: "sk-test"},
			wantErr: false,
		},
		{
			name:    "openai-compat without base URL",
			cfg:     Config{Provider: "openai-compat"},
			wantErr: true,
		},
		{
			name:    "openai-compat with base URL",
			cfg:     Config{Provider: "openai-compat", BaseURL: "http://localhost:8000/v1"},
			wantErr: false,
		},
		{
			name:    "ollama defaults",
			cfg:     Config{Provider: "ollama"},
			wantErr: false,
		},
		{
			name:    "unknown provider",
			cfg:     Config{Provider: "unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			_, err := NewProvider(&cfg)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
