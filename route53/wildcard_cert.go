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
	"tailscale.com/util/singleflight"
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
	certificateRenewBefore  = 10 * 24 * time.Hour
)

type WildcardCertManager struct {
	dnsProvider  *DNSProvider
	diskCache    autocert.Cache // persistent cache for certificates
	acmeClient   *acme.Client
	domains      []string // list of domains managed by this cert manager; each entry covers itself and a wildcard cert
	certRequests prometheus.Counter
	sf           singleflight.Group[string, *tls.Certificate] // singleflight group for certificate requests

	renewalsInFlight sync.Map // background renewals currently running per domain

	mu       sync.Mutex                  // protects certificates
	memCerts map[string]*tls.Certificate // in-memory cache of certificates to avoid repeated decoding and disk reads
}

// NewWildcardCertManager creates a new wildcard certificate manager
func NewWildcardCertManager(domains []string, diskCache autocert.Cache, certRequests prometheus.Counter) *WildcardCertManager {
	if len(domains) == 0 {
		panic("NewWildcardCertManager: no domains provided")
	}
	if diskCache == nil {
		panic("NewWildcardCertManager: nil diskCache")
	}
	if certRequests == nil {
		panic("NewWildcardCertManager: nil certRequests counter")
	}

	// Try to load existing ACME account key from cache, or generate new one
	key, err := loadOrGenerateACMEKey(diskCache)
	if err != nil {
		panic(fmt.Sprintf("failed to load or generate ACME key: %v", err))
	}

	client := &acme.Client{
		Key:          key,
		DirectoryURL: "https://acme-v02.api.letsencrypt.org/directory",
	}

	manager := &WildcardCertManager{
		memCerts:     make(map[string]*tls.Certificate),
		dnsProvider:  NewDNSProvider(),
		diskCache:    diskCache,
		acmeClient:   client,
		domains:      domains,
		certRequests: certRequests,
	}

	manager.warmManagedDomains()

	return manager
}

// GetCertificate implements the tls.Config.GetCertificate interface
func (w *WildcardCertManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello.ServerName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	// Canonicalize server name to lowercase.
	serverName := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")

	// Determine which certificate to use
	rootDomain := w.domainForServerName(serverName)
	if rootDomain == "" {
		// Not a domain we manage.
		return nil, fmt.Errorf("unrecognized domain: %q", hello.ServerName)
	}

	cert, err := w.ensureCertificateForDomain(rootDomain)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate for %s: %w", hello.ServerName, err)
	}

	return cert, nil
}

func (w *WildcardCertManager) ensureCertificateForDomain(rootDomain string) (*tls.Certificate, error) {
	cert := w.memCert(rootDomain)
	if w.isCertValid(rootDomain, cert) {
		return cert, nil
	}

	if diskCert, err := w.loadCertificateFromDisk(rootDomain); err == nil {
		if w.isCertValid(rootDomain, diskCert) {
			w.setMemCert(rootDomain, diskCert)
			return diskCert, nil
		}
	} else if !errors.Is(err, autocert.ErrCacheMiss) {
		slog.Warn("failed to load certificate from disk", "rootDomain", rootDomain, "error", err)
	}

	// Need to obtain a new certificate
	newCert, err := w.obtainCertificate(rootDomain)
	if err != nil {
		return nil, fmt.Errorf("failed to obtain certificate for %s: %w", rootDomain, err)
	}
	if !w.isCertValid(rootDomain, newCert) {
		return nil, fmt.Errorf("obtained invalid certificate for %s", rootDomain)
	}

	w.setMemCert(rootDomain, newCert)
	return newCert, nil
}

func (w *WildcardCertManager) warmManagedDomains() {
	for _, domain := range w.domains {
		go func() {
			if _, err := w.ensureCertificateForDomain(domain); err != nil {
				slog.Warn("failed to warm wildcard certificate cache", "domain", domain, "error", err)
			}
		}()
	}
}

func (w *WildcardCertManager) memCert(domain string) *tls.Certificate {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.memCerts[domain]
}

func (w *WildcardCertManager) setMemCert(domain string, cert *tls.Certificate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.memCerts[domain] = cert
}

// isSingleLevelSubdomain reports whether serverName is a single-level subdomain of domain.
// It assumes that both domain and serverName are lowercase.
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
// It assumes that serverName is lowercase.
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

func (w *WildcardCertManager) isCertValid(domain string, cert *tls.Certificate) bool {
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

	if time.Now().After(cert.Leaf.NotAfter) {
		return false
	}

	if time.Until(cert.Leaf.NotAfter) < certificateRenewBefore {
		w.triggerRenewal(domain)
	}

	return true
}

func (w *WildcardCertManager) triggerRenewal(domain string) {
	if _, loaded := w.renewalsInFlight.LoadOrStore(domain, struct{}{}); loaded {
		return
	}

	go func() {
		slog.Info("starting background certificate renewal", "domain", domain)
		defer func() { w.renewalsInFlight.Delete(domain) }()

		newCert, err := w.obtainCertificate(domain)
		if err != nil {
			slog.Warn("background renewal failed", "domain", domain, "error", err)
			return
		}

		if !w.isCertValid(domain, newCert) {
			slog.Warn("background renewal produced unusable certificate", "domain", domain)
			return
		}

		w.setMemCert(domain, newCert)
		// requestCertificateFromLE already writes to disk cache
		slog.Info("completed background certificate renewal", "domain", domain)
	}()
}

// obtainCertificate obtains a new wildcard certificate from Let's Encrypt.
// domain must be the root domain for the certificate, as obtained from domainForServerName.
// obtainCertificate is single-flighted.
func (w *WildcardCertManager) obtainCertificate(domain string) (*tls.Certificate, error) {
	cert, err, _ := w.sf.Do(domain, func() (*tls.Certificate, error) {
		return w.requestCertificateFromLE(domain)
	})
	if err != nil {
		return nil, err
	}
	return cert, nil
}

// requestCertificateFromLE implements obtainCertificate.
// Do not call it directly; use obtainCertificate instead.
func (w *WildcardCertManager) requestCertificateFromLE(domain string) (*tls.Certificate, error) {
	w.certRequests.Inc()

	domains := []string{domain, "*." + domain} // always request both root and wildcard
	slog.Info("obtaining certificate", "domains", domains)

	// Create a context with timeout for certificate acquisition
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create an ACME order for the desired domains
	order, err := w.acmeClient.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return nil, fmt.Errorf("failed to authorize order: %w", err)
	}

	type stagedChallenge struct {
		domain    string
		authzURL  string
		challenge *acme.Challenge
		keyAuth   string
		recordID  string
	}

	var staged []stagedChallenge
	cleanUpChallenges := func() {
		for _, c := range staged {
			w.dnsProvider.CleanupACMEChallenge(ctx, c.domain, c.keyAuth)
		}
	}

	// Stage every DNS-01 token before accepting any challenge so shared records
	// (e.g. example.com and *.example.com) remain valid for the entire order.
	for _, authzURL := range order.AuthzURLs {
		slog.Debug("fetching authorization", "url", authzURL)

		authorization, err := w.acmeClient.GetAuthorization(ctx, authzURL)
		if err != nil {
			cleanUpChallenges()
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
			cleanUpChallenges()
			return nil, fmt.Errorf("no DNS-01 challenge found")
		}
		slog.Debug("selected DNS-01 challenge", "challengeURI", challenge.URI)

		// Calculate key authorization
		keyAuth, err := w.acmeClient.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			cleanUpChallenges()
			return nil, fmt.Errorf("failed to calculate key authorization: %w", err)
		}

		slog.Info("creating DNS TXT record for ACME challenge", "domain", authorization.Identifier.Value)
		recordID, err := w.dnsProvider.CreateACMEChallenge(ctx, authorization.Identifier.Value, keyAuth)
		if err != nil {
			cleanUpChallenges()
			return nil, fmt.Errorf("failed to create ACME challenge: %w", err)
		}
		slog.Info("DNS TXT record created for ACME challenge", "recordID", recordID)

		staged = append(staged, stagedChallenge{
			domain:    authorization.Identifier.Value,
			authzURL:  authzURL,
			challenge: challenge,
			keyAuth:   keyAuth,
			recordID:  recordID,
		})
	}

	if len(staged) == 0 {
		return nil, fmt.Errorf("no challenges staged for order")
	}

	// Allow the staged TXT records to propagate before any challenge is validated.
	//
	// The AWS console Route53 banner says:
	// "Route 53 propagates your changes to all of the Route 53 authoritative DNS servers within 60 seconds."
	//
	// TODO: in theory we could do DNS lookups earlier, and in a retry loop to verify propagation.
	// This gets messy to Do Right, though: we have to avoid caching,
	// check every single authoritative nameserver, etc.
	//
	// In practice, a fixed sleep seems to work fine so far.
	// And except for the very first time, we'll be refreshing certs
	// in the background, so it's invisible anyway.
	time.Sleep(time.Minute)

	for _, c := range staged {
		// Accept the challenge
		_, err = w.acmeClient.Accept(ctx, c.challenge)
		if err != nil {
			// Clean up on error
			cleanUpChallenges()
			return nil, fmt.Errorf("failed to accept challenge: %w", err)
		}

		// Wait for authorization to complete
		_, err = w.acmeClient.WaitAuthorization(ctx, c.authzURL)
		if err != nil {
			// Clean up on error
			cleanUpChallenges()
			return nil, fmt.Errorf("authorization failed: %w", err)
		}

		// Log successful challenge
		slog.Info("completed DNS-01 challenge",
			"domain", c.domain,
			"recordID", c.recordID)
	}

	// Clean up challenges now that all authorizations have finished.
	cleanUpChallenges()

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

	if err := w.writeCertificateToDisk(domain, cert); err != nil && !errors.Is(err, autocert.ErrCacheMiss) {
		slog.Warn("failed to cache certificate", "domain", domain, "error", err)
	}

	return cert, nil
}

func (w *WildcardCertManager) writeCertificateToDisk(domain string, cert *tls.Certificate) error {
	if w.diskCache == nil || cert == nil {
		return autocert.ErrCacheMiss
	}

	data, err := encodeCertificate(cert)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), certificateCacheTimeout)
	defer cancel()
	return w.diskCache.Put(ctx, domain, data)
}

func (w *WildcardCertManager) loadCertificateFromDisk(domain string) (*tls.Certificate, error) {
	if w.diskCache == nil {
		return nil, autocert.ErrCacheMiss
	}

	ctx, cancel := context.WithTimeout(context.Background(), certificateCacheTimeout)
	defer cancel()

	data, err := w.diskCache.Get(ctx, domain)
	if err != nil {
		return nil, err
	}

	return decodeCertificate(data)
}

func encodeCertificate(cert *tls.Certificate) ([]byte, error) {
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

func decodeCertificate(data []byte) (*tls.Certificate, error) {
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
