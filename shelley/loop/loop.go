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

// Loop manages a conversation with an LLM including tool execution and message recording
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

// NewLoop creates a new Loop instance
func NewLoop(llmService llm.Service, history []llm.Message, tools []*llm.Tool, recordMessage MessageRecordFunc) *Loop {
	return &Loop{
		llm:           llmService,
		history:       history,
		tools:         tools,
		recordMessage: recordMessage,
		messageQueue:  make([]llm.Message, 0),
		logger:        slog.Default(),
	}
}

// SetLLM sets the LLM service to use
func (l *Loop) SetLLM(service llm.Service) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.llm = service
}

// SetSystem sets the system messages
func (l *Loop) SetSystem(system []llm.SystemContent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.system = system
}

// SetLogger sets a custom logger
func (l *Loop) SetLogger(logger *slog.Logger) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logger = logger
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
			if err := l.processLLMRequest(ctx); err != nil {
				l.logger.Error("failed to process LLM request", "error", err)
				time.Sleep(time.Second) // Wait before retrying
				continue
			}
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
	l.mu.Unlock()

	req := &llm.Request{
		Messages: messages,
		Tools:    tools,
		System:   system,
	}

	l.logger.Debug("sending LLM request", "message_count", len(messages), "tool_count", len(tools))

	resp, err := l.llm.Do(ctx, req)
	if err != nil {
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

	// Record assistant message
	if err := l.recordMessage(ctx, assistantMessage, resp.Usage); err != nil {
		l.logger.Error("failed to record assistant message", "error", err)
	}

	// Handle tool calls if any
	if resp.StopReason == llm.StopReasonToolUse {
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
