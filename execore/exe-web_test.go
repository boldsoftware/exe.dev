package execore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeweb"
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

func TestBoldDevAllowedForTLSCert(t *testing.T) {
	t.Parallel()

	s := &Server{env: stage.Prod(), log: tslog.Slogger(t)}
	ctx := context.Background()

	if err := s.validateHostForTLSCert(ctx, "bold.dev"); err != nil {
		t.Fatalf("validateHostForTLSCert(%q) error = %v, want nil", "bold.dev", err)
	}
}

func TestResolveCustomDomainRejectsIPAddress(t *testing.T) {
	t.Parallel()

	s := &Server{env: stage.Prod(), log: tslog.Slogger(t)}

	// IP addresses should be rejected without any DNS lookups
	for _, ip := range []string{"35.95.182.1", "192.168.1.1", "::1", "2001:db8::1"} {
		_, err := s.resolveCustomDomainBoxName(context.Background(), ip)
		if err != exeweb.ErrHostIsIPAddress {
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

// TestResolveBoxNameApexDomainWithLobbyIP tests that apex domains can point to the
// lobby IP (the IP for ssh exe.dev), not just shard IPs. This is important because
// users often set their apex domain A record to point at exe.xyz (which resolves to
// the lobby IP), rather than a shard IP like s001.exe.xyz.
func TestResolveBoxNameApexDomainWithLobbyIP(t *testing.T) {
	t.Parallel()

	lobbyIP := netip.MustParseAddr("203.0.113.99") // The lobby IP (ssh exe.dev)
	shardIP := netip.MustParseAddr("203.0.113.10") // A shard IP (s001.exe.xyz)

	s := &Server{
		env:     stage.Prod(),
		LobbyIP: lobbyIP,
		PublicIPs: map[netip.Addr]publicips.PublicIP{
			netip.MustParseAddr("10.0.0.5"): {
				IP:     shardIP,
				Domain: "s001.exe.xyz",
				Shard:  1,
			},
		},
		log: tslog.Slogger(t),
	}

	s.lookupCNAMEFunc = func(_ context.Context, host string) (string, error) {
		switch host {
		case "example.com":
			// Apex domain - no CNAME, return itself
			return "example.com.", nil
		case "www.example.com":
			// www CNAME points to the box
			return "mybox.exe.xyz.", nil
		default:
			return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	s.lookupAFunc = func(_ context.Context, network, host string) ([]netip.Addr, error) {
		switch host {
		case "example.com":
			// Customer's apex domain points to the lobby IP (exe.xyz's IP)
			return []netip.Addr{lobbyIP}, nil
		default:
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}

	boxName, err := s.resolveBoxName(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("resolveBoxName(%q) error = %v, want nil", "example.com", err)
	}
	if boxName != "mybox" {
		t.Fatalf("resolveBoxName(%q) = %q, want %q", "example.com", boxName, "mybox")
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

	req := httptest.NewRequest(http.MethodGet, exeweb.SSHKnownHostsPath, nil)
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

	req := httptest.NewRequest(http.MethodGet, exeweb.SSHKnownHostsPath, nil)
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
	req := httptest.NewRequest(http.MethodGet, exeweb.SSHKnownHostsPath, nil)
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
			path:         exeweb.SSHKnownHostsPath,
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
			name:         "exe.new/moltbot redirects with prompt",
			host:         "exe.new",
			path:         "/moltbot",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new?prompt=" + url.QueryEscape(exeweb.ExeNewPathPrompts["/moltbot"]),
		},
		{
			name:         "exe.new/clawdbot redirects with prompt",
			host:         "exe.new",
			path:         "/clawdbot",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new?prompt=" + url.QueryEscape(exeweb.ExeNewPathPrompts["/clawdbot"]),
		},
		{
			name:         "exe.new/moltbot with invite passes through invite",
			host:         "exe.new",
			path:         "/moltbot?invite=TESTCODE",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new?prompt=" + url.QueryEscape(exeweb.ExeNewPathPrompts["/moltbot"]) + "&invite=TESTCODE",
		},
		{
			name:         "exe.new/clawdbot with invite passes through invite",
			host:         "exe.new",
			path:         "/clawdbot?invite=TESTCODE",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new?prompt=" + url.QueryEscape(exeweb.ExeNewPathPrompts["/clawdbot"]) + "&invite=TESTCODE",
		},
		{
			name:         "exe.new/openclaw redirects with prompt",
			host:         "exe.new",
			path:         "/openclaw",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new?prompt=" + url.QueryEscape(exeweb.ExeNewPathPrompts["/openclaw"]),
		},
		{
			name:         "exe.new/openclaw with invite passes through invite",
			host:         "exe.new",
			path:         "/openclaw?invite=TESTCODE",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new?prompt=" + url.QueryEscape(exeweb.ExeNewPathPrompts["/openclaw"]) + "&invite=TESTCODE",
		},
		{
			name:         "exe.new with invite but no prompt",
			host:         "exe.new",
			path:         "/?invite=TESTCODE",
			wantRedirect: true,
			wantLocation: "http://" + s.env.WebHost + "/new?invite=TESTCODE",
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

func TestBoldDevRedirectsToWebHost(t *testing.T) {
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
			name:         "bold.dev redirects to https WebHost",
			host:         "bold.dev",
			path:         "/",
			wantRedirect: true,
			wantLocation: "https://" + s.env.WebHost + "/",
		},
		{
			name:         "bold.dev preserves path",
			host:         "bold.dev",
			path:         "/foo/bar",
			wantRedirect: true,
			wantLocation: "https://" + s.env.WebHost + "/foo/bar",
		},
		{
			name:         "bold.dev with port redirects",
			host:         "bold.dev:443",
			path:         "/",
			wantRedirect: true,
			wantLocation: "https://" + s.env.WebHost + "/",
		},
		{
			name:         "bold.dev preserves query string",
			host:         "bold.dev",
			path:         "/new?foo=bar",
			wantRedirect: true,
			wantLocation: "https://" + s.env.WebHost + "/new?foo=bar",
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
			}
		})
	}
}

func TestCurlHomepageEasterEgg(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)

	tests := []struct {
		name       string
		userAgent  string
		wantScript bool
	}{
		{
			name:       "curl gets shell script",
			userAgent:  "curl/7.64.1",
			wantScript: true,
		},
		{
			name:       "curl with different version",
			userAgent:  "curl/8.0.0",
			wantScript: true,
		},
		{
			name:       "browser gets HTML",
			userAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
			wantScript: false,
		},
		{
			name:       "empty user agent gets HTML",
			userAgent:  "",
			wantScript: false,
		},
		{
			name:       "wget gets HTML",
			userAgent:  "Wget/1.21",
			wantScript: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = s.env.WebHost
			if tt.userAgent != "" {
				req.Header.Set("User-Agent", tt.userAgent)
			}
			rr := httptest.NewRecorder()

			s.ServeHTTP(rr, req)

			if tt.wantScript {
				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
				}
				body := rr.Body.String()
				if !strings.HasPrefix(body, "#!/bin/sh\n") {
					t.Errorf("expected shell script, got: %s", body)
				}
				if !strings.Contains(body, "ssh ") {
					t.Errorf("expected 'ssh' command in body, got: %s", body)
				}
				if !strings.Contains(body, "</dev/tty") {
					t.Errorf("expected '</dev/tty' redirect in body, got: %s", body)
				}
				if !strings.Contains(body, s.env.ReplHost) {
					t.Errorf("expected ReplHost %q in body, got: %s", s.env.ReplHost, body)
				}
				ct := rr.Header().Get("Content-Type")
				if ct != "text/x-shellscript" {
					t.Errorf("content-type = %q, want %q", ct, "text/x-shellscript")
				}
			} else {
				// For non-curl requests, we expect HTML (or a redirect for authenticated users)
				ct := rr.Header().Get("Content-Type")
				if strings.HasPrefix(ct, "text/x-shellscript") {
					t.Errorf("non-curl request got shell script content-type: %s", ct)
				}
			}
		})
	}
}
