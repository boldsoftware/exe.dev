package execore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"

	"exe.dev/domz"
	"exe.dev/exedb"
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

	integration, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetAttachedIntegrationByOwnerNameAndBoxID, exedb.GetAttachedIntegrationByOwnerNameAndBoxIDParams{
		OwnerUserID: box.CreatedByUserID,
		Name:        integrationName,
		BoxID:       int64(box.ID),
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

	sloghttp.AddCustomAttributes(r, slog.String("integration", integrationName))
	sloghttp.AddCustomAttributes(r, slog.String("integration_type", integration.Type))
	sloghttp.AddCustomAttributes(r, slog.Int("box_id", box.ID))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(integrationConfigResponse{
		OK:     true,
		Type:   integration.Type,
		Config: integration.Config,
	})
}
