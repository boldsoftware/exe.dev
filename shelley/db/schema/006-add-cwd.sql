-- Add cwd (current working directory) column to conversations
-- This allows each conversation to have its own working directory for tools

ALTER TABLE conversations ADD COLUMN cwd TEXT;
