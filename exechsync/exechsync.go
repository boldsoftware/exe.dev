// Package exechsync syncs a small subset of the exed SQLite database to a
// ClickHouse data warehouse on a daily cadence. See README.md.
package exechsync

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tracing"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/prometheus/client_golang/prometheus"
)

const maxRows = 1_000_000

var (
	rowsDesc = prometheus.NewDesc(
		"clickhouse_sync_rows",
		"Number of rows synced to ClickHouse.",
		[]string{"table"}, nil,
	)
	failuresDesc = prometheus.NewDesc(
		"clickhouse_sync_failures_total",
		"Total number of ClickHouse sync failures.",
		[]string{"table"}, nil,
	)
)

// collector exposes clickhouse sync metrics via Prometheus.
type collector struct {
	rows     map[string]int   // last successful row count per table
	failures map[string]int64 // cumulative failure count per table
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- rowsDesc
	ch <- failuresDesc
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	for table, n := range c.rows {
		ch <- prometheus.MustNewConstMetric(rowsDesc, prometheus.GaugeValue, float64(n), table)
	}
	for table, n := range c.failures {
		ch <- prometheus.MustNewConstMetric(failuresDesc, prometheus.CounterValue, float64(n), table)
	}
}

// Config configures a daily sync run.
type Config struct {
	DSN      string // clickhouse DSN; if empty, Start is a no-op.
	DB       *sqlite.DB
	Logger   *slog.Logger
	Registry *prometheus.Registry
}

// Start runs a daily background sync of users, teams, team_members, accounts,
// account_plans, and boxes into a ClickHouse "prod" database. Each row is
// tagged with extract_date. On startup, syncs immediately if today's data is
// missing. Blocks until ctx is canceled.
func Start(ctx context.Context, cfg Config) {
	if cfg.DSN == "" {
		return
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	opts, err := clickhouse.ParseDSN(cfg.DSN)
	if err != nil {
		log.ErrorContext(ctx, "clickhouse_sync: bad DSN", "error", err)
		return
	}
	opts.TLS = &tls.Config{}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		log.ErrorContext(ctx, "clickhouse_sync: failed to open", "error", err)
		return
	}
	defer conn.Close()

	if err := conn.Ping(ctx); err != nil {
		log.ErrorContext(ctx, "clickhouse_sync: ping failed", "error", err)
		return
	}

	if err := CreateTables(ctx, conn); err != nil {
		log.ErrorContext(ctx, "clickhouse_sync: create tables failed", "error", err)
		return
	}

	c := &collector{
		rows:     make(map[string]int),
		failures: make(map[string]int64),
	}
	if cfg.Registry != nil {
		cfg.Registry.MustRegister(c)
	}

	s := &syncer{db: cfg.DB, log: log, c: c}

	// Check if today's data already exists (use users table as sentinel).
	today := time.Now().UTC().Truncate(24 * time.Hour)
	var count uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM users WHERE extract_date = ?", today).Scan(&count); err != nil {
		log.ErrorContext(ctx, "clickhouse_sync: check failed", "error", err)
		return
	}
	if count > 0 {
		log.InfoContext(ctx, "clickhouse_sync: today's data already exists, skipping startup sync", "rows", count)
	} else {
		s.once(ctx, conn, today)
	}

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			day := time.Now().UTC().Truncate(24 * time.Hour)
			s.once(ctx, conn, day)
		}
	}
}

// CreateTables (re)creates the ClickHouse tables and *_latest views. Idempotent.
func CreateTables(ctx context.Context, conn clickhouse.Conn) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			extract_date Date,
			user_id String,
			email String
		) ENGINE = ReplacingMergeTree()
		ORDER BY (extract_date, user_id)`,

		`CREATE TABLE IF NOT EXISTS teams (
			extract_date Date,
			team_id String,
			display_name String
		) ENGINE = ReplacingMergeTree()
		ORDER BY (extract_date, team_id)`,

		`CREATE TABLE IF NOT EXISTS team_members (
			extract_date Date,
			team_id String,
			user_id String,
			role String
		) ENGINE = ReplacingMergeTree()
		ORDER BY (extract_date, team_id, user_id)`,

		`CREATE TABLE IF NOT EXISTS accounts (
			extract_date Date,
			id String,
			created_by String,
			parent_id String
		) ENGINE = ReplacingMergeTree()
		ORDER BY (extract_date, id)`,

		`CREATE TABLE IF NOT EXISTS account_plans (
			extract_date Date,
			account_id String,
			plan_id String,
			started_at DateTime,
			ended_at Nullable(DateTime),
			trial_expires_at Nullable(DateTime)
		) ENGINE = ReplacingMergeTree()
		ORDER BY (extract_date, account_id, started_at)`,

		`CREATE TABLE IF NOT EXISTS boxes (
			extract_date Date,
			name String,
			created_by_user_id String,
			status String,
			region String
		) ENGINE = ReplacingMergeTree()
		ORDER BY (extract_date, name)`,
	}
	for _, stmt := range stmts {
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("exec: %w", err)
		}
	}
	return createViews(ctx, conn)
}

// LatestViews pairs each base table with its *_latest view name. Each view
// shows only rows from the most recent extract_date.
var LatestViews = []struct {
	View, Table string
}{
	{"users_latest", "users"},
	{"teams_latest", "teams"},
	{"team_members_latest", "team_members"},
	{"accounts_latest", "accounts"},
	{"account_plans_latest", "account_plans"},
	{"boxes_latest", "boxes"},
}

// createViews (re)creates *_latest views pointing at the most recent
// extract_date in each base table. Uses CREATE OR REPLACE so schema changes to
// underlying tables are picked up on next boot.
func createViews(ctx context.Context, conn clickhouse.Conn) error {
	for _, v := range LatestViews {
		stmt := fmt.Sprintf(
			`CREATE OR REPLACE VIEW %s AS SELECT * FROM %s WHERE extract_date = (SELECT max(extract_date) FROM %s)`,
			v.View, v.Table, v.Table,
		)
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("create view %s: %w", v.View, err)
		}
	}
	return nil
}

type syncer struct {
	db  *sqlite.DB
	log *slog.Logger
	c   *collector
}

func (s *syncer) once(ctx context.Context, conn clickhouse.Conn, today time.Time) {
	ctx = tracing.ContextWithTraceID(ctx, tracing.GenerateTraceID())
	start := time.Now()

	tables := []struct {
		name string
		fn   func(context.Context, clickhouse.Conn, time.Time) (int, error)
	}{
		{"users", s.syncUsers},
		{"teams", s.syncTeams},
		{"team_members", s.syncTeamMembers},
		{"accounts", s.syncAccounts},
		{"account_plans", s.syncAccountPlans},
		{"boxes", s.syncBoxes},
	}

	attrs := []any{"duration", nil}
	for _, t := range tables {
		n, err := t.fn(ctx, conn, today)
		if err != nil {
			s.c.failures[t.name]++
			s.log.ErrorContext(ctx, "clickhouse_sync: failed", "table", t.name, "error", err)
			return
		}
		s.c.rows[t.name] = n
		attrs = append(attrs, t.name, n)
	}
	attrs[1] = time.Since(start).Round(time.Millisecond)
	s.log.InfoContext(ctx, "clickhouse_sync: done", attrs...)
}

var errTooManyRows = errors.New("clickhouse_sync: table exceeds 1M row limit")

func checkLimit[T any](rows []T) error {
	if len(rows) > maxRows {
		return errTooManyRows
	}
	return nil
}

func (s *syncer) syncUsers(ctx context.Context, conn clickhouse.Conn, today time.Time) (int, error) {
	rows, err := exedb.WithRxRes0(s.db, ctx, (*exedb.Queries).ExtractUsersForClickHouse)
	if err != nil {
		return 0, err
	}
	if err := checkLimit(rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO users (extract_date, user_id, email)")
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := batch.Append(today, r.UserID, r.Email); err != nil {
			return 0, err
		}
	}
	return len(rows), batch.Send()
}

func (s *syncer) syncTeams(ctx context.Context, conn clickhouse.Conn, today time.Time) (int, error) {
	rows, err := exedb.WithRxRes0(s.db, ctx, (*exedb.Queries).ExtractTeamsForClickHouse)
	if err != nil {
		return 0, err
	}
	if err := checkLimit(rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO teams (extract_date, team_id, display_name)")
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := batch.Append(today, r.TeamID, r.DisplayName); err != nil {
			return 0, err
		}
	}
	return len(rows), batch.Send()
}

func (s *syncer) syncTeamMembers(ctx context.Context, conn clickhouse.Conn, today time.Time) (int, error) {
	rows, err := exedb.WithRxRes0(s.db, ctx, (*exedb.Queries).ExtractTeamMembersForClickHouse)
	if err != nil {
		return 0, err
	}
	if err := checkLimit(rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO team_members (extract_date, team_id, user_id, role)")
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := batch.Append(today, r.TeamID, r.UserID, r.Role); err != nil {
			return 0, err
		}
	}
	return len(rows), batch.Send()
}

func (s *syncer) syncAccounts(ctx context.Context, conn clickhouse.Conn, today time.Time) (int, error) {
	rows, err := exedb.WithRxRes0(s.db, ctx, (*exedb.Queries).ExtractAccountsForClickHouse)
	if err != nil {
		return 0, err
	}
	if err := checkLimit(rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO accounts (extract_date, id, created_by, parent_id)")
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		parentID := ""
		if r.ParentID != nil {
			parentID = *r.ParentID
		}
		if err := batch.Append(today, r.ID, r.CreatedBy, parentID); err != nil {
			return 0, err
		}
	}
	return len(rows), batch.Send()
}

func (s *syncer) syncAccountPlans(ctx context.Context, conn clickhouse.Conn, today time.Time) (int, error) {
	rows, err := exedb.WithRxRes0(s.db, ctx, (*exedb.Queries).ExtractAccountPlansForClickHouse)
	if err != nil {
		return 0, err
	}
	if err := checkLimit(rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO account_plans (extract_date, account_id, plan_id, started_at, ended_at, trial_expires_at)")
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := batch.Append(today, r.AccountID, r.PlanID, r.StartedAt, r.EndedAt, r.TrialExpiresAt); err != nil {
			return 0, err
		}
	}
	return len(rows), batch.Send()
}

func (s *syncer) syncBoxes(ctx context.Context, conn clickhouse.Conn, today time.Time) (int, error) {
	rows, err := exedb.WithRxRes0(s.db, ctx, (*exedb.Queries).ExtractBoxesForClickHouse)
	if err != nil {
		return 0, err
	}
	if err := checkLimit(rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO boxes (extract_date, name, created_by_user_id, status, region)")
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if err := batch.Append(today, r.Name, r.CreatedByUserID, r.Status, r.Region); err != nil {
			return 0, err
		}
	}
	return len(rows), batch.Send()
}
