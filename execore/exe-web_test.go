package execore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/publicips"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"exe.dev/tslog"
	"golang.org/x/crypto/ssh"
)

func TestHostPolicyAcceptsApexARecord(t *testing.T) {
	t.Parallel()

	s := &Server{
		env:       stage.Prod(),
		PublicIPs: map[netip.Addr]publicips.PublicIP{},
		log:       tslog.Slogger(t),
	}
	ctx := context.Background()

	knownHostIP := netip.MustParseAddr("203.0.113.10")
	googleIP := netip.MustParseAddr("8.8.8.8")

	s.lookupCNAMEFunc = func(_ context.Context, host string) (string, error) {
		switch host {
		case "knownhosts.net", "google.com":
			return host + ".", nil
		case "www.knownhosts.net":
			return "knownhosts.exe.xyz.", nil
		case "www.google.com":
			return "www.google.com.", nil
		default:
			return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	s.lookupAFunc = func(_ context.Context, network, host string) ([]netip.Addr, error) {
		switch host {
		case "knownhosts.net", "knownhosts.exe.xyz":
			return []netip.Addr{knownHostIP}, nil
		case "google.com":
			return []netip.Addr{googleIP}, nil
		default:
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	s.boxExistsFunc = func(_ context.Context, name string) bool {
		return name == "knownhosts"
	}

	s.PublicIPs = map[netip.Addr]publicips.PublicIP{
		netip.MustParseAddr("10.0.0.5"): {
			IP:     knownHostIP,
			Domain: "knownhosts.exe.xyz",
		},
	}

	if err := s.validateHostForTLSCert(ctx, "knownhosts.net"); err != nil {
		t.Fatalf("hostPolicy(%q) error = %v, want nil", "knownhosts.net", err)
	}
	if err := s.validateHostForTLSCert(ctx, "google.com"); err == nil {
		t.Fatalf("hostPolicy(%q) error = nil, want non-nil", "google.com")
	}
}

func TestResolveCustomDomainRejectsIPAddress(t *testing.T) {
	t.Parallel()

	s := &Server{env: stage.Prod(), log: tslog.Slogger(t)}

	// IP addresses should be rejected without any DNS lookups
	for _, ip := range []string{"35.95.182.1", "192.168.1.1", "::1", "2001:db8::1"} {
		_, err := s.resolveCustomDomainBoxName(context.Background(), ip)
		if err != errHostIsIPAddress {
			t.Errorf("resolveCustomDomainBoxName(%q) = %v, want errHostIsIPAddress", ip, err)
		}
	}
}

func TestResolveBoxNameApexDomain(t *testing.T) {
	t.Parallel()

	s := &Server{
		env: stage.Prod(),
		PublicIPs: map[netip.Addr]publicips.PublicIP{
			netip.MustParseAddr("10.0.0.5"): {
				IP:     netip.MustParseAddr("203.0.113.10"),
				Domain: "knownhosts.exe.xyz",
			},
		},
		log: tslog.Slogger(t),
	}

	s.lookupCNAMEFunc = func(_ context.Context, host string) (string, error) {
		switch host {
		case "knownhosts.net":
			return "knownhosts.net.", nil
		case "www.knownhosts.net":
			return "knownhosts.exe.xyz.", nil
		default:
			return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	s.lookupAFunc = func(_ context.Context, network, host string) ([]netip.Addr, error) {
		switch host {
		case "knownhosts.net":
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		default:
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}

	boxName, err := s.resolveBoxName(context.Background(), "knownhosts.net")
	if err != nil {
		t.Fatalf("resolveBoxName(%q) error = %v, want nil", "knownhosts.net", err)
	}
	if boxName != "knownhosts" {
		t.Fatalf("resolveBoxName(%q) = %q, want %q", "knownhosts.net", boxName, "knownhosts")
	}
}

func TestResolveBoxNameShelleySubdomain(t *testing.T) {
	t.Parallel()

	s := &Server{
		env: stage.Test(), // BoxHost is exe.cloud
		log: tslog.Slogger(t),
	}

	tests := []struct {
		hostname string
		wantBox  string
		wantErr  bool
	}{
		{"mybox.shelley.exe.cloud", "mybox", false},
		{"galaxy-uncle.shelley.exe.cloud", "galaxy-uncle", false},
		{"mybox.exe.cloud", "mybox", false},
		{"mybox.xterm.exe.cloud", "", true}, // xterm is not handled by resolveBoxName
	}

	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			boxName, err := s.resolveBoxName(context.Background(), tt.hostname)
			if tt.wantErr {
				if err == nil && boxName != "" {
					t.Errorf("resolveBoxName(%q) = %q, want error or empty", tt.hostname, boxName)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBoxName(%q) error = %v, want nil", tt.hostname, err)
			}
			if boxName != tt.wantBox {
				t.Errorf("resolveBoxName(%q) = %q, want %q", tt.hostname, boxName, tt.wantBox)
			}
		})
	}
}

func TestKnownHostsLineFromStoredCert(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	ca := installTestHostCertificate(t, s)

	line, err := s.knownHostsLine(context.Background(), s.env.ReplHost)
	if err != nil {
		t.Fatalf("knownHostsLine() error = %v", err)
	}

	expected := buildExpectedKnownHostsLine(t, s, ca, s.env.ReplHost)
	if line != expected {
		t.Fatalf("knownHostsLine() = %q, want %q", line, expected)
	}
}

func TestKnownHostsLineAddsWildcardForExeDev(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env = stage.Prod()
	s.piperdPort = 22

	ca := installTestHostCertificate(t, s)

	line, err := s.knownHostsLine(context.Background(), s.env.ReplHost)
	if err != nil {
		t.Fatalf("knownHostsLine() error = %v", err)
	}

	if !strings.HasPrefix(line, "@cert-authority exe.dev,*.exe.dev ") {
		t.Fatalf("knownHostsLine() missing wildcard host prefix, got %q", line)
	}

	expected := buildExpectedKnownHostsLine(t, s, ca, s.env.ReplHost)
	if line != expected {
		t.Fatalf("knownHostsLine() = %q, want %q", line, expected)
	}
}

func TestKnownHostsLineAddsWildcardForBoxHost(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env = stage.Prod()
	s.piperdPort = 22

	ca := installTestHostCertificate(t, s)

	line, err := s.knownHostsLine(context.Background(), s.env.BoxHost)
	if err != nil {
		t.Fatalf("knownHostsLine() error = %v", err)
	}

	if !strings.HasPrefix(line, "@cert-authority exe.xyz,*.exe.xyz ") {
		t.Fatalf("knownHostsLine() missing box wildcard host prefix, got %q", line)
	}

	expected := buildExpectedKnownHostsLine(t, s, ca, s.env.BoxHost)
	if line != expected {
		t.Fatalf("knownHostsLine() = %q, want %q", line, expected)
	}
}

func TestHandleKnownHostsSuccess(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	ca := installTestHostCertificate(t, s)

	req := httptest.NewRequest(http.MethodGet, sshKnownHostsPath, nil)
	req.Host = s.env.ReplHost
	rr := httptest.NewRecorder()

	s.handleKnownHosts(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("handleKnownHosts status = %d, want %d", rr.Code, http.StatusOK)
	}

	expectedLine := buildExpectedKnownHostsLine(t, s, ca, s.env.ReplHost)
	if body := strings.TrimSpace(rr.Body.String()); body != expectedLine {
		t.Fatalf("handleKnownHosts body = %q, want %q", body, expectedLine)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("handleKnownHosts content type = %q, want %q", ct, "text/plain; charset=utf-8")
	}
	if cache := rr.Header().Get("Cache-Control"); cache != "public, max-age=300" {
		t.Fatalf("handleKnownHosts cache header = %q, want %q", cache, "public, max-age=300")
	}
}

func TestHandleKnownHostsBoxHost(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	s.env = stage.Prod()
	s.piperdPort = 22
	ca := installTestHostCertificate(t, s)

	req := httptest.NewRequest(http.MethodGet, sshKnownHostsPath, nil)
	req.Host = s.env.BoxHost
	rr := httptest.NewRecorder()

	s.handleKnownHosts(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("handleKnownHosts status = %d, want %d", rr.Code, http.StatusOK)
	}

	expectedLine := buildExpectedKnownHostsLine(t, s, ca, s.env.BoxHost)
	if body := strings.TrimSpace(rr.Body.String()); body != expectedLine {
		t.Fatalf("handleKnownHosts body = %q, want %q", body, expectedLine)
	}
}

func TestHandleKnownHostsMissingCert(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	req := httptest.NewRequest(http.MethodGet, sshKnownHostsPath, nil)
	req.Host = s.env.ReplHost
	rr := httptest.NewRecorder()

	s.handleKnownHosts(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("handleKnownHosts status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func installTestHostCertificate(t *testing.T, s *Server) ssh.Signer {
	t.Helper()

	ctx := t.Context()
	row, err := withRxRes0(s, ctx, (*exedb.Queries).GetSSHHostKey)
	if err != nil {
		t.Fatalf("failed to load host key: %v", err)
	}

	hostSigner, err := ssh.ParsePrivateKey([]byte(row.PrivateKey))
	if err != nil {
		t.Fatalf("failed to parse host private key: %v", err)
	}

	_, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate CA key: %v", err)
	}
	caSigner, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatalf("failed to build CA signer: %v", err)
	}

	validPrincipals := []string{s.env.ReplHost}
	if boxHost := strings.TrimSpace(s.env.BoxHost); boxHost != "" && boxHost != s.env.ReplHost {
		validPrincipals = append(validPrincipals, boxHost)
	}

	now := time.Now()
	cert := &ssh.Certificate{
		Key:             hostSigner.PublicKey(),
		Serial:          1,
		CertType:        ssh.HostCert,
		KeyId:           "test-host",
		ValidPrincipals: validPrincipals,
		ValidAfter:      uint64(now.Add(-time.Hour).Unix()),
		ValidBefore:     uint64(now.Add(time.Hour).Unix()),
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("failed to sign host certificate: %v", err)
	}

	certData := string(ssh.MarshalAuthorizedKey(cert))
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE ssh_host_key SET cert_sig = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`, certData)
		return err
	})
	if err != nil {
		t.Fatalf("failed to store host certificate: %v", err)
	}

	return caSigner
}

func buildExpectedKnownHostsLine(t testing.TB, s *Server, ca ssh.Signer, host string) string {
	t.Helper()

	target, err := s.knownHostsTarget(host)
	if err != nil {
		t.Fatalf("failed to build known hosts target for %s: %v", host, err)
	}
	caKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ca.PublicKey())))
	comment := host + " ssh ca"
	if fields := strings.Fields(caKey); len(fields) <= 2 {
		caKey = caKey + " " + comment
	}
	return "@cert-authority " + target + " " + caKey
}

// TestBoxHostApexRedirectsToWebHost tests that requests to the BoxHost apex (exe.xyz)
// are redirected to WebHost (exe.dev) to avoid passkey RPID mismatch errors.
func TestBoxHostApexRedirectsToWebHost(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	// In test env, BoxHost="exe.cloud" and WebHost="localhost"
	// These are different, so the redirect should happen.
	if s.env.BoxHost == s.env.WebHost {
		t.Skip("BoxHost and WebHost are the same in this stage, skip")
	}

	tests := []struct {
		name         string
		host         string
		path         string
		wantRedirect bool
		wantLocation string
	}{
		{
			name:         "apex BoxHost redirects",
			host:         s.env.BoxHost,
			path:         "/",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/",
		},
		{
			name:         "apex BoxHost with path redirects",
			host:         s.env.BoxHost,
			path:         "/auth?redirect=/foo",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/auth?redirect=/foo",
		},
		{
			name:         "apex BoxHost with port redirects",
			host:         s.env.BoxHost + ":443",
			path:         "/",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/",
		},
		{
			name:         "known hosts on BoxHost apex does not redirect",
			host:         s.env.BoxHost,
			path:         sshKnownHostsPath,
			wantRedirect: false,
		},
		{
			name:         "subdomain of BoxHost does not redirect",
			host:         "mybox." + s.env.BoxHost,
			path:         "/",
			wantRedirect: false,
		},
		{
			name:         "WebHost does not redirect",
			host:         s.env.WebHost,
			path:         "/",
			wantRedirect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Host = tt.host
			rr := httptest.NewRecorder()

			s.ServeHTTP(rr, req)

			if tt.wantRedirect {
				if rr.Code != http.StatusTemporaryRedirect {
					t.Errorf("status = %d, want %d", rr.Code, http.StatusTemporaryRedirect)
				}
				if loc := rr.Header().Get("Location"); loc != tt.wantLocation {
					t.Errorf("Location = %q, want %q", loc, tt.wantLocation)
				}
			} else {
				if rr.Code == http.StatusTemporaryRedirect {
					t.Errorf("unexpected redirect to %s", rr.Header().Get("Location"))
				}
			}
		})
	}
}

func TestExeNewRedirectsToWebHostNew(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	tests := []struct {
		name         string
		host         string
		path         string
		wantRedirect bool
		wantLocation string
	}{
		{
			name:         "exe.new redirects to /new",
			host:         "exe.new",
			path:         "/",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new",
		},
		{
			name:         "exe.new with path still redirects to /new",
			host:         "exe.new",
			path:         "/foo",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new",
		},
		{
			name:         "exe.new with port redirects",
			host:         "exe.new:443",
			path:         "/",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new",
		},
		{
			name:         "WebHost does not redirect",
			host:         s.env.WebHost,
			path:         "/new",
			wantRedirect: false,
		},
		{
			name:         "other domain does not redirect",
			host:         "other.test",
			path:         "/",
			wantRedirect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Host = tt.host
			rr := httptest.NewRecorder()

			s.ServeHTTP(rr, req)

			if tt.wantRedirect {
				if rr.Code != http.StatusTemporaryRedirect {
					t.Errorf("status = %d, want %d", rr.Code, http.StatusTemporaryRedirect)
				}
				if loc := rr.Header().Get("Location"); loc != tt.wantLocation {
					t.Errorf("Location = %q, want %q", loc, tt.wantLocation)
				}
			} else {
				if rr.Code == http.StatusTemporaryRedirect && rr.Header().Get("Location") == "http://"+s.env.WebHost+"/new" {
					t.Errorf("unexpected redirect to %s", rr.Header().Get("Location"))
				}
			}
		})
	}
}
