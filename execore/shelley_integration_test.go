package execore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestShelleyStreamParsing tests the SSE stream parsing logic directly
// by building and running a real Shelley instance
func TestShelleyStreamParsing(t *testing.T) {
	t.Parallel()
	// TODO(philip): Working on this.
	t.Skip("Skipping integration test that builds and runs shelley")

	// Build Shelley
	shelleyBinary := buildShelley(t)
	defer os.Remove(shelleyBinary)

	// Create a temporary database
	dbPath := filepath.Join(t.TempDir(), "shelley.db")

	// Start Shelley server with predictable model
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shelleyBinary,
		"-db", dbPath,
		"-predictable-only",
		"serve",
		"-port", "0", // Use random port
	)

	// Capture stdout to get the port
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("Failed to get stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start Shelley: %v", err)
	}
	defer cmd.Process.Kill()

	// Read stderr in background to avoid blocking
	go io.Copy(io.Discard, stderr)

	// Wait for server to be ready and get the actual port
	// Shelley logs to stdout (slog default)
	scanner := bufio.NewScanner(stdout)
	var port string
	for scanner.Scan() {
		line := scanner.Text()
		// Look for: level=INFO msg="Server starting" port=12345
		if strings.Contains(line, "Server starting") && strings.Contains(line, "port=") {
			// Parse port from structured log
			for part := range strings.FieldsSeq(line) {
				if after, ok := strings.CutPrefix(part, "port="); ok {
					port = after
					break
				}
			}
			if port != "" {
				break
			}
		}
	}

	if port == "" {
		t.Fatal("Failed to get Shelley port from stdout")
	}

	baseURL := fmt.Sprintf("http://localhost:%s", port)

	// Read remaining output in background
	go io.Copy(io.Discard, stderr)
	go io.Copy(io.Discard, stdout)

	// Test 1: Create a conversation and verify we can parse the stream
	t.Run("ParsePredictableResponse", func(t *testing.T) {
		// Use the helper to create a conversation (reuses production code)
		conversationID := createTestConversation(t, baseURL, "hello", "predictable")

		// Stream the conversation
		streamURL := fmt.Sprintf("%s/api/conversation/%s/stream", baseURL, conversationID)
		streamResp, err := http.Get(streamURL)
		if err != nil {
			t.Fatalf("Failed to connect to stream: %v", err)
		}
		defer streamResp.Body.Close()

		if streamResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(streamResp.Body)
			t.Fatalf("Stream returned status %d: %s", streamResp.StatusCode, string(body))
		}

		// Parse the stream using the same logic as the production code
		messages := parseSSEStream(t, streamResp.Body)

		// Verify we got the expected message
		if len(messages) == 0 {
			t.Fatal("Expected at least one message")
		}

		if !slices.Contains(messages, "Well, hi there!") {
			t.Errorf("Expected to find 'Well, hi there!' in messages, got: %v", messages)
		}
	})

	// Test 2: Test with a different predictable response
	t.Run("ParseEchoResponse", func(t *testing.T) {
		conversationID := createTestConversation(t, baseURL, "echo: test message", "predictable")

		streamURL := fmt.Sprintf("%s/api/conversation/%s/stream", baseURL, conversationID)
		streamResp, err := http.Get(streamURL)
		if err != nil {
			t.Fatalf("Failed to connect to stream: %v", err)
		}
		defer streamResp.Body.Close()

		messages := parseSSEStream(t, streamResp.Body)

		if !slices.Contains(messages, "test message") {
			t.Errorf("Expected to find 'test message' in messages, got: %v", messages)
		}
	})
}

// createTestConversation creates a conversation using the production code path
func createTestConversation(t *testing.T, baseURL, message, model string) string {
	t.Helper()

	// Reuse the production struct and logic
	chatReq := ShelleyChatRequest{
		Message: message,
		Model:   model,
	}
	reqBody, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	resp, err := http.Post(baseURL+"/api/conversations/new", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("Failed to create conversation: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Failed to create conversation: status=%d body=%s", resp.StatusCode, string(body))
	}

	var chatResp struct {
		Status         string `json:"status"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		t.Fatalf("Failed to parse chat response: %v", err)
	}

	if chatResp.ConversationID == "" {
		t.Fatal("No conversation ID in response")
	}

	return chatResp.ConversationID
}

// buildShelley builds the Shelley binary and returns the path to it
func buildShelley(t *testing.T) string {
	t.Helper()

	binaryPath := filepath.Join(t.TempDir(), "shelley")

	// Build from the shelley directory (it's a separate module)
	// Use a build tag to skip the UI embedding
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/shelley")
	cmd.Dir = "./shelley"
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build Shelley: %v\nOutput: %s", err, output)
	}

	return binaryPath
}

// parseSSEStream parses an SSE stream and returns all text messages from agent messages
// This mirrors the logic in streamShelleyConversation
func parseSSEStream(t *testing.T, body io.Reader) []string {
	t.Helper()

	var messages []string
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	timeout := time.After(5 * time.Second)
	done := make(chan struct{})
	var lastSequenceID int64

	go func() {
		defer close(done)

		for scanner.Scan() {
			line := scanner.Text()

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			var streamData ShelleyStreamResponse
			if err := json.Unmarshal([]byte(data), &streamData); err != nil {
				t.Logf("Failed to parse JSON: %v", err)
				continue
			}

			for _, msg := range streamData.Messages {
				if msg.SequenceID <= lastSequenceID {
					continue
				}
				lastSequenceID = msg.SequenceID

				if msg.Type != "agent" {
					continue
				}

				// Try display_data first
				if msg.DisplayData != nil {
					var agentMsg ShelleyAgentMessage
					if err := json.Unmarshal([]byte(*msg.DisplayData), &agentMsg); err == nil {
						if agentMsg.Text != "" {
							messages = append(messages, agentMsg.Text)
						}
						if agentMsg.EndOfTurn {
							return
						}
					}
					continue
				}

				// Parse llm_data for text content
				if msg.LlmData != nil {
					var llmData ShelleyLLMData
					if err := json.Unmarshal([]byte(*msg.LlmData), &llmData); err != nil {
						t.Logf("Failed to parse llm_data: %v", err)
						continue
					}

					for _, content := range llmData.Content {
						if content.Type == 2 && content.Text != "" {
							messages = append(messages, content.Text)
						}
					}
					// Check explicit EndOfTurn flag
					if llmData.EndOfTurn {
						return
					}
				}
			}
		}
	}()

	select {
	case <-done:
		return messages
	case <-timeout:
		t.Fatal("Timeout waiting for stream to complete")
		return messages
	}
}

// TestSSEStreamParsingUnit tests the stream parsing logic with mock data
func TestSSEStreamParsingUnit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		sseData      string
		wantMessages []string
	}{
		{
			name: "text in llm_data",
			sseData: `data: {"messages":[{"message_id":"1","conversation_id":"test","sequence_id":1,"type":"agent","llm_data":"{\"Role\":1,\"Content\":[{\"Type\":2,\"Text\":\"Hello world\"}],\"EndOfTurn\":true}"}],"conversation":{"conversation_id":"test"}}

`,
			wantMessages: []string{"Hello world"},
		},
		{
			name: "text in display_data",
			sseData: `data: {"messages":[{"message_id":"1","conversation_id":"test","sequence_id":1,"type":"agent","display_data":"{\"text\":\"From display\",\"end_of_turn\":true}"}],"conversation":{"conversation_id":"test"}}

`,
			wantMessages: []string{"From display"},
		},
		{
			name: "non-agent message skipped",
			sseData: `data: {"messages":[{"message_id":"1","conversation_id":"test","sequence_id":1,"type":"user","llm_data":"{}"}],"conversation":{"conversation_id":"test"}}

data: {"messages":[{"message_id":"2","conversation_id":"test","sequence_id":2,"type":"agent","llm_data":"{\"Role\":1,\"Content\":[{\"Type\":2,\"Text\":\"Agent response\"}],\"EndOfTurn\":true}"}],"conversation":{"conversation_id":"test"}}

`,
			wantMessages: []string{"Agent response"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := parseSSEStream(t, strings.NewReader(tt.sseData))

			if len(messages) != len(tt.wantMessages) {
				t.Errorf("Got %d messages, want %d: %v", len(messages), len(tt.wantMessages), messages)
				return
			}

			for i, want := range tt.wantMessages {
				if messages[i] != want {
					t.Errorf("Message %d: got %q, want %q", i, messages[i], want)
				}
			}
		})
	}
}
