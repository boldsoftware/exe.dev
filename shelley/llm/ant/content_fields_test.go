package ant

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

// TestTextContentNoExtraFields verifies that text content doesn't include fields from other content types
func TestTextContentNoExtraFields(t *testing.T) {
	tests := []struct {
		name          string
		content       llm.Content
		allowedFields map[string]bool
	}{
		{
			name: "text content",
			content: llm.Content{
				Type: llm.ContentTypeText,
				Text: "Hello world",
			},
			allowedFields: map[string]bool{
				"type": true,
				"text": true,
			},
		},
		{
			name: "tool_use content",
			content: llm.Content{
				Type:      llm.ContentTypeToolUse,
				ID:        "toolu_123",
				ToolName:  "bash",
				ToolInput: json.RawMessage(`{"command":"ls"}`),
			},
			allowedFields: map[string]bool{
				"type":  true,
				"id":    true,
				"name":  true,
				"input": true,
			},
		},
		{
			name: "tool_result content",
			content: llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: "toolu_123",
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: "result"},
				},
			},
			allowedFields: map[string]bool{
				"type":        true,
				"tool_use_id": true,
				"content":     true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			antContent := fromLLMContent(tt.content)
			jsonBytes, err := json.Marshal(antContent)
			if err != nil {
				t.Fatalf("failed to marshal content: %v", err)
			}

			var result map[string]interface{}
			if err := json.Unmarshal(jsonBytes, &result); err != nil {
				t.Fatalf("failed to unmarshal JSON: %v", err)
			}

			// Check that only allowed fields are present
			for field := range result {
				if !tt.allowedFields[field] {
					t.Errorf("unexpected field %q in %s content: %s", field, tt.name, string(jsonBytes))
				}
			}

			// Check that all required fields are present
			for field := range tt.allowedFields {
				if _, ok := result[field]; !ok && field != "cache_control" {
					// cache_control is optional, so we don't require it
					if field != "content" || tt.content.Type == llm.ContentTypeToolResult {
						// Only check for content field if it's a tool_result
						if field == "content" && tt.content.Type == llm.ContentTypeToolResult {
							t.Errorf("missing required field %q in %s content: %s", field, tt.name, string(jsonBytes))
						}
					}
				}
			}
		})
	}
}
