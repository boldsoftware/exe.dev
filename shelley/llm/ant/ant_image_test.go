package ant

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/llm"
)

func TestAnthropicImageToolResult(t *testing.T) {
	// Create a tool result with both text and image content
	textContent := llm.Content{
		Type: llm.ContentTypeText,
		Text: "15 degrees",
	}

	imageContent := llm.Content{
		Type:      llm.ContentTypeText, // Will be mapped to "image" in Anthropic format
		MediaType: "image/jpeg",
		Data:      "/9j/4AAQSkZJRg...", // Shortened base64 encoded image
	}

	toolResult := llm.Content{
		Type:       llm.ContentTypeToolResult,
		ToolUseID:  "toolu_01A09q90qw90lq917835lq9",
		ToolResult: []llm.Content{textContent, imageContent},
	}

	// Convert to Anthropic format
	anthropicContent := fromLLMContent(toolResult)

	// Check the type
	if anthropicContent.Type != "tool_result" {
		t.Errorf("Expected type to be 'tool_result', got '%s'", anthropicContent.Type)
	}

	// Check the tool_use_id
	if anthropicContent.ToolUseID != "toolu_01A09q90qw90lq917835lq9" {
		t.Errorf("Expected tool_use_id to be 'toolu_01A09q90qw90lq917835lq9', got '%s'", anthropicContent.ToolUseID)
	}

	// Check that we have two content items in the tool result
	if len(anthropicContent.ToolResult) != 2 {
		t.Errorf("Expected 2 content items, got %d", len(anthropicContent.ToolResult))
	}

	// Check that the first item is text
	if anthropicContent.ToolResult[0].Type != "text" {
		t.Errorf("Expected first content type to be 'text', got '%s'", anthropicContent.ToolResult[0].Type)
	}

	if *anthropicContent.ToolResult[0].Text != "15 degrees" {
		t.Errorf("Expected first content text to be '15 degrees', got '%s'", *anthropicContent.ToolResult[0].Text)
	}

	// Check that the second item is an image
	if anthropicContent.ToolResult[1].Type != "image" {
		t.Errorf("Expected second content type to be 'image', got '%s'", anthropicContent.ToolResult[1].Type)
	}

	// Check that the image source contains the expected format
	var source map[string]any
	if err := json.Unmarshal(anthropicContent.ToolResult[1].Source, &source); err != nil {
		t.Errorf("Failed to unmarshal image source: %v", err)
	}

	if source["type"] != "base64" {
		t.Errorf("Expected source type to be 'base64', got '%s'", source["type"])
	}

	if source["media_type"] != "image/jpeg" {
		t.Errorf("Expected media_type to be 'image/jpeg', got '%s'", source["media_type"])
	}

	if source["data"] != "/9j/4AAQSkZJRg..." {
		t.Errorf("Expected data to be '/9j/4AAQSkZJRg...', got '%s'", source["data"])
	}
}
