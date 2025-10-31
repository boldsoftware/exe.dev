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
	domainNameFormat = "s%03d.exe.dev"
	maxDomainShards  = 25
)

var fallbackDomains = []string{"exe.dev"}

var (
	metadataEndpoint = metadataEndpointDefault
	newHTTPClient    = defaultHTTPClient

	lookupDomainIPs = defaultLookupDomainIPs

	errMetadataUnavailable = errors.New("metadata service unavailable")
	errIMDSv1Only          = errors.New("metadata service requires IMDSv1")
)

// PublicIP describes the public IPv4 address and associated domain name mapped
// to a private IPv4 address.
type PublicIP struct {
	IP     netip.Addr
	Domain string
}

type ipMapping struct {
	public  netip.Addr
	private netip.Addr
}

// IPs returns a mapping of private IPv4 addresses to their associated public IPv4
// metadata on the current EC2 instance. If the process is not running on EC2,
// it returns an empty map and a nil error.
func IPs(ctx context.Context) (map[netip.Addr]PublicIP, error) {
	if ctx == nil {
		return nil, errors.New("publicips: context must not be nil")
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

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

	domains, err := resolveDomains(ctx, mappings)
	if err != nil {
		return nil, fmt.Errorf("publicips: resolve domains: %w", err)
	}

	for _, mapping := range mappings {
		domain, ok := domains[mapping.public]
		if !ok {
			return nil, fmt.Errorf("publicips: missing domain for public IP %s", mapping.public)
		}
		result[mapping.private] = PublicIP{
			IP:     mapping.public,
			Domain: domain,
		}
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

func resolveDomains(ctx context.Context, mappings []ipMapping) (map[netip.Addr]string, error) {
	if len(mappings) == 0 {
		return map[netip.Addr]string{}, nil
	}

	needed := make(map[netip.Addr]struct{}, len(mappings))
	for _, mapping := range mappings {
		needed[mapping.public] = struct{}{}
	}

	resolved := make(map[netip.Addr]string, len(needed))
	for shard := 1; shard <= maxDomainShards; shard++ {
		if len(resolved) == len(needed) {
			break
		}

		domain := fmt.Sprintf(domainNameFormat, shard)

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
			if _, ok := needed[addr]; ok {
				resolved[addr] = domain
			}
		}
	}

	if len(resolved) != len(needed) {
		for _, domain := range fallbackDomains {
			if len(resolved) == len(needed) {
				break
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
				if _, ok := needed[addr]; ok {
					resolved[addr] = domain
				}
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
