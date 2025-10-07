-- Rename description to prompt in mobile_pending_vm table
ALTER TABLE mobile_pending_vm RENAME COLUMN description TO prompt;

-- Add creation_log column to boxes table to store creation output
ALTER TABLE boxes ADD COLUMN creation_log TEXT;

-- Record migration as executed
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (028, '028_mobile_pending_prompt');
