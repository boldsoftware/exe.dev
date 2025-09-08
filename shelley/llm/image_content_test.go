package llm

import (
	"encoding/json"
	"testing"
)

func TestImageContent(t *testing.T) {
	// Create a Content structure with an image
	imageContent := Content{
		Type:      ContentTypeText, // In the future, we might add a specific ContentTypeImage
		MediaType: "image/jpeg",
		Data:      "/9j/4AAQSkZJRg...", // Shortened base64 encoded image
	}

	// Verify the structure is correct
	if imageContent.MediaType != "image/jpeg" {
		t.Errorf("Expected MediaType to be 'image/jpeg', got '%s'", imageContent.MediaType)
	}

	if imageContent.Data != "/9j/4AAQSkZJRg..." {
		t.Errorf("Expected Data to contain base64 image data")
	}

	// Create a tool result that contains text and image content
	toolResult := Content{
		Type:      ContentTypeToolResult,
		ToolUseID: "toolu_01A09q90qw90lq917835lq9",
		ToolResult: []Content{
			{
				Type: ContentTypeText,
				Text: "15 degrees",
			},
			imageContent,
		},
	}

	// Check that the tool result contains two content items
	if len(toolResult.ToolResult) != 2 {
		t.Errorf("Expected tool result to contain 2 content items, got %d", len(toolResult.ToolResult))
	}

	// Verify JSON marshaling works as expected
	bytes, err := json.Marshal(toolResult)
	if err != nil {
		t.Errorf("Failed to marshal content to JSON: %v", err)
	}

	// Unmarshal and verify structure is preserved
	var unmarshaled Content
	if err := json.Unmarshal(bytes, &unmarshaled); err != nil {
		t.Errorf("Failed to unmarshal JSON: %v", err)
	}

	if len(unmarshaled.ToolResult) != 2 {
		t.Errorf("Expected unmarshaled tool result to contain 2 content items, got %d", len(unmarshaled.ToolResult))
	}

	if unmarshaled.ToolResult[1].MediaType != "image/jpeg" {
		t.Errorf("Expected unmarshaled image MediaType to be 'image/jpeg', got '%s'", unmarshaled.ToolResult[1].MediaType)
	}
}
