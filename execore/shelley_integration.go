package execore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"golang.org/x/crypto/ssh"
)

// ShelleyMessage represents a message in the Shelley API response
type ShelleyMessage struct {
	MessageID      string  `json:"message_id"`
	ConversationID string  `json:"conversation_id"`
	SequenceID     int64   `json:"sequence_id"`
	Type           string  `json:"type"`
	DisplayData    *string `json:"display_data,omitempty"`
	LlmData        *string `json:"llm_data,omitempty"`
}

// ShelleyLLMData represents the parsed LLM data structure
type ShelleyLLMData struct {
	Role      int                 `json:"Role"`
	Content   []ShelleyLLMContent `json:"Content"`
	EndOfTurn bool                `json:"EndOfTurn"`
}

// ShelleyLLMContent represents a content item in LLM data
type ShelleyLLMContent struct {
	Type     int    `json:"Type"` // 2=text, 5=tool_use
	Text     string `json:"Text,omitempty"`
	ToolName string `json:"ToolName,omitempty"`
}

// ShelleyStreamResponse represents the SSE stream response from Shelley
type ShelleyStreamResponse struct {
	Messages     []ShelleyMessage `json:"messages"`
	Conversation struct {
		ConversationID string `json:"conversation_id"`
	} `json:"conversation"`
}

// ShelleyChatRequest represents a chat request to Shelley
type ShelleyChatRequest struct {
	Message string `json:"message"`
	Model   string `json:"model,omitempty"`
}

// ShelleyAgentMessage represents the display data for agent messages
type ShelleyAgentMessage struct {
	Text      string `json:"text,omitempty"`
	EndOfTurn bool   `json:"end_of_turn,omitempty"`
}

// runShelleyPrompt sends a prompt to Shelley and streams the response back to the user
func (ss *SSHServer) runShelleyPrompt(ctx context.Context, cc *exemenu.CommandContext, box *exedb.Box, sshKey ssh.Signer, ctrhost, prompt, shelleyUrl, model string) error {
	if box.SSHPort == nil {
		return fmt.Errorf("box does not have SSH port configured")
	}
	if box.SSHUser == nil {
		return fmt.Errorf("box does not have SSH user configured")
	}

	// Create HTTP client that tunnels through SSH to the container
	sshHost := ss.server.resolveSSHHost(ctrhost)
	httpClient := &http.Client{
		Transport: ss.server.createSSHTunnelTransport(sshHost, box, sshKey),
	}

	// Create conversation and get ID
	conversationID, err := ss.createShelleyConversation(ctx, httpClient, prompt, model)
	if err != nil {
		return err
	}

	cc.Write("🐌 Connected to Shelley\r\n")
	cc.Write("🐌 Follow along at %s\r\n\r\n", shelleyUrl)

	// Stream the conversation and display agent messages
	return ss.streamShelleyConversation(ctx, httpClient, conversationID, cc, box.Name)
}

// createShelleyConversation creates a new Shelley conversation with the given prompt
func (ss *SSHServer) createShelleyConversation(ctx context.Context, httpClient *http.Client, prompt, model string) (string, error) {
	// Create the chat request
	chatReq := ShelleyChatRequest{
		Message: prompt,
		Model:   model,
	}

	reqBody, err := json.Marshal(chatReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal chat request: %w", err)
	} // Wait for Shelley to start up and send the chat request with retries
	// The sshDialer handles SSH connection retries, so these retries are
	// specifically for waiting for Shelley (a systemd service) to be ready
	shelleyURL := "http://localhost:9999/api/conversations/new"
	retryDelays := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		3 * time.Second,
		5 * time.Second,
		5 * time.Second,
		10 * time.Second,
	}

	var resp *http.Response
	for i := 0; i <= len(retryDelays); i++ {
		req, err := http.NewRequestWithContext(ctx, "POST", shelleyURL, bytes.NewReader(reqBody))
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err = httpClient.Do(req)
		if err == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated) {
			// Success! (201 Created or 200 OK)
			break
		}

		// Failed - close response if we got one
		if resp != nil {
			resp.Body.Close()
			resp = nil
		}

		// If we've exhausted retries, return the error
		if i >= len(retryDelays) {
			if err != nil {
				return "", fmt.Errorf("failed to connect to Shelley after retries: %w", err)
			}
			if resp != nil {
				return "", fmt.Errorf("shelley returned status %d after retries", resp.StatusCode)
			}
			return "", fmt.Errorf("failed to connect to Shelley: unknown error")
		}

		// Wait before retrying
		select {
		case <-time.After(retryDelays[i]):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	if resp == nil {
		return "", fmt.Errorf("no response from Shelley")
	}
	defer resp.Body.Close()

	// Parse the response to get conversation ID
	var chatResp struct {
		Status         string `json:"status"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("failed to parse chat response: %w", err)
	}

	if chatResp.ConversationID == "" {
		return "", fmt.Errorf("no conversation ID in response")
	}

	return chatResp.ConversationID, nil
}

// streamShelleyConversation streams a Shelley conversation and displays agent messages
func (ss *SSHServer) streamShelleyConversation(ctx context.Context, httpClient *http.Client, conversationID string, cc *exemenu.CommandContext, boxName string) error {
	// Connect to the conversation stream
	streamURL := fmt.Sprintf("http://localhost:9999/api/conversation/%s/stream", conversationID)
	streamReq, err := http.NewRequestWithContext(ctx, "GET", streamURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create stream request: %w", err)
	}

	streamResp, err := httpClient.Do(streamReq)
	if err != nil {
		return fmt.Errorf("failed to connect to stream: %w", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResp.Body)
		return fmt.Errorf("shelley stream returned status %d: %s", streamResp.StatusCode, string(body))
	}

	// Process SSE stream with a timeout
	// Create a channel to signal when we're done
	done := make(chan error, 1)
	messageReceived := make(chan struct{}, 1)

	go func() {
		scanner := bufio.NewScanner(streamResp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 64KB buffer, 1MB max

		var lastSequenceID int64
		for scanner.Scan() {
			line := scanner.Text()

			// SSE format: "data: {json}"
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			var streamData ShelleyStreamResponse
			if err := json.Unmarshal([]byte(data), &streamData); err != nil {
				slog.WarnContext(ctx, "Failed to parse SSE JSON", "error", err)
				continue
			}

			// Process new messages
			for _, msg := range streamData.Messages {
				// Skip messages we've already seen
				if msg.SequenceID <= lastSequenceID {
					continue
				}
				lastSequenceID = msg.SequenceID

				// Signal that we received a message (reset idle timeout)
				select {
				case messageReceived <- struct{}{}:
				default:
				}

				// Only show agent messages
				if msg.Type != "agent" {
					continue
				}

				// First try display_data (for tool results)
				// TODO(philip): Using both llm_data and display_data seems fishy; need to see
				// what's really up here.
				if msg.DisplayData != nil {
					var agentMsg ShelleyAgentMessage
					if err := json.Unmarshal([]byte(*msg.DisplayData), &agentMsg); err != nil {
						slog.WarnContext(ctx, "Failed to parse agent message display data", "error", err)
						continue
					}
					if agentMsg.Text != "" {
						cc.Write("🤖 %s\r\n", strings.TrimSpace(agentMsg.Text))
					}

					// Check if this is end of turn
					if agentMsg.EndOfTurn {
						done <- nil
						return
					}
					continue
				}

				// If no display_data, parse llm_data for text content
				if msg.LlmData != nil {
					var llmData ShelleyLLMData
					if err := json.Unmarshal([]byte(*msg.LlmData), &llmData); err != nil {
						slog.WarnContext(ctx, "Failed to parse llm_data", "error", err)
						continue
					}

					// Extract and display content (text and tool calls)
					for _, content := range llmData.Content {
						if content.Type == 2 && content.Text != "" {
							// Text content
							cc.Write("🤖 %s\r\n", strings.TrimSpace(content.Text))
						} else if content.Type == 5 && content.ToolName != "" {
							// Tool use (Type 5 = ContentTypeToolUse)
							cc.Write("🛠️ Calling '%s'...\r\n", content.ToolName)
						}
					}

					// Check if this message marks end of turn
					if llmData.EndOfTurn {
						cc.Write("🏁 Shelley finished its turn. Continue the conversation at\r\n")
						url := ss.server.shelleyURL(boxName)
						cc.Write("  %s\r\n", url)
						done <- nil
						return
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			done <- fmt.Errorf("error reading stream: %w", err)
		} else {
			done <- fmt.Errorf("stream ended without end_of_turn signal")
		}
	}()

	// Wait for completion with both absolute and idle (5m) timeouts
	absoluteTimeout := time.NewTimer(longOperationTimeout)
	defer absoluteTimeout.Stop()

	idleTimeout := time.NewTimer(5 * time.Minute)
	defer idleTimeout.Stop()

	for {
		select {
		case err := <-done:
			return err
		case <-messageReceived:
			// Reset idle timeout when we receive a message
			if !idleTimeout.Stop() {
				<-idleTimeout.C
			}
			idleTimeout.Reset(5 * time.Minute)
		case <-idleTimeout.C:
			return fmt.Errorf("idle timeout: no messages received for 5 minutes")
		case <-absoluteTimeout.C:
			return fmt.Errorf("absolute timeout: Shelley processing exceeded 30 minutes")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
