package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/wildcardcert"
	sloghttp "github.com/samber/slog-http"
	"tailscale.com/net/tsaddr"
)

// integrationConfigResponse is the JSON returned by /_/integration-config.
// It returns a generic proxy configuration so the exelet can proxy the
// request without any type-specific logic.
type integrationConfigResponse struct {
	OK                  bool              `json:"ok"`
	Target              string            `json:"target,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	BasicAuth           *basicAuthConfig  `json:"basic_auth,omitempty"`
	AllowedPathPrefixes []string          `json:"allowed_path_prefixes,omitempty"`

	// GatewayPath, when set, tells the exelet to forward the request to
	// exed at this path (with X-Exedev-Box) instead of proxying to an
	// external target.
	GatewayPath string `json:"gateway_path,omitempty"`
}

type basicAuthConfig struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// handleIntegrationConfig serves GET /_/integration-config?vm_name={name}&integration={name}.
// Exelets call this to look up an integration's proxy configuration.
func (s *Server) handleIntegrationConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Security: only accept from Tailscale IPs or in GatewayDev mode.
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !s.env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	vmName := r.URL.Query().Get("vm_name")
	integrationName := r.URL.Query().Get("integration")
	if vmName == "" || integrationName == "" {
		http.Error(w, "missing vm_name or integration parameter", http.StatusBadRequest)
		return
	}

	sloghttp.AddCustomAttributes(r, slog.String("vm_name", vmName))
	sloghttp.AddCustomAttributes(r, slog.String("integration", integrationName))

	notFound := func(reason string) {
		sloghttp.AddCustomAttributes(r, slog.String("integration_result", reason))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(integrationConfigResponse{OK: false})
	}

	box, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).BoxNamed, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			notFound("box_not_found")
			return
		}
		s.slog().ErrorContext(ctx, "integration config: box lookup failed", "error", err, "vm_name", vmName)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	sloghttp.AddCustomAttributes(r, slog.Int("box_id", box.ID))

	// "notify" is a synthetic integration that exists for every user,
	// attached to all VMs. It forwards push notifications to exed.
	if integrationName == "notify" {
		sloghttp.AddCustomAttributes(r, slog.String("integration_type", "notify"))
		sloghttp.AddCustomAttributes(r, slog.String("integration_result", "ok"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(integrationConfigResponse{
			OK:          true,
			GatewayPath: "/_/gateway/push/send",
		})
		return
	}

	integration, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetIntegrationByOwnerAndName, exedb.GetIntegrationByOwnerAndNameParams{
		OwnerUserID: box.CreatedByUserID,
		Name:        integrationName,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			notFound("integration_not_found")
			return
		}
		s.slog().ErrorContext(ctx, "integration config: lookup failed", "error", err, "vm_name", vmName, "integration", integrationName)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if !exedb.IntegrationMatchesBox(&integration, &box) {
		notFound("not_attached")
		return
	}

	sloghttp.AddCustomAttributes(r, slog.String("integration_type", integration.Type))
	sloghttp.AddCustomAttributes(r, slog.String("integration_result", "ok"))

	resp, err := s.buildProxyConfig(ctx, box.CreatedByUserID, integration.Type, integration.Config)
	if err != nil {
		s.slog().ErrorContext(ctx, "integration config: failed to build proxy config", "error", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(integrationConfigResponse{OK: false})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// buildProxyConfig transforms a type-specific integration config into a
// generic proxy configuration that the exelet can apply without knowing
// about integration types.
func (s *Server) buildProxyConfig(ctx context.Context, ownerUserID, typ, configJSON string) (integrationConfigResponse, error) {
	switch typ {
	case "http-proxy":
		return buildHTTPProxyConfig(configJSON)
	case "github":
		return s.buildGitHubProxyConfig(ctx, ownerUserID, configJSON)
	default:
		return integrationConfigResponse{OK: false}, errors.New("unsupported integration type: " + typ)
	}
}

func buildHTTPProxyConfig(configJSON string) (integrationConfigResponse, error) {
	var cfg httpProxyConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return integrationConfigResponse{OK: false}, err
	}

	resp := integrationConfigResponse{
		OK:      true,
		Target:  cfg.Target,
		Headers: make(map[string]string),
	}

	if name, value, ok := strings.Cut(cfg.Header, ":"); ok {
		resp.Headers[name] = strings.TrimSpace(value)
	}

	// Extract URL credentials as basic auth and strip them from the target.
	if u, err := url.Parse(cfg.Target); err == nil && u.User != nil {
		pass, _ := u.User.Password()
		resp.BasicAuth = &basicAuthConfig{User: u.User.Username(), Pass: pass}
		u.User = nil
		resp.Target = u.String()
	}

	return resp, nil
}

func (s *Server) buildGitHubProxyConfig(ctx context.Context, ownerUserID, configJSON string) (integrationConfigResponse, error) {
	var cfg githubIntegrationConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return integrationConfigResponse{OK: false}, err
	}

	// Resolve the current installation ID from github_accounts rather than
	// using the potentially stale value baked into the integration config.
	// When a GitHub App is reinstalled, it gets a new installation ID;
	// the github_accounts table is updated by "integrations setup github"
	// but existing integration configs are not.
	if len(cfg.Repositories) > 0 {
		repoOwner, _, _ := strings.Cut(cfg.Repositories[0], "/")
		if repoOwner != "" {
			ghInstall, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetGitHubInstallationByTarget, exedb.GetGitHubInstallationByTargetParams{
				UserID:             ownerUserID,
				GitHubAccountLogin: repoOwner,
			})
			if err == nil && ghInstall.GitHubAppInstallationID != cfg.InstallationID {
				s.slog().InfoContext(ctx, "integration config: resolved updated installation ID",
					"old_installation_id", cfg.InstallationID,
					"new_installation_id", ghInstall.GitHubAppInstallationID,
					"target", repoOwner,
				)
				cfg.InstallationID = ghInstall.GitHubAppInstallationID
			}
		}
	}

	token, err := s.mintGitHubToken(ctx, cfg)
	if err != nil {
		return integrationConfigResponse{OK: false}, err
	}

	// Build allowed path prefixes from the configured repositories.
	// Each repo "owner/repo" allows paths starting with "/owner/repo".
	var prefixes []string
	for _, repo := range cfg.Repositories {
		prefixes = append(prefixes, "/"+repo)
	}

	return integrationConfigResponse{
		OK:                  true,
		Target:              "https://github.com",
		AllowedPathPrefixes: prefixes,
		BasicAuth: &basicAuthConfig{
			User: "x-access-token",
			Pass: token,
		},
	}, nil
}

// ghTokenCacheKey identifies a cached GitHub installation access token.
type ghTokenCacheKey struct {
	InstallationID int64
	Repositories   string // comma-joined repo full names
}

// ghTokenCacheEntry holds a cached installation access token.
type ghTokenCacheEntry struct {
	Token     string
	ExpiresAt time.Time
}

// mintGitHubToken returns a cached or freshly-minted installation access token.
func (s *Server) mintGitHubToken(ctx context.Context, cfg githubIntegrationConfig) (string, error) {
	key := ghTokenCacheKey{InstallationID: cfg.InstallationID, Repositories: strings.Join(cfg.Repositories, ",")}

	s.ghTokenCacheMu.Lock()
	entry, ok := s.ghTokenCache[key]
	s.ghTokenCacheMu.Unlock()

	// Use cached token if it's still valid with a 10 minute buffer.
	if ok && time.Until(entry.ExpiresAt) > 10*time.Minute {
		return entry.Token, nil
	}

	// Mint a new token.
	iat, err := s.githubApp.MintInstallationToken(ctx, cfg.InstallationID, cfg.Repositories)
	if err != nil {
		return "", err
	}

	s.ghTokenCacheMu.Lock()
	s.ghTokenCache[key] = &ghTokenCacheEntry{
		Token:     iat.Token,
		ExpiresAt: iat.ExpiresAt,
	}
	s.ghTokenCacheMu.Unlock()

	return iat.Token, nil
}

// handleIntegrationCert serves GET /_/integration-cert.
// Exelets call this to fetch the wildcard TLS certificate for *.int.{BoxHost}.
func (s *Server) handleIntegrationCert(w http.ResponseWriter, r *http.Request) {
	s.serveIntegrationCert(w, r, "int")
}

// handleTeamIntegrationCert serves GET /_/team-integration-cert.
// Exelets call this to fetch the wildcard TLS certificate for *.team-int.{BoxHost}.
func (s *Server) handleTeamIntegrationCert(w http.ResponseWriter, r *http.Request) {
	s.serveIntegrationCert(w, r, "team-int")
}

func (s *Server) serveIntegrationCert(w http.ResponseWriter, r *http.Request, domain string) {
	// Security: only accept from Tailscale IPs or in GatewayDev mode.
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !s.env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if s.wildcardCertManager == nil {
		http.Error(w, "no wildcard certificate manager", http.StatusServiceUnavailable)
		return
	}

	// Request a cert for a name under {domain}.{BoxHost}; the wildcard cert
	// covers all of *.{domain}.{BoxHost}.
	serverName := "integration-cert." + s.env.BoxSub(domain)
	cert, err := s.wildcardCertManager.GetCertificate(serverName)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "integration cert: GetCertificate failed", "domain", domain, "error", err)
		http.Error(w, "failed to get certificate", http.StatusInternalServerError)
		return
	}

	pem, err := wildcardcert.EncodeCertificate(cert)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "integration cert: EncodeCertificate failed", "domain", domain, "error", err)
		http.Error(w, "failed to encode certificate", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Write(pem)
}
