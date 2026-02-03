// Package sqlite implements a connection pool for SQLite.
//
// # Functions
//
// This package registers a 'now()' SQL function that returns the current
// time as a UTC DATETIME string. It provides several advantages over the
// builtin CURRENT_TIMESTAMP: nanosecond-level precision instead of
// second-level, and compatibility with synctest time bubbles for testing
// time-dependent logic like TTLs.
//
// The function formats timestamps using a consistent layout to keep
// lexicographical comparisons valid in SQL queries.
//
//	Example: SELECT * FROM table WHERE created_at < now();
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TODO: add instrumentation, so we can measure how long we are waiting
// for DB connections and see the slow points. E.g. add an HTTP handler.

// Prometheus metrics for connection pool monitoring
func generateLogarithmicBuckets(min, max float64, count int) []float64 {
	buckets := make([]float64, count)
	logMin := math.Log(min)
	logMax := math.Log(max)
	for i := range count {
		factor := float64(i) / float64(count-1)
		buckets[i] = math.Exp(logMin + factor*(logMax-logMin))
	}
	return buckets
}

var (
	// SQL-level connection pool metrics
	openConnectionsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sqlite_pool_open_connections",
		Help: "Number of established connections to the SQLite database.",
	})

	inUseConnectionsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sqlite_pool_in_use_connections",
		Help: "Number of connections currently in use.",
	})

	idleConnectionsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sqlite_pool_idle_connections",
		Help: "Number of idle connections.",
	})

	// Channel-level connection pool metrics (our custom pooling)
	availableWritersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sqlite_pool_available_writers",
		Help: "Number of writer connections available in the channel.",
	})

	availableReadersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sqlite_pool_available_readers",
		Help: "Number of reader connections available in the channel.",
	})

	totalWritersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sqlite_pool_total_writers",
		Help: "Total capacity of the writer connection pool.",
	})

	totalReadersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "sqlite_pool_total_readers",
		Help: "Total capacity of the reader connection pool.",
	})

	// Leak detection metrics
	txLeaksCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sqlite_tx_leaks_total",
		Help: "Total number of write transaction leaks detected.",
	})

	rxLeaksCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sqlite_rx_leaks_total",
		Help: "Total number of read transaction leaks detected.",
	})

	latencyBuckets1To10KMS = generateLogarithmicBuckets(1, 10000, 10)

	// Latency metrics
	rxLatencyHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "sqlite_rx_latency",
		Help:    "Microseconds spent executing a Rx callback fn",
		Buckets: latencyBuckets1To10KMS,
	})

	txLatencyHistogram = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "sqlite_tx_latency",
		Help:    "Microseconds spent executing a Tx callback fn",
		Buckets: latencyBuckets1To10KMS,
	})
)

// RegisterSQLiteMetrics registers SQLite pool metrics with the provided registry
func RegisterSQLiteMetrics(reg *prometheus.Registry) {
	// Connection pool metrics
	reg.MustRegister(openConnectionsGauge)
	reg.MustRegister(inUseConnectionsGauge)
	reg.MustRegister(idleConnectionsGauge)
	reg.MustRegister(availableWritersGauge)
	reg.MustRegister(availableReadersGauge)
	reg.MustRegister(totalWritersGauge)
	reg.MustRegister(totalReadersGauge)

	// Leak detection metrics
	reg.MustRegister(txLeaksCounter)
	reg.MustRegister(rxLeaksCounter)

	// Latency metrics
	reg.MustRegister(rxLatencyHistogram)
	reg.MustRegister(txLatencyHistogram)
}

// DB is an SQLite connection pool.
//
// We deliberately minimize our use of database/sql machinery because
// the semantics do not match SQLite well.
//
// Instead, we choose a single connection to use for writing (because
// SQLite is single-writer) and use the rest as readers.
type DB struct {
	db      *sql.DB
	writer  chan *sql.Conn
	readers chan *sql.Conn

	shutdown func() error
}

func New(dataSourceName string, readerCount int) (*DB, error) {
	if dataSourceName == ":memory:" {
		return nil, fmt.Errorf(":memory: is not supported (because multiple conns are needed); use a temp file")
	}
	// TODO: a caller could override PRAGMA query_only.
	// Consider opening two *sql.DBs, one configured as read-only,
	// to ensure read-only transactions are always such.
	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("sqlite.New: %w", err)
	}
	numConns := readerCount + 1
	if err := InitDB(db, numConns); err != nil {
		return nil, fmt.Errorf("sqlite.New: %w", err)
	}

	ctx, _useShutdownInstead := context.WithCancel(context.Background())

	shutdown := func() error {
		_useShutdownInstead()
		return db.Close()
	}

	var conns []*sql.Conn
	for range numConns {
		conn, err := db.Conn(ctx)
		if err != nil {
			shutdown()
			return nil, fmt.Errorf("sqlite.New: %w", err)
		}
		conns = append(conns, conn)
	}

	p := &DB{
		db:       db,
		writer:   make(chan *sql.Conn, 1),
		readers:  make(chan *sql.Conn, readerCount),
		shutdown: shutdown,
	}
	p.writer <- conns[0]
	for _, conn := range conns[1:] {
		if _, err := conn.ExecContext(ctx, "PRAGMA query_only=1;"); err != nil {
			shutdown()
			return nil, fmt.Errorf("sqlite.New query_only: %w", err)
		}
		p.readers <- conn
	}

	// Set initial metrics
	p.UpdateMetrics()

	// Start a goroutine to periodically update metrics
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			p.UpdateMetrics()
		}
	}()

	return p, nil
}

// InitDB fixes the database/sql pool to a set of fixed connections.
func InitDB(db *sql.DB, numConns int) error {
	db.SetMaxIdleConns(numConns)
	db.SetMaxOpenConns(numConns)
	db.SetConnMaxLifetime(-1)
	db.SetConnMaxIdleTime(-1)

	initQueries := []string{
		"PRAGMA journal_mode=wal;",
		"PRAGMA busy_timeout=1000",
	}

	var conns []*sql.Conn
	for i := 0; i < numConns; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			db.Close()
			return fmt.Errorf("sqlite.InitDB: %w", err)
		}
		for _, q := range initQueries {
			if _, err := conn.ExecContext(context.Background(), q); err != nil {
				db.Close()
				return fmt.Errorf("sqlite.InitDB %d: %w", i, err)
			}
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			db.Close()
			return fmt.Errorf("sqlite.InitDB: %w", err)
		}
	}
	return nil
}

func (p *DB) Close() error {
	return p.shutdown()
}

// UpdateMetrics updates Prometheus metrics with current connection pool status
func (p *DB) UpdateMetrics() {
	// Update SQL-level connection pool metrics
	stats := p.db.Stats()
	openConnectionsGauge.Set(float64(stats.OpenConnections))
	inUseConnectionsGauge.Set(float64(stats.InUse))
	idleConnectionsGauge.Set(float64(stats.Idle))

	// Update our custom channel-level metrics
	availableWritersGauge.Set(float64(len(p.writer)))
	availableReadersGauge.Set(float64(len(p.readers)))
	totalWritersGauge.Set(float64(cap(p.writer)))
	totalReadersGauge.Set(float64(cap(p.readers)))
}

type ctxKeyType int

// CtxKey is the context value key used to store the current *Tx or *Rx.
// In general this should not be used, plumb the tx directly.
// This code is here is used for an exception: the slog package.
var CtxKey any = ctxKeyType(0)

// sqlCtx converts ctx into a context suitable for passing to modernc.org/sqlite.
func sqlCtx(ctx context.Context) context.Context {
	// We always return context.Background() to avoid a race condition in the driver.
	// When a context is cancelled, the driver spawns a goroutine that calls sqlite3_interrupt().
	// If the connection is reused before that goroutine runs, the interrupt affects the wrong query.
	//
	// Until this is fixed upstream, we simply never pass cancellable contexts to the driver.
	//
	// See https://gitlab.com/cznic/sqlite/-/issues/241
	return context.Background()
}

// isRecoverableErr reports whether err is transient and has left the connection usable.
func isRecoverableErr(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	s := err.Error()
	// SQLITE_BUSY is lock contention.
	// SQLITE_INTERRUPT means the operation was cancelled (e.g. context cancelled).
	// Neither corrupts the connection.
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "interrupted")
}

func checkNoTx(ctx context.Context, typ string) {
	x := ctx.Value(CtxKey)
	if x == nil {
		return
	}
	orig := "unexpected"
	switch x := x.(type) {
	case *Tx:
		orig = "Tx (" + x.caller + ")"
	case *Rx:
		orig = "Rx (" + x.caller + ")"
	}
	panic(typ + " inside " + orig)
}

// Exec executes a single statement outside of a transaction.
// Useful in the rare case of PRAGMAs that cannot execute inside a tx,
// such as PRAGMA wal_checkpoint.
func (p *DB) Exec(ctx context.Context, query string, args ...interface{}) error {
	checkNoTx(ctx, "Tx")
	var conn *sql.Conn
	select {
	case <-ctx.Done():
		return fmt.Errorf("DB.Exec: %w", ctx.Err())
	case conn = <-p.writer:
	}
	var err error
	defer func() {
		p.writer <- conn
	}()
	_, err = conn.ExecContext(sqlCtx(ctx), query, args...)
	return wrapErr("db.exec", err)
}

func (p *DB) Tx(ctx context.Context, fn func(ctx context.Context, tx *Tx) error) error {
	checkNoTx(ctx, "Tx")
	var conn *sql.Conn
	select {
	case <-ctx.Done():
		return fmt.Errorf("Tx: %w", ctx.Err())
	case conn = <-p.writer:
	}

	// If the context is closed, we want BEGIN to succeed and then
	// we roll it back later.
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE;"); err != nil {
		if isRecoverableErr(err) {
			p.writer <- conn
			return fmt.Errorf("sqlite.Tx begin: %w", err)
		}
		// Unrecoverable error (e.g. SQLITE_CORRUPT, SQLITE_IOERR).
		// Close the connection to release resources, but don't return it to the pool.
		slog.ErrorContext(ctx, "sqlite.Tx: unrecoverable error starting write tx", "err", err)
		// The pool will have one fewer writer connection.
		txLeaksCounter.Inc()
		conn.Close()
		return fmt.Errorf("sqlite.Tx LEAK %w", err)
	}
	tx := &Tx{
		Rx:  &Rx{conn: conn, p: p, caller: callerOfCaller(1)},
		Now: time.Now(),
	}
	tx.ctx = context.WithValue(ctx, CtxKey, tx)

	var err error
	defer func() {
		if err == nil {
			_, commitErr := tx.conn.ExecContext(context.Background(), "COMMIT;")
			if commitErr != nil {
				err = fmt.Errorf("Tx: commit: %w", commitErr)
			}
		}
		connUsable := true
		if err != nil {
			connUsable = p.rollback(tx.ctx, "Tx", tx.conn)
		}
		if connUsable {
			tx.p.writer <- tx.conn
		}
		tx.Rx.conn = nil // poison against use-after-close
		p.UpdateMetrics()
	}()
	if ctxErr := tx.ctx.Err(); ctxErr != nil {
		return ctxErr // fast path for canceled context
	}
	t0 := time.Now()
	err = fn(tx.ctx, tx)
	txLatencyHistogram.Observe(float64(time.Since(t0).Milliseconds()))

	return err
}

func (p *DB) Rx(ctx context.Context, fn func(ctx context.Context, rx *Rx) error) error {
	checkNoTx(ctx, "Rx")
	var conn *sql.Conn
	select {
	case <-ctx.Done():
		return ctx.Err()
	case conn = <-p.readers:
	}

	// If the context is closed, we want BEGIN to succeed and then
	// we roll it back later.
	if _, err := conn.ExecContext(context.Background(), "BEGIN;"); err != nil {
		if isRecoverableErr(err) {
			p.readers <- conn
			return fmt.Errorf("sqlite.Rx begin: %w", err)
		}
		slog.ErrorContext(ctx, "sqlite.Rx: unrecoverable error starting read tx", "err", err)
		// Unrecoverable error (e.g. tx-inside-tx misuse, SQLITE_CORRUPT, SQLITE_IOERR).
		// Close the connection to release resources, but don't return it to the pool.
		// The pool will have one fewer reader connection.
		rxLeaksCounter.Inc()
		conn.Close()
		return fmt.Errorf("sqlite.Rx LEAK: %w", err)
	}
	rx := &Rx{conn: conn, p: p, caller: callerOfCaller(1)}
	rx.ctx = context.WithValue(ctx, CtxKey, rx)

	defer func() {
		if connUsable := p.rollback(rx.ctx, "Rx", rx.conn); connUsable {
			rx.p.readers <- rx.conn
		}
		rx.conn = nil // poison against use-after-close
		p.UpdateMetrics()
	}()
	if ctxErr := rx.ctx.Err(); ctxErr != nil {
		return ctxErr // fast path for canceled context
	}
	t0 := time.Now()
	err := fn(rx.ctx, rx)
	rxLatencyHistogram.Observe(float64(time.Since(t0).Milliseconds()))
	return err
}

// rollback rolls back the transaction and reports whether the connection is still usable.
// If it returns false, the connection has been closed and must not be returned to the pool.
func (p *DB) rollback(ctx context.Context, txType string, conn *sql.Conn) (connOK bool) {
	_, err := conn.ExecContext(context.Background(), "ROLLBACK;")
	if err == nil || strings.Contains(err.Error(), "no transaction is active") || isRecoverableErr(err) {
		return true
	}
	// Unrecoverable error (e.g. SQLITE_CORRUPT, SQLITE_IOERR). Bad news bears.
	// Close the connection and tell the caller not to return it to the pool.
	slog.ErrorContext(ctx, "sqlite: unrecoverable rollback error", "err", err)
	if txType == "Tx" {
		txLeaksCounter.Inc()
	} else {
		rxLeaksCounter.Inc()
	}
	conn.Close()
	// Don't close the db, just the connection.
	// Fingers crossed we'll be able to keep limping along.
	return false
}

type Tx struct {
	*Rx
	Now time.Time
}

func (tx *Tx) Exec(query string, args ...interface{}) (sql.Result, error) {
	res, err := tx.conn.ExecContext(sqlCtx(tx.ctx), query, args...)
	return res, wrapErr("exec", err)
}

type Rx struct {
	ctx    context.Context
	conn   *sql.Conn
	p      *DB
	caller string // for debugging
}

func (rx *Rx) Context() context.Context {
	return rx.ctx
}

func (rx *Rx) Query(query string, args ...interface{}) (*sql.Rows, error) {
	rows, err := rx.conn.QueryContext(sqlCtx(rx.ctx), query, args...)
	return rows, wrapErr("query", err)
}

func (rx *Rx) QueryRow(query string, args ...interface{}) *Row {
	rows, err := rx.conn.QueryContext(sqlCtx(rx.ctx), query, args...)
	return &Row{err: err, rows: rows}
}

// Conn returns a wrapped sql.Conn-like thing for use with external libraries like sqlc.
func (rx *Rx) Conn() *dbtxWrapper {
	return &dbtxWrapper{conn: rx.conn}
}

// dbtxWrapper wraps *sql.Conn, substituting sqlCtx for the context for all database operations.
type dbtxWrapper struct {
	conn *sql.Conn
}

func (c *dbtxWrapper) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return c.conn.ExecContext(sqlCtx(ctx), query, args...)
}

func (c *dbtxWrapper) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return c.conn.PrepareContext(sqlCtx(ctx), query)
}

func (c *dbtxWrapper) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return c.conn.QueryContext(sqlCtx(ctx), query, args...)
}

func (c *dbtxWrapper) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return c.conn.QueryRowContext(sqlCtx(ctx), query, args...)
}

// Row is equivalent to *sql.Row, but we provide a more useful error.
type Row struct {
	err  error
	rows *sql.Rows
}

func (r *Row) Scan(dest ...any) error {
	if r.err != nil {
		return wrapErr("QueryRow", r.err)
	}

	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return wrapErr("QueryRow.Scan", err)
		}
		return wrapErr("QueryRow.Scan", sql.ErrNoRows)
	}
	err := r.rows.Scan(dest...)
	if err != nil {
		return wrapErr("QueryRow.Scan", err)
	}
	return wrapErr("QueryRow.Scan", r.rows.Close())
}

func wrapErr(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %s: %w", callerOfCaller(2), prefix, err)
}

func callerOfCaller(depth int) string {
	caller := "sqlite.unknown"
	pc := make([]uintptr, 3)
	const addedSkip = 3 // runtime.Callers, callerOfCaller, our caller (e.g. wrapErr or Rx)
	if n := runtime.Callers(addedSkip+depth-1, pc[:]); n > 0 {
		frames := runtime.CallersFrames(pc[:n])
		frame, _ := frames.Next()
		if frame.Function != "" {
			caller = frame.Function
		}
		// This is a special case.
		//
		// We expect people to wrap the sqlite.Tx/Rx objects
		// in another domain-specific Tx/Rx object. That means
		// they almost certainly have matching Tx/Rx methods,
		// which aren't useful for debugging. So if we see that,
		// we remove it.
		if strings.HasSuffix(caller, ".Tx") || strings.HasSuffix(caller, ".Rx") {
			frame, more := frames.Next()
			if more && frame.Function != "" {
				caller = frame.Function
			}
		}
	}
	if i := strings.LastIndexByte(caller, '/'); i >= 0 {
		caller = caller[i+1:]
	}
	return caller
}
