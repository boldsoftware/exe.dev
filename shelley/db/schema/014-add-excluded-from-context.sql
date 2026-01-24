-- Add excluded_from_context column to messages table.
-- Messages with this flag set are stored for billing/cost tracking purposes
-- but are NOT included when building the LLM request context.
-- This is used for truncated responses that we want to keep for cost tracking
-- but that would confuse the LLM if sent back (e.g., partial tool calls).

ALTER TABLE messages ADD COLUMN excluded_from_context BOOLEAN NOT NULL DEFAULT FALSE;
