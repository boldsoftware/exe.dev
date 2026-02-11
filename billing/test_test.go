package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		LeveledLogger: &stripeErrorLogger{t: t},
	})
	m := &Manager{
		APIKey: TestAPIKey,
		Client: stripe.NewClient(TestAPIKey,
			stripe.WithBackends(backends),
		),
	}

	if err := installTestPrices(m); err != nil {
		t.Fatalf("installTestPrices: %v", err)
	}

	return m
}

type stripeErrorLogger struct {
	t *testing.T
}

func (l *stripeErrorLogger) logf(format string, args ...any) {
	l.t.Helper()
	fmt.Fprintf(l.t.Output(), format+"\n", args...)
}

func (l *stripeErrorLogger) Debugf(format string, args ...any) {
	l.t.Helper()
	l.logf("[DEBUG] "+format, args...)
}

func (l *stripeErrorLogger) Errorf(format string, args ...any) {
	l.t.Helper()
	l.logf("[ERROR] "+format, args...)
}

func (l *stripeErrorLogger) Infof(format string, args ...any) {
	l.t.Helper()
	l.logf("[INFO] "+format, args...)
}

func (l *stripeErrorLogger) Warnf(format string, args ...any) {
	l.t.Helper()
	l.logf("[WARN] "+format, args...)
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

func new_[T any](v T) *T {
	return &v
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
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
		rawDB.Close()
		t.Fatalf("failed to run migrations: %v", err)
	}
	rawDB.Close()

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
	dsn := "file:" + dbPath + "?_go_time=true"
	rawDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
		rawDB.Close()
		t.Fatalf("failed to run migrations: %v", err)
	}
	rawDB.Close()

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

// installTestPrices reads prices.json and creates any prices that do not
// already exist in Stripe.
//
// Using without a test API key is an error.
//
// It does not update existing prices.
// If there is a conflict with an existing price in your Sandbox,
// manually archive it and clear the lookup key to allow this to recreate it.
func installTestPrices(m *Manager) error {
	if !strings.Contains(m.APIKey, "_test_") {
		return fmt.Errorf("client API key does not contain '_test_'")
	}

	data, err := os.ReadFile("prices.json")
	if err != nil {
		return fmt.Errorf("read prices.json: %w", err)
	}

	var priceList struct {
		Data []*stripe.Price `json:"data"`
	}
	if err := json.Unmarshal(data, &priceList); err != nil {
		return fmt.Errorf("unmarshal prices.json: %w", err)
	}

	ctx := context.Background()
	c := m.client()

	createdProducts := make(map[string]bool)
	ensureProduct := func(id string) error {
		if createdProducts[id] {
			return nil
		}
		createdProducts[id] = true

		_, err := c.V1Products.Retrieve(ctx, id, nil)
		if err == nil {
			return nil
		}
		if e, ok := errorz.AsType[*stripe.Error](err); ok && e.Code == stripe.ErrorCodeResourceMissing {
			_, err := c.V1Products.Create(ctx, &stripe.ProductCreateParams{
				ID:   new_(id),
				Name: new_("Invididual"),
			})
			if err != nil {
				return fmt.Errorf("create product %q: %w", id, err)
			}
			return nil
		}
		return fmt.Errorf("retrieve product %q: %w", id, err)
	}

	for _, want := range priceList.Data {
		if want.LookupKey == "" {
			continue
		}

		if want.Product == nil || want.Product.ID == "" {
			return fmt.Errorf("price %q: missing product ID in prices.json", want.LookupKey)
		}
		if err := ensureProduct(want.Product.ID); err != nil {
			return err
		}

		// Check if price with this lookup key already exists
		got := func() *stripe.Price {
			for p, err := range c.V1Prices.List(ctx, &stripe.PriceListParams{
				LookupKeys: []*string{&want.LookupKey},
			}) {
				if err == nil {
					return p
				}
			}
			return nil
		}()
		if got != nil {
			// We'll write this one out in the .json and git-diff
			// will tell us if anything changed.
			continue
		}

		// Create the price
		params := &stripe.PriceCreateParams{
			LookupKey:  &want.LookupKey,
			Currency:   new_(string(want.Currency)),
			UnitAmount: &want.UnitAmount,
			Product:    &want.Product.ID,
		}

		if want.Recurring != nil {
			interval := string(want.Recurring.Interval)
			params.Recurring = &stripe.PriceCreateRecurringParams{
				Interval: &interval,
			}
			if want.Recurring.IntervalCount > 0 {
				params.Recurring.IntervalCount = &want.Recurring.IntervalCount
			}
		}

		_, err := c.V1Prices.Create(ctx, params)
		if err != nil {
			if !isStripeParamError(err, "lookup_key") {
				return fmt.Errorf("create price %q: %w", want.LookupKey, err)
			}
		}
	}

	updatedJOSN, err := json.MarshalIndent(priceList, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal updated prices.json: %w", err)
	}
	if err := os.WriteFile("prices.json", updatedJOSN, 0o644); err != nil {
		return fmt.Errorf("write updated prices.json: %w", err)
	}

	return nil
}
