package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"shelley.exe.dev/llm"
)

// PredictableService is an LLM service that returns predictable responses for testing
type PredictableService struct {
	// TokenContextWindow size
	tokenContextWindow int
}

// NewPredictableService creates a new predictable LLM service
func NewPredictableService() *PredictableService {
	return &PredictableService{
		tokenContextWindow: 200000,
	}
}

// TokenContextWindow returns the maximum token context window size
func (s *PredictableService) TokenContextWindow() int {
	return s.tokenContextWindow
}

// Do processes a request and returns a predictable response based on the input text
func (s *PredictableService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	// Extract the text content from the last user message
	var inputText string
	if len(req.Messages) > 0 {
		lastMessage := req.Messages[len(req.Messages)-1]
		if lastMessage.Role == llm.MessageRoleUser {
			for _, content := range lastMessage.Content {
				if content.Type == llm.ContentTypeText {
					inputText = strings.TrimSpace(content.Text)
					break
				}
			}
		}
	}

	// Handle input using case statements
	switch inputText {
	case "hello":
		return s.makeResponse("Well, hi there!"), nil

	case "Hello":
		return s.makeResponse("Hello! I'm Shelley, your AI assistant. How can I help you today?"), nil

	case "Create an example":
		return s.makeThinkToolResponse("I'll create a simple example for you."), nil

	case "echo: foo":
		return s.makeResponse("foo"), nil

	default:
		// Handle pattern-based inputs
		if strings.HasPrefix(inputText, "echo: ") {
			text := strings.TrimPrefix(inputText, "echo: ")
			return s.makeResponse(text), nil
		}

		if strings.HasPrefix(inputText, "bash: ") {
			cmd := strings.TrimPrefix(inputText, "bash: ")
			return s.makeBashToolResponse(cmd), nil
		}

		if strings.HasPrefix(inputText, "think: ") {
			thoughts := strings.TrimPrefix(inputText, "think: ")
			return s.makeThinkToolResponse(thoughts), nil
		}

		if strings.HasPrefix(inputText, "patch: ") {
			filePath := strings.TrimPrefix(inputText, "patch: ")
			return s.makePatchToolResponse(filePath), nil
		}

		if strings.HasPrefix(inputText, "error: ") {
			errorMsg := strings.TrimPrefix(inputText, "error: ")
			return s.makeResponse(fmt.Sprintf("Error: %s", errorMsg)), nil
		}

		// Default response for undefined inputs
		return s.makeResponse("edit predictable.go to add a response for that one..."), nil
	}
}

// makeResponse creates a simple text response
func (s *PredictableService) makeResponse(text string) *llm.Response {
	return &llm.Response{
		ID:    fmt.Sprintf("pred-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: text},
		},
		StopReason: llm.StopReasonStopSequence,
		Usage: llm.Usage{
			InputTokens:  uint64(len(strings.Fields(text))),
			OutputTokens: uint64(len(strings.Fields(text))),
			CostUSD:      0.001,
		},
	}
}

// makeBashToolResponse creates a response that calls the bash tool
func (s *PredictableService) makeBashToolResponse(command string) *llm.Response {
	// Properly marshal the command to avoid JSON escaping issues
	toolInputData := map[string]string{"command": command}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	return &llm.Response{
		ID:    fmt.Sprintf("pred-bash-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: fmt.Sprintf("I'll run the command: %s", command)},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "bash",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  uint64(len(strings.Fields(command)) + 5),
			OutputTokens: uint64(len(strings.Fields(command)) + 10),
			CostUSD:      0.002,
		},
	}
}

// makeThinkToolResponse creates a response that calls the think tool
func (s *PredictableService) makeThinkToolResponse(thoughts string) *llm.Response {
	// Properly marshal the thoughts to avoid JSON escaping issues
	toolInputData := map[string]string{"thoughts": thoughts}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	return &llm.Response{
		ID:    fmt.Sprintf("pred-think-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Let me think about this."},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "think",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  uint64(len(strings.Fields(thoughts)) + 5),
			OutputTokens: uint64(len(strings.Fields(thoughts)) + 5),
			CostUSD:      0.002,
		},
	}
}

// makePatchToolResponse creates a response that calls the patch tool
func (s *PredictableService) makePatchToolResponse(filePath string) *llm.Response {
	// Properly marshal the patch data to avoid JSON escaping issues
	toolInputData := map[string]interface{}{
		"path": filePath,
		"patches": []map[string]string{
			{
				"operation": "replace",
				"oldText":   "example",
				"newText":   "updated example",
			},
		},
	}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	return &llm.Response{
		ID:    fmt.Sprintf("pred-patch-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: fmt.Sprintf("I'll patch the file: %s", filePath)},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "patch",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  uint64(len(strings.Fields(filePath)) + 10),
			OutputTokens: uint64(len(strings.Fields(filePath)) + 15),
			CostUSD:      0.003,
		},
	}
}
