package billing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"exe.dev/sqlite"
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
	return NewWithClient(db, stripeClient), func() { mockServer.Close() }
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
