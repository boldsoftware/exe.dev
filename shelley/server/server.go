package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
	"shelley.exe.dev/slug"
	"shelley.exe.dev/ui"
)

// StreamResponse represents the response format for conversation streaming
type StreamResponse struct {
	Messages     []generated.Message    `json:"messages"`
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

// handleLogsStream handles GET /api/logs/stream
func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Send current logs
	currentLogs := s.logBuffer.GetAll()
	data, _ := json.Marshal(currentLogs)
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()

	// Subscribe to new log entries
	subscriptionID := fmt.Sprintf("%d", time.Now().UnixNano())
	updateChan := make(chan LogEntry, 10)
	s.logBuffer.Subscribe(subscriptionID, updateChan)
	defer s.logBuffer.Unsubscribe(subscriptionID)

	// Listen for updates or context cancellation
	for {
		select {
		case <-ctx.Done():
			return
		case entry := <-updateChan:
			data, _ := json.Marshal([]LogEntry{entry})
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
	}
}

// Server manages the HTTP API and active conversations
type Server struct {
	db                  *db.DB
	llmManager          LLMProvider
	tools               []*llm.Tool
	activeConversations map[string]*ConversationManager
	mu                  sync.RWMutex
	logger              *slog.Logger
	logBuffer           *LogBuffer
	predictableOnly     bool
}

// Subscriber represents a client subscribed to conversation updates
type Subscriber struct {
	channel        chan StreamResponse
	lastSequenceID int64 // Last message sequence_id this subscriber has seen
}

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID string
	loop           *loop.Loop
	subscribers    map[string]*Subscriber
	mu             sync.RWMutex
	lastActivity   time.Time
}

// NewServer creates a new server instance
func NewServer(database *db.DB, llmManager LLMProvider, tools []*llm.Tool, logger *slog.Logger, logBuffer *LogBuffer, predictableOnly bool) *Server {
	return &Server{
		db:                  database,
		llmManager:          llmManager,
		tools:               tools,
		activeConversations: make(map[string]*ConversationManager),
		logger:              logger,
		logBuffer:           logBuffer,
		predictableOnly:     predictableOnly,
	}
}

// RegisterRoutes registers HTTP routes on the given mux
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// API routes
	mux.HandleFunc("/api/conversations", s.handleConversations)
	mux.HandleFunc("/api/conversations/new", s.handleNewConversation)
	mux.HandleFunc("/api/conversation/", s.handleConversation)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/logs/stream", s.handleLogsStream)

	// Serve embedded UI assets with conservative caching
	mux.Handle("/", s.staticHandler(ui.Assets()))
}

// staticHandler serves files from the provided filesystem and disables caching for HTML/CSS/JS to avoid stale bundles
func (s *Server) staticHandler(fs http.FileSystem) http.Handler {
	fileServer := http.FileServer(fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// handleModels returns available models and whether they are ready (i.e., envs present)
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type ModelInfo struct {
		ID    string `json:"id"`
		Ready bool   `json:"ready"`
	}

	var out []ModelInfo

	// If predictable-only mode is enabled, only return the predictable model
	if s.predictableOnly {
		out = append(out, ModelInfo{ID: "predictable", Ready: true})
	} else {
		models := s.llmManager.GetAvailableModels()
		for _, id := range models {
			_, err := s.llmManager.GetService(id)
			out = append(out, ModelInfo{ID: id, Ready: err == nil})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleConversations handles GET /conversations
func (s *Server) handleConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.handleGetConversations(w, r)
}

// handleGetConversations handles GET /conversations
func (s *Server) handleGetConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := 5000
	offset := 0
	var query string

	// Parse query parameters
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}
	query = r.URL.Query().Get("q")

	// Get conversations from database
	var conversations []generated.Conversation
	var err error

	if query != "" {
		conversations, err = s.db.SearchConversations(ctx, query, int64(limit), int64(offset))
	} else {
		conversations, err = s.db.ListConversations(ctx, int64(limit), int64(offset))
	}

	if err != nil {
		s.logger.Error("Failed to get conversations", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(conversations)
}

// handleConversation handles conversation-specific routes
func (s *Server) handleConversation(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/conversation/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Conversation ID required", http.StatusBadRequest)
		return
	}

	conversationID := parts[0]

	// Handle different endpoints
	if len(parts) == 1 {
		// /conversation/<id>
		s.handleGetConversation(w, r, conversationID)
	} else {
		switch parts[1] {
		case "stream":
			// /conversation/<id>/stream
			s.handleStreamConversation(w, r, conversationID)
		case "chat":
			// /conversation/<id>/chat
			s.handleChatConversation(w, r, conversationID)
		default:
			http.Error(w, "Not found", http.StatusNotFound)
		}
	}
}

// handleGetConversation handles GET /conversation/<id>
func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	messages, err := s.db.Queries.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation messages", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

// ChatRequest represents a chat message from the user
type ChatRequest struct {
	Message string `json:"message"`
	Model   string `json:"model,omitempty"`
}

// handleChatConversation handles POST /conversation/<id>/chat
func (s *Server) handleChatConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse request
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	// Get LLM service for the requested model
	modelID := req.Model
	if modelID == "" {
		// Default to Qwen3 Coder on Fireworks
		modelID = "qwen3-coder-fireworks"
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		s.logger.Error("Unsupported model requested", "model", modelID, "error", err)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	// Check if this is the first message and store system prompt before creating manager
	messageCount, err := s.db.Queries.CountMessagesInConversation(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to count messages", "conversationID", conversationID, "error", err)
		// Continue processing even if we can't count messages
	} else {
		if messageCount == 0 {
			// This is the first message, store system prompt first
			systemPrompt, err := GenerateSystemPrompt()
			if err != nil {
				s.logger.Error("Failed to generate system prompt", "error", err)
			} else if systemPrompt != "" {
				systemMessage := llm.Message{
					Role:    llm.MessageRoleUser, // Store as user role but with system type
					Content: []llm.Content{{Type: llm.ContentTypeText, Text: systemPrompt}},
				}
				_, err = s.db.CreateMessage(ctx, db.CreateMessageParams{
					ConversationID: conversationID,
					Type:           db.MessageTypeSystem,
					LLMData:        systemMessage,
					UserData:       nil,
					UsageData:      llm.Usage{},
				})
				if err != nil {
					s.logger.Error("Failed to store system prompt", "conversationID", conversationID, "error", err)
				} else {
					s.logger.Info("Stored system prompt (existing conversation)", "conversationID", conversationID, "length", len(systemPrompt))
				}
			}

			// Generate slug in parallel
			go func() {
				slugCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				_, err := slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, req.Message)
				if err != nil {
					s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
				} else {
					// Notify subscribers about the slug update
					go s.notifySubscribers(context.Background(), conversationID)
				}
			}()
		}
	}

	// Get or create conversation manager (after system prompt is stored)
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Update the LLM service for this request
	manager.loop.SetLLM(llmService)

	// Create user message
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: req.Message},
		},
	}

	// Queue user message
	manager.loop.QueueUserMessage(userMessage)

	// Start processing in background with a timeout context
	go func() {
		processCtx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		if err := manager.loop.Go(processCtx); err != nil && err != context.DeadlineExceeded {
			s.logger.Error("Failed to process loop", "conversationID", conversationID, "error", err)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// handleNewConversation handles POST /api/conversations/new - creates conversation implicitly on first message
func (s *Server) handleNewConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Parse request
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Message == "" {
		http.Error(w, "Message is required", http.StatusBadRequest)
		return
	}

	// Get LLM service for the requested model
	modelID := req.Model
	if modelID == "" {
		// Default to Qwen3 Coder on Fireworks
		modelID = "qwen3-coder-fireworks"
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		s.logger.Error("Unsupported model requested", "model", modelID, "error", err)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	// Create new conversation
	conversation, err := s.db.CreateConversation(ctx, nil, true)
	if err != nil {
		s.logger.Error("Failed to create conversation", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	conversationID := conversation.ConversationID

	// Generate and store system prompt as first message
	systemPrompt, err := GenerateSystemPrompt()
	if err != nil {
		s.logger.Error("Failed to generate system prompt", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if systemPrompt != "" {
		systemMessage := llm.Message{
			Role:    llm.MessageRoleUser, // Store as user role but with system type
			Content: []llm.Content{{Type: llm.ContentTypeText, Text: systemPrompt}},
		}
		_, err = s.db.CreateMessage(ctx, db.CreateMessageParams{
			ConversationID: conversationID,
			Type:           db.MessageTypeSystem,
			LLMData:        systemMessage,
			UserData:       nil,
			UsageData:      llm.Usage{},
		})
		if err != nil {
			s.logger.Error("Failed to store system prompt", "conversationID", conversationID, "error", err)
			// Continue anyway - not critical
		} else {
			s.logger.Debug("Stored system prompt", "conversationID", conversationID, "length", len(systemPrompt))
		}
	}

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Update the LLM service for this request
	manager.loop.SetLLM(llmService)

	// Create user message
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: req.Message},
		},
	}

	// Queue user message
	manager.loop.QueueUserMessage(userMessage)

	// Start processing in background with a timeout context
	go func() {
		processCtx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)
		defer cancel()
		if err := manager.loop.Go(processCtx); err != nil && err != context.DeadlineExceeded {
			s.logger.Error("Failed to process loop", "conversationID", conversationID, "error", err)
		}
	}()

	// Generate slug in parallel
	go func() {
		slugCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err := slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, req.Message)
		if err != nil {
			s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
		} else {
			// Notify subscribers about the slug update
			go s.notifySubscribers(context.Background(), conversationID)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "accepted",
		"conversation_id": conversationID,
	})
}

// handleStreamConversation handles GET /conversation/<id>/stream
func (s *Server) handleStreamConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get current messages and conversation data
	messages, err := s.db.Queries.ListMessages(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation messages", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	conversation, err := s.db.Queries.GetConversation(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Send current messages and conversation data
	streamData := StreamResponse{
		Messages:     messages,
		Conversation: conversation,
	}
	data, _ := json.Marshal(streamData)
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		return
	}

	// Subscribe to updates
	subscriptionID := fmt.Sprintf("%d", time.Now().UnixNano())
	updateChan := make(chan StreamResponse, 10)
	manager.subscribe(subscriptionID, updateChan)
	defer manager.unsubscribe(subscriptionID)

	// Track the last sequence_id and seen message IDs for this subscriber
	var lastSequenceID int64 = 0
	if len(messages) > 0 {
		lastSequenceID = messages[len(messages)-1].SequenceID
	}
	manager.mu.Lock()
	if sub, exists := manager.subscribers[subscriptionID]; exists {
		sub.lastSequenceID = lastSequenceID
	}
	manager.mu.Unlock()

	// Listen for updates or context cancellation
	for {
		select {
		case <-ctx.Done():
			return
		case streamData := <-updateChan:
			// Always forward updates, even if only the conversation changed (e.g., slug added)
			data, _ := json.Marshal(streamData)
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
	}
}

// getOrCreateConversationManager gets an existing conversation manager or creates a new one
func (s *Server) getOrCreateConversationManager(ctx context.Context, conversationID string) (*ConversationManager, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if manager already exists
	if manager, exists := s.activeConversations[conversationID]; exists {
		manager.lastActivity = time.Now()
		return manager, nil
	}

	// Verify conversation exists
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("conversation not found: %w", err)
	}

	// Get existing messages to build history
	messages, err := s.db.Queries.ListMessages(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation history: %w", err)
	}

	// Create message record function
	recordMessage := func(ctx context.Context, message llm.Message, usage llm.Usage) error {
		return s.recordMessage(ctx, conversationID, message, usage)
	}

	// Convert messages to LLM format for history and extract system prompt
	var history []llm.Message
	var system []llm.SystemContent
	for _, msg := range messages {
		// Extract system prompt from system messages
		if msg.Type == string(db.MessageTypeSystem) {
			llmMsg, err := s.convertToLLMMessage(msg)
			if err != nil {
				s.logger.Warn("Failed to convert system message to LLM format", "messageID", msg.MessageID, "error", err)
				continue
			}
			// Extract text content from system message
			for _, content := range llmMsg.Content {
				if content.Type == llm.ContentTypeText && content.Text != "" {
					system = append(system, llm.SystemContent{
						Type: "text",
						Text: content.Text,
					})
				}
			}
			// Don't add system messages to history - they're sent separately
			continue
		}
		llmMsg, err := s.convertToLLMMessage(msg)
		if err != nil {
			s.logger.Warn("Failed to convert message to LLM format", "messageID", msg.MessageID, "error", err)
			continue
		}
		history = append(history, llmMsg)
	}

	// Log system prompt info
	if len(system) > 0 {
		systemLen := 0
		for _, sys := range system {
			systemLen += len(sys.Text)
		}
		s.logger.Info("Loaded system prompt from database", "conversationID", conversationID, "system_items", len(system), "total_length", systemLen)
	} else {
		s.logger.Warn("No system prompt found in database", "conversationID", conversationID, "message_count", len(messages))
	}

	// Create loop with history (temporarily use predictable service, will be overridden per request)
	convLoop := loop.NewLoop(loop.Config{
		LLM:           loop.NewPredictableService(),
		History:       history,
		Tools:         s.tools,
		RecordMessage: recordMessage,
		Logger:        s.logger.With("conversationID", conversationID),
		System:        system,
	})

	// Create manager
	manager := &ConversationManager{
		conversationID: conversationID,
		loop:           convLoop,
		subscribers:    make(map[string]*Subscriber),
		lastActivity:   time.Now(),
	}

	s.activeConversations[conversationID] = manager
	_ = conversation // avoid unused variable

	return manager, nil
}

// recordMessage records a new message to the database
func (s *Server) recordMessage(ctx context.Context, conversationID string, message llm.Message, usage llm.Usage) error {
	// Convert LLM message to database format
	messageType, err := s.getMessageType(message)
	if err != nil {
		return fmt.Errorf("failed to determine message type: %w", err)
	}

	// The message service will handle JSON marshalling

	// Create message
	_, err = s.db.CreateMessage(ctx, db.CreateMessageParams{
		ConversationID: conversationID,
		Type:           messageType,
		LLMData:        message,
		UserData:       nil,
		UsageData:      usage,
	})
	if err != nil {
		return fmt.Errorf("failed to create message: %w", err)
	}

	// Update conversation's last updated timestamp for correct ordering
	if err := s.db.Queries.UpdateConversationTimestamp(ctx, conversationID); err != nil {
		s.logger.Warn("Failed to update conversation timestamp", "conversationID", conversationID, "error", err)
	}

	// Touch active manager activity time if present
	s.mu.Lock()
	if mgr, ok := s.activeConversations[conversationID]; ok {
		mgr.lastActivity = time.Now()
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
	s.mu.RLock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.RUnlock()

	if !exists {
		return
	}

	// Get conversation data (this is always sent)
	conversation, err := s.db.Queries.GetConversation(ctx, conversationID)
	if err != nil {
		s.logger.Error("Failed to get conversation for notification", "conversationID", conversationID, "error", err)
		return
	}

	// Notify subscribers with incremental updates
	manager.mu.Lock()
	defer manager.mu.Unlock()

	for subscriptionID, sub := range manager.subscribers {
		var messages []generated.Message

		// Get messages since the last message this subscriber has seen
		if sub.lastSequenceID == 0 {
			// New subscriber or no messages seen yet - send all messages
			allMessages, err := s.db.Queries.ListMessages(ctx, conversationID)
			if err != nil {
				s.logger.Error("Failed to get all messages for new subscriber", "conversationID", conversationID, "subscriptionID", subscriptionID, "error", err)
				continue
			}
			messages = allMessages
		} else {
			// Existing subscriber - send only new messages
			newMessages, err := s.db.Queries.ListMessagesSince(ctx, generated.ListMessagesSinceParams{
				ConversationID: conversationID,
				SequenceID:     sub.lastSequenceID,
			})
			if err != nil {
				s.logger.Error("Failed to get new messages for subscriber", "conversationID", conversationID, "subscriptionID", subscriptionID, "error", err)
				continue
			}
			messages = newMessages
		}

		// Update the subscriber's last seen sequence_id
		if len(messages) > 0 {
			sub.lastSequenceID = messages[len(messages)-1].SequenceID
		}

		// Send the update even if there are no new messages so clients can react to conversation-only changes (e.g., slug updates)
		streamData := StreamResponse{
			Messages:     messages,
			Conversation: conversation,
		}

		select {
		case sub.channel <- streamData:
		default:
			// Channel is full, skip this subscriber
			s.logger.Warn("Subscriber channel full, skipping update", "conversationID", conversationID, "subscriptionID", subscriptionID)
		}
	}
}

// subscribe adds a subscriber to a conversation
func (cm *ConversationManager) subscribe(id string, ch chan StreamResponse) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.subscribers[id] = &Subscriber{
		channel:        ch,
		lastSequenceID: 0, // Will be set after first message batch
	}
}

// unsubscribe removes a subscriber from a conversation
func (cm *ConversationManager) unsubscribe(id string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if sub, exists := cm.subscribers[id]; exists {
		close(sub.channel)
		delete(cm.subscribers, id)
	}
}

// Cleanup removes inactive conversation managers
func (s *Server) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for id, manager := range s.activeConversations {
		// Remove managers that have been inactive for more than 30 minutes
		if now.Sub(manager.lastActivity) > 30*time.Minute {
			// Close all subscriber channels
			manager.mu.Lock()
			for _, sub := range manager.subscribers {
				close(sub.channel)
			}
			manager.mu.Unlock()

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

	// Start server in goroutine
	serverErrCh := make(chan error, 1)
	go func() {
		s.logger.Info("Server starting", "addr", httpServer.Addr, "url", "http://localhost:"+port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
