# Loop Package

The `loop` package provides the core agentic conversation loop for Shelley,
handling LLM interactions, tool execution, and message recording.

## Features

- **LLM Integration**: Works with any LLM service implementing the `llm.Service` interface
- **Predictable Testing**: Includes a `PredictableService` for deterministic testing
- **Tool Execution**: Automatically executes tools called by the LLM
- **Message Recording**: Records all conversation messages via a configurable function
- **Usage Tracking**: Tracks token usage and costs across all LLM calls
- **Context Cancellation**: Gracefully handles context cancellation
- **Thread Safety**: All methods are safe for concurrent use

## Basic Usage

// TODOX: 1. The LLM should be passed in explicitly at creation. No auto-configuring
// or implicit stuff.
// 2. The loop should run until the end of turn. You can still inject messages
// during a turn, but it should exit after the turn ends.

```go
// Create tools (using claudetool package or custom tools)
tools := []*llm.Tool{bashTool, patchTool, thinkTool}

// Define message recording function (typically saves to database)
recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
    return messageService.Create(ctx, db.CreateMessageParams{
        ConversationID: conversationID,
        Type: getMessageType(message.Role),
        LLMData: message,
        UsageData: usage,
    })
}

// Create loop with conversation history
history := []llm.Message{ /* existing messages */ }
loop := loop.NewLoop(history, tools, recordMessage)

// Auto-configure LLM (uses ANTHROPIC_API_KEY if available, otherwise predictable service)
// Or set explicitly:
loop.SetLLM(&ant.Service{APIKey: apiKey})

// Queue user messages
loop.QueueUserMessage(llm.UserStringMessage("Hello, please help me with something"))

// Run the conversation loop
ctx := context.Background()
err := loop.Go(ctx) // Runs until context canceled
```

## Testing with PredictableService

// TODOX: Instead of configuring the predictable service every time,
// let's just always configure it!

The `PredictableService` allows you to create deterministic tests:

```go
service := loop.NewPredictableService()
service.SetResponses([]loop.PredictableResponse{
    {
        Content: "I'll help you with that.",
        StopReason: llm.StopReasonStopSequence,
        Usage: llm.Usage{InputTokens: 10, OutputTokens: 8},
    },
    {
        Content: "Let me use a tool.",
        ToolCalls: []loop.PredictableToolCall{
            {ID: "tool-1", Name: "bash", Input: json.RawMessage(`{"command": "ls"}`)}},
        StopReason: llm.StopReasonToolUse,
    },
})

loop.SetLLM(service)
```
