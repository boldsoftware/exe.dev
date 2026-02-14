package execore

import (
	"context"

	"exe.dev/exeweb"
)

// resolveBoxName converts a hostname to a box name.
// If hostname is a subdomain of the main domain (e.g., box.exe.dev),
// it returns the box name with the main domain suffix stripped (e.g., "box").
// Shelley subdomains (box.shelley.exe.xyz) are handled by stripping the ".shelley" part.
// For all other hostname values, a CNAME lookup is performed, and the above
// rules are applied to the result; otherwise an error is returned.
func (s *Server) resolveBoxName(ctx context.Context, hostname string) (string, error) {
	return s.domainResolver().ResolveBoxName(ctx, hostname)
}

// resolveCustomDomainBoxName determines the box name associated with a custom domain.
// It handles both traditional CNAME-based custom domains and apex domains that rely on
// ALIAS/ANAME records which resolve to A records pointing at exe.dev infrastructure.
func (s *Server) resolveCustomDomainBoxName(ctx context.Context, host string) (string, error) {
	return s.domainResolver().ResolveCustomDomainBoxName(ctx, host)
}

// domainResolver returns a [exeweb.DomainResolver] for [Server].
func (s *Server) domainResolver() *exeweb.DomainResolver {
	return &exeweb.DomainResolver{
		Lg:              s.slog(),
		Env:             &s.env,
		LobbyIP:         s.LobbyIP,
		PublicIPs:       s.PublicIPs,
		LookupCNAMEFunc: s.lookupCNAMEFunc,
		LookupAFunc:     s.lookupAFunc,
	}
}
