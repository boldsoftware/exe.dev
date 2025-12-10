package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
	"shelley.exe.dev/server"
	"shelley.exe.dev/templates"
	"shelley.exe.dev/version"
)

type GlobalConfig struct {
	DBPath          string
	Debug           bool
	Model           string
	PredictableOnly bool
	ConfigPath      string
	TerminalURL     string
	DefaultModel    string
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
	flag.StringVar(&global.ConfigPath, "config", "", "Path to shelley.json configuration file (optional)")
	flag.StringVar(&global.DefaultModel, "default-model", "claude-sonnet-4.5", "Default model for web UI (default: claude-sonnet-4.5)")

	// Custom usage function
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [global-flags] <command> [command-flags]\n\n", os.Args[0])
		fmt.Fprintf(flag.CommandLine.Output(), "Global flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nCommands:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  serve [flags]                 Start the web server\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  models                        List supported models and env requirements\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  unpack-template <name> <dir>  Unpack a project template to a directory\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  version                       Print version information as JSON\n")
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
	case "models":
		runModels(global, args[1:])
	case "unpack-template":
		runUnpackTemplate(args[1:])
	case "version":
		runVersion()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		flag.Usage()
		os.Exit(1)
	}
}

func runServe(global GlobalConfig, args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.String("port", "9000", "Port to listen on")
	systemdActivation := fs.Bool("systemd-activation", false, "Use systemd socket activation (listen on fd from systemd)")
	fs.Parse(args)

	logger := setupLogging(global.Debug)

	database := setupDatabase(global.DBPath, logger)
	defer database.Close()

	// Build LLM configuration
	llmConfig := buildLLMConfig(logger, global.ConfigPath, global.TerminalURL, global.DefaultModel)

	// Create request history for debugging
	llmHistory := models.NewLLMRequestHistory(10)

	// Initialize LLM service manager
	llmManager := server.NewLLMServiceManager(llmConfig, llmHistory)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tools, toolsCleanup := setupTools(ctx, llmManager)
	defer toolsCleanup()

	// Create server
	svr := server.NewServer(database, llmManager, tools, logger, global.PredictableOnly, llmConfig.TerminalURL, llmConfig.DefaultModel, llmConfig.Links)

	var err error
	if *systemdActivation {
		listener, listenerErr := systemdListener()
		if listenerErr != nil {
			logger.Error("Failed to get systemd listener", "error", listenerErr)
			os.Exit(1)
		}
		logger.Info("Using systemd socket activation")
		err = svr.StartWithListener(listener)
	} else {
		err = svr.Start(*port)
	}

	if err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
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

	// Build LLM configuration
	llmConfig := buildLLMConfig(logger, global.ConfigPath, global.TerminalURL, global.DefaultModel)

	// Always use the service manager to ensure consistent logging
	// For CLI usage, we don't need request history tracking
	llmManager := server.NewLLMServiceManager(llmConfig, nil)
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

	fmt.Println("Supported models:")
	for _, m := range models.All() {
		ready := true
		missing := []string{}
		for _, env := range m.RequiredEnvVars {
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
		if len(m.RequiredEnvVars) > 0 {
			fmt.Printf("  Required env: %s\n", strings.Join(m.RequiredEnvVars, ", "))
			if len(missing) > 0 {
				fmt.Printf("  Missing: %s\n", strings.Join(missing, ", "))
			}
		} else {
			fmt.Printf("  Required env: none\n")
		}
	}
}

// runUnpackTemplate unpacks a project template to a directory
func runUnpackTemplate(args []string) {
	fs := flag.NewFlagSet("unpack-template", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: shelley unpack-template <template-name> <directory>\n\n")
		fmt.Fprintf(fs.Output(), "Unpacks a project template to the specified directory.\n\n")
		fmt.Fprintf(fs.Output(), "Available templates:\n")
		names, err := templates.List()
		if err != nil {
			fmt.Fprintf(fs.Output(), "  (error listing templates: %v)\n", err)
		} else if len(names) == 0 {
			fmt.Fprintf(fs.Output(), "  (no templates available)\n")
		} else {
			for _, name := range names {
				fmt.Fprintf(fs.Output(), "  %s\n", name)
			}
		}
	}
	fs.Parse(args)

	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}

	templateName := fs.Arg(0)
	destDir := fs.Arg(1)

	// Verify template exists
	names, err := templates.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing templates: %v\n", err)
		os.Exit(1)
	}
	found := false
	for _, name := range names {
		if name == templateName {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "Error: template %q not found\n", templateName)
		fmt.Fprintf(os.Stderr, "Available templates: %s\n", strings.Join(names, ", "))
		os.Exit(1)
	}

	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory %q: %v\n", destDir, err)
		os.Exit(1)
	}

	// Unpack the template
	if err := templates.Unpack(templateName, destDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error unpacking template: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Template %q unpacked to %s\n", templateName, destDir)
}

// runVersion prints version information as JSON
func runVersion() {
	info := version.GetInfo()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding version: %v\n", err)
		os.Exit(1)
	}
}

func setupTools(ctx context.Context, llmProvider claudetool.LLMServiceProvider) ([]*llm.Tool, func()) {
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
	patchTool := &claudetool.PatchTool{
		Simplified:       true,
		Pwd:              wd,
		ClipboardEnabled: true,
	}
	keywordTool := claudetool.NewKeywordTool(llmProvider)

	tools := []*llm.Tool{
		claudetool.Think,
		bashTool.Tool(),
		patchTool.Tool(),
		keywordTool.Tool(),
	}

	browserTools, browserCleanup := browse.RegisterBrowserTools(ctx, true)
	if len(browserTools) > 0 {
		tools = append(tools, browserTools...)
	}

	cleanups := make([]func(), 0, 1)
	if browserCleanup != nil {
		cleanups = append(cleanups, browserCleanup)
	}

	return tools, func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cleanups[i] != nil {
				cleanups[i]()
			}
		}
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

// buildLLMConfig constructs LLMConfig from environment variables and optional config file
func buildLLMConfig(logger *slog.Logger, configPath, terminalURL, defaultModel string) *server.LLMConfig {
	llmCfg := &server.LLMConfig{
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
		FireworksAPIKey: os.Getenv("FIREWORKS_API_KEY"),
		TerminalURL:     terminalURL,
		DefaultModel:    defaultModel,
		Logger:          logger,
	}

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			if !os.IsNotExist(err) {
				logger.Warn("Failed to read config file", "path", configPath, "error", err)
			}
			return llmCfg
		}

		var cfg struct {
			LLMGateway   string        `json:"llm_gateway"`
			TerminalURL  string        `json:"terminal_url"`
			DefaultModel string        `json:"default_model"`
			Links        []server.Link `json:"links"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			logger.Warn("Failed to parse config file", "path", configPath, "error", err)
			return llmCfg
		}

		if cfg.LLMGateway != "" {
			gateway := strings.TrimSuffix(cfg.LLMGateway, "/")
			llmCfg.Gateway = gateway
			logger.Info("Using LLM gateway", "gateway", gateway)

			// When using a gateway, default all API keys to "implicit" unless otherwise set
			if llmCfg.AnthropicAPIKey == "" {
				llmCfg.AnthropicAPIKey = "implicit"
			}
			if llmCfg.OpenAIAPIKey == "" {
				llmCfg.OpenAIAPIKey = "implicit"
			}
			if llmCfg.GeminiAPIKey == "" {
				llmCfg.GeminiAPIKey = "implicit"
			}
			if llmCfg.FireworksAPIKey == "" {
				llmCfg.FireworksAPIKey = "implicit"
			}
		}

		// Override terminal URL from config file if present and not already set via flag
		if cfg.TerminalURL != "" && llmCfg.TerminalURL == "" {
			llmCfg.TerminalURL = cfg.TerminalURL
			logger.Info("Using terminal URL from config", "url", cfg.TerminalURL)
		}

		// Override default model from config file if present and not already set via flag
		if cfg.DefaultModel != "" && llmCfg.DefaultModel == "" {
			llmCfg.DefaultModel = cfg.DefaultModel
			logger.Info("Using default model from config", "model", cfg.DefaultModel)
		}

		// Load links from config file if present
		if len(cfg.Links) > 0 {
			llmCfg.Links = cfg.Links
			logger.Info("Loaded links from config", "count", len(cfg.Links))
		}
	}

	return llmCfg
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

// systemdListener returns a net.Listener from systemd socket activation.
// Systemd passes file descriptors starting at fd 3, with LISTEN_FDS indicating the count.
func systemdListener() (net.Listener, error) {
	// Check LISTEN_PID matches our PID (optional but recommended)
	pidStr := os.Getenv("LISTEN_PID")
	if pidStr != "" {
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			return nil, fmt.Errorf("invalid LISTEN_PID: %w", err)
		}
		if pid != os.Getpid() {
			return nil, fmt.Errorf("LISTEN_PID %d does not match current PID %d", pid, os.Getpid())
		}
	}

	// Get the number of file descriptors passed
	fdsStr := os.Getenv("LISTEN_FDS")
	if fdsStr == "" {
		return nil, fmt.Errorf("LISTEN_FDS not set; not running under systemd socket activation")
	}
	nfds, err := strconv.Atoi(fdsStr)
	if err != nil {
		return nil, fmt.Errorf("invalid LISTEN_FDS: %w", err)
	}
	if nfds < 1 {
		return nil, fmt.Errorf("LISTEN_FDS=%d; expected at least 1", nfds)
	}

	// Systemd passes file descriptors starting at fd 3
	const listenFDsStart = 3
	fd := listenFDsStart

	// Create a file from the descriptor
	f := os.NewFile(uintptr(fd), "systemd-socket")
	if f == nil {
		return nil, fmt.Errorf("failed to create file from fd %d", fd)
	}

	// Create a listener from the file
	listener, err := net.FileListener(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to create listener from fd %d: %w", fd, err)
	}

	// Close the original file; the listener now owns the descriptor
	f.Close()

	return listener, nil
}
