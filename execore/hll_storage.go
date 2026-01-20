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
	row, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetHLLSketch, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row.Data, nil
}

func (s *hllStorage) Save(ctx context.Context, key string, data []byte) error {
	return exedb.WithTx1(s.db, ctx, (*exedb.Queries).UpsertHLLSketch, exedb.UpsertHLLSketchParams{
		Key:  key,
		Data: data,
	})
}
