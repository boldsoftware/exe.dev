-- Add display_data column to messages table for tool-specific UI rendering
-- This allows us to separate what's sent to the LLM from what's displayed to the user

ALTER TABLE messages ADD COLUMN display_data TEXT; -- JSON data for tool-specific display
