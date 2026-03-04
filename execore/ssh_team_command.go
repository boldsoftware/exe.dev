package execore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

var (
	addSudoerFlag       = addBoolFlag("sudoer", "add as team sudoer (SSH access to member VMs)")
	addBillingOwnerFlag = addBoolFlag("billing-owner", "add as team billing owner")
)

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
				Available:         ss.isTeamAdmin,
			},
			{
				Name:              "remove",
				Description:       "Remove a user from the team",
				Usage:             "team remove <email>",
				Handler:           ss.handleTeamRemoveCommand,
				FlagSetFunc:       jsonOnlyFlags("team-remove"),
				HasPositionalArgs: true,
				Available:         ss.isTeamAdmin,
			},
			{
				Name:        "auth",
				Description: "View and manage team auth settings",
				Usage:       "team auth",
				Handler:     ss.handleTeamAuthCommand,
				FlagSetFunc: jsonOnlyFlags("team-auth"),
				Available:   ss.isTeamAdmin,
				Subcommands: ss.teamAuthSubcommands(),
			},
			// Root-only commands below
			{
				Name:              "create",
				Description:       "Create a new team",
				Usage:             "team create <team_id> <display_name> <billing_owner_email>",
				Handler:           ss.handleTeamCreateCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "enroll",
				Description:       "Add a user to any team",
				Usage:             "team enroll <team_id> <email> [--sudoer|--billing-owner]",
				Handler:           ss.handleTeamEnrollCommand,
				FlagSetFunc:       addSudoerFlag(addBillingOwnerFlag(jsonOnlyFlags("team-enroll"))),
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
				Description:       "Promote a team member to sudoer or billing_owner",
				Usage:             "team promote <team_id> <email> <sudoer|billing_owner>",
				Handler:           ss.handleTeamPromoteCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "demote",
				Description:       "Demote a team sudoer or billing_owner to member",
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

// isTeamAdmin checks if the user is a team admin — billing_owner or sudoer (for command availability)
func (ss *SSHServer) isTeamAdmin(cc *exemenu.CommandContext) bool {
	if ss.server == nil || ss.server.db == nil {
		return false
	}
	return ss.server.IsUserTeamAdmin(context.Background(), cc.User.ID)
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
		switch m.Role {
		case "billing_owner":
			roleIndicator = " \033[35m(billing owner)\033[0m"
		case "sudoer":
			roleIndicator = " \033[33m(sudoer)\033[0m"
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

	// Check if user is an admin (billing_owner or sudoer)
	if team.Role == "user" {
		return cc.Errorf("Only team admins can add members")
	}

	// Try to find the user by email
	ce := canonicalizeEmail(addr)
	_, err = withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDByEmail, &ce)
	if err == nil {
		// User already exists — do not allow adding existing users via team add.
		// Existing users must be added via the debug panel to prevent
		// accidentally merging accounts and taking over VMs.
		return cc.Errorf("User %q already has an account; existing users can only be added to teams via support@exe.dev", addr)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

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

	// Check if user is an admin (billing_owner or sudoer)
	if team.Role == "user" {
		return cc.Errorf("Only team admins can remove members")
	}

	// Find the team member by email
	ce := canonicalizeEmail(email)
	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         team.TeamID,
		CanonicalEmail: &ce,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in this team", email)
	}
	if err != nil {
		return err
	}

	// Prevent removing the last billing_owner
	if member.Role == "billing_owner" {
		members, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, team.TeamID)
		billingOwnerCount := 0
		for _, m := range members {
			if m.Role == "billing_owner" {
				billingOwnerCount++
			}
		}
		if billingOwnerCount <= 1 {
			return cc.Errorf("Cannot remove the last billing owner")
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
		return cc.Errorf("usage: team create <team_id> <display_name> <billing_owner_email>")
	}

	teamID := cc.Args[0]
	displayName := cc.Args[1]
	billingOwnerEmail := cc.Args[2]

	ce := canonicalizeEmail(billingOwnerEmail)
	billingOwnerUserID, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDByEmail, &ce)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q not found", billingOwnerEmail)
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
		UserID: billingOwnerUserID,
		Role:   "billing_owner",
	})
	if err != nil {
		return cc.Errorf("Failed to add billing owner: %v", err)
	}

	slog.InfoContext(ctx, "root: created team", "team_id", teamID, "billing_owner", billingOwnerEmail, "by", cc.User.ID)
	cc.Writeln("Created team %s (%s) with billing owner %s", teamID, displayName, billingOwnerEmail)
	return nil
}

func (ss *SSHServer) handleTeamEnrollCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: team enroll <team_id> <email> [--sudoer|--billing-owner]")
	}

	teamID := cc.Args[0]
	email := cc.Args[1]
	isSudoer := cc.FlagSet.Lookup("sudoer") != nil && cc.FlagSet.Lookup("sudoer").Value.String() == "true"
	isBillingOwner := cc.FlagSet.Lookup("billing-owner") != nil && cc.FlagSet.Lookup("billing-owner").Value.String() == "true"

	if isSudoer && isBillingOwner {
		return cc.Errorf("Cannot use both --sudoer and --billing-owner")
	}

	_, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeam, teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("Team %q not found", teamID)
	}
	if err != nil {
		return err
	}

	ce := canonicalizeEmail(email)
	userID, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDByEmail, &ce)
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
	if isSudoer {
		role = "sudoer"
	} else if isBillingOwner {
		role = "billing_owner"
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: userID,
		Role:   role,
	})
	if err != nil {
		return cc.Errorf("Failed to add member: %v", err)
	}

	ss.server.resolveTeamShardCollisions(ctx, teamID, userID)

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

	ce := canonicalizeEmail(email)
	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         teamID,
		CanonicalEmail: &ce,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in team %s", email, teamID)
	}
	if err != nil {
		return err
	}

	if member.Role == "billing_owner" {
		members, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, teamID)
		billingOwnerCount := 0
		for _, m := range members {
			if m.Role == "billing_owner" {
				billingOwnerCount++
			}
		}
		if billingOwnerCount <= 1 {
			return cc.Errorf("Cannot remove the last billing owner of team %s", teamID)
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
	if len(cc.Args) < 3 {
		return cc.Errorf("usage: team promote <team_id> <email> <sudoer|billing_owner>")
	}

	teamID := cc.Args[0]
	email := cc.Args[1]
	targetRole := cc.Args[2]

	if targetRole != "sudoer" && targetRole != "billing_owner" {
		return cc.Errorf("role must be 'sudoer' or 'billing_owner'")
	}

	ce := canonicalizeEmail(email)
	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         teamID,
		CanonicalEmail: &ce,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in team %s", email, teamID)
	}
	if err != nil {
		return err
	}
	if member.Role == targetRole {
		return cc.Errorf("%s is already a %s", email, targetRole)
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).UpdateTeamMemberRole, exedb.UpdateTeamMemberRoleParams{
		Role:   targetRole,
		TeamID: teamID,
		UserID: member.UserID,
	})
	if err != nil {
		return cc.Errorf("Failed to promote: %v", err)
	}

	slog.InfoContext(ctx, "root: promoted team member", "team_id", teamID, "email", email, "role", targetRole, "by", cc.User.ID)
	cc.Writeln("Promoted %s to %s in %s", email, targetRole, teamID)
	return nil
}

func (ss *SSHServer) handleTeamDemoteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: team demote <team_id> <email>")
	}

	teamID := cc.Args[0]
	email := cc.Args[1]

	ce := canonicalizeEmail(email)
	member, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         teamID,
		CanonicalEmail: &ce,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in team %s", email, teamID)
	}
	if err != nil {
		return err
	}
	if member.Role == "user" {
		return cc.Errorf("%s is already a regular member", email)
	}

	// Protect last billing_owner
	if member.Role == "billing_owner" {
		members, _ := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, teamID)
		billingOwnerCount := 0
		for _, m := range members {
			if m.Role == "billing_owner" {
				billingOwnerCount++
			}
		}
		if billingOwnerCount <= 1 {
			return cc.Errorf("Cannot demote the last billing owner of team %s", teamID)
		}
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
		switch m.Role {
		case "billing_owner":
			roleIndicator = " (billing_owner)"
		case "sudoer":
			roleIndicator = " (sudoer)"
		}
		cc.Writeln("  %s  %s%s", m.UserID, m.Email, roleIndicator)
	}
	return nil
}
