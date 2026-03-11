-- Rename team role "sudoer" to "admin".

CREATE TABLE team_members_new (
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    user_id TEXT NOT NULL REFERENCES users(user_id),
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('billing_owner', 'admin', 'user')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (team_id, user_id),
    UNIQUE (user_id)
);

INSERT INTO team_members_new (team_id, user_id, role, created_at)
SELECT team_id, user_id,
    CASE WHEN role = 'sudoer' THEN 'admin' ELSE role END,
    created_at
FROM team_members;

DROP TABLE team_members;
ALTER TABLE team_members_new RENAME TO team_members;
CREATE INDEX idx_team_members_team_id ON team_members(team_id);
