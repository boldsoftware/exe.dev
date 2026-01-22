package exedb

import "database/sql"

// testCodeMigration is a trivial code migration that verifies
// the code migration infrastructure works. It creates a simple
// table that can be checked in tests.
func testCodeMigration(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		CREATE TABLE IF NOT EXISTS code_migration_test (
			id INTEGER PRIMARY KEY,
			message TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		INSERT OR IGNORE INTO code_migration_test (id, message) VALUES (1, 'code migration ran successfully')
	`)
	if err != nil {
		return err
	}

	return tx.Commit()
}
