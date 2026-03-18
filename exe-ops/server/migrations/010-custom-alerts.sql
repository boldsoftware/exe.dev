CREATE TABLE custom_alerts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    metric TEXT NOT NULL,
    operator TEXT NOT NULL CHECK(operator IN ('>', '<', '>=', '<=', '==', '!=')),
    threshold REAL NOT NULL,
    severity TEXT NOT NULL DEFAULT 'warning' CHECK(severity IN ('warning', 'critical')),
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
