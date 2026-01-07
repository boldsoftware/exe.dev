package execore

import (
	"context"
	"database/sql"
	"errors"

	"exe.dev/exedb"
	"exe.dev/hll"
	"exe.dev/sqlite"
)

// hllStorage implements hll.Storage using SQLite via exedb.
type hllStorage struct {
	db *sqlite.DB
}

// newHLLStorage creates a new SQLite-backed HLL storage.
func newHLLStorage(db *sqlite.DB) hll.Storage {
	return &hllStorage{db: db}
}

func (s *hllStorage) Load(ctx context.Context, key string) ([]byte, error) {
	var data []byte
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		row, err := exedb.New(rx.Conn()).GetHLLSketch(ctx, key)
		if err != nil {
			return err
		}
		data = row.Data
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func (s *hllStorage) Save(ctx context.Context, key string, data []byte) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		return exedb.New(tx.Conn()).UpsertHLLSketch(ctx, exedb.UpsertHLLSketchParams{
			Key:  key,
			Data: data,
		})
	})
}
