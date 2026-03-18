package server

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001-initial.sql
var migration001 string

//go:embed migrations/002-agent-version.sql
var migration002 string

//go:embed migrations/003-backup-zfs.sql
var migration003 string

//go:embed migrations/004-failed-units.sql
var migration004 string

//go:embed migrations/005-monitoring-phase1.sql
var migration005 string

//go:embed migrations/006-swap-total.sql
var migration006 string

//go:embed migrations/007-zfs-pools.sql
var migration007 string

//go:embed migrations/008-net-counter-baselines.sql
var migration008 string

//go:embed migrations/009-chat.sql
var migration009 string

//go:embed migrations/010-custom-alerts.sql
var migration010 string

//go:embed migrations/011-exelet-capacity.sql
var migration011 string

// OpenDB opens a SQLite database and applies migrations.
func OpenDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite only supports a single writer. Limiting to one connection
	// serializes all access through Go's sql.DB, eliminating SQLITE_BUSY
	// errors under concurrent load (e.g. many agents reporting at once).
	db.SetMaxOpenConns(1)

	// Enable WAL mode and set busy timeout.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %q: %w", pragma, err)
		}
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(migration001)
	if err != nil {
		return fmt.Errorf("apply migration 001: %w", err)
	}

	// Migration 002: add agent_version, arch, upgrade_pending columns.
	// Check if columns already exist before applying.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name = 'agent_version'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 002: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration002)
		if err != nil {
			return fmt.Errorf("apply migration 002: %w", err)
		}
	}

	// Migration 003: add backup_zfs_used, backup_zfs_free columns.
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('reports') WHERE name = 'backup_zfs_used'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 003: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration003)
		if err != nil {
			return fmt.Errorf("apply migration 003: %w", err)
		}
	}

	// Migration 004: add failed_units column.
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('reports') WHERE name = 'failed_units'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 004: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration004)
		if err != nil {
			return fmt.Errorf("apply migration 004: %w", err)
		}
	}

	// Migration 005: add monitoring phase 1 columns.
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('reports') WHERE name = 'load_avg_1'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 005: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration005)
		if err != nil {
			return fmt.Errorf("apply migration 005: %w", err)
		}
	}

	// Migration 006: add mem_swap_total column.
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('reports') WHERE name = 'mem_swap_total'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 006: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration006)
		if err != nil {
			return fmt.Errorf("apply migration 006: %w", err)
		}
	}

	// Migration 007: add zfs_pools column.
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('reports') WHERE name = 'zfs_pools'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 007: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration007)
		if err != nil {
			return fmt.Errorf("apply migration 007: %w", err)
		}
	}

	// Migration 008: add net counter baseline columns to servers.
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('servers') WHERE name = 'net_rx_errors_baseline'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 008: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration008)
		if err != nil {
			return fmt.Errorf("apply migration 008: %w", err)
		}
	}

	// Migration 009: add chat tables.
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chat_conversations'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 009: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration009)
		if err != nil {
			return fmt.Errorf("apply migration 009: %w", err)
		}
	}

	// Migration 010: add custom_alerts table.
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='custom_alerts'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 010: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration010)
		if err != nil {
			return fmt.Errorf("apply migration 010: %w", err)
		}
	}

	// Migration 011: add exelet_capacity table.
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='exelet_capacity'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check migration 011: %w", err)
	}
	if count == 0 {
		_, err = db.Exec(migration011)
		if err != nil {
			return fmt.Errorf("apply migration 011: %w", err)
		}
	}

	return nil
}
