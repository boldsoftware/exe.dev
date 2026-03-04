CREATE TABLE integrations (
    integration_id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL,
    type TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    config TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (owner_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
CREATE INDEX idx_integrations_owner ON integrations(owner_user_id);
CREATE UNIQUE INDEX idx_integrations_owner_name ON integrations(owner_user_id, name);

CREATE TABLE integration_attachments (
    integration_id TEXT NOT NULL,
    box_id INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (integration_id, box_id),
    FOREIGN KEY (integration_id) REFERENCES integrations(integration_id) ON DELETE CASCADE,
    FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
);
CREATE INDEX idx_integration_attachments_box ON integration_attachments(box_id);
