package billing

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"exe.dev/billing/httprr"
	"exe.dev/billing/stripetest"
	"exe.dev/errorz"
	"exe.dev/exedb"
	exesqlite "exe.dev/sqlite"
	_ "exe.dev/sqlite/sqltest"
	"exe.dev/tslog"
	"github.com/stripe/stripe-go/v82"
)

// newTestManager returns a Manager configured to use an httprr recorder
// for the test named t.Name(). It also returns a cleanup function that
// can be used to cleanup resources used by the Manager before exiting.
// This is typically needed in synctest bubbles to ensure the channels created
// in the Go resolver are cleaned up to prevent use outside syntest bubble.
func newTestManager(t *testing.T) *Manager {
	// t.Helper()

	fname := "testdata/stripe/" + t.Name()

	rr, err := httprr.Open(fname, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rr.Close(); err != nil {
			t.Errorf("failed to close httprr: %v", err)
		}
	})

	var counter atomic.Int64
	rr.ScrubReq(func(r *http.Request) error {
		r.Header.Del("Authorization")
		r.Header.Del("Idempotency-Key")
		r.Header.Del("Stripe-Version")
		r.Header.Del("User-Agent")
		r.Header.Del("X-Stripe-Client-User-Agent")
		r.Header.Del("X-Stripe-Client-Telemetry")

		// Normalize Replay-Id header to make recordings stable.
		// This prevents a response to an identical request in the same test
		// from clobbering the first request's recording.
		r.Header.Set("Replay-Id", fmt.Sprintf("req_%d", counter.Add(1)))

		// Normalize test names in request bodies
		if r.Body != nil {
			if body, ok := r.Body.(*httprr.Body); ok {
				// Replace name=TestXXX with name=TestNormalized
				re := regexp.MustCompile(`name=Test[^&]*`)
				body.Data = re.ReplaceAll(body.Data, []byte("name=TestNormalized"))
				body.ReadOffset = 0
			}
		}
		return nil
	})
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient:    rr.Client(),
		LeveledLogger: stripetest.LeveledLogger(t),
	})
	m := &Manager{
		DB:     newTestDB(t),
		Logger: tslog.Slogger(t),
		Client: stripe.NewClient(TestAPIKey,
			stripe.WithBackends(backends),
		),
	}

	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices: %v", err)
	}

	return m
}

type testClock struct {
	t *testing.T
	*stripetest.Clock
}

// startClock starts a new test clock for the provided Manager and
// registers a t.Cleanup function to delete the clock when the test
// completes successfully, or preserve it for debugging if the test failed.
func (m *Manager) startClock(t *testing.T) *testClock {
	t.Helper()
	clock, err := stripetest.Start(t.Context(), m.Client, t.Name())
	if err != nil {
		t.Fatalf("stripetest.Start: %v", err)
	}

	t.Cleanup(func() {
		t.Helper()
		if t.Failed() {
			t.Logf("Test clock preserved for debugging: %s", clock.Link())
		} else {
			clock.Close()
		}
	})

	t.Logf("test clock %q started (now=%v)", clock.ID(), clock.Now())
	m.testClockID = clock.ID()
	return &testClock{t, clock}
}

func (tc *testClock) Sleep(d time.Duration) {
	tc.t.Helper()
	err := tc.Clock.Sleep(tc.t.Context(), d, func(status string) {
		tc.t.Logf("test clock %q: %s", tc.ID(), status)
	})
	if err != nil {
		tc.t.Fatalf("clock.Sleep(%v): %v", d, err)
	}
}

func isStripeParamError(err error, param string) bool {
	if e, ok := errorz.AsType[*stripe.Error](err); ok {
		return e.Param == param
	}
	return false
}

// newTestDB creates a test database with all migrations applied and returns
// an exe.dev/sqlite.DB suitable for use in billing Manager tests.
func newTestDB(t *testing.T) *exesqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "billing_test.db")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}

	db, err := exesqlite.New(dbPath, 1)
	if err != nil {
		t.Fatalf("failed to create sqlite wrapper: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestDBGoTime is like newTestDB, but uses SQLite's _go_time mode so
// CURRENT_TIMESTAMP follows Go's time.Now, including synctest bubbles.
func newTestDBGoTime(t *testing.T) *exesqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "billing_test.db")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}
	dsn := "file:" + dbPath + "?_go_time=true"

	db, err := exesqlite.New(dsn, 1)
	if err != nil {
		t.Fatalf("failed to create sqlite wrapper: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// createTestAccount creates a test user and account in the DB for billing tests.
func createTestAccount(t *testing.T, db *exesqlite.DB, accountID, userID string) {
	t.Helper()
	err := exedb.WithTx(db, context.Background(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  userID + "@example.com",
			Region: "pdx",
		}); err != nil {
			return err
		}
		return q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: userID,
		})
	})
	if err != nil {
		t.Fatalf("createTestAccount: %v", err)
	}
}
