package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"exe.dev/exe-ops/apitype"
	"exe.dev/exe-ops/server/aiagent"
)

// HandleChatConfig handles GET /api/v1/chat/config — returns AI provider info (no secrets).
func (h *Handlers) HandleChatConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg := map[string]string{
		"provider": "",
		"model":    "",
	}
	if h.aiConfig != nil {
		cfg["provider"] = h.aiConfig.Provider
		cfg["model"] = h.aiConfig.Model
	}
	writeJSON(w, cfg)
}

// HandleListConversations handles GET /api/v1/chat/conversations.
func (h *Handlers) HandleListConversations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	convos, err := h.store.ListConversations(r.Context())
	if err != nil {
		h.log.Error("list conversations", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, convos)
}

// HandleConversation handles DELETE and PATCH on /api/v1/chat/conversations/{id}.
func (h *Handlers) HandleConversation(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/chat/conversations/")
	if id == "" {
		http.Error(w, "conversation id required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		if err := h.store.DeleteConversation(r.Context(), id); err != nil {
			h.log.Error("delete conversation", "error", err, "id", id)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodPatch:
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := h.store.UpdateConversationTitle(r.Context(), id, body.Title); err != nil {
			h.log.Error("update conversation title", "error", err, "id", id)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleListChatMessages handles GET /api/v1/chat/messages.
func (h *Handlers) HandleListChatMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	conversationID := r.URL.Query().Get("conversation_id")
	if conversationID == "" {
		http.Error(w, "conversation_id required", http.StatusBadRequest)
		return
	}

	messages, err := h.store.ListChatMessages(r.Context(), conversationID)
	if err != nil {
		h.log.Error("list chat messages", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, messages)
}

// HandleChatSend handles POST /api/v1/chat/send — streams AI response via SSE.
func (h *Handlers) HandleChatSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.aiAgent == nil {
		http.Error(w, "AI agent not configured. Set EXE_OPS_AI_PROVIDER to enable.", http.StatusNotImplemented)
		return
	}

	var req apitype.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Create conversation if new.
	conversationID := req.ConversationID
	isNewConversation := conversationID == ""
	if isNewConversation {
		conversationID = generateID()
		title := summarizeTitle(req.Message)
		if err := h.store.CreateConversation(ctx, conversationID, title); err != nil {
			h.log.Error("create conversation", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Insert user message.
	if err := h.store.InsertChatMessage(ctx, conversationID, "user", req.Message); err != nil {
		h.log.Error("insert user message", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Load full conversation history.
	dbMessages, err := h.store.ListChatMessages(ctx, conversationID)
	if err != nil {
		h.log.Error("load chat history", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build system prompt with live fleet data.
	fleet, err := h.store.ListFleet(ctx)
	if err != nil {
		h.log.Error("load fleet for system prompt", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	systemPrompt := aiagent.BuildSystemPrompt(fleet)

	// Convert DB messages to provider messages.
	providerMessages := []aiagent.Message{{Role: "system", Content: systemPrompt}}
	for _, m := range dbMessages {
		providerMessages = append(providerMessages, aiagent.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	// Set SSE headers.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Send conversation ID immediately.
	fmt.Fprintf(w, "event: conversation\ndata: %s\n\n", mustJSON(map[string]string{"id": conversationID}))
	flusher.Flush()

	tools := aiagent.ToolDefs()

	// Tool-calling loop: keep calling the provider until we get a text response.
	const maxToolRounds = 10
	for round := 0; round < maxToolRounds; round++ {
		stream, err := h.aiAgent.ChatStream(ctx, providerMessages, tools)
		if err != nil {
			h.log.Error("ai stream error", "error", err)
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": "AI provider error"}))
			flusher.Flush()
			return
		}

		var fullText strings.Builder
		var pendingToolCalls []aiagent.ToolCall
		hadText := false

		for event := range stream {
			switch event.Type {
			case "text":
				hadText = true
				fullText.WriteString(event.Text)
				fmt.Fprintf(w, "event: delta\ndata: %s\n\n", mustJSON(map[string]string{"text": event.Text}))
				flusher.Flush()
			case "tool_call":
				if event.ToolCall != nil {
					pendingToolCalls = append(pendingToolCalls, *event.ToolCall)
				}
			case "error":
				h.log.Error("ai stream event error", "error", event.Error)
				fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": event.Error}))
				flusher.Flush()
				return
			case "done":
				// handled below
			}
		}

		// If we got tool calls, execute them and continue the loop.
		if len(pendingToolCalls) > 0 {
			// Add assistant message with tool calls to history.
			assistantMsg := aiagent.Message{
				Role:      "assistant",
				Content:   fullText.String(),
				ToolCalls: pendingToolCalls,
			}
			providerMessages = append(providerMessages, assistantMsg)

			for _, tc := range pendingToolCalls {
				h.log.Info("executing tool call", "tool", tc.Name, "conversation", conversationID)
				result := h.executeToolCall(ctx, tc, conversationID)
				providerMessages = append(providerMessages, aiagent.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
				})
			}
			continue // next round with tool results
		}

		// No tool calls — we have the final response.
		if hadText {
			if err := h.store.InsertChatMessage(ctx, conversationID, "assistant", fullText.String()); err != nil {
				h.log.Error("insert assistant message", "error", err)
			}
		}

		// Generate a title for new conversations before sending done.
		if isNewConversation && hadText {
			if title := h.generateTitle(ctx, req.Message, fullText.String()); title != "" {
				if err := h.store.UpdateConversationTitle(ctx, conversationID, title); err != nil {
					h.log.Error("update conversation title", "error", err)
				} else {
					fmt.Fprintf(w, "event: title\ndata: %s\n\n", mustJSON(map[string]string{"title": title}))
					flusher.Flush()
				}
			}
		}

		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
		return
	}

	// Exceeded max tool rounds.
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": "exceeded maximum tool call rounds"}))
	flusher.Flush()
}

// executeToolCall runs a tool against the store.
func (h *Handlers) executeToolCall(ctx context.Context, tc aiagent.ToolCall, conversationID string) string {
	if !aiagent.AllowedTools[tc.Name] {
		return fmt.Sprintf(`{"error": "unknown tool: %s"}`, tc.Name)
	}

	switch tc.Name {
	case "list_servers":
		servers, err := h.store.ListServers(ctx)
		if err != nil {
			return fmt.Sprintf(`{"error": "%s"}`, err.Error())
		}
		b, _ := json.Marshal(servers)
		return string(b)

	case "get_server_details":
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(tc.Arguments, &args); err != nil {
			return fmt.Sprintf(`{"error": "invalid arguments: %s"}`, err.Error())
		}
		server, err := h.store.GetServer(ctx, args.Name)
		if err != nil {
			return fmt.Sprintf(`{"error": "%s"}`, err.Error())
		}
		if server == nil {
			return `{"error": "server not found"}`
		}
		b, _ := json.Marshal(server)
		return string(b)

	case "get_fleet_status":
		fleet, err := h.store.ListFleet(ctx)
		if err != nil {
			return fmt.Sprintf(`{"error": "%s"}`, err.Error())
		}
		b, _ := json.Marshal(fleet)
		return string(b)

	default:
		return `{"error": "unknown tool"}`
	}
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// summarizeTitle returns the first 8 words of a message, truncated with ellipsis.
func summarizeTitle(msg string) string {
	words := strings.Fields(msg)
	if len(words) <= 8 {
		return msg
	}
	return strings.Join(words[:8], " ") + "…"
}

// generateTitle asks the AI to generate a short conversation title.
func (h *Handlers) generateTitle(ctx context.Context, userMsg, assistantMsg string) string {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	messages := []aiagent.Message{
		{Role: "system", Content: "Generate a concise title (5 words or less) for this conversation. Respond with ONLY the title, nothing else. No quotes, no punctuation at the end."},
		{Role: "user", Content: userMsg},
		{Role: "assistant", Content: assistantMsg},
		{Role: "user", Content: "Generate a 5 word or less title for the above conversation."},
	}

	stream, err := h.aiAgent.ChatStream(ctx, messages, nil)
	if err != nil {
		h.log.Error("generate title: stream", "error", err)
		return ""
	}

	var title strings.Builder
	for event := range stream {
		if event.Type == "text" {
			title.WriteString(event.Text)
		}
	}

	t := strings.TrimSpace(title.String())
	if len(t) > 80 {
		t = t[:80]
	}
	return t
}
