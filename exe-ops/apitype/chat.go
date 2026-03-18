package apitype

// Conversation represents a chat conversation.
type Conversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	ID             int    `json:"id"`
	ConversationID string `json:"conversation_id"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	CreatedAt      string `json:"created_at"`
}

// ChatRequest is the payload for POST /api/v1/chat/send.
type ChatRequest struct {
	ConversationID string `json:"conversation_id,omitempty"`
	Message        string `json:"message"`
}
