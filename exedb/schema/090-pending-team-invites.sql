CREATE TABLE pending_team_invites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    email TEXT NOT NULL,
    canonical_email TEXT NOT NULL,
    invited_by_user_id TEXT NOT NULL REFERENCES users(user_id),
    token TEXT NOT NULL UNIQUE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accepted_at DATETIME,
    accepted_by_user_id TEXT,
    UNIQUE(team_id, canonical_email)
);
CREATE INDEX idx_pending_team_invites_canonical_email ON pending_team_invites(canonical_email);
CREATE INDEX idx_pending_team_invites_token ON pending_team_invites(token);
