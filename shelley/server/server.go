package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/subpub"
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
}

// StreamResponse represents the response format for conversation streaming
type StreamResponse struct {
	Messages     []APIMessage           `json:"messages"`
	Conversation generated.Conversation `json:"conversation"`
}

// LLMProvider is an interface for getting LLM services
type LLMProvider interface {
	GetService(modelID string) (llm.Service, error)
	GetAvailableModels() []string
	HasModel(modelID string) bool
}

// NewLLMServiceManager creates a new LLM service manager from config
func NewLLMServiceManager(cfg *LLMConfig) LLMProvider {
	// Convert LLMConfig to models.Config
	modelConfig := &models.Config{
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		GeminiAPIKey:    cfg.GeminiAPIKey,
		FireworksAPIKey: cfg.FireworksAPIKey,
		Gateway:         cfg.Gateway,
		Logger:          cfg.Logger,
	}

	manager, err := models.NewManager(modelConfig)
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
		}
		apiMessages[i] = apiMsg
	}
	return apiMessages
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

	// Generic read route restricted to safe paths
	mux.HandleFunc("/api/read", s.handleRead)

	// Serve embedded UI assets with conservative caching
	mux.Handle("/", s.staticHandler(ui.Assets()))
}

// getOrCreateConversationManager gets an existing conversation manager or creates a new one
func (s *Server) getOrCreateConversationManager(ctx context.Context, conversationID string, llmService llm.Service, modelID string) (*ConversationManager, error) {
	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()

	if exists {
		var existingModel string
		manager.mu.Lock()
		manager.lastActivity = time.Now()
		existingModel = manager.modelID
		manager.mu.Unlock()

		if existingModel != "" && modelID != "" && existingModel != modelID {
			return nil, fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}

		if llmService != nil {
			if err := manager.ensureLoop(llmService, modelID); err != nil {
				return nil, err
			}
		}

		return manager, nil
	}

	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("conversation not found: %w", err)
	}

	var messages []generated.Message
	err = s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, conversationID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation history: %w", err)
	}

	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return s.recordMessage(ctx, conversationID, message, usage)
	}

	var history []llm.Message
	var system []llm.SystemContent
	for _, msg := range messages {
		if msg.Type == string(db.MessageTypeSystem) {
			llmMsg, err := s.convertToLLMMessage(msg)
			if err != nil {
				s.logger.Warn("Failed to convert system message to LLM format", "messageID", msg.MessageID, "error", err)
				continue
			}
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeText && content.Text != "" {
					system = append(system, llm.SystemContent{Type: "text", Text: content.Text})
				}
			}
			continue
		}
		llmMsg, err := s.convertToLLMMessage(msg)
		if err != nil {
			s.logger.Warn("Failed to convert message to LLM format", "messageID", msg.MessageID, "error", err)
			continue
		}
		history = append(history, llmMsg)
	}

	if len(system) > 0 {
		systemLen := 0
		for _, sys := range system {
			systemLen += len(sys.Text)
		}
		s.logger.Info("Loaded system prompt from database", "conversationID", conversationID, "system_items", len(system), "total_length", systemLen)
	} else {
		s.logger.Warn("No system prompt found in database", "conversationID", conversationID, "message_count", len(messages))
	}

	manager = &ConversationManager{
		conversationID: conversationID,
		lastActivity:   time.Now(),
		history:        history,
		system:         system,
		recordMessage:  recordMessage,
		logger:         s.logger.With("conversationID", conversationID),
		tools:          append([]*llm.Tool(nil), s.tools...),
		subpub:         subpub.New[StreamResponse](),
	}

	s.mu.Lock()
	s.activeConversations[conversationID] = manager
	s.mu.Unlock()
	_ = conversation // avoid unused variable

	if llmService != nil {
		if err := manager.ensureLoop(llmService, modelID); err != nil {
			s.mu.Lock()
			delete(s.activeConversations, conversationID)
			s.mu.Unlock()
			return nil, err
		}
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

// recordMessage records a new message to the database
func (s *Server) recordMessage(ctx context.Context, conversationID string, message llm.Message, usage llm.Usage) error {
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
	s.mu.Unlock()
	if ok {
		mgr.mu.Lock()
		mgr.lastActivity = time.Now()
		mgr.mu.Unlock()
	}

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
func (s *Server) convertToLLMMessage(msg generated.Message) (llm.Message, error) {
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
	streamData := StreamResponse{
		Messages:     toAPIMessages(messages),
		Conversation: conversation,
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
		s.logger.Error("Failed to create listener", "error", err)
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
