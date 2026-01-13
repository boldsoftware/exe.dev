-- Add discord_id column to users table for Discord account linking
ALTER TABLE users ADD COLUMN discord_id TEXT;
CREATE INDEX idx_users_discord_id ON users(discord_id);
