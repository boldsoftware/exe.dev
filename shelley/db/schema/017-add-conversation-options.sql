-- Add conversation_options column to conversations
-- This is a JSON column for extensible conversation settings
-- Currently supports: {"type": "normal" | "orchestrator"}
-- Default is '{}' (empty JSON object, treated as type=normal)

ALTER TABLE conversations ADD COLUMN conversation_options TEXT NOT NULL DEFAULT '{}';
