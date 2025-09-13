-- Add image_login_user field to tag_resolutions table
-- This field stores the exe.dev/login-user label value for SSH user mapping

ALTER TABLE tag_resolutions ADD COLUMN image_login_user TEXT;

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) VALUES (020, '020-image-login-user');
