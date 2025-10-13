package billing

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/testutil"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
	"github.com/stripe/stripe-mock/embedded"
	"github.com/stripe/stripe-mock/server"
)

func NewWithMockStripe(t *testing.T, db *sqlite.DB) (Billing, func()) {
	mockServer := newMockStripeServer(t)

	// Create mock stripe client pointing to our in-process server
	config := &stripe.BackendConfig{
		URL: stripe.String(mockServer.URL),
	}

	stripeClient := client.New("sk_test_123", &stripe.Backends{
		API: stripe.GetBackendWithConfig(stripe.APIBackend, config),
	})

	// Create billing service with mock client
	return NewWithClient(testutil.Slogger(t), db, stripeClient), func() { mockServer.Close() }
}

func newMockStripeServer(t *testing.T) *httptest.Server {
	// Load stripe-mock's embedded fixtures and spec
	fixtures, err := server.LoadFixtures(embedded.OpenAPIFixtures, "")
	if err != nil {
		t.Fatalf("Failed to load fixtures: %v", err)
	}

	spec, err := server.LoadSpec(embedded.OpenAPISpec, "")
	if err != nil {
		t.Fatalf("Failed to load spec: %v", err)
	}

	// Create the mock handler
	stubServer, err := server.NewStubServer(fixtures, spec, false, false)
	if err != nil {
		t.Fatalf("Failed to create stub server: %v", err)
	}

	// Wrap in HTTP test server
	mockServer := httptest.NewServer(http.HandlerFunc(stubServer.HandleRequest))
	return mockServer
}

func NewTestDB(t *testing.T) *sqlite.DB {
	t.Helper()

	// Create temporary database
	tmpDBFile, err := os.CreateTemp("", "test_billing_mock_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	t.Cleanup(func() { os.Remove(tmpDBFile.Name()) })
	tmpDBFile.Close()

	// First open with sql.DB for migrations
	sqlDB, err := sql.Open("sqlite", tmpDBFile.Name())
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	if err := exedb.RunMigrations(testutil.Slogger(t), sqlDB); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Now open with sqlite.DB for the billing service
	db, err := sqlite.New(tmpDBFile.Name(), 5)
	if err != nil {
		t.Fatalf("failed to create sqlite.DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}
