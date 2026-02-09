CREATE TABLE teams (
    team_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    limits TEXT,  -- JSON: {"max_boxes": n, "max_memory": bytes, "max_disk": bytes, "max_cpus": n}
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE team_members (
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    user_id TEXT NOT NULL REFERENCES users(user_id),
    role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('owner', 'user')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (team_id, user_id),
    UNIQUE (user_id)  -- One team per user
);

CREATE INDEX idx_team_members_team_id ON team_members(team_id);

CREATE TABLE box_team_shares (
    box_id INTEGER NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    shared_by TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (box_id, team_id)
);
