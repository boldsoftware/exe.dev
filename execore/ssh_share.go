package execore

// TODO(philip): consolidate into one tx per handle function,
// moving any emailing or heavy stream IO outside of the tx

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// This file contains SSH command handlers for the 'share' command.
// The share command allows users to share box access with others via email or share links.

// shareAddFlags creates a FlagSet for the share add subcommand
func shareAddFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("share-add", flag.ContinueOnError)
	fs.String("message", "", "message to include in share invitation")
	fs.Bool("json", false, "output in JSON format")
	return fs
}

// shareCommand returns the command definition for the share command
func (ss *SSHServer) shareCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "share",
		Description: "Share HTTPS box access with others",
		Usage:       "share <subcommand> <box> [args...]",
		Handler:     ss.handleShareHelp,
		FlagSetFunc: jsonOnlyFlags("share"),
		Subcommands: []*exemenu.Command{
			{
				Name:              "show",
				Description:       "Show current shares for a box",
				Usage:             "share show <box>",
				Handler:           ss.handleShareShowCmd,
				FlagSetFunc:       jsonOnlyFlags("share-show"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "port",
				Description:       "Set the HTTP proxy port for a box",
				Usage:             "share port <box> [port]",
				Handler:           ss.handleSharePortCmd,
				FlagSetFunc:       jsonOnlyFlags("share-port"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
				Examples: []string{
					"share port mybox 8080",
				},
			},
			{
				Name:              "set-public",
				Description:       "Make the HTTP proxy publicly accessible",
				Usage:             "share set-public <box>",
				Handler:           ss.handleShareSetPublicCmd,
				FlagSetFunc:       jsonOnlyFlags("share-set-public"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "set-private",
				Description:       "Restrict the HTTP proxy to authenticated users",
				Usage:             "share set-private <box>",
				Handler:           ss.handleShareSetPrivateCmd,
				FlagSetFunc:       jsonOnlyFlags("share-set-private"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "add",
				Description:       "Share box with a user via email",
				Usage:             "share add <box> <email> [--message='...']",
				Handler:           ss.handleShareAddCmd,
				FlagSetFunc:       shareAddFlags,
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
				Examples: []string{
					"share add mybox user@example.com",
					"share add mybox user@example.com --message='Check this out'",
				},
			},
			{
				Name:              "remove",
				Description:       "Revoke a user's access to a box",
				Usage:             "share remove <box> <email>",
				Handler:           ss.handleShareRemoveCmd,
				FlagSetFunc:       jsonOnlyFlags("share-remove"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "add-link",
				Aliases:           []string{"add-share-link"},
				Description:       "Create a shareable link for a box",
				Usage:             "share add-link <box>",
				Handler:           ss.handleShareAddLinkCmd,
				FlagSetFunc:       jsonOnlyFlags("share-add-link"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "remove-link",
				Aliases:           []string{"remove-share-link"},
				Description:       "Revoke a shareable link",
				Usage:             "share remove-link <box> <token>",
				Handler:           ss.handleShareRemoveLinkCmd,
				FlagSetFunc:       jsonOnlyFlags("share-remove-link"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
		},
	}
}

func (ss *SSHServer) handleShareHelp(ctx context.Context, cc *exemenu.CommandContext) error {
	// Show help for the share command
	cmd := ss.commands.FindCommand([]string{"share"})
	if cmd != nil {
		cmd.Help(cc)
	}
	return nil
}

func (ss *SSHServer) handleShareShowCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("usage: share show <box>")
	}

	boxName := cc.Args[0]

	// Get the box and verify ownership
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found or access denied", boxName)
	}
	if err != nil {
		return err
	}

	return ss.handleShareShow(ctx, cc, &box)
}

func (ss *SSHServer) handleShareShow(ctx context.Context, cc *exemenu.CommandContext, box *exedb.Box) error {
	// Get pending shares
	pendingShares, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.PendingBoxShare, error) {
		return queries.GetPendingBoxSharesByBoxID(ctx, int64(box.ID))
	})
	if err != nil {
		return err
	}

	// Get active shares
	activeShares, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.GetBoxSharesByBoxIDRow, error) {
		return queries.GetBoxSharesByBoxID(ctx, int64(box.ID))
	})
	if err != nil {
		return err
	}

	// Get share links
	shareLinks, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.BoxShareLink, error) {
		return queries.GetBoxShareLinksByBoxID(ctx, exedb.GetBoxShareLinksByBoxIDParams{
			BoxID:           int64(box.ID),
			CreatedByUserID: box.CreatedByUserID,
		})
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		route := box.GetRoute()
		// JSON output
		type userShare struct {
			Email     string `json:"email"`
			Status    string `json:"status"`
			InvitedAt string `json:"invited_at"`
		}
		type linkShare struct {
			Token      string  `json:"token"`
			CreatedAt  string  `json:"created_at"`
			UseCount   int64   `json:"use_count"`
			LastUsedAt *string `json:"last_used_at"`
		}

		var users []userShare
		for _, ps := range pendingShares {
			us := userShare{
				Email:     ps.SharedWithEmail,
				Status:    "pending",
				InvitedAt: ps.CreatedAt.Format(time.RFC3339),
			}
			users = append(users, us)
		}
		for _, as := range activeShares {
			us := userShare{
				Email:     as.SharedWithUserEmail,
				Status:    "active",
				InvitedAt: as.CreatedAt.Format(time.RFC3339),
			}
			users = append(users, us)
		}

		var links []linkShare
		for _, sl := range shareLinks {
			ls := linkShare{
				Token:     sl.ShareToken,
				CreatedAt: sl.CreatedAt.Format(time.RFC3339),
				UseCount:  *sl.UseCount,
			}
			if sl.LastUsedAt != nil {
				lua := sl.LastUsedAt.Format(time.RFC3339)
				ls.LastUsedAt = &lua
			}
			links = append(links, ls)
		}

		cc.WriteJSON(map[string]any{
			"box_name": box.Name,
			"status":   route.Share,
			"port":     route.Port,
			"url":      ss.server.boxProxyAddress(box.Name),
			"users":    users,
			"links":    links,
		})
		return nil
	}

	// Text output
	route := box.GetRoute()
	isPublic := route.Share == "public"

	cc.Writeln("")
	cc.Writeln("\033[1;36mSharing for box '%s'\033[0m", box.Name)
	cc.Writeln("URL: %s", ss.server.boxProxyAddress(box.Name))
	cc.Writeln("Port: %d", route.Port)
	if isPublic {
		cc.Writeln("\033[1;33mMode: PUBLIC\033[0m - Anyone can access this box without authentication")
	} else {
		cc.Writeln("Mode: Private")
	}
	cc.Writeln("")

	if isPublic {
		cc.Writeln("\033[1;33mNote:\033[0m This box is publicly accessible. Individual shares are not needed.")
		cc.Writeln("To make it private, use: share set-private %s", box.Name)
		cc.Writeln("")
		if len(pendingShares)+len(activeShares)+len(shareLinks) == 0 {
			return nil
		}
		cc.Writeln("Existing shares (will take effect if box becomes private):")
		cc.Writeln("")
	}

	if !isPublic && len(pendingShares)+len(activeShares)+len(shareLinks) == 0 {
		cc.Writeln("No shares configured.")
		cc.Writeln("")
		cc.Writeln("To share with someone, use:")
		cc.Writeln("  share add %s user@example.com", box.Name)
		cc.Writeln("  share add-link %s", box.Name)
		cc.Writeln("")
		return nil
	}

	if len(pendingShares)+len(activeShares) > 0 {
		cc.Writeln("\033[1mShared with users:\033[0m")
		for _, ps := range pendingShares {
			age := formatAge(ps.CreatedAt)
			cc.Writeln("  %s (invited %s)", ps.SharedWithEmail, age)
		}
		for _, as := range activeShares {
			age := formatAge(as.CreatedAt)
			cc.Writeln("  %s (active, invited %s)", as.SharedWithUserEmail, age)
		}
		cc.Writeln("")
	} else {
		cc.Writeln("No users shared yet.")
		cc.Writeln("")
	}

	if len(shareLinks) > 0 {
		cc.Writeln("\033[1mShare links:\033[0m")
		for _, sl := range shareLinks {
			age := formatAge(sl.CreatedAt)
			cc.Writeln("  %s (created %s, used %d times)", sl.ShareToken, age, *sl.UseCount)
		}
		cc.Writeln("")
	} else {
		cc.Writeln("No share links yet. Create one with:")
		cc.Writeln("  share add-link %s", box.Name)
		cc.Writeln("")
	}

	return nil
}

func (ss *SSHServer) handleSharePortCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("usage: share port <box> [port]")
	}

	boxName := cc.Args[0]
	if len(cc.Args) == 1 {
		return ss.showRouteConfiguration(ctx, cc, boxName)
	}

	portStr := cc.Args[1]
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return cc.Errorf("port must be a valid port number (1-65535), got %q", portStr)
	}

	return ss.updateBoxRoute(ctx, cc, boxName, func(route *exedb.Route) error {
		route.Port = port
		return nil
	})
}

func (ss *SSHServer) handleShareSetPublicCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	return ss.handleShareVisibilityCmd(ctx, cc, "public")
}

func (ss *SSHServer) handleShareSetPrivateCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	return ss.handleShareVisibilityCmd(ctx, cc, "private")
}

func (ss *SSHServer) handleShareVisibilityCmd(ctx context.Context, cc *exemenu.CommandContext, shareMode string) error {
	if len(cc.Args) != 1 {
		switch shareMode {
		case "public":
			return cc.Errorf("usage: share set-public <box>")
		case "private":
			return cc.Errorf("usage: share set-private <box>")
		default:
			return cc.Errorf("box argument missing")
		}
	}

	boxName := cc.Args[0]

	return ss.updateBoxRoute(ctx, cc, boxName, func(route *exedb.Route) error {
		route.Share = shareMode
		return nil
	})
}

func (ss *SSHServer) getOwnedBox(ctx context.Context, cc *exemenu.CommandContext, boxName string) (exedb.Box, error) {
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return exedb.Box{}, cc.Errorf("box %q not found or access denied", boxName)
	}
	if err != nil {
		return exedb.Box{}, err
	}
	return box, nil
}

func (ss *SSHServer) showRouteConfiguration(ctx context.Context, cc *exemenu.CommandContext, boxName string) error {
	box, err := ss.getOwnedBox(ctx, cc, boxName)
	if err != nil {
		return err
	}
	route := box.GetRoute()

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"box_name": boxName,
			"port":     route.Port,
			"share":    route.Share,
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;36mRoute configuration for box '%s':\033[0m", boxName)
	cc.Writeln("  Port: %d", route.Port)
	cc.Writeln("  Share: %s", route.Share)
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) updateBoxRoute(ctx context.Context, cc *exemenu.CommandContext, boxName string, mutate func(*exedb.Route) error) error {
	box, err := ss.getOwnedBox(ctx, cc, boxName)
	if err != nil {
		return err
	}

	route := box.GetRoute()
	if err := mutate(&route); err != nil {
		return err
	}

	box.SetRoute(route)

	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateBoxRoutes(ctx, exedb.UpdateBoxRoutesParams{
			Routes:          box.Routes,
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		result := map[string]any{
			"box_name": boxName,
			"port":     route.Port,
			"share":    route.Share,
			"status":   "updated",
		}
		cc.WriteJSON(result)
		return nil
	}

	cc.Writeln("\033[1;32m✓ Route updated successfully\033[0m")
	cc.Writeln("  Port: %d", route.Port)
	cc.Writeln("  Share: %s", route.Share)
	cc.Writeln("")
	return nil
}

func formatAge(t *time.Time) string {
	if t == nil {
		return "unknown"
	}
	return humanize.Time(*t)
}

func (ss *SSHServer) handleShareAddCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// share add <box> <email> [--message=...]
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: share add <box> <email> [--message='...']")
	}

	boxName := cc.Args[0]

	// Get the box and verify ownership
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found or access denied", boxName)
	}
	if err != nil {
		return err
	}

	email := strings.ToLower(strings.TrimSpace(cc.Args[1]))

	// Validate email format
	if !isValidEmail(email) {
		return cc.Errorf("invalid email address: %q", email)
	}

	// Get message flag if provided
	message := ""
	if msg := cc.FlagSet.Lookup("message"); msg != nil && msg.Value.String() != "" {
		message = msg.Value.String()
	}

	// Check email rate limit
	err = ss.server.checkAndIncrementEmailQuota(ctx, cc.User.ID)
	if err != nil {
		if strings.Contains(err.Error(), "daily email limit") {
			if !cc.WantJSON() {
				cc.Writeln("")
				cc.Writeln("\033[1;31mError:\033[0m %v", err)
				cc.Writeln("")
				cc.Writeln("You can create share links instead:")
				cc.Writeln("  share add-link %s", box.Name)
				cc.Writeln("")
			}
		}
		return err
	}

	// Check if user exists
	var targetUserID string
	var userExists bool
	err = ss.server.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		targetUserID, err = queries.GetUserIDByEmail(ctx, email)
		if errors.Is(err, sql.ErrNoRows) {
			userExists = false
			return nil
		}
		userExists = true
		return err
	})
	if err != nil {
		return err
	}

	// Create share (pending or active depending on user existence)
	var msgPtr *string
	if message != "" {
		msgPtr = &message
	}

	if userExists {
		// User exists - create active share
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			_, err := queries.CreateBoxShare(ctx, exedb.CreateBoxShareParams{
				BoxID:            int64(box.ID),
				SharedWithUserID: targetUserID,
				SharedByUserID:   cc.User.ID,
				Message:          msgPtr,
			})
			return err
		})
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				return cc.Errorf("%s is already shared with %s", box.Name, email)
			}
			return err
		}
	} else {
		// User doesn't exist - create pending share
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			_, err := queries.CreatePendingBoxShare(ctx, exedb.CreatePendingBoxShareParams{
				BoxID:           int64(box.ID),
				SharedWithEmail: email,
				SharedByUserID:  cc.User.ID,
				Message:         msgPtr,
			})
			return err
		})
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				return cc.Errorf("%s is already shared with %s", box.Name, email)
			}
			return err
		}
	}

	boxURL := ss.server.boxProxyAddress(box.Name)
	webURL := ss.server.webBaseURLNoRequest()

	// Send invitation email
	subject := fmt.Sprintf("%s shared %s with you using %s", cc.User.Email, webURL, ss.server.env.WebHost)
	body := fmt.Sprintf(`Hi,

%s has shared %s with you using %s.

`, cc.User.Email, webURL, ss.server.env.WebHost)

	if message != "" {
		body += fmt.Sprintf("Message from %s:\n\"%s\"\n\n", cc.User.Email, message)
	}

	body += fmt.Sprintf(`To access it, visit %s and log in with this e-mail address (%s)`,
		boxURL, email)

	body += fmt.Sprintf(`

---
%s
`, ss.server.env.WebHost)

	if err := ss.server.sendEmail(email, subject, body); err != nil {
		return fmt.Errorf("failed to send share invitation email: %w", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"status":      "success",
			"box_name":    box.Name,
			"shared_with": email,
			"message":     "Invitation sent",
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32m✓\033[0m Invitation sent to %s", email)
	cc.Writeln("")
	cc.Writeln("%s will receive an email with access instructions.", email)
	cc.Writeln("You can also share this URL directly: %s", boxURL)
	cc.Writeln("(They'll need to log in with %s to access it)", email)

	// Warn if box is public
	route := box.GetRoute()
	if route.Share == "public" {
		cc.Writeln("")
		cc.Writeln("\033[1;33mNote:\033[0m This box is currently PUBLIC. The share will only take effect if you make it private.")
		cc.Writeln("To make it private: share set-private %s", box.Name)
	}
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleShareRemoveCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// share remove <box> <email>
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: share remove <box> <email>")
	}

	boxName := cc.Args[0]

	// Get the box and verify ownership
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found or access denied", boxName)
	}
	if err != nil {
		return err
	}

	email := strings.ToLower(strings.TrimSpace(cc.Args[1]))

	// Try to delete both pending and active shares
	// (a user might have both if they were invited before registering)
	var deletedPending, deletedActive bool

	// Delete pending share
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.DeletePendingBoxShareByBoxAndEmail(ctx, exedb.DeletePendingBoxShareByBoxAndEmailParams{
			BoxID:           int64(box.ID),
			SharedWithEmail: email,
		})
	})
	// Ignore "not found" errors for pending shares
	if err == nil {
		deletedPending = true
	}

	// Try to delete active share if user exists
	var targetUserID string
	err = ss.server.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		targetUserID, err = queries.GetUserIDByEmail(ctx, email)
		return err
	})
	if err == nil {
		// User exists, try to delete their active share
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteBoxShareByBoxAndUser(ctx, exedb.DeleteBoxShareByBoxAndUserParams{
				BoxID:            int64(box.ID),
				SharedWithUserID: targetUserID,
			})
		})
		if err == nil {
			deletedActive = true
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		// Real error (not just user not found)
		return err
	}

	// Error if we didn't delete anything
	if !deletedPending && !deletedActive {
		return cc.Errorf("no share found for %s", email)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"status":   "success",
			"box_name": box.Name,
			"message":  fmt.Sprintf("Removed %s's access", email),
		})
		return nil
	}

	userShares, linkShares, err := ss.server.countTotalShares(ctx, box.ID)
	if err != nil {
		return err
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32m✓\033[0m Removed %s's access to '%s'", email, box.Name)
	if userShares == 0 && linkShares == 0 {
		cc.Writeln("\033[1;32m✓\033[0m Box '%s' is now private (no shares remaining)", box.Name)
	}
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleShareAddLinkCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// share add-link <box>
	if len(cc.Args) == 0 {
		return cc.Errorf("usage: share add-link <box>")
	}

	boxName := cc.Args[0]

	// Get the box and verify ownership
	box, err := withRxRes(ss.server, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("box %q not found or access denied", boxName)
	}
	if err != nil {
		return err
	}

	// Generate token
	token := generateShareToken()

	// Create share link
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		_, err := queries.CreateBoxShareLink(ctx, exedb.CreateBoxShareLinkParams{
			BoxID:           int64(box.ID),
			ShareToken:      token,
			CreatedByUserID: cc.User.ID,
		})
		return err
	})
	if err != nil {
		return err
	}

	shareURL := fmt.Sprintf("%s?share=%s", ss.server.boxProxyAddress(box.Name), token)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"status":   "success",
			"box_name": box.Name,
			"token":    token,
			"url":      shareURL,
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32m✓\033[0m Share link created")
	cc.Writeln("")

	// Warn if box is public
	route := box.GetRoute()
	if route.Share == "public" {
		cc.Writeln("\033[1;33mNote:\033[0m This box is currently PUBLIC (no authentication required).")
		cc.Writeln("The share link will only matter if you make the box private.")
		cc.Writeln("")
	}

	cc.Writeln("Anyone with this link can access your box after logging in:")
	cc.Writeln("\033[1m%s\033[0m", shareURL)
	cc.Writeln("")
	cc.Writeln("To revoke this link, use:")
	cc.Writeln("  share remove-link %s %s", box.Name, token)
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleShareRemoveLinkCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// share remove-link <box> <token>
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: share remove-link <box> <token>")
	}

	boxName := cc.Args[0]
	token := strings.TrimSpace(cc.Args[1])

	// Get the box, verify ownership, and delete share link in a single transaction
	var box exedb.Box
	err := ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		box, err = queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            boxName,
			CreatedByUserID: cc.User.ID,
		})
		if err != nil {
			return err
		}

		return queries.DeleteBoxShareLinkByBoxAndToken(ctx, exedb.DeleteBoxShareLinkByBoxAndTokenParams{
			BoxID:      int64(box.ID),
			ShareToken: token,
		})
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cc.Errorf("box %q not found or share link '%s' not found", boxName, token)
		}
		return err
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"status":   "success",
			"box_name": box.Name,
			"message":  "Share link removed",
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32m✓\033[0m Removed share link %s", token)
	cc.Writeln("")
	return nil
}
