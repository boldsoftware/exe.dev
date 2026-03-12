package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/wildcardcert"
	sloghttp "github.com/samber/slog-http"
	"tailscale.com/net/tsaddr"
)

// integrationConfigResponse is the JSON returned by /_/integration-config.
// It returns the integration type and raw config so that the exelet can
// handle type-specific proxy logic.
type integrationConfigResponse struct {
	OK     bool   `json:"ok"`
	Type   string `json:"type,omitempty"`
	Config string `json:"config,omitempty"`
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

	notFound := func() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(integrationConfigResponse{OK: false})
	}

	box, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).BoxNamed, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			notFound()
			return
		}
		s.slog().ErrorContext(ctx, "integration config: box lookup failed", "error", err, "vm_name", vmName)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	integration, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetIntegrationByOwnerAndName, exedb.GetIntegrationByOwnerAndNameParams{
		OwnerUserID: box.CreatedByUserID,
		Name:        integrationName,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			notFound()
			return
		}
		s.slog().ErrorContext(ctx, "integration config: lookup failed", "error", err, "vm_name", vmName, "integration", integrationName)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if !exedb.IntegrationMatchesBox(&integration, &box) {
		notFound()
		return
	}

	sloghttp.AddCustomAttributes(r, slog.String("integration", integrationName))
	sloghttp.AddCustomAttributes(r, slog.String("integration_type", integration.Type))
	sloghttp.AddCustomAttributes(r, slog.Int("box_id", box.ID))

	config := integration.Config

	// For GitHub integrations, enrich the config with a fresh installation access token.
	if integration.Type == "github" {
		enriched, err := s.enrichGitHubConfig(ctx, config)
		if err != nil {
			s.slog().ErrorContext(ctx, "integration config: failed to enrich github config", "error", err)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(integrationConfigResponse{OK: false})
			return
		}
		config = enriched
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(integrationConfigResponse{
		OK:     true,
		Type:   integration.Type,
		Config: config,
	})
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

// enrichGitHubConfig parses a github integration config, mints (or retrieves
// from cache) an installation access token, and returns the config JSON with
// a "token" field added.
func (s *Server) enrichGitHubConfig(ctx context.Context, configJSON string) (string, error) {
	var cfg githubIntegrationConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return "", err
	}

	key := ghTokenCacheKey{InstallationID: cfg.InstallationID, Repositories: strings.Join(cfg.Repositories, ",")}

	s.ghTokenCacheMu.Lock()
	entry, ok := s.ghTokenCache[key]
	s.ghTokenCacheMu.Unlock()

	// Use cached token if it's still valid with a 10 minute buffer.
	if ok && time.Until(entry.ExpiresAt) > 10*time.Minute {
		return marshalEnrichedConfig(cfg, entry.Token)
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

	return marshalEnrichedConfig(cfg, iat.Token)
}

// githubIntegrationConfigWithToken is the enriched config sent to exelets.
type githubIntegrationConfigWithToken struct {
	Repositories   []string `json:"repositories"`
	InstallationID int64    `json:"installation_id"`
	Token          string   `json:"token"`
}

func marshalEnrichedConfig(cfg githubIntegrationConfig, token string) (string, error) {
	enriched := githubIntegrationConfigWithToken{
		Repositories:   cfg.Repositories,
		InstallationID: cfg.InstallationID,
		Token:          token,
	}
	b, err := json.Marshal(enriched)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// handleIntegrationCert serves GET /_/integration-cert.
// Exelets call this to fetch the wildcard TLS certificate for *.int.{BoxHost}.
func (s *Server) handleIntegrationCert(w http.ResponseWriter, r *http.Request) {
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

	// Request a cert for any name under int.{BoxHost}; the wildcard cert
	// covers all of *.int.{BoxHost}.
	serverName := "integration-cert." + s.env.BoxSub("int")
	cert, err := s.wildcardCertManager.GetCertificate(serverName)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "integration cert: GetCertificate failed", "error", err)
		http.Error(w, "failed to get certificate", http.StatusInternalServerError)
		return
	}

	pem, err := wildcardcert.EncodeCertificate(cert)
	if err != nil {
		s.slog().ErrorContext(r.Context(), "integration cert: EncodeCertificate failed", "error", err)
		http.Error(w, "failed to encode certificate", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Write(pem)
}
