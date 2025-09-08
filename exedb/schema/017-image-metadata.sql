-- Add image metadata fields to tag_resolutions table
-- These fields cache the image configuration (user, entrypoint, cmd) 
-- that was previously stored in the imageCache map in NerdctlManager

ALTER TABLE tag_resolutions ADD COLUMN image_user TEXT;
ALTER TABLE tag_resolutions ADD COLUMN image_entrypoint TEXT; -- JSON array
ALTER TABLE tag_resolutions ADD COLUMN image_cmd TEXT; -- JSON array

-- Create index for faster lookups by host-specific keys
CREATE INDEX IF NOT EXISTS idx_tag_resolutions_host_lookup 
    ON tag_resolutions(registry, repository, tag, platform);

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (017, '017_image_metadata');