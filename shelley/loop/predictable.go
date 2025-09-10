package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"shelley.exe.dev/llm"
)

// PredictableService is an LLM service that returns predictable responses for testing
type PredictableService struct {
	// Responses is a list of canned responses to return in order
	Responses []PredictableResponse
	// Current index in the Responses slice
	currentIndex int
	// TokenContextWindow size
	tokenContextWindow int
}

type PredictableResponse struct {
	Content    string
	ToolCalls  []PredictableToolCall
	StopReason llm.StopReason
	Usage      llm.Usage
}

type PredictableToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// NewPredictableService creates a new predictable LLM service
func NewPredictableService() *PredictableService {
	return &PredictableService{
		Responses: []PredictableResponse{
			{
				Content:    "Hello! I'm a predictable AI assistant. How can I help you today?",
				StopReason: llm.StopReasonStopSequence,
				Usage: llm.Usage{
					InputTokens:  10,
					OutputTokens: 12,
					CostUSD:      0.001,
				},
			},
		},
		tokenContextWindow: 200000,
	}
}

// NewPredictableServiceWithTestResponses creates a service with useful test responses
func NewPredictableServiceWithTestResponses() *PredictableService {
	return &PredictableService{
		Responses: []PredictableResponse{
			// Initial greeting
			{
				Content:    "Hello! I'm Shelley, your AI assistant. I can help you with coding tasks, file management, and more. What would you like to work on?",
				StopReason: llm.StopReasonStopSequence,
				Usage: llm.Usage{
					InputTokens:  15,
					OutputTokens: 25,
					CostUSD:      0.002,
				},
			},
			// Response with tool call
			{
				Content: "I'll help you create a simple example. Let me think about this first.",
				ToolCalls: []PredictableToolCall{
					{
						ID:    "tool_001",
						Name:  "think",
						Input: json.RawMessage(`{"thoughts": "The user wants me to create an example. I should think about what would be most helpful."}`),
					},
				},
				StopReason: llm.StopReasonToolUse,
				Usage: llm.Usage{
					InputTokens:  20,
					OutputTokens: 15,
					CostUSD:      0.003,
				},
			},
			// Follow-up response after tool use
			{
				Content:    "Great! Based on my analysis, I can help you with that. Let me know if you need any modifications or have other questions.",
				StopReason: llm.StopReasonStopSequence,
				Usage: llm.Usage{
					InputTokens:  25,
					OutputTokens: 22,
					CostUSD:      0.004,
				},
			},
			// Response with bash tool call
			{
				Content: "I'll run a command to check the current directory structure.",
				ToolCalls: []PredictableToolCall{
					{
						ID:    "tool_002",
						Name:  "bash",
						Input: json.RawMessage(`{"command": "ls -la"}`),
					},
				},
				StopReason: llm.StopReasonToolUse,
				Usage: llm.Usage{
					InputTokens:  18,
					OutputTokens: 12,
					CostUSD:      0.002,
				},
			},
			// Final response
			{
				Content:    "Perfect! I can see the directory structure now. Is there anything else you'd like me to help you with?",
				StopReason: llm.StopReasonStopSequence,
				Usage: llm.Usage{
					InputTokens:  30,
					OutputTokens: 18,
					CostUSD:      0.005,
				},
			},
		},
		tokenContextWindow: 200000,
	}
}

// SetResponses sets the canned responses for the service
func (s *PredictableService) SetResponses(responses []PredictableResponse) {
	s.Responses = responses
	s.currentIndex = 0
}

// AddResponse adds a new canned response
func (s *PredictableService) AddResponse(response PredictableResponse) {
	s.Responses = append(s.Responses, response)
}

// AddSimpleResponse adds a simple text response
func (s *PredictableService) AddSimpleResponse(text string) {
	s.AddResponse(PredictableResponse{
		Content:    text,
		StopReason: llm.StopReasonStopSequence,
		Usage: llm.Usage{
			InputTokens:  uint64(len(strings.Fields(text))),
			OutputTokens: uint64(len(strings.Fields(text))),
			CostUSD:      0.001,
		},
	})
}

// TokenContextWindow returns the maximum token context window size
func (s *PredictableService) TokenContextWindow() int {
	return s.tokenContextWindow
}

// Do processes a request and returns a predictable response
func (s *PredictableService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	// Check for special test commands in the user message
	if len(req.Messages) > 0 {
		lastMessage := req.Messages[len(req.Messages)-1]
		if lastMessage.Role == llm.MessageRoleUser {
			for _, content := range lastMessage.Content {
				if content.Type == llm.ContentTypeText {
					if response := s.handleSpecialCommand(content.Text); response != nil {
						return response, nil
					}
				}
			}
		}
	}

	if s.currentIndex >= len(s.Responses) {
		// If we've exhausted all responses, return a default one
		return &llm.Response{
			ID:    fmt.Sprintf("pred-%d", time.Now().UnixNano()),
			Type:  "message",
			Role:  llm.MessageRoleAssistant,
			Model: "predictable-v1",
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "I've run out of predictable responses. Try special commands like 'echo foo', 'error bar', or 'tool bash ls'."},
			},
			StopReason: llm.StopReasonStopSequence,
			Usage: llm.Usage{
				InputTokens:  5,
				OutputTokens: 15,
				CostUSD:      0.001,
			},
		}, nil
	}

	response := s.Responses[s.currentIndex]
	s.currentIndex++

	// Build content from the response
	var content []llm.Content

	if response.Content != "" {
		content = append(content, llm.Content{
			Type: llm.ContentTypeText,
			Text: response.Content,
		})
	}

	// Add tool calls if any
	for _, toolCall := range response.ToolCalls {
		content = append(content, llm.Content{
			ID:        toolCall.ID,
			Type:      llm.ContentTypeToolUse,
			ToolName:  toolCall.Name,
			ToolInput: toolCall.Input,
		})
	}

	stopReason := response.StopReason
	if len(response.ToolCalls) > 0 {
		stopReason = llm.StopReasonToolUse
	}

	return &llm.Response{
		ID:         fmt.Sprintf("pred-%d", time.Now().UnixNano()),
		Type:       "message",
		Role:       llm.MessageRoleAssistant,
		Model:      "predictable-v1",
		Content:    content,
		StopReason: stopReason,
		Usage:      response.Usage,
	}, nil
}

// Reset resets the response index to 0
func (s *PredictableService) Reset() {
	s.currentIndex = 0
}

// CurrentIndex returns the current response index
func (s *PredictableService) CurrentIndex() int {
	return s.currentIndex
}

// handleSpecialCommand handles special test commands
func (s *PredictableService) handleSpecialCommand(text string) *llm.Response {
	text = strings.TrimSpace(text)

	// Match "echo <text>" command
	if matched, _ := regexp.MatchString(`^echo\s+(.+)$`, text); matched {
		re := regexp.MustCompile(`^echo\s+(.+)$`)
		matches := re.FindStringSubmatch(text)
		if len(matches) > 1 {
			return &llm.Response{
				ID:    fmt.Sprintf("pred-echo-%d", time.Now().UnixNano()),
				Type:  "message",
				Role:  llm.MessageRoleAssistant,
				Model: "predictable-v1",
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: matches[1]},
				},
				StopReason: llm.StopReasonStopSequence,
				Usage: llm.Usage{
					InputTokens:  uint64(len(strings.Fields(text))),
					OutputTokens: uint64(len(strings.Fields(matches[1]))),
					CostUSD:      0.001,
				},
			}
		}
	}

	// Match "error <message>" command
	if matched, _ := regexp.MatchString(`^error\s+(.+)$`, text); matched {
		re := regexp.MustCompile(`^error\s+(.+)$`)
		matches := re.FindStringSubmatch(text)
		if len(matches) > 1 {
			return &llm.Response{
				ID:    fmt.Sprintf("pred-error-%d", time.Now().UnixNano()),
				Type:  "message",
				Role:  llm.MessageRoleAssistant,
				Model: "predictable-v1",
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: fmt.Sprintf("Error: %s", matches[1])},
				},
				StopReason: llm.StopReasonStopSequence,
				Usage: llm.Usage{
					InputTokens:  uint64(len(strings.Fields(text))),
					OutputTokens: uint64(len(strings.Fields(matches[1])) + 1),
					CostUSD:      0.001,
				},
			}
		}
	}

	// Match "tool <toolname> <args>" command
	if matched, _ := regexp.MatchString(`^tool\s+(\w+)\s*(.*)$`, text); matched {
		re := regexp.MustCompile(`^tool\s+(\w+)\s*(.*)$`)
		matches := re.FindStringSubmatch(text)
		if len(matches) > 1 {
			toolName := matches[1]
			toolArgs := matches[2]

			var toolInput json.RawMessage
			switch toolName {
			case "bash":
				if toolArgs == "" {
					toolArgs = "echo 'Hello from predictable bash tool'"
				}
				toolInput = json.RawMessage(fmt.Sprintf(`{"command": "%s"}`, toolArgs))
			case "think":
				if toolArgs == "" {
					toolArgs = "This is a predictable thought"
				}
				toolInput = json.RawMessage(fmt.Sprintf(`{"thoughts": "%s"}`, toolArgs))
			default:
				toolInput = json.RawMessage(fmt.Sprintf(`{"input": "%s"}`, toolArgs))
			}

			return &llm.Response{
				ID:    fmt.Sprintf("pred-tool-%d", time.Now().UnixNano()),
				Type:  "message",
				Role:  llm.MessageRoleAssistant,
				Model: "predictable-v1",
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: fmt.Sprintf("I'll use the %s tool now.", toolName)},
					{
						ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
						Type:      llm.ContentTypeToolUse,
						ToolName:  toolName,
						ToolInput: toolInput,
					},
				},
				StopReason: llm.StopReasonToolUse,
				Usage: llm.Usage{
					InputTokens:  uint64(len(strings.Fields(text))),
					OutputTokens: uint64(10 + len(strings.Fields(toolArgs))),
					CostUSD:      0.002,
				},
			}
		}
	}

	return nil // No special command matched
}
