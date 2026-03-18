package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"exe.dev/boxname"
	"exe.dev/domz"
	"exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/oidcauth"
)

// validTeamSlug matches one or more lowercase alphanumeric/underscore characters.
var validTeamSlug = regexp.MustCompile(`^[a-z0-9_]+$`)

// parseTeamID normalizes a team identifier to the canonical "tm_" prefixed form.
// Accepts either "foo" or "tm_foo" and returns "tm_foo".
// Returns an error if the slug portion is empty or contains invalid characters.
func parseTeamID(raw string) (string, error) {
	slug := strings.TrimPrefix(raw, "tm_")
	if slug == "" {
		return "", fmt.Errorf("team ID cannot be empty")
	}
	if !validTeamSlug.MatchString(slug) {
		return "", fmt.Errorf("team ID %q must contain only lowercase letters, numbers, and underscores", raw)
	}
	return "tm_" + slug, nil
}

// TeamBoxAccessType represents how a user has access to a box.
type TeamBoxAccessType int

const (
	TeamBoxAccessNone      TeamBoxAccessType = iota
	TeamBoxAccessOwner                       // User created the box
	TeamBoxAccessTeamAdmin                   // User is team admin, box belongs to team member
)

// FindAccessibleBox finds a box that the user can access for management operations.
// First checks direct ownership, then team admin access.
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

	// 2. Check team admin access
	box, err = withRxRes1(s, ctx, (*exedb.Queries).GetBoxAccessibleByTeamAdmin, exedb.GetBoxAccessibleByTeamAdminParams{
		BoxName:     boxName,
		AdminUserID: userID,
	})
	if err == nil {
		return &box, TeamBoxAccessTeamAdmin, nil
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

// IsUserTeamAdmin checks if the user is a team admin (billing_owner or admin).
// Returns false if user is not in a team or is a regular member.
func (s *Server) IsUserTeamAdmin(ctx context.Context, userID string) bool {
	isAdmin, err := withRxRes1(s, ctx, (*exedb.Queries).IsUserTeamAdmin, userID)
	if err != nil {
		return false
	}
	return isAdmin
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

// ListTeamBoxesForAdmin returns boxes created by other team members.
// Returns nil if user is not a team admin.
func (s *Server) ListTeamBoxesForAdmin(ctx context.Context, userID string) ([]exedb.ListTeamBoxesForAdminRow, error) {
	if !s.IsUserTeamAdmin(ctx, userID) {
		return nil, nil
	}
	return withRxRes1(s, ctx, (*exedb.Queries).ListTeamBoxesForAdmin, userID)
}

// FindTeamBoxByIPShard finds a team member's box by local IP address when requester is a team admin.
// This enables DNS-based routing (ssh vmname.exe.cloud) for team admins accessing member boxes.
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

	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByTeamAdminAndShard, exedb.GetBoxByTeamAdminAndShardParams{
		Shard:       int64(info.Shard),
		AdminUserID: userID,
	})
	if err != nil {
		return nil
	}
	return &box
}

// FindTeamSSHSharedBoxByIPShard finds a team member's box by IP shard when the box has team SSH enabled.
// This enables any team member to SSH into boxes where the owner has run `share access allow`.
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

// transferBox transfers ownership of a box to a new user within the same team.
// It updates the box owner, IP shard user, and cleans up all shares (individual + team).
func (s *Server) transferBox(ctx context.Context, box exedb.Box, newOwnerID string) error {
	// Look up shares before the transaction so we can notify the proxy after.
	individualShares, _ := withRxRes1(s, ctx, (*exedb.Queries).GetBoxSharesByBoxID, int64(box.ID))
	teamShares, _ := withRxRes1(s, ctx, (*exedb.Queries).GetBoxTeamSharesByBoxID, int64(box.ID))

	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.UpdateBoxOwner(ctx, exedb.UpdateBoxOwnerParams{
			CreatedByUserID: newOwnerID,
			ID:              box.ID,
		}); err != nil {
			return fmt.Errorf("updating box owner: %w", err)
		}
		if err := queries.UpdateBoxIPShardUser(ctx, exedb.UpdateBoxIPShardUserParams{
			UserID: newOwnerID,
			BoxID:  box.ID,
		}); err != nil {
			return fmt.Errorf("updating box IP shard user: %w", err)
		}
		if err := queries.DeleteBoxSharesByBox(ctx, int64(box.ID)); err != nil {
			return fmt.Errorf("deleting individual shares: %w", err)
		}
		for _, ts := range teamShares {
			if err := queries.DeleteBoxTeamShare(ctx, exedb.DeleteBoxTeamShareParams{
				BoxID:  ts.BoxID,
				TeamID: ts.TeamID,
			}); err != nil {
				return fmt.Errorf("deleting team share: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Notify proxy to evict cached data for this box.
	proxyChangeDeletedBox(box.Name)
	for _, share := range individualShares {
		proxyChangeDeletedBoxShare(box.Name, share.SharedWithUserID)
	}
	for _, ts := range teamShares {
		proxyChangeDeletedBoxShareTeam(ts.TeamID, int(ts.BoxID), box.Name)
	}

	return nil
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

// createPendingTeamInvite creates a pending invite and sends an email.
// If userExists is true, the email directs the user to accept via their profile page;
// otherwise it directs them to sign up via the invite link.
func (s *Server) createPendingTeamInvite(ctx context.Context, teamID, teamName, invitedEmail, invitedByUserID string, userExists bool) error {
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
		ExpiresAt:       time.Now().Add(24 * time.Hour),
		AuthProvider:    authProvider,
	})
	if err != nil {
		return fmt.Errorf("failed to create pending team invite: %w", err)
	}

	// Send invite email — wording differs for existing vs new users.
	subject := fmt.Sprintf("You've been invited to %s on %s", teamName, s.env.WebHost)
	var link, body string
	if userExists {
		link = fmt.Sprintf("https://%s/user", s.env.WebHost)
		body = fmt.Sprintf(`Hello,

You've been invited to join the team "%s" on %s.

You can review and accept this invite from your profile:

%s

Note: accepting will make your existing VMs visible to team admins.

This invite expires in 24 hours.

---
%s`, teamName, s.env.WebHost, link, s.env.WebHost)
	} else {
		link = fmt.Sprintf("https://%s/auth?team_invite=%s", s.env.WebHost, token)
		body = fmt.Sprintf(`Hello,

You've been invited to join the team "%s" on %s.

Click below to create your account and join the team:

%s

This invite expires in 24 hours.

---
%s`, teamName, s.env.WebHost, link, s.env.WebHost)
	}

	if err := s.sendEmail(ctx, sendEmailParams{
		emailType: email.TypeTeamInvitation,
		to:        invitedEmail,
		subject:   subject,
		body:      body,
		fromName:  "",
		replyTo:   "",
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

// handleTeamInviteAccept handles POST /team/invite/accept.
// The logged-in user accepts a pending team invite by token.
func (s *Server) handleTeamInviteAccept(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing invite token", http.StatusBadRequest)
		return
	}

	// Look up the invite
	invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingTeamInviteByToken, token)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Invite not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to look up team invite", "error", err, "token", token)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Verify the invite is for this user
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ce := canonicalizeEmail(user.Email)
	if ce != invite.CanonicalEmail {
		http.Error(w, "This invite is not for your account", http.StatusForbidden)
		return
	}

	// Check user isn't already in a team
	existingTeam, _ := s.GetTeamForUser(ctx, userID)
	if existingTeam != nil {
		http.Error(w, "You are already in a team", http.StatusConflict)
		return
	}

	// Add user to team
	err = withTx1(s, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: invite.TeamID,
		UserID: userID,
		Role:   "user",
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to add user to team", "error", err, "team_id", invite.TeamID, "user_id", userID)
		http.Error(w, "Failed to join team", http.StatusInternalServerError)
		return
	}

	s.resolveTeamShardCollisions(ctx, invite.TeamID, userID)

	// Mark invite as accepted
	if err := withTx1(s, ctx, (*exedb.Queries).MarkPendingTeamInviteAccepted, exedb.MarkPendingTeamInviteAcceptedParams{
		AcceptedByUserID: &userID,
		ID:               invite.ID,
	}); err != nil {
		s.slog().ErrorContext(ctx, "failed to mark invite accepted", "error", err, "invite_id", invite.ID)
	}

	slog.InfoContext(ctx, "user accepted team invite",
		"team_id", invite.TeamID, "user_id", userID, "email", user.Email)

	http.Redirect(w, r, "/user", http.StatusSeeOther)
}

// handleTeamInviteDecline handles POST /team/invite/decline.
// The logged-in user declines a pending team invite by token.
func (s *Server) handleTeamInviteDecline(w http.ResponseWriter, r *http.Request, userID string) {
	ctx := r.Context()
	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing invite token", http.StatusBadRequest)
		return
	}

	// Look up the invite
	invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingTeamInviteByToken, token)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Invite not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to look up team invite", "error", err, "token", token)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Verify the invite is for this user
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ce := canonicalizeEmail(user.Email)
	if ce != invite.CanonicalEmail {
		http.Error(w, "This invite is not for your account", http.StatusForbidden)
		return
	}

	// Delete the invite
	if err := withTx1(s, ctx, (*exedb.Queries).DeletePendingTeamInvite, token); err != nil {
		s.slog().ErrorContext(ctx, "failed to delete team invite", "error", err, "token", token)
		http.Error(w, "Failed to decline invite", http.StatusInternalServerError)
		return
	}

	slog.InfoContext(ctx, "user declined team invite",
		"team_id", invite.TeamID, "user_id", userID, "email", user.Email)

	http.Redirect(w, r, "/user", http.StatusSeeOther)
}

// EnableTeam creates a new team with the given display name and adds the user as billing_owner.
// Returns the generated team ID on success.
func (s *Server) EnableTeam(ctx context.Context, userID, displayName string) (string, error) {
	// Verify not already in a team
	team, err := s.GetTeamForUser(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("check existing team: %w", err)
	}
	if team != nil {
		return "", fmt.Errorf("already in a team: %s", team.DisplayName)
	}

	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return "", fmt.Errorf("team name cannot be empty")
	}

	teamID, err := generateID("tm_")
	if err != nil {
		return "", fmt.Errorf("generate team ID: %w", err)
	}

	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.InsertTeam(ctx, exedb.InsertTeamParams{
			TeamID:      teamID,
			DisplayName: displayName,
		}); err != nil {
			return fmt.Errorf("insert team: %w", err)
		}
		if err := queries.InsertTeamMember(ctx, exedb.InsertTeamMemberParams{
			TeamID: teamID,
			UserID: userID,
			Role:   "billing_owner",
		}); err != nil {
			return fmt.Errorf("insert member: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	slog.InfoContext(ctx, "team created",
		"team_id", teamID,
		"display_name", displayName,
		"user_id", userID)

	return teamID, nil
}

// DisableTeam disbands the user's team, cascade-deleting all related data.
// The user must be the billing_owner and the only remaining member.
func (s *Server) DisableTeam(ctx context.Context, userID string) error {
	team, err := s.GetTeamForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get team: %w", err)
	}
	if team == nil {
		return fmt.Errorf("not part of a team")
	}
	if team.Role != "billing_owner" {
		return fmt.Errorf("only billing owners can disable teams")
	}

	members, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamMembers, team.TeamID)
	if err != nil {
		return fmt.Errorf("get members: %w", err)
	}
	if len(members) > 1 {
		return fmt.Errorf("team still has %d other member(s)", len(members)-1)
	}

	teamID := team.TeamID

	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.DeleteBoxTeamSharesByTeamID(ctx, teamID); err != nil {
			return fmt.Errorf("delete team shares: %w", err)
		}
		if err := queries.DeletePendingTeamInvitesByTeamID(ctx, teamID); err != nil {
			return fmt.Errorf("delete pending invites: %w", err)
		}
		if err := queries.DeleteTeamSSOProvider(ctx, teamID); err != nil {
			return fmt.Errorf("delete SSO provider: %w", err)
		}
		if err := queries.SetTeamAuthProvider(ctx, exedb.SetTeamAuthProviderParams{
			AuthProvider: nil,
			TeamID:       teamID,
		}); err != nil {
			return fmt.Errorf("clear auth provider: %w", err)
		}
		if err := queries.DeleteTeamMember(ctx, exedb.DeleteTeamMemberParams{
			TeamID: teamID,
			UserID: userID,
		}); err != nil {
			return fmt.Errorf("delete self from team: %w", err)
		}
		if err := queries.DeleteTeam(ctx, teamID); err != nil {
			return fmt.Errorf("delete team: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	proxyChangeDeletedTeamMember(teamID, userID)

	slog.InfoContext(ctx, "team disabled",
		"team_id", teamID,
		"display_name", team.DisplayName,
		"user_id", userID)

	return nil
}

// handleTeamEnable handles POST /team/enable from the web UI.
func (s *Server) handleTeamEnable(w http.ResponseWriter, r *http.Request, userID string) {
	// Re-check billing eligibility at POST time (page may have been open for hours)
	if !s.env.SkipBilling {
		billingStatus, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetUserBillingStatus, userID)
		if err != nil || userNeedsBilling(&billingStatus) {
			writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "Active billing is required to create a team."})
			return
		}
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid request"})
		return
	}

	teamID, err := s.EnableTeam(r.Context(), userID, req.Name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "team_id": teamID})
}

// handleTeamDisable handles POST /team/disable from the web UI.
func (s *Server) handleTeamDisable(w http.ResponseWriter, r *http.Request, userID string) {
	var req struct {
		ConfirmName string `json:"confirm_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid request"})
		return
	}

	// Verify the typed name matches
	team, err := s.GetTeamForUser(r.Context(), userID)
	if err != nil || team == nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "You are not part of a team"})
		return
	}
	if req.ConfirmName != team.DisplayName {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": "Team name does not match."})
		return
	}

	if err := s.DisableTeam(r.Context(), userID); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
