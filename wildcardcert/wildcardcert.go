// Package wildcardcert manages wildcard TLS certificates using DNS-01 challenges.
// It uses the exens DNS server for ACME challenge TXT records.
package wildcardcert

import (
	"bytes"
	"context"
	"crypto"
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

	"exe.dev/domz"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"tailscale.com/util/singleflight"
)

// ErrUnrecognizedDomain is returned when GetCertificate is called with a domain
// that is not managed by this Manager.
var ErrUnrecognizedDomain = errors.New("unrecognized domain")

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
			slog.InfoContext(ctx, "loaded cached ACME account key")
			return key, nil
		}
		slog.WarnContext(ctx, "failed to parse cached ACME account key, generating new one")
	} else {
		slog.InfoContext(ctx, "no cached ACME account key found, generating new one")
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
		slog.WarnContext(ctx, "failed to cache ACME account key", "error", err)
	} else {
		slog.InfoContext(ctx, "generated and cached new ACME account key")
	}

	return key, nil
}

const (
	certificateCacheTimeout = 5 * time.Second
	certificateRenewBefore  = 10 * 24 * time.Hour
)

// DNSProvider is the interface for managing ACME DNS-01 challenge TXT records.
type DNSProvider interface {
	// SetTXTRecord sets an in-memory TXT record for ACME challenges.
	SetTXTRecord(name, value string)
	// DeleteTXTRecord removes an in-memory TXT record.
	DeleteTXTRecord(name, value string)
}

// Manager manages wildcard certificates using DNS-01 challenges.
type Manager struct {
	dnsProvider  DNSProvider
	diskCache    autocert.Cache // persistent cache for certificates
	acmeClient   *acme.Client
	domains      []string // list of domains managed by this cert manager; each entry covers itself and a wildcard cert
	certRequests prometheus.Counter
	sf           singleflight.Group[string, *tls.Certificate] // singleflight group for certificate requests

	renewalsInFlight sync.Map // background renewals currently running per domain

	mu       sync.Mutex                  // protects certificates
	memCerts map[string]*tls.Certificate // in-memory cache of certificates to avoid repeated decoding and disk reads
}

// NewManager creates a new wildcard certificate manager.
func NewManager(domains []string, diskCache autocert.Cache, certRequests prometheus.Counter, dnsProvider DNSProvider) *Manager {
	if len(domains) == 0 {
		panic("NewManager: no domains provided")
	}
	if diskCache == nil {
		panic("NewManager: nil diskCache")
	}
	if certRequests == nil {
		panic("NewManager: nil certRequests counter")
	}
	if dnsProvider == nil {
		panic("NewManager: nil dnsProvider")
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

	// Register or retrieve the ACME account
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	acct := &acme.Account{
		Contact: []string{"mailto:admin@exe.dev"},
	}
	_, err = client.Register(ctx, acct, acme.AcceptTOS)
	if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		panic(fmt.Sprintf("failed to register ACME account: %v", err))
	}

	manager := &Manager{
		memCerts:     make(map[string]*tls.Certificate),
		dnsProvider:  dnsProvider,
		diskCache:    diskCache,
		acmeClient:   client,
		domains:      domains,
		certRequests: certRequests,
	}

	manager.warmManagedDomains()

	return manager
}

// GetCertificate returns a [tls.Certificate] for a server.
func (w *Manager) GetCertificate(serverName string) (*tls.Certificate, error) {
	if serverName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	serverName = domz.Canonicalize(serverName)

	// Determine which certificate to use
	rootDomain := w.domainForServerName(serverName)
	if rootDomain == "" {
		// Not a domain we manage.
		return nil, fmt.Errorf("%w: %q", ErrUnrecognizedDomain, serverName)
	}

	cert, err := w.ensureCertificateForDomain(rootDomain)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate for %s: %w", serverName, err)
	}

	return cert, nil
}

func (w *Manager) ensureCertificateForDomain(rootDomain string) (*tls.Certificate, error) {
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

func (w *Manager) warmManagedDomains() {
	for _, domain := range w.domains {
		go func() {
			if _, err := w.ensureCertificateForDomain(domain); err != nil {
				slog.Warn("failed to warm wildcard certificate cache", "domain", domain, "error", err)
			}
		}()
	}
}

func (w *Manager) memCert(domain string) *tls.Certificate {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.memCerts[domain]
}

func (w *Manager) setMemCert(domain string, cert *tls.Certificate) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.memCerts[domain] = cert
}

// isSingleLevelSubdomain reports whether serverName is a single-level subdomain of domain.
// It assumes that both domain and serverName are canonicalized.
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
// It assumes that serverName is canonicalized.
func (w *Manager) domainForServerName(serverName string) string {
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

func (w *Manager) isCertValid(domain string, cert *tls.Certificate) bool {
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

func (w *Manager) triggerRenewal(domain string) {
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
func (w *Manager) obtainCertificate(domain string) (*tls.Certificate, error) {
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
func (w *Manager) requestCertificateFromLE(domain string) (*tls.Certificate, error) {
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
	}

	var staged []stagedChallenge
	cleanUpChallenges := func() {
		for _, c := range staged {
			acmeName := acmeChallengeName(c.domain)
			baseDomain := extractDomain(c.domain)
			fullName := acmeName + "." + baseDomain
			w.dnsProvider.DeleteTXTRecord(fullName, c.keyAuth)
		}
	}

	// Stage every DNS-01 token before accepting any challenge so shared records
	// (e.g. example.com and *.example.com) remain valid for the entire order.
	for _, authzURL := range order.AuthzURLs {
		slog.DebugContext(ctx, "fetching authorization", "url", authzURL)

		authorization, err := w.acmeClient.GetAuthorization(ctx, authzURL)
		if err != nil {
			cleanUpChallenges()
			return nil, fmt.Errorf("failed to get authorization: %w", err)
		}

		// Find DNS-01 challenge
		slog.DebugContext(ctx, "looking for DNS-01 challenge", "challengeCount", len(authorization.Challenges))
		var challenge *acme.Challenge
		for _, c := range authorization.Challenges {
			slog.DebugContext(ctx, "found challenge type", "type", c.Type)
			if c.Type == "dns-01" {
				challenge = c
				break
			}
		}

		if challenge == nil {
			cleanUpChallenges()
			return nil, fmt.Errorf("no DNS-01 challenge found")
		}
		slog.DebugContext(ctx, "selected DNS-01 challenge", "challengeURI", challenge.URI)

		// Calculate key authorization
		keyAuth, err := w.acmeClient.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			cleanUpChallenges()
			return nil, fmt.Errorf("failed to calculate key authorization: %w", err)
		}

		// Create TXT record for ACME challenge
		acmeName := acmeChallengeName(authorization.Identifier.Value)
		baseDomain := extractDomain(authorization.Identifier.Value)
		fullName := acmeName + "." + baseDomain
		slog.InfoContext(ctx, "creating DNS TXT record for ACME challenge", "domain", authorization.Identifier.Value, "name", fullName)
		w.dnsProvider.SetTXTRecord(fullName, keyAuth)

		staged = append(staged, stagedChallenge{
			domain:    authorization.Identifier.Value,
			authzURL:  authzURL,
			challenge: challenge,
			keyAuth:   keyAuth,
		})
	}

	if len(staged) == 0 {
		return nil, fmt.Errorf("no challenges staged for order")
	}

	// Allow the staged TXT records to propagate.
	// Since we're using our own DNS server, propagation should be instant,
	// but we give a brief moment for any caching to settle.
	time.Sleep(5 * time.Second)

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
		slog.InfoContext(ctx, "completed DNS-01 challenge", "domain", c.domain)
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
			slog.WarnContext(ctx, "failed to parse leaf certificate", "error", err)
		} else {
			cert.Leaf = leaf
		}
	}

	if err := w.writeCertificateToDisk(domain, cert); err != nil && !errors.Is(err, autocert.ErrCacheMiss) {
		slog.WarnContext(ctx, "failed to cache certificate", "domain", domain, "error", err)
	}

	return cert, nil
}

func (w *Manager) writeCertificateToDisk(domain string, cert *tls.Certificate) error {
	if w.diskCache == nil || cert == nil {
		return autocert.ErrCacheMiss
	}

	data, err := EncodeCertificate(cert)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), certificateCacheTimeout)
	defer cancel()
	return w.diskCache.Put(ctx, domain, data)
}

func (w *Manager) loadCertificateFromDisk(domain string) (*tls.Certificate, error) {
	if w.diskCache == nil {
		return nil, autocert.ErrCacheMiss
	}

	ctx, cancel := context.WithTimeout(context.Background(), certificateCacheTimeout)
	defer cancel()

	data, err := w.diskCache.Get(ctx, domain)
	if err != nil {
		return nil, err
	}

	return DecodeCertificate(data)
}

// EncodeCertificate encodes a wildcard certificate as a []byte.
func EncodeCertificate(cert *tls.Certificate) ([]byte, error) {
	if cert == nil {
		return nil, fmt.Errorf("certificate is nil")
	}

	var buf bytes.Buffer
	switch key := cert.PrivateKey.(type) {
	case *rsa.PrivateKey:
		keyBytes := x509.MarshalPKCS1PrivateKey(key)

		if err := pem.Encode(&buf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes}); err != nil {
			return nil, err
		}

	default:
		// We could use this for rsa.PrivateKey too,
		// if we're OK with storing different data.
		keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}

		if err := pem.Encode(&buf, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}); err != nil {
			return nil, err
		}
	}

	for _, der := range cert.Certificate {
		if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// DecodeCertificate undoes [EncodeCertificate].
func DecodeCertificate(data []byte) (*tls.Certificate, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("certificate cache data is empty")
	}

	var (
		certs [][]byte
		key   crypto.PrivateKey
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
				return nil, fmt.Errorf("failed to parse cached RSA private key: %w", err)
			}
			key = parsedKey
		case "EC PRIVATE KEY":
			parsedKey, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse cached EC private key: %w", err)
			}
			key = parsedKey
		case "PRIVATE KEY":
			// PKCS#8 format - can contain RSA, EC, or other key types
			parsedKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("failed to parse cached PKCS8 private key: %w", err)
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

// extractDomain extracts the base domain from a FQDN
// For wildcard certificates, we need to extract the domain that's registered
func extractDomain(fqdn string) string {
	parts := strings.Split(fqdn, ".")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], ".")
	}
	return fqdn
}

// acmeChallengeName returns the TXT record prefix for a domain (wildcard or otherwise).
func acmeChallengeName(domain string) string {
	baseDomain := extractDomain(domain)
	target := strings.TrimPrefix(domain, "*.")
	if target == baseDomain {
		// "*.exe.dev" -> "_acme-challenge"
		return "_acme-challenge"
	}

	sub, ok := strings.CutSuffix(target, "."+baseDomain)
	if ok {
		// "*.sub.exe.dev" -> "_acme-challenge.sub"
		return "_acme-challenge." + sub
	}

	// "exe.dev" -> "_acme-challenge"
	return "_acme-challenge"
}
