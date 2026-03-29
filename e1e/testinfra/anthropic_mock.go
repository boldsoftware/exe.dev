package testinfra

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// MockAnthropicServer is a fake Anthropic API for e1e tests.
// It returns a canned tool_use response followed by a text response,
// simulating a simple agentic loop.
type MockAnthropicServer struct {
	Server *httptest.Server

	mu       sync.Mutex
	requests []json.RawMessage
}

// NewMockAnthropicServer creates and starts a mock Anthropic API server.
// The server responds to POST /v1/messages with deterministic responses:
//   - First call: tool_use calling "exe_command" with {"command": "ls"}
//   - Subsequent calls: text response summarizing what it found
func NewMockAnthropicServer() *MockAnthropicServer {
	m := &MockAnthropicServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", m.handleMessages)
	m.Server = httptest.NewServer(mux)
	return m
}

func (m *MockAnthropicServer) handleMessages(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.requests = append(m.requests, json.RawMessage(body))
	callNum := len(m.requests)
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	if callNum == 1 {
		// First call: ask the model to call exe_command with "ls"
		json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_mock_1",
			"type":        "message",
			"role":        "assistant",
			"model":       "claude-opus-4-6",
			"stop_reason": "tool_use",
			"usage":       map[string]int{"input_tokens": 100, "output_tokens": 50},
			"content": []map[string]any{
				{
					"type": "text",
					"text": "Let me check your VMs.",
				},
				{
					"type": "tool_use",
					"id":   "toolu_mock_1",
					"name": "exe_command",
					"input": map[string]string{
						"command": "ls",
					},
				},
			},
		})
		return
	}

	// Subsequent calls: return a text summary and end the turn
	json.NewEncoder(w).Encode(map[string]any{
		"id":          "msg_mock_2",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-opus-4-6",
		"stop_reason": "end_turn",
		"usage":       map[string]int{"input_tokens": 200, "output_tokens": 30},
		"content": []map[string]any{
			{
				"type": "text",
				"text": "MOCK_PROMPT_RESULT: I found your VMs.",
			},
		},
	})
}

// RequestCount returns how many API calls were made.
func (m *MockAnthropicServer) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

// Close shuts down the mock server.
func (m *MockAnthropicServer) Close() {
	m.Server.Close()
}

// URL returns the base URL of the mock server.
func (m *MockAnthropicServer) URL() string {
	return m.Server.URL
}
