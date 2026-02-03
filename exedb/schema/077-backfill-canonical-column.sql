-- Add canonical_email column for deduplication lookups.
-- The original email column is preserved for sending emails to the user's preferred address.
-- Lookups should use canonical_email (after canonicalizing the input).
--
-- NOTE: Index is NOT unique initially. After backfill completes and any duplicates
-- are manually resolved, add uniqueness via a separate migration.

ALTER TABLE users ADD COLUMN canonical_email TEXT;

-- Backfill existing users
UPDATE users SET canonical_email = LOWER(
  CASE
    WHEN email LIKE '%@googlemail.com' THEN REPLACE(email, '@googlemail.com', '@gmail.com')
    WHEN email LIKE '%@google.com' THEN REPLACE(email, '@google.com', '@gmail.com')
    WHEN email LIKE '%@hotmail.com' THEN REPLACE(email, '@hotmail.com', '@outlook.com')
    WHEN email LIKE '%@live.com' THEN REPLACE(email, '@live.com', '@outlook.com')
    WHEN email LIKE '%@ymail.com' THEN REPLACE(email, '@ymail.com', '@yahoo.com')
    WHEN email LIKE '%@proton.me' THEN REPLACE(email, '@proton.me', '@protonmail.com')
    WHEN email LIKE '%@pm.me' THEN REPLACE(email, '@pm.me', '@protonmail.com')
    WHEN email LIKE '%@me.com' THEN REPLACE(email, '@me.com', '@icloud.com')
    WHEN email LIKE '%@mac.com' THEN REPLACE(email, '@mac.com', '@icloud.com')
    WHEN email LIKE '%@fastmail.fm' THEN REPLACE(email, '@fastmail.fm', '@fastmail.com')
    ELSE email
  END
);

CREATE INDEX idx_users_canonical_email ON users(canonical_email);
