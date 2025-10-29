package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"shelley.exe.dev/claudetool/browse"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/slug"
)

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
		case "cancel":
			// /conversation/<id>/cancel
			s.handleCancelConversation(w, r, conversationID)
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
	var (
		messages     []generated.Message
		conversation generated.Conversation
	)
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
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Conversation not found", http.StatusNotFound)
			return
		}
		s.logger.Error("Failed to get conversation messages", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	apiMessages := toAPIMessages(messages)
	json.NewEncoder(w).Encode(StreamResponse{
		Messages:     apiMessages,
		Conversation: conversation,
		AgentWorking: agentWorking(apiMessages),
	})
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

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
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

	firstMessage, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage)
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Error("Failed to accept user message", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if firstMessage {
		go func() {
			slugCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, req.Message)
			if err != nil {
				s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
			} else {
				go s.notifySubscribers(context.Background(), conversationID)
			}
		}()
	}

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

	// Get or create conversation manager
	manager, err := s.getOrCreateConversationManager(ctx, conversationID)
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Error("Failed to get conversation manager", "conversationID", conversationID, "error", err)
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

	firstMessage, err := manager.AcceptUserMessage(ctx, llmService, modelID, userMessage)
	if err != nil {
		if errors.Is(err, errConversationModelMismatch) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.logger.Error("Failed to accept user message", "conversationID", conversationID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if firstMessage {
		go func() {
			slugCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := slug.GenerateSlug(slugCtx, s.llmManager, s.db, s.logger, conversationID, req.Message)
			if err != nil {
				s.logger.Warn("Failed to generate slug for conversation", "conversationID", conversationID, "error", err)
			} else {
				go s.notifySubscribers(context.Background(), conversationID)
			}
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "accepted",
		"conversation_id": conversationID,
	})
}

// handleCancelConversation handles POST /conversation/<id>/cancel
func (s *Server) handleCancelConversation(w http.ResponseWriter, r *http.Request, conversationID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Get the conversation manager if it exists
	s.mu.Lock()
	manager, exists := s.activeConversations[conversationID]
	s.mu.Unlock()

	if !exists {
		// No active conversation to cancel
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "no_active_conversation"})
		return
	}

	// Cancel the conversation
	if err := manager.CancelConversation(ctx); err != nil {
		s.logger.Error("Failed to cancel conversation", "conversationID", conversationID, "error", err)
		http.Error(w, "Failed to cancel conversation", http.StatusInternalServerError)
		return
	}

	s.logger.Info("Conversation cancelled", "conversationID", conversationID)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
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
	apiMessages := toAPIMessages(messages)
	streamData := StreamResponse{
		Messages:     apiMessages,
		Conversation: conversation,
		AgentWorking: agentWorking(apiMessages),
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

	// Subscribe to new messages after the last one we sent
	last := int64(-1)
	if len(messages) > 0 {
		last = messages[len(messages)-1].SequenceID
	}
	next := manager.subpub.Subscribe(ctx, last)
	for {
		streamData, cont := next()
		if !cont {
			break
		}
		// Always forward updates, even if only the conversation changed (e.g., slug added)
		data, _ := json.Marshal(streamData)
		fmt.Fprintf(w, "data: %s\n\n", data)
		w.(http.Flusher).Flush()
	}
}
