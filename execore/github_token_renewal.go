package execore

import (
	"context"
	"math/rand"
	"time"

	"exe.dev/exedb"
	"exe.dev/tracing"
)

const (
	// githubTokenRenewalInterval is how often we check for tokens needing renewal.
	// Refresh tokens last ~6 months; we renew with 30 days of margin, so
	// checking once a day is plenty.
	githubTokenRenewalInterval = 24 * time.Hour
	// githubTokenRenewalBatchSize is the max number of accounts to renew per tick.
	githubTokenRenewalBatchSize = 50
)

// startGitHubTokenRenewal runs a background loop that keeps GitHub refresh
// tokens alive. Refresh tokens expire after ~6 months but are rotated on each
// use; this loop uses them periodically so they never expire. Access tokens
// are short-lived but cheap to obtain on demand via the refresh token.
func (s *Server) startGitHubTokenRenewal(ctx context.Context) {
	if !s.githubApp.Enabled() {
		return
	}

	ticker := time.NewTicker(githubTokenRenewalInterval)
	defer ticker.Stop()

	// Delay startup to avoid thundering herd after deploys.
	// Jitter of equal magnitude is added to the base delay.
	base := s.env.GitHubTokenRenewalStartupDelay
	delay := base + time.Duration(rand.Int63n(int64(base)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	s.renewGitHubTokens(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.renewGitHubTokens(ctx)
		}
	}
}

func (s *Server) renewGitHubTokens(ctx context.Context) {
	ctx = tracing.ContextWithTraceID(ctx, tracing.GenerateTraceID())

	accounts, err := withRxRes1(s, ctx, (*exedb.Queries).ListGitHubAccountsNeedingRenewal, int64(githubTokenRenewalBatchSize))
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to list GitHub accounts needing renewal", "error", err)
		return
	}

	for _, acct := range accounts {
		if acct.RefreshToken == "" {
			continue
		}

		s.githubRefreshMu.Lock()
		s.renewOneGitHubToken(ctx, acct)
		s.githubRefreshMu.Unlock()
	}
}

func (s *Server) renewOneGitHubToken(ctx context.Context, acct exedb.GithubAccount) {
	tokenResp, err := s.githubApp.RefreshUserToken(ctx, acct.RefreshToken)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to refresh GitHub token",
			"user_id", acct.UserID,
			"github_login", acct.GitHubLogin,
			"installation_id", acct.InstallationID,
			"error", err,
		)
		return
	}

	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateGitHubAccountTokens(ctx, exedb.UpdateGitHubAccountTokensParams{
			AccessToken:           tokenResp.AccessToken,
			RefreshToken:          tokenResp.RefreshToken,
			AccessTokenExpiresAt:  tokenResp.AccessTokenExpiresAt(),
			RefreshTokenExpiresAt: tokenResp.RefreshTokenExpiresAt(),
			UserID:                acct.UserID,
			InstallationID:        acct.InstallationID,
		})
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to save renewed GitHub tokens",
			"user_id", acct.UserID,
			"installation_id", acct.InstallationID,
			"error", err,
		)
		return
	}

	s.slog().InfoContext(ctx, "renewed GitHub token",
		"user_id", acct.UserID,
		"github_login", acct.GitHubLogin,
		"installation_id", acct.InstallationID,
	)
}
