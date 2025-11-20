PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT UNIQUE,

    CONSTRAINT valid_user_id CHECK (id LIKE 'user_%')
);

CREATE TABLE IF NOT EXISTS ssh_keys (
    public_key TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    last_used_at DATETIME NOT NULL,
    CONSTRAINT ssh_key_has_user FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS email_verifications (
    code TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    public_key TEXT, -- If null, it's for general email verification and no SSH key is associated on completion
    created_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,

    CONSTRAINT check_valid_email_format CHECK (email LIKE '%_@__%.__%'),

    CONSTRAINT fk_email_verifications_ssh_keys FOREIGN KEY (public_key) REFERENCES ssh_keys(public_key)
);
