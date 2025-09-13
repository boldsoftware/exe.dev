-- Add image_labels field to tag_resolutions table to persist image label JSON

ALTER TABLE tag_resolutions ADD COLUMN image_labels TEXT;

-- Record migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (021, '021_image_labels');

