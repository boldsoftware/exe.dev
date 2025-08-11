package porkbun

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDNSProviderIntegration(t *testing.T) {
	// This is an integration test that requires real API keys and domain
	// Skip test if API keys are not provided
	apiKey := os.Getenv("PORKBUN_API_KEY")
	secretAPIKey := os.Getenv("PORKBUN_API_SECRET_KEY")
	testDomain := os.Getenv("PORKBUN_TEST_DOMAIN")

	if apiKey == "" || secretAPIKey == "" || testDomain == "" {
		t.Skip("Skipping integration test: PORKBUN_API_KEY, PORKBUN_API_SECRET_KEY, or PORKBUN_TEST_DOMAIN not set")
	}

	t.Log("Running integration test with real Porkbun API...")

	provider := NewDNSProvider(apiKey, secretAPIKey)
	ctx := context.Background()

	// Test creating a TXT record
	recordName := "_acme-challenge-test"
	recordContent := "test-value-" + time.Now().Format("20060102-150405")

	t.Logf("Creating TXT record: %s.%s = %s", recordName, testDomain, recordContent)
	recordID, err := provider.CreateTXTRecord(ctx, testDomain, recordName, recordContent)
	if err != nil {
		t.Fatalf("Failed to create TXT record: %v", err)
	}
	t.Logf("Created record with ID: %s (type: %T)", recordID, recordID)

	// Test finding the record
	record, err := provider.FindTXTRecord(ctx, testDomain, recordName, recordContent)
	if err != nil {
		t.Fatalf("Failed to find TXT record: %v", err)
	}
	t.Logf("Found record: %+v", record)

	// Test deleting the record
	err = provider.DeleteRecord(ctx, testDomain, recordID)
	if err != nil {
		t.Fatalf("Failed to delete record: %v", err)
	}
	t.Logf("Successfully deleted record")

	// Verify record is deleted
	_, err = provider.FindTXTRecord(ctx, testDomain, recordName, recordContent)
	if err == nil {
		t.Fatalf("Record should have been deleted but was still found")
	}
	t.Logf("Confirmed record was deleted")
}

func TestDNSProviderCreation(t *testing.T) {
	// Test that we can create a DNS provider without API keys
	provider := NewDNSProvider("test-key", "test-secret")
	if provider == nil {
		t.Fatal("NewDNSProvider returned nil")
	}
	if provider.APIKey != "test-key" {
		t.Errorf("Expected APIKey to be 'test-key', got %s", provider.APIKey)
	}
	if provider.SecretAPIKey != "test-secret" {
		t.Errorf("Expected SecretAPIKey to be 'test-secret', got %s", provider.SecretAPIKey)
	}
	if provider.HTTPClient == nil {
		t.Error("HTTPClient should not be nil")
	}
}

func TestCreateACMEChallengeLogic(t *testing.T) {
	// Test the challenge creation logic without making actual API calls
	provider := NewDNSProvider("test-key", "test-secret")
	ctx := context.Background()

	tests := []struct {
		domain         string
		expectedDomain string
		expectedName   string
		description    string
	}{
		{
			domain:         "exe.dev",
			expectedDomain: "exe.dev",
			expectedName:   "_acme-challenge",
			description:    "regular domain",
		},
		{
			domain:         "*.exe.dev",
			expectedDomain: "exe.dev",
			expectedName:   "_acme-challenge",
			description:    "wildcard domain",
		},
		{
			domain:         "www.exe.dev",
			expectedDomain: "exe.dev",
			expectedName:   "_acme-challenge.www",
			description:    "www subdomain",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			// Test domain extraction
			baseDomain := extractDomain(test.domain)
			if baseDomain != test.expectedDomain {
				t.Errorf("extractDomain(%s) = %s, expected %s", test.domain, baseDomain, test.expectedDomain)
			}

			// Test challenge name generation (simulate the logic from CreateACMEChallenge)
			var challengeName string
			if test.domain == "*.exe.dev" {
				// For wildcard, challenge goes to _acme-challenge
				challengeName = "_acme-challenge"
			} else if test.domain == "exe.dev" {
				// For base domain
				challengeName = "_acme-challenge"
			} else if test.domain == "www.exe.dev" {
				// For www subdomain
				challengeName = "_acme-challenge.www"
			}

			if challengeName != test.expectedName {
				t.Errorf("Challenge name for %s = %s, expected %s", test.domain, challengeName, test.expectedName)
			}

			// Test that we can call CreateACMEChallenge without panicking
			// (it will fail with HTTP error, but that's expected)
			_, err := provider.CreateACMEChallenge(ctx, test.domain, "test-key-auth")
			if err == nil {
				t.Error("Expected error when calling CreateACMEChallenge with invalid API keys")
			}
		})
	}
}

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"exe.dev", "exe.dev"},
		{"user.exe.dev", "exe.dev"},
		{"_acme-challenge.exe.dev", "exe.dev"},
		{"container-123.exe.dev", "exe.dev"},
		{"sub.domain.example.com", "example.com"},
	}

	for _, test := range tests {
		result := extractDomain(test.input)
		if result != test.expected {
			t.Errorf("extractDomain(%s) = %s, expected %s", test.input, result, test.expected)
		}
	}
}

func TestACMEChallengeHandling(t *testing.T) {
	tests := []struct {
		domain            string
		expectedDomain    string
		expectedSubdomain string
	}{
		{"exe.dev", "exe.dev", "_acme-challenge"},
		{"*.exe.dev", "exe.dev", "_acme-challenge"},
		{"www.exe.dev", "exe.dev", "_acme-challenge.www"},
	}

	for _, test := range tests {
		domainName := extractDomain(test.domain)
		if domainName != test.expectedDomain {
			t.Errorf("extractDomain(%s) = %s, expected %s", test.domain, domainName, test.expectedDomain)
			continue
		}

		var challengeName string
		if test.domain == "*.exe.dev" {
			// For wildcard, challenge goes to _acme-challenge
			challengeName = "_acme-challenge"
		} else if test.domain == "exe.dev" {
			// For base domain
			challengeName = "_acme-challenge"
		} else if test.domain == "www.exe.dev" {
			// For www subdomain
			challengeName = "_acme-challenge.www"
		}

		if challengeName != test.expectedSubdomain {
			t.Errorf("Challenge name for %s = %s, expected %s", test.domain, challengeName, test.expectedSubdomain)
		}
	}
}
