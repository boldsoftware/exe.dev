-- Prefix all team_ids with 'tm_' where not already prefixed.
-- FK enforcement is OFF, so we update all tables independently.

UPDATE team_members SET team_id = 'tm_' || team_id WHERE team_id NOT LIKE 'tm_%';
UPDATE box_team_shares SET team_id = 'tm_' || team_id WHERE team_id NOT LIKE 'tm_%';
UPDATE pending_team_invites SET team_id = 'tm_' || team_id WHERE team_id NOT LIKE 'tm_%';
UPDATE team_sso_providers SET team_id = 'tm_' || team_id WHERE team_id NOT LIKE 'tm_%';
UPDATE teams SET team_id = 'tm_' || team_id WHERE team_id NOT LIKE 'tm_%';
