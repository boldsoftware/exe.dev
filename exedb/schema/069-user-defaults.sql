-- User defaults with explicit columns per setting
CREATE TABLE user_defaults (
    user_id TEXT PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    new_vm_email INTEGER, -- NULL = not set (use default), 0 = false, 1 = true
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
