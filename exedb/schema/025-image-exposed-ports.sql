-- Add image_exposed_ports field to tag_resolutions table to persist exposed ports JSON

ALTER TABLE tag_resolutions ADD COLUMN image_exposed_ports TEXT;

-- Record migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (025, '025_image_exposed_ports');
