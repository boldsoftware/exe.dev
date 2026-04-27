-- Add reasoning_effort column to custom models.
-- Free-form string sent as reasoning.effort to the OpenAI Responses API.
-- Empty string means "use default" (medium).

ALTER TABLE models ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT '';
