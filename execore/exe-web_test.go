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

	ips, err := s.lookupAFunc(ctx, "knownhosts.exe.dev")
	if err != nil {
		t.Fatalf("lookupA(%q) error = %v, want nil", "knownhosts.exe.dev", err)
	}
	if len(ips) == 0 {
		t.Fatalf("lookupA(%q) returned no IPs", "knownhosts.exe.dev")
	}

	s.PublicIPs = map[netip.Addr]publicips.PublicIP{
		ips[0]: {
			IP:     ips[0],
			Domain: "knownhosts.exe.dev",
		},
	}

	if err := s.hostPolicy(ctx, "knownhosts.net"); err != nil {
		t.Fatalf("hostPolicy(%q) error = %v, want nil", "knownhosts.net", err)
	}
	if err := s.hostPolicy(ctx, "google.com"); err == nil {
		t.Fatalf("hostPolicy(%q) error = nil, want non-nil", "google.com")
	}
}
