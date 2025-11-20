package users

import (
	"context"
	"database/sql"
	"iter"
	"strings"
	"time"

	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
	msqlite "modernc.org/sqlite"
)

type rowScanner interface {
	Scan(dest ...any) error
}

type customRows struct {
	err  error
	rows *sql.Rows
}

type scannerFunc func(src any) error

func (f scannerFunc) Scan(src any) error {
	return f(src)
}

func (r *customRows) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *time.Time:
			dest[i] = scannerFunc(func(src any) error {
				var t sql.NullTime
				err := t.Scan(src)
				if err == nil {
					*d = t.Time
				}
				return err
			})
		case *ssh.PublicKey:
			dest[i] = scannerFunc(func(src any) error {
				var b sql.Null[[]byte]
				if err := b.Scan(src); err != nil {
					return err
				}
				if !b.Valid {
					*d = nil
					return nil
				}
				pk, err := ssh.ParsePublicKey(b.V)
				if err == nil {
					*d = pk
					return nil
				}
				pk, _, _, _, err = ssh.ParseAuthorizedKey(b.V)
				if err == nil {
					*d = pk
				}
				return nil
			})
		}
	}
	return r.rows.Scan(dest...)
}

// queryRW executes a read-write query within a transaction, yielding each resulting rowScanner.
// If an error occurs, it yields a single rowScanner that returns the error on Scan.
func (s *Store) queryRW(ctx context.Context, query string, args ...any) iter.Seq[rowScanner] {
	return func(yield func(rowScanner) bool) {
		err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			rows, err := tx.Query(query, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if !yield(&customRows{rows: rows}) {
					break
				}
			}
			return rows.Err()
		})
		if err != nil {
			yield(&customRows{err: err})
		}
	}
}

func (s *Store) queryRO(ctx context.Context, query string, args ...any) iter.Seq[rowScanner] {
	return func(yield func(rowScanner) bool) {
		err := s.db.Rx(ctx, func(ctx context.Context, tx *sqlite.Rx) error {
			rows, err := tx.Query(query, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if !yield(&customRows{rows: rows}) {
					break
				}
			}
			return rows.Err()
		})
		if err != nil {
			yield(&customRows{err: err})
		}
	}
}

func parseConstraintName(me *msqlite.Error) string {
	// Parse the constriant name out of the error message.
	// The only structure we can "rely" on is that the constraint name
	// follows "CHECK constraint failed: ".
	_, what, ok := strings.Cut(me.Error(), " constraint failed: ")
	if !ok {
		return ""
	}

	// The constraint name may be followed by more context,
	// e.g. "CHECK constraint failed: valid_email (some details)".
	// Trim that off.
	what, _, _ = strings.Cut(what, " ") // trim any trailing context

	return what
}
