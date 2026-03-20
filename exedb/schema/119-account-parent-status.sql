-- Add parent_id and status columns to accounts table.
-- parent_id: NULL = top-level/individual account. When set, points to the parent account
--   (e.g., a team member's account points to the team's account).
-- status: account operational status. Canceling a subscription is a plan change to Basic,
--   not a status change. 'restricted' means admin-flagged; 'past_due' means payment overdue.
ALTER TABLE accounts ADD COLUMN parent_id TEXT REFERENCES accounts(id);
ALTER TABLE accounts ADD COLUMN status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'restricted', 'past_due'));
