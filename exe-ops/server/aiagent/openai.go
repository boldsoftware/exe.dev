package aiagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type openaiProvider struct {
	apiKey  string
	model   string
	baseURL string
}

func (p *openaiProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	var apiMessages []map[string]any
	for _, m := range messages {
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var tcs []map[string]any
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": string(tc.Arguments),
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		if m.Role == "tool" {
			msg["tool_call_id"] = m.ToolCallID
		}
		apiMessages = append(apiMessages, msg)
	}

	body := map[string]any{
		"model":    p.model,
		"messages": apiMessages,
		"stream":   true,
	}
	if len(tools) > 0 {
		var toolDefs []map[string]any
		for _, t := range tools {
			toolDefs = append(toolDefs, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = toolDefs
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(p.baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("openai API error %d: %s", resp.StatusCode, string(b))
	}

	ch := make(chan StreamEvent, 16)
	go p.readStream(ctx, resp.Body, ch)
	return ch, nil
}

func (p *openaiProvider) readStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	// Track tool calls being accumulated
	toolCalls := make(map[int]*ToolCall) // index -> tool call

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			select {
			case ch <- StreamEvent{Type: "done"}:
			case <-ctx.Done():
			}
			return
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			select {
			case ch <- StreamEvent{Type: "text", Text: delta.Content}:
			case <-ctx.Done():
				return
			}
		}

		for _, tc := range delta.ToolCalls {
			existing, ok := toolCalls[tc.Index]
			if !ok {
				existing = &ToolCall{}
				toolCalls[tc.Index] = existing
			}
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Function.Name != "" {
				existing.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				existing.Arguments = append(existing.Arguments, []byte(tc.Function.Arguments)...)
			}
		}

		// Emit completed tool calls on finish_reason="tool_calls"
		if chunk.Choices[0].FinishReason != nil && *chunk.Choices[0].FinishReason == "tool_calls" {
			for _, tc := range toolCalls {
				if len(tc.Arguments) == 0 {
					tc.Arguments = json.RawMessage("{}")
				}
				select {
				case ch <- StreamEvent{Type: "tool_call", ToolCall: tc}:
				case <-ctx.Done():
					return
				}
			}
			toolCalls = make(map[int]*ToolCall)
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		select {
		case ch <- StreamEvent{Type: "error", Error: err.Error()}:
		case <-ctx.Done():
		}
	}
}
