package publicips

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

const testBoxDomain = "exe.dev"

func stubDomainLookup(t *testing.T, responses map[string][]netip.Addr) {
	t.Helper()

	orig := lookupDomainIPs
	lookupDomainIPs = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip4" {
			t.Fatalf("unexpected network: %s", network)
		}
		if addrs, ok := responses[host]; ok {
			return addrs, nil
		}
		return []netip.Addr{}, nil
	}
	t.Cleanup(func() {
		lookupDomainIPs = orig
	})
}

func TestIPsNotOnAWS(t *testing.T) {
	stubDomainLookup(t, map[string][]netip.Addr{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	withMetadataServer(t, handler)

	ips, err := IPs(context.Background(), testBoxDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}
	if len(ips) != 0 {
		t.Fatalf("expected empty map outside AWS, got %v", ips)
	}
}

func TestIPsWithToken(t *testing.T) {
	publicOne := netip.MustParseAddr("198.51.100.10")
	publicTwo := netip.MustParseAddr("203.0.113.5")
	privateOne := netip.MustParseAddr("10.0.0.1")
	privateTwo := netip.MustParseAddr("10.0.0.2")

	stubDomainLookup(t, map[string][]netip.Addr{
		fmt.Sprintf(domainShardFormat, 1, testBoxDomain): {publicOne},
		fmt.Sprintf(domainShardFormat, 2, testBoxDomain): {publicTwo},
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("token"))
		case macsPath:
			expectHeader(t, r, headerIMDSToken, "token")
			_, _ = w.Write([]byte("aa:bb:cc:dd:ee:ff/\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/":
			expectHeader(t, r, headerIMDSToken, "token")
			_, _ = w.Write([]byte("198.51.100.10\n203.0.113.5\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/198.51.100.10":
			expectHeader(t, r, headerIMDSToken, "token")
			_, _ = w.Write([]byte("10.0.0.1"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/203.0.113.5":
			expectHeader(t, r, headerIMDSToken, "token")
			_, _ = w.Write([]byte("10.0.0.2"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	withMetadataServer(t, handler)

	ips, err := IPs(context.Background(), testBoxDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	want := map[netip.Addr]PublicIP{
		privateOne: {
			IP:     publicOne,
			Domain: fmt.Sprintf(domainShardFormat, 1, testBoxDomain),
		},
		privateTwo: {
			IP:     publicTwo,
			Domain: fmt.Sprintf(domainShardFormat, 2, testBoxDomain),
		},
	}
	if len(ips) != len(want) {
		t.Fatalf("unexpected number of entries: got %d want %d", len(ips), len(want))
	}
	for priv, info := range want {
		got, ok := ips[priv]
		if !ok {
			t.Fatalf("missing mapping for %s", priv)
		}
		if got.IP != info.IP || got.Domain != info.Domain {
			t.Fatalf("mapping mismatch for %s: got %+v want %+v", priv, got, info)
		}
	}
}

func TestIPsIMDSv1Fallback(t *testing.T) {
	publicAddr := netip.MustParseAddr("198.51.100.42")
	privateAddr := netip.MustParseAddr("10.0.0.42")

	stubDomainLookup(t, map[string][]netip.Addr{
		fmt.Sprintf(domainShardFormat, 3, testBoxDomain): {publicAddr},
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			http.NotFound(w, r)
		case macsPath:
			_, _ = w.Write([]byte("aa:bb:cc:dd:ee:ff/\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/":
			_, _ = w.Write([]byte("198.51.100.42\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/198.51.100.42":
			_, _ = w.Write([]byte("10.0.0.42"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	withMetadataServer(t, handler)

	ips, err := IPs(context.Background(), testBoxDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	want := map[netip.Addr]PublicIP{
		privateAddr: {
			IP:     publicAddr,
			Domain: fmt.Sprintf(domainShardFormat, 3, testBoxDomain),
		},
	}
	if len(ips) != len(want) {
		t.Fatalf("unexpected number of entries: got %d want %d", len(ips), len(want))
	}
	got, ok := ips[privateAddr]
	if !ok {
		t.Fatalf("missing mapping for %s", privateAddr)
	}
	if got.IP != want[privateAddr].IP || got.Domain != want[privateAddr].Domain {
		t.Fatalf("mapping mismatch for %s: got %+v want %+v", privateAddr, got, want[privateAddr])
	}
}

func TestIPsFallbackDomain(t *testing.T) {
	publicAddr := netip.MustParseAddr("203.0.113.155")
	privateAddr := netip.MustParseAddr("10.0.0.155")

	// IP not found in any shard, but found in the base boxDomain itself
	stubDomainLookup(t, map[string][]netip.Addr{
		testBoxDomain: {publicAddr},
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("token"))
		case macsPath:
			_, _ = w.Write([]byte("aa:bb:cc:dd:ee:ff/\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/":
			_, _ = w.Write([]byte("203.0.113.155\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/203.0.113.155":
			_, _ = w.Write([]byte("10.0.0.155"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	withMetadataServer(t, handler)

	ips, err := IPs(context.Background(), testBoxDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	info, ok := ips[privateAddr]
	if !ok {
		t.Fatalf("missing mapping for %s", privateAddr)
	}
	if info.IP != publicAddr {
		t.Fatalf("unexpected public IP: got %s want %s", info.IP, publicAddr)
	}
	if info.Domain != testBoxDomain {
		t.Fatalf("unexpected domain: got %q want %q", info.Domain, testBoxDomain)
	}
}

func TestIPsStagingDomain(t *testing.T) {
	const stagingDomain = "exe-staging.xyz"
	publicAddr := netip.MustParseAddr("198.51.100.77")
	privateAddr := netip.MustParseAddr("10.0.0.77")

	// Staging shards should use staging domain, not hardcoded exe.dev
	stubDomainLookup(t, map[string][]netip.Addr{
		fmt.Sprintf(domainShardFormat, 5, stagingDomain): {publicAddr},
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("token"))
		case macsPath:
			expectHeader(t, r, headerIMDSToken, "token")
			_, _ = w.Write([]byte("aa:bb:cc:dd:ee:ff/\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/":
			expectHeader(t, r, headerIMDSToken, "token")
			_, _ = w.Write([]byte("198.51.100.77\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/198.51.100.77":
			expectHeader(t, r, headerIMDSToken, "token")
			_, _ = w.Write([]byte("10.0.0.77"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	withMetadataServer(t, handler)

	ips, err := IPs(context.Background(), stagingDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	info, ok := ips[privateAddr]
	if !ok {
		t.Fatalf("missing mapping for %s", privateAddr)
	}
	if info.IP != publicAddr {
		t.Fatalf("unexpected public IP: got %s want %s", info.IP, publicAddr)
	}
	wantDomain := fmt.Sprintf(domainShardFormat, 5, stagingDomain)
	if info.Domain != wantDomain {
		t.Fatalf("unexpected domain: got %q want %q", info.Domain, wantDomain)
	}
}

func TestIPsMissingPrivateAddress(t *testing.T) {
	stubDomainLookup(t, map[string][]netip.Addr{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("token"))
		case macsPath:
			_, _ = w.Write([]byte("aa:bb:cc:dd:ee:ff/\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/":
			_, _ = w.Write([]byte("198.51.100.10\n"))
		case macsPath + "aa:bb:cc:dd:ee:ff/ipv4-associations/198.51.100.10":
			http.NotFound(w, r)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	withMetadataServer(t, handler)

	_, err := IPs(context.Background(), testBoxDomain)
	if err == nil {
		t.Fatalf("expected error when private IP missing")
	}
}

func withMetadataServer(t *testing.T, handler http.Handler) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	origEndpoint := metadataEndpoint
	origClient := newHTTPClient

	metadataEndpoint = server.URL
	newHTTPClient = func() *http.Client {
		return server.Client()
	}

	t.Cleanup(func() {
		metadataEndpoint = origEndpoint
		newHTTPClient = origClient
	})
}

func expectHeader(t *testing.T, r *http.Request, key, want string) {
	t.Helper()
	if got := r.Header.Get(key); got != want {
		t.Fatalf("header %s = %q, want %q", key, got, want)
	}
}
