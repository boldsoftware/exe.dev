package execore

import (
	"context"

	"exe.dev/exeweb"
)

// resolveCustomDomainBoxName determines the box name associated with a custom domain.
// It handles both traditional CNAME-based custom domains and apex domains that rely on
// ALIAS/ANAME records which resolve to A records pointing at exe.dev infrastructure.
func (s *Server) resolveCustomDomainBoxName(ctx context.Context, host string) (string, error) {
	dr := exeweb.DomainResolver{
		Lg:              s.slog(),
		Env:             &s.env,
		LobbyIP:         s.LobbyIP,
		PublicIPs:       s.PublicIPs,
		LookupCNAMEFunc: s.lookupCNAMEFunc,
		LookupAFunc:     s.lookupAFunc,
	}
	return dr.ResolveCustomDomainBoxName(ctx, host)
}
