-- Messages table
-- Each message is part of a conversation and can be from user, agent, or tool
CREATE TABLE IF NOT EXISTS messages (
    message_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('user', 'agent', 'tool')),
    llm_data TEXT, -- JSON data sent to/from LLM
    user_data TEXT, -- JSON data for UI display
    usage_data TEXT, -- JSON data about token usage, etc.
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (conversation_id) REFERENCES conversations(conversation_id) ON DELETE CASCADE
);

-- Index on conversation_id for efficient message retrieval
CREATE INDEX IF NOT EXISTS idx_messages_conversation_id ON messages(conversation_id);
-- Index on conversation_id and created_at for chronological ordering
CREATE INDEX IF NOT EXISTS idx_messages_conversation_created ON messages(conversation_id, created_at);
-- Index on type for filtering by message type
CREATE INDEX IF NOT EXISTS idx_messages_type ON messages(type);
