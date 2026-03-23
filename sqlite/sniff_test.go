package sqlite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnifferEndToEnd(t *testing.T) {
	db := newTestDB(t)

	c, err := db.Sniff.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Sniff.unsubscribe(c)

	ctx := context.Background()

	// Write: fires pre-update hook (change event) + exec event.
	if err := db.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (name) VALUES (?)", "hello")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Read via dbtxWrapper (simulating sqlc).
	if err := db.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		rows, err := rx.Conn().QueryContext(ctx, "SELECT id, name FROM t")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}

	events := drain(t, c)

	// Expect: change (from pre-update hook), exec (INSERT), query (SELECT).
	if len(events) != 3 {
		for i, ev := range events {
			t.Logf("event[%d]: %v", i, ev)
		}
		t.Fatalf("got %d events, want 3", len(events))
	}

	if events[0]["type"] != "change" {
		t.Errorf("event[0].type = %v, want change", events[0]["type"])
	}
	if events[0]["table"] != "t" {
		t.Errorf("event[0].table = %v, want t", events[0]["table"])
	}
	if events[0]["op"] != "INSERT" {
		t.Errorf("event[0].op = %v, want INSERT", events[0]["op"])
	}

	if events[1]["type"] != "exec" {
		t.Errorf("event[1].type = %v, want exec", events[1]["type"])
	}
	if events[1]["rows_affected"] != float64(1) {
		t.Errorf("event[1].rows_affected = %v, want 1", events[1]["rows_affected"])
	}

	if events[2]["type"] != "query" {
		t.Errorf("event[2].type = %v, want query", events[2]["type"])
	}
	if !strings.Contains(events[2]["sql"].(string), "SELECT") {
		t.Errorf("event[2].sql = %v, want SELECT", events[2]["sql"])
	}
}

func TestSnifferMaxOneClient(t *testing.T) {
	db := newTestDB(t)

	c, err := db.Sniff.subscribe("")
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Sniff.subscribe("")
	if err == nil {
		t.Fatal("expected error on second subscribe")
	}

	db.Sniff.unsubscribe(c)

	// Should work again after unsubscribe.
	c2, err := db.Sniff.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	db.Sniff.unsubscribe(c2)
}

func TestSnifferForceDisconnect(t *testing.T) {
	db := newTestDB(t)

	c, err := db.Sniff.subscribe("")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Fill the channel buffer (256) + extra to trigger force disconnect.
	for i := range 300 {
		if err := db.Rx(ctx, func(ctx context.Context, rx *Rx) error {
			_, err := rx.Conn().QueryContext(ctx, "SELECT 1")
			return err
		}); err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
	}

	// done channel should be closed.
	select {
	case <-c.done:
		// good
	default:
		t.Fatal("expected force disconnect")
	}

	// New client should be able to connect.
	c2, err := db.Sniff.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	db.Sniff.unsubscribe(c2)
}

func TestSnifferHTTPFilterSQL(t *testing.T) {
	db := newTestDB(t)

	c, err := db.Sniff.subscribe("sql")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Sniff.unsubscribe(c)

	ctx := context.Background()

	if err := db.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (name) VALUES (?)", "x")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		_, err := rx.Conn().QueryContext(ctx, "SELECT * FROM t")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	events := drain(t, c)

	for _, ev := range events {
		if ev["type"] == "change" {
			t.Errorf("got change event with sql filter")
		}
	}
	if len(events) != 2 {
		for i, ev := range events {
			t.Logf("event[%d]: %v", i, ev)
		}
		t.Fatalf("got %d events, want 2 (exec + query)", len(events))
	}
}

func TestSnifferHTTPFilterChanges(t *testing.T) {
	db := newTestDB(t)

	c, err := db.Sniff.subscribe("changes")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Sniff.unsubscribe(c)

	ctx := context.Background()

	if err := db.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("INSERT INTO t (name) VALUES (?)", "x")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Rx(ctx, func(ctx context.Context, rx *Rx) error {
		_, err := rx.Conn().QueryContext(ctx, "SELECT * FROM t")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	events := drain(t, c)

	if len(events) != 1 {
		for i, ev := range events {
			t.Logf("event[%d]: %v", i, ev)
		}
		t.Fatalf("got %d events, want 1 change event", len(events))
	}
	if events[0]["type"] != "change" {
		t.Errorf("event type = %v, want change", events[0]["type"])
	}
}

func TestSnifferHTTPRejectsSecondClient(t *testing.T) {
	db := newTestDB(t)

	ts := httptest.NewServer(&db.Sniff)
	defer ts.Close()

	// First client connects.
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	req1, _ := http.NewRequestWithContext(ctx1, "GET", ts.URL, nil)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != 200 {
		t.Fatalf("first client status = %d, want 200", resp1.StatusCode)
	}

	// Second client should be rejected.
	resp2, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second client status = %d, want %d", resp2.StatusCode, http.StatusConflict)
	}
}

// --- helpers ---

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := New(dbPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := db.Tx(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

func drain(t *testing.T, c *sniffClient) []map[string]any {
	t.Helper()
	var events []map[string]any
	for {
		select {
		case v := <-c.ch:
			data, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var ev map[string]any
			if err := json.Unmarshal(data, &ev); err != nil {
				t.Fatal(err)
			}
			events = append(events, ev)
		default:
			return events
		}
	}
}
