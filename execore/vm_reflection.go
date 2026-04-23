package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"exe.dev/exedb"
)

// handleVMReflection serves requests routed through /_/gateway/reflection.
// It is invoked by the exelet for integrations of type "reflection".
// Supported paths (gated by the integration config's Fields list):
//
//	/               index of enabled endpoints
//	/email          the owner's email address
//	/integrations   list of the owner's integrations attached to the VM
//	/tags           the VM's tags
//
// Authentication: trusted via X-Exedev-Box (requires Tailscale IP, enforced
// by the route wrapper) and X-Exedev-Integration identifies which reflection
// integration the request came in through.
func (s *Server) handleVMReflection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	vmName := r.Header.Get("X-Exedev-Box")
	integrationName := r.Header.Get("X-Exedev-Integration")
	if vmName == "" || integrationName == "" {
		http.Error(w, "missing required headers", http.StatusBadRequest)
		return
	}

	box, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).BoxNamed, vmName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "box not found", http.StatusNotFound)
			return
		}
		s.slog().ErrorContext(ctx, "reflection: box lookup failed", "error", err, "vm_name", vmName)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Look up the integration (personal first, then team).
	ig, isTeam, err := s.lookupReflectionIntegration(ctx, &box, integrationName)
	if err != nil {
		http.Error(w, "integration not found", http.StatusNotFound)
		return
	}
	if !exedb.IntegrationMatchesBox(&ig, &box) {
		http.Error(w, "integration not attached", http.StatusForbidden)
		return
	}
	if ig.Type != "reflection" {
		http.Error(w, "not a reflection integration", http.StatusBadRequest)
		return
	}

	var cfg reflectionIntegrationConfig
	if err := json.Unmarshal([]byte(ig.Config), &cfg); err != nil {
		s.slog().ErrorContext(ctx, "reflection: bad config", "error", err, "integration", integrationName)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	fieldEnabled := func(name string) bool { return slices.Contains(cfg.Fields, name) }

	// Route based on original request path.
	origPath := r.Header.Get("X-Exedev-Original-Path")
	if origPath == "" {
		origPath = "/"
	}
	// Strip trailing slash except for root.
	p := strings.TrimSuffix(origPath, "/")
	if p == "" {
		p = "/"
	}

	// Owner lookup is shared by email and integrations.
	ownerUserID := box.CreatedByUserID

	switch p {
	case "/":
		s.writeReflectionIndex(w, cfg.Fields)
	case "/email":
		if !fieldEnabled(reflectionFieldEmail) {
			http.Error(w, "email field not enabled", http.StatusForbidden)
			return
		}
		user, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetUserWithDetails, ownerUserID)
		if err != nil {
			s.slog().ErrorContext(ctx, "reflection: user lookup failed", "error", err, "user_id", ownerUserID)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		writeReflectionJSON(w, map[string]any{"email": user.Email})
	case "/tags":
		if !fieldEnabled(reflectionFieldTags) {
			http.Error(w, "tags field not enabled", http.StatusForbidden)
			return
		}
		tags := box.GetTags()
		if tags == nil {
			tags = []string{}
		}
		writeReflectionJSON(w, map[string]any{"tags": tags})
	case "/integrations":
		if !fieldEnabled(reflectionFieldIntegrations) {
			http.Error(w, "integrations field not enabled", http.StatusForbidden)
			return
		}
		items, err := s.buildReflectionIntegrationList(ctx, &box, isTeam)
		if err != nil {
			s.slog().ErrorContext(ctx, "reflection: list integrations failed", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		writeReflectionJSON(w, map[string]any{"integrations": items})
	default:
		http.NotFound(w, r)
	}
}

// lookupReflectionIntegration finds an integration by name on the VM,
// trying personal integrations first and then team integrations.
func (s *Server) lookupReflectionIntegration(ctx context.Context, box *exedb.Box, name string) (exedb.Integration, bool, error) {
	ig, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetIntegrationByOwnerAndName, exedb.GetIntegrationByOwnerAndNameParams{
		OwnerUserID: box.CreatedByUserID,
		Name:        name,
	})
	if err == nil {
		return ig, false, nil
	}
	team, terr := s.GetTeamForUser(ctx, box.CreatedByUserID)
	if terr != nil || team == nil {
		return exedb.Integration{}, false, err
	}
	ig2, err2 := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).GetIntegrationByTeamAndName, exedb.GetIntegrationByTeamAndNameParams{
		TeamID: &team.TeamID,
		Name:   name,
	})
	if err2 != nil {
		return exedb.Integration{}, false, err2
	}
	return ig2, true, nil
}

// buildReflectionIntegrationList returns the set of integrations available to
// the given VM (i.e. those whose attachments match the box). It includes both
// personal integrations for the box owner and any team integrations visible
// to that owner.
func (s *Server) buildReflectionIntegrationList(ctx context.Context, box *exedb.Box, includeTeamFromSameTeam bool) ([]map[string]any, error) {
	personal, err := exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).ListIntegrationsByUser, box.CreatedByUserID)
	if err != nil {
		return nil, err
	}

	var team []exedb.Integration
	if t, _ := s.GetTeamForUser(ctx, box.CreatedByUserID); t != nil {
		team, _ = exedb.WithRxRes1(s.db, ctx, (*exedb.Queries).ListIntegrationsByTeam, &t.TeamID)
	}
	_ = includeTeamFromSameTeam

	items := make([]map[string]any, 0, len(personal)+len(team))
	add := func(ig exedb.Integration, isTeam bool) {
		if !exedb.IntegrationMatchesBox(&ig, box) {
			return
		}
		entry := map[string]any{
			"name":    ig.Name,
			"type":    ig.Type,
			"help":    reflectionHelpFor(ig),
			"comment": ig.Comment,
		}
		if isTeam {
			entry["team"] = true
		}
		items = append(items, entry)
	}
	for _, ig := range personal {
		add(ig, false)
	}
	for _, ig := range team {
		add(ig, true)
	}
	return items, nil
}

// reflectionHelpFor returns a short human-readable usage hint for an
// integration (e.g. "curl http://foo.int.exe.cloud/" or "git clone ...").
//
// Note: this is an advisory string only. The exact host depends on the VM's
// stage; we use ".int.exe.cloud" as a stable display host (matching what
// the exelet resolves internally) rather than reaching into BoxHost.
func reflectionHelpFor(ig exedb.Integration) string {
	host := ig.Name + ".int.exe.cloud"
	if ig.IsTeam() {
		host = ig.Name + ".team.exe.cloud"
	}
	switch ig.Type {
	case "http-proxy":
		return fmt.Sprintf("curl http://%s/", host)
	case "github":
		var cfg githubIntegrationConfig
		if err := json.Unmarshal([]byte(ig.Config), &cfg); err == nil && len(cfg.Repositories) > 0 {
			return fmt.Sprintf("git clone http://%s/%s.git", host, cfg.Repositories[0])
		}
		return fmt.Sprintf("git clone http://%s/<owner>/<repo>.git", host)
	case "reflection":
		return fmt.Sprintf("curl http://%s/", host)
	case "notify":
		return "push notifications to device"
	}
	return ""
}

func (s *Server) writeReflectionIndex(w http.ResponseWriter, fields []string) {
	paths := []map[string]string{}
	for _, f := range fields {
		switch f {
		case reflectionFieldEmail:
			paths = append(paths, map[string]string{"path": "/email", "description": "owner email address"})
		case reflectionFieldIntegrations:
			paths = append(paths, map[string]string{"path": "/integrations", "description": "integrations available to this VM"})
		case reflectionFieldTags:
			paths = append(paths, map[string]string{"path": "/tags", "description": "tags set on this VM"})
		}
	}
	writeReflectionJSON(w, map[string]any{"paths": paths})
}

func writeReflectionJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}
