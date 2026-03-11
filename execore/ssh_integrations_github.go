package execore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
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

	// Check if already connected.
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetGitHubAccount, cc.User.ID)
	if err == nil {
		return cc.Errorf("GitHub account already connected as %s", existing.GitHubLogin)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
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

	installURL := ss.server.githubApp.InstallURL(state)
	cc.Writeln("Install the GitHub App:")
	cc.Writeln("  %s", installURL)
	cc.Writeln("")
	cc.Writeln("Waiting for installation...")

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

	// Store the connection.
	err = ss.server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertGitHubAccount(ctx, exedb.InsertGitHubAccountParams{
			UserID:         cc.User.ID,
			GitHubLogin:    setup.GitHubLogin,
			InstallationID: setup.InstallationID,
			AccessToken:    setup.AccessToken,
			RefreshToken:   setup.RefreshToken,
		})
	})
	if err != nil {
		return cc.Errorf("failed to save GitHub connection: %v", err)
	}

	cc.Writeln("Connected GitHub account: %s", setup.GitHubLogin)
	return nil
}

func (ss *SSHServer) handleDeleteGitHub(ctx context.Context, cc *exemenu.CommandContext) error {
	// Check if connected.
	existing, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetGitHubAccount, cc.User.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return cc.Errorf("no GitHub account connected")
	}
	if err != nil {
		return err
	}

	err = withTx1(ss.server, ctx, (*exedb.Queries).DeleteGitHubAccount, cc.User.ID)
	if err != nil {
		return err
	}

	cc.Writeln("Disconnected GitHub account: %s", existing.GitHubLogin)
	return nil
}
