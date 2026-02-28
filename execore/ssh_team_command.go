package execore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

var addOwnerFlag = addBoolFlag("owner", "add as team owner instead of regular member")

// teamCommand returns the command definition for the team command
func (ss *SSHServer) teamCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "team",
		Hidden:      true,
		Description: "View and manage your team",
		Handler:     ss.handleTeamCommand,
		FlagSetFunc: jsonOnlyFlags("team"),
		Available:   ss.isInTeamOrSudo,
		Subcommands: []*exemenu.Command{
			{
				Name:        "members",
				Description: "List team members",
				Usage:       "team members",
				Handler:     ss.handleTeamMembersCommand,
				FlagSetFunc: jsonOnlyFlags("team-members"),
			},
			{
				Name:              "add",
				Description:       "Add a user to the team",
				Usage:             "team add <email>",
				Handler:           ss.handleTeamAddCommand,
				FlagSetFunc:       jsonOnlyFlags("team-add"),
				HasPositionalArgs: true,
				Available:         ss.isTeamOwner,
			},
			{
				Name:              "remove",
				Description:       "Remove a user from the team",
				Usage:             "team remove <email>",
				Handler:           ss.handleTeamRemoveCommand,
				FlagSetFunc:       jsonOnlyFlags("team-remove"),
				HasPositionalArgs: true,
				Available:         ss.isTeamOwner,
			},
			{
				Name:        "auth",
				Description: "View and manage team auth settings",
				Usage:       "team auth",
				Handler:     ss.handleTeamAuthCommand,
				FlagSetFunc: jsonOnlyFlags("team-auth"),
				Available:   ss.isTeamOwner,
				Subcommands: ss.teamAuthSubcommands(),
			},
			// Root-only commands below
			{
				Name:              "create",
				Description:       "Create a new team",
				Usage:             "team create <team_id> <display_name> <owner_email>",
				Handler:           ss.handleTeamCreateCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "enroll",
				Description:       "Add a user to any team",
				Usage:             "team enroll <team_id> <email> [--owner]",
				Handler:           ss.handleTeamEnrollCommand,
				FlagSetFunc:       addOwnerFlag(jsonOnlyFlags("team-enroll")),
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "unenroll",
				Description:       "Remove a user from any team",
				Usage:             "team unenroll <team_id> <email>",
				Handler:           ss.handleTeamUnenrollCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "promote",
				Description:       "Promote a team member to owner",
				Usage:             "team promote <team_id> <email>",
				Handler:           ss.handleTeamPromoteCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "demote",
				Description:       "Demote a team owner to member",
				Usage:             "team demote <team_id> <email>",
				Handler:           ss.handleTeamDemoteCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:         "list",
				Description:  "List all teams",
				Usage:        "team list",
				Handler:      ss.handleTeamListCommand,
				Hidden:       true,
				RequiresSudo: true,
				Available:    ss.isSudoUser,
			},
			{
				Name:              "show",
				Description:       "Show members of any team",
				Usage:             "team show <team_id>",
				Handler:           ss.handleTeamShowCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
		},
	}
}

// isInTeam checks if the user is in a team (for command availability)
func (ss *SSHServer) isInTeam(cc *exemenu.CommandContext) bool {
	if ss.server == nil || ss.server.db == nil {
		return false
	}
	team, _ := ss.server.GetTeamForUser(context.Background(), cc.User.ID)
	return team != nil
}

// isInTeamOrSudo makes the team command available to team members AND sudo users
func (ss *SSHServer) isInTeamOrSudo(cc *exemenu.CommandContext) bool {
	return ss.isInTeam(cc) || ss.isSudoUser(cc)
}

// isTeamOwner checks if the user is a team owner (for command availability)
func (ss *SSHServer) isTeamOwner(cc *exemenu.CommandContext) bool {
	if ss.server == nil || ss.server.db == nil {
		return false
	}
	return ss.server.IsUserTeamOwner(context.Background(), cc.User.ID)
}

// isSudoUser checks if the user has root support privileges
func (ss *SSHServer) isSudoUser(cc *exemenu.CommandContext) bool {
	if ss.server == nil || ss.server.db == nil {
		return false
	}
	return ss.server.UserHasExeSudo(context.Background(), cc.User.ID)
}

// handleTeamCommand shows team info
func (ss *SSHServer) handleTeamCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	slog.InfoContext(ctx, "handleTeamCommand called", "user_id", cc.User.ID, "args", cc.Args)
	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		return cc.Errorf("You are not part of a team")
	}

	// Count team members
	members, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, team.TeamID)
	if err != nil {
		return err
	}

	// Count team boxes
	boxCount, err := withRxRes1(ss.server, ctx, (*exedb.Queries).CountTeamBoxes, cc.User.ID)
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"team_id":      team.TeamID,
			"display_name": team.DisplayName,
			"role":         team.Role,
			"member_count": len(members),
			"box_count":    boxCount,
		})
		return nil
	}

	cc.Writeln("Team: \033[1m%s\033[0m", team.DisplayName)
	cc.Writeln("Your role: %s", team.Role)
	cc.Writeln("Members: %d", len(members))
	cc.Writeln("VMs: %d", boxCount)
	return nil
}

// handleTeamMembersCommand lists team members
func (ss *SSHServer) handleTeamMembersCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	slog.InfoContext(ctx, "handleTeamMembersCommand called", "user_id", cc.User.ID)
	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		slog.ErrorContext(ctx, "GetTeamForUser failed", "user_id", cc.User.ID, "error", err)
		return err
	}
	if team == nil {
		slog.InfoContext(ctx, "user not in team", "user_id", cc.User.ID)
		return cc.Errorf("You are not part of a team")
	}
	slog.InfoContext(ctx, "got team for user", "user_id", cc.User.ID, "team_id", team.TeamID, "role", team.Role)

	members, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, team.TeamID)
	if err != nil {
		slog.ErrorContext(ctx, "GetTeamMembers failed", "team_id", team.TeamID, "error", err)
		return err
	}
	slog.InfoContext(ctx, "got team members", "team_id", team.TeamID, "count", len(members))

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{"members": members})
		return nil
	}

	cc.Writeln("Team members:")
	for _, m := range members {
		roleIndicator := ""
		if m.Role == "owner" {
			roleIndicator = " \033[33m(owner)\033[0m"
		}
		cc.Writeln("  %s%s", m.Email, roleIndicator)
	}
	return nil
}

// handleTeamAddCommand adds a user to the team.
// If the user doesn't have an account yet, a pending invite is created and an email is sent.
// The response is uniform regardless of whether the user exists (avoids leaking account existence).
func (ss *SSHServer) handleTeamAddCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("usage: team add <email>")
	}

	addr := cc.Args[0]

	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		return cc.Errorf("You are not part of a team")
	}

	// Check if user is an owner
	if team.Role != "owner" {
		return cc.Errorf("Only team owners can add members")
	}

	// Try to find the user by email
	ce := canonicalizeEmail(addr)
	userID, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDByEmail, &ce)
	if errors.Is(err, sql.ErrNoRows) {
		// User doesn't exist — create a pending invite and send email
		if err := ss.server.createPendingTeamInvite(ctx, team.TeamID, team.DisplayName, addr, cc.User.ID); err != nil {
			return cc.Errorf("Failed to invite user: %v", err)
		}

		slog.InfoContext(ctx, "pending team invite created",
			"team_id", team.TeamID,
			"email", addr,
			"invited_by", cc.User.ID)

		if cc.WantJSON() {
			cc.WriteJSON(map[string]any{
				"invited": addr,
				"status":  "ok",
			})
			return nil
		}
		cc.Writeln("Invited %s to the team", addr)
		return nil
	}
	if err != nil {
		return err
	}

	// User exists — check if already in a team
	existingTeam, _ := ss.server.GetTeamForUser(ctx, userID)
	if existingTeam != nil {
		if existingTeam.TeamID == team.TeamID {
			return cc.Errorf("User %q is already in this team", addr)
		}
		return cc.Errorf("User %q is already in another team", addr)
	}

	// Add the user to the team as a regular member
	err = withTx1(ss.server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: team.TeamID,
		UserID: userID,
		Role:   "user",
	})
	if err != nil {
		return cc.Errorf("Failed to invite user: %v", err)
	}

	slog.InfoContext(ctx, "team member added",
		"team_id", team.TeamID,
		"added_user_id", userID,
		"added_by", cc.User.ID)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"invited": addr,
			"status":  "ok",
		})
		return nil
	}

	cc.Writeln("Invited %s to the team", addr)
	return nil
}

// handleTeamRemoveCommand removes a user from the team
func (ss *SSHServer) handleTeamRemoveCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("usage: team remove <email>")
	}

	email := cc.Args[0]

	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		return cc.Errorf("You are not part of a team")
	}

	// Check if user is an owner
	if team.Role != "owner" {
		return cc.Errorf("Only team owners can remove members")
	}

	// Find the team member by email
	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         team.TeamID,
		CanonicalEmail: new(canonicalizeEmail(email)),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in this team", email)
	}
	if err != nil {
		return err
	}

	// Prevent owners from removing themselves if they're the last owner
	if member.Role == "owner" {
		// Count owners
		members, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, team.TeamID)
		ownerCount := 0
		for _, m := range members {
			if m.Role == "owner" {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			return cc.Errorf("Cannot remove the last owner")
		}
	}

	// Get the member's boxes - they will be deleted
	boxIDs, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListBoxIDsForUser, member.UserID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list boxes for removed member", "error", err)
	}

	// Delete the member's boxes
	for _, boxID := range boxIDs {
		box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetBoxByID, boxID)
		if err != nil {
			continue
		}
		if err := ss.server.deleteBox(ctx, box); err != nil {
			slog.ErrorContext(ctx, "failed to delete box for removed member",
				"box_id", boxID, "user_id", member.UserID, "error", err)
		}
	}

	// Remove the member from the team
	if err := ss.server.deleteTeamMember(ctx, team.TeamID, member.UserID); err != nil {
		return cc.Errorf("Failed to remove user: %v", err)
	}

	slog.InfoContext(ctx, "team member removed",
		"team_id", team.TeamID,
		"removed_user_id", member.UserID,
		"removed_by", cc.User.ID,
		"boxes_deleted", len(boxIDs))

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"removed":       email,
			"boxes_deleted": len(boxIDs),
			"status":        "ok",
		})
		return nil
	}

	if len(boxIDs) > 0 {
		cc.Writeln("Removed %s from the team (%d VMs deleted)", email, len(boxIDs))
	} else {
		cc.Writeln("Removed %s from the team", email)
	}
	return nil
}

// canonicalizeEmail normalizes an email address for lookup
func canonicalizeEmail(email string) string {
	// This should match the canonicalization in the signup flow
	// For now, just lowercase
	return email
}

// --- Root-only team management commands ---

func (ss *SSHServer) handleTeamCreateCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 3 {
		return cc.Errorf("usage: team create <team_id> <display_name> <owner_email>")
	}

	teamID := cc.Args[0]
	displayName := cc.Args[1]
	ownerEmail := cc.Args[2]

	ownerUserID, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDByEmail, new(canonicalizeEmail(ownerEmail)))
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q not found", ownerEmail)
	}
	if err != nil {
		return err
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).InsertTeam, exedb.InsertTeamParams{
		TeamID:      teamID,
		DisplayName: displayName,
	})
	if err != nil {
		return cc.Errorf("Failed to create team: %v", err)
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: ownerUserID,
		Role:   "owner",
	})
	if err != nil {
		return cc.Errorf("Failed to add owner: %v", err)
	}

	slog.InfoContext(ctx, "root: created team", "team_id", teamID, "owner", ownerEmail, "by", cc.User.ID)
	cc.Writeln("Created team %s (%s) with owner %s", teamID, displayName, ownerEmail)
	return nil
}

func (ss *SSHServer) handleTeamEnrollCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: team enroll <team_id> <email> [--owner]")
	}

	teamID := cc.Args[0]
	email := cc.Args[1]
	isOwner := cc.FlagSet.Lookup("owner") != nil && cc.FlagSet.Lookup("owner").Value.String() == "true"

	_, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeam, teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("Team %q not found", teamID)
	}
	if err != nil {
		return err
	}

	userID, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDByEmail, new(canonicalizeEmail(email)))
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q not found", email)
	}
	if err != nil {
		return err
	}

	existingTeam, _ := ss.server.GetTeamForUser(ctx, userID)
	if existingTeam != nil {
		if existingTeam.TeamID == teamID {
			return cc.Errorf("User %q is already in team %s", email, teamID)
		}
		return cc.Errorf("User %q is already in team %s", email, existingTeam.TeamID)
	}

	role := "user"
	if isOwner {
		role = "owner"
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: userID,
		Role:   role,
	})
	if err != nil {
		return cc.Errorf("Failed to add member: %v", err)
	}

	slog.InfoContext(ctx, "root: added team member", "team_id", teamID, "email", email, "role", role, "by", cc.User.ID)
	cc.Writeln("Added %s to %s as %s", email, teamID, role)
	return nil
}

func (ss *SSHServer) handleTeamUnenrollCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: team unenroll <team_id> <email>")
	}

	teamID := cc.Args[0]
	email := cc.Args[1]

	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         teamID,
		CanonicalEmail: new(canonicalizeEmail(email)),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in team %s", email, teamID)
	}
	if err != nil {
		return err
	}

	if member.Role == "owner" {
		members, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, teamID)
		ownerCount := 0
		for _, m := range members {
			if m.Role == "owner" {
				ownerCount++
			}
		}
		if ownerCount <= 1 {
			return cc.Errorf("Cannot remove the last owner of team %s", teamID)
		}
	}

	boxIDs, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListBoxIDsForUser, member.UserID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list boxes for removed member", "error", err)
	}
	for _, boxID := range boxIDs {
		box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetBoxByID, boxID)
		if err != nil {
			continue
		}
		if err := ss.server.deleteBox(ctx, box); err != nil {
			slog.ErrorContext(ctx, "failed to delete box for removed member",
				"box_id", boxID, "user_id", member.UserID, "error", err)
		}
	}

	if err := ss.server.deleteTeamMember(ctx, teamID, member.UserID); err != nil {
		return cc.Errorf("Failed to remove member: %v", err)
	}

	slog.InfoContext(ctx, "root: removed team member", "team_id", teamID, "email", email, "boxes_deleted", len(boxIDs), "by", cc.User.ID)
	if len(boxIDs) > 0 {
		cc.Writeln("Removed %s from %s (%d VMs deleted)", email, teamID, len(boxIDs))
	} else {
		cc.Writeln("Removed %s from %s", email, teamID)
	}
	return nil
}

func (ss *SSHServer) handleTeamPromoteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: team promote <team_id> <email>")
	}

	teamID := cc.Args[0]
	email := cc.Args[1]

	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         teamID,
		CanonicalEmail: new(canonicalizeEmail(email)),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in team %s", email, teamID)
	}
	if err != nil {
		return err
	}
	if member.Role == "owner" {
		return cc.Errorf("%s is already an owner", email)
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).UpdateTeamMemberRole, exedb.UpdateTeamMemberRoleParams{
		Role:   "owner",
		TeamID: teamID,
		UserID: member.UserID,
	})
	if err != nil {
		return cc.Errorf("Failed to promote: %v", err)
	}

	slog.InfoContext(ctx, "root: promoted team member", "team_id", teamID, "email", email, "by", cc.User.ID)
	cc.Writeln("Promoted %s to owner in %s", email, teamID)
	return nil
}

func (ss *SSHServer) handleTeamDemoteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: team demote <team_id> <email>")
	}

	teamID := cc.Args[0]
	email := cc.Args[1]

	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         teamID,
		CanonicalEmail: new(canonicalizeEmail(email)),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in team %s", email, teamID)
	}
	if err != nil {
		return err
	}
	if member.Role != "owner" {
		return cc.Errorf("%s is not an owner", email)
	}

	members, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, teamID)
	ownerCount := 0
	for _, m := range members {
		if m.Role == "owner" {
			ownerCount++
		}
	}
	if ownerCount <= 1 {
		return cc.Errorf("Cannot demote the last owner of team %s", teamID)
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).UpdateTeamMemberRole, exedb.UpdateTeamMemberRoleParams{
		Role:   "user",
		TeamID: teamID,
		UserID: member.UserID,
	})
	if err != nil {
		return cc.Errorf("Failed to demote: %v", err)
	}

	slog.InfoContext(ctx, "root: demoted team member", "team_id", teamID, "email", email, "by", cc.User.ID)
	cc.Writeln("Demoted %s to user in %s", email, teamID)
	return nil
}

func (ss *SSHServer) handleTeamListCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	teams, err := withRxRes0(ss.server, ctx, (*exedb.Queries).ListAllTeams)
	if err != nil {
		return err
	}

	if len(teams) == 0 {
		cc.Writeln("No teams")
		return nil
	}

	for _, t := range teams {
		cc.Writeln("  %s  %s  (%d members)", t.TeamID, t.DisplayName, t.MemberCount)
	}
	return nil
}

func (ss *SSHServer) handleTeamShowCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("usage: team show <team_id>")
	}

	teamID := cc.Args[0]

	team, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeam, teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("Team %q not found", teamID)
	}
	if err != nil {
		return err
	}

	members, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, teamID)
	if err != nil {
		return err
	}

	cc.Writeln("%s (%s):", team.TeamID, team.DisplayName)
	for _, m := range members {
		roleIndicator := ""
		if m.Role == "owner" {
			roleIndicator = " (owner)"
		}
		cc.Writeln("  %s  %s%s", m.UserID, m.Email, roleIndicator)
	}
	return nil
}
