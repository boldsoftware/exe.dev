-- Add archived column to conversations
ALTER TABLE conversations ADD COLUMN archived BOOLEAN NOT NULL DEFAULT FALSE;

-- Index on archived for filtering
CREATE INDEX idx_conversations_archived ON conversations(archived);
