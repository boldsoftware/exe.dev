-- Add prefix deduplication columns to llm_requests table
-- This allows storing only the suffix of request_body when there's a shared prefix
-- with a previous request in the same conversation.

ALTER TABLE llm_requests ADD COLUMN prefix_request_id INTEGER REFERENCES llm_requests(id);
ALTER TABLE llm_requests ADD COLUMN prefix_length INTEGER;

-- Index for efficient prefix lookups
CREATE INDEX idx_llm_requests_prefix_request_id ON llm_requests(prefix_request_id) WHERE prefix_request_id IS NOT NULL;
