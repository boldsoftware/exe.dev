package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBillingEventStorage(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	p, err := New(dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// Create billing_events table
	err = p.Tx(context.Background(), func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `
			CREATE TABLE billing_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				account_id TEXT NOT NULL,
				event_type TEXT NOT NULL,
				event_at DATETIME NOT NULL
			)
		`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Insert two events with different timestamps.
	// The driver formats these as "YYYY-MM-DD HH:MM:SS" in UTC
	// thanks to _time_format=datetime&_timezone=UTC in the DSN.
	t1 := time.Date(2026, 1, 24, 15, 28, 22, 0, time.FixedZone("PST", -8*3600))
	t2 := time.Date(2026, 1, 24, 15, 28, 48, 0, time.FixedZone("PST", -8*3600))

	err = p.Tx(context.Background(), func(ctx context.Context, tx *Tx) error {
		_, err := tx.Conn().ExecContext(ctx, "INSERT INTO billing_events (account_id, event_type, event_at) VALUES (?, ?, ?)",
			"acct1", "active", t1)
		if err != nil {
			return err
		}
		_, err = tx.Conn().ExecContext(ctx, "INSERT INTO billing_events (account_id, event_type, event_at) VALUES (?, ?, ?)",
			"acct1", "canceled", t2)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var eventType string
	err = p.Rx(context.Background(), func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`
			SELECT event_type
			FROM billing_events
			WHERE account_id = ?
			ORDER BY event_at DESC
			LIMIT 1
		`, "acct1").Scan(&eventType)
	})
	if err != nil {
		t.Fatal(err)
	}

	if eventType != "canceled" {
		t.Errorf("Expected latest event_type to be 'canceled', got %q", eventType)
	}
}
