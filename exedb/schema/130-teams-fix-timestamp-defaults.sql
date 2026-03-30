-- Fix teams-related tables to use CURRENT_TIMESTAMP default,
-- matching every other table in the schema.
-- These tables originally used strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
-- which produces ISO 8601 format with a 'T' separator.

CREATE TABLE teams_new (
    team_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    limits TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    auth_provider TEXT
);
INSERT INTO teams_new SELECT * FROM teams;
DROP TABLE teams;
ALTER TABLE teams_new RENAME TO teams;

CREATE TABLE team_members_new (
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    user_id TEXT NOT NULL REFERENCES users(user_id),
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('billing_owner', 'admin', 'user')),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, user_id),
    UNIQUE (user_id)
);
INSERT INTO team_members_new SELECT * FROM team_members;
DROP TABLE team_members;
ALTER TABLE team_members_new RENAME TO team_members;
CREATE INDEX idx_team_members_team_id ON team_members(team_id);

CREATE TABLE box_team_shares_new (
    box_id INTEGER NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    shared_by TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (box_id, team_id)
);
INSERT INTO box_team_shares_new SELECT * FROM box_team_shares;
DROP TABLE box_team_shares;
ALTER TABLE box_team_shares_new RENAME TO box_team_shares;
