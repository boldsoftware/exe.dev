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
	OK        bool              `json:"ok"`
	Target    string            `json:"target,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	BasicAuth *basicAuthConfig  `json:"basic_auth,omitempty"`
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

	resp, err := s.buildProxyConfig(ctx, integration.Type, integration.Config)
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
func (s *Server) buildProxyConfig(ctx context.Context, typ, configJSON string) (integrationConfigResponse, error) {
	switch typ {
	case "http-proxy":
		return buildHTTPProxyConfig(configJSON)
	case "github":
		return s.buildGitHubProxyConfig(ctx, configJSON)
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

func (s *Server) buildGitHubProxyConfig(ctx context.Context, configJSON string) (integrationConfigResponse, error) {
	token, err := s.mintGitHubToken(ctx, configJSON)
	if err != nil {
		return integrationConfigResponse{OK: false}, err
	}

	return integrationConfigResponse{
		OK:     true,
		Target: "https://github.com",
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
func (s *Server) mintGitHubToken(ctx context.Context, configJSON string) (string, error) {
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
