package porkbun

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
func NewWildcardCertManager(domain, email, porkbunAPIKey, porkbunSecretAPIKey string, cache autocert.Cache) *WildcardCertManager {
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
		dnsProvider:  NewDNSProvider(porkbunAPIKey, porkbunSecretAPIKey),
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
		// Parse the certificate if leaf is not set
		x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return false
		}
		cert.Leaf = x509Cert
	}

	// Check if certificate expires within 30 days
	return time.Now().Add(30 * 24 * time.Hour).Before(cert.Leaf.NotAfter)
}

// obtainCertificate obtains a new certificate using DNS-01 challenge
func (w *WildcardCertManager) obtainCertificate(ctx context.Context, domain string) (*tls.Certificate, error) {
	log.Printf("obtainCertificate called with domain: %s", domain)
	// First, try to get from cache
	if w.cache != nil {
		log.Printf("Checking cache for certificate: %s", domain)
		if certData, err := w.cache.Get(ctx, domain); err == nil {
			// Parse the PEM data to extract cert and key
			var certPEMBlock, keyPEMBlock []byte
			rest := certData
			for len(rest) > 0 {
				var block *pem.Block
				block, rest = pem.Decode(rest)
				if block == nil {
					break
				}
				if block.Type == "CERTIFICATE" {
					certPEMBlock = append(certPEMBlock, pem.EncodeToMemory(block)...)
				} else if block.Type == "RSA PRIVATE KEY" {
					keyPEMBlock = pem.EncodeToMemory(block)
				}
			}

			if len(certPEMBlock) > 0 && len(keyPEMBlock) > 0 {
				// Try to parse the cached certificate
				if cert, err := tls.X509KeyPair(certPEMBlock, keyPEMBlock); err == nil {
					if w.isCertValid(&cert) {
						log.Printf("Using cached certificate for %s", domain)
						return &cert, nil
					}
					log.Printf("Cached certificate for %s is expired or expiring soon", domain)
				} else {
					log.Printf("Failed to parse cached certificate for %s: %v", domain, err)
				}
			} else {
				log.Printf("Invalid cached certificate format for %s", domain)
			}
		} else {
			log.Printf("No cached certificate found for %s", domain)
		}
	}

	// Register with ACME if not already registered
	if w.acmeClient.HTTPClient == nil {
		log.Printf("Registering with ACME directory for %s", w.email)
		account := &acme.Account{
			Contact: []string{"mailto:" + w.email},
		}
		_, err := w.acmeClient.Register(ctx, account, acme.AcceptTOS)
		if err != nil {
			// Handle "account already exists" error gracefully
			if strings.Contains(err.Error(), "account already exists") {
				log.Printf("ACME account already exists for %s, continuing with existing account", w.email)
				// Continue - we have the right key for the existing account
			} else {
				return nil, fmt.Errorf("failed to register with ACME: %w", err)
			}
		} else {
			log.Printf("Successfully registered new ACME account")
		}
	}

	// Create order for the certificate
	// For wildcard certificates, we need to request both the domain and *.domain
	var authzIDs []acme.AuthzID
	if domain == w.domain {
		// Main domain cert should include both exe.dev and *.exe.dev
		log.Printf("Creating ACME order for wildcard certificate: %s and *.%s", domain, domain)
		authzIDs = []acme.AuthzID{
			{Type: "dns", Value: domain},
			{Type: "dns", Value: "*." + domain},
		}
	} else if strings.HasPrefix(domain, "*.") {
		// Already a wildcard domain
		log.Printf("Creating ACME order for wildcard domain: %s", domain)
		authzIDs = []acme.AuthzID{
			{Type: "dns", Value: domain},
		}
	} else {
		// Single subdomain
		log.Printf("Creating ACME order for domain: %s", domain)
		authzIDs = []acme.AuthzID{
			{Type: "dns", Value: domain},
		}
	}

	log.Printf("Calling ACME AuthorizeOrder API...")
	order, err := w.acmeClient.AuthorizeOrder(ctx, authzIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to create order: %w", err)
	}
	log.Printf("ACME order created successfully with %d authorizations", len(order.AuthzURLs))

	// Process each authorization
	log.Printf("Processing %d authorizations", len(order.AuthzURLs))
	for i, authzURL := range order.AuthzURLs {
		log.Printf("Processing authorization %d/%d: %s", i+1, len(order.AuthzURLs), authzURL)
		authorization, err := w.acmeClient.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("failed to get authorization: %w", err)
		}
		log.Printf("Authorization status: %s for domain: %s", authorization.Status, authorization.Identifier.Value)

		if authorization.Status == acme.StatusValid {
			log.Printf("Authorization already valid, skipping")
			continue
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

	// Parse the leaf certificate
	x509Cert, err := x509.ParseCertificate(der[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}
	cert.Leaf = x509Cert

	// Cache the certificate (convert DER to PEM for caching)
	if w.cache != nil {
		// Convert certificate chain to PEM format
		var certPEM []byte
		for _, certDER := range der {
			block := &pem.Block{
				Type:  "CERTIFICATE",
				Bytes: certDER,
			}
			certPEM = append(certPEM, pem.EncodeToMemory(block)...)
		}

		// Convert private key to PEM format
		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})

		// Combine cert and key for caching
		fullPEM := append(certPEM, keyPEM...)
		w.cache.Put(ctx, domain, fullPEM)
	}

	log.Printf("Successfully obtained certificate for %s", domain)
	return cert, nil
}

// TLSConfig returns a TLS configuration that uses this wildcard cert manager
func (w *WildcardCertManager) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: w.GetCertificate,
	}
}
