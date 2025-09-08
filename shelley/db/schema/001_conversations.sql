-- Conversations table
-- Each conversation represents a single chat session with the AI agent
CREATE TABLE IF NOT EXISTS conversations (
    conversation_id TEXT PRIMARY KEY,
    slug TEXT, -- human-readable identifier, can be null initially
    user_initiated BOOLEAN NOT NULL DEFAULT TRUE, -- FALSE for subagent/tool conversations
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Partial unique index on slug (only for non-NULL values) for uniqueness and faster lookups
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_slug_unique ON conversations(slug) WHERE slug IS NOT NULL;
-- Index on updated_at for ordering by recent activity
CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations(updated_at DESC);
