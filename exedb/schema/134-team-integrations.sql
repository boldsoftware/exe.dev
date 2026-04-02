-- Add team_id column to integrations table for team-owned integrations.
-- When team_id IS NULL, the integration is personal (owned by owner_user_id).
-- When team_id IS NOT NULL, the integration belongs to a team.
ALTER TABLE integrations ADD COLUMN team_id TEXT REFERENCES teams(team_id) ON DELETE CASCADE;

-- Team integrations must have unique names within a team.
CREATE UNIQUE INDEX idx_integrations_team_name ON integrations(team_id, name) WHERE team_id IS NOT NULL;
