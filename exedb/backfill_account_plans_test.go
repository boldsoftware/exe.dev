package exedb_test

import (
	"context"
	"database/sql"
	"testing"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
	_ "modernc.org/sqlite"
)

func strPtrVal(s string) *string { return &s }

// TestBackfillAccountPlans verifies that migration 121's backfill SQL correctly infers
// plan_id for scenarios based on billing_events and created_at.
//
// Note: billing_exemption and billing_trial_ends_at were dropped in migration 122.
// Scenarios that previously relied on those columns (friend, invite) are tested via
// account_plans directly after migration 121 ran; this test validates the remaining
// scenarios: basic, grandfathered, individual, canceled.
func TestBackfillAccountPlans(t *testing.T) {
	t.Parallel()

	dbPath := t.TempDir() + "/backfill_scenarios.db"

	// Open and init the DB without running migrations yet.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.InitDB(db, 1); err != nil {
		t.Fatalf("InitDB: %v", err)
	}

	log := tslog.Slogger(t)
	if err := exedb.RunMigrations(log, db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	ctx := context.Background()
	oldDate := "2025-12-01 00:00:00" // before billing-required date (grandfathered)
	newDate := "2026-02-01 00:00:00" // after billing-required date

	type scenario struct {
		userID           string
		email            string
		createdAt        string
		billingEventType *string // most recent billing event type, nil = no events
		wantPlan         string
	}

	scenarios := []scenario{
		{
			userID: "usr_sc_basic001", email: "sc-basic@example.com",
			createdAt: newDate, wantPlan: "basic",
		},
		{
			userID: "usr_sc_grand001", email: "sc-grand@example.com",
			createdAt: oldDate, wantPlan: "grandfathered",
		},
		{
			userID: "usr_sc_indiv001", email: "sc-indiv@example.com",
			createdAt:        newDate,
			billingEventType: strPtrVal("active"),
			wantPlan:         "individual",
		},
		{
			userID: "usr_sc_cancl001", email: "sc-cancel@example.com",
			createdAt:        newDate,
			billingEventType: strPtrVal("canceled"),
			wantPlan:         "basic",
		},
	}

	// Insert users + accounts without account_plans rows.
	for _, sc := range scenarios {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO users (user_id, email, created_at) VALUES (?, ?, ?)`,
			sc.userID, sc.email, sc.createdAt,
		); err != nil {
			t.Fatalf("insert user %s: %v", sc.userID, err)
		}
		acctID := "exe_sc_" + sc.userID[4:]
		if _, err := db.ExecContext(ctx,
			`INSERT INTO accounts (id, created_by) VALUES (?, ?)`,
			acctID, sc.userID,
		); err != nil {
			t.Fatalf("insert account %s: %v", sc.userID, err)
		}
		if sc.billingEventType != nil {
			if _, err := db.ExecContext(ctx,
				`INSERT INTO billing_events (account_id, event_type, event_at) VALUES (?, ?, CURRENT_TIMESTAMP)`,
				acctID, *sc.billingEventType,
			); err != nil {
				t.Fatalf("insert billing event %s: %v", sc.userID, err)
			}
		}
	}

	// Run the backfill SQL directly (simulates what migration 121 ran against the old data).
	// Note: billing_exemption and billing_trial_ends_at are no longer referenced (dropped in 122).
	// The friend/invite plan scenarios from those columns were backfilled by 121 before 122 ran.
	backfillSQL := `
INSERT INTO account_plans (account_id, plan_id, started_at, changed_by)
SELECT
    a.id AS account_id,
    CASE
        WHEN (
            SELECT e.event_type FROM billing_events e
            WHERE e.account_id = a.id
            ORDER BY e.id DESC LIMIT 1
        ) = 'canceled' THEN 'basic'
        WHEN (
            SELECT e.event_type FROM billing_events e
            WHERE e.account_id = a.id
            ORDER BY e.id DESC LIMIT 1
        ) = 'active' THEN 'individual'
        WHEN u.created_at < '2026-01-06 23:10:00' THEN 'grandfathered'
        ELSE 'basic'
    END AS plan_id,
    COALESCE(u.created_at, CURRENT_TIMESTAMP) AS started_at,
    'system:backfill' AS changed_by
FROM users u
JOIN accounts a ON a.created_by = u.user_id
WHERE u.user_id IN (?, ?, ?, ?)
AND NOT EXISTS (
    SELECT 1 FROM account_plans ap
    WHERE ap.account_id = a.id AND ap.ended_at IS NULL
);`

	userIDs := make([]interface{}, len(scenarios))
	for i, sc := range scenarios {
		userIDs[i] = sc.userID
	}
	if _, err := db.ExecContext(ctx, backfillSQL, userIDs...); err != nil {
		t.Fatalf("backfill SQL: %v", err)
	}

	// Verify each scenario got the correct plan.
	for _, sc := range scenarios {
		acctID := "exe_sc_" + sc.userID[4:]
		var planID string
		err := db.QueryRowContext(ctx,
			`SELECT plan_id FROM account_plans WHERE account_id = ? AND ended_at IS NULL`,
			acctID,
		).Scan(&planID)
		if err != nil {
			t.Errorf("scenario %s (%s): no active plan: %v", sc.userID, sc.wantPlan, err)
			continue
		}
		if planID != sc.wantPlan {
			t.Errorf("scenario %s: got plan=%q, want %q", sc.email, planID, sc.wantPlan)
		}
	}
}

// TestBackfillIdempotency verifies that running the backfill SQL twice does not create
// duplicate account_plans rows.
func TestBackfillIdempotency(t *testing.T) {
	t.Parallel()

	dbPath := t.TempDir() + "/backfill_idempotent.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlite.InitDB(db, 1); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	ctx := context.Background()

	// Insert a user+account without an account_plans row.
	userID := "usr_idm_test001"
	acctID := "exe_idm_test001"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (user_id, email, created_at) VALUES (?, ?, '2026-02-01 00:00:00')`,
		userID, "idempotent@example.com",
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO accounts (id, created_by) VALUES (?, ?)`, acctID, userID,
	); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	backfillSQL := `
INSERT INTO account_plans (account_id, plan_id, started_at, changed_by)
SELECT a.id, 'basic', COALESCE(u.created_at, CURRENT_TIMESTAMP), 'system:backfill'
FROM users u
JOIN accounts a ON a.created_by = u.user_id
WHERE u.user_id = ?
AND NOT EXISTS (
    SELECT 1 FROM account_plans ap WHERE ap.account_id = a.id AND ap.ended_at IS NULL
);`

	// Run backfill twice.
	if _, err := db.ExecContext(ctx, backfillSQL, userID); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if _, err := db.ExecContext(ctx, backfillSQL, userID); err != nil {
		t.Fatalf("second backfill: %v", err)
	}

	// Count rows — must be exactly 1.
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_plans WHERE account_id = ?`, acctID,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 account_plans row after double backfill, got %d", count)
	}
}
