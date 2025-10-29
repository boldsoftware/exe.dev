package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/llm"
)

// PredictableService is an LLM service that returns predictable responses for testing.
//
// To add new test patterns, update the Do() method directly by adding cases to the switch
// statement or new prefix checks. Do not extend or wrap this service - modify it in place.
// Available patterns include:
//   - "echo: <text>" - echoes the text back
//   - "bash: <command>" - triggers bash tool with command
//   - "think: <thoughts>" - triggers think tool
//   - "delay: <seconds>" - delays response by specified seconds
//   - See Do() method for complete list of supported patterns
type PredictableService struct {
	// TokenContextWindow size
	tokenContextWindow int
	mu                 sync.Mutex
	// Recent requests for testing inspection
	recentRequests []*llm.Request
	responseDelay  time.Duration
}

// NewPredictableService creates a new predictable LLM service
func NewPredictableService() *PredictableService {
	svc := &PredictableService{
		tokenContextWindow: 200000,
	}

	if delayEnv := os.Getenv("PREDICTABLE_DELAY_MS"); delayEnv != "" {
		if ms, err := strconv.Atoi(delayEnv); err == nil && ms > 0 {
			svc.responseDelay = time.Duration(ms) * time.Millisecond
		}
	}

	return svc
}

// TokenContextWindow returns the maximum token context window size
func (s *PredictableService) TokenContextWindow() int {
	return s.tokenContextWindow
}

// Do processes a request and returns a predictable response based on the input text
func (s *PredictableService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	// Store request for testing inspection
	s.mu.Lock()
	delay := s.responseDelay
	s.recentRequests = append(s.recentRequests, req)
	// Keep only last 10 requests
	if len(s.recentRequests) > 10 {
		s.recentRequests = s.recentRequests[len(s.recentRequests)-10:]
	}
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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

	case "screenshot":
		// Trigger a screenshot of the current page
		return s.makeScreenshotToolResponse(""), nil

	case "tool smorgasbord":
		// Return a response with all tool types for testing
		return s.makeToolSmorgasbordResponse(), nil

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
			return nil, fmt.Errorf("predictable error: %s", errorMsg)
		}

		if strings.HasPrefix(inputText, "screenshot: ") {
			selector := strings.TrimSpace(strings.TrimPrefix(inputText, "screenshot: "))
			return s.makeScreenshotToolResponse(selector), nil
		}

		if strings.HasPrefix(inputText, "delay: ") {
			delayStr := strings.TrimPrefix(inputText, "delay: ")
			delaySeconds, err := strconv.ParseFloat(delayStr, 64)
			if err == nil && delaySeconds > 0 {
				delayDuration := time.Duration(delaySeconds * float64(time.Second))
				select {
				case <-time.After(delayDuration):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return s.makeResponse(fmt.Sprintf("Delayed for %s seconds", delayStr)), nil
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

// GetRecentRequests returns the recent requests made to this service
func (s *PredictableService) GetRecentRequests() []*llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.recentRequests) == 0 {
		return nil
	}

	requests := make([]*llm.Request, len(s.recentRequests))
	copy(requests, s.recentRequests)
	return requests
}

// GetLastRequest returns the most recent request, or nil if none
func (s *PredictableService) GetLastRequest() *llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.recentRequests) == 0 {
		return nil
	}
	return s.recentRequests[len(s.recentRequests)-1]
}

// ClearRequests clears the request history
func (s *PredictableService) ClearRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recentRequests = nil
}

// makeScreenshotToolResponse creates a response that calls the screenshot tool
func (s *PredictableService) makeScreenshotToolResponse(selector string) *llm.Response {
	toolInputData := map[string]any{}
	if selector != "" {
		toolInputData["selector"] = selector
	}
	toolInputBytes, _ := json.Marshal(toolInputData)
	toolInput := json.RawMessage(toolInputBytes)
	return &llm.Response{
		ID:    fmt.Sprintf("pred-screenshot-%d", time.Now().UnixNano()),
		Type:  "message",
		Role:  llm.MessageRoleAssistant,
		Model: "predictable-v1",
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: "Taking a screenshot..."},
			{
				ID:        fmt.Sprintf("tool_%d", time.Now().UnixNano()%1000),
				Type:      llm.ContentTypeToolUse,
				ToolName:  "browser_take_screenshot",
				ToolInput: toolInput,
			},
		},
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  5,
			OutputTokens: 5,
			CostUSD:      0.0,
		},
	}
}

// makeToolSmorgasbordResponse creates a response that uses all available tool types
func (s *PredictableService) makeToolSmorgasbordResponse() *llm.Response {
	baseNano := time.Now().UnixNano()
	content := []llm.Content{
		{Type: llm.ContentTypeText, Text: "Here's a sample of all the tools:"},
	}

	// bash tool
	bashInput, _ := json.Marshal(map[string]string{"command": "echo 'hello from bash'"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_bash_%d", baseNano%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "bash",
		ToolInput: json.RawMessage(bashInput),
	})

	// think tool
	thinkInput, _ := json.Marshal(map[string]string{"thoughts": "I'm thinking about the best approach for this task. Let me consider all the options available."})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_think_%d", (baseNano+1)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "think",
		ToolInput: json.RawMessage(thinkInput),
	})

	// patch tool
	patchInput, _ := json.Marshal(map[string]interface{}{
		"path": "/tmp/example.txt",
		"patches": []map[string]string{
			{"operation": "replace", "oldText": "foo", "newText": "bar"},
		},
	})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_patch_%d", (baseNano+2)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "patch",
		ToolInput: json.RawMessage(patchInput),
	})

	// screenshot tool
	screenshotInput, _ := json.Marshal(map[string]string{})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_screenshot_%d", (baseNano+3)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_take_screenshot",
		ToolInput: json.RawMessage(screenshotInput),
	})

	// keyword_search tool
	keywordInput, _ := json.Marshal(map[string]interface{}{
		"query":        "find all references",
		"search_terms": []string{"reference", "example"},
	})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_keyword_%d", (baseNano+4)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "keyword_search",
		ToolInput: json.RawMessage(keywordInput),
	})

	// browser_navigate tool
	navigateInput, _ := json.Marshal(map[string]string{"url": "https://example.com"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_navigate_%d", (baseNano+5)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_navigate",
		ToolInput: json.RawMessage(navigateInput),
	})

	// browser_eval tool
	evalInput, _ := json.Marshal(map[string]string{"script": "document.title"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_eval_%d", (baseNano+6)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_eval",
		ToolInput: json.RawMessage(evalInput),
	})

	// read_image tool
	readImageInput, _ := json.Marshal(map[string]string{"path": "/tmp/image.png"})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_readimg_%d", (baseNano+7)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "read_image",
		ToolInput: json.RawMessage(readImageInput),
	})

	// browser_recent_console_logs tool
	consoleInput, _ := json.Marshal(map[string]string{})
	content = append(content, llm.Content{
		ID:        fmt.Sprintf("tool_console_%d", (baseNano+8)%1000),
		Type:      llm.ContentTypeToolUse,
		ToolName:  "browser_recent_console_logs",
		ToolInput: json.RawMessage(consoleInput),
	})

	return &llm.Response{
		ID:         fmt.Sprintf("pred-smorgasbord-%d", baseNano),
		Type:       "message",
		Role:       llm.MessageRoleAssistant,
		Model:      "predictable-v1",
		Content:    content,
		StopReason: llm.StopReasonToolUse,
		Usage: llm.Usage{
			InputTokens:  100,
			OutputTokens: 200,
			CostUSD:      0.01,
		},
	}
}
