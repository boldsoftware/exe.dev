-- Track when an invite code was allocated (shown to the user for distribution)
ALTER TABLE invite_codes ADD COLUMN allocated_at TIMESTAMP;
