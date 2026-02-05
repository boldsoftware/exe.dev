-- Add limits JSON column to users table for per-user resource limit overrides.
-- The limits column stores a JSON object with optional fields:
--   {"max_memory": <bytes>, "max_disk": <bytes>, "max_cpus": <count>}
-- When NULL or when a specific field is absent, the default limits apply.
-- User limits cannot exceed the support user limits (enforced in application code).

ALTER TABLE users ADD COLUMN limits TEXT;
