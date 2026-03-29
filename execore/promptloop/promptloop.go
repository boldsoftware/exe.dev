// Package promptloop implements a multi-turn agentic conversation loop
// that runs in the SSH session. It calls the Anthropic API directly and
// provides the model with exe.dev command tools plus the ability to suggest
// commands for the user to approve.
package promptloop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// --- Anthropic API types ---

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
)

type ContentBlock struct {
	Type      ContentType `json:"type"`
	Text      string      `json:"text,omitempty"`
	ID        string      `json:"id,omitempty"`          // for tool_use
	Name      string      `json:"name,omitempty"`        // for tool_use
	Input     any         `json:"input,omitempty"`       // for tool_use
	ToolUseID string      `json:"tool_use_id,omitempty"` // for tool_result
	Content   any         `json:"content,omitempty"`     // for tool_result (string or blocks)
	IsError   bool        `json:"is_error,omitempty"`    // for tool_result
}

type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

type ToolInputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

type Property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema ToolInputSchema `json:"input_schema"`
}

type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Response struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       Role           `json:"role"`
	Content    []ContentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// --- Model interface for testability ---

// Model sends a request and returns a response. Implementations include
// the real Anthropic API client and a fake for testing.
type Model interface {
	SendMessage(ctx context.Context, req *Request) (*Response, error)
}

// AnthropicModel calls the Anthropic Messages API.
type AnthropicModel struct {
	APIKey     string
	BaseURL    string // defaults to "https://api.anthropic.com"
	HTTPClient *http.Client
}

func (m *AnthropicModel) SendMessage(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	baseURL := m.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", m.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := m.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseSize = 10 << 20 // 10 MiB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var apiResp Response
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &apiResp, nil
}

// --- Command dispatch ---

// CommandDispatcher executes exe.dev lobby commands. The real implementation
// dispatches through the SSH command tree. Tests can substitute a fake.
type CommandDispatcher interface {
	// Dispatch executes an exe.dev command and returns its output and exit code.
	// The command is a string like "ls", "ls -l", etc.
	Dispatch(ctx context.Context, command string) (output string, exitCode int)
}

// --- Output interface ---

// Output is where the loop writes visible text and prompts for user input.
type Output interface {
	// WriteText writes assistant text to the terminal.
	WriteText(text string)
	// WriteToolCall indicates a tool is being called.
	WriteToolCall(name, input string)
	// WriteToolResult shows the (possibly truncated) result of a tool call.
	WriteToolResult(name, result string, isError bool)
	// PromptUser asks the user a question and returns their response.
	PromptUser(prompt string) (string, error)
	// WriteStatus writes a status line (thinking, etc).
	WriteStatus(text string)
}

// --- The loop ---

const (
	defaultModel     = "claude-opus-4-6"
	defaultMaxTokens = 8192
	maxTurns         = 50
)

var tools = []Tool{
	{
		Name:        "exe_command",
		Description: "Execute a read-only exe.dev command. Available commands include: ls (list VMs), help (list all commands), help <command> (command details), whoami, ssh-key list, and more. Use 'help' to discover commands. This tool is for read-only commands only; use suggest_command for anything that modifies state.",
		InputSchema: ToolInputSchema{
			Type: "object",
			Properties: map[string]Property{
				"command": {
					Type:        "string",
					Description: "The exe.dev command to execute.",
				},
			},
			Required: []string{"command"},
		},
	},
	{
		Name:        "suggest_command",
		Description: "Suggest an exe.dev command for the user to run. Use for commands that create, delete, or modify resources: new, rm, restart, rename, cp, resize, etc. The user must approve before execution.",
		InputSchema: ToolInputSchema{
			Type: "object",
			Properties: map[string]Property{
				"command": {
					Type:        "string",
					Description: "The exe.dev command to suggest to the user.",
				},
				"explanation": {
					Type:        "string",
					Description: "Brief explanation of what this command does and why.",
				},
			},
			Required: []string{"command", "explanation"},
		},
	},
}

// Config configures the agentic loop.
type Config struct {
	Model        Model
	Dispatcher   CommandDispatcher
	Output       Output
	SystemPrompt string
	ModelName    string // e.g. "claude-opus-4.6"
	MaxTokens    int
}

// Run starts the agentic loop with an initial user prompt. It runs multiple
// turns, calling tools as needed, until the model stops or maxTurns is reached.
// After each assistant turn that ends without tool calls, it prompts the user
// for follow-up input. An empty input ends the conversation.
func Run(ctx context.Context, cfg Config, initialPrompt string) error {
	if cfg.ModelName == "" {
		cfg.ModelName = defaultModel
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = "You are an expert assistant for exe.dev, a cloud VM service. You help users manage their VMs, debug issues, and find information. Be concise and helpful. Use the exe_command tool to run read-only exe.dev commands (ls, help, whoami, etc). Use suggest_command for commands that modify state (new, rm, restart, cp, rename, resize)."
	}

	messages := []Message{
		{
			Role: RoleUser,
			Content: []ContentBlock{
				{Type: ContentTypeText, Text: initialPrompt},
			},
		},
	}

	for turn := 0; turn < maxTurns; turn++ {
		cfg.Output.WriteStatus("thinking...")

		req := &Request{
			Model:     cfg.ModelName,
			MaxTokens: cfg.MaxTokens,
			System:    cfg.SystemPrompt,
			Messages:  messages,
			Tools:     tools,
		}

		resp, err := cfg.Model.SendMessage(ctx, req)
		if err != nil {
			return fmt.Errorf("model error on turn %d: %w", turn, err)
		}

		// Add assistant response to history
		messages = append(messages, Message{
			Role:    RoleAssistant,
			Content: resp.Content,
		})

		// Display any text blocks
		for _, block := range resp.Content {
			if block.Type == ContentTypeText && block.Text != "" {
				cfg.Output.WriteText(block.Text)
			}
		}

		// If no tool use, the model is done with this turn.
		// Prompt user for follow-up.
		if resp.StopReason == "end_turn" || !hasToolUse(resp.Content) {
			userInput, err := cfg.Output.PromptUser("\n> ")
			if err != nil {
				return nil // EOF or error reading input ends the loop
			}
			userInput = strings.TrimSpace(userInput)
			if userInput == "" {
				return nil // empty input ends conversation
			}
			messages = append(messages, Message{
				Role: RoleUser,
				Content: []ContentBlock{
					{Type: ContentTypeText, Text: userInput},
				},
			})
			continue
		}

		// Process tool calls
		toolResults := processToolCalls(ctx, cfg, resp.Content)
		messages = append(messages, Message{
			Role:    RoleUser,
			Content: toolResults,
		})
	}

	return fmt.Errorf("conversation exceeded %d turns", maxTurns)
}

func hasToolUse(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == ContentTypeToolUse {
			return true
		}
	}
	return false
}

// readOnlyCommands is the allowlist of commands that exe_command can run
// without user confirmation. All other commands must go through suggest_command.
// readOnlyCommands lists commands allowed via exe_command without confirmation.
// Entries can be a single word ("ls") or "command subcommand" ("integrations list").
var readOnlyCommands = map[string]bool{
	"ls":                true,
	"help":              true,
	"whoami":            true,
	"doc":               true,
	"integrations list": true,
}

// isReadOnlyCommand checks whether a command matches the read-only allowlist.
func isReadOnlyCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	// Check "command subcommand" first, then just "command".
	if len(fields) >= 2 && readOnlyCommands[fields[0]+" "+fields[1]] {
		return true
	}
	return readOnlyCommands[fields[0]]
}

func parseToolInput(block ContentBlock) (map[string]string, error) {
	inputBytes, err := json.Marshal(block.Input)
	if err != nil {
		return nil, fmt.Errorf("marshal tool input: %w", err)
	}
	var input map[string]string
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		return nil, fmt.Errorf("unmarshal tool input: %w", err)
	}
	return input, nil
}

func processToolCalls(ctx context.Context, cfg Config, blocks []ContentBlock) []ContentBlock {
	var results []ContentBlock
	for _, block := range blocks {
		if block.Type != ContentTypeToolUse {
			continue
		}

		input, err := parseToolInput(block)
		if err != nil {
			results = append(results, ContentBlock{
				Type:      ContentTypeToolResult,
				ToolUseID: block.ID,
				Content:   fmt.Sprintf("Failed to parse tool input: %v", err),
				IsError:   true,
			})
			continue
		}

		var result string
		var isError bool

		switch block.Name {
		case "exe_command":
			cmd := input["command"]
			if cmd == "" {
				result = "Empty command. Use 'help' to see available commands."
				isError = true
				cfg.Output.WriteToolResult("exe_command", result, isError)
			} else if !isReadOnlyCommand(cmd) {
				result = fmt.Sprintf("Command %q is not read-only. Use suggest_command instead so the user can approve it.", cmd)
				isError = true
				cfg.Output.WriteToolResult("exe_command", result, isError)
			} else {
				cfg.Output.WriteToolCall("exe_command", cmd)
				out, exitCode := cfg.Dispatcher.Dispatch(ctx, cmd)
				result = truncateOutput(out, 10000)
				if exitCode != 0 {
					isError = true
				}
				cfg.Output.WriteToolResult("exe_command", result, isError)
			}

		case "suggest_command":
			cmd := input["command"]
			explanation := input["explanation"]
			cfg.Output.WriteToolCall("suggest_command", cmd)
			prompt := fmt.Sprintf("Run this command?\n  %s\n  (%s)\n[y/N] ", cmd, explanation)
			answer, err := cfg.Output.PromptUser(prompt)
			if err != nil {
				result = "User declined (input error)."
				isError = true
			} else {
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer == "y" || answer == "yes" {
					out, exitCode := cfg.Dispatcher.Dispatch(ctx, cmd)
					if exitCode != 0 {
						result = fmt.Sprintf("Command failed (exit code %d):\n%s", exitCode, truncateOutput(out, 10000))
						isError = true
					} else {
						result = fmt.Sprintf("Command executed successfully.\nOutput:\n%s", truncateOutput(out, 10000))
					}
				} else {
					result = "User declined to run this command."
				}
			}
			cfg.Output.WriteToolResult("suggest_command", result, isError)

		default:
			result = fmt.Sprintf("Unknown tool: %s", block.Name)
			isError = true
		}

		results = append(results, ContentBlock{
			Type:      ContentTypeToolResult,
			ToolUseID: block.ID,
			Content:   result,
			IsError:   isError,
		})
	}
	return results
}

func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Truncate at a valid UTF-8 boundary.
	truncated := s[:maxLen]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "\n... (truncated)"
}
