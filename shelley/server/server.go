package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/util/singleflight"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/ui"
)

// APIMessage is the message format sent to clients
// TODO: We could maybe omit llm_data when display_data is available
type APIMessage struct {
	MessageID      string    `json:"message_id"`
	ConversationID string    `json:"conversation_id"`
	SequenceID     int64     `json:"sequence_id"`
	Type           string    `json:"type"`
	LlmData        *string   `json:"llm_data,omitempty"`
	UserData       *string   `json:"user_data,omitempty"`
	UsageData      *string   `json:"usage_data,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	DisplayData    *string   `json:"display_data,omitempty"`
	EndOfTurn      *bool     `json:"end_of_turn,omitempty"`
}

// StreamResponse represents the response format for conversation streaming
type StreamResponse struct {
	Messages     []APIMessage           `json:"messages"`
	Conversation generated.Conversation `json:"conversation"`
	AgentWorking bool                   `json:"agent_working"`
}

// LLMProvider is an interface for getting LLM services
type LLMProvider interface {
	GetService(modelID string) (llm.Service, error)
	GetAvailableModels() []string
	HasModel(modelID string) bool
}

// NewLLMServiceManager creates a new LLM service manager from config
func NewLLMServiceManager(cfg *LLMConfig, history *models.LLMRequestHistory) LLMProvider {
	// Convert LLMConfig to models.Config
	modelConfig := &models.Config{
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		GeminiAPIKey:    cfg.GeminiAPIKey,
		FireworksAPIKey: cfg.FireworksAPIKey,
		Gateway:         cfg.Gateway,
		Logger:          cfg.Logger,
	}

	manager, err := models.NewManager(modelConfig, history)
	if err != nil {
		// This shouldn't happen in practice, but handle it gracefully
		cfg.Logger.Error("Failed to create models manager", "error", err)
	}

	return manager
}

// toAPIMessages converts database messages to API messages
func toAPIMessages(messages []generated.Message) []APIMessage {
	apiMessages := make([]APIMessage, len(messages))
	for i, msg := range messages {
		var endOfTurnPtr *bool
		if msg.LlmData != nil && msg.Type == string(db.MessageTypeAgent) {
			if endOfTurn, ok := extractEndOfTurn(*msg.LlmData); ok {
				endOfTurnCopy := endOfTurn
				endOfTurnPtr = &endOfTurnCopy
			}
		}

		apiMsg := APIMessage{
			MessageID:      msg.MessageID,
			ConversationID: msg.ConversationID,
			SequenceID:     msg.SequenceID,
			Type:           msg.Type,
			LlmData:        msg.LlmData,
			UserData:       msg.UserData,
			UsageData:      msg.UsageData,
			CreatedAt:      msg.CreatedAt,
			DisplayData:    msg.DisplayData,
			EndOfTurn:      endOfTurnPtr,
		}
		apiMessages[i] = apiMsg
	}
	return apiMessages
}

func extractEndOfTurn(raw string) (bool, bool) {
	var message llm.Message
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		return false, false
	}
	return message.EndOfTurn, true
}

func agentWorking(messages []APIMessage) bool {
	if len(messages) == 0 {
		return false
	}

	last := messages[len(messages)-1]
	if last.Type == string(db.MessageTypeAgent) {
		if last.EndOfTurn == nil {
			return true
		}
		return !*last.EndOfTurn
	}

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Type != string(db.MessageTypeAgent) {
			continue
		}
		if msg.EndOfTurn == nil {
			return true
		}
		if !*msg.EndOfTurn {
			return true
		}
		// Agent ended turn, but newer non-agent messages exist, so agent is working again.
		return true
	}

	// No agent message found yet but conversation has activity, assume agent is working.
	return true
}

// Server manages the HTTP API and active conversations
type Server struct {
	db                  *db.DB
	llmManager          LLMProvider
	tools               []*llm.Tool
	activeConversations map[string]*ConversationManager
	mu                  sync.Mutex
	logger              *slog.Logger
	predictableOnly     bool
	terminalURL         string
	defaultModel        string
	links               []Link
	conversationGroup   singleflight.Group[string, *ConversationManager]
}

// NewServer creates a new server instance
func NewServer(database *db.DB, llmManager LLMProvider, tools []*llm.Tool, logger *slog.Logger, predictableOnly bool, terminalURL, defaultModel string, links []Link) *Server {
	return &Server{
		db:                  database,
		llmManager:          llmManager,
		tools:               tools,
		activeConversations: make(map[string]*ConversationManager),
		logger:              logger,
		predictableOnly:     predictableOnly,
		terminalURL:         terminalURL,
		defaultModel:        defaultModel,
		links:               links,
	}
}

// RegisterRoutes registers HTTP routes on the given mux
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// API routes
	mux.HandleFunc("/api/conversations", s.handleConversations)
	mux.HandleFunc("/api/conversations/new", s.handleNewConversation)
	mux.HandleFunc("/api/conversation/", s.handleConversation)
	mux.HandleFunc("/api/validate-cwd", s.handleValidateCwd)

	// Generic read route restricted to safe paths
	mux.HandleFunc("/api/read", s.handleRead)

	// Debug routes
	mux.HandleFunc("/debug/llm", s.handleDebugLLM)

	// Serve embedded UI assets with conservative caching
	mux.Handle("/", s.staticHandler(ui.Assets()))
}

// handleValidateCwd validates that a path exists and is a directory
func (s *Server) handleValidateCwd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"valid": false,
			"error": "path is required",
		})
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if os.IsNotExist(err) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"valid": false,
				"error": "directory does not exist",
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"valid": false,
				"error": err.Error(),
			})
		}
		return
	}

	if !info.IsDir() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"valid": false,
			"error": "path is not a directory",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"valid": true,
	})
}

// getOrCreateConversationManager gets an existing conversation manager or creates a new one.
func (s *Server) getOrCreateConversationManager(ctx context.Context, conversationID string) (*ConversationManager, error) {
	manager, err, _ := s.conversationGroup.Do(conversationID, func() (*ConversationManager, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if manager, exists := s.activeConversations[conversationID]; exists {
			manager.Touch()
			return manager, nil
		}

		recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
			return s.recordMessage(ctx, conversationID, message, usage)
		}

		manager := NewConversationManager(conversationID, s.db, s.logger, s.tools, recordMessage)
		if err := manager.Hydrate(ctx); err != nil {
			return nil, err
		}

		s.activeConversations[conversationID] = manager
		return manager, nil
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

// ExtractDisplayData extracts display data from message content for storage
func ExtractDisplayData(message llm.Message) interface{} {
	// Build a map of tool_use_id to tool_name for lookups
	toolNameMap := make(map[string]string)
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeToolUse {
			toolNameMap[content.ID] = content.ToolName
		}
	}

	var displayData []any
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeToolResult && content.Display != nil {
			// Include tool name if we can find it
			toolName := toolNameMap[content.ToolUseID]
			displayData = append(displayData, map[string]any{
				"tool_use_id": content.ToolUseID,
				"tool_name":   toolName,
				"display":     content.Display,
			})
		}
	}

	if len(displayData) > 0 {
		return displayData
	}
	return nil
}

// recordMessage records a new message to the database and also notifies subscribers
func (s *Server) recordMessage(ctx context.Context, conversationID string, message llm.Message, usage llm.Usage) error {
	// Log message based on role
	if message.Role == llm.MessageRoleUser {
		s.logger.Info("User message", "conversation_id", conversationID, "content_items", len(message.Content))
	} else if message.Role == llm.MessageRoleAssistant {
		s.logger.Info("Agent message", "conversation_id", conversationID, "content_items", len(message.Content), "end_of_turn", message.EndOfTurn)
	}

	// Convert LLM message to database format
	messageType, err := s.getMessageType(message)
	if err != nil {
		return fmt.Errorf("failed to determine message type: %w", err)
	}

	// Extract display data from content items
	displayDataToStore := ExtractDisplayData(message)

	// Create message
	_, err = s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           messageType,
		LLMData:        message,
		UserData:       nil,
		UsageData:      usage,
		DisplayData:    displayDataToStore,
	})
	if err != nil {
		return fmt.Errorf("failed to create message: %w", err)
	}

	// Update conversation's last updated timestamp for correct ordering
	if err := s.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.UpdateConversationTimestamp(ctx, conversationID)
	}); err != nil {
		s.logger.Warn("Failed to update conversation timestamp", "conversationID", conversationID, "error", err)
	}

	// Touch active manager activity time if present
	s.mu.Lock()
	mgr, ok := s.activeConversations[conversationID]
	if ok {
		mgr.Touch()
	}
	s.mu.Unlock()

	// Notify subscribers
	go s.notifySubscribers(ctx, conversationID)

	return nil
}

// getMessageType determines the message type from an LLM message
func (s *Server) getMessageType(message llm.Message) (db.MessageType, error) {
	switch message.Role {
	case llm.MessageRoleUser:
		return db.MessageTypeUser, nil
	case llm.MessageRoleAssistant:
		// Check if this is an error message by looking at content
		for _, content := range message.Content {
			if content.Type == llm.ContentTypeText && strings.HasPrefix(content.Text, "LLM request failed:") {
				return db.MessageTypeError, nil
			}
		}
		return db.MessageTypeAgent, nil
	default:
		// For tool messages, check if it's a tool call or tool result
		for _, content := range message.Content {
			if content.Type == llm.ContentTypeToolUse {
				return db.MessageTypeTool, nil
			}
			if content.Type == llm.ContentTypeToolResult {
				return db.MessageTypeTool, nil
			}
		}
		return db.MessageTypeAgent, nil
	}
}

// convertToLLMMessage converts a database message to an LLM message
func convertToLLMMessage(msg generated.Message) (llm.Message, error) {
	var llmMsg llm.Message
	if msg.LlmData == nil {
		return llm.Message{}, fmt.Errorf("message has no LLM data")
	}
	if err := json.Unmarshal([]byte(*msg.LlmData), &llmMsg); err != nil {
		return llm.Message{}, fmt.Errorf("failed to unmarshal LLM data: %w", err)
	}
	return llmMsg, nil
}

// notifySubscribers sends updated messages and conversation data to all subscribers
func (s *Server) notifySubscribers(ctx context.Context, conversationID string) {
	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !exists {
		return
	}

	// Get conversation data and all messages
	var conversation generated.Conversation
	var messages []generated.Message
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conversation, err = q.GetConversation(ctx, conversationID)
		if err != nil {
			return err
		}
		messages, err = q.ListMessages(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get conversation data for notification", "conversationID", conversationID, "error", err)
		return
	}

	// Determine the latest sequence ID
	var latestSequenceID int64
	if len(messages) > 0 {
		latestSequenceID = messages[len(messages)-1].SequenceID
	}

	// Publish to all subscribers
	// TODO(philip): We're re-publishing ALL the messages, and that's wrong. We should only be publishing new stuff.
	apiMessages := toAPIMessages(messages)
	streamData := StreamResponse{
		Messages:     apiMessages,
		Conversation: conversation,
		AgentWorking: agentWorking(apiMessages),
	}
	manager.subpub.Publish(latestSequenceID, streamData)
}

// Cleanup removes inactive conversation managers
func (s *Server) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, manager := range s.activeConversations {
		// Remove managers that have been inactive for more than 30 minutes
		manager.mu.Lock()
		lastActivity := manager.lastActivity
		manager.mu.Unlock()
		if now.Sub(lastActivity) > 30*time.Minute {
			manager.stopLoop()
			delete(s.activeConversations, id)
			s.logger.Debug("Cleaned up inactive conversation", "conversationID", id)
		}
	}
}

// Start starts the HTTP server and handles the complete lifecycle
func (s *Server) Start(port string) error {
	// Set up HTTP server with routes and middleware
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	// Add middleware
	handler := LoggerMiddleware(s.logger)(mux)
	handler = CORSMiddleware()(handler)

	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: handler,
	}

	// Start cleanup routine
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.Cleanup()
		}
	}()

	// Create listener to get actual port (important when port is "0")
	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		s.logger.Error("Failed to create listener", "error", err, "port_info", getPortOwnerInfo(port))
		return err
	}

	// Get actual port from listener
	actualPort := listener.Addr().(*net.TCPAddr).Port

	// Start server in goroutine
	serverErrCh := make(chan error, 1)
	go func() {
		s.logger.Info("Server starting", "port", actualPort, "url", fmt.Sprintf("http://localhost:%d", actualPort))
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	// Wait for shutdown signal or server error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrCh:
		s.logger.Error("Server failed", "error", err)
		return err
	case <-quit:
		s.logger.Info("Shutting down server")
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		s.logger.Error("Server forced to shutdown", "error", err)
		return err
	}

	s.logger.Info("Server exited")
	return nil
}

// getPortOwnerInfo tries to identify what process is using a port.
// Returns a human-readable string with the PID and process name, or an error message.
func getPortOwnerInfo(port string) string {
	// Use lsof to find the process using the port
	cmd := exec.Command("lsof", "-i", ":"+port, "-sTCP:LISTEN", "-n", "-P")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("(unable to determine: %v)", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return "(no process found)"
	}

	// Parse lsof output: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
	// Skip the header line
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			command := fields[0]
			pid := fields[1]
			return fmt.Sprintf("pid=%s process=%s", pid, command)
		}
	}

	return "(could not parse lsof output)"
}
