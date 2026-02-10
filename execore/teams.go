package execore

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"

	"exe.dev/domz"
	"exe.dev/exedb"
)

// TeamBoxAccessType represents how a user has access to a box.
type TeamBoxAccessType int

const (
	TeamBoxAccessNone      TeamBoxAccessType = iota
	TeamBoxAccessOwner                       // User created the box
	TeamBoxAccessTeamOwner                   // User is team owner, box belongs to team member
)

// FindAccessibleBox finds a box that the user can access for management operations.
// First checks direct ownership, then team owner access.
// Returns the box, access type, and error. Returns sql.ErrNoRows if not found.
func (s *Server) FindAccessibleBox(ctx context.Context, userID, boxName string) (*exedb.Box, TeamBoxAccessType, error) {
	// 1. Try direct ownership first (most common case)
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: userID,
	})
	if err == nil {
		return &box, TeamBoxAccessOwner, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, TeamBoxAccessNone, err
	}

	// 2. Check team owner access
	box, err = withRxRes1(s, ctx, (*exedb.Queries).GetBoxAccessibleByTeamOwner, exedb.GetBoxAccessibleByTeamOwnerParams{
		BoxName:     boxName,
		OwnerUserID: userID,
	})
	if err == nil {
		return &box, TeamBoxAccessTeamOwner, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, TeamBoxAccessNone, err
	}

	return nil, TeamBoxAccessNone, sql.ErrNoRows
}

// GetTeamForUser returns the team a user belongs to, or nil if not in a team.
func (s *Server) GetTeamForUser(ctx context.Context, userID string) (*exedb.GetTeamForUserRow, error) {
	team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamForUser, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &team, nil
}

// IsUserTeamOwner checks if the user is a team owner.
// Returns false if user is not in a team or is a regular member.
func (s *Server) IsUserTeamOwner(ctx context.Context, userID string) bool {
	isOwner, err := withRxRes1(s, ctx, (*exedb.Queries).IsUserTeamOwner, userID)
	if err != nil {
		return false
	}
	return isOwner
}

// GetEffectiveLimits returns the limits that apply to a user.
// If user is in a team, returns team limits. Otherwise returns user limits.
func (s *Server) GetEffectiveLimits(ctx context.Context, userID string) (*UserLimits, error) {
	team, err := s.GetTeamForUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	if team != nil && team.Limits != nil {
		// Use team limits
		return ParseUserLimitsFromJSON(*team.Limits), nil
	}

	// Use user limits
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		return nil, err
	}
	return ParseUserLimits(&user), nil
}

// CountBoxesForLimitCheck returns the box count to use for limit checking.
// If user is in a team, returns total team boxes. Otherwise returns user's boxes.
func (s *Server) CountBoxesForLimitCheck(ctx context.Context, userID string) (int64, error) {
	team, err := s.GetTeamForUser(ctx, userID)
	if err != nil {
		return 0, err
	}

	if team != nil {
		return withRxRes1(s, ctx, (*exedb.Queries).CountTeamBoxes, userID)
	}

	return withRxRes1(s, ctx, (*exedb.Queries).CountBoxesForUser, userID)
}

// ListTeamBoxesForOwner returns boxes created by other team members.
// Returns nil if user is not a team owner.
func (s *Server) ListTeamBoxesForOwner(ctx context.Context, userID string) ([]exedb.ListTeamBoxesForOwnerRow, error) {
	if !s.IsUserTeamOwner(ctx, userID) {
		return nil, nil
	}
	return withRxRes1(s, ctx, (*exedb.Queries).ListTeamBoxesForOwner, userID)
}

// FindTeamBoxByIPShard finds a team member's box by local IP address when requester is a team owner.
// This enables DNS-based routing (ssh vmname.exe.cloud) for team owners accessing member boxes.
func (s *Server) FindTeamBoxByIPShard(ctx context.Context, userID, localIP string) *exedb.Box {
	if userID == "" || localIP == "" {
		return nil
	}
	host := domz.StripPort(localIP)
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}

	info, ok := s.PublicIPs[addr]
	if !ok {
		return nil
	}

	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByTeamOwnerAndShard, exedb.GetBoxByTeamOwnerAndShardParams{
		Shard:       int64(info.Shard),
		OwnerUserID: userID,
	})
	if err != nil {
		return nil
	}
	s.slog().InfoContext(ctx, "FindTeamBoxByIPShard found team box", "owner_id", userID, "localIP", localIP, "shard", info.Shard, "box_name", box.Name)
	return &box
}
