// Package client implements the experimental Shelley CLI client.
// It communicates with a running Shelley server over a Unix socket or HTTP.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSocketPath returns the default Unix socket path (~/.config/shelley/shelley.sock).
func DefaultSocketPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "shelley", "shelley.sock")
}

func defaultClientURL() string {
	return "unix://" + DefaultSocketPath()
}

func parseClientURL(rawURL string) (scheme, address string, err error) {
	if strings.HasPrefix(rawURL, "unix://") {
		sockPath := strings.TrimPrefix(rawURL, "unix://")
		if sockPath == "" {
			return "", "", fmt.Errorf("unix:// URL must include a socket path")
		}
		return "unix", sockPath, nil
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return strings.SplitN(rawURL, "://", 2)[0], rawURL, nil
	}
	return "", "", fmt.Errorf("unsupported URL scheme: %s (use unix://, http://, or https://)", rawURL)
}

type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ", ") }

func (f *multiFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type clientConfig struct {
	serverURL string
	headers   map[string]string
}

func (cc *clientConfig) newHTTPClient() (*http.Client, string, error) {
	scheme, address, err := parseClientURL(cc.serverURL)
	if err != nil {
		return nil, "", err
	}

	switch scheme {
	case "unix":
		transport := &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", address)
			},
		}
		return &http.Client{Transport: transport}, "http://localhost", nil
	case "http", "https":
		return &http.Client{}, address, nil
	default:
		return nil, "", fmt.Errorf("unsupported scheme: %s", scheme)
	}
}

func (cc *clientConfig) newRequest(method, url string, body *strings.Reader) (*http.Request, error) {
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, url, body)
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("X-Shelley-Request", "1")
	}
	for k, v := range cc.headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Run is the entry point for "shelley client [args...]".
func Run(args []string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	urlFlag := fs.String("url", defaultClientURL(), "Server URL (unix:///path, http://host:port, https://host:port)")
	var headerFlags multiFlag
	fs.Var(&headerFlags, "H", `Extra HTTP header ("Name: Value", can be repeated)`)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "EXPERIMENTAL: Shelley CLI client\n\n")
		fmt.Fprintf(fs.Output(), "Usage: shelley client [flags] <subcommand> [args...]\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nSubcommands:\n")
		fmt.Fprintf(fs.Output(), "  chat     Send a message (new or existing conversation)\n")
		fmt.Fprintf(fs.Output(), "  read     Read conversation messages\n")
		fmt.Fprintf(fs.Output(), "  list     List conversations\n")
		fmt.Fprintf(fs.Output(), "  archive  Archive a conversation\n")
		fmt.Fprintf(fs.Output(), "  help     Print detailed help\n")
	}
	fs.Parse(args)

	headers := make(map[string]string)
	for _, h := range headerFlags {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid header %q (expected \"Name: Value\")\n", h)
			os.Exit(1)
		}
		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	cc := &clientConfig{serverURL: *urlFlag, headers: headers}

	subArgs := fs.Args()
	if len(subArgs) == 0 {
		fs.Usage()
		os.Exit(1)
	}

	switch subArgs[0] {
	case "chat":
		cmdChat(cc, subArgs[1:])
	case "read":
		cmdRead(cc, subArgs[1:])
	case "list":
		cmdList(cc, subArgs[1:])
	case "archive":
		cmdArchive(cc, subArgs[1:])
	case "help":
		cmdHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subArgs[0])
		fs.Usage()
		os.Exit(1)
	}
}

func cmdChat(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client chat", flag.ExitOnError)
	prompt := fs.String("p", "", "Message to send (required)")
	convID := fs.String("c", "", "Conversation ID to continue (creates new if omitted)")
	model := fs.String("model", "", "Model to use (server default if empty)")
	cwd := fs.String("cwd", "", "Working directory for the conversation")
	fs.Parse(args)

	if *prompt == "" {
		fmt.Fprintf(os.Stderr, "Error: -p PROMPT is required\n")
		os.Exit(1)
	}

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	reqBody := map[string]string{"message": *prompt}
	if *model != "" {
		reqBody["model"] = *model
	}
	if *cwd != "" {
		reqBody["cwd"] = *cwd
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var apiURL string
	if *convID != "" {
		apiURL = baseURL + "/api/conversation/" + *convID + "/chat"
	} else {
		apiURL = baseURL + "/api/conversations/new"
	}

	req, err := cc.newRequest("POST", apiURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var errBody map[string]any
		if json.NewDecoder(resp.Body).Decode(&errBody) == nil {
			fmt.Fprintf(os.Stderr, "Error (HTTP %d): %v\n", resp.StatusCode, errBody)
		} else {
			fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		}
		os.Exit(1)
	}

	var respBody map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	cid := respBody["conversation_id"]
	if cid == nil {
		cid = *convID // when continuing, the chat endpoint doesn't echo the ID back
	}
	output := map[string]any{
		"conversation_id": cid,
	}
	if slug, ok := respBody["slug"]; ok {
		output["slug"] = slug
	}

	json.NewEncoder(os.Stdout).Encode(output)
}

// streamEvent is the simplified output format for read.
type streamEvent struct {
	SequenceID int64  `json:"sequence_id"`
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	EndOfTurn  bool   `json:"end_of_turn"`
}

func cmdRead(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client read", flag.ExitOnError)
	wait := fs.Bool("wait", false, "Wait for agent turn to finish (stream new messages)")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Usage: shelley client read [-wait] CONVERSATION_ID\n")
		os.Exit(1)
	}
	conversationID := fs.Arg(0)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *wait {
		readStream(cc, client, baseURL, conversationID)
	} else {
		readSnapshot(cc, client, baseURL, conversationID)
	}
}

func readSnapshot(cc *clientConfig, client *http.Client, baseURL, conversationID string) {
	req, err := cc.newRequest("GET", baseURL+"/api/conversation/"+conversationID, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var sr streamResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	for _, msg := range sr.Messages {
		json.NewEncoder(os.Stdout).Encode(simplifyMessage(msg))
	}
}

func readStream(cc *clientConfig, client *http.Client, baseURL, conversationID string) {
	req, err := cc.newRequest("GET", baseURL+"/api/conversation/"+conversationID+"/stream", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	seenSeqIDs := make(map[int64]bool)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var sr streamResponseWire
		if err := json.Unmarshal([]byte(data), &sr); err != nil {
			continue
		}

		if sr.Heartbeat || len(sr.Messages) == 0 {
			continue
		}

		for _, msg := range sr.Messages {
			if seenSeqIDs[msg.SequenceID] {
				continue
			}
			seenSeqIDs[msg.SequenceID] = true

			event := simplifyMessage(msg)
			json.NewEncoder(os.Stdout).Encode(event)

			if (msg.Type == "agent" || msg.Type == "error") && event.EndOfTurn {
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading stream: %v\n", err)
		os.Exit(1)
	}
}

func cmdList(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client list", flag.ExitOnError)
	archived := fs.Bool("archived", false, "List archived conversations instead")
	limit := fs.Int("limit", 50, "Maximum number of conversations to return")
	query := fs.String("q", "", "Search query")
	fs.Parse(args)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	endpoint := "/api/conversations"
	if *archived {
		endpoint = "/api/conversations/archived"
	}

	params := fmt.Sprintf("?limit=%d", *limit)
	if *query != "" {
		params += "&q=" + url.QueryEscape(*query)
	}

	req, err := cc.newRequest("GET", baseURL+endpoint+params, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var conversations []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&conversations); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	for _, conv := range conversations {
		var c struct {
			ConversationID string  `json:"conversation_id"`
			Slug           *string `json:"slug"`
			CreatedAt      string  `json:"created_at"`
			UpdatedAt      string  `json:"updated_at"`
			Working        bool    `json:"working"`
			Model          *string `json:"model"`
		}
		if json.Unmarshal(conv, &c) == nil {
			json.NewEncoder(os.Stdout).Encode(c)
		}
	}
}

func cmdArchive(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client archive", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Usage: shelley client archive CONVERSATION_ID\n")
		os.Exit(1)
	}
	conversationID := fs.Arg(0)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	req, err := cc.newRequest("POST", baseURL+"/api/conversation/"+conversationID+"/archive", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Archived %s\n", conversationID)
}

// --- Wire types for JSON parsing ---

type streamResponseWire struct {
	Messages  []messageWire `json:"messages"`
	Heartbeat bool          `json:"heartbeat"`
}

type messageWire struct {
	SequenceID int64   `json:"sequence_id"`
	Type       string  `json:"type"`
	LlmData    *string `json:"llm_data,omitempty"`
	EndOfTurn  *bool   `json:"end_of_turn,omitempty"`
}

type llmMessageWire struct {
	Content []llmContentWire `json:"Content"`
}

type llmContentWire struct {
	Type     int    `json:"Type"`
	Text     string `json:"Text,omitempty"`
	ToolName string `json:"ToolName,omitempty"`
}

// Content type constants matching llm.ContentType iota values from llm/llm.go.
const (
	contentTypeText       = 2
	contentTypeToolUse    = 5
	contentTypeToolResult = 6
)

func simplifyMessage(msg messageWire) streamEvent {
	event := streamEvent{
		SequenceID: msg.SequenceID,
		Type:       msg.Type,
	}

	if msg.EndOfTurn != nil {
		event.EndOfTurn = *msg.EndOfTurn
	}

	if msg.LlmData == nil {
		return event
	}

	var llmMsg llmMessageWire
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return event
	}

	var texts []string
	for _, c := range llmMsg.Content {
		switch c.Type {
		case contentTypeText:
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		case contentTypeToolUse:
			if event.ToolName == "" && c.ToolName != "" {
				event.ToolName = c.ToolName
			}
		case contentTypeToolResult:
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		}
	}
	event.Text = strings.Join(texts, "\n")

	return event
}

func cmdHelp() {
	fmt.Printf(`EXPERIMENTAL: Shelley CLI Client

Usage:
  shelley client [flags] <subcommand> [args...]

Flags:
  -url URL     Server URL (default: unix://%s)
  -H HEADER    Extra HTTP header "Name: Value" (can be repeated)

Subcommands:
  chat -p PROMPT [-c CONVERSATION_ID] [-model MODEL] [-cwd DIR]
      Send a message. Creates a new conversation unless -c is given.
      Prints JSON with conversation_id to stdout.

  read [-wait] CONVERSATION_ID
      Read all messages in a conversation as JSON lines.
      With -wait, streams via SSE until the agent turn ends.

  list [-archived] [-limit N] [-q QUERY]
      List conversations as JSON lines.

  archive CONVERSATION_ID
      Archive a conversation.

  help
      Print this help text.

Connecting over HTTP with auth headers:
  shelley client -url http://localhost:9999 -H "X-Exedev-Userid: user" list

Examples:
  # Start a conversation and wait for the agent
  ID=$(shelley client chat -p "list files" | jq -r .conversation_id)
  shelley client read -wait "$ID"

  # Continue a conversation
  shelley client chat -c "$ID" -p "now count them"

  # Read current state
  shelley client read "$ID"

NOTE: This feature is EXPERIMENTAL and may change without notice.
`, DefaultSocketPath())
}
