-- Add a comment column to integrations. Set at creation time.
-- Surfaced through reflection integrations and `integrations list`.
ALTER TABLE integrations ADD COLUMN comment TEXT NOT NULL DEFAULT '';
