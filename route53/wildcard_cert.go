package route53

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

func pkFromData(keyData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyData)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		return nil, fmt.Errorf("failed to decode PEM block containing RSA private key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSA private key: %w", err)
	}
	return key, nil
}

// loadOrGenerateACMEKey loads an existing ACME account key from cache or generates a new one
func loadOrGenerateACMEKey(cache autocert.Cache) (*rsa.PrivateKey, error) {
	const acmeAccountKeyName = "acme_account+key"
	if cache == nil {
		return nil, errors.New("loadOrGenerateACMEKey: nil cache")
	}
	ctx := context.Background()
	keyData, err := cache.Get(ctx, acmeAccountKeyName)
	if err == nil {
		key, err := pkFromData(keyData)
		if err == nil {
			slog.Info("loaded cached ACME account key")
			return key, nil
		}
		slog.Warn("failed to parse cached ACME account key, generating new one")
	} else {
		slog.Info("no cached ACME account key found, generating new one")
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048) // standard for ACME account keys
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA key: %w", err)
	}

	keyBytes := x509.MarshalPKCS1PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: keyBytes,
	})

	// Best effort to cache the new key
	err = cache.Put(ctx, acmeAccountKeyName, keyPEM)
	if err != nil {
		slog.Warn("failed to cache ACME account key", "error", err)
	} else {
		slog.Info("generated and cached new ACME account key")
	}

	return key, nil
}

// WildcardCertManager manages wildcard certificates using DNS-01 challenge
const (
	certificateCacheTimeout = 5 * time.Second
)

type WildcardCertManager struct {
	mu           sync.RWMutex
	certificates map[string]*tls.Certificate
	dnsProvider  *DNSProvider
	cache        autocert.Cache
	acmeClient   *acme.Client
	domains      []string
	certRequests prometheus.Counter
}

// NewWildcardCertManager creates a new wildcard certificate manager
func NewWildcardCertManager(domains []string, cache autocert.Cache, certRequests prometheus.Counter) *WildcardCertManager {
	// Try to load existing ACME account key from cache, or generate new one
	key, err := loadOrGenerateACMEKey(cache)
	if err != nil {
		panic(fmt.Sprintf("failed to load or generate ACME key: %v", err))
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
		domains:      domains,
		certRequests: certRequests,
	}
}

// GetCertificate implements the tls.Config.GetCertificate interface
func (w *WildcardCertManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello.ServerName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	// Determine which certificate to use
	certKey := w.domainForServerName(hello.ServerName)
	if certKey == "" {
		// Not a domain we manage.
		return nil, fmt.Errorf("unrecognized domain: %q", hello.ServerName)
	}

	w.mu.RLock()
	cert, exists := w.certificates[certKey]
	w.mu.RUnlock()

	if exists && w.isCertValid(cert) {
		return cert, nil
	}

	if cachedCert, err := w.loadCertificateFromCache(certKey); err == nil {
		if w.isCertValid(cachedCert) {
			w.mu.Lock()
			w.certificates[certKey] = cachedCert
			w.mu.Unlock()
			return cachedCert, nil
		}
	} else if !errors.Is(err, autocert.ErrCacheMiss) {
		slog.Warn("failed to load cached certificate", "certKey", certKey, "error", err)
	}

	// Need to obtain a new certificate
	// Create a context with timeout for certificate acquisition
	certCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	slog.Warn("Obtaining new certificate (cache fail)", "certKey", certKey, "serverName", hello.ServerName)
	newCert, err := w.obtainCertificate(certCtx, certKey)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain certificate for %s: %w", certKey, err)
	}

	w.mu.Lock()
	w.certificates[certKey] = newCert
	w.mu.Unlock()

	return newCert, nil
}

// isSingleLevelSubdomain reports whether serverName is a single-level subdomain of domain.
// For example:
//
//	isSingleLevelSubdomain("www.domain.com", "domain.com") == true
//	isSingleLevelSubdomain("api.domain.com", "domain.com") == true
//	isSingleLevelSubdomain("api.www.domain.com", "domain.com") == false
//	isSingleLevelSubdomain("domain.com", "domain.com") == false
func isSingleLevelSubdomain(domain, serverName string) bool {
	prefix, ok := strings.CutSuffix(serverName, "."+domain)
	return ok && !strings.Contains(prefix, ".")
}

// domainForServerName returns the (possibly wildcard) domain corresponding to serverName.
// If domainForServerName returns an empty string, we do not manager serverName.
func (w *WildcardCertManager) domainForServerName(serverName string) string {
	// We accept apex domains and single-level subdomains.
	// Note that when the set of domains includes subdomains,
	// such as {"exe.dev", "xterm.exe.dev"},
	// we can match "xterm.exe.dev" in multiple ways:
	//   - exact match to "xterm.exe.dev"
	//   - single-level subdomain match to "exe.dev"
	// In such cases, we use the exact match.

	// Prefer exact matches.
	for _, domain := range w.domains {
		if serverName == domain {
			return domain
		}
	}

	// Now check for single-level subdomains.
	// Convert single-level subdomains to their corresponding apex domain,
	// because that's the certificate we will use.
	for _, domain := range w.domains {
		if isSingleLevelSubdomain(domain, serverName) {
			return domain
		}
	}

	return ""
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
			slog.Warn("failed to parse certificate", "error", err)
			return false
		}
		cert.Leaf = leaf
	}

	// Check expiration - renew 10 days before expiry
	renewBefore := 10 * 24 * time.Hour
	if time.Until(cert.Leaf.NotAfter) < renewBefore {
		slog.Warn("certificate expiring soon",
			"commonName", cert.Leaf.Subject.CommonName,
			"expiresAt", cert.Leaf.NotAfter)
		return false
	}

	return true
}

// obtainCertificate obtains a new wildcard certificate from Let's Encrypt.
// domain must be the root domain for the certificate, as obtained from domainForServerName.
func (w *WildcardCertManager) obtainCertificate(ctx context.Context, domain string) (*tls.Certificate, error) {
	domains := []string{domain, "*." + domain} // always request both root and wildcard
	slog.Info("obtaining certificate", "domains", domains)

	// Create an ACME order for the desired domains
	order, err := w.acmeClient.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return nil, fmt.Errorf("failed to authorize order: %w", err)
	}

	// Increment metric for Let's Encrypt certificate request
	if w.certRequests != nil {
		w.certRequests.Inc()
	}

	// Handle each authorization
	for _, authzURL := range order.AuthzURLs {
		slog.Debug("fetching authorization", "url", authzURL)

		authorization, err := w.acmeClient.GetAuthorization(ctx, authzURL)
		if err != nil {
			return nil, fmt.Errorf("failed to get authorization: %w", err)
		}

		// Find DNS-01 challenge
		slog.Debug("looking for DNS-01 challenge", "challengeCount", len(authorization.Challenges))
		var challenge *acme.Challenge
		for _, c := range authorization.Challenges {
			slog.Debug("found challenge type", "type", c.Type)
			if c.Type == "dns-01" {
				challenge = c
				break
			}
		}

		if challenge == nil {
			return nil, fmt.Errorf("no DNS-01 challenge found")
		}
		slog.Debug("selected DNS-01 challenge", "challengeURI", challenge.URI)

		// Calculate key authorization
		keyAuth, err := w.acmeClient.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate key authorization: %w", err)
		}

		// Present the challenge
		slog.Info("creating DNS TXT record for ACME challenge",
			"domain", authorization.Identifier.Value)
		recordID, err := w.dnsProvider.CreateACMEChallenge(ctx, authorization.Identifier.Value, keyAuth)
		if err != nil {
			return nil, fmt.Errorf("failed to create ACME challenge: %w", err)
		}
		slog.Info("DNS TXT record created for ACME challenge", "recordID", recordID)

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
		slog.Info("completed DNS-01 challenge",
			"domain", authorization.Identifier.Value,
			"recordID", recordID)
	}

	// Generate certificate signing request
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate key: %w", err)
	}

	// Build the CSR with the exact domains requested in the ACME order
	if len(domains) == 0 {
		return nil, fmt.Errorf("no domains configured for CSR")
	}

	commonName := domains[0]
	dnsNames := slices.Clone(domains)

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
			slog.Warn("failed to parse leaf certificate", "error", err)
		} else {
			cert.Leaf = leaf
		}
	}

	if err := w.cacheCertificate(domain, cert); err != nil && !errors.Is(err, autocert.ErrCacheMiss) {
		slog.Warn("failed to cache certificate", "domain", domain, "error", err)
	}

	return cert, nil
}

func (w *WildcardCertManager) cacheCertificate(domain string, cert *tls.Certificate) error {
	if w.cache == nil || cert == nil {
		return autocert.ErrCacheMiss
	}

	data, err := encodeCertificateForCache(cert)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), certificateCacheTimeout)
	defer cancel()
	return w.cache.Put(ctx, w.cacheKey(domain), data)
}

func (w *WildcardCertManager) loadCertificateFromCache(domain string) (*tls.Certificate, error) {
	if w.cache == nil {
		return nil, autocert.ErrCacheMiss
	}

	ctx, cancel := context.WithTimeout(context.Background(), certificateCacheTimeout)
	defer cancel()

	cacheKey := w.cacheKey(domain)
	data, err := w.cache.Get(ctx, cacheKey)
	if err != nil {
		return nil, err
	}

	return decodeCertificateFromCache(data)
}

// TOOD(philip): We can probably just cache by domain or domain.lower() and things would be fine.
func (w *WildcardCertManager) cacheKey(domain string) string {
	return wildcardCachePrefix + strings.ToLower(domain)
}

func encodeCertificateForCache(cert *tls.Certificate) ([]byte, error) {
	if cert == nil {
		return nil, fmt.Errorf("certificate is nil")
	}

	key, ok := cert.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("unsupported private key type %T", cert.PrivateKey)
	}

	keyBytes := x509.MarshalPKCS1PrivateKey(key)

	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return nil, err
	}

	for _, der := range cert.Certificate {
		if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

func decodeCertificateFromCache(data []byte) (*tls.Certificate, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("certificate cache data is empty")
	}

	var (
		certs [][]byte
		key   *rsa.PrivateKey
		rest  = data
	)

	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		switch block.Type {
		case "CERTIFICATE":
			certs = append(certs, block.Bytes)
		case "RSA PRIVATE KEY":
			parsedKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse cached private key: %w", err)
			}
			key = parsedKey
		default:
			// Ignore unknown blocks
		}
	}

	if key == nil {
		return nil, fmt.Errorf("cached certificate missing private key")
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("cached certificate missing certificate chain")
	}

	result := &tls.Certificate{
		Certificate: certs,
		PrivateKey:  key,
	}

	if leaf, err := x509.ParseCertificate(certs[0]); err == nil {
		result.Leaf = leaf
	}

	return result, nil
}
