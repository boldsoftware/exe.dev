-- Store the absolute path to the Maildir directory for inbound email.
-- This is resolved from $HOME when email receiving is enabled, ensuring
-- consistency across BYO images with non-standard home directories.
-- Empty string means not configured (valid paths are always absolute).
ALTER TABLE boxes ADD COLUMN email_maildir_path TEXT NOT NULL DEFAULT '';
