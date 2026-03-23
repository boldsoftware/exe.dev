package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	sqlitelib "modernc.org/sqlite"
)

// Sniffer streams SQL activity to a single connected HTTP client as NDJSON.
//
// At most one client may be connected. A second connection attempt fails.
// A client that can't keep up is forcibly disconnected.
//
// Two event sources, selected via ?type= query param:
//
//   - "sql":     SQL text from ExecContext/QueryContext (all statements)
//   - "changes": row-level changes from SQLite's pre-update hook
//
// Default (no param) streams both.
type Sniffer struct {
	client atomic.Pointer[sniffClient]
	mu     sync.Mutex // guards subscribe
}

type sniffClient struct {
	ch     chan any      // typed event structs; reader goroutine marshals
	done   chan struct{} // closed on force disconnect
	filter string        // "sql", "changes", or ""
}

// registerHook registers the pre-update hook on a *sql.Conn.
// The pre-update hook fires during statement execution, before commit.
// Change events may include rows from transactions that are later rolled back.
// The commit/rollback hooks don't provide row-level data, so this is the best
// available hook for row-level change tracking.
func (s *Sniffer) registerHook(conn *sql.Conn) error {
	return conn.Raw(func(driverConn any) error {
		h, ok := driverConn.(sqlitelib.HookRegisterer)
		if !ok {
			return errors.New("sqlite driver does not support HookRegisterer")
		}
		h.RegisterPreUpdateHook(func(d sqlitelib.SQLitePreUpdateData) {
			c := s.client.Load()
			if c == nil {
				return
			}
			if c.filter != "" && c.filter != "changes" {
				return
			}
			s.trySend(c, ChangeEvent{
				Type:     "change",
				Op:       opName(d.Op),
				Database: d.DatabaseName,
				Table:    d.TableName,
				OldRowID: d.OldRowID,
				NewRowID: d.NewRowID,
			})
		})
		return nil
	})
}

func opName(op int32) string {
	switch op {
	case 9:
		return "DELETE"
	case 18:
		return "INSERT"
	case 23:
		return "UPDATE"
	default:
		return "UNKNOWN"
	}
}

// trySend sends v to c, or force-disconnects c if the channel is full.
func (s *Sniffer) trySend(c *sniffClient, v any) {
	select {
	case c.ch <- v:
	default:
		if s.client.CompareAndSwap(c, nil) {
			close(c.done)
		}
	}
}

// --- Event types ---

// ExecEvent is emitted for ExecContext calls (writes, pragmas, DDL).
type ExecEvent struct {
	Type         string  `json:"type"`
	SQL          string  `json:"sql"`
	RowsAffected int64   `json:"rows_affected"`
	DurationMS   float64 `json:"duration_ms"`
	Error        string  `json:"error,omitempty"`
	Time         string  `json:"time"`
}

// QueryEvent is emitted for QueryContext/QueryRowContext calls (reads).
type QueryEvent struct {
	Type       string  `json:"type"`
	SQL        string  `json:"sql"`
	DurationMS float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
	Time       string  `json:"time"`
}

// ChangeEvent is emitted by SQLite's pre-update hook, once per row changed.
type ChangeEvent struct {
	Type     string `json:"type"`
	Op       string `json:"op"`
	Database string `json:"database"`
	Table    string `json:"table"`
	OldRowID int64  `json:"old_rowid"`
	NewRowID int64  `json:"new_rowid"`
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func msec(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func (s *Sniffer) emitExec(query string, d time.Duration, res sql.Result, err error) {
	c := s.client.Load()
	if c == nil {
		return
	}
	if c.filter != "" && c.filter != "sql" {
		return
	}
	var rowsAffected int64
	if res != nil {
		rowsAffected, _ = res.RowsAffected()
	}
	s.trySend(c, ExecEvent{
		Type:         "exec",
		SQL:          query,
		RowsAffected: rowsAffected,
		DurationMS:   msec(d),
		Error:        errStr(err),
		Time:         time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Sniffer) emitQuery(query string, d time.Duration, err error) {
	c := s.client.Load()
	if c == nil {
		return
	}
	if c.filter != "" && c.filter != "sql" {
		return
	}
	s.trySend(c, QueryEvent{
		Type:       "query",
		SQL:        query,
		DurationMS: msec(d),
		Error:      errStr(err),
		Time:       time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// --- HTTP handler ---

var errAlreadyConnected = errors.New("another sniff client is already connected")

func (s *Sniffer) subscribe(filter string) (*sniffClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client.Load() != nil {
		return nil, errAlreadyConnected
	}
	c := &sniffClient{
		ch:     make(chan any, 256),
		done:   make(chan struct{}),
		filter: filter,
	}
	s.client.Store(c)
	return c, nil
}

func (s *Sniffer) unsubscribe(c *sniffClient) {
	s.client.CompareAndSwap(c, nil)
}

func (s *Sniffer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	filter := r.URL.Query().Get("type")
	if filter != "" && filter != "sql" && filter != "changes" {
		http.Error(w, `type must be "sql" or "changes"`, http.StatusBadRequest)
		return
	}

	c, err := s.subscribe(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer s.unsubscribe(c)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	newline := []byte("\n")
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case v := <-c.ch:
			data, err := json.Marshal(v)
			if err != nil {
				continue
			}
			w.Write(data)
			w.Write(newline)
			flusher.Flush()
		}
	}
}
