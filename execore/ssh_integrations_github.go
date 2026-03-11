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
)

// GitHubSetup represents a pending GitHub App installation (in-memory).
type GitHubSetup struct {
	State     string
	UserID    string
	CreatedAt time.Time

	CompleteChan chan struct{}
	closeOnce    sync.Once

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

func setupGitHubFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("integrations setup github", flag.ContinueOnError)
	fs.Bool("d", false, "disconnect GitHub account")
	fs.Bool("delete", false, "disconnect GitHub account")
	fs.Bool("reconnect", false, "reconnect to existing GitHub App installation")
	return fs
}

func (ss *SSHServer) handleIntegrationsSetup(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
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

	// Show existing connections.
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubAccounts, cc.User.ID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		var accounts []string
		for _, a := range existing {
			accounts = append(accounts, a.TargetLogin)
		}
		cc.Writeln("Already connected: %s", strings.Join(accounts, ", "))
		cc.Writeln("Installing on another account...")
	}

	// Generate random state token.
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return cc.Errorf("failed to generate state token: %v", err)
	}
	state := hex.EncodeToString(stateBytes)

	// Store in-memory setup.
	setup := &GitHubSetup{
		State:        state,
		UserID:       cc.User.ID,
		CreatedAt:    time.Now(),
		CompleteChan: make(chan struct{}),
	}
	ss.server.githubSetupsMu.Lock()
	ss.server.githubSetups[state] = setup
	ss.server.githubSetupsMu.Unlock()

	// Clean up on exit.
	defer func() {
		ss.server.githubSetupsMu.Lock()
		delete(ss.server.githubSetups, state)
		ss.server.githubSetupsMu.Unlock()
	}()

	reconnect := cc.FlagSet.Lookup("reconnect").Value.String() == "true"

	if reconnect {
		authorizeURL := ss.server.githubApp.AuthorizeURL(state)
		cc.Writeln("Authorize (reconnect existing installation):")
		cc.Writeln("  %s", authorizeURL)
	} else {
		installURL := ss.server.githubApp.InstallURL(state)
		cc.Writeln("Install the GitHub App:")
		cc.Writeln("  %s", installURL)
	}
	cc.Writeln("")
	cc.Writeln("Waiting...")

	// Wait for web callback or timeout.
	select {
	case <-setup.CompleteChan:
		// Callback completed.
	case <-ctx.Done():
		return cc.Errorf("GitHub setup canceled")
	case <-time.After(10 * time.Minute):
		return cc.Errorf("GitHub setup timed out (10 minutes)")
	}

	if setup.Err != nil {
		return cc.Errorf("GitHub authorization failed: %v", setup.Err)
	}

	// Build list of installations to store.
	type installInfo struct {
		InstallationID int64
		TargetLogin    string
	}
	var installs []installInfo

	if setup.InstallationID != 0 {
		// Fresh install flow: we got the installation_id from the callback.
		var targetLogin string
		if ss.server.githubApp.InstallationTokensEnabled() {
			tl, err := ss.server.githubApp.GetInstallationAccount(ctx, setup.InstallationID)
			if err != nil {
				ss.server.slog().WarnContext(ctx, "failed to look up installation account", "error", err)
			} else {
				targetLogin = tl
			}
		}
		installs = append(installs, installInfo{setup.InstallationID, targetLogin})
	} else {
		// OAuth-only flow (app already installed): discover installations via API.
		userInstalls, err := ss.server.githubApp.GetUserInstallations(ctx, setup.AccessToken)
		if err != nil {
			return cc.Errorf("failed to discover installations: %v", err)
		}
		if len(userInstalls) == 0 {
			return cc.Errorf("no GitHub App installations found; install at: %s", ss.server.githubApp.InstallURL(state))
		}
		for _, inst := range userInstalls {
			installs = append(installs, installInfo{inst.ID, inst.Account.Login})
		}
	}

	// Store new installations (skip duplicates).
	existingIDs := map[int64]bool{}
	for _, a := range existing {
		existingIDs[a.InstallationID] = true
	}

	var added []string
	for _, inst := range installs {
		if existingIDs[inst.InstallationID] {
			cc.Writeln("Already connected: %s", inst.TargetLogin)
			continue
		}
		err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.InsertGitHubAccount(ctx, exedb.InsertGitHubAccountParams{
				UserID:         cc.User.ID,
				GitHubLogin:    setup.GitHubLogin,
				InstallationID: inst.InstallationID,
				TargetLogin:    inst.TargetLogin,
				AccessToken:    setup.AccessToken,
				RefreshToken:   setup.RefreshToken,
			})
		})
		if err != nil {
			return cc.Errorf("failed to save GitHub connection: %v", err)
		}
		added = append(added, inst.TargetLogin)
	}

	if len(added) > 0 {
		cc.Writeln("Connected GitHub account: %s (%s)", setup.GitHubLogin, strings.Join(added, ", "))
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
