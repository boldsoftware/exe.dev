package testinfra

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// MockAnthropicServer is a fake Anthropic API for e1e tests.
// It returns deterministic responses based on the user's initial prompt,
// simulating various agentic loop scenarios.
type MockAnthropicServer struct {
	Server *httptest.Server

	mu       sync.Mutex
	requests []json.RawMessage
}

// NewMockAnthropicServer creates and starts a mock Anthropic API server.
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
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	// Parse the request to detect the scenario from the initial user prompt
	// and determine the conversation turn from message count.
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)

	// Detect scenario from the first user message.
	initialPrompt := ""
	if len(req.Messages) > 0 && len(req.Messages[0].Content) > 0 {
		initialPrompt = req.Messages[0].Content[0].Text
	}

	// Determine conversation turn: 1 message = first turn, >1 = subsequent.
	turn := len(req.Messages)

	switch {
	case strings.Contains(initialPrompt, "suggest-test"):
		m.handleSuggestScenario(w, turn)
	default:
		m.handleDefaultScenario(w, turn)
	}
}

// handleDefaultScenario: first turn returns exe_command(ls), then text.
func (m *MockAnthropicServer) handleDefaultScenario(w http.ResponseWriter, turn int) {
	if turn == 1 {
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

// handleSuggestScenario: first turn returns suggest_command(help), then text.
func (m *MockAnthropicServer) handleSuggestScenario(w http.ResponseWriter, turn int) {
	if turn == 1 {
		json.NewEncoder(w).Encode(map[string]any{
			"id":          "msg_mock_s1",
			"type":        "message",
			"role":        "assistant",
			"model":       "claude-opus-4-6",
			"stop_reason": "tool_use",
			"usage":       map[string]int{"input_tokens": 100, "output_tokens": 50},
			"content": []map[string]any{
				{
					"type": "tool_use",
					"id":   "toolu_mock_s1",
					"name": "suggest_command",
					"input": map[string]string{
						"command":     "help",
						"explanation": "Shows help information.",
					},
				},
			},
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"id":          "msg_mock_s2",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-opus-4-6",
		"stop_reason": "end_turn",
		"usage":       map[string]int{"input_tokens": 200, "output_tokens": 30},
		"content": []map[string]any{
			{
				"type": "text",
				"text": "MOCK_SUGGEST_DONE: The command ran successfully.",
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
