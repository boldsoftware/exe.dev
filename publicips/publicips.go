package publicips

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"exe.dev/errorz"
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
	// MaxDomainShards is the largest valid shard ID.
	// Shards map to public IP addresses: sNNN.exe.dev.
	// MaxDomainShards must match the DB CHECK constraint.
	// IP shards are 1-based. (The zero value is intentionally invalid.)
	MaxDomainShards = 253
)

// ShardIsValid reports whether shard is within the valid range (0, MaxDomainShards].
func ShardIsValid(shard int) bool {
	return shard >= 1 && shard <= MaxDomainShards
}

var (
	metadataEndpoint = metadataEndpointDefault
	newHTTPClient    = defaultHTTPClient

	lookupDomainIPs = defaultLookupDomainIPs

	errMetadataUnavailable = errors.New("metadata service unavailable")
	errIMDSv1Only          = errors.New("metadata service requires IMDSv1")
)

func ShardSub(shard int) string {
	return fmt.Sprintf("s%03d", shard)
}

// LatitudeShardSub returns the latitude subdomain label for a shard (e.g. "n007").
func LatitudeShardSub(shard int) string {
	return fmt.Sprintf("n%03d", shard)
}

// PublicIP describes a public IPv4 address, associated domain name, and shard.
type PublicIP struct {
	IP     netip.Addr
	Domain string // full domain, e.g. "s007.exe.cloud"
	Shard  int    // shard number, e.g. 7
}

// IPMapping describes a public/private IPv4 address pair from EC2 metadata.
type IPMapping struct {
	Public  netip.Addr
	Private netip.Addr
}

// EC2IPMappings queries EC2 metadata to get (public, private) IP pairs.
// This does NOT do any DNS lookups. Returns nil if not running on EC2.
func EC2IPMappings(ctx context.Context) ([]IPMapping, error) {
	if ctx == nil {
		return nil, errors.New("publicips: context must not be nil")
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
		return nil, nil
	default:
		return nil, fmt.Errorf("publicips: fetch metadata token: %w", err)
	}

	macs, err := client.macAddresses(ctx, token, useToken)
	if errors.Is(err, errMetadataUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("publicips: list interfaces: %w", err)
	}

	mappings := make([]IPMapping, 0, len(macs))
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
			mappings = append(mappings, IPMapping{
				Public:  publicAddr,
				Private: privateAddr,
			})
		}
	}
	return mappings, nil
}

// EC2PrivateIPs returns the private IPv4 addresses from EC2 metadata.
// This does NOT do any DNS lookups - it only queries EC2 metadata.
// Returns nil if not running on EC2.
func EC2PrivateIPs(ctx context.Context) ([]netip.Addr, error) {
	mappings, err := EC2IPMappings(ctx)
	if err != nil {
		return nil, err
	}
	if len(mappings) == 0 {
		return nil, nil
	}
	result := make([]netip.Addr, 0, len(mappings))
	for _, m := range mappings {
		result = append(result, m.Private)
	}
	return result, nil
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

	mappings, err := EC2IPMappings(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[netip.Addr]PublicIP, len(mappings))

	if len(mappings) == 0 {
		return result, nil
	}

	byPublicIP, err := resolveDomains(ctx, mappings, boxDomain, MaxDomainShards)
	if err != nil {
		return nil, fmt.Errorf("publicips: resolve domains: %w", err)
	}

	for _, mapping := range mappings {
		info, ok := byPublicIP[mapping.Public]
		if !ok {
			return nil, fmt.Errorf("publicips: missing domain for public IP %s", mapping.Public)
		}
		result[mapping.Private] = info
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
	if isUnavailable(err) {
		return "", errMetadataUnavailable
	}
	if err != nil {
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
	if isUnavailable(err) {
		return nil, 0, errMetadataUnavailable
	}
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func resolveDomains(ctx context.Context, mappings []IPMapping, boxDomain string, numShards int) (map[netip.Addr]PublicIP, error) {
	if boxDomain == "" {
		return nil, fmt.Errorf("box domain must not be empty")
	}

	resolved := make(map[netip.Addr]PublicIP, len(mappings))
	if len(mappings) == 0 {
		return resolved, nil
	}

	needed := make(map[netip.Addr]struct{}, len(mappings))
	for _, mapping := range mappings {
		needed[mapping.Public] = struct{}{}
	}

	// Try shards s001-sNNN, then fall back to the base domain itself (shard 0).
	for shard := 1; shard <= numShards+1; shard++ {
		if len(resolved) == len(needed) {
			break
		}

		domain := ShardSub(shard) + "." + boxDomain
		if shard > numShards {
			// fall back to base domain (shard 0)
			shard = 0
			domain = boxDomain
		}

		addrs, err := lookupDomainIPs(ctx, "ip4", domain)
		if dnsErr, ok := errorz.AsType[*net.DNSError](err); ok && dnsErr.IsNotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("lookup %q: %w", domain, err)
		}

		for _, addr := range addrs {
			if !addr.IsValid() || !addr.Is4() {
				continue
			}
			if _, ok := needed[addr]; ok {
				resolved[addr] = PublicIP{IP: addr, Domain: domain, Shard: shard}
			}
		}
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
	if netErr, ok := errorz.AsType[net.Error](err); ok {
		if netErr.Timeout() {
			return true
		}
	}
	return errorz.HasType[*net.OpError](err)
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

// LocalhostIPs returns a map of localhost IPs to PublicIP info for local development.
// In dev, public == private (no NAT). Shards are mapped to the 127.21.0.0/16 range:
// shard 1 → 127.21.0.1, shard 254 → 127.21.0.254, shard 255 → 127.21.1.1, etc.
func LocalhostIPs(ctx context.Context, boxHost string, numShards int) (map[netip.Addr]PublicIP, error) {
	m := make(map[netip.Addr]PublicIP, numShards)
	for shard := 1; shard <= numShards; shard++ {
		// Map shard to 127.21.X.Y, avoiding .0 addresses.
		// Each /24 holds 254 shards (1-254), giving 254*256 = 65024 capacity.
		x := byte((shard - 1) / 254)
		y := byte((shard-1)%254 + 1)
		ip := netip.AddrFrom4([4]byte{127, 21, x, y})
		m[ip] = PublicIP{
			IP:     ip,
			Domain: ShardSub(shard) + "." + boxHost,
			Shard:  shard,
		}
	}
	return m, nil
}
