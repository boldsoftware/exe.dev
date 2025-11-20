package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestFunctionNow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "test.db")
		p, err := New(dsn, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()

		selectNow := func() time.Time {
			t.Helper()
			var now time.Time
			err := p.Rx(t.Context(), func(ctx context.Context, rx *Rx) error {
				var s string
				if err := rx.QueryRow("SELECT now();").Scan(&s); err != nil {
					return err
				}
				parsed, err := time.Parse(Time10, s)
				if err != nil {
					return err
				}
				now = parsed
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			return now
		}

		start := time.Now()

		// Time should match exactly on repeated calls.
		// No need for approximation since we control SQL time and time.Now().
		now0 := selectNow()
		if !now0.Equal(start) {
			t.Errorf("now()=%v, want %v", now0, start)
		}

		now1 := selectNow()
		if !now1.Equal(now0) {
			t.Errorf("now()=%v, want %v", now1, now0)
		}

		time.Sleep(1 * time.Hour)

		now2 := selectNow()
		want := start.Add(1 * time.Hour)
		if !now2.Equal(want) {
			t.Errorf("now()=%v, want %v", now2, want)
		}
	})
}
