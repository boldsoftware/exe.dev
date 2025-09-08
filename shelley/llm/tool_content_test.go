package llm

import (
	"testing"
)

func TestToolResultArray(t *testing.T) {
	// Test a tool result with multiple content items
	textContent := Content{
		Type: ContentTypeText,
		Text: "15 degrees",
	}

	imageContent := Content{
		Type:      ContentTypeText, // In the future, this could be ContentTypeImage
		Text:      "",
		MediaType: "image/jpeg",
		Data:      "/9j/4AAQSkZJRg...", // Base64 encoded image sample
	}

	toolResult := Content{
		ToolResult: []Content{textContent, imageContent},
	}

	// Check the structure
	if len(toolResult.ToolResult) != 2 {
		t.Errorf("Expected 2 content items in ToolResult, got %d", len(toolResult.ToolResult))
	}

	if toolResult.ToolResult[0].Text != "15 degrees" {
		t.Errorf("Expected first item text to be '15 degrees', got '%s'", toolResult.ToolResult[0].Text)
	}

	if toolResult.ToolResult[1].MediaType != "image/jpeg" {
		t.Errorf("Expected second item media type to be 'image/jpeg', got '%s'", toolResult.ToolResult[1].MediaType)
	}
}
