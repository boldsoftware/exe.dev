package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeTime(t *testing.T) {
	// Create a time with monotonic clock
	t1 := time.Now()

	// Normalize it
	t2 := NormalizeTime(t1)

	// Should be equal in wall clock time
	if !t1.Equal(t2) {
		t.Errorf("NormalizeTime changed the time: got %v, want %v", t2, t1)
	}

	// Should be in UTC
	if t2.Location() != time.UTC {
		t.Errorf("NormalizeTime not in UTC: got %v", t2.Location())
	}

	// String representation should not have monotonic clock (m=+...)
	s := t2.String()
	if len(s) > 50 && len(s) > len(t1.String()) {
		t.Errorf("NormalizeTime string suspiciously long: %q", s)
	}
	// More importantly, check it doesn't contain the monotonic marker
	for i := 0; i < len(s)-2; i++ {
		if s[i:i+2] == "m=" {
			t.Errorf("NormalizeTime has monotonic clock: %q", s)
		}
	}
}

func TestParseTimestampFunction(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	p, err := New(dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	tests := []struct {
		input string
		want  string
	}{
		{
			// Go time.String() format with monotonic clock
			input: "2026-01-24 15:28:48.123456789 -0800 PST m=+123.456",
			want:  "2026-01-24 23:28:48.123456789+00:00",
		},
		{
			// Go time.String() format without monotonic clock
			input: "2026-01-24 15:28:48 -0800 PST",
			want:  "2026-01-24 23:28:48+00:00",
		},
		{
			// SQLite CURRENT_TIMESTAMP format (UTC, no offset)
			input: "2026-01-24 23:28:24",
			want:  "2026-01-24 23:28:24+00:00",
		},
		{
			// Time10 format
			input: "2026-01-24 23:28:24.123456789+00:00",
			want:  "2026-01-24 23:28:24.123456789+00:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var result string
			err := p.Rx(context.Background(), func(ctx context.Context, rx *Rx) error {
				return rx.QueryRow("SELECT parse_timestamp(?)", tt.input).Scan(&result)
			})
			if err != nil {
				t.Fatalf("parse_timestamp(%q) error: %v", tt.input, err)
			}
			if result != tt.want {
				t.Errorf("parse_timestamp(%q) = %q, want %q", tt.input, result, tt.want)
			}
		})
	}
}

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

	// Insert two events with different timestamps
	t1 := time.Date(2026, 1, 24, 15, 28, 22, 0, time.FixedZone("PST", -8*3600))
	t2 := time.Date(2026, 1, 24, 15, 28, 48, 0, time.FixedZone("PST", -8*3600))

	err = p.Tx(context.Background(), func(ctx context.Context, tx *Tx) error {
		// Insert with normalized times
		_, err := tx.Conn().ExecContext(ctx, "INSERT INTO billing_events (account_id, event_type, event_at) VALUES (?, ?, ?)",
			"acct1", "active", NormalizeTime(t1))
		if err != nil {
			return err
		}
		_, err = tx.Conn().ExecContext(ctx, "INSERT INTO billing_events (account_id, event_type, event_at) VALUES (?, ?, ?)",
			"acct1", "canceled", NormalizeTime(t2))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Query with parse_timestamp to get the latest
	var eventType string
	err = p.Rx(context.Background(), func(ctx context.Context, rx *Rx) error {
		return rx.QueryRow(`
			SELECT event_type
			FROM billing_events
			WHERE account_id = ?
			ORDER BY parse_timestamp(event_at) DESC
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
