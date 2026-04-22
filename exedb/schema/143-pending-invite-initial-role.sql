-- Allows team admins to pre-select the role a pending invite will grant when accepted.
-- Values: 'user' | 'admin' | 'billing_owner'. NULL / empty means 'user' for backwards compat.
ALTER TABLE pending_team_invites ADD COLUMN initial_role TEXT;
