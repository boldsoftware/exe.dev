-- Add model column to conversations table
-- This stores the LLM model used for the conversation

ALTER TABLE conversations ADD COLUMN model TEXT;
