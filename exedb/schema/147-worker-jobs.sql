CREATE TABLE worker_jobs (
    id INTEGER PRIMARY KEY,
    event TEXT NOT NULL,
    payload BLOB,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_worker_jobs_status ON worker_jobs(status);
