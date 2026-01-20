package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/llm"
)

// TestInterruptionDuringToolExecution tests that user messages queued during
// tool execution are processed after the tool completes but before the next
// tool starts (not at the end of the entire turn).
func TestInterruptionDuringToolExecution(t *testing.T) {
	// Track when the tool is called and when it completes
	var toolStarted atomic.Bool
	var toolCompleted atomic.Bool
	var interruptionSeen atomic.Bool

	// Create a slow tool
	slowTool := &llm.Tool{
		Name:        "slow_tool",
		Description: "A tool that takes time to execute",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {"input": {"type": "string"}}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			toolStarted.Store(true)
			// Sleep to simulate slow tool execution
			time.Sleep(200 * time.Millisecond)
			toolCompleted.Store(true)
			return llm.ToolOut{
				LLMContent: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Tool completed"},
				},
			}
		},
	}

	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return nil
	}

	// Create a service that detects the interruption
	service := &customPredictableService{
		responseFunc: func(req *llm.Request) (*llm.Response, error) {
			// Check if we've seen the interruption
			toolResults := 0
			for _, msg := range req.Messages {
				for _, c := range msg.Content {
					if c.Type == llm.ContentTypeToolResult {
						toolResults++
					}
					if c.Type == llm.ContentTypeText && c.Text == "INTERRUPTION" {
						interruptionSeen.Store(true)
						return &llm.Response{
							Role:       llm.MessageRoleAssistant,
							StopReason: llm.StopReasonEndTurn,
							Content: []llm.Content{
								{Type: llm.ContentTypeText, Text: "Acknowledged interruption"},
							},
						}, nil
					}
				}
			}

			// First call: use the slow tool
			if toolResults == 0 {
				return &llm.Response{
					Role:       llm.MessageRoleAssistant,
					StopReason: llm.StopReasonToolUse,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "I'll use the slow tool"},
						{
							Type:      llm.ContentTypeToolUse,
							ID:        "tool_1",
							ToolName:  "slow_tool",
							ToolInput: json.RawMessage(`{"input":"test"}`),
						},
					},
				}, nil
			}

			// After tool result, continue with more work
			return &llm.Response{
				Role:       llm.MessageRoleAssistant,
				StopReason: llm.StopReasonEndTurn,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Done with tool"},
				},
			}, nil
		},
	}

	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         []*llm.Tool{slowTool},
		RecordMessage: recordMessage,
	})

	// Queue initial user message that will trigger tool use
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "use the tool"}},
	})

	// Run the loop in background
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var loopDone sync.WaitGroup
	loopDone.Add(1)
	go func() {
		defer loopDone.Done()
		loop.Go(ctx)
	}()

	// Wait for tool to start
	for !toolStarted.Load() {
		time.Sleep(10 * time.Millisecond)
	}

	// Queue an interruption message while tool is executing
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "INTERRUPTION"}},
	})
	t.Log("Queued interruption message while tool is executing")

	// The message should remain in queue while tool is executing
	time.Sleep(50 * time.Millisecond)
	if !toolCompleted.Load() {
		loop.mu.Lock()
		queueLen := len(loop.messageQueue)
		loop.mu.Unlock()
		if queueLen > 0 {
			t.Log("Message is waiting in queue during tool execution (expected)")
		}
	}

	// Wait for loop to finish
	time.Sleep(500 * time.Millisecond)
	cancel()
	loopDone.Wait()

	// Verify the interruption was seen by the LLM
	if interruptionSeen.Load() {
		t.Log("SUCCESS: Interruption was seen by LLM after tool completed")
	} else {
		t.Error("Interruption was never seen by the LLM")
	}
}

// TestInterruptionDuringMultiToolChain tests interruption during a chain of tool calls.
// With the fix, the interruption should be visible to the LLM after the first tool completes.
func TestInterruptionDuringMultiToolChain(t *testing.T) {
	var toolCallCount atomic.Int32
	var interruptionSeenAtToolResult atomic.Int32 // -1 means not seen

	// Create a tool that's called multiple times
	multiTool := &llm.Tool{
		Name:        "multi_tool",
		Description: "A tool that might be called multiple times",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {"step": {"type": "integer"}}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			count := toolCallCount.Add(1)
			time.Sleep(100 * time.Millisecond) // Simulate some work
			_ = count
			return llm.ToolOut{
				LLMContent: []llm.Content{
					{Type: llm.ContentTypeText, Text: "Tool step completed"},
				},
			}
		},
	}

	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return nil
	}

	// Service that makes multiple tool calls but stops when it sees "STOP"
	interruptionSeenAtToolResult.Store(-1)
	service := &customPredictableService{
		responseFunc: func(req *llm.Request) (*llm.Response, error) {
			// Check if we've seen the STOP message
			toolResults := 0
			for _, msg := range req.Messages {
				for _, c := range msg.Content {
					if c.Type == llm.ContentTypeToolResult {
						toolResults++
					}
					if c.Type == llm.ContentTypeText && c.Text == "STOP" {
						// Record when we first saw the interruption
						interruptionSeenAtToolResult.CompareAndSwap(-1, int32(toolResults))
						// Stop immediately when we see the interruption
						return &llm.Response{
							Role:       llm.MessageRoleAssistant,
							StopReason: llm.StopReasonEndTurn,
							Content: []llm.Content{
								{Type: llm.ContentTypeText, Text: "Stopped due to user interruption"},
							},
						}, nil
					}
				}
			}

			if toolResults < 5 {
				// Keep calling the tool (would do 5 if not interrupted)
				return &llm.Response{
					Role:       llm.MessageRoleAssistant,
					StopReason: llm.StopReasonToolUse,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Calling tool again"},
						{
							Type:      llm.ContentTypeToolUse,
							ID:        fmt.Sprintf("tool_%d", toolResults+1),
							ToolName:  "multi_tool",
							ToolInput: json.RawMessage(fmt.Sprintf(`{"step":%d}`, toolResults+1)),
						},
					},
				}, nil
			}

			// Done with tools
			return &llm.Response{
				Role:       llm.MessageRoleAssistant,
				StopReason: llm.StopReasonEndTurn,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "All tools completed"},
				},
			}, nil
		},
	}

	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         []*llm.Tool{multiTool},
		RecordMessage: recordMessage,
	})

	// Queue initial user message
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run the tool 5 times"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var loopDone sync.WaitGroup
	loopDone.Add(1)
	go func() {
		defer loopDone.Done()
		loop.Go(ctx)
	}()

	// Wait for first tool call to complete
	for toolCallCount.Load() < 1 {
		time.Sleep(10 * time.Millisecond)
	}

	// Queue interruption after first tool
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "STOP"}},
	})
	t.Logf("Queued STOP message after tool call %d", toolCallCount.Load())

	// Wait for loop to process and stop
	time.Sleep(500 * time.Millisecond)

	cancel()
	loopDone.Wait()

	finalToolCount := toolCallCount.Load()
	seenAt := interruptionSeenAtToolResult.Load()

	t.Logf("Final tool call count: %d (would be 5 without interruption)", finalToolCount)
	t.Logf("Interruption was seen by LLM after tool result %d", seenAt)

	// With the fix, the interruption should be seen after just 1 tool result
	// (the tool that was running when we queued the STOP message)
	if seenAt == 1 {
		t.Log("SUCCESS: Interruption was processed immediately after first tool completed")
	} else if seenAt > 1 {
		t.Errorf("Interruption was delayed: seen after %d tool results, expected 1", seenAt)
	} else if seenAt == -1 {
		t.Error("Interruption was never seen by the LLM")
	}

	// The tool should only be called a small number of times since we interrupted
	if finalToolCount > 2 {
		t.Errorf("Too many tool calls (%d): interruption should have stopped the chain earlier", finalToolCount)
	}
}

// customPredictableService allows custom response logic for testing
type customPredictableService struct {
	responses    []customResponse
	responseFunc func(req *llm.Request) (*llm.Response, error)
	callIndex    int
	mu           sync.Mutex
}

type customResponse struct {
	response *llm.Response
	err      error
}

func (s *customPredictableService) Do(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.responseFunc != nil {
		return s.responseFunc(req)
	}

	if s.callIndex >= len(s.responses) {
		// Default response
		return &llm.Response{
			Role:       llm.MessageRoleAssistant,
			StopReason: llm.StopReasonEndTurn,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "No more responses configured"},
			},
		}, nil
	}

	resp := s.responses[s.callIndex]
	s.callIndex++
	return resp.response, resp.err
}

func (s *customPredictableService) GetDefaultModel() string {
	return "custom-test"
}

func (s *customPredictableService) TokenContextWindow() int {
	return 100000
}

func (s *customPredictableService) MaxImageDimension() int {
	return 8000
}

// TestNoInterruptionNormalFlow verifies that normal tool chains work correctly
// when no interruption is queued.
func TestNoInterruptionNormalFlow(t *testing.T) {
	var toolCallCount atomic.Int32

	// Create a tool that tracks calls
	multiTool := &llm.Tool{
		Name:        "multi_tool",
		Description: "A tool",
		InputSchema: llm.MustSchema(`{"type": "object", "properties": {"step": {"type": "integer"}}}`),
		Run: func(ctx context.Context, input json.RawMessage) llm.ToolOut {
			toolCallCount.Add(1)
			return llm.ToolOut{
				LLMContent: []llm.Content{
					{Type: llm.ContentTypeText, Text: "done"},
				},
			}
		},
	}

	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return nil
	}

	// Service that makes 3 tool calls then finishes
	service := &customPredictableService{
		responseFunc: func(req *llm.Request) (*llm.Response, error) {
			toolResults := 0
			for _, msg := range req.Messages {
				for _, c := range msg.Content {
					if c.Type == llm.ContentTypeToolResult {
						toolResults++
					}
				}
			}

			if toolResults < 3 {
				return &llm.Response{
					Role:       llm.MessageRoleAssistant,
					StopReason: llm.StopReasonToolUse,
					Content: []llm.Content{
						{Type: llm.ContentTypeText, Text: "Calling tool"},
						{
							Type:      llm.ContentTypeToolUse,
							ID:        fmt.Sprintf("tool_%d", toolResults+1),
							ToolName:  "multi_tool",
							ToolInput: json.RawMessage(fmt.Sprintf(`{"step":%d}`, toolResults+1)),
						},
					},
				}, nil
			}

			return &llm.Response{
				Role:       llm.MessageRoleAssistant,
				StopReason: llm.StopReasonEndTurn,
				Content: []llm.Content{
					{Type: llm.ContentTypeText, Text: "All done"},
				},
			}, nil
		},
	}

	loop := NewLoop(Config{
		LLM:           service,
		History:       []llm.Message{},
		Tools:         []*llm.Tool{multiTool},
		RecordMessage: recordMessage,
	})

	// Queue initial user message (no interruption)
	loop.QueueUserMessage(llm.Message{
		Role:    llm.MessageRoleUser,
		Content: []llm.Content{{Type: llm.ContentTypeText, Text: "run tools"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var loopDone sync.WaitGroup
	loopDone.Add(1)
	go func() {
		defer loopDone.Done()
		loop.Go(ctx)
	}()

	// Wait for completion
	time.Sleep(500 * time.Millisecond)
	cancel()
	loopDone.Wait()

	finalCount := toolCallCount.Load()
	if finalCount != 3 {
		t.Errorf("Expected 3 tool calls, got %d", finalCount)
	} else {
		t.Log("SUCCESS: Normal flow completed 3 tool calls as expected")
	}
}
