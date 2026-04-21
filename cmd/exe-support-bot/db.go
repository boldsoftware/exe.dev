package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// dbSchemaSQL is applied unconditionally; all statements are `CREATE IF NOT
// EXISTS` so this is idempotent.
const dbSchemaSQL = `
CREATE TABLE IF NOT EXISTS conversations (
    id TEXT PRIMARY KEY,
    subject TEXT,
    created_at INTEGER,
    last_activity_at INTEGER,
    team_name TEXT,
    assignees_json TEXT,   -- JSON array of {name,email}
    labels_json TEXT,      -- JSON array of {id,name}
    raw_json TEXT,         -- full raw object from Missive list
    closed INTEGER,        -- 1 if closed, 0 if open, NULL if unknown
    first_seen INTEGER NOT NULL,
    last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversations_last_activity
    ON conversations(last_activity_at);

CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    subject TEXT,
    delivered_at INTEGER,
    from_address TEXT,
    from_name TEXT,
    to_json TEXT,
    body_text TEXT,   -- plain-text extraction
    body_html TEXT,   -- raw body (may be HTML)
    raw_json TEXT,
    first_seen INTEGER NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id)
);
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id);
CREATE INDEX IF NOT EXISTS idx_messages_delivered ON messages(delivered_at);

CREATE TABLE IF NOT EXISTS comments (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    author_name TEXT,
    author_email TEXT,
    body TEXT,
    created_at INTEGER,
    raw_json TEXT,
    first_seen INTEGER NOT NULL,
    FOREIGN KEY (conversation_id) REFERENCES conversations(id)
);
CREATE INDEX IF NOT EXISTS idx_comments_conv ON comments(conversation_id);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    subject, body_text, from_address, from_name,
    content='messages', content_rowid='rowid',
    tokenize='porter unicode61'
);
CREATE VIRTUAL TABLE IF NOT EXISTS comments_fts USING fts5(
    body, author_name, author_email,
    content='comments', content_rowid='rowid',
    tokenize='porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, subject, body_text, from_address, from_name)
    VALUES (new.rowid, new.subject, new.body_text, new.from_address, new.from_name);
END;
CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, subject, body_text, from_address, from_name)
    VALUES('delete', old.rowid, old.subject, old.body_text, old.from_address, old.from_name);
  INSERT INTO messages_fts(rowid, subject, body_text, from_address, from_name)
    VALUES (new.rowid, new.subject, new.body_text, new.from_address, new.from_name);
END;
CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, subject, body_text, from_address, from_name)
    VALUES('delete', old.rowid, old.subject, old.body_text, old.from_address, old.from_name);
END;

CREATE TRIGGER IF NOT EXISTS comments_ai AFTER INSERT ON comments BEGIN
  INSERT INTO comments_fts(rowid, body, author_name, author_email)
    VALUES (new.rowid, new.body, new.author_name, new.author_email);
END;
CREATE TRIGGER IF NOT EXISTS comments_au AFTER UPDATE ON comments BEGIN
  INSERT INTO comments_fts(comments_fts, rowid, body, author_name, author_email)
    VALUES('delete', old.rowid, old.body, old.author_name, old.author_email);
  INSERT INTO comments_fts(rowid, body, author_name, author_email)
    VALUES (new.rowid, new.body, new.author_name, new.author_email);
END;
CREATE TRIGGER IF NOT EXISTS comments_ad AFTER DELETE ON comments BEGIN
  INSERT INTO comments_fts(comments_fts, rowid, body, author_name, author_email)
    VALUES('delete', old.rowid, old.body, old.author_name, old.author_email);
END;

CREATE TABLE IF NOT EXISTS scrape_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    convs_seen INTEGER NOT NULL DEFAULT 0,
    msgs_seen  INTEGER NOT NULL DEFAULT 0,
    comments_seen INTEGER NOT NULL DEFAULT 0,
    error TEXT
);

CREATE TABLE IF NOT EXISTS results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL,
    conversation_id TEXT,
    prompt TEXT,
    output TEXT NOT NULL,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0,
    steps_json TEXT -- JSON log of agent steps for display
);
CREATE INDEX IF NOT EXISTS idx_results_created ON results(created_at DESC);

CREATE TABLE IF NOT EXISTS docs_cache (
    path TEXT PRIMARY KEY,
    fetched_at INTEGER NOT NULL,
    body TEXT NOT NULL
);
`

func openDB(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// With WAL + busy_timeout multiple readers are fine. We cap writers to 1.
	db.SetMaxOpenConns(5)
	if _, err := db.Exec(dbSchemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return db, nil
}
