package server

import (
	"encoding/json"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

// TestContextWindowSizeCalculation tests that the context window size is correctly
// calculated including cached tokens.
func TestContextWindowSizeCalculation(t *testing.T) {
	// Test the calculateContextWindowSize function directly
	t.Run("includes_all_token_types", func(t *testing.T) {
		// Create usage data with all token types
		usage := llm.Usage{
			InputTokens:              100,
			CacheCreationInputTokens: 50,
			CacheReadInputTokens:     200,
			OutputTokens:             30,
		}
		usageJSON, _ := json.Marshal(usage)
		usageStr := string(usageJSON)

		messages := []APIMessage{
			{
				Type:      string(db.MessageTypeAgent),
				UsageData: &usageStr,
			},
		}

		// Expected: 100 + 50 + 200 + 30 = 380
		got := calculateContextWindowSize(messages)
		want := uint64(380)

		if got != want {
			t.Errorf("calculateContextWindowSize() = %d, want %d", got, want)
		}
	})

	t.Run("only_input_tokens", func(t *testing.T) {
		// Test with just input tokens (no caching)
		usage := llm.Usage{
			InputTokens:  150,
			OutputTokens: 50,
		}
		usageJSON, _ := json.Marshal(usage)
		usageStr := string(usageJSON)

		messages := []APIMessage{
			{
				Type:      string(db.MessageTypeAgent),
				UsageData: &usageStr,
			},
		}

		// Expected: 150 + 50 = 200
		got := calculateContextWindowSize(messages)
		want := uint64(200)

		if got != want {
			t.Errorf("calculateContextWindowSize() = %d, want %d", got, want)
		}
	})

	t.Run("uses_last_message_with_usage", func(t *testing.T) {
		// Test that we use the last message, not the first
		usage1 := llm.Usage{
			InputTokens:  100,
			OutputTokens: 50,
		}
		usage1JSON, _ := json.Marshal(usage1)
		usage1Str := string(usage1JSON)

		usage2 := llm.Usage{
			InputTokens:          200,
			CacheReadInputTokens: 100,
			OutputTokens:         75,
		}
		usage2JSON, _ := json.Marshal(usage2)
		usage2Str := string(usage2JSON)

		messages := []APIMessage{
			{
				Type:      string(db.MessageTypeAgent),
				UsageData: &usage1Str,
			},
			{
				Type:      string(db.MessageTypeUser),
				UsageData: nil, // User messages typically don't have usage
			},
			{
				Type:      string(db.MessageTypeAgent),
				UsageData: &usage2Str,
			},
		}

		// Expected: 200 + 100 + 75 = 375 (from the last message)
		got := calculateContextWindowSize(messages)
		want := uint64(375)

		if got != want {
			t.Errorf("calculateContextWindowSize() = %d, want %d", got, want)
		}
	})

	t.Run("empty_messages", func(t *testing.T) {
		messages := []APIMessage{}
		got := calculateContextWindowSize(messages)
		want := uint64(0)

		if got != want {
			t.Errorf("calculateContextWindowSize() = %d, want %d", got, want)
		}
	})
}

// TestContextWindowGrowsWithConversation tests that the context window size grows
// as the conversation progresses, using the test harness and predictable service.
func TestContextWindowGrowsWithConversation(t *testing.T) {
	h := NewTestHarness(t)
	defer h.Close()

	// Start a new conversation
	h.NewConversation("echo: first message", "/tmp")

	// Wait for the response
	resp1 := h.WaitResponse()
	t.Logf("First response: %q", resp1)

	// Get the context window size from the first message
	firstSize := h.GetContextWindowSize()
	t.Logf("First context window size: %d", firstSize)
	if firstSize == 0 {
		t.Fatal("expected non-zero context window size after first message")
	}

	// Send another message
	h.Chat("echo: second message that is longer")
	resp2 := h.WaitResponse()
	t.Logf("Second response: %q", resp2)

	// Context window should have grown
	secondSize := h.GetContextWindowSize()
	t.Logf("Second context window size: %d", secondSize)
	if secondSize <= firstSize {
		t.Errorf("context window should grow: first=%d, second=%d", firstSize, secondSize)
	}

	// Send a third message
	h.Chat("echo: third message with even more text to demonstrate growth")
	resp3 := h.WaitResponse()
	t.Logf("Third response: %q", resp3)

	thirdSize := h.GetContextWindowSize()
	t.Logf("Third context window size: %d", thirdSize)
	if thirdSize <= secondSize {
		t.Errorf("context window should grow: second=%d, third=%d", secondSize, thirdSize)
	}

	t.Logf("Context window sizes: first=%d, second=%d, third=%d", firstSize, secondSize, thirdSize)
}
