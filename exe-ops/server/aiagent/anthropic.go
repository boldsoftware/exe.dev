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

type anthropicProvider struct {
	apiKey  string
	model   string
	baseURL string
}

func (p *anthropicProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamEvent, error) {
	// Extract system message.
	var system string
	var apiMessages []map[string]any
	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		msg := map[string]any{"role": m.Role}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// Build content blocks for tool use
			var content []map[string]any
			if m.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				var args any
				if err := json.Unmarshal(tc.Arguments, &args); err != nil {
					args = map[string]any{}
				}
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Name,
					"input": args,
				})
			}
			msg["content"] = content
		} else if m.Role == "tool" {
			// Anthropic uses "user" role with tool_result content block
			msg["role"] = "user"
			msg["content"] = []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			}}
		} else {
			msg["content"] = m.Content
		}
		apiMessages = append(apiMessages, msg)
	}

	body := map[string]any{
		"model":      p.model,
		"messages":   apiMessages,
		"max_tokens": 4096,
		"stream":     true,
	}
	if system != "" {
		body["system"] = system
	}
	if len(tools) > 0 {
		var toolDefs []map[string]any
		for _, t := range tools {
			toolDefs = append(toolDefs, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.Parameters,
			})
		}
		body["tools"] = toolDefs
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(p.baseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(b))
	}

	ch := make(chan StreamEvent, 16)
	go p.readStream(ctx, resp.Body, ch)
	return ch, nil
}

func (p *anthropicProvider) readStream(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent) {
	defer close(ch)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)

	// Track tool calls being built up across events
	var currentToolCall *ToolCall
	var toolInputBuf strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				currentToolCall = &ToolCall{
					ID:   event.ContentBlock.ID,
					Name: event.ContentBlock.Name,
				}
				toolInputBuf.Reset()
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				select {
				case ch <- StreamEvent{Type: "text", Text: event.Delta.Text}:
				case <-ctx.Done():
					return
				}
			} else if event.Delta.Type == "input_json_delta" && currentToolCall != nil {
				toolInputBuf.WriteString(event.Delta.PartialJSON)
			}
		case "content_block_stop":
			if currentToolCall != nil {
				args := toolInputBuf.String()
				if args == "" {
					args = "{}"
				}
				currentToolCall.Arguments = json.RawMessage(args)
				select {
				case ch <- StreamEvent{Type: "tool_call", ToolCall: currentToolCall}:
				case <-ctx.Done():
					return
				}
				currentToolCall = nil
				toolInputBuf.Reset()
			}
		case "message_stop":
			select {
			case ch <- StreamEvent{Type: "done"}:
			case <-ctx.Done():
			}
			return
		case "error":
			select {
			case ch <- StreamEvent{Type: "error", Error: data}:
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
