package execore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"sync"
	"time"

	"exe.dev/exedb"
	"exe.dev/exemenu"
)

// GitHubSetup represents a pending GitHub App setup flow (in-memory).
// Used by both SSH-initiated and web-initiated flows.
type GitHubSetup struct {
	State     string
	UserID    string
	WebFlow   bool // true if initiated from web UI (no SSH session)
	CreatedAt time.Time

	CompleteChan chan struct{}
	closeOnce    sync.Once

	// RespondCh carries the callback's HTTP response decision.
	// After signaling CompleteChan, the callback waits on this channel.
	// Empty string means show "Connected" page; non-empty means redirect.
	RespondCh chan string

	// Filled by web callback:
	GitHubLogin           string
	InstallationID        int64
	AccessToken           string
	RefreshToken          string
	AccessTokenExpiresAt  *string
	RefreshTokenExpiresAt *string
	Err                   error
}

// Close signals completion to the waiting session.
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

// handleSetupGitHub enables the GitHub integration feature flag and directs
// the user to the web UI where they can install the app and create integrations.
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

	// Enable the GitHub integration feature flag.
	one := int64(1)
	if err := ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpsertUserDefaultGitHubIntegration(ctx, exedb.UpsertUserDefaultGitHubIntegrationParams{
			UserID:            cc.User.ID,
			GitHubIntegration: &one,
		})
	}); err != nil {
		return cc.Errorf("failed to enable GitHub integration: %v", err)
	}

	// Show the web URL.
	webURL := ss.server.webBaseURLNoRequest() + "/integrations#github"
	cc.Writeln("GitHub integration enabled.")
	cc.Writeln("")
	cc.Writeln("Continue setup in your browser:")
	cc.Writeln("  %s", webURL)

	return nil
}

// handleListGitHub shows the current GitHub account connections.
func (ss *SSHServer) handleListGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	installations, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubInstallations, cc.User.ID)
	if err != nil {
		return err
	}
	if len(installations) == 0 {
		cc.Writeln("No GitHub accounts connected.")
		cc.Writeln("Run: integrations setup github")
		return nil
	}

	cc.Writeln("GitHub accounts:")
	for _, inst := range installations {
		cc.Writeln("  %s", formatGitHubAccount(inst.GitHubAccountLogin, inst.GitHubLogin))
	}
	return nil
}

// handleVerifyGitHub verifies that stored GitHub tokens are valid and
// that the GitHub App is still installed on each target account.
// Token refresh is left to the background renewal loop.
func (ss *SSHServer) handleVerifyGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	installations, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubInstallations, cc.User.ID)
	if err != nil {
		return err
	}
	if len(installations) == 0 {
		cc.Writeln("No GitHub accounts connected.")
		cc.Writeln("Run: integrations setup github")
		return nil
	}

	// Build remote installation set per GitHub login (a user may have
	// installations linked to different GitHub accounts).
	remoteSet := make(map[int64]bool)
	verifiedLogins := make(map[string]bool)
	for _, inst := range installations {
		if verifiedLogins[inst.GitHubLogin] {
			continue
		}
		accessToken, err := ss.server.resolveGitHubTokenWeb(ctx, cc.User.ID, inst.GitHubLogin)
		if err != nil {
			cc.Writeln("  %s: token error: %v", inst.GitHubLogin, err)
			verifiedLogins[inst.GitHubLogin] = true
			continue
		}
		remoteInstalls, err := ss.server.githubApp.GetUserInstallations(ctx, accessToken)
		if err != nil {
			cc.Writeln("  %s: failed to check installations: %v", inst.GitHubLogin, err)
			verifiedLogins[inst.GitHubLogin] = true
			continue
		}
		for _, ri := range remoteInstalls {
			remoteSet[ri.ID] = true
		}
		verifiedLogins[inst.GitHubLogin] = true
	}

	allOK := true
	for _, inst := range installations {
		label := formatGitHubAccount(inst.GitHubAccountLogin, inst.GitHubLogin)
		if !remoteSet[inst.GitHubAppInstallationID] {
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

func (ss *SSHServer) handleDeleteGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	installations, err := withRxRes1(ss.server, ctx, (*exedb.Queries).ListGitHubInstallations, cc.User.ID)
	if err != nil {
		return err
	}
	if len(installations) == 0 {
		return cc.Errorf("no GitHub account connected")
	}

	// Delete installations first (FK constraint), then the tokens, atomically.
	if err := ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.DeleteAllGitHubInstallations(ctx, cc.User.ID); err != nil {
			return err
		}
		return queries.DeleteAllGitHubUserTokens(ctx, cc.User.ID)
	}); err != nil {
		return err
	}

	cc.Writeln("Disconnected:")
	for _, inst := range installations {
		cc.Writeln("  %s", formatGitHubAccount(inst.GitHubAccountLogin, inst.GitHubLogin))
	}
	return nil
}

// formatGitHubAccount formats a GitHub account for display.
func formatGitHubAccount(target, login string) string {
	if target == "" {
		return login
	}
	if target == login {
		return target
	}
	return fmt.Sprintf("%s (via %s)", target, login)
}

// registerGitHubSetup creates and registers a pending GitHub setup on the Server.
func (s *Server) registerGitHubSetup(userID string, webFlow bool) (*GitHubSetup, func(), error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, nil, fmt.Errorf("failed to generate state token: %v", err)
	}
	state := hex.EncodeToString(stateBytes)

	setup := &GitHubSetup{
		State:        state,
		UserID:       userID,
		WebFlow:      webFlow,
		CreatedAt:    time.Now(),
		CompleteChan: make(chan struct{}),
		RespondCh:    make(chan string, 1),
	}
	s.githubSetupsMu.Lock()
	s.githubSetups[state] = setup
	s.githubSetupsMu.Unlock()

	cleanup := func() {
		setup.respond("")
		s.githubSetupsMu.Lock()
		delete(s.githubSetups, state)
		s.githubSetupsMu.Unlock()
	}
	return setup, cleanup, nil
}

// userHasGitHubIntegrationFlag checks whether the user has the github-integration
// feature flag enabled.
func (s *Server) userHasGitHubIntegrationFlag(ctx context.Context, userID string) bool {
	defaults, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserDefaults, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		return false
	}
	return defaults.GitHubIntegration != nil && *defaults.GitHubIntegration != 0
}
