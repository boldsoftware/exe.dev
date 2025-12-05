package publicips

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

const testBoxDomain = "exe.dev"

func stubDomainLookup(t *testing.T, responses map[string][]netip.Addr) {
	t.Helper()

	orig := lookupDomainIPs
	origSkip := skipShardDistinctCheck
	skipShardDistinctCheck = true
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
		skipShardDistinctCheck = origSkip
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

	ips, err := EC2IPs(context.Background(), testBoxDomain)
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
		ShardSub(1) + "." + testBoxDomain: {publicOne},
		ShardSub(2) + "." + testBoxDomain: {publicTwo},
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

	ips, err := EC2IPs(context.Background(), testBoxDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	want := map[netip.Addr]PublicIP{
		privateOne: {
			IP:     publicOne,
			Domain: ShardSub(1) + "." + testBoxDomain,
			Shard:  1,
		},
		privateTwo: {
			IP:     publicTwo,
			Domain: ShardSub(2) + "." + testBoxDomain,
			Shard:  2,
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
		if got != info {
			t.Fatalf("mapping mismatch for %s: got %+v want %+v", priv, got, info)
		}
	}
}

func TestIPsIMDSv1Fallback(t *testing.T) {
	publicAddr := netip.MustParseAddr("198.51.100.42")
	privateAddr := netip.MustParseAddr("10.0.0.42")

	stubDomainLookup(t, map[string][]netip.Addr{
		ShardSub(3) + "." + testBoxDomain: {publicAddr},
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

	ips, err := EC2IPs(context.Background(), testBoxDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	want := PublicIP{
		IP:     publicAddr,
		Domain: ShardSub(3) + "." + testBoxDomain,
		Shard:  3,
	}
	got, ok := ips[privateAddr]
	if !ok {
		t.Fatalf("missing mapping for %s", privateAddr)
	}
	if got != want {
		t.Fatalf("mapping mismatch for %s: got %+v want %+v", privateAddr, got, want)
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

	ips, err := EC2IPs(context.Background(), testBoxDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	info, ok := ips[privateAddr]
	if !ok {
		t.Fatalf("missing mapping for %s", privateAddr)
	}
	want := PublicIP{
		IP:     publicAddr,
		Domain: testBoxDomain,
		Shard:  0, // apex domain, no shard
	}
	if info != want {
		t.Fatalf("mapping mismatch: got %+v want %+v", info, want)
	}
}

func TestIPsStagingDomain(t *testing.T) {
	const stagingDomain = "exe-staging.xyz"
	publicAddr := netip.MustParseAddr("198.51.100.77")
	privateAddr := netip.MustParseAddr("10.0.0.77")

	// Staging shards should use staging domain, not hardcoded exe.dev
	stubDomainLookup(t, map[string][]netip.Addr{
		ShardSub(5) + "." + stagingDomain: {publicAddr},
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

	ips, err := EC2IPs(context.Background(), stagingDomain)
	if err != nil {
		t.Fatalf("IPs returned error: %v", err)
	}

	info, ok := ips[privateAddr]
	if !ok {
		t.Fatalf("missing mapping for %s", privateAddr)
	}
	want := PublicIP{
		IP:     publicAddr,
		Domain: ShardSub(5) + "." + stagingDomain,
		Shard:  5,
	}
	if info != want {
		t.Fatalf("mapping mismatch: got %+v want %+v", info, want)
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

	_, err := EC2IPs(context.Background(), testBoxDomain)
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

func TestLocalhostIPs(t *testing.T) {
	ips, err := LocalhostIPs(context.Background(), "exe.cloud")
	if err != nil {
		t.Fatalf("LocalhostIPs failed: %v", err)
	}

	// Should have exactly MaxDomainShards entries
	if len(ips) != MaxDomainShards {
		t.Errorf("DevIPs returned %d entries, want %d", len(ips), MaxDomainShards)
	}

	// Check specific entries
	tests := []struct {
		ip      string
		wantIP  string
		wantDom string
		wantSh  int
	}{
		{"127.21.0.1", "127.21.0.1", "s001.exe.cloud", 1},
		{"127.21.0.7", "127.21.0.7", "s007.exe.cloud", 7},
		{"127.21.0.25", "127.21.0.25", "s025.exe.cloud", 25},
	}

	for _, tt := range tests {
		addr := netip.MustParseAddr(tt.ip)
		info, ok := ips[addr]
		if !ok {
			t.Errorf("DevIPs[%s] not found", tt.ip)
			continue
		}
		if info.IP.String() != tt.wantIP {
			t.Errorf("DevIPs[%s].IP = %s, want %s", tt.ip, info.IP, tt.wantIP)
		}
		if info.Domain != tt.wantDom {
			t.Errorf("DevIPs[%s].Domain = %s, want %s", tt.ip, info.Domain, tt.wantDom)
		}
		if info.Shard != tt.wantSh {
			t.Errorf("DevIPs[%s].Shard = %d, want %d", tt.ip, info.Shard, tt.wantSh)
		}
	}

	// Should not have invalid entries
	invalidIPs := []string{
		"127.21.0.0",  // shard 0 invalid
		"127.21.0.26", // shard > 25 invalid
		"127.0.0.1",   // not 127.21.0.X
		"10.0.0.1",    // private IP, not dev
	}
	for _, ip := range invalidIPs {
		addr := netip.MustParseAddr(ip)
		if _, ok := ips[addr]; ok {
			t.Errorf("DevIPs should not contain %s", ip)
		}
	}
}
