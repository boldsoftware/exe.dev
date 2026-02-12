package wildcardcert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

func TestIsSingleLevelSubdomain(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		name       string
		domain     string
		serverName string
		want       bool
	}{
		{
			name:       "apex is not subdomain",
			domain:     "exe.dev",
			serverName: "exe.dev",
			want:       false,
		},
		{
			name:       "single level subdomain",
			domain:     "exe.dev",
			serverName: "api.exe.dev",
			want:       true,
		},
		{
			name:       "multi level subdomain rejected",
			domain:     "exe.dev",
			serverName: "box.team.exe.dev",
			want:       false,
		},
		{
			name:       "www single level subdomain",
			domain:     "exe.dev",
			serverName: "www.exe.dev",
			want:       true,
		},
		{
			name:       "subdomain of nested root",
			domain:     "xterm.exe.dev",
			serverName: "console.xterm.exe.dev",
			want:       true,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isSingleLevelSubdomain(tc.domain, tc.serverName)
			if got != tc.want {
				t.Fatalf("isSingleLevelSubdomain(%q, %q) = %v, want %v", tc.domain, tc.serverName, got, tc.want)
			}
		})
	}
}

func TestManager_domainForServerName(t *testing.T) {
	t.Parallel()

	tcs := []struct {
		name       string
		domains    []string
		serverName string
		want       string
	}{
		{
			name:       "apex domain match",
			domains:    []string{"exe.dev"},
			serverName: "exe.dev",
			want:       "exe.dev",
		},
		{
			name:       "single level subdomain collapses to root",
			domains:    []string{"exe.dev"},
			serverName: "api.exe.dev",
			want:       "exe.dev",
		},
		{
			name:       "multi level subdomain rejected",
			domains:    []string{"exe.dev"},
			serverName: "machine.team.exe.dev",
			want:       "",
		},
		{
			name:       "prefer exact match over parent root",
			domains:    []string{"exe.dev", "xterm.exe.dev"},
			serverName: "xterm.exe.dev",
			want:       "xterm.exe.dev",
		},
		{
			name:       "single level subdomain of nested root",
			domains:    []string{"exe.dev", "xterm.exe.dev"},
			serverName: "console.xterm.exe.dev",
			want:       "xterm.exe.dev",
		},
		{
			name:       "unknown domain",
			domains:    []string{"exe.dev"},
			serverName: "example.com",
			want:       "",
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &Manager{
				domains: tc.domains,
			}
			got := w.domainForServerName(tc.serverName)
			if got != tc.want {
				t.Fatalf("domainForServerName(%q) = %q, want %q", tc.serverName, got, tc.want)
			}
		})
	}
}

func TestManager_isCertValid(t *testing.T) {
	t.Parallel()

	const testDomain = "example.com"

	w := &Manager{}
	// Prevent background renewals from running because this test does not
	// configure the dependencies required by obtainCertificate.
	w.renewalsInFlight.Store(testDomain, struct{}{})
	now := time.Now()
	goodExpiry := now.Add(certificateRenewBefore + time.Hour)
	soonExpiry := now.Add(certificateRenewBefore / 2)
	expired := now.Add(-time.Hour)

	tcs := []struct {
		name       string
		cert       *tls.Certificate
		wantUsable bool
	}{
		{
			name:       "nil certificate",
			wantUsable: false,
		},
		{
			name:       "expired certificate",
			cert:       &tls.Certificate{Leaf: &x509.Certificate{NotAfter: expired}},
			wantUsable: false,
		},
		{
			name:       "valid certificate",
			cert:       &tls.Certificate{Leaf: &x509.Certificate{NotAfter: goodExpiry}},
			wantUsable: true,
		},
		{
			name:       "expiring soon still valid",
			cert:       &tls.Certificate{Leaf: &x509.Certificate{NotAfter: soonExpiry}},
			wantUsable: true,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := w.isCertValid(testDomain, tc.cert)
			if got != tc.wantUsable {
				t.Fatalf("isCertValid = %v, want %v", got, tc.wantUsable)
			}
		})
	}

	t.Run("parses leaf when missing", func(t *testing.T) {
		t.Parallel()
		der := mustCreateTestCertDER(t, goodExpiry)
		cert := &tls.Certificate{Certificate: [][]byte{der}}
		if !w.isCertValid(testDomain, cert) {
			t.Fatalf("isCertValid returned false for valid certificate")
		}
		if cert.Leaf == nil {
			t.Fatalf("isCertValid failed to populate leaf")
		}
	})
}

func mustCreateTestCertDER(t *testing.T, notAfter time.Time) []byte {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		Subject:               pkix.Name{CommonName: "example.com"},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"example.com"},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	return der
}
