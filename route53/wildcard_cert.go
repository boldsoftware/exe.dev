package route53

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// loadOrGenerateACMEKey loads an existing ACME account key from cache or generates a new one
func loadOrGenerateACMEKey(cache autocert.Cache) (*rsa.PrivateKey, error) {
	const acmeAccountKeyName = "acme_account+key"

	if cache != nil {
		// Try to load existing key from cache
		if keyData, err := cache.Get(context.Background(), acmeAccountKeyName); err == nil {
			// Parse PEM-encoded private key
			block, _ := pem.Decode(keyData)
			if block != nil && block.Type == "RSA PRIVATE KEY" {
				if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
					log.Printf("Loaded existing ACME account key from cache")
					return key, nil
				}
			}
			log.Printf("Failed to parse cached ACME account key, generating new one")
		} else {
			log.Printf("No cached ACME account key found, generating new one")
		}
	}

	// Generate new key
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	// Save to cache
	if cache != nil {
		// Encode key as PEM
		keyBytes := x509.MarshalPKCS1PrivateKey(key)
		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: keyBytes,
		})

		if err := cache.Put(context.Background(), acmeAccountKeyName, keyPEM); err != nil {
			log.Printf("Failed to cache ACME account key: %v", err)
		} else {
			log.Printf("Generated and cached new ACME account key")
		}
	}

	return key, nil
}

// WildcardCertManager manages wildcard certificates using DNS-01 challenge
type WildcardCertManager struct {
	mu           sync.RWMutex
	certificates map[string]*tls.Certificate
	dnsProvider  *DNSProvider
	cache        autocert.Cache
	acmeClient   *acme.Client
	domain       string
	email        string
}

// NewWildcardCertManager creates a new wildcard certificate manager
func NewWildcardCertManager(domain, email string, cache autocert.Cache) *WildcardCertManager {
	// Try to load existing ACME account key from cache, or generate new one
	key, err := loadOrGenerateACMEKey(cache)
	if err != nil {
		log.Fatalf("Failed to load or generate ACME key: %v", err)
	}

	client := &acme.Client{
		Key:          key,
		DirectoryURL: "https://acme-v02.api.letsencrypt.org/directory",
	}

	return &WildcardCertManager{
		certificates: make(map[string]*tls.Certificate),
		dnsProvider:  NewDNSProvider(),
		cache:        cache,
		acmeClient:   client,
		domain:       domain,
		email:        email,
	}
}

// GetCertificate implements the tls.Config.GetCertificate interface
func (w *WildcardCertManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello.ServerName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	// Check if this is a domain we should handle
	if !w.shouldHandleDomain(hello.ServerName) {
		return nil, fmt.Errorf("domain %s not handled by wildcard cert manager", hello.ServerName)
	}

	// Determine which certificate to use
	certKey := w.getCertificateKey(hello.ServerName)

	w.mu.RLock()
	cert, exists := w.certificates[certKey]
	w.mu.RUnlock()

	if exists && w.isCertValid(cert) {
		return cert, nil
	}

	// Need to obtain a new certificate
	// Create a context with timeout for certificate acquisition
	certCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	newCert, err := w.obtainCertificate(certCtx, certKey)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain certificate for %s: %w", certKey, err)
	}

	w.mu.Lock()
	w.certificates[certKey] = newCert
	w.mu.Unlock()

	return newCert, nil
}

// shouldHandleDomain checks if we should handle this domain
func (w *WildcardCertManager) shouldHandleDomain(serverName string) bool {
	// Handle main domain
	if serverName == w.domain {
		return true
	}

	// Handle www.domain
	if serverName == "www."+w.domain {
		return true
	}

	// Handle any subdomain of the main domain (for wildcard cert)
	if strings.HasSuffix(serverName, "."+w.domain) {
		return true
	}

	return false
}

// getCertificateKey returns the certificate key to use for a given server name
func (w *WildcardCertManager) getCertificateKey(serverName string) string {
	// For main domain and www, use the main domain cert
	if serverName == w.domain || serverName == "www."+w.domain {
		return w.domain
	}

	// For subdomains, extract the team name and return *.team.exe.dev
	if strings.HasSuffix(serverName, "."+w.domain) {
		// Remove the main domain suffix to get the subdomain part
		subdomain := strings.TrimSuffix(serverName, "."+w.domain)

		// Split subdomain parts (e.g., "machine.team" -> ["machine", "team"])
		parts := strings.Split(subdomain, ".")

		if len(parts) == 1 {
			// Single level subdomain (e.g., "api.exe.dev") - use regular wildcard
			return "*." + w.domain
		} else if len(parts) == 2 {
			// Two level subdomain (e.g., "machine.team.exe.dev") - use team-specific wildcard
			teamName := parts[1]
			return "*." + teamName + "." + w.domain
		} else {
			// More than 2 levels - not supported
			return serverName
		}
	}

	return serverName
}

// isCertValid checks if a certificate is valid and not expiring soon
func (w *WildcardCertManager) isCertValid(cert *tls.Certificate) bool {
	if cert == nil {
		return false
	}

	if cert.Leaf == nil {
		// Check if we have any certificate data
		if len(cert.Certificate) == 0 {
			return false
		}

		// Parse the leaf certificate
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			log.Printf("Failed to parse certificate: %v", err)
			return false
		}
		cert.Leaf = leaf
	}

	// Check expiration - renew 10 days before expiry
	renewBefore := 10 * 24 * time.Hour
	if time.Until(cert.Leaf.NotAfter) < renewBefore {
		log.Printf("Certificate for %s is expiring soon (expires at %v), needs renewal", cert.Leaf.Subject.CommonName, cert.Leaf.NotAfter)
		return false
	}

	return true
}

// obtainCertificate obtains a new wildcard certificate from Let's Encrypt
func (w *WildcardCertManager) obtainCertificate(ctx context.Context, domain string) (*tls.Certificate, error) {
	log.Printf("Obtaining certificate for domain: %s", domain)

	// Determine domains to include in the certificate request
	var domains []string
	if domain == w.domain {
		// Main domain certificate should include wildcard for subdomains
		domains = []string{w.domain, "*." + w.domain}
	} else if strings.HasPrefix(domain, "*.") {
		// Wildcard domain like *.team.exe.dev
		domains = []string{domain}
	} else {
		// Specific subdomain
		domains = []string{domain}
	}

	// Create an ACME order for the desired domains
	order, err := w.acmeClient.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return nil, fmt.Errorf("failed to authorize order: %w", err)
	}

	// Handle each authorization
	for _, authzURL := range order.AuthzURLs {
		log.Printf("Fetching authorization: %s", authzURL)

		authorization, err := w.acmeClient.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("failed to get authorization: %w", err)
		}

		// Find DNS-01 challenge
		log.Printf("Looking for DNS-01 challenge among %d challenges", len(authorization.Challenges))
		var challenge *acme.Challenge
		for _, c := range authorization.Challenges {
			log.Printf("Found challenge type: %s", c.Type)
			if c.Type == "dns-01" {
				challenge = c
				break
			}
		}

		if challenge == nil {
			return nil, fmt.Errorf("no DNS-01 challenge found")
		}
		log.Printf("Found DNS-01 challenge: %s", challenge.URI)

		// Calculate key authorization
		keyAuth, err := w.acmeClient.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate key authorization: %w", err)
		}

		// Present the challenge
		log.Printf("Creating DNS TXT record for challenge: domain=%s, keyAuth=%s", authorization.Identifier.Value, keyAuth)
		recordID, err := w.dnsProvider.CreateACMEChallenge(ctx, authorization.Identifier.Value, keyAuth)
		if err != nil {
			return nil, fmt.Errorf("failed to create ACME challenge: %w", err)
		}
		log.Printf("Successfully created DNS TXT record with ID: %s", recordID)

		// Wait a bit for DNS propagation
		time.Sleep(10 * time.Second)

		// Accept the challenge
		_, err = w.acmeClient.Accept(ctx, challenge)
		if err != nil {
			// Clean up on error
			w.dnsProvider.CleanupACMEChallenge(ctx, authorization.Identifier.Value, keyAuth)
			return nil, fmt.Errorf("failed to accept challenge: %w", err)
		}

		// Wait for authorization to complete
		_, err = w.acmeClient.WaitAuthorization(ctx, authzURL)
		if err != nil {
			// Clean up on error
			w.dnsProvider.CleanupACMEChallenge(ctx, authorization.Identifier.Value, keyAuth)
			return nil, fmt.Errorf("authorization failed: %w", err)
		}

		// Clean up the challenge record
		w.dnsProvider.CleanupACMEChallenge(ctx, authorization.Identifier.Value, keyAuth)

		// Log successful challenge
		log.Printf("Successfully completed DNS-01 challenge for %s (record ID: %s)", authorization.Identifier.Value, recordID)
	}

	// Generate certificate signing request
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate key: %w", err)
	}

	// Build the CSR with the same domains we requested in the order
	var dnsNames []string
	var commonName string

	if domain == w.domain {
		// Main domain cert should include both exe.dev and *.exe.dev
		commonName = domain
		dnsNames = []string{domain, "*." + domain}
	} else if strings.HasPrefix(domain, "*.") {
		// Already a wildcard domain
		commonName = domain
		dnsNames = []string{domain}
	} else {
		// Single subdomain
		commonName = domain
		dnsNames = []string{domain}
	}

	req := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: commonName,
		},
		DNSNames: dnsNames,
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, req, key)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	// Finalize the order
	der, _, err := w.acmeClient.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	if err != nil {
		return nil, fmt.Errorf("failed to finalize order: %w", err)
	}

	// Create the certificate from DER chain and private key
	cert := &tls.Certificate{
		Certificate: der,
		PrivateKey:  key,
	}

	// Parse and assign the leaf certificate for easier access
	if len(der) > 0 {
		leaf, err := x509.ParseCertificate(der[0])
		if err != nil {
			log.Printf("Failed to parse leaf certificate: %v", err)
		} else {
			cert.Leaf = leaf
		}
	}

	return cert, nil
}
