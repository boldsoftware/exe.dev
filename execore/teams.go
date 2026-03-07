package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"exe.dev/boxname"
	"exe.dev/domz"
	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/oidcauth"
)

// TeamBoxAccessType represents how a user has access to a box.
type TeamBoxAccessType int

const (
	TeamBoxAccessNone       TeamBoxAccessType = iota
	TeamBoxAccessOwner                        // User created the box
	TeamBoxAccessTeamSudoer                   // User is team sudoer, box belongs to team member
)

// FindAccessibleBox finds a box that the user can access for management operations.
// First checks direct ownership, then team sudoer access.
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

	// 2. Check team sudoer access
	box, err = withRxRes1(s, ctx, (*exedb.Queries).GetBoxAccessibleByTeamSudoer, exedb.GetBoxAccessibleByTeamSudoerParams{
		BoxName:      boxName,
		SudoerUserID: userID,
	})
	if err == nil {
		return &box, TeamBoxAccessTeamSudoer, nil
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

// IsUserTeamAdmin checks if the user is a team admin (billing_owner or sudoer).
// Returns false if user is not in a team or is a regular member.
func (s *Server) IsUserTeamAdmin(ctx context.Context, userID string) bool {
	isAdmin, err := withRxRes1(s, ctx, (*exedb.Queries).IsUserTeamAdmin, userID)
	if err != nil {
		return false
	}
	return isAdmin
}

// IsUserTeamSudoer checks if the user is a team sudoer (has SSH access to member VMs).
func (s *Server) IsUserTeamSudoer(ctx context.Context, userID string) bool {
	isSudoer, err := withRxRes1(s, ctx, (*exedb.Queries).IsUserTeamSudoer, userID)
	if err != nil {
		return false
	}
	return isSudoer
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

// ListTeamBoxesForSudoer returns boxes created by other team members.
// Returns nil if user is not a team sudoer.
func (s *Server) ListTeamBoxesForSudoer(ctx context.Context, userID string) ([]exedb.ListTeamBoxesForSudoerRow, error) {
	if !s.IsUserTeamSudoer(ctx, userID) {
		return nil, nil
	}
	return withRxRes1(s, ctx, (*exedb.Queries).ListTeamBoxesForSudoer, userID)
}

// FindTeamBoxByIPShard finds a team member's box by local IP address when requester is a team sudoer.
// This enables DNS-based routing (ssh vmname.exe.cloud) for team sudoers accessing member boxes.
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

	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByTeamSudoerAndShard, exedb.GetBoxByTeamSudoerAndShardParams{
		Shard:        int64(info.Shard),
		SudoerUserID: userID,
	})
	if err != nil {
		return nil
	}
	return &box
}

// FindTeamSSHSharedBoxByIPShard finds a team member's box by IP shard when the box has team SSH enabled.
// This enables any team member to SSH into boxes where the owner has run `share ssh allow`.
func (s *Server) FindTeamSSHSharedBoxByIPShard(ctx context.Context, userID, localIP string) *exedb.Box {
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

	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByTeamSSHAndShard, exedb.GetBoxByTeamSSHAndShardParams{
		Shard:  int64(info.Shard),
		UserID: userID,
	})
	if err != nil {
		return nil
	}
	return &box
}

// FindTeamSSHSharedBoxByName finds a team member's box by name when the box has team SSH enabled.
// This enables username routing: ssh boxname@exe.xyz for team SSH shared boxes.
func (s *Server) FindTeamSSHSharedBoxByName(ctx context.Context, userID, boxName string) *exedb.Box {
	if userID == "" || boxName == "" || !boxname.IsValid(boxName) {
		return nil
	}
	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByTeamSSHAndName, exedb.GetBoxByTeamSSHAndNameParams{
		BoxName: boxName,
		UserID:  userID,
	})
	if err != nil {
		return nil
	}
	return &box
}

// resolveTeamShardCollisions reassigns IP shards for a newly-joined team member
// whose existing boxes collide with other team members' shards.
// This is best-effort: errors are logged but do not fail the join.
func (s *Server) resolveTeamShardCollisions(ctx context.Context, teamID, newUserID string) {
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		collisions, err := queries.GetTeamShardCollisions(ctx, exedb.GetTeamShardCollisionsParams{
			TeamID:    teamID,
			NewUserID: newUserID,
		})
		if err != nil {
			return fmt.Errorf("checking shard collisions: %w", err)
		}
		if len(collisions) == 0 {
			return nil
		}

		// Get all shards now used by the team (including the new member).
		allShards, err := queries.ListIPShardsForTeam(ctx, newUserID)
		if err != nil {
			return fmt.Errorf("listing team shards: %w", err)
		}
		used := make([]bool, s.env.NumShards+1)
		for _, shard := range allShards {
			if s.env.ShardIsValid(int(shard)) {
				used[int(shard)] = true
			}
		}

		for _, c := range collisions {
			// Find first unused shard.
			var newShard int
			for candidate := 1; candidate <= s.env.NumShards; candidate++ {
				if !used[candidate] {
					newShard = candidate
					break
				}
			}
			if newShard == 0 {
				slog.WarnContext(ctx, "no free shard for collision resolution",
					"box_id", c.BoxID, "old_shard", c.IPShard, "team_id", teamID)
				continue
			}

			if err := queries.UpdateBoxIPShard(ctx, exedb.UpdateBoxIPShardParams{
				IPShard: int64(newShard),
				BoxID:   c.BoxID,
			}); err != nil {
				return fmt.Errorf("reassigning box %d from shard %d to %d: %w", c.BoxID, c.IPShard, newShard, err)
			}

			used[newShard] = true
			slog.InfoContext(ctx, "resolved team shard collision",
				"box_id", c.BoxID, "old_shard", c.IPShard, "new_shard", newShard,
				"team_id", teamID, "user_id", newUserID)
		}
		return nil
	})
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve team shard collisions",
			"error", err, "team_id", teamID, "user_id", newUserID)
	}
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

	// Inherit auth_provider: prefer team-level setting, then team SSO provider,
	// then fall back to inviter's auth_provider.
	var authProvider *string
	if tap, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamAuthProvider, teamID); err == nil && tap != nil {
		authProvider = tap
	} else if _, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamSSOProvider, teamID); err == nil {
		op := oidcauth.ProviderName
		authProvider = &op
	} else if ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserAuthProvider, invitedByUserID); err == nil {
		authProvider = ap.AuthProvider
	}

	err := withTx1(s, ctx, (*exedb.Queries).InsertPendingTeamInvite, exedb.InsertPendingTeamInviteParams{
		TeamID:          teamID,
		Email:           invitedEmail,
		CanonicalEmail:  ce,
		InvitedByUserID: invitedByUserID,
		Token:           token,
		ExpiresAt:       time.Now().Add(30 * 24 * time.Hour),
		AuthProvider:    authProvider,
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

	if err := s.sendEmail(ctx, sendEmailParams{
		emailType: email.TypeTeamInvitation,
		to:        invitedEmail,
		subject:   subject,
		body:      body,
		fromName:  "",
		attrs:     []slog.Attr{slog.String("invited_by_user_id", invitedByUserID)},
	}); err != nil {
		slog.ErrorContext(ctx, "failed to send team invite email", "error", err, "email", invitedEmail, "team_id", teamID)
		// Don't fail the invite creation if email sending fails
	}

	return nil
}

// resolvePendingTeamInvites adds users to teams when they have pending invites.
// Called after user creation or login, following the same pattern as resolvePendingShares.
// Only processes invites for users whose account was created after the invite,
// to prevent accidentally merging existing accounts into teams.
func (s *Server) resolvePendingTeamInvites(ctx context.Context, userEmail, userID string) error {
	ce := canonicalizeEmail(userEmail)
	invites, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingTeamInvitesByEmail, ce)
	if err != nil {
		return err
	}

	if len(invites) == 0 {
		return nil
	}

	// Look up when the user account was created.
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		return fmt.Errorf("looking up user for team invite resolution: %w", err)
	}

	for _, invite := range invites {
		// Only allow the invite if the user account was created after the invite.
		// This prevents existing users from being silently merged into a team
		// when they happen to log in after someone invited their email.
		if invite.CreatedAt != nil && user.CreatedAt.Before(*invite.CreatedAt) {
			slog.WarnContext(ctx, "skipping team invite for pre-existing user",
				"team_id", invite.TeamID, "user_id", userID, "email", userEmail,
				"user_created_at", user.CreatedAt, "invite_created_at", invite.CreatedAt)
			continue
		}

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

		s.resolveTeamShardCollisions(ctx, invite.TeamID, userID)

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
