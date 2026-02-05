-- Add is_locked_out field to users table
-- When set to 1, the user is locked out and cannot log in via web or SSH

ALTER TABLE users ADD COLUMN is_locked_out INTEGER NOT NULL DEFAULT 0;
