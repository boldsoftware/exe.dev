package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/server"
	"shelley.exe.dev/slug"
)

type GlobalConfig struct {
	DBPath          string
	Debug           bool
	Model           string
	PredictableOnly bool
}

// Message emoji constants
const (
	// User messages: person emoji
	userEmoji = "👤"
	// Assistant messages: robot emoji
	assistantEmoji = "🤖"
	// Tool messages: wrench emoji
	toolEmoji = "🔧"
)

func main() {
	// Define global flags
	var global GlobalConfig
	flag.StringVar(&global.DBPath, "db", "shelley.db", "Path to SQLite database file")
	flag.BoolVar(&global.Debug, "debug", false, "Enable debug logging")
	flag.StringVar(&global.Model, "model", "qwen3-coder-fireworks", "LLM model to use (default: qwen3-coder-fireworks; use 'predictable' for testing)")
	flag.BoolVar(&global.PredictableOnly, "predictable-only", false, "Use only the predictable service, ignoring all other models")

	// Custom usage function
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [global-flags] <command> [command-flags]\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Global flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nCommands:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  serve [flags]                 Start the web server\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  prompt [flags] <text>         Run a single conversation loop\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  list [flags]                 List conversations\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  inspect [flags] <id>          Show a conversation\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  models                        List supported models and env requirements\n")
		fmt.Fprintf(flag.CommandLine.Output(), "\nUse '%s <command> -h' for command-specific help\n", os.Args[0])
	}

	// Parse all flags first
	flag.Parse()
	args := flag.Args()

	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	command := args[0]
	switch command {
	case "serve":
		runServe(global, args[1:])
	case "prompt":
		runPrompt(global, args[1:])
	case "list":
		runList(global, args[1:])
	case "inspect":
		runInspect(global, args[1:])
	case "models":
		runModels(global, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}
}

func runServe(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.String("port", "9000", "Port to listen on")
	fs.Parse(args)

	// Create log buffer first
	logBuffer := server.NewLogBuffer(1000) // Keep last 1000 log entries
	logger := setupLoggingWithBuffer(global.Debug, logBuffer)

	database := setupDatabase(global.DBPath, logger)
	defer database.Close()

	logger.Info("Starting Shelley", "port", *port, "db", global.DBPath)

	// Initialize LLM service manager (no auto-detection)
	llmManager := server.NewLLMServiceManager(logger)

	tools := setupTools(llmManager)

	// Create and start server
	svr := server.NewServer(database, llmManager, tools, logger, logBuffer, global.PredictableOnly)
	if err := svr.Start(*port); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func runPrompt(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("prompt", flag.ExitOnError)
	continueID := fs.String("continue", "", "Continue existing conversation with given ID")
	timeout := fs.Duration("timeout", 30*time.Second, "Timeout for LLM request")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintf(os.Stderr, "Error: prompt text is required\n")
		fs.Usage()
		os.Exit(1)
	}

	promptText := strings.Join(fs.Args(), " ")

	logger := setupLogging(global.Debug)
	database := setupDatabase(global.DBPath, logger)
	defer database.Close()

	// Initialize LLM service for the main conversation
	llmService := setupLLMService(global, logger)

	// Initialize LLM service manager for tools (same as HTTP server)
	llmManager := server.NewLLMServiceManager(logger)
	tools := setupTools(llmManager)
	ctx := context.Background()

	var conversationID string
	var history []llm.Message

	if *continueID != "" {
		// Continue existing conversation - try by ID first, then by slug
		conv, err := database.GetConversationByID(ctx, *continueID)
		if err != nil {
			// Try by slug if ID lookup failed
			conv, err = database.GetConversationBySlug(ctx, *continueID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: conversation not found by ID or slug '%s': %s\n", *continueID, err)
				os.Exit(1)
			}
		}
		conversationID = conv.ConversationID

		// Load message history (use high limit to get all messages)
		messages, err := database.ListMessagesByConversationPaginated(ctx, conversationID, 1000, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading conversation history: %s\n", err)
			os.Exit(1)
		}

		// Convert to LLM messages
		for _, msg := range messages {
			llmMsg, err := convertToLLMMessage(msg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error converting message: %s\n", err)
				os.Exit(1)
			}
			history = append(history, llmMsg)
		}
	} else {
		// Create new conversation
		conversation, err := database.CreateConversation(ctx, nil, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating conversation: %s\n", err)
			os.Exit(1)
		}
		conversationID = conversation.ConversationID
		fmt.Printf("Created conversation: %s\n", conversationID)
	}

	// Set up message recording + console printing
	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		// Persist to DB
		msgType := getMessageType(message)
		if _, err := database.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conversationID,
			Type:           msgType,
			LLMData:        message,
			UserData:       nil,
			UsageData:      usage,
		}); err != nil {
			// Log DB failure but continue to print to console
			fmt.Fprintf(os.Stderr, "Failed to record message: %v\n", err)
		}

		// Print to console as messages occur
		printMessageToConsole(message)
		return nil
	}

	// Generate system prompt
	systemPrompt, err := server.GenerateSystemPrompt()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating system prompt: %s\n", err)
		os.Exit(1)
	}

	system := []llm.SystemContent{}
	if systemPrompt != "" {
		system = []llm.SystemContent{{Type: "text", Text: systemPrompt}}
	}

	// Create loop with configuration
	l := loop.NewLoop(loop.Config{
		LLM:           llmService,
		History:       history,
		Tools:         tools,
		RecordMessage: recordMessage,
		Logger:        logger,
		System:        system,
	})

	// Add the user prompt
	l.QueueUserMessage(llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{{
			Type: llm.ContentTypeText,
			Text: promptText,
		}},
	})

	// Run one complete turn with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	// Process one complete turn (user message + assistant response)
	err = l.ProcessOneTurn(timeoutCtx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running conversation: %s\n", err)
		os.Exit(1)
	}

	// Start slug generation for new conversations in parallel (only if this is not a continuation)
	var slugDone chan struct{}
	if *continueID == "" {
		slugDone = make(chan struct{})

		go func() {
			defer close(slugDone)
			slugCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			_, err := slug.GenerateSlug(slugCtx, llmManager, database, logger, conversationID, promptText)
			if err != nil {
				logger.Warn("Failed to generate slug", "error", err)
			}
		}()
	}

	// Wait for slug generation if it was started
	if slugDone != nil {
		select {
		case <-slugDone:
			// Slug generation completed (successfully or with error)
		case <-time.After(10 * time.Second):
			// Timeout waiting for slug, continue without it
			logger.Debug("Timeout waiting for slug generation")
		}
	}
	// Get conversation details to show continuation info
	conv, err := database.GetConversationByID(ctx, conversationID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not fetch conversation details: %s\n", err)
		fmt.Printf("Conversation completed: %s\n", conversationID)
	} else {
		// Show conversation ID and continuation command
		fmt.Printf("Conversation completed: %s\n", conversationID)
		// Check if slug was generated and use it, otherwise use conversation ID
		if conv.Slug != nil && *conv.Slug != "" {
			fmt.Printf("To continue: shelley prompt -continue %s \"<your message>\"\n", *conv.Slug)
		} else {
			fmt.Printf("To continue: shelley prompt -continue %s \"<your message>\"\n", conversationID)
		}
	}
}

func runList(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	limit := fs.Int64("limit", 20, "Maximum number of conversations to list")
	offset := fs.Int64("offset", 0, "Number of conversations to skip")
	fs.Parse(args)

	logger := setupLogging(global.Debug)
	database := setupDatabase(global.DBPath, logger)
	defer database.Close()

	ctx := context.Background()

	conversations, err := database.ListConversations(ctx, *limit, *offset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing conversations: %s\n", err)
		os.Exit(1)
	}

	if len(conversations) == 0 {
		fmt.Println("No conversations found.")
		return
	}

	fmt.Printf("%-36s %-20s %-20s %s\n", "ID", "Created", "Updated", "Slug")
	fmt.Println(strings.Repeat("-", 100))

	for _, conv := range conversations {
		slug := ""
		if conv.Slug != nil {
			slug = *conv.Slug
		}
		fmt.Printf("%-36s %-20s %-20s %s\n",
			conv.ConversationID,
			conv.CreatedAt.Format("2006-01-02 15:04:05"),
			conv.UpdatedAt.Format("2006-01-02 15:04:05"),
			slug)
	}
}

func runInspect(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	fs.Parse(args)

	if len(fs.Args()) != 1 {
		fmt.Fprintf(os.Stderr, "Error: conversation ID or slug is required\n")
		fs.Usage()
		os.Exit(1)
	}

	conversationIDOrSlug := fs.Args()[0]

	logger := setupLogging(global.Debug)
	database := setupDatabase(global.DBPath, logger)
	defer database.Close()

	ctx := context.Background()

	// Get conversation details - try by ID first, then by slug
	conversation, err := database.GetConversationByID(ctx, conversationIDOrSlug)
	if err != nil {
		// Try by slug if ID lookup failed
		conversation, err = database.GetConversationBySlug(ctx, conversationIDOrSlug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: conversation not found by ID or slug '%s': %s\n", conversationIDOrSlug, err)
			os.Exit(1)
		}
	}

	// Get messages (use high limit to get all messages)
	messages, err := database.ListMessagesByConversationPaginated(ctx, conversation.ConversationID, 1000, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading messages: %s\n", err)
		os.Exit(1)
	}

	// Display conversation info
	fmt.Printf("Conversation ID: %s\n", conversation.ConversationID)
	if conversation.Slug != nil {
		fmt.Printf("Slug: %s\n", *conversation.Slug)
	}
	fmt.Printf("Created: %s\n", conversation.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("Updated: %s\n", conversation.UpdatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("Messages: %d\n\n", len(messages))

	// Display messages
	for i, msg := range messages {
		fmt.Printf("[%d] %s - %s\n", i+1, msg.Type, msg.CreatedAt.Format("15:04:05"))

		// Show content preview from LLM data if available
		if msg.LlmData != nil {
			var llmMsg llm.Message
			if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err == nil {
				content := getMessageContentPreview(llmMsg)
				if len(content) > 200 {
					content = content[:200] + "..."
				}
				// Replace newlines with space for compact display
				content = strings.ReplaceAll(content, "\n", " ")
				fmt.Printf("    %s\n\n", content)
			} else {
				fmt.Printf("    [Error parsing message data]\n\n")
			}
		} else {
			fmt.Printf("    [No message data]\n\n")
		}
	}
}

func setupLogging(debug bool) *slog.Logger {
	logLevel := slog.LevelInfo
	if debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)
	return logger
}

func setupLoggingWithBuffer(debug bool, logBuffer *server.LogBuffer) *slog.Logger {
	logLevel := slog.LevelInfo
	if debug {
		logLevel = slog.LevelDebug
	}

	// Create the base handler
	baseHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})

	// Wrap with buffered handler
	bufferedHandler := server.NewBufferedLogHandler(baseHandler, logBuffer)

	logger := slog.New(bufferedHandler)
	slog.SetDefault(logger)
	return logger
}

func setupDatabase(dbPath string, logger *slog.Logger) *db.DB {
	database, err := db.New(db.Config{DSN: dbPath})
	if err != nil {
		logger.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}

	// Run database migrations
	if err := database.Migrate(context.Background()); err != nil {
		logger.Error("Failed to run database migrations", "error", err)
		os.Exit(1)
	}
	logger.Debug("Database migrations completed successfully")
	return database
}

func setupLLMService(global GlobalConfig, logger *slog.Logger) llm.Service {
	// If predictable-only flag is set, always use predictable service
	if global.PredictableOnly {
		logger.Info("Using specified model", "model", "predictable-only")
		return loop.NewPredictableService()
	}

	// Default model if none provided
	modelID := strings.TrimSpace(global.Model)
	if modelID == "" {
		modelID = "qwen3-coder-fireworks"
	}

	// Check for predictable model
	if modelID == "predictable" {
		logger.Info("Using specified model", "model", modelID)
		return loop.NewPredictableService()
	}

	// Always use the service manager to ensure consistent logging
	llmManager := server.NewLLMServiceManager(logger)
	svc, err := llmManager.GetService(modelID)
	if err != nil {
		// Provide a helpful message with env hints
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Tip: run 'shelley models' to see required env vars.\n")
		os.Exit(1)
	}
	logger.Info("Using specified model", "model", modelID)
	return svc
}

// runModels prints supported models, readiness, and required env variables
func runModels(global GlobalConfig, args []string) {
	logger := setupLogging(global.Debug)
	_ = logger

	type modelInfo struct {
		ID          string
		Provider    string
		EnvVars     []string
		Description string
	}

	models := []modelInfo{
		{ID: "qwen3-coder-fireworks", Provider: "Fireworks", EnvVars: []string{"FIREWORKS_API_KEY"}, Description: "Qwen3 Coder 480B on Fireworks (default)"},
		{ID: "openai-gpt4", Provider: "OpenAI", EnvVars: []string{"OPENAI_API_KEY"}, Description: "GPT-4.1 family"},
		{ID: "openai-gpt4-turbo", Provider: "OpenAI", EnvVars: []string{"OPENAI_API_KEY"}, Description: "GPT-4o family"},
		{ID: "gpt-5-thinking", Provider: "OpenAI", EnvVars: []string{"OPENAI_API_KEY"}, Description: "GPT-5 thinking model (alias: gpt-5)"},
		{ID: "gpt-5-thinking-mini", Provider: "OpenAI", EnvVars: []string{"OPENAI_API_KEY"}, Description: "GPT-5 thinking mini model (alias: gpt-5-mini)"},
		{ID: "gpt-5-thinking-nano", Provider: "OpenAI", EnvVars: []string{"OPENAI_API_KEY"}, Description: "GPT-5 thinking nano model (alias: gpt-5-nano)"},
		{ID: "claude-sonnet-3.5", Provider: "Anthropic", EnvVars: []string{"ANTHROPIC_API_KEY"}, Description: "Claude Sonnet"},
		{ID: "predictable", Provider: "Built-in", EnvVars: []string{}, Description: "Deterministic test model (no API key)"},
	}

	fmt.Println("Supported models:")
	for _, m := range models {
		ready := true
		missing := []string{}
		for _, env := range m.EnvVars {
			if os.Getenv(env) == "" {
				ready = false
				missing = append(missing, env)
			}
		}
		status := "ready"
		if !ready {
			status = "not ready"
		}
		fmt.Printf("- %s [%s] - %s\n", m.ID, m.Provider, status)
		if m.Description != "" {
			fmt.Printf("  %s\n", m.Description)
		}
		if len(m.EnvVars) > 0 {
			fmt.Printf("  Required env: %s\n", strings.Join(m.EnvVars, ", "))
			if len(missing) > 0 {
				fmt.Printf("  Missing: %s\n", strings.Join(missing, ", "))
			}
		} else {
			fmt.Printf("  Required env: none\n")
		}
	}
}

func setupTools(llmProvider claudetool.LLMServiceProvider) []*llm.Tool {
	wd, err := os.Getwd()
	if err != nil {
		// Fallback to "/" if we can't get working directory
		wd = "/"
	}
	bashTool := &claudetool.BashTool{
		Pwd:              wd,
		LLMProvider:      llmProvider,
		EnableJITInstall: claudetool.EnableBashToolJITInstall,
	}
	patchTool := &claudetool.PatchTool{}
	keywordTool := claudetool.NewKeywordTool(llmProvider)

	return []*llm.Tool{
		claudetool.Think,
		bashTool.Tool(),
		patchTool.Tool(),
		keywordTool.Tool(),
	}
}

// Helper functions to convert between message formats
func convertToLLMMessage(msg generated.Message) (llm.Message, error) {
	if msg.LlmData == nil {
		return llm.Message{}, fmt.Errorf("message has no LLM data")
	}
	var llmMsg llm.Message
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return llm.Message{}, fmt.Errorf("failed to unmarshal LLM data: %w", err)
	}
	return llmMsg, nil
}

func getMessageType(message llm.Message) db.MessageType {
	switch message.Role {
	case llm.MessageRoleUser:
		return db.MessageTypeUser
	case llm.MessageRoleAssistant:
		return db.MessageTypeAgent
	default:
		// For tool messages, check content
		for _, content := range message.Content {
			if content.Type == llm.ContentTypeToolUse || content.Type == llm.ContentTypeToolResult {
				return db.MessageTypeTool
			}
		}
		return db.MessageTypeAgent
	}
}

func getMessageContentPreview(message llm.Message) string {
	var content strings.Builder
	for _, c := range message.Content {
		switch c.Type {
		case llm.ContentTypeText:
			content.WriteString(c.Text)
		case llm.ContentTypeToolUse:
			content.WriteString(fmt.Sprintf("[Tool: %s]", c.ToolName))
		case llm.ContentTypeToolResult:
			content.WriteString("[Tool Result]")
		}
	}
	return content.String()
}

// printMessageToConsole prints a readable representation of a message to stdout, as it occurs.
func printMessageToConsole(message llm.Message) {
	// Determine emoji prefix based on message role and content
	var emojiPrefix string
	if message.Role == llm.MessageRoleAssistant {
		emojiPrefix = assistantEmoji
	} else if message.Role == llm.MessageRoleUser {
		// Distinguish between actual user input vs tool results (which are sent back as a user message)
		hasToolResult := false
		for _, c := range message.Content {
			if c.Type == llm.ContentTypeToolResult {
				hasToolResult = true
				break
			}
		}
		if hasToolResult {
			emojiPrefix = toolEmoji
		} else {
			emojiPrefix = userEmoji
		}
	} else {
		emojiPrefix = "" // Default for other message types
	}

	// Build output lines
	var lines []string
	for _, c := range message.Content {
		switch c.Type {
		case llm.ContentTypeText:
			if strings.TrimSpace(c.Text) != "" {
				lines = append(lines, c.Text)
			}
		case llm.ContentTypeToolUse:
			// Show a concise tool call line
			input := strings.TrimSpace(string(c.ToolInput))
			if len(input) > 200 {
				input = input[:200] + "..."
			}
			lines = append(lines, fmt.Sprintf("-> Tool call: %s %s", c.ToolName, input))
		case llm.ContentTypeToolResult:
			// Show a concise tool result line
			var parts []string
			for _, tr := range c.ToolResult {
				if tr.Type == llm.ContentTypeText && strings.TrimSpace(tr.Text) != "" {
					parts = append(parts, tr.Text)
				}
			}
			text := strings.Join(parts, "\n")
			if text == "" {
				if c.ToolError {
					text = "[error]"
				} else {
					text = "[ok]"
				}
			}
			lines = append(lines, fmt.Sprintf("<- Tool result%s\n%s", func() string {
				if c.ToolError {
					return " (error)"
				}
				return ""
			}(), text))
		case llm.ContentTypeThinking:
			// Skip hidden thinking by default
			continue
		default:
			// Fallback for unknown content types - show type and any available content
			var parts []string
			parts = append(parts, fmt.Sprintf("[%s]", c.Type.String()))

			// Show text content if available
			if c.Text != "" {
				parts = append(parts, c.Text)
			}

			// Show media content info if available
			if c.MediaType != "" {
				if c.MediaType == "image/jpeg" || c.MediaType == "image/png" {
					parts = append(parts, fmt.Sprintf("[Image: %s]", c.MediaType))
				} else {
					parts = append(parts, fmt.Sprintf("[Media: %s]", c.MediaType))
				}
			}

			// Show tool data if this is a tool-related unknown type
			if c.ToolName != "" {
				parts = append(parts, fmt.Sprintf("Tool: %s", c.ToolName))
			}

			lines = append(lines, strings.Join(parts, " "))
		}
	}

	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if out == "" {
		out = "(no content)"
	}
	// Print with emoji prefix and timestamp
	timestamp := time.Now().Format("15:04:05")
	fmt.Printf("%s [%s] %s\n\n", emojiPrefix, timestamp, out)
}
