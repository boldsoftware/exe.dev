package exeweb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"

	"exe.dev/dnsresolver"
	"exe.dev/domz"
	"exe.dev/errorz"
	"exe.dev/publicips"
	"exe.dev/stage"
)

// ErrHostIsIPAddress is returned when a custom domain request
// uses an IP address instead of a domain name.
// This commonly happens from port scanners/bots.
var ErrHostIsIPAddress = errors.New("host is an IP address, not a domain")

// DomainResolver resolves custom domains.
type DomainResolver struct {
	Lg        *slog.Logger
	Env       *stage.Env
	LobbyIP   netip.Addr
	PublicIPs map[netip.Addr]publicips.PublicIP

	// Hooks for testing.
	LookupCNAMEFunc func(context.Context, string) (string, error)
	LookupAFunc     func(context.Context, string, string) ([]netip.Addr, error)
}

// ErrInvalidBoxName is returned by [DomainResolver.ResolveBoxName]
// if the box name is invalid.
var ErrInvalidBoxName = errors.New("invalid box name")

// ResolveBoxName converts a hostname to a box name.
// If hostname is a subdomain of the main domain (e.g., box.exe.dev),
// it returns the box name with the main domain suffix stripped (e.g., "box").
// Shelley subdomains (box.shelley.exe.xyz) are handled by
// stripping the ".shelley" part.
// For all other hostname values, a CNAME lookup is performed, and the above
// rules are applied to the result; otherwise an error is returned.
func (dr *DomainResolver) ResolveBoxName(ctx context.Context, hostname string) (string, error) {
	hostname = domz.Canonicalize(hostname)
	// Reject empty hostnames (cheap check).
	if hostname == "" {
		return "", ErrInvalidBoxName
	}
	// Reject exact box domain (apex).
	if hostname == dr.Env.BoxHost {
		return "", ErrInvalidBoxName
	}
	// If a subdomain of our box domain, return the box name.
	// Use CutBase (not Label) to handle multi-level subdomains like box.shelley.exe.xyz
	sub, ok := domz.CutBase(hostname, dr.Env.BoxHost)
	if ok && sub != "" {
		// Handle shelley subdomain: box.shelley.exe.xyz -> box
		if boxName, isShelley := strings.CutSuffix(sub, ".shelley"); isShelley {
			return boxName, nil
		}
		// For regular subdomains, only accept single-level (no dots)
		if !strings.Contains(sub, ".") {
			return sub, nil
		}
	}

	// Reject non-domain hostnames.
	if !strings.Contains(hostname, ".") {
		return "", ErrInvalidBoxName
	}

	return dr.ResolveCustomDomainBoxName(ctx, hostname)
}

// ResolveCustomDomainBoxName determines the box name
// associated with a custom domain.
// It handles both traditional CNAME-based custom domains
// and apex domains that rely on ALIAS/ANAME records which
// resolve to A records pointing at exe.dev infrastructure.
func (dr *DomainResolver) ResolveCustomDomainBoxName(ctx context.Context, host string) (string, error) {
	host = domz.Canonicalize(host)
	if host == "" {
		return "", errors.New("host is empty")
	}

	// Reject IP addresses early.
	// This commonly happens from port scanners/bots
	// connecting with the IP as the TLS SNI hostname.
	if domz.IsIPAddr(host) {
		return "", ErrHostIsIPAddress
	}

	cname, err := dr.lookupCNAME(ctx, host)
	if err != nil {
		if dnsErr, ok := errorz.AsType[*net.DNSError](err); ok && dnsErr.IsNotFound {
			// No CNAME found for host – treat as apex domain.
			return dr.resolveApexDomainBoxName(ctx, host)
		}

		dr.Lg.WarnContext(ctx, "resolveCustomDomain: CNAME lookup failed", "host", host, "error", err)
		return "", fmt.Errorf("CNAME lookup failed for %s: %w", host, err)
	}

	cname = domz.Canonicalize(cname)
	if cname != host {
		return dr.boxNameFromCNAME(ctx, host, cname)
	}

	// If the canonical name matches the queried host,
	// treat it as an apex domain.
	return dr.resolveApexDomainBoxName(ctx, host)
}

// resolveApexDomainBoxName returns a box name if *both* the host
// (e.g. "example.com") has an A record pointed at us, and the www
// record (e.g. "www.example.com") has a CNAME pointed at a box.
func (dr *DomainResolver) resolveApexDomainBoxName(ctx context.Context, host string) (string, error) {
	ips, err := dr.lookupA(ctx, host)
	if err != nil {
		dr.Lg.WarnContext(ctx, "resolveCustomDomain: A lookup failed", "host", host, "error", err)
		return "", fmt.Errorf("A record lookup failed for %s: %w", host, err)
	}

	if len(dr.PublicIPs) == 0 {
		dr.Lg.WarnContext(ctx, "resolveCustomDomain: no public IP metadata available", "host", host)
		return "", fmt.Errorf("public IP metadata not available for %s", host)
	}

	if !dr.apexPointsToPublicIP(ips) {
		dr.Lg.WarnContext(ctx, "resolveCustomDomain: A record does not point to exe public IP", "host", host, "ips", ips)
		return "", fmt.Errorf("A record for %s does not point to exe public IPs: %v", host, ips)
	}

	wwwHost := "www." + host
	cname, err := dr.lookupCNAME(ctx, wwwHost)
	if err != nil {
		dr.Lg.WarnContext(ctx, "resolveCustomDomain: www CNAME lookup failed", "host", wwwHost, "error", err)
		return "", fmt.Errorf("CNAME lookup failed for %s: %w", wwwHost, err)
	}
	cname = domz.Canonicalize(cname)
	return dr.boxNameFromCNAME(ctx, wwwHost, cname)
}

// apexPointsToPublicIP reports whether any of the ips are the same as
// any of the public IPs.
func (dr *DomainResolver) apexPointsToPublicIP(ips []netip.Addr) bool {
	for _, addr := range ips {
		// Check lobby IP (the IP for exe.xyz / ssh exe.dev)
		if dr.LobbyIP.IsValid() && addr == dr.LobbyIP {
			return true
		}
		// Check shard IPs (naNNN.exe.xyz)
		for _, info := range dr.PublicIPs {
			if addr == info.IP {
				return true
			}
		}
	}
	return false
}

// boxNameFromCname returns the box name to use for a CNAME.
func (dr *DomainResolver) boxNameFromCNAME(ctx context.Context, queryHost, cname string) (string, error) {
	name, ok := domz.CutBase(cname, dr.Env.BoxHost)
	if !ok {
		dr.Lg.WarnContext(ctx, "resolveCustomDomain: CNAME does not point to main domain", "host", queryHost, "cname", cname, "mainDomain", dr.Env.BoxHost)
		return "", fmt.Errorf("CNAME does not point to %s: %s -> %s", dr.Env.BoxHost, queryHost, cname)
	}
	if name == "" {
		dr.Lg.WarnContext(ctx, "resolveCustomDomain: empty box name from CNAME", "host", queryHost, "cname", cname)
		return "", fmt.Errorf("CNAME does not include VM name for %s: %s", queryHost, cname)
	}
	if strings.Contains(name, ".") {
		dr.Lg.WarnContext(ctx, "resolveCustomDomain: nested box name from CNAME", "host", queryHost, "cname", cname, "boxName", name)
		return "", fmt.Errorf("CNAME must use single-label VM name for %s: %s", queryHost, cname)
	}
	return name, nil
}

// lookupCNAME does a CNAME DNS lookup for a host.
func (dr *DomainResolver) lookupCNAME(ctx context.Context, host string) (string, error) {
	if dr.LookupCNAMEFunc != nil {
		return dr.LookupCNAMEFunc(ctx, host)
	}
	cname, err := dnsresolver.LookupCNAME(ctx, host)
	if err == nil {
		return cname, nil
	}
	if errorz.HasType[*net.DNSError](err) {
		return "", err
	}
	dr.Lg.WarnContext(ctx, "lookupCNAME: fallback to net resolver", "host", host, "error", err)
	return net.DefaultResolver.LookupCNAME(ctx, host)
}

// lookupA does an A DNS lookup for a host.
func (dr *DomainResolver) lookupA(ctx context.Context, host string) ([]netip.Addr, error) {
	fn := dnsresolver.Resolver().LookupNetIP
	if dr.LookupAFunc != nil {
		fn = dr.LookupAFunc
	}
	addrs, err := fn(ctx, "ip4", host)
	for i, addr := range addrs {
		addrs[i] = addr.Unmap()
	}
	return addrs, err
}
