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

type ollamaProvider struct {
	model   string
	baseURL string
}

func (p *ollamaProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	var apiMessages []map[string]any
	for _, m := range messages {
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var tcs []map[string]any
			for _, tc := range m.ToolCalls {
				var args any
				if err := json.Unmarshal(tc.Arguments, &args); err != nil {
					args = map[string]any{}
				}
				tcs = append(tcs, map[string]any{
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": args,
					},
				})
			}
			msg["tool_calls"] = tcs
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

	url := strings.TrimRight(p.baseURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("ollama API error %d: %s", resp.StatusCode, string(b))
	}

	ch := make(chan StreamEvent, 16)
	go p.readStream(ctx, resp.Body, ch)
	return ch, nil
}

func (p *ollamaProvider) readStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var chunk struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}

		if chunk.Message.Content != "" {
			select {
			case ch <- StreamEvent{Type: "text", Text: chunk.Message.Content}:
			case <-ctx.Done():
				return
			}
		}

		for i, tc := range chunk.Message.ToolCalls {
			args := tc.Function.Arguments
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			select {
			case ch <- StreamEvent{Type: "tool_call", ToolCall: &ToolCall{
				ID:        fmt.Sprintf("ollama_%d", i),
				Name:      tc.Function.Name,
				Arguments: args,
			}}:
			case <-ctx.Done():
				return
			}
		}

		if chunk.Done {
			select {
			case ch <- StreamEvent{Type: "done"}:
			case <-ctx.Done():
			}
			return
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		select {
		case ch <- StreamEvent{Type: "error", Error: err.Error()}:
		case <-ctx.Done():
		}
	}
}
