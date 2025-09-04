package billing

import (
	"context"
	"os"
	"testing"
	"time"

	"exe.dev/sqlite"
	"exe.dev/vouch"
	"github.com/stripe/stripe-go/v76"
	_ "modernc.org/sqlite"
)

// TestWithStripeMock tests billing functionality against stripe-mock
func TestWithStripeMock(t *testing.T) {
	t.Parallel()
	vouch.For("banksean")

	db := NewTestDB(t)

	billing, cleanup := NewWithMockStripe(t, db)
	defer cleanup()

	// Create test user and allocation
	userID := "test-user-" + time.Now().Format("20060102150405")
	allocID := "test-alloc-" + time.Now().Format("20060102150405")

	// Create user using sqlite.DB transaction
	err := db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (user_id, email, created_at) VALUES (?, ?, datetime('now'))`,
			userID, "test@example.com")
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create allocation using sqlite.DB transaction
	err = db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err = tx.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at)
				VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'))`,
			allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create test alloc: %v", err)
	}

	// Test SetupBilling with stripe-mock
	err = billing.SetupBilling(allocID, "test@billing.com", "4242424242424242", "12", "2025", "123")
	if err != nil {
		t.Fatalf("SetupBilling failed: %v", err)
	}

	// Verify billing info was saved
	billingInfo, err := billing.GetBillingInfo(t.Context(), allocID)
	if err != nil {
		t.Fatalf("GetBillingInfo failed: %v", err)
	}

	if !billingInfo.HasBilling {
		t.Error("Expected HasBilling to be true")
	}
	if billingInfo.Email != "test@billing.com" {
		t.Errorf("Expected email 'test@billing.com', got '%s'", billingInfo.Email)
	}
	if billingInfo.StripeCustomerID == "" {
		t.Error("Expected StripeCustomerID to be set")
	}

	t.Logf("Successfully tested billing with stripe-mock: customer_id=%s", billingInfo.StripeCustomerID)

	// Test UpdatePaymentMethod
	err = billing.UpdatePaymentMethod(billingInfo.StripeCustomerID, "4000000000000002", "01", "2026", "456")
	if err != nil {
		t.Fatalf("UpdatePaymentMethod failed: %v", err)
	}

	// Test UpdateBillingEmail
	err = billing.UpdateBillingEmail(allocID, billingInfo.StripeCustomerID, "newemail@test.com")
	if err != nil {
		t.Fatalf("UpdateBillingEmail failed: %v", err)
	}

	// Verify email was updated
	updatedInfo, err := billing.GetBillingInfo(t.Context(), allocID)
	if err != nil {
		t.Fatalf("GetBillingInfo failed after email update: %v", err)
	}
	if updatedInfo.Email != "newemail@test.com" {
		t.Errorf("Expected updated email 'newemail@test.com', got '%s'", updatedInfo.Email)
	}

	// Test DeleteBillingInfo
	err = billing.DeleteBillingInfo(allocID)
	if err != nil {
		t.Fatalf("DeleteBillingInfo failed: %v", err)
	}

	// Verify billing info was deleted
	deletedInfo, err := billing.GetBillingInfo(t.Context(), allocID)
	if err != nil {
		t.Fatalf("GetBillingInfo failed after delete: %v", err)
	}
	if deletedInfo.HasBilling {
		t.Error("Expected HasBilling to be false after deletion")
	}
}

// TestEnvironmentBasedMocking tests that STRIPE_MOCK_URL environment variable works.
// Since this alters global state (os.Setenv/os.Getenv) do NOT run this with t.Parallel().
func TestEnvironmentBasedMocking(t *testing.T) {
	vouch.For("banksean")
	// Start up an in-process mock stripe server
	mockServer := newMockStripeServer(t)
	defer mockServer.Close()

	// Set environment variable to our in-process mock server and API key
	oldStripeKey := stripe.Key
	stripe.Key = "sk_test_123"
	os.Setenv("STRIPE_MOCK_URL", mockServer.URL)
	defer func() {
		stripe.Key = oldStripeKey
		os.Unsetenv("STRIPE_MOCK_URL")
	}()

	db := NewTestDB(t)

	// Use standard constructor - should pick up environment variables
	billing := New(db)

	// Create test user and allocation
	userID := "test-user-env-" + time.Now().Format("20060102150405")
	allocID := "test-alloc-env-" + time.Now().Format("20060102150405")

	// Create user using sqlite.DB transaction
	err := db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`INSERT INTO users (user_id, email, created_at) VALUES (?, ?, datetime('now'))`,
			userID, "test@example.com")
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create allocation using sqlite.DB transaction
	err = db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err = tx.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at)
				VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'))`,
			allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create test alloc: %v", err)
	}

	// Test that billing works through environment-based mock configuration
	err = billing.SetupBilling(allocID, "env-test@billing.com", "4242424242424242", "12", "2025", "123")
	if err != nil {
		t.Fatalf("SetupBilling failed with env mock: %v", err)
	}

	// Verify it worked
	billingInfo, err := billing.GetBillingInfo(t.Context(), allocID)
	if err != nil {
		t.Fatalf("GetBillingInfo failed: %v", err)
	}

	if !billingInfo.HasBilling {
		t.Error("Expected HasBilling to be true")
	}
}
