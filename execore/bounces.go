package execore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// bounceStore implements email.BounceStore using the server's database.
type bounceStore struct {
	db *sqlite.DB
}

// newBounceStore creates a new bounce store.
func newBounceStore(db *sqlite.DB) *bounceStore {
	return &bounceStore{db: db}
}

// GetLastBouncesPollTime implements email.BounceStore.
func (s *bounceStore) GetLastBouncesPollTime(ctx context.Context) (time.Time, error) {
	var result time.Time
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		q := exedb.New(rx.Conn())
		lastPoll, err := q.GetLastBouncesPoll(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			// Never polled before - return zero time
			return nil
		}
		if err != nil {
			return err
		}
		// Parse the stored timestamp
		result, err = time.Parse(time.RFC3339, lastPoll)
		if err != nil {
			// Invalid format - return zero time to trigger full lookback
			return nil
		}
		return nil
	})
	return result, err
}

// SetLastBouncesPollTime implements email.BounceStore.
func (s *bounceStore) SetLastBouncesPollTime(ctx context.Context, t time.Time) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		return q.SetLastBouncesPoll(ctx, t.Format(time.RFC3339))
	})
}

// StoreBounce implements email.BounceStore.
func (s *bounceStore) StoreBounce(ctx context.Context, bounce email.BounceRecord) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		q := exedb.New(tx.Conn())
		return q.InsertEmailBounce(ctx, exedb.InsertEmailBounceParams{
			Email:  bounce.Email,
			Reason: bounce.Reason,
		})
	})
}
