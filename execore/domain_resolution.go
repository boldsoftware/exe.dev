package execore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"exe.dev/domz"
)

// resolveCustomDomainBoxName determines the box name associated with a custom domain.
// It handles both traditional CNAME-based custom domains and apex domains that rely on
// ALIAS/ANAME records which resolve to A records pointing at exe.dev infrastructure.
func (s *Server) resolveCustomDomainBoxName(ctx context.Context, host string) (string, error) {
	host = domz.Canonicalize(host)
	if host == "" {
		return "", fmt.Errorf("host is empty")
	}

	cname, err := s.lookupCNAME(ctx, host)
	if err == nil {
		cname = domz.Canonicalize(cname)
		if cname != host {
			return s.boxNameFromCNAME(host, cname)
		}
		// If the canonical name matches the queried host, treat it as an apex domain.
		return s.resolveApexDomainBoxName(ctx, host)
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		// No CNAME found for host – treat as apex domain.
		return s.resolveApexDomainBoxName(ctx, host)
	}

	s.slog().WarnContext(ctx, "resolveCustomDomain: CNAME lookup failed", "host", host, "error", err)
	return "", fmt.Errorf("CNAME lookup failed for %s: %w", host, err)
}

// resolveApexDomainBoxName returns a box name if *both* the host
// (e.g. "example.com") has an A record pointed at us, and the www
// record (e.g. "www.example.com") has a CNAME pointed at a box.
func (s *Server) resolveApexDomainBoxName(ctx context.Context, host string) (string, error) {
	ips, err := s.lookupA(ctx, host)
	if err != nil {
		s.slog().WarnContext(ctx, "resolveCustomDomain: A lookup failed", "host", host, "error", err)
		return "", fmt.Errorf("A record lookup failed for %s: %w", host, err)
	}

	if len(s.PublicIPs) == 0 {
		s.slog().WarnContext(ctx, "resolveCustomDomain: no public IP metadata available", "host", host)
		return "", fmt.Errorf("public IP metadata not available for %s", host)
	}

	if !s.apexPointsToPublicIP(ips) {
		s.slog().WarnContext(ctx, "resolveCustomDomain: A record does not point to exe public IP", "host", host, "ips", ips)
		return "", fmt.Errorf("A record for %s does not point to exe public IPs: %v", host, ips)
	}

	wwwHost := "www." + host
	cname, err := s.lookupCNAME(ctx, wwwHost)
	if err != nil {
		s.slog().WarnContext(ctx, "resolveCustomDomain: www CNAME lookup failed", "host", wwwHost, "error", err)
		return "", fmt.Errorf("CNAME lookup failed for %s: %w", wwwHost, err)
	}
	cname = domz.Canonicalize(cname)
	return s.boxNameFromCNAME(wwwHost, cname)
}

func (s *Server) apexPointsToPublicIP(ips []netip.Addr) bool {
	for _, addr := range ips {
		for _, info := range s.PublicIPs {
			if addr == info.IP {
				return true
			}
		}
	}
	return false
}

func (s *Server) boxNameFromCNAME(queryHost, cname string) (string, error) {
	name, ok := strings.CutSuffix(cname, "."+s.getMainDomain())
	if !ok {
		s.slog().Warn("resolveCustomDomain: CNAME does not point to main domain", "host", queryHost, "cname", cname, "mainDomain", s.getMainDomain())
		return "", fmt.Errorf("CNAME does not point to %s: %s -> %s", s.getMainDomain(), queryHost, cname)
	}
	if name == "" {
		s.slog().Warn("resolveCustomDomain: empty box name from CNAME", "host", queryHost, "cname", cname)
		return "", fmt.Errorf("CNAME does not include box name for %s: %s", queryHost, cname)
	}
	return name, nil
}
