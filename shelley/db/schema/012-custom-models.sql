-- Models table
-- Stores user-configured LLM models with API keys

CREATE TABLE models (
    model_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    provider_type TEXT NOT NULL CHECK (provider_type IN ('anthropic', 'openai', 'openai-responses', 'gemini')),
    endpoint TEXT NOT NULL,
    api_key TEXT NOT NULL,
    model_name TEXT NOT NULL,  -- The actual model name sent to the API (e.g., "claude-sonnet-4-5-20250514")
    max_tokens INTEGER NOT NULL DEFAULT 200000,
    tags TEXT NOT NULL DEFAULT '',  -- Comma-separated tags (e.g., "slug" for slug generation)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
