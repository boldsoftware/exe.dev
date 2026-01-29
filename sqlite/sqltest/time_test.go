// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqltest

import (
	"database/sql"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// TestGoTimeSynctest demonstrates CURRENT_TIMESTAMP, CURRENT_DATE, and CURRENT_TIME
// work with synctest's controlled time progression.
func TestGoTimeSynctest(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		db, err := sql.Open("sqlite", "file::memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		// Verify CURRENT_* returns real current time
		var stamps [3]string
		if err := db.QueryRow("SELECT CURRENT_TIMESTAMP, CURRENT_DATE, CURRENT_TIME").Scan(&stamps[0], &stamps[1], &stamps[2]); err != nil {
			t.Fatal(err)
		}
		for _, stamp := range stamps {
			if strings.HasPrefix(stamp, "2000-01-01") {
				t.Errorf("got synctest time %s without _go_time=true", stamp)
			}
		}
	})

	t.Run("enabled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file::memory:?_go_time=true")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			// Verify CURRENT_* returns synctest start time (2000-01-01 00:00:00)
			var ts, date, tm string
			if err := db.QueryRow("SELECT CURRENT_TIMESTAMP, CURRENT_DATE, CURRENT_TIME").Scan(&ts, &date, &tm); err != nil {
				t.Fatal(err)
			}
			if ts != "2000-01-01 00:00:00" || date != "2000-01-01" || tm != "00:00:00" {
				t.Errorf("got %s, %s, %s; want 2000-01-01 00:00:00, 2000-01-01, 00:00:00", ts, date, tm)
			}

			// Verify CURRENT_* is deterministic within a statement
			var ts1, ts2, ts3 string
			if err := db.QueryRow("SELECT CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP").Scan(&ts1, &ts2, &ts3); err != nil {
				t.Fatal(err)
			}
			if ts1 != ts2 || ts2 != ts3 {
				t.Errorf("CURRENT_TIMESTAMP not deterministic: %s, %s, %s", ts1, ts2, ts3)
			}

			// Verify time advances with time.Sleep
			time.Sleep(5 * time.Second)
			if err := db.QueryRow("SELECT CURRENT_TIMESTAMP").Scan(&ts); err != nil {
				t.Fatal(err)
			}
			if ts != "2000-01-01 00:00:05" {
				t.Errorf("after 5s got %s; want 2000-01-01 00:00:05", ts)
			}

			// Verify date rollover
			time.Sleep(25 * time.Hour)
			if err := db.QueryRow("SELECT CURRENT_DATE, CURRENT_TIME").Scan(&date, &tm); err != nil {
				t.Fatal(err)
			}
			if date != "2000-01-02" {
				t.Errorf("after 25h got date %s; want 2000-01-02", date)
			}
		})
	})
}

// TestGoTimeSynctestDefaults demonstrates DEFAULT CURRENT_* works in synctest bubbles.
func TestGoTimeSynctestTableColumDefaults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, err := sql.Open("sqlite", "file::memory:?_go_time=true")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		if _, err := db.Exec(`
			CREATE TABLE events (
				ts TEXT DEFAULT CURRENT_TIMESTAMP,
				date TEXT DEFAULT CURRENT_DATE,
				time TEXT DEFAULT CURRENT_TIME
			)
		`); err != nil {
			t.Fatal(err)
		}

		// Insert without specifying timestamp columns
		if _, err := db.Exec("INSERT INTO events DEFAULT VALUES"); err != nil {
			t.Fatal(err)
		}

		var ts1, date1, time1 string
		if err := db.QueryRow("SELECT ts, date, time FROM events").Scan(&ts1, &date1, &time1); err != nil {
			t.Fatal(err)
		}
		if ts1 != "2000-01-01 00:00:00" {
			t.Errorf("first insert got %s; want 2000-01-01 00:00:00", ts1)
		}

		// Advance time and insert again
		time.Sleep(10 * time.Second)
		if _, err := db.Exec("INSERT INTO events DEFAULT VALUES"); err != nil {
			t.Fatal(err)
		}

		var ts2 string
		if err := db.QueryRow("SELECT ts FROM events ORDER BY rowid DESC LIMIT 1").Scan(&ts2); err != nil {
			t.Fatal(err)
		}
		if ts2 != "2000-01-01 00:00:10" {
			t.Errorf("second insert got %s; want 2000-01-01 00:00:10", ts2)
		}
	})
}
