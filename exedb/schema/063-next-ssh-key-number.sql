-- Add next_ssh_key_number to users table for generating key-N comments
ALTER TABLE users ADD COLUMN next_ssh_key_number INTEGER NOT NULL DEFAULT 1;
