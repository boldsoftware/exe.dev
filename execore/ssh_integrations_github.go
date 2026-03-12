package execore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"strings"
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
	authSetup, authCleanup, err := ss.registerGitHubSetup(cc.User.ID)
	if err != nil {
		return cc.Errorf("%v", err)
	}
	defer authCleanup()

	authorizeURL := ss.server.githubApp.AuthorizeURL(authSetup.State)
	if err := ss.server.createRedirectForKey(ctx, authSetup.State, authorizeURL); err != nil {
		return cc.Errorf("failed to create redirect: %v", err)
	}
	cc.Writeln("Authorize your GitHub account:")
	cc.Writeln("  %s", ss.server.redirectURL(authSetup.State))
	cc.Writeln("")
	cc.Writeln("Waiting...")

	if err := ss.waitForGitHubSetup(ctx, cc, authSetup); err != nil {
		return err
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
	var targets []string
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
		targets = append(targets, inst.Account.Login)
	}
	cc.Writeln("Connected: %s", strings.Join(targets, ", "))

	// Redirect the browser to the app's install page so the user can
	// install on additional accounts, then run setup again to sync.
	authSetup.respond(fmt.Sprintf("https://github.com/apps/%s/installations/new", ss.server.githubApp.AppSlug))
	cc.Writeln("To install on another account, choose one in your browser and then run \"integrations setup github\" again.")
	ss.printGitHubAppSettingsHint(cc)
	return nil
}

// setupGitHubInstallChained chains from an authorize callback to the install
// flow by redirecting the user's browser instead of showing a new URL.
func (ss *SSHServer) setupGitHubInstallChained(ctx context.Context, cc *exemenu.CommandContext, existingIDs map[int64]bool, prevSetup *GitHubSetup) error {
	setup, cleanup, err := ss.registerGitHubSetup(cc.User.ID)
	if err != nil {
		return cc.Errorf("%v", err)
	}
	defer cleanup()

	// Redirect the authorize callback's browser to the install URL.
	installURL := ss.server.githubApp.InstallURL(setup.State)
	prevSetup.respond(installURL)

	cc.Writeln("Waiting...")

	if err := ss.waitForGitHubSetup(ctx, cc, setup); err != nil {
		return err
	}
	return ss.storeGitHubInstall(ctx, cc, setup, existingIDs)
}

// storeGitHubInstall processes the result of a GitHub App install callback.
func (ss *SSHServer) storeGitHubInstall(ctx context.Context, cc *exemenu.CommandContext, setup *GitHubSetup, existingIDs map[int64]bool) error {
	if setup.InstallationID == 0 {
		return cc.Errorf("no installation ID received from GitHub")
	}

	if existingIDs[setup.InstallationID] {
		cc.Writeln("Already connected (installation %d).", setup.InstallationID)
		return nil
	}

	var targetLogin string
	if ss.server.githubApp.InstallationTokensEnabled() {
		tl, err := ss.server.githubApp.GetInstallationAccount(ctx, setup.InstallationID)
		if err != nil {
			return cc.Errorf("failed to look up installation account: %v", err)
		}
		targetLogin = tl
	} else {
		return cc.Errorf("GitHub App is not fully configured (missing app_id or private key)")
	}

	err := ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpsertGitHubAccount(ctx, exedb.UpsertGitHubAccountParams{
			UserID:         cc.User.ID,
			GitHubLogin:    setup.GitHubLogin,
			InstallationID: setup.InstallationID,
			TargetLogin:    targetLogin,
			AccessToken:    setup.AccessToken,
			RefreshToken:   setup.RefreshToken,
		})
	})
	if err != nil {
		return cc.Errorf("failed to save GitHub connection: %v", err)
	}

	label := setup.GitHubLogin
	if targetLogin != "" {
		label = fmt.Sprintf("%s (%s)", setup.GitHubLogin, targetLogin)
	}
	cc.Writeln("Connected: %s", label)
	ss.printGitHubAppSettingsHint(cc)
	return nil
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
		target := a.TargetLogin
		if target == "" {
			target = "(unknown)"
		}
		cc.Writeln("  %s (installed on %s)", a.GitHubLogin, target)
	}
	cc.Writeln("")
	cc.Writeln("Manage app permissions: https://github.com/apps/%s", ss.server.githubApp.AppSlug)
	return nil
}

// handleVerifyGitHub verifies that stored GitHub tokens are valid by calling the GitHub API.
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
		label := acct.GitHubLogin
		if acct.TargetLogin != "" {
			label = fmt.Sprintf("%s (installed on %s)", acct.GitHubLogin, acct.TargetLogin)
		}

		// Try the stored access token.
		login, err := ss.server.githubApp.GetUser(ctx, acct.AccessToken)
		if err == nil {
			cc.Writeln("✓ %s — verified (API user: %s)", label, login)
			continue
		}

		// Only attempt refresh for auth failures (expired/revoked tokens).
		if !githubapp.IsAuthError(err) {
			cc.Writeln("✗ %s — API error: %v", label, err)
			allOK = false
			continue
		}

		if acct.RefreshToken == "" {
			cc.Writeln("✗ %s — token expired, no refresh token", label)
			allOK = false
			continue
		}

		tokenResp, refreshErr := ss.server.githubApp.RefreshUserToken(ctx, acct.RefreshToken)
		if refreshErr != nil {
			cc.Writeln("✗ %s — token expired, refresh failed: %v", label, refreshErr)
			allOK = false
			continue
		}

		// Verify the new token.
		login, err = ss.server.githubApp.GetUser(ctx, tokenResp.AccessToken)
		if err != nil {
			cc.Writeln("✗ %s — refreshed token also failed: %v", label, err)
			allOK = false
			continue
		}

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
			cc.Writeln("✗ %s — verified but failed to save refreshed token: %v", label, err)
			allOK = false
			continue
		}
		cc.Writeln("✓ %s — verified after token refresh (API user: %s)", label, login)
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

	var accounts []string
	for _, a := range existing {
		label := a.GitHubLogin
		if a.TargetLogin != "" {
			label = fmt.Sprintf("%s (%s)", a.GitHubLogin, a.TargetLogin)
		}
		accounts = append(accounts, label)
	}
	cc.Writeln("Disconnected GitHub: %s", strings.Join(accounts, ", "))
	return nil
}
