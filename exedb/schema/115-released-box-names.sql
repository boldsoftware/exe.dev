CREATE TABLE released_box_names (
    name TEXT NOT NULL,
    box_id INTEGER NOT NULL,
    user_id TEXT NOT NULL,
    released_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (name),
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
