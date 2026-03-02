package execore

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/oidcauth"
)

var (
	addIssuerURLFlag    = addStringFlag("issuer-url", "", "OIDC issuer URL (e.g. https://accounts.google.com)")
	addClientIDFlag     = addStringFlag("client-id", "", "OIDC client ID")
	addClientSecretFlag = addStringFlag("client-secret", "", "OIDC client secret")
	addDisplayNameFlag  = addStringFlag("display-name", "", "display name for the SSO provider")
)

func teamAuthSetFlags(base func() *flag.FlagSet) func() *flag.FlagSet {
	return addDisplayNameFlag(addClientSecretFlag(addClientIDFlag(addIssuerURLFlag(base))))
}

func (ss *SSHServer) teamAuthSubcommands() []*exemenu.Command {
	return []*exemenu.Command{
		{
			Name:              "set",
			Description:       "Set the team auth provider (default, google, oidc)",
			Usage:             "team auth set <default|google|oidc> [--issuer-url=<url> --client-id=<id> --client-secret=<secret>]",
			Handler:           ss.handleTeamAuthSetCommand,
			FlagSetFunc:       teamAuthSetFlags(jsonOnlyFlags("team-auth-set")),
			HasPositionalArgs: true,
			Available:         ss.isTeamAdmin,
		},
	}
}

// handleTeamAuthCommand shows the current auth configuration for the team.
func (ss *SSHServer) handleTeamAuthCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		return cc.Errorf("You are not part of a team")
	}

	authProvider, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamAuthProvider, team.TeamID)
	if err != nil {
		return err
	}

	provider := "default"
	if authProvider != nil {
		provider = *authProvider
	}

	result := map[string]any{
		"provider": provider,
	}

	if provider == oidcauth.ProviderName {
		ssoProvider, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamSSOProvider, team.TeamID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			sso := map[string]any{
				"issuer_url": ssoProvider.IssuerUrl,
				"client_id":  ssoProvider.ClientID,
			}
			if ssoProvider.DisplayName != nil {
				sso["display_name"] = *ssoProvider.DisplayName
			}
			result["sso"] = sso
		}
	}

	if cc.WantJSON() {
		cc.WriteJSON(result)
		return nil
	}

	cc.Writeln("Auth provider: \033[1m%s\033[0m", provider)
	if sso, ok := result["sso"].(map[string]any); ok {
		cc.Writeln("SSO issuer:    %s", sso["issuer_url"])
		cc.Writeln("SSO client ID: %s", sso["client_id"])
		if dn, ok := sso["display_name"]; ok {
			cc.Writeln("SSO name:      %s", dn)
		}
	}

	return nil
}

// handleTeamAuthSetCommand sets the team auth provider.
// For "oidc", it also configures the SSO provider (flags required).
// For "default" or "google", it clears any existing SSO configuration.
func (ss *SSHServer) handleTeamAuthSetCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 1 {
		return cc.Errorf("usage: team auth set <default|google|oidc>")
	}

	team, err := ss.server.GetTeamForUser(ctx, cc.User.ID)
	if err != nil {
		return err
	}
	if team == nil {
		return cc.Errorf("You are not part of a team")
	}
	if team.Role != "owner" {
		return cc.Errorf("Only team owners can change auth settings")
	}

	provider := strings.ToLower(cc.Args[0])
	switch provider {
	case "default":
		return ss.setTeamAuthDefault(ctx, cc, team.TeamID)
	case "google":
		return ss.setTeamAuthGoogle(ctx, cc, team.TeamID)
	case "oidc":
		return ss.setTeamAuthOIDC(ctx, cc, team.TeamID)
	default:
		return cc.Errorf("Invalid provider %q: must be default, google, or oidc", provider)
	}
}

func (ss *SSHServer) setTeamAuthDefault(ctx context.Context, cc *exemenu.CommandContext, teamID string) error {
	err := exedb.WithTx(ss.server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.SetTeamAuthProvider(ctx, exedb.SetTeamAuthProviderParams{
			AuthProvider: nil,
			TeamID:       teamID,
		}); err != nil {
			return err
		}
		if err := q.DeleteTeamSSOProvider(ctx, teamID); err != nil {
			slog.WarnContext(ctx, "failed to delete SSO provider during auth reset", "team_id", teamID, "err", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "team auth provider set", "team_id", teamID, "provider", "default", "by", cc.User.ID)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{"provider": "default", "status": "ok"})
		return nil
	}
	cc.Writeln("Auth provider set to \033[1mdefault\033[0m")
	return nil
}

func (ss *SSHServer) setTeamAuthGoogle(ctx context.Context, cc *exemenu.CommandContext, teamID string) error {
	google := "google"
	err := exedb.WithTx(ss.server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.SetTeamAuthProvider(ctx, exedb.SetTeamAuthProviderParams{
			AuthProvider: &google,
			TeamID:       teamID,
		}); err != nil {
			return err
		}
		if err := q.DeleteTeamSSOProvider(ctx, teamID); err != nil {
			slog.WarnContext(ctx, "failed to delete SSO provider during auth change", "team_id", teamID, "err", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "team auth provider set", "team_id", teamID, "provider", "google", "by", cc.User.ID)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{"provider": "google", "status": "ok"})
		return nil
	}
	cc.Writeln("Auth provider set to \033[1mgoogle\033[0m")
	return nil
}

func (ss *SSHServer) setTeamAuthOIDC(ctx context.Context, cc *exemenu.CommandContext, teamID string) error {
	issuerURL := strings.TrimRight(cc.FlagSet.Lookup("issuer-url").Value.String(), "/")
	clientID := cc.FlagSet.Lookup("client-id").Value.String()
	clientSecret := cc.FlagSet.Lookup("client-secret").Value.String()
	displayName := cc.FlagSet.Lookup("display-name").Value.String()

	if issuerURL == "" || clientID == "" || clientSecret == "" {
		return cc.Errorf("--issuer-url, --client-id, and --client-secret are required for oidc")
	}

	// Run OIDC discovery to validate the provider
	cc.Writeln("Running OIDC discovery for %s...", issuerURL)
	doc, err := oidcauth.TestConnectivity(ctx, issuerURL)
	if err != nil {
		return cc.Errorf("OIDC discovery failed: %v", err)
	}

	var dnPtr *string
	if displayName != "" {
		dnPtr = &displayName
	}

	// If updating and client_secret is masked, preserve the existing secret.
	existing, existErr := withRxRes1(ss.server, ctx, (*exedb.Queries).GetTeamSSOProvider, teamID)
	if existErr == nil && clientSecret == "***" {
		clientSecret = existing.ClientSecret
	}

	// Upsert SSO provider and set auth provider atomically.
	oidcProvider := oidcauth.ProviderName
	err = exedb.WithTx(ss.server.db, ctx, func(ctx context.Context, q *exedb.Queries) error {
		if existErr == nil {
			if err := q.UpdateTeamSSOProvider(ctx, exedb.UpdateTeamSSOProviderParams{
				IssuerUrl:    issuerURL,
				ClientID:     clientID,
				ClientSecret: clientSecret,
				DisplayName:  dnPtr,
				AuthUrl:      &doc.AuthorizationEndpoint,
				TokenUrl:     &doc.TokenEndpoint,
				UserinfoUrl:  &doc.UserinfoEndpoint,
				TeamID:       teamID,
			}); err != nil {
				return fmt.Errorf("update SSO provider: %w", err)
			}
		} else {
			if err := q.InsertTeamSSOProvider(ctx, exedb.InsertTeamSSOProviderParams{
				TeamID:       teamID,
				IssuerUrl:    issuerURL,
				ClientID:     clientID,
				ClientSecret: clientSecret,
				DisplayName:  dnPtr,
				AuthUrl:      &doc.AuthorizationEndpoint,
				TokenUrl:     &doc.TokenEndpoint,
				UserinfoUrl:  &doc.UserinfoEndpoint,
			}); err != nil {
				return fmt.Errorf("insert SSO provider: %w", err)
			}
		}
		if err := q.SetTeamAuthProvider(ctx, exedb.SetTeamAuthProviderParams{
			AuthProvider: &oidcProvider,
			TeamID:       teamID,
		}); err != nil {
			return fmt.Errorf("set auth provider: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	callbackURL := ss.server.webBaseURLNoRequest() + "/oauth/oidc/callback"

	slog.InfoContext(ctx, "team auth provider set",
		"team_id", teamID,
		"provider", "oidc",
		"issuer", issuerURL,
		"by", cc.User.ID)

	if cc.WantJSON() {
		cc.WriteJSON(map[string]any{
			"status":       "ok",
			"provider":     "oidc",
			"issuer_url":   issuerURL,
			"callback_url": callbackURL,
		})
		return nil
	}

	cc.Writeln("Auth provider set to \033[1moidc\033[0m")
	cc.Writeln("SSO issuer:    %s", issuerURL)
	cc.Writeln("Callback URL:  %s", callbackURL)
	cc.Writeln("")
	cc.Writeln("Set your IdP's redirect URI to the callback URL above.")
	return nil
}
