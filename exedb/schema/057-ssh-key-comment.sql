-- Add comment column to ssh_keys table to store the user@host comment
ALTER TABLE ssh_keys ADD COLUMN comment TEXT;
