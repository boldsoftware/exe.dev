CREATE TABLE checkout_params (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    vm_name TEXT NOT NULL DEFAULT '',
    vm_prompt TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_checkout_params_created_at ON checkout_params(created_at);
