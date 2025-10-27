package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/loop"
	"shelley.exe.dev/models"
	"shelley.exe.dev/slug"
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

// Subscriber represents a client subscribed to conversation updates
type Subscriber struct {
	channel        chan StreamResponse
	lastSequenceID int64 // Last message sequence_id this subscriber has seen
}

var errConversationModelMismatch = errors.New("conversation model mismatch")

// ConversationManager manages a single active conversation
type ConversationManager struct {
	conversationID string
	loop           *loop.Loop
	loopCancel     context.CancelFunc
	subscribers    map[string]*Subscriber
	mu             sync.Mutex
	lastActivity   time.Time
	modelID        string
	history        []llm.Message
	system         []llm.SystemContent
	recordMessage  loop.MessageRecordFunc
	logger         *slog.Logger
	tools          []*llm.Tool
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

// handleRead serves files from limited allowed locations via /api/read?path=
func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	// Clean and enforce prefix restriction
	clean := p
	// Do not resolve symlinks here; enforce string prefix restriction only
	if !(strings.HasPrefix(clean, browse.ScreenshotDir+"/")) {
		http.Error(w, "path not allowed", http.StatusForbidden)
		return
	}
	f, err := os.Open(clean)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	// Determine content type by extension first, then fallback to sniffing
	ext := strings.ToLower(filepath.Ext(clean))
	switch ext {
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".gif":
		w.Header().Set("Content-Type", "image/gif")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	default:
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		contentType := http.DetectContentType(buf[:n])
		if _, err := f.Seek(0, 0); err != nil {
			http.Error(w, "seek failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
	}
	// Reasonable short-term caching for assets, allow quick refresh during sessions
	w.Header().Set("Cache-Control", "public, max-age=300")
	io.Copy(w, f)
}

// staticHandler serves files from the provided filesystem and disables caching for HTML/CSS/JS to avoid stale bundles
func (s *Server) staticHandler(fs http.FileSystem) http.Handler {
	fileServer := http.FileServer(fs)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject initialization data into index.html
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			w.Header().Set("Content-Type", "text/html")
			s.serveIndexWithInit(w, r, fs)
			return
		}

		if strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveIndexWithInit serves index.html with injected initialization data
func (s *Server) serveIndexWithInit(w http.ResponseWriter, r *http.Request, fs http.FileSystem) {
	// Read index.html from the filesystem
	file, err := fs.Open("/index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	indexHTML, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "Failed to read index.html", http.StatusInternalServerError)
		return
	}

	// Build initialization data
	type ModelInfo struct {
		ID    string `json:"id"`
		Ready bool   `json:"ready"`
	}

	var models []ModelInfo
	if s.predictableOnly {
		models = append(models, ModelInfo{ID: "predictable", Ready: true})
	} else {
		modelIDs := s.llmManager.GetAvailableModels()
		for _, id := range modelIDs {
			// Skip predictable model unless predictable-only flag is set
			if id == "predictable" {
				continue
			}
			_, err := s.llmManager.GetService(id)
			models = append(models, ModelInfo{ID: id, Ready: err == nil})
		}
	}

	// Select default model - use configured default if available, otherwise first ready model
	defaultModel := s.defaultModel
	if defaultModel == "" {
		defaultModel = "claude-sonnet-4.5"
	}
	defaultModelAvailable := false
	for _, m := range models {
		if m.ID == defaultModel && m.Ready {
			defaultModelAvailable = true
			break
		}
	}
	if !defaultModelAvailable {
		// Fall back to first ready model
		for _, m := range models {
			if m.Ready {
				defaultModel = m.ID
				break
			}
		}
	}

	// Get hostname
	hostname := "localhost"
	if h, err := os.Hostname(); err == nil {
		hostname = h
	}

	initData := map[string]interface{}{
		"models":        models,
		"default_model": defaultModel,
		"hostname":      hostname,
	}
	if s.terminalURL != "" {
		initData["terminal_url"] = s.terminalURL
	}
	if len(s.links) > 0 {
		initData["links"] = s.links
	}

	initJSON, err := json.Marshal(initData)
	if err != nil {
		http.Error(w, "Failed to marshal init data", http.StatusInternalServerError)
		return
	}

	// Inject the script tag before </head>
	initScript := fmt.Sprintf(`<script>window.__SHELLEY_INIT__=%s;</script>`, initJSON)
	modifiedHTML := strings.Replace(string(indexHTML), "</head>", initScript+"</head>", 1)

	w.Write([]byte(modifiedHTML))
}

// handleConfig returns server configuration
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
	var messages []generated.Message
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get conversation messages", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toAPIMessages(messages))
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
		modelID = s.defaultModel
	}

	llmService, err := s.llmManager.GetService(modelID)
	if err != nil {
		s.logger.Error("Unsupported model requested", "model", modelID, "error", err)
		http.Error(w, fmt.Sprintf("Unsupported model: %s", modelID), http.StatusBadRequest)
		return
	}

	// Check if this is the first message and store system prompt before creating manager
	var messageCount int64
	err = s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messageCount, err = q.CountMessagesInConversation(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to count messages", "conversationID", conversationID, "error", err)
		http.Error(w, fmt.Sprintf("db error: %s", err.Error()), http.StatusInternalServerError)
		return
	}

	if messageCount == 0 {
		// This is the first message, store system prompt first
		systemPrompt, err := GenerateSystemPrompt()
		if err != nil {
			s.logger.Error("Failed to generate system prompt", "error", err)
			http.Error(w, fmt.Sprintf("error: %s", err.Error()), http.StatusInternalServerError)
			return
		}
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
			http.Error(w, fmt.Sprintf("db error: %s", err.Error()), http.StatusInternalServerError)
			return
		}

		s.logger.Info("Stored system prompt (existing conversation)", "conversationID", conversationID, "length", len(systemPrompt))

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

	// Get or create conversation manager (after system prompt is stored)
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, llmService, modelID)
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	manager.mu.Lock()
	loopInstance := manager.loop
	manager.mu.Unlock()
	if loopInstance == nil {
		s.logger.Error("Conversation loop not initialized", "conversationID", conversationID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Create user message
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: req.Message},
		},
	}

	// Queue user message
	// The conversation loop is already running and will pick up this message
	loopInstance.QueueUserMessage(userMessage)

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
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, llmService, modelID)
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	manager.mu.Lock()
	loopInstance := manager.loop
	manager.mu.Unlock()
	if loopInstance == nil {
		s.logger.Error("Conversation loop not initialized", "conversationID", conversationID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Create user message
	userMessage := llm.Message{
		Role: llm.MessageRoleUser,
		Content: []llm.Content{
			{Type: llm.ContentTypeText, Text: req.Message},
		},
	}

	// Queue user message
	// The conversation loop is already running and will pick up this message
	loopInstance.QueueUserMessage(userMessage)

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
	var messages []generated.Message
	var conversation generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		messages, err = q.ListMessages(ctx, conversationID)
		if err != nil {
			return err
		}
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
	if err != nil {
		s.logger.Error("Failed to get conversation data", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Send current messages and conversation data
	streamData := StreamResponse{
		Messages:     toAPIMessages(messages),
		Conversation: conversation,
	}
	data, _ := json.Marshal(streamData)
	fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID, nil, "")
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
		case streamData, ok := <-updateChan:
			if !ok {
				return
			}
			// Always forward updates, even if only the conversation changed (e.g., slug added)
			data, _ := json.Marshal(streamData)
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
	}
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
		subscribers:    make(map[string]*Subscriber),
		lastActivity:   time.Now(),
		history:        history,
		system:         system,
		recordMessage:  recordMessage,
		logger:         s.logger.With("conversationID", conversationID),
		tools:          append([]*llm.Tool(nil), s.tools...),
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

func (cm *ConversationManager) ensureLoop(service llm.Service, modelID string) error {
	cm.mu.Lock()
	if cm.loop != nil {
		existingModel := cm.modelID
		cm.mu.Unlock()
		if existingModel != "" && modelID != "" && existingModel != modelID {
			return fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}
		return nil
	}

	history := append([]llm.Message(nil), cm.history...)
	system := append([]llm.SystemContent(nil), cm.system...)
	recordMessage := cm.recordMessage
	tools := append([]*llm.Tool(nil), cm.tools...)
	logger := cm.logger
	cm.mu.Unlock()

	loopInstance := loop.NewLoop(loop.Config{
		LLM:           service,
		History:       history,
		Tools:         tools,
		RecordMessage: recordMessage,
		Logger:        logger,
		System:        system,
	})

	processCtx, cancel := context.WithTimeout(context.Background(), 12*time.Hour)

	cm.mu.Lock()
	if cm.loop != nil {
		cm.mu.Unlock()
		cancel()
		existingModel := cm.modelID
		if existingModel != "" && modelID != "" && existingModel != modelID {
			return fmt.Errorf("%w: conversation already uses model %s; requested %s", errConversationModelMismatch, existingModel, modelID)
		}
		return nil
	}
	cm.loop = loopInstance
	cm.loopCancel = cancel
	cm.modelID = modelID
	cm.history = nil
	cm.system = nil
	cm.mu.Unlock()

	go func() {
		if err := loopInstance.Go(processCtx); err != nil && err != context.DeadlineExceeded {
			if logger != nil {
				logger.Error("Conversation loop stopped", "error", err)
			} else {
				slog.Default().Error("Conversation loop stopped", "error", err)
			}
		}
	}()

	return nil
}

func (cm *ConversationManager) stopLoop() {
	cm.mu.Lock()
	cancel := cm.loopCancel
	cm.loopCancel = nil
	cm.loop = nil
	cm.modelID = ""
	cm.mu.Unlock()

	if cancel != nil {
		cancel()
	}
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

	// Get conversation data (this is always sent)
	var conversation generated.Conversation
	err := s.db.Queries(ctx, func(q *generated.Queries) error {
		var err error
		conversation, err = q.GetConversation(ctx, conversationID)
		return err
	})
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
			err := s.db.Queries(ctx, func(q *generated.Queries) error {
				var err error
				messages, err = q.ListMessages(ctx, conversationID)
				return err
			})
			if err != nil {
				s.logger.Error("Failed to get all messages for new subscriber", "conversationID", conversationID, "subscriptionID", subscriptionID, "error", err)
				continue
			}
		} else {
			// Existing subscriber - send only new messages
			err := s.db.Queries(ctx, func(q *generated.Queries) error {
				var err error
				messages, err = q.ListMessagesSince(ctx, generated.ListMessagesSinceParams{
					ConversationID: conversationID,
					SequenceID:     sub.lastSequenceID,
				})
				return err
			})
			if err != nil {
				s.logger.Error("Failed to get new messages for subscriber", "conversationID", conversationID, "subscriptionID", subscriptionID, "error", err)
				continue
			}
		}

		// Update the subscriber's last seen sequence_id
		if len(messages) > 0 {
			sub.lastSequenceID = messages[len(messages)-1].SequenceID
		}

		// Send the update even if there are no new messages so clients can react to conversation-only changes (e.g., slug updates)
		streamData := StreamResponse{
			Messages:     toAPIMessages(messages),
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
			manager.stopLoop()

			// Collect subscriber IDs then unsubscribe outside the manager lock to avoid double-closing channels
			manager.mu.Lock()
			subscriberIDs := make([]string, 0, len(manager.subscribers))
			for subscriberID := range manager.subscribers {
				subscriberIDs = append(subscriberIDs, subscriberID)
			}
			manager.mu.Unlock()

			for _, subscriberID := range subscriberIDs {
				manager.unsubscribe(subscriberID)
			}

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
