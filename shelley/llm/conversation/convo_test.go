package conversation

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/ant"
	"sketch.dev/httprr"
)

func TestBasicConvo(t *testing.T) {
	ctx := context.Background()
	rr, err := httprr.Open("testdata/basic_convo.httprr", http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	rr.ScrubReq(func(req *http.Request) error {
		req.Header.Del("x-api-key")
		return nil
	})

	apiKey := cmp.Or(os.Getenv("OUTER_SKETCH_MODEL_API_KEY"), os.Getenv("ANTHROPIC_API_KEY"))
	srv := &ant.Service{
		APIKey: apiKey,
		Model:  ant.Claude4Sonnet, // Use specific model to match cached responses
		HTTPC:  rr.Client(),
	}
	convo := New(ctx, srv, nil)

	const name = "Cornelius"
	res, err := convo.SendUserTextMessage("Hi, my name is " + name)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range res.Content {
		t.Logf("%s", part.Text)
	}
	res, err = convo.SendUserTextMessage("What is my name?")
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, part := range res.Content {
		got += part.Text
	}
	if !strings.Contains(got, name) {
		t.Errorf("model does not know the given name %s: %q", name, got)
	}
}

// TestCancelToolUse tests the CancelToolUse function of the Convo struct
func TestCancelToolUse(t *testing.T) {
	tests := []struct {
		name         string
		setupToolUse bool
		toolUseID    string
		cancelErr    error
		expectError  bool
		expectCancel bool
	}{
		{
			name:         "Cancel existing tool use",
			setupToolUse: true,
			toolUseID:    "tool123",
			cancelErr:    nil,
			expectError:  false,
			expectCancel: true,
		},
		{
			name:         "Cancel existing tool use with error",
			setupToolUse: true,
			toolUseID:    "tool456",
			cancelErr:    context.Canceled,
			expectError:  false,
			expectCancel: true,
		},
		{
			name:         "Cancel non-existent tool use",
			setupToolUse: false,
			toolUseID:    "tool789",
			cancelErr:    nil,
			expectError:  true,
			expectCancel: false,
		},
	}

	srv := &ant.Service{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convo := New(context.Background(), srv, nil)

			var cancelCalled bool
			var cancelledWithErr error

			if tt.setupToolUse {
				// Setup a mock cancel function to track calls
				mockCancel := func(err error) {
					cancelCalled = true
					cancelledWithErr = err
				}

				convo.toolUseCancelMu.Lock()
				convo.toolUseCancel[tt.toolUseID] = mockCancel
				convo.toolUseCancelMu.Unlock()
			}

			err := convo.CancelToolUse(tt.toolUseID, tt.cancelErr)

			// Check if we got the expected error state
			if (err != nil) != tt.expectError {
				t.Errorf("CancelToolUse() error = %v, expectError %v", err, tt.expectError)
			}

			// Check if the cancel function was called as expected
			if cancelCalled != tt.expectCancel {
				t.Errorf("Cancel function called = %v, expectCancel %v", cancelCalled, tt.expectCancel)
			}

			// If we expected the cancel to be called, verify it was called with the right error
			if tt.expectCancel && cancelledWithErr != tt.cancelErr {
				t.Errorf("Cancel function called with error = %v, expected %v", cancelledWithErr, tt.cancelErr)
			}

			// Verify the toolUseID was removed from the map if it was initially added
			if tt.setupToolUse {
				convo.toolUseCancelMu.Lock()
				_, exists := convo.toolUseCancel[tt.toolUseID]
				convo.toolUseCancelMu.Unlock()

				if exists {
					t.Errorf("toolUseID %s still exists in the map after cancellation", tt.toolUseID)
				}
			}
		})
	}
}

// TestInsertMissingToolResults tests the insertMissingToolResults function
// to ensure it doesn't create duplicate tool results when multiple tool uses are missing results.
func TestInsertMissingToolResults(t *testing.T) {
	tests := []struct {
		name            string
		messages        []llm.Message
		currentMsg      llm.Message
		expectedCount   int
		expectedToolIDs []string
	}{
		{
			name: "Single missing tool result",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool1",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{},
			},
			expectedCount:   1,
			expectedToolIDs: []string{"tool1"},
		},
		{
			name: "Multiple missing tool results",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool1",
						},
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool2",
						},
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool3",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{},
			},
			expectedCount:   3,
			expectedToolIDs: []string{"tool1", "tool2", "tool3"},
		},
		{
			name: "No missing tool results when results already present",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeToolUse,
							ID:   "tool1",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role: llm.MessageRoleUser,
				Content: []llm.Content{
					{
						Type:      llm.ContentTypeToolResult,
						ToolUseID: "tool1",
					},
				},
			},
			expectedCount:   1, // Only the existing one
			expectedToolIDs: []string{"tool1"},
		},
		{
			name: "No tool uses in previous message",
			messages: []llm.Message{
				{
					Role: llm.MessageRoleAssistant,
					Content: []llm.Content{
						{
							Type: llm.ContentTypeText,
							Text: "Just some text",
						},
					},
				},
			},
			currentMsg: llm.Message{
				Role:    llm.MessageRoleUser,
				Content: []llm.Content{},
			},
			expectedCount:   0,
			expectedToolIDs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &ant.Service{}
			convo := New(context.Background(), srv, nil)

			// Create request with messages
			req := &llm.Request{
				Messages: append(tt.messages, tt.currentMsg),
			}

			// Call insertMissingToolResults
			msg := tt.currentMsg
			convo.insertMissingToolResults(req, &msg)

			// Count tool results in the message
			toolResultCount := 0
			toolIDs := []string{}
			for _, content := range msg.Content {
				if content.Type == llm.ContentTypeToolResult {
					toolResultCount++
					toolIDs = append(toolIDs, content.ToolUseID)
				}
			}

			// Verify count
			if toolResultCount != tt.expectedCount {
				t.Errorf("Expected %d tool results, got %d", tt.expectedCount, toolResultCount)
			}

			// Verify no duplicates by checking unique tool IDs
			seenIDs := make(map[string]int)
			for _, id := range toolIDs {
				seenIDs[id]++
			}

			// Check for duplicates
			for id, count := range seenIDs {
				if count > 1 {
					t.Errorf("Duplicate tool result for ID %s: found %d times", id, count)
				}
			}

			// Verify all expected tool IDs are present
			for _, expectedID := range tt.expectedToolIDs {
				if !slices.Contains(toolIDs, expectedID) {
					t.Errorf("Expected tool ID %s not found in results", expectedID)
				}
			}
		})
	}
}
