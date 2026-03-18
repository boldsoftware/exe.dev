CREATE TABLE IF NOT EXISTS servers (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL UNIQUE,
    hostname   TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT '',
    region     TEXT NOT NULL DEFAULT '',
    env        TEXT NOT NULL DEFAULT '',
    instance   TEXT NOT NULL DEFAULT '',
    tags       TEXT NOT NULL DEFAULT '[]',
    first_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS reports (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id    INTEGER NOT NULL REFERENCES servers(id),
    timestamp    DATETIME NOT NULL,
    cpu_percent  REAL NOT NULL,
    mem_total    INTEGER NOT NULL,
    mem_used     INTEGER NOT NULL,
    mem_free     INTEGER NOT NULL,
    mem_swap     INTEGER NOT NULL,
    disk_total   INTEGER NOT NULL,
    disk_used    INTEGER NOT NULL,
    disk_free    INTEGER NOT NULL,
    net_send     INTEGER NOT NULL,
    net_recv     INTEGER NOT NULL,
    zfs_used     INTEGER,
    zfs_free     INTEGER,
    uptime_secs  INTEGER NOT NULL DEFAULT 0,
    components   TEXT NOT NULL DEFAULT '[]',
    updates      TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_reports_server_time ON reports(server_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_reports_timestamp ON reports(timestamp);
