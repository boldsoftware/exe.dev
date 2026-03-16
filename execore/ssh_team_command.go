package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

var (
	addAdminFlag        = addBoolFlag("admin", "add as team admin (SSH access to member VMs)")
	addBillingOwnerFlag = addBoolFlag("billing-owner", "add as team billing owner")
	addForceFlag        = addBoolFlag("force", "force operation even if user has VMs")
)

// teamCommand returns the command definition for the team command
func (ss *SSHServer) teamCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "team",
		Hidden:      true,
		Description: "View and manage your team",
		Handler:     ss.handleTeamCommand,
		FlagSetFunc: jsonOnlyFlags("team"),
		Available:   ss.isInTeamOrSudoOrCanEnable,
		Subcommands: []*exemenu.Command{
			{
				Name:        "enable",
				Description: "Create a new team",
				Usage:       "team enable",
				Handler:     ss.handleTeamEnableCommand,
				Available:   ss.isNotInTeamWithBilling,
			},
			{
				Name:        "disable",
				Description: "Disband your team",
				Usage:       "team disable",
				Handler:     ss.handleTeamDisableCommand,
				Available:   ss.isTeamBillingOwner,
			},
			{
				Name:        "members",
				Aliases:     []string{"ls"},
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
				Name:              "transfer",
				Description:       "Transfer a VM to another team member",
				Usage:             "team transfer <vm_name> <target_email>",
				Handler:           ss.handleTeamTransferCommand,
				FlagSetFunc:       jsonOnlyFlags("team-transfer"),
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
				Usage:             "team enroll <team_id> <email> [--admin|--billing-owner]",
				Handler:           ss.handleTeamEnrollCommand,
				FlagSetFunc:       addAdminFlag(addBillingOwnerFlag(jsonOnlyFlags("team-enroll"))),
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "unenroll",
				Description:       "Remove a user from any team",
				Usage:             "team unenroll <team_id> <email> [--force]",
				Handler:           ss.handleTeamUnenrollCommand,
				FlagSetFunc:       addForceFlag(jsonOnlyFlags("team-unenroll")),
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "promote",
				Description:       "Promote a team member to admin or billing_owner",
				Usage:             "team promote <team_id> <email> <admin|billing_owner>",
				Handler:           ss.handleTeamPromoteCommand,
				HasPositionalArgs: true,
				Hidden:            true,
				RequiresSudo:      true,
				Available:         ss.isSudoUser,
			},
			{
				Name:              "demote",
				Description:       "Demote a team admin or billing_owner to member",
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

// isInTeamOrSudoOrCanEnable makes the team command visible to team members, sudo users,
// and users who are eligible to enable teams (not in a team + active billing).
func (ss *SSHServer) isInTeamOrSudoOrCanEnable(cc *exemenu.CommandContext) bool {
	return ss.isInTeam(cc) || ss.isSudoUser(cc) || ss.isNotInTeamWithBilling(cc)
}

// isTeamAdmin checks if the user is a team admin — billing_owner or admin (for command availability)
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

// isNotInTeamWithBilling checks if user is NOT in a team and HAS active billing (for team enable)
func (ss *SSHServer) isNotInTeamWithBilling(cc *exemenu.CommandContext) bool {
	if ss.server == nil || ss.server.db == nil {
		return false
	}
	// Must not already be in a team
	team, _ := ss.server.GetTeamForUser(context.Background(), cc.User.ID)
	if team != nil {
		return false
	}
	// Must have active billing
	if ss.server.env.SkipBilling {
		return true
	}
	billingStatus, err := withRxRes1(ss.server, context.Background(), (*exedb.Queries).GetUserBillingStatus, cc.User.ID)
	if err != nil {
		return false
	}
	return !userNeedsBilling(&billingStatus)
}

// isTeamBillingOwner checks if the user is a team billing owner (for team disable)
func (ss *SSHServer) isTeamBillingOwner(cc *exemenu.CommandContext) bool {
	if ss.server == nil || ss.server.db == nil {
		return false
	}
	isOwner, err := withRxRes1(ss.server, context.Background(), (*exedb.Queries).IsUserTeamBillingOwner, cc.User.ID)
	if err != nil {
		return false
	}
	return isOwner
}

// slugifyTeamName converts a display name to a valid team slug.
// Lowercases, replaces non-alphanumeric with underscores, collapses runs, trims.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyTeamName(name string) string {
	s := strings.ToLower(name)
	s = nonAlphanumRe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	return s
}

// handleTeamEnableCommand lets a user create a new team for themselves.
func (ss *SSHServer) handleTeamEnableCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	slog.InfoContext(ctx, "handleTeamEnableCommand called", "user_id", cc.User.ID)

	// Double-check: not already in a team
	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team != nil {
		return cc.Errorf("You are already in a team: %s", team.DisplayName)
	}

	if !cc.IsInteractive() {
		return cc.Errorf("team enable requires an interactive session")
	}

	cc.Writeln("")
	cc.Writeln("Teams lets you:")
	cc.Writeln("  - Manage shared billing for your organization")
	cc.Writeln("  - Invite members and control access")
	cc.Writeln("  - SSH into team members' VMs as an admin")
	cc.Writeln("  - Share VMs across your team")
	cc.Writeln("")
	cc.Writeln("You will become the billing owner for this team.")
	cc.Writeln("Your existing VMs will become part of the team.")
	cc.Writeln("")

	cc.Write("Enable teams? (yes/no): ")
	confirm, err := cc.ReadLine()
	if err != nil {
		return err
	}
	confirm = strings.TrimSpace(strings.ToLower(confirm))
	if confirm != "yes" && confirm != "y" {
		cc.Writeln("Cancelled.")
		return nil
	}

	// Prompt for team name, retry on slug collision
	for {
		cc.Write("Team name: ")
		displayName, err := cc.ReadLine()
		if err != nil {
			return err
		}
		displayName = strings.TrimSpace(displayName)
		if displayName == "" {
			cc.Writeln("Team name cannot be empty.")
			continue
		}

		slug := slugifyTeamName(displayName)
		if slug == "" {
			cc.Writeln("Team name must contain at least one letter or number.")
			continue
		}

		teamID := "tm_" + slug

		// Create team + add self as billing_owner in one transaction
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			if err := queries.InsertTeam(ctx, exedb.InsertTeamParams{
				TeamID:      teamID,
				DisplayName: displayName,
			}); err != nil {
				return fmt.Errorf("insert team: %w", err)
			}
			if err := queries.InsertTeamMember(ctx, exedb.InsertTeamMemberParams{
				TeamID: teamID,
				UserID: cc.User.ID,
				Role:   "billing_owner",
			}); err != nil {
				return fmt.Errorf("insert member: %w", err)
			}
			return nil
		})
		if err != nil {
			// Check for unique constraint violation (slug collision)
			if strings.Contains(err.Error(), "UNIQUE constraint failed") || strings.Contains(err.Error(), "teams.team_id") {
				cc.Writeln("Team ID %q is already taken. Please choose a different name.", teamID)
				continue
			}
			return cc.Errorf("Failed to create team: %v", err)
		}

		slog.InfoContext(ctx, "team created via enable",
			"team_id", teamID,
			"display_name", displayName,
			"user_id", cc.User.ID)

		cc.Writeln("")
		cc.Writeln("Team \033[1m%s\033[0m created! (ID: %s)", displayName, teamID)
		cc.Writeln("Use \033[1mteam add <email>\033[0m to invite members.")
		return nil
	}
}

// handleTeamDisableCommand lets a billing owner disband their team.
func (ss *SSHServer) handleTeamDisableCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	slog.InfoContext(ctx, "handleTeamDisableCommand called", "user_id", cc.User.ID)

	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		return cc.Errorf("You are not part of a team")
	}
	if team.Role != "billing_owner" {
		return cc.Errorf("Only billing owners can disable teams")
	}

	// Check team has no other members
	members, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMembers, team.TeamID)
	if err != nil {
		return err
	}
	if len(members) > 1 {
		cc.Writeln("Your team still has %d other member(s).", len(members)-1)
		cc.Writeln("Remove all members with \033[1mteam remove <email>\033[0m before disabling.")
		return nil
	}

	if !cc.IsInteractive() {
		return cc.Errorf("team disable requires an interactive session")
	}

	cc.Writeln("")
	cc.Writeln("Disabling team \033[1m%s\033[0m will:", team.DisplayName)
	cc.Writeln("  - Remove all team shares")
	cc.Writeln("  - Cancel all pending invites")
	cc.Writeln("  - Remove team auth/SSO configuration")
	cc.Writeln("  - Delete the team")
	cc.Writeln("")
	cc.Writeln("Your VMs will remain on your personal account.")
	cc.Writeln("")

	cc.Write("Disable team? (yes/no): ")
	confirm, err := cc.ReadLine()
	if err != nil {
		return err
	}
	confirm = strings.TrimSpace(strings.ToLower(confirm))
	if confirm != "yes" && confirm != "y" {
		cc.Writeln("Cancelled.")
		return nil
	}

	teamID := team.TeamID

	// Delete everything in a transaction
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.DeleteBoxTeamSharesByTeamID(ctx, teamID); err != nil {
			return fmt.Errorf("delete team shares: %w", err)
		}
		if err := queries.DeletePendingTeamInvitesByTeamID(ctx, teamID); err != nil {
			return fmt.Errorf("delete pending invites: %w", err)
		}
		if err := queries.DeleteTeamSSOProvider(ctx, teamID); err != nil {
			return fmt.Errorf("delete SSO provider: %w", err)
		}
		// Clear team auth_provider column
		if err := queries.SetTeamAuthProvider(ctx, exedb.SetTeamAuthProviderParams{
			AuthProvider: nil,
			TeamID:       teamID,
		}); err != nil {
			return fmt.Errorf("clear auth provider: %w", err)
		}
		if err := queries.DeleteTeamMember(ctx, exedb.DeleteTeamMemberParams{
			TeamID: teamID,
			UserID: cc.User.ID,
		}); err != nil {
			return fmt.Errorf("delete self from team: %w", err)
		}
		if err := queries.DeleteTeam(ctx, teamID); err != nil {
			return fmt.Errorf("delete team: %w", err)
		}
		return nil
	})
	if err != nil {
		return cc.Errorf("Failed to disable team: %v", err)
	}

	// Notify proxy
	proxyChangeDeletedTeamMember(teamID, cc.User.ID)

	slog.InfoContext(ctx, "team disabled",
		"team_id", teamID,
		"display_name", team.DisplayName,
		"user_id", cc.User.ID)

	cc.Writeln("Team \033[1m%s\033[0m has been disabled.", team.DisplayName)
	return nil
}

// handleTeamCommand shows team info
func (ss *SSHServer) handleTeamCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	slog.InfoContext(ctx, "handleTeamCommand called", "user_id", cc.User.ID, "args", cc.Args)
	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		cc.Writeln("You are not part of a team.")
		cc.Writeln("Use \033[1mteam enable\033[0m to create one.")
		return nil
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
		case "admin":
			roleIndicator = " \033[33m(admin)\033[0m"
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

	// Check if user is an admin (billing_owner or admin)
	if team.Role == "user" {
		return cc.Errorf("Only team admins can add members")
	}

	// Check if the user already exists — this affects the invite email wording.
	ce := canonicalizeEmail(addr)
	_, err = withRxRes1(ss.server, ctx, (*exedb.Queries).GetUserIDByEmail, &ce)
	userExists := err == nil
	if !userExists && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// Create a pending invite and send email.
	// Existing users must explicitly accept via the web UI;
	// new users auto-join when they sign up through the invite link.
	if err := ss.server.createPendingTeamInvite(ctx, team.TeamID, team.DisplayName, addr, cc.User.ID, userExists); err != nil {
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

	// Check if user is an admin (billing_owner or admin)
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

	// Refuse to remove a member who still has VMs.
	boxIDs, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListBoxIDsForUser, member.UserID)
	if err != nil {
		return cc.Errorf("Failed to check member's VMs: %v", err)
	}
	if len(boxIDs) > 0 {
		return cc.Errorf("Cannot remove %s: they still have %d VM(s). Ask them to delete their VMs first.", email, len(boxIDs))
	}

	// Remove the member from the team
	if err := ss.server.deleteTeamMember(ctx, team.TeamID, member.UserID); err != nil {
		return cc.Errorf("Failed to remove user: %v", err)
	}

	slog.InfoContext(ctx, "team member removed",
		"team_id", team.TeamID,
		"removed_user_id", member.UserID,
		"removed_by", cc.User.ID)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"removed": email,
			"status":  "ok",
		})
		return nil
	}

	cc.Writeln("Removed %s from the team", email)
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

	teamID, err := parseTeamID(cc.Args[0])
	if err != nil {
		return cc.Errorf("%v", err)
	}
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
		return cc.Errorf("usage: team enroll <team_id> <email> [--admin|--billing-owner]")
	}

	teamID, err := parseTeamID(cc.Args[0])
	if err != nil {
		return cc.Errorf("%v", err)
	}
	email := cc.Args[1]
	isAdmin := cc.FlagSet.Lookup("admin") != nil && cc.FlagSet.Lookup("admin").Value.String() == "true"
	isBillingOwner := cc.FlagSet.Lookup("billing-owner") != nil && cc.FlagSet.Lookup("billing-owner").Value.String() == "true"

	if isAdmin && isBillingOwner {
		return cc.Errorf("Cannot use both --admin and --billing-owner")
	}

	_, err = withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeam, teamID)
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
	if isAdmin {
		role = "admin"
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

	teamID, err := parseTeamID(cc.Args[0])
	if err != nil {
		return cc.Errorf("%v", err)
	}
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

	force := cc.FlagSet.Lookup("force") != nil && cc.FlagSet.Lookup("force").Value.String() == "true"

	boxIDs, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListBoxIDsForUser, member.UserID)
	if err != nil {
		return cc.Errorf("Failed to check member's VMs: %v", err)
	}
	if len(boxIDs) > 0 && !force {
		return cc.Errorf("Cannot remove %s: they still have %d VM(s). Use --force to remove anyway (VMs will be orphaned from the team).", email, len(boxIDs))
	}

	if err := ss.server.deleteTeamMember(ctx, teamID, member.UserID); err != nil {
		return cc.Errorf("Failed to remove member: %v", err)
	}

	slog.InfoContext(ctx, "root: removed team member", "team_id", teamID, "email", email, "force", force, "by", cc.User.ID)
	cc.Writeln("Removed %s from %s", email, teamID)
	return nil
}

func (ss *SSHServer) handleTeamPromoteCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 3 {
		return cc.Errorf("usage: team promote <team_id> <email> <admin|billing_owner>")
	}

	teamID, err := parseTeamID(cc.Args[0])
	if err != nil {
		return cc.Errorf("%v", err)
	}
	email := cc.Args[1]
	targetRole := cc.Args[2]

	if targetRole != "admin" && targetRole != "billing_owner" {
		return cc.Errorf("role must be 'admin' or 'billing_owner'")
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

	teamID, err := parseTeamID(cc.Args[0])
	if err != nil {
		return cc.Errorf("%v", err)
	}
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

	teamID, err := parseTeamID(cc.Args[0])
	if err != nil {
		return cc.Errorf("%v", err)
	}

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
		case "admin":
			roleIndicator = " (admin)"
		}
		cc.Writeln("  %s  %s%s", m.UserID, m.Email, roleIndicator)
	}
	return nil
}

// handleTeamTransferCommand transfers a VM from one team member to another.
func (ss *SSHServer) handleTeamTransferCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: team transfer <vm_name> <target_email>")
	}

	boxName := cc.Args[0]
	targetEmail := cc.Args[1]

	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		return cc.Errorf("You are not part of a team")
	}
	if team.Role == "user" {
		return cc.Errorf("Only team admins can transfer VMs")
	}

	// Find the box (checks direct ownership + admin access)
	box, _, err := ss.server.FindAccessibleBox(ctx, cc.User.ID, boxName)
	if err != nil {
		return cc.Errorf("VM %q not found", boxName)
	}

	// Resolve target user — must be in the same team
	ce := canonicalizeEmail(targetEmail)
	target, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamMemberByEmail, exedb.GetTeamMemberByEmailParams{
		TeamID:         team.TeamID,
		CanonicalEmail: &ce,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("User %q is not in this team", targetEmail)
	}
	if err != nil {
		return err
	}

	// Reject no-op
	if box.CreatedByUserID == target.UserID {
		return cc.Errorf("VM %q is already owned by %s", boxName, targetEmail)
	}

	if err := ss.server.transferBox(ctx, *box, target.UserID); err != nil {
		return cc.Errorf("Failed to transfer VM: %v", err)
	}

	slog.InfoContext(ctx, "team VM transferred",
		"team_id", team.TeamID,
		"box_name", boxName,
		"from_user", box.CreatedByUserID,
		"to_user", target.UserID,
		"by", cc.User.ID)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"transferred": boxName,
			"to":          targetEmail,
			"status":      "ok",
		})
		return nil
	}

	cc.Writeln("Transferred %s to %s", boxName, targetEmail)
	return nil
}
