# Billing Package

The billing package provides Stripe integration for handling customer billing and payment methods.

## Unit testing with NewWithMockStripe

The unit tests in this package instantiate an in-process mock stripe server and exercise package billing's methods against that.

If you need to write unit tests for something with a mock implementation of the stripe service dependency outside this package, you can use `NewWithMockStripe` like so:

```go
import (
  // ...
  "exe.dev/billing"
  // ...
)

func TestFoo(t *testing.T) {
  // set up prerequisites for
  db := NewTestDB(t)

  billing, cleanup := billing.NewWithMockStripe(t, db)
  defer cleanup()

  server, _ := NewServer(...)

  sshServer := NewSSHServer(server, billing)

  // exercise methods on sshServer ...
}
```
## Manual testing with stripe-mock

To run exed with the mock Stripe service instead of the real Stripe API:

### 1. Install stripe-mock

```bash
# With Go
go install github.com/stripe/stripe-mock@latest

# Or with Homebrew
brew install stripe/stripe-mock/stripe-mock
```

### 2. Start the mock server

```bash
stripe-mock
```

This starts the mock server on `http://localhost:12111` by default.

### 3. Run exed with mock configuration

```bash
export STRIPE_MOCK_URL=http://localhost:12111
make run-dev
```

The billing service will automatically detect the `STRIPE_MOCK_URL` environment variable and configure the Stripe client to use the mock server instead of the real Stripe API.

### 4. Custom mock server port

If you need to run stripe-mock on a different port:

```bash
# Start stripe-mock on port 8080
stripe-mock -http-port 8080

# Configure exed to use the custom port
export STRIPE_MOCK_URL=http://localhost:8080
make run-dev
```

## How it works

- When `STRIPE_MOCK_URL` is set, the billing service creates a Stripe client with a custom backend pointing to the mock server
- When `STRIPE_MOCK_URL` is not set, the billing service uses the standard Stripe API endpoints
- All billing interface calls (customer creation, payment methods, etc.) are invoked identically regardless of whether using mock or real Stripe

## Important Notes

- stripe-mock provides basic API compatibility but doesn't replicate all Stripe business logic
- Always test critical payment flows against Stripe's test environment before production deployment
- Mock responses are hardcoded and may not reflect real-world edge cases
