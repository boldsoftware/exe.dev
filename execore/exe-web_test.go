package execore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/publicips"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"golang.org/x/crypto/ssh"
)

func TestHostPolicyAcceptsApexARecord(t *testing.T) {
	t.Parallel()

	s := &Server{
		env:       stage.Prod(),
		PublicIPs: map[netip.Addr]publicips.PublicIP{},
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
	s.lookupAFunc = func(_ context.Context, host string) ([]netip.Addr, error) {
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
	s.lookupAFunc = func(_ context.Context, host string) ([]netip.Addr, error) {
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

func TestKnownHostsLineFromStoredCert(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	ca := installTestHostCertificate(t, s)

	line, err := s.knownHostsLine(context.Background())
	if err != nil {
		t.Fatalf("knownHostsLine() error = %v", err)
	}

	expected := buildExpectedKnownHostsLine(s, ca)
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

	line, err := s.knownHostsLine(context.Background())
	if err != nil {
		t.Fatalf("knownHostsLine() error = %v", err)
	}

	if !strings.HasPrefix(line, "@cert-authority exe.dev,*.exe.dev ") {
		t.Fatalf("knownHostsLine() missing wildcard host prefix, got %q", line)
	}

	expected := buildExpectedKnownHostsLine(s, ca)
	if line != expected {
		t.Fatalf("knownHostsLine() = %q, want %q", line, expected)
	}
}

func TestHandleKnownHostsSuccess(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	ca := installTestHostCertificate(t, s)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/ssh/knownhosts", nil)
	rr := httptest.NewRecorder()

	s.handleKnownHosts(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("handleKnownHosts status = %d, want %d", rr.Code, http.StatusOK)
	}

	expectedLine := buildExpectedKnownHostsLine(s, ca)
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

func TestHandleKnownHostsMissingCert(t *testing.T) {
	t.Parallel()

	s := newUnstartedServer(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/ssh/knownhosts", nil)
	rr := httptest.NewRecorder()

	s.handleKnownHosts(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("handleKnownHosts status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func installTestHostCertificate(t *testing.T, s *Server) ssh.Signer {
	t.Helper()

	ctx := t.Context()
	row, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.GetSSHHostKeyRow, error) {
		return queries.GetSSHHostKey(ctx)
	})
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

	now := time.Now()
	cert := &ssh.Certificate{
		Key:             hostSigner.PublicKey(),
		Serial:          1,
		CertType:        ssh.HostCert,
		KeyId:           "test-host",
		ValidPrincipals: []string{s.env.ReplHost},
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

func buildExpectedKnownHostsLine(s *Server, ca ssh.Signer) string {
	host := s.env.ReplHost
	target := host
	if s.piperdPort != 22 {
		target = "[" + host + "]:" + strconv.Itoa(s.piperdPort)
	} else if host == "exe.dev" {
		target = "exe.dev,*.exe.dev"
	}
	caKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ca.PublicKey())))
	comment := host + " ssh ca"
	if fields := strings.Fields(caKey); len(fields) <= 2 {
		caKey = caKey + " " + comment
	}
	return "@cert-authority " + target + " " + caKey
}
