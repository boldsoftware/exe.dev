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

```go
// Create tools (using claudetool package or custom tools)
tools := []*llm.Tool{bashTool, patchTool, thinkTool}

// Define message recording function (typically saves to the database)
recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
    return messageService.Create(ctx, db.CreateMessageParams{
        ConversationID: conversationID,
        Type:            getMessageType(message.Role),
        LLMData:         message,
        UsageData:       usage,
    })
}

// Create loop with explicit LLM configuration
agentLoop := loop.NewLoop(loop.Config{
    LLM:           &ant.Service{APIKey: apiKey},
    History:       history, // existing conversation history
    Tools:         tools,
    RecordMessage: recordMessage,
    Logger:        logger,
    System:        systemPrompt, // []llm.SystemContent
})

// Queue user messages for the current turn
agentLoop.QueueUserMessage(llm.UserStringMessage("Hello, please help me with something"))

// Run the conversation turn
ctx := context.Background()
if err := agentLoop.ProcessOneTurn(ctx); err != nil {
    log.Fatalf("conversation failed: %v", err)
}
```

## Testing with PredictableService

The `PredictableService` records requests and returns deterministic responses that are convenient for tests:

```go
service := loop.NewPredictableService()

testLoop := loop.NewLoop(loop.Config{
    LLM:           service,
    RecordMessage: func(context.Context, llm.Message, llm.Usage) error { return nil },
})

testLoop.QueueUserMessage(llm.UserStringMessage("hello"))
if err := testLoop.ProcessOneTurn(context.Background()); err != nil {
    t.Fatalf("loop failed: %v", err)
}

last := service.GetLastRequest()
require.NotNil(t, last)
```
