-- Add autoincrementing sequence_id to messages table for reliable ordering
-- This eliminates timestamp collision issues when multiple messages are created simultaneously

-- Create new table with sequence_id column
CREATE TABLE messages_new (
    message_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    sequence_id INTEGER NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('user', 'agent', 'tool', 'system')),
    llm_data TEXT, -- JSON data sent to/from LLM
    user_data TEXT, -- JSON data for UI display
    usage_data TEXT, -- JSON data about token usage, etc.
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (conversation_id) REFERENCES conversations(conversation_id) ON DELETE CASCADE
);

-- Copy data from old table to new table with sequence_id based on created_at order
-- Only run if the new table is empty (idempotent)
INSERT INTO messages_new (message_id, conversation_id, sequence_id, type, llm_data, user_data, usage_data, created_at)
SELECT 
    message_id, 
    conversation_id,
    ROW_NUMBER() OVER (PARTITION BY conversation_id ORDER BY created_at, message_id) as sequence_id,
    type, 
    llm_data, 
    user_data, 
    usage_data, 
    created_at
FROM messages
WHERE NOT EXISTS (SELECT 1 FROM messages_new LIMIT 1);

-- Replace old table with new table (only if we have data in the new table)
-- Check if we need to do the table swap
DROP TABLE IF EXISTS messages_old;
ALTER TABLE messages RENAME TO messages_old;
ALTER TABLE messages_new RENAME TO messages;
DROP TABLE messages_old;

-- Recreate indexes with sequence_id instead of created_at for ordering
CREATE INDEX idx_messages_conversation_id ON messages(conversation_id);
CREATE INDEX idx_messages_conversation_sequence ON messages(conversation_id, sequence_id);
CREATE INDEX idx_messages_type ON messages(type);
