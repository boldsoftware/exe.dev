package execore

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"exe.dev/publicips"
)

func TestHostPolicyAcceptsApexARecord(t *testing.T) {
	t.Parallel()

	s := &Server{
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
			return "knownhosts.exe.dev.", nil
		case "www.google.com":
			return "www.google.com.", nil
		default:
			return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
	}
	s.lookupAFunc = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "knownhosts.net", "knownhosts.exe.dev":
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
			Domain: "knownhosts.exe.dev",
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
		PublicIPs: map[netip.Addr]publicips.PublicIP{
			netip.MustParseAddr("10.0.0.5"): {
				IP:     netip.MustParseAddr("203.0.113.10"),
				Domain: "knownhosts.exe.dev",
			},
		},
	}

	s.lookupCNAMEFunc = func(_ context.Context, host string) (string, error) {
		switch host {
		case "knownhosts.net":
			return "knownhosts.net.", nil
		case "www.knownhosts.net":
			return "knownhosts.exe.dev.", nil
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
