-- Private exelets: exelets that are excluded from normal scheduling.
-- An exelet address appearing here means no user will be scheduled onto it
-- unless a team_exelets row explicitly grants access.
CREATE TABLE private_exelets (
    exelet_addr TEXT PRIMARY KEY,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Team exelets: maps a team to the exelet addresses it may use.
-- If the exelet is private, this overrides the private bit for that team's members.
-- If the exelet is not private, this has no scheduling effect (all users can already use it).
CREATE TABLE team_exelets (
    team_id TEXT NOT NULL REFERENCES teams(team_id),
    exelet_addr TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, exelet_addr)
);
CREATE INDEX idx_team_exelets_addr ON team_exelets(exelet_addr);
