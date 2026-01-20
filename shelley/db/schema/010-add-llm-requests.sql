-- LLM Requests table for tracking/debugging API calls
-- Each row represents one HTTP request/response to an LLM provider

CREATE TABLE llm_requests (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT,  -- optional, may be NULL for requests outside conversations
    model TEXT NOT NULL,   -- model ID used for the request
    provider TEXT NOT NULL, -- e.g., "anthropic", "openai", "gemini"
    url TEXT NOT NULL,
    request_body TEXT,     -- JSON request body
    response_body TEXT,    -- JSON response body
    status_code INTEGER,
    error TEXT,            -- error message if any
    duration_ms INTEGER,   -- request duration in milliseconds
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index on conversation_id for debugging specific conversations
CREATE INDEX idx_llm_requests_conversation_id ON llm_requests(conversation_id);

-- Index on created_at for time-based queries
CREATE INDEX idx_llm_requests_created_at ON llm_requests(created_at DESC);

-- Index on model for filtering by model
CREATE INDEX idx_llm_requests_model ON llm_requests(model);
