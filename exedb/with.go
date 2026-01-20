package exedb

import (
	"context"

	"exe.dev/sqlite"
)

// WithRx executes a function with a read-only database transaction and exedb queries.
func WithRx(db *sqlite.DB, ctx context.Context, fn func(context.Context, *Queries) error) error {
	return db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := New(rx.Conn())
		return fn(ctx, queries)
	})
}

// WithTx executes a function with a read-write database transaction and exedb queries.
func WithTx(db *sqlite.DB, ctx context.Context, fn func(context.Context, *Queries) error) error {
	return db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := New(tx.Conn())
		return fn(ctx, queries)
	})
}

// WithRxRes0 executes a sqlc query with a read-only database transaction and no arguments, returning a value.
func WithRxRes0[T any](db *sqlite.DB, ctx context.Context, fn func(*Queries, context.Context) (T, error)) (T, error) {
	var result T
	err := WithRx(db, ctx, func(ctx context.Context, queries *Queries) error {
		var err error
		result, err = fn(queries, ctx)
		return err
	})
	return result, err
}

// WithRxRes1 executes a sqlc query with a read-only database transaction and one argument, returning a value.
func WithRxRes1[T, A any](db *sqlite.DB, ctx context.Context, fn func(*Queries, context.Context, A) (T, error), a A) (T, error) {
	var result T
	err := WithRx(db, ctx, func(ctx context.Context, queries *Queries) error {
		var err error
		result, err = fn(queries, ctx, a)
		return err
	})
	return result, err
}

// WithTx0 executes a sqlc query with a read-write database transaction and no arguments.
func WithTx0(db *sqlite.DB, ctx context.Context, fn func(*Queries, context.Context) error) error {
	return WithTx(db, ctx, func(ctx context.Context, queries *Queries) error {
		return fn(queries, ctx)
	})
}

// WithTx1 executes a sqlc query with a read-write database transaction and one argument.
func WithTx1[A any](db *sqlite.DB, ctx context.Context, fn func(*Queries, context.Context, A) error, a A) error {
	return WithTx(db, ctx, func(ctx context.Context, queries *Queries) error {
		return fn(queries, ctx, a)
	})
}

// WithTxRes0 executes a function with a read-write database transaction, returning a value.
func WithTxRes0[T any](db *sqlite.DB, ctx context.Context, fn func(*Queries, context.Context) (T, error)) (T, error) {
	var result T
	err := WithTx(db, ctx, func(ctx context.Context, queries *Queries) error {
		var err error
		result, err = fn(queries, ctx)
		return err
	})
	return result, err
}

// WithTxRes1 executes a function with a read-write database transaction and one argument, returning a value.
func WithTxRes1[T, A any](db *sqlite.DB, ctx context.Context, fn func(*Queries, context.Context, A) (T, error), a A) (T, error) {
	var result T
	err := WithTx(db, ctx, func(ctx context.Context, queries *Queries) error {
		var err error
		result, err = fn(queries, ctx, a)
		return err
	})
	return result, err
}
