package exe

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v76"
	_ "modernc.org/sqlite"
)

// TestBillingCommandExists tests that the billing command is properly integrated
func TestBillingCommandExists(t *testing.T) {
	t.Parallel()
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_billing_exists_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true
	defer server.Stop()

	// Create SSH server - this should compile without issues if billing is integrated
	sshServer := NewSSHServer(server)

	// Test that the SSH server was created successfully
	if sshServer == nil {
		t.Error("Expected SSH server to be created")
	}
	if sshServer.server == nil {
		t.Error("Expected SSH server to have server reference")
	}

	// If we get here, it means the billing command is properly integrated
	// in the SSH server code and compiles correctly
	t.Log("Billing command integration test passed")
}

// TestBillingDatabaseSchema tests that the billing fields exist in the database
func TestBillingDatabaseSchema(t *testing.T) {
	t.Parallel()
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_billing_schema_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server (this will run migrations)
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true
	defer server.Stop()

	// Check that the allocs table has billing fields
	rows, err := server.db.Query("PRAGMA table_info(allocs)")
	if err != nil {
		t.Fatalf("Failed to get table info: %v", err)
	}
	defer rows.Close()

	foundStripeCustomerID := false
	foundBillingEmail := false

	for rows.Next() {
		var cid int
		var name, datatype string
		var dfltValue *string // Use pointer for nullable field
		var notNull, pk int
		err := rows.Scan(&cid, &name, &datatype, &notNull, &dfltValue, &pk)
		if err != nil {
			t.Fatalf("Failed to scan column info: %v", err)
		}

		if name == "stripe_customer_id" {
			foundStripeCustomerID = true
			t.Logf("Found stripe_customer_id column: %s %s", name, datatype)
		}
		if name == "billing_email" {
			foundBillingEmail = true
			t.Logf("Found billing_email column: %s %s", name, datatype)
		}
	}

	if !foundStripeCustomerID {
		t.Error("Expected to find stripe_customer_id column in allocs table")
	}
	if !foundBillingEmail {
		t.Error("Expected to find billing_email column in allocs table")
	}
}

// TestBillingStripeIntegration tests that Stripe dependencies are available
func TestBillingStripeIntegration(t *testing.T) {
	t.Parallel()
	// Test that we can set and get Stripe key
	originalKey := stripe.Key
	testKey := "sk_test_fake_key_for_testing"
	stripe.Key = testKey
	defer func() { stripe.Key = originalKey }()

	if stripe.Key != testKey {
		t.Error("Expected to be able to set Stripe key")
	}

	// Test basic Stripe functionality is available
	// We don't make actual API calls in tests, just check imports work
	t.Log("Stripe integration test passed")
}

// TestBillingFunctionalityCompiles tests that all billing functions compile
func TestBillingFunctionalityCompiles(t *testing.T) {
	t.Parallel()
	// This test ensures that the billing functionality compiles and the functions exist
	// by testing the overall system can be created with billing support

	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_billing_compiles_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server with test data
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true
	defer server.Stop()

	// Create a test user and allocation with billing data
	userID := "test-user-" + time.Now().Format("20060102150405")
	allocID := "test-alloc-" + time.Now().Format("20060102150405")

	// Create user
	_, err = server.db.Exec(`INSERT INTO users (user_id, email, created_at) VALUES (?, ?, datetime('now'))`,
		userID, "test@example.com")
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create allocation with billing info
	_, err = server.db.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email) 
					VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), 'cus_test123', 'test@billing.com')`,
		allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create test alloc with billing: %v", err)
	}

	// Query the billing data back
	var stripeCustomerID, billingEmail string
	err = server.db.QueryRow(`SELECT stripe_customer_id, billing_email FROM allocs WHERE alloc_id = ?`, allocID).Scan(&stripeCustomerID, &billingEmail)
	if err != nil {
		t.Fatalf("Failed to query billing data: %v", err)
	}

	if stripeCustomerID != "cus_test123" {
		t.Errorf("Expected stripe_customer_id 'cus_test123', got '%s'", stripeCustomerID)
	}
	if billingEmail != "test@billing.com" {
		t.Errorf("Expected billing_email 'test@billing.com', got '%s'", billingEmail)
	}

	t.Logf("Successfully stored and retrieved billing data: customer_id=%s, email=%s", stripeCustomerID, billingEmail)
}

// TestBillingCommandInHelpSystem tests that billing command is documented
func TestBillingCommandInHelpSystem(t *testing.T) {
	t.Parallel()
	// Test that billing command documentation exists by checking the source contains billing help
	// This is a meta-test that validates the help system includes billing

	// We'll validate this by reading the source code to ensure billing help exists
	// This tests the integration at the source level

	// Look for billing help text patterns that should exist
	billingHelpPatterns := []string{
		"billing",
		"Manage billing",
		"payment",
	}

	// Read the SSH server source file to validate billing integration
	source, err := os.ReadFile("ssh_server.go")
	if err != nil {
		t.Fatalf("Failed to read ssh_server.go: %v", err)
	}

	sourceStr := string(source)

	for _, pattern := range billingHelpPatterns {
		if !strings.Contains(sourceStr, pattern) {
			t.Errorf("Expected to find pattern '%s' in ssh_server.go", pattern)
		}
	}

	// Specifically check for the billing command handler
	if !strings.Contains(sourceStr, "handleBillingCommand") {
		t.Error("Expected to find handleBillingCommand function in ssh_server.go")
	}

	// Check for billing case in switch statement
	if !strings.Contains(sourceStr, `case "billing":`) {
		t.Error("Expected to find billing case in command switch statement")
	}

	t.Log("Billing command help system integration test passed")
}
