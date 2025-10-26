package route53

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDNSProviderIntegration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	testDomain := os.Getenv("ROUTE53_TEST_DOMAIN")

	if testDomain == "" {
		t.Skip("Skipping integration test: ROUTE53_TEST_DOMAIN not set")
	}

	t.Log("Running integration test with real Route53 API...")

	provider := NewDNSProvider()
	ctx := t.Context()

	recordName := "_acme-challenge-test"
	recordContent := "test-value-" + time.Now().Format("20060102-150405")

	t.Logf("Creating TXT record: %s.%s = %s", recordName, testDomain, recordContent)
	recordID, err := provider.CreateTXTRecord(ctx, testDomain, recordName, recordContent)
	if err != nil {
		t.Fatalf("Failed to create TXT record: %v", err)
	}
	t.Logf("Created record with ID: %s", recordID)

	record, err := provider.FindTXTRecord(ctx, testDomain, recordName, recordContent)
	if err != nil {
		t.Fatalf("Failed to find TXT record: %v", err)
	}
	t.Logf("Found record: %+v", record)

	if err := provider.DeleteRecord(ctx, testDomain, recordID); err != nil {
		t.Fatalf("Failed to delete record: %v", err)
	}
	t.Logf("Successfully deleted record")

	if _, err := provider.FindTXTRecord(ctx, testDomain, recordName, recordContent); err == nil {
		t.Fatalf("Record should have been deleted but was still found")
	}
	t.Logf("Confirmed record was deleted")
}

func TestDNSProviderCreation(t *testing.T) {
	provider := NewDNSProvider()
	if provider == nil {
		t.Fatal("NewDNSProvider returned nil")
	}
	if provider.HTTPClient == nil {
		t.Error("HTTPClient should not be nil")
	}
}

func TestCreateACMEChallengeLogic(t *testing.T) {
	provider := NewDNSProvider()
	ctx := t.Context()

	tests := []struct {
		domain         string
		expectedDomain string
		expectedName   string
		description    string
	}{
		{
			domain:         "example.invalid",
			expectedDomain: "example.invalid",
			expectedName:   "_acme-challenge",
			description:    "regular domain",
		},
		{
			domain:         "*.example.invalid",
			expectedDomain: "example.invalid",
			expectedName:   "_acme-challenge",
			description:    "wildcard domain",
		},
		{
			domain:         "www.example.invalid",
			expectedDomain: "example.invalid",
			expectedName:   "_acme-challenge.www",
			description:    "www subdomain",
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			baseDomain := extractDomain(test.domain)
			if baseDomain != test.expectedDomain {
				t.Errorf("extractDomain(%s) = %s, expected %s", test.domain, baseDomain, test.expectedDomain)
			}

			var challengeName string
			switch {
			case test.domain == test.expectedDomain:
				challengeName = "_acme-challenge"
			case strings.HasPrefix(test.domain, "*."):
				challengeName = "_acme-challenge"
			case strings.HasSuffix(test.domain, "."+test.expectedDomain):
				sub := strings.TrimSuffix(test.domain, "."+test.expectedDomain)
				challengeName = "_acme-challenge." + sub
			}

			if challengeName != test.expectedName {
				t.Errorf("Challenge name for %s = %s, expected %s", test.domain, challengeName, test.expectedName)
			}

			if _, err := provider.CreateACMEChallenge(ctx, test.domain, "test-key-auth"); err == nil {
				t.Error("Expected error when calling CreateACMEChallenge with invalid credentials")
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
		if result := extractDomain(test.input); result != test.expected {
			t.Errorf("extractDomain(%s) = %s, expected %s", test.input, result, test.expected)
		}
	}
}
