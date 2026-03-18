package execore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"sync"
	"time"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/githubapp"
)

// GitHubSetup represents a pending GitHub App installation (in-memory).
type GitHubSetup struct {
	State     string
	UserID    string
	CreatedAt time.Time

	CompleteChan chan struct{}
	closeOnce    sync.Once

	// RespondCh carries the callback's HTTP response decision.
	// After signaling CompleteChan, the callback waits on this channel.
	// Empty string means show "Connected" page; non-empty means redirect.
	RespondCh chan string

	// Filled by web callback:
	GitHubLogin    string
	InstallationID int64
	AccessToken    string
	RefreshToken   string
	Err            error
}

// Close signals completion to the waiting SSH session.
func (gs *GitHubSetup) Close() {
	gs.closeOnce.Do(func() {
		close(gs.CompleteChan)
	})
}

// respond sends a response to the callback handler.
// Empty string = show "Connected" page; non-empty = redirect to URL.
func (gs *GitHubSetup) respond(redirectURL string) {
	select {
	case gs.RespondCh <- redirectURL:
	default:
	}
}

func setupGitHubFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("integrations setup github", flag.ContinueOnError)
	fs.Bool("d", false, "disconnect GitHub account")
	fs.Bool("delete", false, "disconnect GitHub account")
	fs.Bool("list", false, "list connected GitHub accounts")
	fs.Bool("verify", false, "verify GitHub connections are working")
	return fs
}

func (ss *SSHServer) handleIntegrationsSetup(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) != 1 {
		return cc.Errorf("usage: integrations setup <type> [-d]")
	}
	switch cc.Args[0] {
	case "github":
		return ss.handleSetupGitHub(ctx, cc)
	default:
		return cc.Errorf("unknown integration type %q (supported: github)", cc.Args[0])
	}
}

func (ss *SSHServer) handleSetupGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	if !ss.server.githubApp.Enabled() {
		return cc.Errorf("GitHub integration is not configured on this server")
	}

	// Check for delete flag.
	deleteFlag := cc.FlagSet.Lookup("d").Value.String() == "true" ||
		cc.FlagSet.Lookup("delete").Value.String() == "true"
	if deleteFlag {
		return ss.handleDeleteGitHub(ctx, cc)
	}

	// Check for list flag.
	listFlag := cc.FlagSet.Lookup("list").Value.String() == "true"
	if listFlag {
		return ss.handleListGitHub(ctx, cc)
	}

	// Check for verify flag.
	verifyFlag := cc.FlagSet.Lookup("verify").Value.String() == "true"
	if verifyFlag {
		return ss.handleVerifyGitHub(ctx, cc)
	}

	// Load existing connections.
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubAccounts, cc.User.ID)
	if err != nil {
		return err
	}
	existingIDs := map[int64]bool{}
	for _, a := range existing {
		existingIDs[a.InstallationID] = true
	}

	// Phase 1: OAuth authorize to discover existing installations.
	// Retry loop: if the user picks the wrong GitHub account from the
	// browser's account chooser, the token will be invalid. Let them retry.
	const maxOAuthAttempts = 3
	var authSetup *GitHubSetup
	for attempt := range maxOAuthAttempts {
		var authCleanup func()
		var err error
		authSetup, authCleanup, err = ss.registerGitHubSetup(cc.User.ID)
		if err != nil {
			return cc.Errorf("%v", err)
		}
		defer authCleanup()

		authorizeURL := ss.server.githubApp.AuthorizeURL(authSetup.State)
		if err := ss.server.createRedirectForKey(ctx, authSetup.State, authorizeURL); err != nil {
			return cc.Errorf("failed to create redirect: %v", err)
		}
		if attempt == 0 {
			cc.Writeln("Authorize your GitHub account:")
		} else {
			cc.Writeln("Try again:")
		}
		cc.Writeln("  %s", ss.server.redirectURL(authSetup.State))
		cc.Writeln("")
		cc.Writeln("Waiting...")

		waitErr := ss.waitForGitHubSetup(ctx, cc, authSetup)
		if waitErr != nil {
			// Check if the underlying cause is an auth error (wrong account).
			if authSetup.Err != nil && githubapp.IsAuthError(authSetup.Err) && attempt < maxOAuthAttempts-1 {
				cc.Writeln("")
				cc.Writeln("Authorization failed \u2014 this can happen if you selected")
				cc.Writeln("the wrong GitHub account. Let's try again.")
				cc.Writeln("")
				continue
			}
			return waitErr
		}
		break
	}

	// Discover installations accessible to this user.
	userInstalls, err := ss.server.githubApp.GetUserInstallations(ctx, authSetup.AccessToken)
	if err != nil {
		return cc.Errorf("failed to discover installations: %v", err)
	}

	if len(userInstalls) == 0 {
		// No installations at all — chain to install flow via browser redirect.
		cc.Writeln("No GitHub App installations found for %s.", authSetup.GitHubLogin)
		return ss.setupGitHubInstallChained(ctx, cc, existingIDs, authSetup)
	}

	// Upsert all discovered installations (syncs tokens, fixes stale installation_ids).
	for _, inst := range userInstalls {
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
				UserID:         cc.User.ID,
				GitHubLogin:    authSetup.GitHubLogin,
				InstallationID: inst.ID,
				TargetLogin:    inst.Account.Login,
				AccessToken:    authSetup.AccessToken,
				RefreshToken:   authSetup.RefreshToken,
			})
		})
		if err != nil {
			return cc.Errorf("failed to save GitHub connection: %v", err)
		}
		existingIDs[inst.ID] = true
	}

	cc.Writeln("")
	cc.Writeln("Connected:")
	for _, inst := range userInstalls {
		cc.Writeln("  %s", formatGitHubAccount(inst.Account.Login, authSetup.GitHubLogin))
	}
	cc.Writeln("")

	// Redirect the browser to the app's install page so the user can
	// install on additional accounts, then run setup again to sync.
	authSetup.respond(ss.server.githubApp.InstallURL(""))
	cc.Writeln("To install on another account, go to https://github.com/apps/%s/installations/new", ss.server.githubApp.AppSlug)
	cc.Writeln("Then run \"integrations setup github\" again to sync.")
	ss.printGitHubAppSettingsHint(cc)
	return nil
}

// setupGitHubInstallChained chains from an authorize callback to the install
// flow by redirecting the user's browser to the GitHub App install page, then
// polling the GitHub API until a new installation appears.
//
// We poll rather than waiting for a callback because GitHub does not reliably
// relay the state parameter for install callbacks (e.g. when an org admin
// approves a requested installation in a separate browser session).
func (ss *SSHServer) setupGitHubInstallChained(ctx context.Context, cc *exemenu.CommandContext, existingIDs map[int64]bool, prevSetup *GitHubSetup) error {
	prevSetup.respond(ss.server.githubApp.InstallURL(""))

	cc.Writeln("Install the app in your browser, then come back here.")
	cc.Writeln("Waiting...")

	// Poll for new installations.
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	timeout := time.After(10 * time.Minute)

	var consecutiveErrors int
	for {
		select {
		case <-ticker.C:
			installs, err := ss.server.githubApp.GetUserInstallations(ctx, prevSetup.AccessToken)
			if err != nil {
				consecutiveErrors++
				if githubapp.IsAuthError(err) {
					return cc.Errorf("GitHub token is no longer valid — run integrations setup github again")
				}
				if consecutiveErrors >= 5 {
					ss.server.slog().WarnContext(ctx, "GitHub polling errors", "consecutive", consecutiveErrors, "error", err)
				}
				continue
			}
			consecutiveErrors = 0

			var newInstalls []githubapp.Installation
			for _, inst := range installs {
				if !existingIDs[inst.ID] {
					newInstalls = append(newInstalls, inst)
				}
			}
			if len(newInstalls) > 0 {
				for _, inst := range newInstalls {
					err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
						return queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
							UserID:         cc.User.ID,
							GitHubLogin:    prevSetup.GitHubLogin,
							InstallationID: inst.ID,
							TargetLogin:    inst.Account.Login,
							AccessToken:    prevSetup.AccessToken,
							RefreshToken:   prevSetup.RefreshToken,
						})
					})
					if err != nil {
						return cc.Errorf("failed to save GitHub connection: %v", err)
					}
				}
				cc.Writeln("")
				cc.Writeln("Connected:")
				for _, inst := range newInstalls {
					cc.Writeln("  %s", formatGitHubAccount(inst.Account.Login, prevSetup.GitHubLogin))
				}
				cc.Writeln("")
				ss.printGitHubAppSettingsHint(cc)
				return nil
			}
		case <-ctx.Done():
			return cc.Errorf("GitHub setup canceled")
		case <-timeout:
			return cc.Errorf("GitHub setup timed out — run integrations setup github to try again")
		}
	}
}

// formatGitHubAccount formats a GitHub account for display.
// When target and login differ (org install), shows "target (via login)".
// When they're the same (personal install), just shows "target".
func formatGitHubAccount(target, login string) string {
	if target == "" {
		return login
	}
	if target == login {
		return target
	}
	return fmt.Sprintf("%s (via %s)", target, login)
}

// printGitHubAppSettingsHint prints a hint about where to manage the GitHub App.
func (ss *SSHServer) printGitHubAppSettingsHint(cc *exemenu.CommandContext) {
	cc.Writeln("Manage app permissions: https://github.com/apps/%s", ss.server.githubApp.AppSlug)
	cc.Writeln("View connections: integrations setup github --list")
	cc.Writeln("Verify connections: integrations setup github --verify")
}

// handleListGitHub shows the current GitHub account connections.
func (ss *SSHServer) handleListGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubAccounts, cc.User.ID)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		cc.Writeln("No GitHub accounts connected.")
		cc.Writeln("Run: integrations setup github")
		return nil
	}

	cc.Writeln("GitHub accounts:")
	for _, a := range existing {
		cc.Writeln("  %s", formatGitHubAccount(a.TargetLogin, a.GitHubLogin))
	}
	return nil
}

// handleVerifyGitHub verifies that stored GitHub tokens are valid and
// that the GitHub App is still installed on each target account.
func (ss *SSHServer) handleVerifyGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubAccounts, cc.User.ID)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		cc.Writeln("No GitHub accounts connected.")
		cc.Writeln("Run: integrations setup github")
		return nil
	}

	allOK := true
	for _, acct := range existing {
		label := formatGitHubAccount(acct.TargetLogin, acct.GitHubLogin)

		// Resolve a working access token.
		workingToken := acct.AccessToken
		_, err := ss.server.githubApp.GetUser(ctx, workingToken)
		if err != nil {
			if !githubapp.IsAuthError(err) {
				cc.Writeln("  %s ✗ %v", label, err)
				allOK = false
				continue
			}
			if acct.RefreshToken == "" {
				cc.Writeln("  %s ✗ token expired", label)
				allOK = false
				continue
			}
			tokenResp, refreshErr := ss.server.githubApp.RefreshUserToken(ctx, acct.RefreshToken)
			if refreshErr != nil {
				cc.Writeln("  %s ✗ token expired, refresh failed", label)
				allOK = false
				continue
			}
			_, err = ss.server.githubApp.GetUser(ctx, tokenResp.AccessToken)
			if err != nil {
				cc.Writeln("  %s ✗ refresh failed", label)
				allOK = false
				continue
			}
			workingToken = tokenResp.AccessToken
			// Update stored tokens.
			if err := ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
				return queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
					UserID:         cc.User.ID,
					GitHubLogin:    acct.GitHubLogin,
					InstallationID: acct.InstallationID,
					TargetLogin:    acct.TargetLogin,
					AccessToken:    tokenResp.AccessToken,
					RefreshToken:   tokenResp.RefreshToken,
				})
			}); err != nil {
				cc.Writeln("  %s ✗ failed to save refreshed token", label)
				allOK = false
				continue
			}
		}

		// Check that the GitHub App installation is still active.
		installs, err := ss.server.githubApp.GetUserInstallations(ctx, workingToken)
		if err != nil {
			cc.Writeln("  %s ✗ failed to check installations: %v", label, err)
			allOK = false
			continue
		}
		found := false
		for _, inst := range installs {
			if inst.ID == acct.InstallationID {
				found = true
				break
			}
		}
		if !found {
			cc.Writeln("  %s ✗ app not installed", label)
			allOK = false
			continue
		}

		cc.Writeln("  %s ✓", label)
	}

	if !allOK {
		cc.Writeln("")
		return cc.Errorf("some connections failed — run: integrations setup github")
	}
	return nil
}

// registerGitHubSetup creates and registers a pending GitHub setup.
// The returned cleanup function ensures the callback gets a response and
// removes the setup from the pending map.
func (ss *SSHServer) registerGitHubSetup(userID string) (*GitHubSetup, func(), error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, nil, fmt.Errorf("failed to generate state token: %v", err)
	}
	state := hex.EncodeToString(stateBytes)

	setup := &GitHubSetup{
		State:        state,
		UserID:       userID,
		CreatedAt:    time.Now(),
		CompleteChan: make(chan struct{}),
		RespondCh:    make(chan string, 1),
	}
	ss.server.githubSetupsMu.Lock()
	ss.server.githubSetups[state] = setup
	ss.server.githubSetupsMu.Unlock()

	cleanup := func() {
		// Ensure the callback handler doesn't hang.
		setup.respond("")
		ss.server.githubSetupsMu.Lock()
		delete(ss.server.githubSetups, state)
		ss.server.githubSetupsMu.Unlock()
	}
	return setup, cleanup, nil
}

// waitForGitHubSetup blocks until the callback signals completion, the context
// is canceled, or the timeout expires.
func (ss *SSHServer) waitForGitHubSetup(ctx context.Context, cc *exemenu.CommandContext, setup *GitHubSetup) error {
	select {
	case <-setup.CompleteChan:
	case <-ctx.Done():
		return cc.Errorf("GitHub setup canceled")
	case <-time.After(10 * time.Minute):
		return cc.Errorf("GitHub setup timed out (10 minutes)")
	}
	if setup.Err != nil {
		return cc.Errorf("GitHub authorization failed: %v", setup.Err)
	}
	return nil
}

func (ss *SSHServer) handleDeleteGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubAccounts, cc.User.ID)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return cc.Errorf("no GitHub account connected")
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).DeleteAllGitHubAccounts, cc.User.ID)
	if err != nil {
		return err
	}

	cc.Writeln("Disconnected:")
	for _, a := range existing {
		cc.Writeln("  %s", formatGitHubAccount(a.TargetLogin, a.GitHubLogin))
	}
	return nil
}
