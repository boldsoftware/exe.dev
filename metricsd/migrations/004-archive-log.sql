CREATE TABLE IF NOT EXISTS archive_log (
	day        DATE PRIMARY KEY,
	file_path  VARCHAR NOT NULL,
	row_count  BIGINT NOT NULL,
	archived_at TIMESTAMP DEFAULT current_timestamp
);
