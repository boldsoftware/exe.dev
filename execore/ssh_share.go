package execore

// TODO(philip): consolidate into one tx per handle function,
// moving any emailing or heavy stream IO outside of the tx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	qrterminal "github.com/mdp/qrterminal/v3"
	"mvdan.cc/sh/v3/syntax"

	emailpkg "exe.dev/email"
	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// This file contains SSH command handlers for the 'share' command.
// The share command allows users to share box access with others via email or share links.

// writeQRCode writes a QR code for the given URL to the writer.
func writeQRCode(w io.Writer, url string) {
	qrterminal.GenerateHalfBlock(url, qrterminal.L, w)
}

// shareCommand returns the command definition for the share command
func (ss *SSHServer) shareCommand() *exemenu.Command {
	return &exemenu.Command{
		Name:        "share",
		Description: "Share HTTPS VM access with others",
		Usage:       "share <subcommand> <vm> [args...]",
		Handler:     ss.handleShareHelp,
		FlagSetFunc: jsonOnlyFlags("share"),
		Subcommands: []*exemenu.Command{
			{
				Name:              "show",
				Description:       "Show current shares for a VM",
				Usage:             "share show <vm>",
				Handler:           ss.handleShareShowCmd,
				FlagSetFunc:       addQRFlag(jsonOnlyFlags("share-show")),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "port",
				Description:       "Set the HTTP proxy port for a VM",
				Usage:             "share port <vm> [port]",
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
				Usage:             "share set-public <vm>",
				Handler:           ss.handleShareSetPublicCmd,
				FlagSetFunc:       jsonOnlyFlags("share-set-public"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "set-private",
				Description:       "Restrict the HTTP proxy to authenticated users",
				Usage:             "share set-private <vm>",
				Handler:           ss.handleShareSetPrivateCmd,
				FlagSetFunc:       jsonOnlyFlags("share-set-private"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "add",
				Description:       "Share VM with a user via email",
				Usage:             "share add <vm> <email> [--message='...']",
				Handler:           ss.handleShareAddCmd,
				FlagSetFunc:       addQRFlag(addShareMessageFlag(jsonOnlyFlags("share-add"))),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
				Examples: []string{
					"share add mybox user@example.com",
					"share add mybox user@example.com --message='Check this out'",
				},
			},
			{
				Name:              "remove",
				Description:       "Revoke a user's access to a VM",
				Usage:             "share remove <vm> <email>",
				Handler:           ss.handleShareRemoveCmd,
				FlagSetFunc:       jsonOnlyFlags("share-remove"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "add-link",
				Aliases:           []string{"add-share-link"},
				Description:       "Create a shareable link for a VM",
				Usage:             "share add-link <vm>",
				Handler:           ss.handleShareAddLinkCmd,
				FlagSetFunc:       addQRFlag(jsonOnlyFlags("share-add-link")),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "remove-link",
				Aliases:           []string{"remove-share-link"},
				Description:       "Revoke a shareable link",
				Usage:             "share remove-link <vm> <token>",
				Handler:           ss.handleShareRemoveLinkCmd,
				FlagSetFunc:       jsonOnlyFlags("share-remove-link"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
			},
			{
				Name:              "receive-email",
				Description:       "Enable or disable inbound email for a VM",
				Usage:             "share receive-email <vm> [on|off]",
				Handler:           ss.handleShareReceiveEmailCmd,
				FlagSetFunc:       jsonOnlyFlags("share-receive-email"),
				HasPositionalArgs: true,
				CompleterFunc:     ss.completeBoxNames,
				Examples: []string{
					"share receive-email mybox on",
					"share receive-email mybox off",
					"share receive-email mybox",
				},
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
		return cc.Errorf("usage: share show <vm>")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	// Get the box and verify ownership
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found or access denied", boxName)
	}
	if err != nil {
		return err
	}

	return ss.handleShareShow(ctx, cc, &box)
}

func (ss *SSHServer) handleShareShow(ctx context.Context, cc *exemenu.CommandContext, box *exedb.Box) error {
	// Get pending shares
	pendingShares, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetPendingBoxSharesByBoxID, int64(box.ID))
	if err != nil {
		return err
	}

	// Get active shares
	activeShares, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetBoxSharesByBoxID, int64(box.ID))
	if err != nil {
		return err
	}

	// Get share links
	shareLinks, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetBoxShareLinksByBoxID, exedb.GetBoxShareLinksByBoxIDParams{
		BoxID:           int64(box.ID),
		CreatedByUserID: box.CreatedByUserID,
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

		result := map[string]any{
			"vm_name": box.Name,
			"status":  route.Share,
			"port":    route.Port,
			"url":     ss.server.boxProxyAddress(box.Name),
			"users":   users,
			"links":   links,
		}
		if box.SupportAccessAllowed == 1 {
			result["support_has_root"] = true
		}
		cc.WriteJSON(result)
		return nil
	}

	// Text output
	route := box.GetRoute()
	isPublic := route.Share == "public"

	boxURL := ss.server.boxProxyAddress(box.Name)

	cc.Writeln("")
	cc.Writeln("\033[1;36mSharing for VM '%s'\033[0m", box.Name)
	cc.Writeln("URL: %s", boxURL)
	if cc.WantQR() {
		cc.Writeln("")
		writeQRCode(cc.Output, boxURL)
	}
	cc.Writeln("Port: %d", route.Port)
	if isPublic {
		cc.Writeln("\033[1;33mMode: PUBLIC\033[0m - Anyone can access this VM without authentication")
	} else {
		cc.Writeln("Mode: Private")
	}
	if box.SupportAccessAllowed == 1 {
		cc.Writeln("\033[1;33mSupport access: ENABLED\033[0m - exe.dev support has root access")
		cc.Writeln("  To disable: grant-support-root %s off", box.Name)
	}
	cc.Writeln("")

	if isPublic {
		cc.Writeln("\033[1;33mNote:\033[0m This VM is publicly accessible. Individual shares are not needed.")
		cc.Writeln("To make it private, use: share set-private %s", box.Name)
		cc.Writeln("")
		if len(pendingShares)+len(activeShares)+len(shareLinks) == 0 {
			return nil
		}
		cc.Writeln("Existing shares (will take effect if VM becomes private):")
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
		return cc.Errorf("usage: share port <vm> [port]")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])
	if len(cc.Args) == 1 {
		return ss.showRouteConfiguration(ctx, cc, boxName)
	}

	portStr := cc.Args[1]
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 3000 || port > 9999 {
		return cc.Errorf("port must be between 3000 and 9999, got %q", portStr)
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
			return cc.Errorf("usage: share set-public <vm>")
		case "private":
			return cc.Errorf("usage: share set-private <vm>")
		default:
			return cc.Errorf("VM argument missing")
		}
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	return ss.updateBoxRoute(ctx, cc, boxName, func(route *exedb.Route) error {
		route.Share = shareMode
		return nil
	})
}

func (ss *SSHServer) getOwnedBox(ctx context.Context, cc *exemenu.CommandContext, boxName string) (exedb.Box, error) {
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return exedb.Box{}, cc.Errorf("VM %q not found or access denied", boxName)
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
			"vm_name": boxName,
			"port":    route.Port,
			"share":   route.Share,
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;36mRoute configuration for VM '%s':\033[0m", boxName)
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

	err = withTx1(ss.server, ctx, (*exedb.Queries).UpdateBoxRoutes, exedb.UpdateBoxRoutesParams{
		Routes:          box.Routes,
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		result := map[string]any{
			"vm_name": boxName,
			"port":    route.Port,
			"share":   route.Share,
			"status":  "updated",
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
		return cc.Errorf("usage: share add <vm> <email> [--message='...']")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	// Get the box and verify ownership
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found or access denied", boxName)
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
	var userExists bool
	targetUserID, err := ss.server.GetUserIDByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		userExists = false
		err = nil
	} else if err == nil {
		userExists = true
	}
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

	if err := ss.server.sendEmail(ctx, emailpkg.TypeShareInvitation, email, subject, body); err != nil {
		return fmt.Errorf("failed to send share invitation email: %w", err)
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"status":      "success",
			"vm_name":     box.Name,
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
		cc.Writeln("\033[1;33mNote:\033[0m This VM is currently PUBLIC. The share will only take effect if you make it private.")
		cc.Writeln("To make it private: share set-private %s", box.Name)
	}
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleShareRemoveCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// share remove <box> <email>
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: share remove <vm> <email>")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	// Get the box and verify ownership
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found or access denied", boxName)
	}
	if err != nil {
		return err
	}

	email := strings.ToLower(strings.TrimSpace(cc.Args[1]))

	// Try to delete both pending and active shares
	// (a user might have both if they were invited before registering)
	var deletedPending, deletedActive bool

	// Delete pending share
	err = withTx1(ss.server, ctx, (*exedb.Queries).DeletePendingBoxShareByBoxAndEmail, exedb.DeletePendingBoxShareByBoxAndEmailParams{
		BoxID:           int64(box.ID),
		SharedWithEmail: email,
	})
	// Ignore "not found" errors for pending shares
	if err == nil {
		deletedPending = true
	}

	// Try to delete active share if user exists
	targetUserID, err := ss.server.GetUserIDByEmail(ctx, email)
	if err == nil {
		// User exists, try to delete their active share
		err = withTx1(ss.server, ctx, (*exedb.Queries).DeleteBoxShareByBoxAndUser, exedb.DeleteBoxShareByBoxAndUserParams{
			BoxID:            int64(box.ID),
			SharedWithUserID: targetUserID,
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
			"status":  "success",
			"vm_name": box.Name,
			"message": fmt.Sprintf("Removed %s's access", email),
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
		cc.Writeln("\033[1;32m✓\033[0m VM '%s' is now private (no shares remaining)", box.Name)
	}
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleShareAddLinkCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// share add-link <box>
	if len(cc.Args) == 0 {
		return cc.Errorf("usage: share add-link <vm>")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	// Get the box and verify ownership
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found or access denied", boxName)
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
			"status":  "success",
			"vm_name": box.Name,
			"token":   token,
			"url":     shareURL,
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32m✓\033[0m Share link created")
	cc.Writeln("")

	// Warn if box is public
	route := box.GetRoute()
	if route.Share == "public" {
		cc.Writeln("\033[1;33mNote:\033[0m This VM is currently PUBLIC (no authentication required).")
		cc.Writeln("The share link will only matter if you make the VM private.")
		cc.Writeln("")
	}

	cc.Writeln("Anyone with this link can access your VM after logging in:")
	cc.Writeln("\033[1m%s\033[0m", shareURL)
	cc.Writeln("")

	if cc.WantQR() {
		writeQRCode(cc.Output, shareURL)
		cc.Writeln("")
	}

	cc.Writeln("To revoke this link, use:")
	cc.Writeln("  share remove-link %s %s", box.Name, token)
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleShareRemoveLinkCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	// share remove-link <box> <token>
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: share remove-link <vm> <token>")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])
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
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found or share link '%s' not found", boxName, token)
	}
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"status":  "success",
			"vm_name": box.Name,
			"message": "Share link removed",
		})
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32m✓\033[0m Removed share link %s", token)
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) handleShareReceiveEmailCmd(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) == 0 {
		return cc.Errorf("usage: share receive-email <vm> [on|off]")
	}

	boxName := ss.normalizeBoxName(cc.Args[0])

	// Get the box and verify ownership
	box, err := withRxRes1(ss.server, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: cc.User.ID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("VM %q not found or access denied", boxName)
	}
	if err != nil {
		return err
	}

	emailAddr := fmt.Sprintf("anything@%s.%s", box.Name, ss.server.env.BoxHost)

	// If no on/off argument, show current status
	if len(cc.Args) == 1 {
		enabled := box.EmailReceiveEnabled == 1

		if cc.WantJSON() {
			cc.WriteJSON(map[string]any{
				"vm_name":       box.Name,
				"email_enabled": enabled,
				"email_address": emailAddr,
			})
			return nil
		}

		cc.Writeln("")
		if enabled {
			cc.Writeln("Inbound email for VM %q is \033[1;32menabled\033[0m", box.Name)
			cc.Writeln("Email address: %s", emailAddr)
			cc.Writeln("Emails are delivered to ~/Maildir/new/")
		} else {
			cc.Writeln("Inbound email for VM %q is \033[1;31mdisabled\033[0m", box.Name)
			cc.Writeln("To enable: share receive-email %s on", box.Name)
		}
		cc.Writeln("")
		return nil
	}

	// Parse on/off
	onOff := strings.ToLower(cc.Args[1])
	var enabling bool
	switch onOff {
	case "on", "true", "1":
		enabling = true
	case "off", "false", "0":
		enabling = false
	default:
		return cc.Errorf("invalid value %q: use on or off", cc.Args[1])
	}

	// If enabling, set up maildir and test delivery before updating DB.
	var maildirPath string
	if enabling {
		setupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		// Resolve $HOME to get the absolute path - this ensures consistency
		// across BYO images with non-standard home directories.
		homeOutput, err := runCommandOnBox(setupCtx, ss.server.sshPool, &box, "echo $HOME")
		if err != nil {
			return cc.Errorf("failed to determine home directory on VM: %v", err)
		}
		homeDir := strings.TrimSpace(string(homeOutput))
		if homeDir == "" || homeDir[0] != '/' {
			return cc.Errorf("invalid home directory on VM: %q", homeDir)
		}
		maildirPath = homeDir + "/Maildir"

		// Create maildir directories (standard Maildir structure)
		quotedPath, err := syntax.Quote(maildirPath, syntax.LangBash)
		if err != nil {
			return cc.Errorf("failed to quote maildir path: %v", err)
		}
		mkdirCmd := fmt.Sprintf("mkdir -p %s/new %s/cur %s/tmp", quotedPath, quotedPath, quotedPath)
		_, err = runCommandOnBox(setupCtx, ss.server.sshPool, &box, mkdirCmd)
		if err != nil {
			return cc.Errorf("failed to create maildir on VM: %v", err)
		}

		// Set maildir path on local box copy for deliverEmailToBox
		box.EmailMaildirPath = maildirPath

		// Write a welcome email to verify delivery works
		welcomeEmail := ss.generateWelcomeEmail(box.Name)
		welcomeRecipient := fmt.Sprintf("welcome@%s.%s", box.Name, ss.server.env.BoxHost)
		if err := deliverEmailToBox(setupCtx, ss.server.sshPool, &box, welcomeRecipient, welcomeEmail); err != nil {
			return cc.Errorf("failed to deliver welcome email: %v", err)
		}
	}

	// Update the database last, after verifying setup works
	var newValue int64
	if enabling {
		newValue = 1
	}
	err = withTx1(ss.server, ctx, (*exedb.Queries).SetBoxEmailReceive, exedb.SetBoxEmailReceiveParams{
		EmailReceiveEnabled: newValue,
		EmailMaildirPath:    maildirPath, // empty string when disabling
		ID:                  box.ID,
	})
	if err != nil {
		return err
	}

	if cc.WantJSON() {
		result := map[string]any{
			"status":        "success",
			"vm_name":       box.Name,
			"email_enabled": enabling,
		}
		if enabling {
			result["email_address"] = emailAddr
		}
		cc.WriteJSON(result)
		return nil
	}

	cc.Writeln("")
	if enabling {
		cc.Writeln("\033[1;32m✓\033[0m Inbound email enabled for VM %q", box.Name)
		cc.Writeln("Email address: %s", emailAddr)
		cc.Writeln("Emails will be delivered to ~/Maildir/new/")
	} else {
		cc.Writeln("\033[1;32m✓\033[0m Inbound email disabled for VM %q", box.Name)
	}
	cc.Writeln("")
	return nil
}

func (ss *SSHServer) generateWelcomeEmail(boxName string) []byte {
	env := ss.server.env
	now := time.Now().UTC().Format(time.RFC1123Z)
	emailAddr := fmt.Sprintf("anything@%s.%s", boxName, env.BoxHost)
	body := fmt.Sprintf(`From: %s <support@%s>
To: <%s>
Subject: Welcome to exe.dev inbound email
Date: %s
Content-Type: text/plain; charset=utf-8

Inbound email is now enabled for your VM.

Any email sent to *@%s.%s will be delivered to ~/Maildir/new/

For documentation, visit: https://%s/docs/receive-email

---
%s
`, env.WebHost, env.WebHost, emailAddr, now, boxName, env.BoxHost, env.WebHost, env.WebHost)
	return []byte(body)
}
