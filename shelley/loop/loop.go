package loop

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"shelley.exe.dev/llm"
)

// MessageRecordFunc is called to record new messages to persistent storage
type MessageRecordFunc func(ctx context.Context, message llm.Message, usage llm.Usage) error

// Config contains all configuration needed to create a Loop
type Config struct {
	LLM           llm.Service
	History       []llm.Message
	Tools         []*llm.Tool
	RecordMessage MessageRecordFunc
	Logger        *slog.Logger
	System        []llm.SystemContent
}

// Loop manages a conversation turn with an LLM including tool execution and message recording.
// Notably, when the turn ends, the "Loop" is over. TODO: maybe rename to Turn?
type Loop struct {
	llm           llm.Service
	tools         []*llm.Tool
	recordMessage MessageRecordFunc
	history       []llm.Message
	messageQueue  []llm.Message
	totalUsage    llm.Usage
	mu            sync.Mutex
	logger        *slog.Logger
	system        []llm.SystemContent
}

// NewLoop creates a new Loop instance with the provided configuration
func NewLoop(config Config) *Loop {
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Loop{
		llm:           config.LLM,
		history:       config.History,
		tools:         config.Tools,
		recordMessage: config.RecordMessage,
		messageQueue:  make([]llm.Message, 0),
		logger:        logger,
		system:        config.System,
	}
}

// QueueUserMessage adds a user message to the queue to be processed
func (l *Loop) QueueUserMessage(message llm.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.messageQueue = append(l.messageQueue, message)
	l.logger.Debug("queued user message", "content_count", len(message.Content))
}

// GetUsage returns the total usage accumulated by this loop
func (l *Loop) GetUsage() llm.Usage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.totalUsage
}

// GetHistory returns a copy of the current conversation history
func (l *Loop) GetHistory() []llm.Message {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Deep copy the messages to prevent modifications
	historyCopy := make([]llm.Message, len(l.history))
	for i, msg := range l.history {
		// Copy the message
		historyCopy[i] = llm.Message{
			Role:    msg.Role,
			ToolUse: msg.ToolUse, // This is a pointer, but we won't modify it in tests
			Content: make([]llm.Content, len(msg.Content)),
		}
		// Copy content slice
		copy(historyCopy[i].Content, msg.Content)
	}
	return historyCopy
}

// Go runs the conversation loop until the context is canceled
func (l *Loop) Go(ctx context.Context) error {
	if l.llm == nil {
		return fmt.Errorf("no LLM service configured")
	}

	l.logger.Info("starting conversation loop", "tools", len(l.tools))

	for {
		select {
		case <-ctx.Done():
			l.logger.Info("conversation loop canceled")
			return ctx.Err()
		default:
		}

		// Process any queued messages
		l.mu.Lock()
		hasQueuedMessages := len(l.messageQueue) > 0
		if hasQueuedMessages {
			// Add queued messages to history
			for _, msg := range l.messageQueue {
				l.history = append(l.history, msg)
				// Record user messages
				if msg.Role == llm.MessageRoleUser {
					if err := l.recordMessage(ctx, msg, llm.Usage{}); err != nil {
						l.logger.Error("failed to record user message", "error", err)
					}
				}
			}
			l.messageQueue = l.messageQueue[:0] // Clear queue
		}
		l.mu.Unlock()

		if hasQueuedMessages {
			// Send request to LLM
			l.logger.Debug("processing queued messages", "count", 1)
			if err := l.processLLMRequest(ctx); err != nil {
				l.logger.Error("failed to process LLM request", "error", err)
				time.Sleep(time.Second) // Wait before retrying
				continue
			}
			l.logger.Debug("finished processing queued messages")
		} else {
			// No queued messages, wait a bit
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
				// Continue loop
			}
		}
	}
}

// ProcessOneTurn processes queued messages through one complete turn (user message + assistant response)
// It stops after the assistant responds, regardless of whether tools were called
func (l *Loop) ProcessOneTurn(ctx context.Context) error {
	if l.llm == nil {
		return fmt.Errorf("no LLM service configured")
	}

	// Process any queued messages first
	l.mu.Lock()
	if len(l.messageQueue) > 0 {
		// Add queued messages to history and record user messages for parity with Go()
		for _, msg := range l.messageQueue {
			l.history = append(l.history, msg)
			if msg.Role == llm.MessageRoleUser {
				if err := l.recordMessage(ctx, msg, llm.Usage{}); err != nil {
					l.logger.Error("failed to record user message", "error", err)
				}
			}
		}
		l.messageQueue = nil
	}
	l.mu.Unlock()

	// Process one LLM request and response
	return l.processLLMRequest(ctx)
}

// processLLMRequest sends a request to the LLM and handles the response
func (l *Loop) processLLMRequest(ctx context.Context) error {
	l.mu.Lock()
	messages := append([]llm.Message(nil), l.history...)
	tools := l.tools
	system := l.system
	llmService := l.llm
	l.mu.Unlock()

	// Enable prompt caching: set cache flag on last tool and last user message content
	// See https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
	if len(tools) > 0 {
		// Make a copy of tools to avoid modifying the shared slice
		tools = append([]*llm.Tool(nil), tools...)
		// Copy the last tool and enable caching
		lastTool := *tools[len(tools)-1]
		lastTool.Cache = true
		tools[len(tools)-1] = &lastTool
	}

	// Set cache flag on the last content block of the last user message
	if len(messages) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == llm.MessageRoleUser && len(messages[i].Content) > 0 {
				// Deep copy the message to avoid modifying the shared history
				msg := messages[i]
				msg.Content = append([]llm.Content(nil), msg.Content...)
				msg.Content[len(msg.Content)-1].Cache = true
				messages[i] = msg
				break
			}
		}
	}

	req := &llm.Request{
		Messages: messages,
		Tools:    tools,
		System:   system,
	}

	// Insert missing tool results if the previous message had tool_use blocks
	// without corresponding tool_result blocks. This can happen when a request
	// is cancelled or fails after the LLM responds but before tools execute.
	l.insertMissingToolResults(req)

	systemLen := 0
	for _, sys := range system {
		systemLen += len(sys.Text)
	}
	l.logger.Debug("sending LLM request", "message_count", len(messages), "tool_count", len(tools), "system_items", len(system), "system_length", systemLen)

	// Add a timeout for the LLM request to prevent indefinite hangs
	llmCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	resp, err := llmService.Do(llmCtx, req)
	if err != nil {
		// Record the error as a message so it can be displayed in the UI
		errorMessage := llm.Message{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{
					Type: llm.ContentTypeText,
					Text: fmt.Sprintf("LLM request failed: %v", err),
				},
			},
		}
		if recordErr := l.recordMessage(ctx, errorMessage, llm.Usage{}); recordErr != nil {
			l.logger.Error("failed to record error message", "error", recordErr)
		}
		return fmt.Errorf("LLM request failed: %w", err)
	}

	l.logger.Debug("received LLM response", "content_count", len(resp.Content), "stop_reason", resp.StopReason.String(), "usage", resp.Usage.String())

	// Update total usage
	l.mu.Lock()
	l.totalUsage.Add(resp.Usage)
	l.mu.Unlock()

	// Convert response to message and add to history
	assistantMessage := resp.ToMessage()
	l.mu.Lock()
	l.history = append(l.history, assistantMessage)
	l.mu.Unlock()

	// Record assistant message with model and timing metadata
	usageWithMeta := resp.Usage
	usageWithMeta.Model = resp.Model
	usageWithMeta.StartTime = resp.StartTime
	usageWithMeta.EndTime = resp.EndTime
	if err := l.recordMessage(ctx, assistantMessage, usageWithMeta); err != nil {
		l.logger.Error("failed to record assistant message", "error", err)
	}

	// Handle tool calls if any
	if resp.StopReason == llm.StopReasonToolUse {
		l.logger.Debug("handling tool calls", "content_count", len(resp.Content))
		return l.handleToolCalls(ctx, resp.Content)
	}

	return nil
}

// handleToolCalls processes tool calls from the LLM response
func (l *Loop) handleToolCalls(ctx context.Context, content []llm.Content) error {
	var toolResults []llm.Content

	for _, c := range content {
		if c.Type != llm.ContentTypeToolUse {
			continue
		}

		l.logger.Debug("executing tool", "name", c.ToolName, "id", c.ID)

		// Find the tool
		var tool *llm.Tool
		for _, t := range l.tools {
			if t.Name == c.ToolName {
				tool = t
				break
			}
		}

		if tool == nil {
			l.logger.Error("tool not found", "name", c.ToolName)
			toolResults = append(toolResults, llm.Content{
				Type:      llm.ContentTypeToolResult,
				ToolUseID: c.ID,
				ToolError: true,
				ToolResult: []llm.Content{
					{Type: llm.ContentTypeText, Text: fmt.Sprintf("Tool '%s' not found", c.ToolName)},
				},
			})
			continue
		}

		// Execute the tool
		startTime := time.Now()
		result := tool.Run(ctx, c.ToolInput)
		endTime := time.Now()

		var toolResultContent []llm.Content
		if result.Error != nil {
			l.logger.Error("tool execution failed", "name", c.ToolName, "error", result.Error)
			toolResultContent = []llm.Content{
				{Type: llm.ContentTypeText, Text: result.Error.Error()},
			}
		} else {
			toolResultContent = result.LLMContent
			l.logger.Debug("tool executed successfully", "name", c.ToolName, "duration", endTime.Sub(startTime))
		}

		toolResults = append(toolResults, llm.Content{
			Type:             llm.ContentTypeToolResult,
			ToolUseID:        c.ID,
			ToolError:        result.Error != nil,
			ToolResult:       toolResultContent,
			ToolUseStartTime: &startTime,
			ToolUseEndTime:   &endTime,
			Display:          result.Display,
		})
	}

	if len(toolResults) > 0 {
		// Add tool results to history as a user message
		toolMessage := llm.Message{
			Role:    llm.MessageRoleUser,
			Content: toolResults,
		}

		l.mu.Lock()
		l.history = append(l.history, toolMessage)
		l.mu.Unlock()

		// Record tool result message
		if err := l.recordMessage(ctx, toolMessage, llm.Usage{}); err != nil {
			l.logger.Error("failed to record tool result message", "error", err)
		}

		// Process another LLM request with the tool results
		return l.processLLMRequest(ctx)
	}

	return nil
}

// insertMissingToolResults adds error results for tool uses that were requested
// but not included in the next message. This can happen when a request is cancelled
// or fails after the LLM responds with tool_use blocks but before the tools execute.
// This prevents the "tool_use ids were found without tool_result blocks" error from
// the Anthropic API. Mutates the request's Messages slice.
func (l *Loop) insertMissingToolResults(req *llm.Request) {
	if len(req.Messages) < 2 {
		return
	}
	prev := req.Messages[len(req.Messages)-2]
	current := req.Messages[len(req.Messages)-1]

	// Only check if previous message is assistant and current is user
	if prev.Role != llm.MessageRoleAssistant || current.Role != llm.MessageRoleUser {
		return
	}

	// Count tool uses in previous message
	var toolUsePrev int
	var toolUseContents []llm.Content
	for _, c := range prev.Content {
		if c.Type == llm.ContentTypeToolUse {
			toolUsePrev++
			toolUseContents = append(toolUseContents, c)
		}
	}
	if toolUsePrev == 0 {
		return
	}

	// Count tool results in current message
	var toolResultCurrent int
	for _, c := range current.Content {
		if c.Type == llm.ContentTypeToolResult {
			toolResultCurrent++
		}
	}

	// If we have tool results already, don't insert missing ones
	// (partial results would be a programmer error)
	if toolResultCurrent != 0 {
		return
	}

	// Create error results for all tool uses
	var prefix []llm.Content
	for _, part := range toolUseContents {
		content := llm.Content{
			Type:      llm.ContentTypeToolResult,
			ToolUseID: part.ID,
			ToolError: true,
			ToolResult: []llm.Content{{
				Type: llm.ContentTypeText,
				Text: "not executed; retry possible",
			}},
		}
		prefix = append(prefix, content)
	}

	// Prepend the synthetic tool results
	current.Content = append(prefix, current.Content...)
	req.Messages[len(req.Messages)-1] = current
	l.logger.Debug("inserted missing tool results", "count", len(prefix))
}
