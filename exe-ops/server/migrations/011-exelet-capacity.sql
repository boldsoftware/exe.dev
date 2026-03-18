CREATE TABLE IF NOT EXISTS exelet_capacity (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_name TEXT NOT NULL,
    env TEXT NOT NULL,
    timestamp DATETIME NOT NULL,
    instances INTEGER NOT NULL,
    capacity INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_exelet_capacity_server_time ON exelet_capacity(server_name, timestamp);
CREATE INDEX IF NOT EXISTS idx_exelet_capacity_timestamp ON exelet_capacity(timestamp);
