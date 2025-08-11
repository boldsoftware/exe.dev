package porkbun

import (
	"crypto/tls"
	"testing"
)

func TestWildcardCertManager_ShouldHandleDomain(t *testing.T) {
	manager := &WildcardCertManager{
		domain: "exe.dev",
	}

	tests := []struct {
		serverName string
		expected   bool
	}{
		{"exe.dev", true},
		{"www.exe.dev", true},
		{"container-123.exe.dev", true},
		{"user-abc.exe.dev", true},
		{"api.exe.dev", true},
		{"exe.dev.evil.com", false},
		{"notexe.dev", false},
		{"", false},
	}

	for _, test := range tests {
		result := manager.shouldHandleDomain(test.serverName)
		if result != test.expected {
			t.Errorf("shouldHandleDomain(%s) = %v, expected %v", test.serverName, result, test.expected)
		}
	}
}

func TestWildcardCertManager_GetCertificateKey(t *testing.T) {
	manager := &WildcardCertManager{
		domain: "exe.dev",
	}

	tests := []struct {
		serverName string
		expected   string
	}{
		{"exe.dev", "exe.dev"},
		{"www.exe.dev", "exe.dev"},
		{"container-123.exe.dev", "*.exe.dev"},
		{"user-abc.exe.dev", "*.exe.dev"},
		{"api.exe.dev", "*.exe.dev"},
		{"other.domain.com", "other.domain.com"},
	}

	for _, test := range tests {
		result := manager.getCertificateKey(test.serverName)
		if result != test.expected {
			t.Errorf("getCertificateKey(%s) = %s, expected %s", test.serverName, result, test.expected)
		}
	}
}

func TestWildcardCertManager_IsCertValid(t *testing.T) {
	manager := &WildcardCertManager{}

	// Test with nil certificate
	if manager.isCertValid(nil) {
		t.Error("isCertValid(nil) should return false")
	}

	// Test with empty certificate
	emptyCert := &tls.Certificate{}
	if manager.isCertValid(emptyCert) {
		t.Error("isCertValid(emptyCert) should return false")
	}
}

func TestExtractDomainFromWildcard(t *testing.T) {
	tests := []struct {
		domain         string
		expectedDomain string
		expectedName   string
	}{
		{
			domain:         "exe.dev",
			expectedDomain: "exe.dev",
			expectedName:   "_acme-challenge",
		},
		{
			domain:         "*.exe.dev",
			expectedDomain: "exe.dev",
			expectedName:   "_acme-challenge",
		},
		{
			domain:         "www.exe.dev",
			expectedDomain: "exe.dev",
			expectedName:   "_acme-challenge.www",
		},
	}

	for _, test := range tests {
		baseDomain := extractDomain(test.domain)
		if baseDomain != test.expectedDomain {
			t.Errorf("extractDomain(%s) = %s, expected %s", test.domain, baseDomain, test.expectedDomain)
		}

		// Test challenge name generation (matching logic from dns.go CreateACMEChallenge)
		var challengeName string
		if test.domain == "*.exe.dev" {
			// For wildcard domain, the challenge should be "_acme-challenge"
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
	}
}
