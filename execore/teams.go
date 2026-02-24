package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"exe.dev/domz"
	"exe.dev/email"
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
	return &box
}

// deleteTeamMember deletes a member from a team.
func (s *Server) deleteTeamMember(ctx context.Context, teamID, userID string) error {
	err := withTx1(s, ctx, (*exedb.Queries).DeleteTeamMember, exedb.DeleteTeamMemberParams{
		TeamID: teamID,
		UserID: userID,
	})
	if err != nil {
		return err
	}
	proxyChangeDeletedTeamMember(teamID, userID)
	return nil
}

// createPendingTeamInvite creates a pending invite for a non-existent user and sends them an email.
// Returns nil if the invite was created and email sent successfully.
func (s *Server) createPendingTeamInvite(ctx context.Context, teamID, teamName, invitedEmail, invitedByUserID string) error {
	ce := canonicalizeEmail(invitedEmail)
	token := generateRegistrationToken()

	err := withTx1(s, ctx, (*exedb.Queries).InsertPendingTeamInvite, exedb.InsertPendingTeamInviteParams{
		TeamID:          teamID,
		Email:           invitedEmail,
		CanonicalEmail:  ce,
		InvitedByUserID: invitedByUserID,
		Token:           token,
		ExpiresAt:       time.Now().Add(30 * 24 * time.Hour),
	})
	if err != nil {
		return fmt.Errorf("failed to create pending team invite: %w", err)
	}

	// Send invite email
	link := fmt.Sprintf("https://%s/auth?team_invite=%s", s.env.WebHost, token)
	subject := fmt.Sprintf("You've been invited to %s on %s", teamName, s.env.WebHost)
	body := fmt.Sprintf(`Hello,

You've been invited to join the team "%s" on %s.

Click below to create your account and join the team:

%s

This invite expires in 30 days.

---
%s`, teamName, s.env.WebHost, link, s.env.WebHost)

	if err := s.sendEmail(ctx, email.TypeTeamInvitation, invitedEmail, subject, body); err != nil {
		slog.ErrorContext(ctx, "failed to send team invite email", "error", err, "email", invitedEmail, "team_id", teamID)
		// Don't fail the invite creation if email sending fails
	}

	return nil
}

// resolvePendingTeamInvites adds users to teams when they have pending invites.
// Called after user creation or login, following the same pattern as resolvePendingShares.
func (s *Server) resolvePendingTeamInvites(ctx context.Context, userEmail, userID string) error {
	ce := canonicalizeEmail(userEmail)
	invites, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingTeamInvitesByEmail, ce)
	if err != nil {
		return err
	}

	if len(invites) == 0 {
		return nil
	}

	for _, invite := range invites {
		// Try to add user to the team
		err := withTx1(s, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
			TeamID: invite.TeamID,
			UserID: userID,
			Role:   "user",
		})
		if err != nil {
			// User might already be in a team (UNIQUE constraint on user_id)
			slog.WarnContext(ctx, "failed to add user to team from pending invite",
				"error", err, "team_id", invite.TeamID, "user_id", userID)
			continue
		}

		// Mark invite as accepted
		if err := withTx1(s, ctx, (*exedb.Queries).MarkPendingTeamInviteAccepted, exedb.MarkPendingTeamInviteAcceptedParams{
			AcceptedByUserID: &userID,
			ID:               invite.ID,
		}); err != nil {
			slog.ErrorContext(ctx, "failed to mark pending team invite accepted",
				"error", err, "invite_id", invite.ID)
		}

		slog.InfoContext(ctx, "resolved pending team invite",
			"team_id", invite.TeamID, "team_name", invite.TeamName,
			"user_id", userID, "email", userEmail)
	}

	return nil
}
