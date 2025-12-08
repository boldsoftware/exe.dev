package publicips

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const (
	metadataEndpointDefault = "http://169.254.169.254"
	tokenPath               = "/latest/api/token"
	macsPath                = "/latest/meta-data/network/interfaces/macs/"
	tokenTTLSeconds         = "21600"
	httpRequestTimeout      = 500 * time.Millisecond
	connectTimeout          = 200 * time.Millisecond
	headerIMDSToken         = "X-aws-ec2-metadata-token"
	headerIMDSTTL           = "X-aws-ec2-metadata-token-ttl-seconds"
)

const (
	// MaxDomainShards is the largest available shard ID.
	// Shards map to IP public IP addresses: sNNN.exe.dev, so ranging from s001.exe.dev to s025.exe.dev.
	// MaxDomainShards must match the DB CHECK constraint.
	// IP shards are 1-based. (The zero value is intentionally invalid.)
	MaxDomainShards = 25
)

// ShardIsValid reports whether shard is within the valid range (0, MaxDomainShards].
func ShardIsValid(shard int) bool {
	return shard >= 1 && shard <= MaxDomainShards
}

var (
	metadataEndpoint = metadataEndpointDefault
	newHTTPClient    = defaultHTTPClient

	lookupDomainIPs = defaultLookupDomainIPs

	// skipShardDistinctCheck allows tests to bypass the check that all shards resolve
	// to distinct IPs. In production, this validation ensures DNS is configured correctly.
	skipShardDistinctCheck = false

	errMetadataUnavailable = errors.New("metadata service unavailable")
	errIMDSv1Only          = errors.New("metadata service requires IMDSv1")
)

func ShardSub(shard int) string {
	return fmt.Sprintf("s%03d", shard)
}

// PublicIP describes a public IPv4 address, associated domain name, and shard.
type PublicIP struct {
	IP     netip.Addr
	Domain string // full domain, e.g. "s007.exe.cloud"
	Shard  int    // shard number, e.g. 7
}

type ipMapping struct {
	public  netip.Addr
	private netip.Addr
}

// EC2IPs returns a mapping of private IPv4 addresses to their associated public IPv4
// metadata on the current EC2 instance. boxDomain specifies the base domain for
// shard records (e.g. exe.dev or exe-staging.dev). If the process is not running
// on EC2, it returns an empty map and a nil error.
func EC2IPs(ctx context.Context, boxDomain string) (map[netip.Addr]PublicIP, error) {
	if ctx == nil {
		return nil, errors.New("publicips: context must not be nil")
	}
	boxDomain = strings.TrimSpace(boxDomain)
	if boxDomain == "" {
		return nil, errors.New("publicips: box domain must not be empty")
	}

	client := newMetadataClient()

	token, err := client.fetchToken(ctx)
	useToken := true
	switch {
	case err == nil:
		// ready to use token
	case errors.Is(err, errIMDSv1Only):
		useToken = false
	case errors.Is(err, errMetadataUnavailable):
		return map[netip.Addr]PublicIP{}, nil
	default:
		return nil, fmt.Errorf("publicips: fetch metadata token: %w", err)
	}

	macs, err := client.macAddresses(ctx, token, useToken)
	if err != nil {
		if errors.Is(err, errMetadataUnavailable) {
			return map[netip.Addr]PublicIP{}, nil
		}
		return nil, fmt.Errorf("publicips: list interfaces: %w", err)
	}

	mappings := make([]ipMapping, 0, len(macs))
	for _, mac := range macs {
		mac = strings.TrimSuffix(mac, "/")
		if mac == "" {
			continue
		}
		publics, err := client.publicIPv4s(ctx, mac, token, useToken)
		if err != nil {
			return nil, fmt.Errorf("publicips: list public IPv4s for %s: %w", mac, err)
		}
		for _, publicStr := range publics {
			publicStr = strings.TrimSpace(publicStr)
			if publicStr == "" {
				continue
			}

			privateStr, err := client.privateIPv4For(ctx, mac, publicStr, token, useToken)
			if err != nil {
				return nil, fmt.Errorf("publicips: resolve private IPv4 for %s/%s: %w", mac, publicStr, err)
			}

			publicAddr, err := netip.ParseAddr(publicStr)
			if err != nil {
				return nil, fmt.Errorf("publicips: invalid public IPv4 %q: %w", publicStr, err)
			}
			privateAddr, err := netip.ParseAddr(privateStr)
			if err != nil {
				return nil, fmt.Errorf("publicips: invalid private IPv4 %q for %s: %w", privateStr, publicStr, err)
			}
			mappings = append(mappings, ipMapping{
				public:  publicAddr,
				private: privateAddr,
			})
		}
	}

	result := make(map[netip.Addr]PublicIP, len(mappings))

	if len(mappings) == 0 {
		return result, nil
	}

	byPublicIP, err := resolveDomains(ctx, mappings, boxDomain)
	if err != nil {
		return nil, fmt.Errorf("publicips: resolve domains: %w", err)
	}

	for _, mapping := range mappings {
		info, ok := byPublicIP[mapping.public]
		if !ok {
			return nil, fmt.Errorf("publicips: missing domain for public IP %s", mapping.public)
		}
		result[mapping.private] = info
	}

	return result, nil
}

type metadataClient struct {
	base   string
	client *http.Client
}

func newMetadataClient() *metadataClient {
	return &metadataClient{
		base:   metadataEndpoint,
		client: newHTTPClient(),
	}
}

func (c *metadataClient) fetchToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+tokenPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(headerIMDSTTL, tokenTTLSeconds)

	resp, err := c.client.Do(req)
	if err != nil {
		if isUnavailable(err) {
			return "", errMetadataUnavailable
		}
		return "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		token, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if err != nil {
			return "", err
		}
		return string(token), nil
	case http.StatusNotFound, http.StatusUnauthorized, http.StatusMethodNotAllowed, http.StatusForbidden:
		return "", errIMDSv1Only
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("unexpected status %d fetching token: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func (c *metadataClient) macAddresses(ctx context.Context, token string, useToken bool) ([]string, error) {
	body, status, err := c.get(ctx, macsPath, token, useToken)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return splitLines(body), nil
	case http.StatusNotFound:
		return nil, errMetadataUnavailable
	default:
		return nil, fmt.Errorf("unexpected status %d listing interfaces", status)
	}
}

func (c *metadataClient) publicIPv4s(ctx context.Context, mac, token string, useToken bool) ([]string, error) {
	path := macsPath + mac + "/ipv4-associations/"
	body, status, err := c.get(ctx, path, token, useToken)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return splitLines(body), nil
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected status %d listing public IPv4s for %s", status, mac)
	}
}

func (c *metadataClient) privateIPv4For(ctx context.Context, mac, public, token string, useToken bool) (string, error) {
	path := macsPath + mac + "/ipv4-associations/" + public
	body, status, err := c.get(ctx, path, token, useToken)
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusOK:
		private := strings.TrimSpace(string(body))
		if private == "" {
			return "", fmt.Errorf("metadata returned empty private IPv4 for %s", public)
		}
		return private, nil
	case http.StatusNotFound:
		return "", fmt.Errorf("metadata missing private IPv4 for %s", public)
	default:
		return "", fmt.Errorf("unexpected status %d resolving private IPv4 for %s", status, public)
	}
}

func (c *metadataClient) get(ctx context.Context, path, token string, useToken bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, 0, err
	}
	if useToken {
		req.Header.Set(headerIMDSToken, token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		if isUnavailable(err) {
			return nil, 0, errMetadataUnavailable
		}
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func resolveDomains(ctx context.Context, mappings []ipMapping, boxDomain string) (map[netip.Addr]PublicIP, error) {
	if boxDomain == "" {
		return nil, fmt.Errorf("box domain must not be empty")
	}

	resolved := make(map[netip.Addr]PublicIP, len(mappings))
	if len(mappings) == 0 {
		return resolved, nil
	}

	needed := make(map[netip.Addr]struct{}, len(mappings))
	for _, mapping := range mappings {
		needed[mapping.public] = struct{}{}
	}

	// Try shards s001-s025, then fall back to the base domain itself (shard 0).
	seenIPs := make(map[netip.Addr]bool) // ensure each shard (and the base domain) are distinct
	for shard := 1; shard <= MaxDomainShards+1; shard++ {
		if len(resolved) == len(needed) {
			break
		}

		domain := ShardSub(shard) + "." + boxDomain
		if shard > MaxDomainShards {
			// fall back to base domain (shard 0)
			shard = 0
			domain = boxDomain
		}

		addrs, err := lookupDomainIPs(ctx, "ip4", domain)
		if err != nil {
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				continue
			}
			return nil, fmt.Errorf("lookup %q: %w", domain, err)
		}

		for _, addr := range addrs {
			if !addr.IsValid() || !addr.Is4() {
				continue
			}
			seenIPs[addr] = true
			if _, ok := needed[addr]; ok {
				resolved[addr] = PublicIP{IP: addr, Domain: domain, Shard: shard}
			}
		}
	}

	if !skipShardDistinctCheck && len(seenIPs) != MaxDomainShards+1 {
		return nil, fmt.Errorf("domain %q does not resolve to %d distinct IPv4 addresses for shards s001 through s%03d and the base domain, got %v (%d) ips, want %d", boxDomain, MaxDomainShards+1, MaxDomainShards, slices.Collect(maps.Keys(seenIPs)), len(seenIPs), MaxDomainShards+1)
	}

	if len(resolved) != len(needed) {
		missing := make([]string, 0, len(needed)-len(resolved))
		for addr := range needed {
			if _, ok := resolved[addr]; !ok {
				missing = append(missing, addr.String())
			}
		}
		slices.Sort(missing)
		return nil, fmt.Errorf("no domain mapping found for public IPs %v", missing)
	}

	return resolved, nil
}

func splitLines(b []byte) []string {
	raw := strings.Split(strings.TrimSpace(string(b)), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

func defaultHTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: connectTimeout}).DialContext,
		DisableKeepAlives:     false,
		ResponseHeaderTimeout: httpRequestTimeout,
		ExpectContinueTimeout: 100 * time.Millisecond,
		TLSHandshakeTimeout:   connectTimeout,
	}
	return &http.Client{
		Timeout:   httpRequestTimeout,
		Transport: tr,
	}
}

func defaultLookupDomainIPs(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

// LocalhostIPs returns a map of localhost IPs (127.21.0.1 through 127.21.0.25) to
// PublicIP info for local development. In dev, public == private (no NAT).
func LocalhostIPs(ctx context.Context, boxHost string) (map[netip.Addr]PublicIP, error) {
	m := make(map[netip.Addr]PublicIP, MaxDomainShards)
	for shard := 1; shard <= MaxDomainShards; shard++ {
		ip := netip.AddrFrom4([4]byte{127, 21, 0, byte(shard)})
		m[ip] = PublicIP{
			IP:     ip,
			Domain: ShardSub(shard) + "." + boxHost,
			Shard:  shard,
		}
	}
	return m, nil
}
