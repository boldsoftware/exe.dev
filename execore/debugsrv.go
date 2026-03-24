package execore

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"html"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/entitlement"
	"exe.dev/billing/tender"
	"exe.dev/email"
	"exe.dev/execore/debug_templates"
	"exe.dev/exedb"
	"exe.dev/exedebug"
	exeletclient "exe.dev/exelet/client"
	"exe.dev/exeweb"
	"exe.dev/llmgateway"
	"exe.dev/logging"
	"exe.dev/oidcauth"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
	"exe.dev/publicips"
	"exe.dev/region"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"tailscale.com/client/local"
)

// debugHandler constructs and returns a handler with Go-standard debug endpoints
// (pprof, expvar). Creating this handler is cheap and avoids global state.
func (s *Server) debugHandler() http.Handler {
	mux := http.NewServeMux()

	// index & aux
	mux.HandleFunc("/debug", s.handleDebugIndex)
	mux.HandleFunc("/debug/", s.handleDebugIndex)
	mux.HandleFunc("/debug/gitsha", s.handleDebugGitsha)
	mux.HandleFunc("/debug/vms", s.handleDebugBoxes)
	mux.HandleFunc("GET /debug/vmlist", s.handleDebugVMList)
	mux.HandleFunc("GET /debug/jump", s.handleDebugJump)
	mux.HandleFunc("/debug/vms/{name}", s.handleDebugBoxDetails)
	mux.HandleFunc("GET /debug/vms/{name}/logs", s.handleDebugBoxLogs)
	mux.HandleFunc("POST /debug/vms/flush-proxy-cache", s.handleDebugBoxFlushProxyCache)
	mux.HandleFunc("POST /debug/vms/delete", s.handleDebugBoxDelete)
	mux.HandleFunc("POST /debug/vms/stop", s.handleDebugBoxStop)
	mux.HandleFunc("POST /debug/vms/start", s.handleDebugBoxStart)
	mux.HandleFunc("GET /debug/vms/migrate", s.handleDebugBoxMigrateForm)
	mux.HandleFunc("POST /debug/vms/migrate", s.handleDebugBoxMigrate)
	mux.HandleFunc("GET /debug/migrate", s.handleDebugMassMigrateForm)
	mux.HandleFunc("GET /debug/migrate/vms", s.handleDebugMassMigrateBoxes)
	mux.HandleFunc("POST /debug/migrate", s.handleDebugMassMigrate)
	mux.HandleFunc("/debug/users", s.handleDebugUsers)
	mux.HandleFunc("/debug/user", s.handleDebugUser)
	mux.HandleFunc("GET /debug/billing", s.handleDebugBilling)
	mux.HandleFunc("POST /debug/user/give-invites", s.handleDebugUserGiveInvites)
	mux.HandleFunc("POST /debug/user/migrate-region", s.handleDebugUserMigrateRegion)
	mux.HandleFunc("POST /debug/user/migrate-vms", s.handleDebugUserMigrateVMs)
	mux.HandleFunc("POST /debug/user/cold-migrate-vm", s.handleDebugUserColdMigrateVM)
	mux.HandleFunc("POST /debug/users/toggle-root-support", s.handleDebugToggleRootSupport)
	mux.HandleFunc("POST /debug/users/toggle-vm-creation", s.handleDebugToggleVMCreation)
	mux.HandleFunc("POST /debug/users/toggle-lockout", s.handleDebugToggleLockout)
	mux.HandleFunc("POST /debug/users/update-credit", s.handleDebugUpdateUserCredit)
	mux.HandleFunc("POST /debug/users/gift-credits", s.handleDebugGiftCredits)
	mux.HandleFunc("POST /debug/users/add-billing", s.handleDebugAddBilling)
	mux.HandleFunc("POST /debug/users/set-limits", s.handleDebugSetUserLimits)
	mux.HandleFunc("POST /debug/users/delete", s.handleDebugDeleteUser)
	mux.HandleFunc("POST /debug/users/rename-email", s.handleDebugRenameUserEmail)
	mux.HandleFunc("/debug/exelets", s.handleDebugExelets)
	mux.HandleFunc("POST /debug/exelets/set-preferred", s.handleDebugSetPreferredExelet)
	mux.HandleFunc("POST /debug/exelets/recover", s.handleDebugExeletRecover)
	mux.HandleFunc("/debug/new-throttle", s.handleDebugNewThrottle)
	mux.HandleFunc("POST /debug/new-throttle", s.handleDebugNewThrottlePost)
	mux.HandleFunc("GET /debug/signup-controls", s.handleDebugSignupControls)
	mux.HandleFunc("POST /debug/signup-limiter", s.handleDebugSignupLimiterPost)
	mux.HandleFunc("POST /debug/signup-pow", s.handleDebugSignupPOWPost)
	mux.HandleFunc("POST /debug/ip-abuse-filter", s.handleDebugIPAbuseFilterPost)
	mux.HandleFunc("/debug/signup-reject", s.handleDebugSignupReject)
	mux.HandleFunc("POST /debug/signup-reject", s.handleDebugSignupRejectPost)
	mux.HandleFunc("/debug/ipshards", s.handleDebugIPShards)
	mux.HandleFunc("POST /debug/ipshards/toggle", s.handleDebugIPShardsToggle)
	mux.HandleFunc("POST /debug/ipshards/latitude", s.handleDebugIPShardsLatitude)
	mux.HandleFunc("POST /debug/ipshards/netactuate", s.handleDebugIPShardsNetActuate)
	mux.HandleFunc("GET /debug/log", s.handleDebugLogForm)
	mux.HandleFunc("POST /debug/log", s.handleDebugLog)
	mux.HandleFunc("/debug/testimonials", s.handleDebugTestimonials)
	mux.HandleFunc("GET /debug/email", s.handleDebugEmailForm)
	mux.HandleFunc("POST /debug/email", s.handleDebugEmailSend)
	mux.HandleFunc("/debug/invite", s.handleDebugInvite)
	mux.HandleFunc("POST /debug/invite", s.handleDebugInvitePost)
	mux.HandleFunc("POST /debug/invite/bulk", s.handleDebugInviteBulkPost)
	mux.HandleFunc("/debug/all-invite-codes", s.handleDebugAllInviteCodes)
	mux.HandleFunc("/debug/invite-tree", s.handleDebugInviteTree)
	mux.HandleFunc("/debug/bounces", s.handleDebugBounces)
	mux.HandleFunc("POST /debug/bounces", s.handleDebugBouncesPost)
	mux.HandleFunc("GET /debug/teams", s.handleDebugTeams)
	mux.HandleFunc("POST /debug/teams/create", s.handleDebugTeamCreate)
	mux.HandleFunc("POST /debug/teams/add-member", s.handleDebugTeamAddMember)
	mux.HandleFunc("GET /debug/teams/members", s.handleDebugTeamMembers)
	mux.HandleFunc("POST /debug/teams/remove-member", s.handleDebugTeamRemoveMember)
	mux.HandleFunc("GET /debug/teams/member-vm-count", s.handleDebugTeamMemberVMCount)
	mux.HandleFunc("POST /debug/teams/update-role", s.handleDebugTeamUpdateRole)
	mux.HandleFunc("POST /debug/teams/set-limits", s.handleDebugTeamSetLimits)
	mux.HandleFunc("POST /debug/teams/set-auth-provider", s.handleDebugTeamSetAuthProvider)
	mux.HandleFunc("POST /debug/teams/set-sso", s.handleDebugTeamSetSSO)
	mux.HandleFunc("POST /debug/teams/delete-sso", s.handleDebugTeamDeleteSSO)
	mux.HandleFunc("POST /debug/teams/test-sso", s.handleDebugTeamTestSSO)
	mux.HandleFunc("GET /debug/integrations", s.handleDebugIntegrations)
	mux.HandleFunc("GET /debug/github-integrations", s.handleDebugGitHubIntegrations)
	mux.HandleFunc("POST /debug/github-integrations/refresh", s.handleDebugGitHubIntegrationsRefresh)
	mux.HandleFunc("GET /debug/ideas", s.handleDebugTemplateReview)
	mux.HandleFunc("POST /debug/ideas", s.handleDebugTemplateReviewPost)
	mux.HandleFunc("GET /debug/regions", s.handleDebugRegions)
	mux.HandleFunc("GET /debug/usage-api", s.handleDebugUsageAPI)

	// SQL query stream
	mux.Handle("GET /debug/sql", &s.db.Sniff)

	// pprof endpoints
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// expvar at /debug/vars
	mux.Handle("/debug/vars", expvar.Handler())

	// Metrics are served at /metrics; no duplicate handler here.

	return mux
}

// handleDebug gates access to debug endpoints: allowed when the
// request originates from a Tailscale IP or loopback.
func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	s.debugHandler().ServeHTTP(w, r)
}

// handleDebugIndex renders a simple HTML index of debug endpoints.
func (s *Server) handleDebugIndex(w http.ResponseWriter, r *http.Request) {
	commit := logging.GitCommit()
	displayCommit := exedebug.DisplayCommit(commit)

	data := struct {
		Stage      string
		StageColor string
		GitCommit  string
		GitHubLink template.HTML
	}{
		Stage:      s.env.DebugLabel,
		StageColor: s.env.DebugColor,
		GitCommit:  displayCommit,
		GitHubLink: exedebug.GitHubLink(commit),
	}

	s.renderDebugTemplate(r.Context(), w, "index.html", data)
}

func (s *Server) handleDebugGitsha(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, logging.GitCommit())
}

// handleDebugBoxes returns the list of container hosts and their containers
func (s *Server) handleDebugBoxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// source=db (default) or source=exelets
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "db"
	}

	// For HTML requests, return the page shell immediately.
	// DataTables will load data via AJAX from the JSON endpoint.
	if r.URL.Query().Get("format") != "json" {
		// Build the navigation links
		var sourceNav template.HTML
		if source == "exelets" {
			sourceNav = `<strong>exelets</strong> | <a href="/debug/vms?source=db">db</a>`
		} else {
			sourceNav = `<a href="/debug/vms?source=exelets">exelets</a> | <strong>db</strong>`
		}

		data := struct {
			Source    string
			SourceNav template.HTML
		}{
			Source:    source,
			SourceNav: sourceNav,
		}

		s.renderDebugTemplate(ctx, w, "boxes.html", data)
		return
	}

	// JSON format requested
	type boxInfo struct {
		Host                 string `json:"host"`
		ID                   string `json:"id,omitempty"`
		Name                 string `json:"name"`
		Status               string `json:"status"`
		OwnerUserID          string `json:"owner_user_id,omitempty"`
		OwnerEmail           string `json:"owner_email,omitempty"`
		Region               string `json:"region"`
		SupportAccessAllowed bool   `json:"support_access_allowed"`
	}

	var boxes []boxInfo

	if source == "exelets" {
		// Fetch from exelet hosts
		ownerCache := make(map[string]exedb.GetBoxOwnerByContainerIDRow)
		getOwner := func(ctx context.Context, containerID string) (exedb.GetBoxOwnerByContainerIDRow, error) {
			if containerID == "" {
				return exedb.GetBoxOwnerByContainerIDRow{}, fmt.Errorf("empty container ID")
			}
			if owner, ok := ownerCache[containerID]; ok {
				return owner, nil
			}
			owner, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxOwnerByContainerID, &containerID)
			if errors.Is(err, sql.ErrNoRows) {
				return exedb.GetBoxOwnerByContainerIDRow{}, fmt.Errorf("container %q not present in database", containerID)
			}
			if err != nil {
				return exedb.GetBoxOwnerByContainerIDRow{}, fmt.Errorf("failed to look up owner for container %q: %w", containerID, err)
			}
			ownerCache[containerID] = owner
			return owner, nil
		}

		for addr, ec := range s.exeletClients {
			stream, err := ec.client.ListInstances(ctx, &computeapi.ListInstancesRequest{})
			if err != nil {
				s.slog().ErrorContext(ctx, "failed to list instances", "host", addr, "error", err)
				continue
			}
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					s.slog().ErrorContext(ctx, "failed to receive instance", "host", addr, "error", err)
					break
				}
				inst := resp.Instance
				info := boxInfo{
					Host:   addr,
					ID:     inst.ID,
					Name:   inst.Name,
					Status: inst.State.String(),
					Region: ec.region.Code,
				}
				if owner, err := getOwner(ctx, inst.ID); err == nil {
					info.OwnerUserID = owner.UserID
					info.OwnerEmail = owner.Email
					info.SupportAccessAllowed = owner.SupportAccessAllowed == 1
				} else {
					s.slog().WarnContext(ctx, "failed to resolve box owner", "boxName", inst.Name, "instanceID", inst.ID, "error", err)
				}
				boxes = append(boxes, info)
			}
		}
	} else {
		// Fetch from database (default)
		dbBoxes, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllBoxesWithOwner)
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to list boxes from database", "error", err)
			http.Error(w, "failed to list VMs", http.StatusInternalServerError)
			return
		}
		for _, b := range dbBoxes {
			info := boxInfo{
				Host:                 b.Ctrhost,
				Name:                 b.Name,
				Status:               b.Status,
				OwnerUserID:          b.OwnerUserID,
				OwnerEmail:           b.OwnerEmail,
				Region:               b.Region,
				SupportAccessAllowed: b.SupportAccessAllowed == 1,
			}
			if b.ContainerID != nil {
				info.ID = *b.ContainerID
			}
			boxes = append(boxes, info)
		}
	}

	// Sort by name for consistent display
	sort.Slice(boxes, func(i, j int) bool {
		return boxes[i].Name < boxes[j].Name
	})

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(boxes); err != nil {
		s.slog().InfoContext(ctx, "Failed to encode boxes", "error", err)
	}
}

// handleDebugBoxFlushProxyCache flushes all exeprox caches for a box
// (routing + shares). This is a nuclear option for manual debug use.
func (s *Server) handleDebugBoxFlushProxyCache(w http.ResponseWriter, r *http.Request) {
	boxName := r.FormValue("box_name")
	if boxName == "" {
		http.Error(w, "box_name is required", http.StatusBadRequest)
		return
	}

	proxyChangeDeletedBox(boxName)
	s.slog().InfoContext(r.Context(), "flushed all proxy caches via debug page", "box", boxName)

	http.Redirect(w, r, "/debug/vms/"+url.PathEscape(boxName), http.StatusSeeOther)
}

// handleDebugBoxDelete handles deletion of a box from the debug page.
func (s *Server) handleDebugBoxDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	boxName := r.FormValue("box_name")
	confirmName := r.FormValue("confirm_name")

	if boxName == "" {
		http.Error(w, "box_name is required", http.StatusBadRequest)
		return
	}

	if boxName != confirmName {
		http.Error(w, "confirmation name does not match", http.StatusBadRequest)
		return
	}

	// Look up the box (without owner restriction - this is an admin page)
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("/debug/vms: failed to look up VM by name: %v", err), http.StatusInternalServerError)
		return
	}

	// Delete the box using the same logic as the REPL `rm` command
	if err := s.deleteBox(ctx, box); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete box: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "box deleted via debug page", "box", boxName)

	// Redirect back to the boxes page
	http.Redirect(w, r, "/debug/vms", http.StatusSeeOther)
}

// handleDebugBoxStop stops a running box via the exelet gRPC API.
func (s *Server) handleDebugBoxStop(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	boxName := r.FormValue("box_name")
	if boxName == "" {
		http.Error(w, "box_name is required", http.StatusBadRequest)
		return
	}

	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up box: %v", err), http.StatusInternalServerError)
		return
	}

	if err := s.stopBox(ctx, box); err != nil {
		http.Error(w, fmt.Sprintf("failed to stop box: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "box stopped via debug page", "box", boxName)
	http.Redirect(w, r, "/debug/vms", http.StatusSeeOther)
}

// stopUserBoxes stops all running boxes belonging to a user.
func (s *Server) stopUserBoxes(ctx context.Context, userID string) error {
	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		return fmt.Errorf("failed to list boxes for user: %w", err)
	}

	for _, box := range boxes {
		if box.Status != "running" {
			continue
		}
		if err := s.stopBox(ctx, box); err != nil {
			s.slog().WarnContext(ctx, "failed to stop box during user lockout", "box", box.Name, "error", err)
		} else {
			s.slog().InfoContext(ctx, "box stopped due to user lockout", "box", box.Name, "user_id", userID)
		}
	}
	return nil
}

// handleDebugVMList returns container IDs as plain text, one per line,
// excluding boxes belonging to locked-out users. Designed for shell loops:
//
//	for vm in $(curl http://exed/debug/vmlist?host=tcp://HOST:9080); do
//	    ./exelet-ctl -a tcp://HOST:9080 compute instances start $vm
//	done
func (s *Server) handleDebugVMList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	dbBoxes, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllBoxesWithOwner)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list VMs: %v", err), http.StatusInternalServerError)
		return
	}

	lockedOutCache := make(map[string]bool)
	isLocked := func(userID string) bool {
		locked, ok := lockedOutCache[userID]
		if !ok {
			locked, _ = s.isUserLockedOut(ctx, userID)
			lockedOutCache[userID] = locked
		}
		return locked
	}

	host := r.URL.Query().Get("host")

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, b := range dbBoxes {
		if b.ContainerID == nil {
			continue
		}
		if host != "" && b.Ctrhost != host {
			continue
		}
		if isLocked(b.OwnerUserID) {
			continue
		}
		fmt.Fprintln(w, *b.ContainerID)
	}
}

// handleDebugBoxStart starts a stopped box via the exelet gRPC API.
func (s *Server) handleDebugBoxStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	boxName := r.FormValue("box_name")
	if boxName == "" {
		http.Error(w, "box_name is required", http.StatusBadRequest)
		return
	}

	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up box: %v", err), http.StatusInternalServerError)
		return
	}

	if box.ContainerID == nil {
		http.Error(w, "box has no container_id", http.StatusBadRequest)
		return
	}

	ec := s.getExeletClient(box.Ctrhost)
	if ec == nil {
		http.Error(w, fmt.Sprintf("exelet %q not available", box.Ctrhost), http.StatusServiceUnavailable)
		return
	}

	if _, err := ec.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: *box.ContainerID}); err != nil {
		http.Error(w, fmt.Sprintf("failed to start instance: %v", err), http.StatusInternalServerError)
		return
	}

	// After starting, sync SSH port from exelet if the DB doesn't have one
	// (e.g. after migrating a stopped instance, the exelet allocates a new port on start).
	if box.SSHPort == nil {
		instance, err := ec.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: *box.ContainerID})
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to get instance info: %v", err), http.StatusInternalServerError)
			return
		}
		if instance.Instance != nil && instance.Instance.SSHPort != 0 {
			newSSHPort := int64(instance.Instance.SSHPort)
			if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxSSHPort, exedb.UpdateBoxSSHPortParams{
				SSHPort: &newSSHPort,
				ID:      box.ID,
			}); err != nil {
				http.Error(w, fmt.Sprintf("failed to update SSH port: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}

	if err := s.updateBoxStatus(ctx, box.ID, "running"); err != nil {
		http.Error(w, fmt.Sprintf("failed to update box status: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "box started via debug page", "box", boxName)
	http.Redirect(w, r, "/debug/vms", http.StatusSeeOther)
}

// handleDebugBoxMigrateForm shows the migration form.
func (s *Server) handleDebugBoxMigrateForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	boxName := r.URL.Query().Get("box_name")
	if boxName == "" {
		http.Error(w, "box_name is required", http.StatusBadRequest)
		return
	}

	// Look up the box to get its current host
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		http.Error(w, fmt.Sprintf("box %q not found: %v", boxName, err), http.StatusNotFound)
		return
	}
	currentHost := box.Ctrhost

	// Get list of exelets for the dropdown, sorted and excluding current host
	var addrs []string
	for addr := range s.exeletClients {
		if addr != currentHost {
			addrs = append(addrs, addr)
		}
	}
	sort.Strings(addrs)

	// JSON-encode the box name for use in JavaScript
	boxNameJSON, _ := json.Marshal(boxName)

	data := struct {
		BoxName       string
		BoxNameJSON   template.JS
		ExeletOptions []string
	}{
		BoxName:       boxName,
		BoxNameJSON:   template.JS(boxNameJSON),
		ExeletOptions: addrs,
	}

	s.renderDebugTemplate(ctx, w, "box-migrate.html", data)
}

// handleDebugBoxMigrate handles migration of a box to a different exelet.
// It streams progress updates to the client.
func (s *Server) handleDebugBoxMigrate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	boxName := r.FormValue("box_name")
	targetAddr := r.FormValue("target")
	confirmName := r.FormValue("confirm_name")
	twoPhase := true

	// Set up streaming response
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	writeProgress := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
		flusher.Flush()
	}

	writeError := func(format string, args ...any) {
		writeProgress("ERROR: "+format, args...)
		writeProgress("MIGRATION_ERROR:")
	}

	if boxName == "" || targetAddr == "" {
		writeError("box_name and target are required")
		return
	}

	if boxName != confirmName {
		writeError("confirm_name must match box_name")
		return
	}

	writeProgress("Looking up box %q...", boxName)

	// Look up the box
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		writeError("box %q not found", boxName)
		return
	}
	if err != nil {
		writeError("failed to look up box: %v", err)
		return
	}

	if box.ContainerID == nil {
		writeError("box has no container_id")
		return
	}

	writeProgress("Box found: container_id=%s, source=%s", *box.ContainerID, box.Ctrhost)

	// Get source exelet client
	sourceClient := s.getExeletClient(box.Ctrhost)
	if sourceClient == nil {
		writeError("source exelet %q not available", box.Ctrhost)
		return
	}

	// Get target exelet client
	targetClient := s.getExeletClient(targetAddr)
	if targetClient == nil {
		writeError("target exelet %q not configured", targetAddr)
		return
	}

	if box.Ctrhost == targetAddr {
		writeError("source and target exelet are the same")
		return
	}

	containerID := *box.ContainerID

	// Use a context that won't be cancelled if the browser disconnects.
	// This ensures migration completes and the VM isn't left stopped on source.
	ctx = context.WithoutCancel(ctx)

	// Check source VM state before migration
	sourceInstance, err := sourceClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
	if err != nil {
		writeError("failed to get instance state: %v", err)
		return
	}
	wasRunning := sourceInstance.Instance.State == computeapi.VMState_RUNNING

	// Default to live migration for running VMs; form checkbox can override to false
	live := wasRunning
	if r.FormValue("live") == "false" {
		live = false
	}

	// Step 1: Stop VM on source (skip for two-phase and live - SendVM handles it)
	if !twoPhase && !live && wasRunning {
		writeProgress("Stopping VM on source exelet...")
		s.slog().InfoContext(ctx, "stopping VM for migration", "box", boxName, "container_id", containerID, "source", box.Ctrhost)
		if _, err := sourceClient.client.StopInstance(ctx, &computeapi.StopInstanceRequest{ID: containerID}); err != nil {
			writeError("failed to stop VM on source: %v", err)
			return
		}
		writeProgress("VM stopped.")
	}

	restartSource := func(reason string) {
		s.restartSourceVM(ctx, sourceClient, containerID, boxName, box.Ctrhost, reason, wasRunning, live, writeProgress)
	}

	// Step 2: Perform migration
	var sshPort *int64
	var coldBooted bool
	dbStatus := "running"

	if live {
		writeProgress("Starting live migration from %s to %s...", box.Ctrhost, targetAddr)
		s.slog().InfoContext(ctx, "starting live migration", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
		var liveSshPort int64
		var err error
		liveSshPort, coldBooted, err = s.migrateVMLive(ctx, migrateVMLiveParams{
			source:     sourceClient.client,
			target:     targetClient.client,
			instanceID: containerID,
			box:        box,
			progress:   writeProgress,
		})
		if err != nil {
			writeError("live migration failed: %v", err)
			restartSource(err.Error())
			return
		}
		sshPort = &liveSshPort
		writeProgress("Live migration complete — VM is running on target.")
		if coldBooted {
			writeProgress("WARNING: Live migration fell back to cold boot — VM was restarted.")

			// Update /etc/hosts — cold boot means the VM restarted with the
			// correct IP on the interface but /etc/hosts still has the old IP.
			if sourceInstance.Instance.VMConfig != nil && sourceInstance.Instance.VMConfig.NetworkInterface != nil {
				targetInstance, err := targetClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
				if err == nil && targetInstance.Instance.VMConfig != nil && targetInstance.Instance.VMConfig.NetworkInterface != nil {
					sourceNet := sourceInstance.Instance.VMConfig.NetworkInterface
					targetNet := targetInstance.Instance.VMConfig.NetworkInterface
					if sourceNet.IP != nil && targetNet.IP != nil {
						targetBox := box
						targetBox.Ctrhost = targetAddr
						targetBox.SSHPort = sshPort
						s.updateVMHostsFile(ctx, &targetBox, sourceNet.IP.IPV4, targetNet.IP.IPV4, writeProgress)
					}
				}
			}
		}
	} else {
		writeProgress("Starting disk transfer from %s to %s...", box.Ctrhost, targetAddr)
		s.slog().InfoContext(ctx, "starting migration", "box", boxName, "source", box.Ctrhost, "target", targetAddr, "two_phase", twoPhase)
		if err := s.migrateVM(ctx, sourceClient.client, targetClient.client, containerID, twoPhase, writeProgress); err != nil {
			writeError("migration failed: %v", err)
			restartSource(err.Error())
			return
		}
		writeProgress("Disk transfer complete.")

		// Step 3: Start VM on target (skip if source was stopped)
		if wasRunning {
			writeProgress("Starting VM on target exelet...")
			s.slog().InfoContext(ctx, "starting VM on target", "box", boxName, "target", targetAddr)
			if _, err := targetClient.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: containerID}); err != nil {
				writeError("failed to start VM on target: %v", err)
				restartSource(err.Error())
				return
			}
			writeProgress("VM started on target.")

			// Step 4: Get new SSH port from target
			writeProgress("Getting new SSH port...")
			instance, err := targetClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
			if err != nil {
				writeError("failed to get instance info from target: %v", err)
				restartSource(err.Error())
				return
			}
			newSSHPort := int64(instance.Instance.SSHPort)
			sshPort = &newSSHPort
			writeProgress("New SSH port: %d", newSSHPort)

			// Update /etc/hosts to reflect the new IP.
			if sourceInstance.Instance.VMConfig != nil && sourceInstance.Instance.VMConfig.NetworkInterface != nil &&
				instance.Instance.VMConfig != nil && instance.Instance.VMConfig.NetworkInterface != nil {
				sourceNet := sourceInstance.Instance.VMConfig.NetworkInterface
				targetNet := instance.Instance.VMConfig.NetworkInterface
				if sourceNet.IP != nil && targetNet.IP != nil {
					targetBox := box
					targetBox.Ctrhost = targetAddr
					targetBox.SSHPort = sshPort
					s.updateVMHostsFile(ctx, &targetBox, sourceNet.IP.IPV4, targetNet.IP.IPV4, writeProgress)
				}
			}
		} else {
			writeProgress("Source VM was stopped, leaving stopped on target.")
			dbStatus = "stopped"
		}
	}

	// Step 5: Update database with new ctrhost, ssh_port, status, and region
	writeProgress("Updating database...")
	if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxMigration, exedb.UpdateBoxMigrationParams{
		Ctrhost: targetAddr,
		SSHPort: sshPort,
		Status:  dbStatus,
		Region:  targetClient.region.Code,
		ID:      box.ID,
	}); err != nil {
		writeError("failed to update database: %v", err)
		restartSource(err.Error())
		return
	}
	writeProgress("Database updated.")

	// Flush exeprox routing cache so all proxy nodes pick up the new ctrhost.
	// Without this, exeprox nodes continue routing to the old exelet.
	// Use MovedBox (not DeletedBox) to avoid unnecessarily purging share caches.
	proxyChangeMovedBox(boxName)
	writeProgress("Proxy caches flushed.")

	// Send maintenance email if the VM was rebooted (non-live migration or
	// live migration that fell back to cold boot).
	if !live || coldBooted {
		go s.sendBoxMaintenanceEmail(context.Background(), boxName)
	}

	// Clean up source instance
	writeProgress("Deleting source instance on %s...", box.Ctrhost)
	if _, err := sourceClient.client.DeleteInstance(ctx, &computeapi.DeleteInstanceRequest{ID: containerID}); err != nil {
		// Non-fatal: migration succeeded, source cleanup is best-effort
		writeProgress("WARNING: failed to delete source instance: %v", err)
		writeProgress("Manual cleanup needed: ./exelet-ctl -a %s compute instances rm %s", box.Ctrhost, containerID)
		s.slog().WarnContext(ctx, "failed to delete source instance after migration",
			"box", boxName, "container_id", containerID, "source", box.Ctrhost, "error", err)
	} else {
		writeProgress("Source instance deleted.")
	}

	writeProgress("")
	writeProgress("=== Migration complete! ===")
	writeProgress("")
	writeProgress("View VM details: /debug/vms/%s", boxName)
	writeProgress("MIGRATION_SUCCESS:%s", boxName)
}

// recvTargetError attempts to retrieve the server-side error from a ReceiveVM
// stream after a Send failure. It uses a short timeout to avoid blocking
// forever if the server is unreachable.
func recvTargetError(stream computeapi.ComputeService_ReceiveVMClient) error {
	ch := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		ch <- err
	}()
	select {
	case err := <-ch:
		if err != nil && err != io.EOF {
			return err
		}
		return nil
	case <-time.After(5 * time.Second):
		return nil
	}
}

// migrateVM performs the SendVM/ReceiveVM streaming between source and target exelets.
// The progress callback is called periodically with status updates.
// When twoPhase is true, the source exelet will snapshot the running VM, send the full
// snapshot, then stop the VM and send only the incremental diff.
func (s *Server) migrateVM(ctx context.Context, source, target *exeletclient.Client, instanceID string, twoPhase bool, progress func(string, ...any)) error {
	// Wrap context with cancel so gRPC streams are cleaned up when this
	// function returns. Without this, a failed migration leaves the source
	// exelet's SendVM stream open, which holds the migration lock forever.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start SendVM on source
	sendStream, err := source.SendVM(ctx)
	if err != nil {
		return fmt.Errorf("failed to start SendVM: %w", err)
	}

	progress("Requesting VM metadata from source...")

	if err := sendStream.Send(&computeapi.SendVMRequest{
		Type: &computeapi.SendVMRequest_Start{
			Start: &computeapi.SendVMStartRequest{
				InstanceID:         instanceID,
				TargetHasBaseImage: true,
				TwoPhase:           twoPhase,
				AcceptStatus:       true,
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to send start request: %w", err)
	}

	// Receive metadata from source (may be preceded by status messages)
	var metadata *computeapi.SendVMMetadata
	for {
		resp, err := sendStream.Recv()
		if err != nil {
			return fmt.Errorf("failed to receive metadata: %w", err)
		}
		if st := resp.GetStatus(); st != nil {
			progress("Source: %s", st.Message)
			continue
		}
		metadata = resp.GetMetadata()
		if metadata == nil {
			return fmt.Errorf("expected metadata, got %T", resp.Type)
		}
		break
	}

	progress("Received metadata: image=%s, base_image=%s, encrypted=%v",
		metadata.Instance.Image, metadata.BaseImageID, metadata.Encrypted)

	// Start ReceiveVM on target
	recvStream, err := target.ReceiveVM(ctx)
	if err != nil {
		return fmt.Errorf("failed to start ReceiveVM: %w", err)
	}

	progress("Initiating receive on target...")

	// Send start request to target
	if err := recvStream.Send(&computeapi.ReceiveVMRequest{
		Type: &computeapi.ReceiveVMRequest_Start{
			Start: &computeapi.ReceiveVMStartRequest{
				InstanceID:     instanceID,
				SourceInstance: metadata.Instance,
				BaseImageID:    metadata.BaseImageID,
				Encrypted:      metadata.Encrypted,
				EncryptionKey:  metadata.EncryptionKey,
				GroupID:        metadata.Instance.GroupID,
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to send receive start: %w", err)
	}

	progress("Waiting for target to prepare (replication sync)...")

	// Wait for ready from target - tells us if target has base image
	recvResp, err := recvStream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive ready: %w", err)
	}
	ready := recvResp.GetReady()
	if ready == nil {
		return fmt.Errorf("expected ready, got %T", recvResp.Type)
	}

	progress("Target ready (has_base_image=%v)", ready.HasBaseImage)

	progress("Transferring disk data...")

	// Pipe data chunks from source to target
	var totalBytes uint64
	lastReportedMB := uint64(0)
	for {
		resp, err := sendStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to receive from source: %w", err)
		}

		switch v := resp.Type.(type) {
		case *computeapi.SendVMResponse_Data:
			totalBytes += uint64(len(v.Data.Data))

			// Report progress every 100MB
			currentMB := totalBytes / (1024 * 1024)
			if currentMB >= lastReportedMB+100 {
				progress("Transferred %d MB...", currentMB)
				lastReportedMB = currentMB
			}

			if err := recvStream.Send(&computeapi.ReceiveVMRequest{
				Type: &computeapi.ReceiveVMRequest_Data{
					Data: &computeapi.ReceiveVMDataChunk{
						Data:        v.Data.Data,
						IsBaseImage: v.Data.IsBaseImage,
					},
				},
			}); err != nil {
				if recvErr := recvTargetError(recvStream); recvErr != nil {
					return fmt.Errorf("target error: %w", recvErr)
				}
				return fmt.Errorf("failed to send to target: %w", err)
			}

		case *computeapi.SendVMResponse_PhaseComplete:
			progress("Phase 1 complete (%d MB), VM stopping for phase 2...",
				v.PhaseComplete.PhaseBytes/(1024*1024))
			if err := recvStream.Send(&computeapi.ReceiveVMRequest{
				Type: &computeapi.ReceiveVMRequest_PhaseComplete{
					PhaseComplete: &computeapi.ReceiveVMPhaseComplete{},
				},
			}); err != nil {
				if recvErr := recvTargetError(recvStream); recvErr != nil {
					return fmt.Errorf("target error: %w", recvErr)
				}
				return fmt.Errorf("failed to send phase complete to target: %w", err)
			}

		case *computeapi.SendVMResponse_Complete:
			progress("Transfer complete, verifying checksum...")
			if err := recvStream.Send(&computeapi.ReceiveVMRequest{
				Type: &computeapi.ReceiveVMRequest_Complete{
					Complete: &computeapi.ReceiveVMComplete{
						Checksum: v.Complete.Checksum,
					},
				},
			}); err != nil {
				if recvErr := recvTargetError(recvStream); recvErr != nil {
					return fmt.Errorf("target error: %w", recvErr)
				}
				return fmt.Errorf("failed to send complete: %w", err)
			}

		default:
			return fmt.Errorf("unexpected response type from source: %T", resp.Type)
		}
	}

	progress("Total transferred: %d MB", totalBytes/(1024*1024))

	// Close send direction on target
	if err := recvStream.CloseSend(); err != nil {
		return fmt.Errorf("failed to close send: %w", err)
	}

	// Wait for result from target
	progress("Waiting for target to finalize...")
	for {
		recvResp, err := recvStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to receive result: %w", err)
		}

		if result := recvResp.GetResult(); result != nil {
			if result.Error != "" {
				return fmt.Errorf("target error: %s", result.Error)
			}
			progress("Target finalized successfully.")
			break
		}
	}

	return nil
}

// migrateVMLiveParams holds the parameters for migrateVMLive.
//
//exe:completeinit
type migrateVMLiveParams struct {
	source     *exeletclient.Client
	target     *exeletclient.Client
	instanceID string
	box        exedb.Box
	progress   func(string, ...any)
}

// migrateVMLive performs a live migration using CH snapshot/restore.
// It coordinates the SendVM(live=true)/ReceiveVM(live=true) streams, SSHes into the VM
// to reconfigure its IP, and returns the new SSH port on the target.
func (s *Server) migrateVMLive(ctx context.Context, p migrateVMLiveParams) (int64, bool, error) {
	source := p.source
	target := p.target
	instanceID := p.instanceID
	box := p.box
	progress := p.progress
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start SendVM on source with live=true
	sendStream, err := source.SendVM(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("failed to start SendVM: %w", err)
	}

	progress("Requesting VM metadata from source (live)...")

	if err := sendStream.Send(&computeapi.SendVMRequest{
		Type: &computeapi.SendVMRequest_Start{
			Start: &computeapi.SendVMStartRequest{
				InstanceID:         instanceID,
				TargetHasBaseImage: true,
				Live:               true,
				AcceptStatus:       true,
			},
		},
	}); err != nil {
		return 0, false, fmt.Errorf("failed to send start request: %w", err)
	}

	// Receive metadata from source (may be preceded by status messages)
	var metadata *computeapi.SendVMMetadata
	for {
		resp, err := sendStream.Recv()
		if err != nil {
			return 0, false, fmt.Errorf("failed to receive metadata: %w", err)
		}
		if st := resp.GetStatus(); st != nil {
			progress("Source: %s", st.Message)
			continue
		}
		metadata = resp.GetMetadata()
		if metadata == nil {
			return 0, false, fmt.Errorf("expected metadata, got %T", resp.Type)
		}
		break
	}

	progress("Received metadata: image=%s, encrypted=%v", metadata.Instance.Image, metadata.Encrypted)

	// Start ReceiveVM on target with live=true
	recvStream, err := target.ReceiveVM(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("failed to start ReceiveVM: %w", err)
	}

	progress("Initiating live receive on target...")

	if err := recvStream.Send(&computeapi.ReceiveVMRequest{
		Type: &computeapi.ReceiveVMRequest_Start{
			Start: &computeapi.ReceiveVMStartRequest{
				InstanceID:     instanceID,
				SourceInstance: metadata.Instance,
				BaseImageID:    metadata.BaseImageID,
				Encrypted:      metadata.Encrypted,
				EncryptionKey:  metadata.EncryptionKey,
				GroupID:        metadata.Instance.GroupID,
				Live:           true,
			},
		},
	}); err != nil {
		return 0, false, fmt.Errorf("failed to send receive start: %w", err)
	}

	progress("Waiting for target to prepare (replication sync, memory check)...")

	// Wait for ready from target — includes allocated network interface
	recvResp, err := recvStream.Recv()
	if err != nil {
		return 0, false, fmt.Errorf("failed to receive ready: %w", err)
	}
	ready := recvResp.GetReady()
	if ready == nil {
		return 0, false, fmt.Errorf("expected ready, got %T", recvResp.Type)
	}

	targetNetwork := ready.TargetNetwork
	if targetNetwork == nil || targetNetwork.IP == nil {
		return 0, false, fmt.Errorf("target did not provide network interface")
	}

	progress("Target ready (target_ip=%s)", targetNetwork.IP.IPV4)
	progress("Transferring disk data...")

	// Pipe data from source to target
	var totalBytes uint64
	lastReportedMB := uint64(0)
	var snapshotBytes uint64
	lastSnapshotReportedMB := uint64(0)
	for {
		resp, err := sendStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, false, fmt.Errorf("failed to receive from source: %w", err)
		}

		switch v := resp.Type.(type) {
		case *computeapi.SendVMResponse_Data:
			totalBytes += uint64(len(v.Data.Data))
			currentMB := totalBytes / (1024 * 1024)
			if currentMB >= lastReportedMB+100 {
				progress("Transferred %d MB...", currentMB)
				lastReportedMB = currentMB
			}

			if err := recvStream.Send(&computeapi.ReceiveVMRequest{
				Type: &computeapi.ReceiveVMRequest_Data{
					Data: &computeapi.ReceiveVMDataChunk{
						Data:        v.Data.Data,
						IsBaseImage: v.Data.IsBaseImage,
					},
				},
			}); err != nil {
				if recvErr := recvTargetError(recvStream); recvErr != nil {
					return 0, false, fmt.Errorf("target error: %w", recvErr)
				}
				return 0, false, fmt.Errorf("failed to send to target: %w", err)
			}

		case *computeapi.SendVMResponse_PhaseComplete:
			progress("Phase complete (%d MB)", v.PhaseComplete.PhaseBytes/(1024*1024))
			if err := recvStream.Send(&computeapi.ReceiveVMRequest{
				Type: &computeapi.ReceiveVMRequest_PhaseComplete{
					PhaseComplete: &computeapi.ReceiveVMPhaseComplete{},
				},
			}); err != nil {
				if recvErr := recvTargetError(recvStream); recvErr != nil {
					return 0, false, fmt.Errorf("target error: %w", recvErr)
				}
				return 0, false, fmt.Errorf("failed to send phase complete to target: %w", err)
			}

		case *computeapi.SendVMResponse_AwaitControl:
			// Source is asking us to reconfigure the VM's IP via SSH
			progress("Source requesting IP reconfiguration...")
			sourceNetwork := v.AwaitControl.SourceNetwork
			if sourceNetwork == nil || sourceNetwork.IP == nil {
				return 0, false, fmt.Errorf("source did not provide network info in AwaitControl")
			}

			// SSH into the running VM and change its IP to the target's IP
			if err := s.reconfigureVMIP(ctx, &box, sourceNetwork, targetNetwork, progress); err != nil {
				return 0, false, fmt.Errorf("failed to reconfigure VM IP: %w", err)
			}

			// Tell source to proceed with pause
			progress("IP reconfigured, sending proceed signal...")
			if err := sendStream.Send(&computeapi.SendVMRequest{
				Type: &computeapi.SendVMRequest_Control{
					Control: &computeapi.SendVMControl{
						Action: computeapi.SendVMControl_PROCEED_WITH_PAUSE,
					},
				},
			}); err != nil {
				return 0, false, fmt.Errorf("failed to send control: %w", err)
			}

		case *computeapi.SendVMResponse_SnapshotData:
			// Forward snapshot file chunks to target
			snapshotBytes += uint64(len(v.SnapshotData.Data))
			currentMB := snapshotBytes / (1024 * 1024)
			if snapshotBytes == uint64(len(v.SnapshotData.Data)) {
				progress("Transferring VM snapshot...")
			} else if currentMB >= lastSnapshotReportedMB+100 {
				progress("VM snapshot: %d MB...", currentMB)
				lastSnapshotReportedMB = currentMB
			}
			if err := recvStream.Send(&computeapi.ReceiveVMRequest{
				Type: &computeapi.ReceiveVMRequest_SnapshotData{
					SnapshotData: &computeapi.ReceiveVMSnapshotChunk{
						Filename:    v.SnapshotData.Filename,
						Data:        v.SnapshotData.Data,
						IsLastChunk: v.SnapshotData.IsLastChunk,
						Compressed:  v.SnapshotData.Compressed,
					},
				},
			}); err != nil {
				if recvErr := recvTargetError(recvStream); recvErr != nil {
					return 0, false, fmt.Errorf("target error: %w", recvErr)
				}
				return 0, false, fmt.Errorf("failed to send snapshot data to target: %w", err)
			}

		case *computeapi.SendVMResponse_Complete:
			progress("Transfer complete, verifying checksum...")
			if err := recvStream.Send(&computeapi.ReceiveVMRequest{
				Type: &computeapi.ReceiveVMRequest_Complete{
					Complete: &computeapi.ReceiveVMComplete{
						Checksum: v.Complete.Checksum,
					},
				},
			}); err != nil {
				if recvErr := recvTargetError(recvStream); recvErr != nil {
					return 0, false, fmt.Errorf("target error: %w", recvErr)
				}
				return 0, false, fmt.Errorf("failed to send complete: %w", err)
			}

		default:
			return 0, false, fmt.Errorf("unexpected response type from source: %T", resp.Type)
		}
	}

	progress("Total transferred: %d MB", totalBytes/(1024*1024))

	if err := recvStream.CloseSend(); err != nil {
		return 0, false, fmt.Errorf("failed to close send: %w", err)
	}

	// Wait for result from target
	progress("Waiting for target to restore VM...")
	var resultInstance *computeapi.Instance
	var coldBooted bool
	for {
		recvResp, err := recvStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, false, fmt.Errorf("failed to receive result: %w", err)
		}

		if result := recvResp.GetResult(); result != nil {
			if result.Error != "" {
				return 0, false, fmt.Errorf("target error: %s", result.Error)
			}
			resultInstance = result.Instance
			coldBooted = result.ColdBooted
			if coldBooted {
				progress("VM restored via cold boot fallback (snapshot restore failed).")
			} else {
				progress("VM restored and running on target.")
			}
			break
		}
	}

	if resultInstance == nil {
		return 0, false, fmt.Errorf("no result instance from target")
	}

	return int64(resultInstance.SSHPort), coldBooted, nil
}

// reconfigureVMIP SSHes into the running VM and changes its IP from source to target.
func (s *Server) reconfigureVMIP(ctx context.Context, box *exedb.Box, sourceNetwork, targetNetwork *computeapi.NetworkInterface, progress func(string, ...any)) error {
	if sourceNetwork.IP == nil || sourceNetwork.IP.IPV4 == "" {
		return fmt.Errorf("source network has no IP address")
	}
	if targetNetwork.IP == nil || targetNetwork.IP.IPV4 == "" {
		return fmt.Errorf("target network has no IP address")
	}
	if targetNetwork.IP.GatewayV4 == "" {
		return fmt.Errorf("target network has no gateway")
	}

	sourceIP := sourceNetwork.IP.IPV4
	targetIP := targetNetwork.IP.IPV4
	targetGW := targetNetwork.IP.GatewayV4

	// If source and target IPs are the same, no reconfiguration needed.
	if sourceIP == targetIP {
		progress("Source and target IPs are the same (%s), skipping IP reconfiguration.", sourceIP)
		return nil
	}

	progress("Reconfiguring VM IP: %s -> %s (gw %s)", sourceIP, targetIP, targetGW)

	logFile := "/var/log/exe-migrate.log"

	// Discover the guest network interface that holds the source IP.
	// The guest interface name varies by kernel/distro (eth0, ens3, etc.).
	sourceIPAddr := sourceIP
	if idx := strings.Index(sourceIPAddr, "/"); idx > 0 {
		sourceIPAddr = sourceIPAddr[:idx]
	}
	if _, err := netip.ParseAddr(sourceIPAddr); err != nil {
		return fmt.Errorf("invalid source IP %q: %w", sourceIPAddr, err)
	}
	targetIPAddr := targetIP
	if idx := strings.Index(targetIPAddr, "/"); idx > 0 {
		targetIPAddr = targetIPAddr[:idx]
	}
	// Escape dots for sed regex so "10.0.0.2" doesn't match "10X0Y0Z2".
	sourceIPSed := strings.ReplaceAll(sourceIPAddr, ".", "\\.")
	detectCmd := fmt.Sprintf("ip -o addr show to %s | awk '{print $2}'", sourceIPAddr)
	devOutput, err := runCommandOnBox(ctx, s.sshPool, box, detectCmd)
	if err != nil {
		return fmt.Errorf("failed to detect guest interface: %w (output: %s)", err, string(devOutput))
	}
	devFields := strings.Fields(string(devOutput))
	if len(devFields) == 0 {
		return fmt.Errorf("no guest interface found for IP %s", sourceIPAddr)
	}
	guestDev := devFields[0]
	if strings.ContainsAny(guestDev, "/'\"\\; \t\n") {
		return fmt.Errorf("invalid guest interface name %q for IP %s", guestDev, sourceIPAddr)
	}
	progress("Detected guest interface: %s", guestDev)

	// Step 1: Enable promote_secondaries and add the new IP. This runs
	// synchronously — the old IP still exists so SSH stays alive.
	// promote_secondaries ensures that when we delete the primary (old) IP,
	// the secondary (new) IP is promoted to primary instead of being removed.
	addCmd := fmt.Sprintf("sudo /exe.dev/bin/sh -c '"+
		"echo \"=== Migration IP reconfig $(date -Iseconds) ===\" >> %s; "+
		"echo \"before:\" >> %s; ip addr show dev %s >> %s 2>&1; ip route >> %s 2>&1; "+
		"echo 1 > /proc/sys/net/ipv4/conf/%s/promote_secondaries; "+
		"ip addr add %s dev %s 2>> %s; "+
		"sed -i \"s/^%s /%s /\" /etc/hosts 2>> %s; sync; "+
		"echo \"after add:\" >> %s; ip addr show dev %s >> %s 2>&1"+
		"'",
		logFile,
		logFile, guestDev, logFile, logFile,
		guestDev,
		targetIP, guestDev, logFile,
		sourceIPSed, targetIPAddr, logFile,
		logFile, guestDev, logFile)
	output, err := runCommandOnBox(ctx, s.sshPool, box, addCmd)
	if err != nil {
		return fmt.Errorf("failed to add target IP: %w (output: %s)", err, string(output))
	}
	progress("Added target IP %s to %s.", targetIP, guestDev)

	// Step 2: Delete the old IP and fix the route in the background.
	// Deleting the old IP kills the SSH connection, so we use nohup.
	// With promote_secondaries, the new IP is promoted to primary automatically.
	delCmd := fmt.Sprintf("nohup sudo /exe.dev/bin/sh -c '"+
		"trap \"\" HUP; "+
		"ip addr del %s dev %s 2>> %s; "+
		"ip route replace default via %s 2>> %s; "+
		"echo \"after del:\" >> %s; ip addr show dev %s >> %s 2>&1; ip route >> %s 2>&1"+
		"' >/dev/null 2>&1 &",
		sourceIP, guestDev, logFile,
		targetGW, logFile,
		logFile, guestDev, logFile, logFile)

	sshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := runCommandOnBox(sshCtx, s.sshPool, box, delCmd); err != nil {
		progress("Old IP cleanup backgrounded (SSH disconnected as expected).")
	}

	progress("IP reconfiguration complete.")
	return nil
}

// updateVMHostsFile SSHes into a running VM and updates /etc/hosts to replace
// sourceIP with targetIP. This is used after cold migrations where the VM boots
// with the correct IP on the interface (from boot args) but /etc/hosts still
// has the old IP baked into the disk image.
func (s *Server) updateVMHostsFile(ctx context.Context, box *exedb.Box, sourceIP, targetIP string, progress func(string, ...any)) {
	// Strip CIDR notation — /etc/hosts uses bare IPs.
	if idx := strings.Index(sourceIP, "/"); idx > 0 {
		sourceIP = sourceIP[:idx]
	}
	if idx := strings.Index(targetIP, "/"); idx > 0 {
		targetIP = targetIP[:idx]
	}
	if sourceIP == targetIP {
		return
	}

	// In /etc/hosts the IP is at the start of the line followed by a space.
	// Match that pattern to avoid partial matches (e.g. 10.0.0.2 inside 10.0.0.20).
	// We avoid \b because BusyBox sed doesn't support it and truncates the file.
	sourceIPSed := strings.ReplaceAll(sourceIP, ".", "\\.")
	cmd := fmt.Sprintf(
		"sudo /exe.dev/bin/sh -c 'sed -i \"s/^%s /%s /\" /etc/hosts && sync'",
		sourceIPSed, targetIP,
	)

	progress("Updating /etc/hosts: %s -> %s", sourceIP, targetIP)

	// Retry with backoff — after a cold boot the VM may take several seconds
	// before SSH is ready to accept connections.
	deadline := time.Now().Add(30 * time.Second)
	delay := 1 * time.Second
	for attempt := 1; ; attempt++ {
		sshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := runCommandOnBox(sshCtx, s.sshPool, box, cmd)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			progress("WARNING: failed to update /etc/hosts after %d attempts: %v", attempt, err)
			return
		}
		time.Sleep(delay)
		delay *= 2
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
	}
}

// handleDebugMassMigrateForm shows the migration form for multiple boxes.
func (s *Server) handleDebugMassMigrateForm(w http.ResponseWriter, r *http.Request) {
	var addrs []string
	for addr := range s.exeletClients {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)

	data := struct {
		Exelets []string
	}{
		Exelets: addrs,
	}

	s.renderDebugTemplate(r.Context(), w, "mass-migrate.html", data)
}

// handleDebugMassMigrateBoxes returns JSON list of boxes on selected exelets.
func (s *Server) handleDebugMassMigrateBoxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sources := r.URL.Query()["source"]

	type boxInfo struct {
		Name        string `json:"name"`
		Host        string `json:"host"`
		ContainerID string `json:"container_id"`
		Status      string `json:"status"`
	}

	var boxes []boxInfo
	for _, source := range sources {
		dbBoxes, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxesByHost, source)
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to get boxes for host", "host", source, "error", err)
			continue
		}
		for _, b := range dbBoxes {
			if b.ContainerID == nil {
				continue
			}
			boxes = append(boxes, boxInfo{
				Name:        b.Name,
				Host:        b.Ctrhost,
				ContainerID: *b.ContainerID,
				Status:      b.Status,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(boxes)
}

// handleDebugMassMigrate handles migration of multiple boxes to a target exelet.
// It streams progress updates to the client.
func (s *Server) handleDebugMassMigrate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("failed to parse form: %v", err), http.StatusBadRequest)
		return
	}

	boxNames := r.PostForm["box_names"]
	targetAddr := r.FormValue("target")
	confirm := r.FormValue("confirm")

	// Set up streaming response
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	writeProgress := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
		flusher.Flush()
	}

	writeError := func(format string, args ...any) {
		writeProgress("ERROR: "+format, args...)
	}

	if len(boxNames) == 0 || targetAddr == "" {
		writeError("box_names and target are required")
		writeProgress("MIGRATION_ERROR")
		return
	}

	expectedConfirm := strconv.Itoa(len(boxNames))
	if confirm != expectedConfirm {
		writeError("confirm must be %q (the number of VMs to migrate)", expectedConfirm)
		writeProgress("MIGRATION_ERROR")
		return
	}

	targetClient := s.getExeletClient(targetAddr)
	if targetClient == nil {
		writeError("target exelet %q not configured", targetAddr)
		writeProgress("MIGRATION_ERROR")
		return
	}

	// Use a background context so migrations complete even if the browser disconnects.
	ctx := context.Background()

	// Lock deployments during mass migration (best-effort).
	prodLocked, err := prodlockSet(ctx, s.env, "lock", fmt.Sprintf("mass VM migration: %d VMs to %s", len(boxNames), targetAddr))
	if err != nil {
		writeProgress("WARNING: failed to lock deployments: %v", err)
	} else if prodLocked {
		writeProgress("Deployments locked.")
	} else {
		writeProgress("Already locked — will not auto-unlock after migration.")
	}

	writeProgress("Starting migration of %d VMs to %s (live for running VMs)", len(boxNames), targetAddr)
	writeProgress("")

	var succeeded, failed int

	for i, boxName := range boxNames {
		writeProgress("=== [%d/%d] Migrating %s ===", i+1, len(boxNames), boxName)

		box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
		if err != nil {
			writeError("box %q not found: %v", boxName, err)
			failed++
			writeProgress("")
			continue
		}

		if box.ContainerID == nil {
			writeError("box %q has no container_id", boxName)
			failed++
			writeProgress("")
			continue
		}

		if box.Ctrhost == targetAddr {
			writeError("box %q is already on target exelet", boxName)
			failed++
			writeProgress("")
			continue
		}

		containerID := *box.ContainerID
		sourceClient := s.getExeletClient(box.Ctrhost)
		if sourceClient == nil {
			writeError("source exelet %q not available for box %q", box.Ctrhost, boxName)
			failed++
			writeProgress("")
			continue
		}

		// Check source VM state
		sourceInstance, err := sourceClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
		if err != nil {
			writeError("failed to get instance state for %q: %v", boxName, err)
			failed++
			writeProgress("")
			continue
		}
		wasRunning := sourceInstance.Instance.State == computeapi.VMState_RUNNING
		live := wasRunning // Use live migration for running VMs

		restartSource := func(reason string) {
			s.restartSourceVM(ctx, sourceClient, containerID, boxName, box.Ctrhost, reason, wasRunning, live, writeProgress)
		}

		var sshPort *int64
		var coldBooted bool
		dbStatus := "running"

		if live {
			writeProgress("Starting live migration from %s to %s...", box.Ctrhost, targetAddr)
			s.slog().InfoContext(ctx, "starting live migration", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
			liveSshPort, cb, err := s.migrateVMLive(ctx, migrateVMLiveParams{
				source:     sourceClient.client,
				target:     targetClient.client,
				instanceID: containerID,
				box:        box,
				progress:   writeProgress,
			})
			if err != nil {
				writeError("live migration failed: %v", err)
				restartSource(err.Error())
				failed++
				writeProgress("")
				continue
			}
			coldBooted = cb
			sshPort = &liveSshPort
			writeProgress("Live migration complete — VM is running on target.")
			if coldBooted {
				writeProgress("WARNING: Live migration fell back to cold boot — VM was restarted.")

				// Update /etc/hosts — cold boot means the VM restarted with the
				// correct IP on the interface but /etc/hosts still has the old IP.
				if sourceInstance.Instance.VMConfig != nil && sourceInstance.Instance.VMConfig.NetworkInterface != nil {
					targetInstance, err := targetClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
					if err == nil && targetInstance.Instance.VMConfig != nil && targetInstance.Instance.VMConfig.NetworkInterface != nil {
						sourceNet := sourceInstance.Instance.VMConfig.NetworkInterface
						targetNet := targetInstance.Instance.VMConfig.NetworkInterface
						if sourceNet.IP != nil && targetNet.IP != nil {
							targetBox := box
							targetBox.Ctrhost = targetAddr
							targetBox.SSHPort = sshPort
							s.updateVMHostsFile(ctx, &targetBox, sourceNet.IP.IPV4, targetNet.IP.IPV4, writeProgress)
						}
					}
				}
			}
		} else {
			writeProgress("Transferring disk from %s to %s...", box.Ctrhost, targetAddr)
			s.slog().InfoContext(ctx, "migration: starting disk transfer", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
			if err := s.migrateVM(ctx, sourceClient.client, targetClient.client, containerID, true, writeProgress); err != nil {
				writeError("disk transfer failed: %v", err)
				restartSource(err.Error())
				failed++
				writeProgress("")
				continue
			}
			writeProgress("Disk transfer complete.")

			if wasRunning {
				writeProgress("Starting VM on target...")
				s.slog().InfoContext(ctx, "migration: starting VM on target", "box", boxName, "target", targetAddr)
				if _, err := targetClient.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: containerID}); err != nil {
					writeError("failed to start VM on target: %v", err)
					restartSource(err.Error())
					failed++
					writeProgress("")
					continue
				}
				writeProgress("VM started on target.")

				writeProgress("Getting new SSH port...")
				instance, err := targetClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
				if err != nil {
					writeError("failed to get instance info from target: %v", err)
					restartSource(err.Error())
					failed++
					writeProgress("")
					continue
				}
				newSSHPort := int64(instance.Instance.SSHPort)
				sshPort = &newSSHPort
				writeProgress("New SSH port: %d", newSSHPort)

				// Update /etc/hosts to reflect the new IP.
				if sourceInstance.Instance.VMConfig != nil && sourceInstance.Instance.VMConfig.NetworkInterface != nil &&
					instance.Instance.VMConfig != nil && instance.Instance.VMConfig.NetworkInterface != nil {
					sourceNet := sourceInstance.Instance.VMConfig.NetworkInterface
					targetNet := instance.Instance.VMConfig.NetworkInterface
					if sourceNet.IP != nil && targetNet.IP != nil {
						targetBox := box
						targetBox.Ctrhost = targetAddr
						targetBox.SSHPort = sshPort
						s.updateVMHostsFile(ctx, &targetBox, sourceNet.IP.IPV4, targetNet.IP.IPV4, writeProgress)
					}
				}
			} else {
				writeProgress("Source VM was stopped, leaving stopped on target.")
				dbStatus = "stopped"
			}
		}

		writeProgress("Updating database...")
		if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxMigration, exedb.UpdateBoxMigrationParams{
			Ctrhost: targetAddr,
			SSHPort: sshPort,
			Status:  dbStatus,
			Region:  targetClient.region.Code,
			ID:      box.ID,
		}); err != nil {
			writeError("failed to update database: %v", err)
			restartSource(err.Error())
			failed++
			writeProgress("")
			continue
		}
		writeProgress("Database updated.")

		proxyChangeMovedBox(boxName)
		writeProgress("Proxy caches flushed.")

		if !live || coldBooted {
			go s.sendBoxMaintenanceEmail(context.Background(), boxName)
		}

		// Clean up source instance
		writeProgress("Deleting source instance on %s...", box.Ctrhost)
		if _, err := sourceClient.client.DeleteInstance(ctx, &computeapi.DeleteInstanceRequest{ID: containerID}); err != nil {
			writeProgress("WARNING: failed to delete source instance: %v", err)
			writeProgress("Manual cleanup: ./exelet-ctl -a %s compute instances rm %s", box.Ctrhost, containerID)
			s.slog().WarnContext(ctx, "failed to delete source instance after migration",
				"box", boxName, "container_id", containerID, "source", box.Ctrhost, "error", err)
		} else {
			writeProgress("Source instance deleted.")
		}

		writeProgress("Box %s migrated successfully.", boxName)
		succeeded++
		writeProgress("")
	}

	// Unlock if we locked it.
	if prodLocked {
		writeProgress("Unlocking deployments...")
		if _, err := prodlockSet(ctx, s.env, "unlock", "mass VM migration complete"); err != nil {
			writeProgress("WARNING: failed to unlock deployments: %v — manual unlock required", err)
		} else {
			writeProgress("Deployments unlocked.")
		}
	}

	writeProgress("=== Migration complete ===")
	writeProgress("Succeeded: %d, Failed: %d, Total: %d", succeeded, failed, len(boxNames))

	if failed == 0 {
		writeProgress("MIGRATION_SUCCESS")
	} else {
		writeProgress("MIGRATION_ERROR")
	}
}

// prodlockSet locks or unlocks a prodlock environment.
// action must be "lock" or "unlock".
// It returns true if the action was applied, or false if the environment
// was already in the requested state (409).
func prodlockSet(ctx context.Context, env stage.Env, action, reason string) (bool, error) {
	if env.ProdLockEnv == "" {
		return false, nil
	}
	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return false, err
	}
	url := fmt.Sprintf("https://prodlock.exe.xyz:8000/api/%s/%s", env.ProdLockEnv, action)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer exe1.RAIPQOV23P6TEQLLCGZ4LRVZNK")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("prodlock %s %s: %d %s", action, env.ProdLockEnv, resp.StatusCode, respBody)
	}
	return true, nil
}

// handleDebugBoxDetails displays detailed information about a specific box.
func (s *Server) handleDebugBoxDetails(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	boxName := r.PathValue("name")

	if boxName == "" {
		http.Error(w, "box name is required", http.StatusBadRequest)
		return
	}

	// Look up the box
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("/debug/vms/detail: failed to look up VM by name: %v", err), http.StatusInternalServerError)
		return
	}

	// Look up owner email
	ownerEmail, err := withRxRes1(s, ctx, (*exedb.Queries).GetEmailByUserID, box.CreatedByUserID)
	if err != nil {
		ownerEmail = box.CreatedByUserID // fallback to user ID
	}

	// Get sharing info
	pendingShares, _ := withRxRes1(s, ctx, (*exedb.Queries).GetPendingBoxSharesByBoxID, int64(box.ID))
	activeShares, _ := withRxRes1(s, ctx, (*exedb.Queries).GetBoxSharesByBoxID, int64(box.ID))
	shareLinks, _ := withRxRes1(s, ctx, (*exedb.Queries).GetAllBoxShareLinksByBoxID, int64(box.ID))

	route := box.GetRoute()

	type shareInfo struct {
		Email     string
		SharedBy  string
		Message   string
		CreatedAt string
	}

	type linkInfo struct {
		Token     string
		CreatedBy string
		CreatedAt string
		LastUsed  string
		UseCount  string
	}

	var activeShareList []shareInfo
	for _, share := range activeShares {
		activeShareList = append(activeShareList, shareInfo{
			Email:     share.SharedWithUserEmail,
			SharedBy:  share.SharedByUserID,
			Message:   ptrStr(share.Message),
			CreatedAt: formatTime(share.CreatedAt),
		})
	}

	var pendingShareList []shareInfo
	for _, share := range pendingShares {
		pendingShareList = append(pendingShareList, shareInfo{
			Email:     share.SharedWithEmail,
			SharedBy:  share.SharedByUserID,
			Message:   ptrStr(share.Message),
			CreatedAt: formatTime(share.CreatedAt),
		})
	}

	var shareLinkList []linkInfo
	for _, link := range shareLinks {
		shareLinkList = append(shareLinkList, linkInfo{
			Token:     link.ShareToken,
			CreatedBy: link.CreatedByEmail,
			CreatedAt: formatTime(link.CreatedAt),
			LastUsed:  formatTime(link.LastUsedAt),
			UseCount:  formatInt64Ptr(link.UseCount),
		})
	}

	var creationLog string
	if box.CreationLog != nil {
		creationLog = *box.CreationLog
	}

	var shardDNS string
	if row, err := withRxRes1(s, ctx, (*exedb.Queries).GetIPShardAndAnycastNetworkByBoxName, box.Name); err == nil {
		var shardSub string
		if row.AnycastNetwork != nil && *row.AnycastNetwork == 1 {
			shardSub = publicips.LatitudeShardSub(int(row.IPShard))
		} else {
			shardSub = publicips.NetActuateShardSub(int(row.IPShard))
		}
		shardDNS = shardSub + "." + s.env.BoxHost
	}

	data := struct {
		Name                 string
		ID                   int64
		Status               string
		Image                string
		Ctrhost              string
		ContainerID          string
		OwnerEmail           string
		OwnerUserID          string
		CreatedAt            string
		UpdatedAt            string
		LastStartedAt        string
		ProxyPort            int
		ShareMode            string
		SSHPort              string
		SSHUser              string
		SSHHost              string
		ShardDNS             string
		HasServerIdentityKey bool
		HasClientPrivateKey  bool
		HasAuthorizedKeys    bool
		ActiveShares         []shareInfo
		PendingShares        []shareInfo
		ShareLinks           []linkInfo
		CreationLog          string
	}{
		Name:                 box.Name,
		ID:                   int64(box.ID),
		Status:               box.Status,
		Image:                box.Image,
		Ctrhost:              box.Ctrhost,
		ContainerID:          ptrStr(box.ContainerID),
		OwnerEmail:           ownerEmail,
		OwnerUserID:          box.CreatedByUserID,
		CreatedAt:            formatTime(box.CreatedAt),
		UpdatedAt:            formatTime(box.UpdatedAt),
		LastStartedAt:        formatTime(box.LastStartedAt),
		ProxyPort:            route.Port,
		ShareMode:            route.Share,
		SSHPort:              formatInt64Ptr(box.SSHPort),
		SSHUser:              ptrStr(box.SSHUser),
		SSHHost:              exeweb.BoxSSHHost(s.slog(), box.Ctrhost),
		ShardDNS:             shardDNS,
		HasServerIdentityKey: len(box.SSHServerIdentityKey) > 0,
		HasClientPrivateKey:  len(box.SSHClientPrivateKey) > 0,
		HasAuthorizedKeys:    box.SSHAuthorizedKeys != nil && *box.SSHAuthorizedKeys != "",
		ActiveShares:         activeShareList,
		PendingShares:        pendingShareList,
		ShareLinks:           shareLinkList,
		CreationLog:          creationLog,
	}

	s.renderDebugTemplate(ctx, w, "box-details.html", data)
}

// handleDebugBoxLogs fetches the instance logs from the exelet and returns them.
func (s *Server) handleDebugBoxLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	boxName := r.PathValue("name")

	if boxName == "" {
		http.Error(w, "box name is required", http.StatusBadRequest)
		return
	}

	// Look up the box
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up box by name: %v", err), http.StatusInternalServerError)
		return
	}

	if box.ContainerID == nil {
		http.Error(w, "box has no container_id", http.StatusBadRequest)
		return
	}

	// Get the exelet client for this box's host
	ec := s.getExeletClient(box.Ctrhost)
	if ec == nil {
		http.Error(w, fmt.Sprintf("exelet %q not available", box.Ctrhost), http.StatusServiceUnavailable)
		return
	}

	// Call GetInstanceLogs on the exelet
	stream, err := ec.client.GetInstanceLogs(ctx, &computeapi.GetInstanceLogsRequest{
		ID: *box.ContainerID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get instance logs: %v", err), http.StatusInternalServerError)
		return
	}

	// Collect all log messages
	var logs strings.Builder
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("error reading logs: %v", err), http.StatusInternalServerError)
			return
		}
		if resp.Log != nil {
			logs.WriteString(resp.Log.Message)
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(logs.String()))
}

// handleDebugUsers displays a list of all users with their root support and VM creation settings.
func (s *Server) handleDebugUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	users, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllUsers)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list users: %v", err), http.StatusInternalServerError)
		return
	}

	// Fetch all accounts and build a map from user_id to account_id
	accounts, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllAccounts)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list accounts: %v", err), http.StatusInternalServerError)
		return
	}
	accountByUser := make(map[string]string)
	for _, a := range accounts {
		accountByUser[a.CreatedBy] = a.ID
	}

	// Fetch all gateway credits and build a map from user_id to credit info
	credits, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllUserLLMCredits)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to list LLM credits", "error", err)
		credits = nil
	}
	creditByUser := make(map[string]exedb.UserLlmCredit)
	for _, c := range credits {
		creditByUser[c.UserID] = c
	}

	// Fetch unallocated invite counts by user (matches what users see in web UI)
	inviteCounts, err := withRxRes0(s, ctx, (*exedb.Queries).CountUnallocatedInviteCodesByUser)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to list invite counts", "error", err)
		inviteCounts = nil
	}
	invitesByUser := make(map[string]int64)
	for _, ic := range inviteCounts {
		if ic.AssignedToUserID != nil {
			invitesByUser[*ic.AssignedToUserID] = ic.Count
		}
	}

	// Count user types
	var regularCount, loginWithExeCount int
	for _, u := range users {
		if u.CreatedForLoginWithExe {
			loginWithExeCount++
		} else {
			regularCount++
		}
	}

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		type userInfo struct {
			UserID                 string  `json:"user_id"`
			Email                  string  `json:"email"`
			CreatedAt              string  `json:"created_at,omitempty"`
			RootSupport            bool    `json:"root_support"`
			VMCreationDisabled     bool    `json:"vm_creation_disabled"`
			IsLockedOut            bool    `json:"is_locked_out"`
			CreatedForLoginWithExe bool    `json:"created_for_login_with_exe"`
			AccountID              string  `json:"account_id,omitempty"`
			BillingURL             string  `json:"billing_url,omitempty"`
			CreditAvailableUSD     float64 `json:"credit_available_usd"`
			CreditTotalUsedUSD     float64 `json:"credit_total_used_usd"`
			CreditLastRefreshAt    string  `json:"credit_last_refresh_at,omitempty"`
			DiscordID              string  `json:"discord_id,omitempty"`
			DiscordUsername        string  `json:"discord_username,omitempty"`
			InviteCount            int64   `json:"invite_count"`
			BillingExemption       string  `json:"billing_exemption,omitempty"`
			Limits                 string  `json:"limits,omitempty"`
		}
		var usersJSON []userInfo
		for _, u := range users {
			createdAt := ""
			if u.CreatedAt != nil {
				createdAt = u.CreatedAt.Format(time.RFC3339)
			}
			acctID := accountByUser[u.UserID]
			var billingURL string
			if acctID != "" {
				billingURL = billing.MakeCustomerDashboardURL(acctID)
			}
			ui := userInfo{
				UserID:                 u.UserID,
				Email:                  u.Email,
				CreatedAt:              createdAt,
				RootSupport:            u.RootSupport == 1,
				VMCreationDisabled:     u.NewVmCreationDisabled,
				IsLockedOut:            u.IsLockedOut,
				CreatedForLoginWithExe: u.CreatedForLoginWithExe,
				AccountID:              acctID,
				BillingURL:             billingURL,
				DiscordID:              ptrStr(u.DiscordID),
				DiscordUsername:        ptrStr(u.DiscordUsername),
				InviteCount:            invitesByUser[u.UserID],
				BillingExemption:       ptrStr(u.BillingExemption),
				Limits:                 ptrStr(u.Limits),
			}
			if credit, ok := creditByUser[u.UserID]; ok {
				ui.CreditAvailableUSD = credit.AvailableCredit
				ui.CreditTotalUsedUSD = credit.TotalUsed
				ui.CreditLastRefreshAt = credit.LastRefreshAt.Format(time.RFC3339)
			}
			usersJSON = append(usersJSON, ui)
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(usersJSON); err != nil {
			s.slog().InfoContext(ctx, "Failed to encode users", "error", err)
		}
		return
	}

	// HTML output
	data := struct {
		RegularCount      int
		LoginWithExeCount int
		TotalCount        int
	}{
		RegularCount:      regularCount,
		LoginWithExeCount: loginWithExeCount,
		TotalCount:        len(users),
	}

	s.renderDebugTemplate(ctx, w, "users.html", data)
}

// handleDebugToggleRootSupport toggles the root support flag for a user.
func (s *Server) handleDebugToggleRootSupport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	enable := r.FormValue("enable")
	confirmEmail := r.FormValue("confirm_email")

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	enabling := enable == "1"

	// If enabling, require email confirmation
	if enabling {
		// Look up user to get their email
		user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to look up user: %v", err), http.StatusInternalServerError)
			return
		}

		if confirmEmail != user.Email {
			http.Error(w, "confirmation email does not match", http.StatusBadRequest)
			return
		}
	}

	// Update the root support flag
	newValue := int64(0)
	if enabling {
		newValue = 1
	}

	err := withTx1(s, ctx, (*exedb.Queries).SetUserRootSupport, exedb.SetUserRootSupportParams{
		RootSupport: newValue,
		UserID:      userID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update root support: %v", err), http.StatusInternalServerError)
		return
	}
	s.sendProxyUserChange(ctx, userID)

	action := "disabled"
	if enabling {
		action = "enabled"
	}
	s.slog().InfoContext(ctx, "root support toggled via debug page", "user_id", userID, "action", action)

	// Redirect back to the users page
	http.Redirect(w, r, "/debug/users", http.StatusSeeOther)
}

func (s *Server) handleDebugToggleVMCreation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	disable := r.FormValue("disable") == "1"

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	err := withTx1(s, ctx, (*exedb.Queries).SetUserNewVMCreationDisabled, exedb.SetUserNewVMCreationDisabledParams{
		NewVmCreationDisabled: disable,
		UserID:                userID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update vm creation: %v", err), http.StatusInternalServerError)
		return
	}

	action := "enabled"
	if disable {
		action = "disabled"
	}
	s.slog().InfoContext(ctx, "vm creation toggled via debug page", "user_id", userID, "action", action)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDebugToggleLockout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	lockout := r.FormValue("lockout") == "1"

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	err := withTx1(s, ctx, (*exedb.Queries).SetUserIsLockedOut, exedb.SetUserIsLockedOutParams{
		IsLockedOut: lockout,
		UserID:      userID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update lockout status: %v", err), http.StatusInternalServerError)
		return
	}
	s.sendProxyUserChange(ctx, userID)

	action := "unlocked"
	if lockout {
		action = "locked out"
		if err := s.stopUserBoxes(ctx, userID); err != nil {
			s.slog().ErrorContext(ctx, "failed to stop user boxes during lockout", "user_id", userID, "error", err)
			http.Error(w, fmt.Sprintf("user locked out but failed to stop VMs: %v", err), http.StatusInternalServerError)
			return
		}
	}
	s.slog().InfoContext(ctx, "user lockout toggled via debug page", "user_id", userID, "action", action)

	w.WriteHeader(http.StatusOK)
}

// handleDebugSetUserLimits sets resource limit overrides for a user.
// Pass JSON like {"max_memory": 8000000000, "max_disk": 20000000000, "max_cpus": 4}
// Pass empty string or "{}" to clear overrides.
func (s *Server) handleDebugSetUserLimits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	limitsJSON := r.FormValue("limits")

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	// Validate JSON if provided (non-empty)
	var limitsPtr *string
	if limitsJSON != "" && limitsJSON != "{}" {
		// Validate it's valid JSON
		var parsed UserLimits
		if err := json.Unmarshal([]byte(limitsJSON), &parsed); err != nil {
			http.Error(w, fmt.Sprintf("invalid limits JSON: %v", err), http.StatusBadRequest)
			return
		}
		limitsPtr = &limitsJSON
	}

	// Update the limits
	err := withTx1(s, ctx, (*exedb.Queries).SetUserLimits, exedb.SetUserLimitsParams{
		Limits: limitsPtr,
		UserID: userID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update limits: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "user limits updated via debug page", "user_id", userID, "limits", limitsJSON)
	w.WriteHeader(http.StatusOK)
}

// handleDebugUpdateUserCredit updates a user's gateway credit settings.
// Pass empty string for max/refresh to clear overrides and use defaults.
func (s *Server) handleDebugUpdateUserCredit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	availableStr := r.FormValue("available")
	maxStr := r.FormValue("max")
	refreshStr := r.FormValue("refresh")

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	var availableUSD float64
	var maxUSD, refreshUSD *float64 // nil means use default
	var err error
	if availableStr != "" {
		availableUSD, err = strconv.ParseFloat(availableStr, 64)
		if err != nil {
			http.Error(w, "invalid available value", http.StatusBadRequest)
			return
		}
	}
	if maxStr != "" {
		v, err := strconv.ParseFloat(maxStr, 64)
		if err != nil {
			http.Error(w, "invalid max value", http.StatusBadRequest)
			return
		}
		maxUSD = &v
	}
	if refreshStr != "" {
		v, err := strconv.ParseFloat(refreshStr, 64)
		if err != nil {
			http.Error(w, "invalid refresh value", http.StatusBadRequest)
			return
		}
		refreshUSD = &v
	}

	// Upsert the credit record
	err = s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		return q.UpsertUserLLMCredit(ctx, exedb.UpsertUserLLMCreditParams{
			UserID:          userID,
			AvailableCredit: availableUSD,
			MaxCredit:       maxUSD,
			RefreshPerHour:  refreshUSD,
			LastRefreshAt:   time.Now(),
		})
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update credit: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "LLM credit updated via debug page",
		"user_id", userID,
		"available_usd", availableUSD,
		"max_usd", maxUSD,
		"refresh_per_hour_usd", refreshUSD)

	http.Redirect(w, r, "/debug/users", http.StatusSeeOther)
}

// handleDebugGiftCredits gifts credits to a user's billing account.
func (s *Server) handleDebugGiftCredits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	amountStr := r.FormValue("amount")
	note := r.FormValue("note")

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}
	if amountStr == "" {
		http.Error(w, "amount is required", http.StatusBadRequest)
		return
	}

	amountUSD, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amountUSD <= 0 {
		http.Error(w, "amount must be a positive number (USD)", http.StatusBadRequest)
		return
	}

	// Look up billing account.
	account, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to find billing account for user %q: %v", userID, err), http.StatusBadRequest)
		return
	}

	if err := s.billing.GiftCredits(ctx, account.ID, &billing.GiftCreditsParams{
		AmountUSD:  amountUSD,
		GiftPrefix: billing.GiftPrefixDebug,
		Note:       note,
	}); err != nil {
		http.Error(w, fmt.Sprintf("failed to gift credits: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "credits gifted via debug page",
		"user_id", userID,
		"account_id", account.ID,
		"amount_usd", amountUSD,
		"gift_prefix", billing.GiftPrefixDebug,
		"note", note)

	// Post to Slack feed. Look up the user's email for the message.
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to look up user for slack feed", "user_id", userID, "error", err)
	} else {
		s.slackFeed.CreditGifted(ctx, user.Email, amountUSD, note)
	}

	http.Redirect(w, r, "/debug/billing?userId="+url.QueryEscape(userID), http.StatusSeeOther)
}

// handleDebugAddBilling creates a billing account for a user in test mode.
// This simulates a user completing the Stripe checkout flow by inserting an
// accounts row, activating it, and granting the signup bonus.
func (s *Server) handleDebugAddBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	// Get or create account for this user.
	acct, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	var accountID string
	if errors.Is(err, sql.ErrNoRows) {
		accountID = "exe_" + crand.Text()[:16]
	} else if err != nil {
		http.Error(w, fmt.Sprintf("failed to check existing account: %v", err), http.StatusInternalServerError)
		return
	} else {
		accountID = acct.ID
	}

	now := sqlite.NormalizeTime(time.Now())

	err = s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		// Create account if it doesn't exist yet.
		if err := q.InsertAccount(ctx, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: userID,
		}); err != nil {
			return fmt.Errorf("insert account: %w", err)
		}
		if err := q.ActivateAccount(ctx, exedb.ActivateAccountParams{
			CreatedBy: userID,
			EventAt:   now,
		}); err != nil {
			return fmt.Errorf("activate account: %w", err)
		}
		if _, err := q.InsertBillingEvent(ctx, exedb.InsertBillingEventParams{
			AccountID: accountID,
			EventType: "active",
			EventAt:   now,
		}); err != nil {
			return fmt.Errorf("insert billing event: %w", err)
		}
		// Upgrade account plan to individual.
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: accountID,
			EndedAt:   &now,
		}); err != nil {
			return fmt.Errorf("close existing plan: %w", err)
		}
		changedBy := "debug:add-billing"
		if err := q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    string(entitlement.VersionIndividual),
			StartedAt: now,
			ChangedBy: &changedBy,
		}); err != nil {
			return fmt.Errorf("insert individual plan: %w", err)
		}
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create billing account: %v", err), http.StatusInternalServerError)
		return
	}

	// Grant signup bonus (same as checkout flow).
	giftSignupBonus(ctx, s.billing, accountID, s.slog())

	s.slog().InfoContext(ctx, "billing account created via debug endpoint",
		"user_id", userID,
		"account_id", accountID)

	w.WriteHeader(http.StatusOK)
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func formatInt64Ptr(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

// handleDebugExelets displays a list of all exelets with their status and allows setting a preferred exelet.
func (s *Server) handleDebugExelets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type exeletInfo struct {
		Address       string `json:"address"`
		Version       string `json:"version"`
		Available     bool   `json:"available"`
		Status        string `json:"status"`
		IsPreferred   bool   `json:"is_preferred"`
		InstanceCount int    `json:"instance_count"`
		InstanceLimit int    `json:"instance_limit"`
		LoadAverage   string `json:"load_average"`
		MemFree       string `json:"mem_free"`
		SwapFree      string `json:"swap_free"`
		DiskFree      string `json:"disk_free"`
		RxRate        string `json:"rx_rate"`
		TxRate        string `json:"tx_rate"`
		Error         string `json:"error,omitempty"`
		DebugURL      string `json:"debug_url"`
		CgtopURL      string `json:"cgtop_url"`
	}

	// Get the preferred exelet setting
	preferredAddr, _ := withRxRes0(s, ctx, (*exedb.Queries).GetPreferredExelet)

	var exelets []exeletInfo
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Gather info from all exelet clients in parallel
	for addr, ec := range s.exeletClients {
		wg.Add(1)
		go func(addr string, ec *exeletClient) {
			defer wg.Done()

			info := exeletInfo{
				Address:     addr,
				Version:     ec.client.Version(),
				IsPreferred: addr == preferredAddr,
			}
			if u, err := url.Parse(addr); err == nil {
				host := u.Hostname()
				info.DebugURL = "http://" + host + ":9081"
				info.CgtopURL = "http://" + host + ":9090"
			}

			// Try to get system info to verify connectivity
			sysInfoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			resp, err := ec.client.GetSystemInfo(sysInfoCtx, &computeapi.GetSystemInfoRequest{})
			cancel()
			if err != nil {
				info.Status = "error"
				info.Error = err.Error()
			} else {
				info.Status = "healthy"
				info.Version = resp.Version
			}

			// Count instances
			listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if count, err := ec.countInstances(listCtx); err == nil {
				info.InstanceCount = count
			}
			cancel()

			info.InstanceLimit = int(ec.VMHardLimit())

			// Get load information.
			kibToGB := func(kib int64) string { return fmt.Sprintf("%.1f", float64(kib)/1048576.0) }
			usageCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if usage, err := ec.client.GetMachineUsage(usageCtx, &resourceapi.GetMachineUsageRequest{}); err == nil {
				info.Available = usage.Available
				info.LoadAverage = fmt.Sprintf("%.2f", usage.Usage.LoadAverage)
				info.MemFree = kibToGB(usage.Usage.MemFree)
				info.SwapFree = kibToGB(usage.Usage.SwapFree)
				info.DiskFree = kibToGB(usage.Usage.DiskFree)
				info.RxRate = fmt.Sprintf("%.1f", float64(usage.Usage.RxBytesRate)*8/1000000)
				info.TxRate = fmt.Sprintf("%.1f", float64(usage.Usage.TxBytesRate)*8/1000000)
			}
			cancel()

			mu.Lock()
			exelets = append(exelets, info)
			mu.Unlock()
		}(addr, ec)
	}
	wg.Wait()

	// Sort exelets by address for consistent display
	sort.Slice(exelets, func(i, j int) bool {
		return exelets[i].Address < exelets[j].Address
	})

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(exelets); err != nil {
			s.slog().InfoContext(ctx, "Failed to encode exelets", "error", err)
		}
		return
	}

	// HTML output
	data := struct {
		Exelets []exeletInfo
	}{
		Exelets: exelets,
	}

	s.renderDebugTemplate(ctx, w, "exelets.html", data)
}

// handleDebugSetPreferredExelet sets or clears the preferred exelet.
func (s *Server) handleDebugSetPreferredExelet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	address := r.FormValue("address")

	if address == "" {
		// Clear the preferred exelet
		err := withTx0(s, ctx, (*exedb.Queries).ClearPreferredExelet)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to clear preferred exelet: %v", err), http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(ctx, "preferred exelet cleared via debug page")
		s.slackFeed.PreferredExeletChanged(ctx, "")
	} else {
		// Verify the address is valid (exists in our exelet clients)
		if _, ok := s.exeletClients[address]; !ok {
			http.Error(w, fmt.Sprintf("unknown exelet address: %s", address), http.StatusBadRequest)
			return
		}

		// Set the preferred exelet
		err := withTx1(s, ctx, (*exedb.Queries).SetPreferredExelet, address)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to set preferred exelet: %v", err), http.StatusInternalServerError)
			return
		}

		// Clear the new throttle when switching preferred exelet
		if err := withTx1(s, ctx, (*exedb.Queries).SetNewThrottleEnabled, "false"); err != nil {
			http.Error(w, fmt.Sprintf("failed to clear new throttle: %v", err), http.StatusInternalServerError)
			return
		}

		s.slog().InfoContext(ctx, "preferred exelet set via debug page (new throttle cleared)", "address", address)
		s.slackFeed.PreferredExeletChanged(ctx, address)
	}

	// Redirect back to the exelets page
	http.Redirect(w, r, "/debug/exelets", http.StatusSeeOther)
}

// handleDebugExeletRecover restarts all VMs that should be running on a given exelet.
// This is the equivalent of the manual one-liner:
//
//	for vm in $(curl .../debug/vmlist?host=$HOST); do exelet-ctl ... start $vm; done
func (s *Server) handleDebugExeletRecover(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	address := r.FormValue("address")
	if address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}

	ec := s.getExeletClient(address)
	if ec == nil {
		http.Error(w, fmt.Sprintf("unknown exelet address: %s", address), http.StatusBadRequest)
		return
	}

	// Get list of VMs that should be on this host (same logic as handleDebugVMList).
	dbBoxes, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllBoxesWithOwner)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list VMs: %v", err), http.StatusInternalServerError)
		return
	}

	lockedOutCache := make(map[string]bool)
	isLocked := func(userID string) bool {
		locked, ok := lockedOutCache[userID]
		if !ok {
			locked, _ = s.isUserLockedOut(ctx, userID)
			lockedOutCache[userID] = locked
		}
		return locked
	}

	var containerIDs []string
	var skippedNoContainer, skippedLocked int
	for _, b := range dbBoxes {
		if b.Ctrhost != address {
			continue
		}
		if b.ContainerID == nil {
			skippedNoContainer++
			continue
		}
		if isLocked(b.OwnerUserID) {
			skippedLocked++
			continue
		}
		containerIDs = append(containerIDs, *b.ContainerID)
	}

	// Stream results to the browser as we start instances serially.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	fmt.Fprintf(w, "recovering %s\n", address)
	fmt.Fprintf(w, "starting %d VMs (skipped: %d no container_id, %d locked out)\n\n", len(containerIDs), skippedNoContainer, skippedLocked)
	flush()

	var started, failed int
	for i, id := range containerIDs {
		startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := ec.client.StartInstance(startCtx, &computeapi.StartInstanceRequest{ID: id})
		cancel()
		if err != nil {
			fmt.Fprintf(w, "[%d/%d] %s ERR: %v\n", i+1, len(containerIDs), id, err)
			s.slog().ErrorContext(ctx, "failed to start instance during recover", "address", address, "id", id, "error", err)
			failed++
		} else {
			fmt.Fprintf(w, "[%d/%d] %s ok\n", i+1, len(containerIDs), id)
			started++
		}
		flush()
	}

	fmt.Fprintf(w, "\ndone: %d started, %d failed\n", started, failed)
	s.slog().InfoContext(ctx, "exelet recover completed", "address", address, "started", started, "failed", failed, "total", len(containerIDs))
}

// NewThrottleConfig represents the configuration for throttling "new" VM creation.
type NewThrottleConfig struct {
	Enabled       bool     `json:"enabled"`
	EmailPatterns []string `json:"email_patterns"`
	Message       string   `json:"message"`
}

// GetNewThrottleConfig retrieves the current throttle configuration from the database.
func (s *Server) GetNewThrottleConfig(ctx context.Context) (*NewThrottleConfig, error) {
	config := &NewThrottleConfig{}

	// Get enabled flag
	enabledStr, err := withRxRes0(s, ctx, (*exedb.Queries).GetNewThrottleEnabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get throttle enabled: %w", err)
	}
	config.Enabled = enabledStr == "true"

	// Get email patterns (stored as JSON array)
	patternsStr, err := withRxRes0(s, ctx, (*exedb.Queries).GetNewThrottleEmailPatterns)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get throttle email patterns: %w", err)
	}
	if patternsStr != "" {
		if err := json.Unmarshal([]byte(patternsStr), &config.EmailPatterns); err != nil {
			return nil, fmt.Errorf("failed to parse email patterns: %w", err)
		}
	}

	// Get message
	config.Message, err = withRxRes0(s, ctx, (*exedb.Queries).GetNewThrottleMessage)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to get throttle message: %w", err)
	}

	return config, nil
}

// CheckNewThrottle checks if a user is throttled from creating new VMs.
// Returns (throttled, message) where throttled is true if the user should be denied,
// and message is the denial message to show.
func (s *Server) CheckNewThrottle(ctx context.Context, userID, email string) (bool, string) {
	config, err := s.GetNewThrottleConfig(ctx)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to get throttle config", "error", err)
		return false, ""
	}

	// Check global toggle first
	if config.Enabled {
		msg := config.Message
		if msg == "" {
			msg = "VM creation is temporarily disabled."
		}
		return true, msg
	}

	// userID == "" for tests.
	if userID != "" {
		// Don't throttle users whose plan grants vm:create.
		if s.UserHasEntitlement(ctx, entitlement.SourceWeb, entitlement.VMCreate, userID) {
			return false, ""
		}
	}

	// Check email patterns
	for _, pattern := range config.EmailPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			s.slog().WarnContext(ctx, "invalid throttle email pattern", "pattern", pattern, "error", err)
			continue
		}
		if re.MatchString(email) {
			msg := config.Message
			if msg == "" {
				msg = "VM creation is not available for your account; contact support@exe.dev"
			}
			return true, msg
		}
	}

	// Check for disposable/anonymized email providers
	if isDisposableEmail(email) {
		msg := config.Message
		if msg == "" {
			msg = "VM creation is currently unavailable for your account."
		}
		return true, msg
	}

	return false, ""
}

// handleDebugSignupLimiter displays the signup rate limiter state and login creation settings.
func (s *Server) handleDebugSignupControls(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	loginDisabled := s.IsLoginCreationDisabled(ctx)
	ipAbuseDisabled := s.IsIPAbuseFilterDisabled(ctx)
	powEnabled := s.IsSignupPOWEnabled(ctx)
	powDifficulty := s.signupPOW.GetDifficulty()

	// Capture rate limiter HTML output
	var rateLimitedBuf, allTrackedBuf strings.Builder
	if s.signupLimiter != nil {
		s.signupLimiter.Allow(netip.Addr{}) // ensure internal cache is initialized
		s.signupLimiter.DumpHTML(&rateLimitedBuf, true)
		s.signupLimiter.DumpHTML(&allTrackedBuf, false)
	} else {
		rateLimitedBuf.WriteString("<p>No rate limiter configured.</p>\n")
		allTrackedBuf.WriteString("<p>No rate limiter configured.</p>\n")
	}

	data := struct {
		LoginDisabled   bool
		IPAbuseDisabled bool
		POWEnabled      bool
		POWDifficulty   int
		POWAvgHashes    int
		RateLimitedHTML template.HTML
		AllTrackedHTML  template.HTML
	}{
		LoginDisabled:   loginDisabled,
		IPAbuseDisabled: ipAbuseDisabled,
		POWEnabled:      powEnabled,
		POWDifficulty:   powDifficulty,
		POWAvgHashes:    1 << powDifficulty,
		RateLimitedHTML: template.HTML(rateLimitedBuf.String()),
		AllTrackedHTML:  template.HTML(allTrackedBuf.String()),
	}

	s.renderDebugTemplate(ctx, w, "signup-controls.html", data)
}

// handleDebugSignupLimiterPost handles saving the login creation disabled setting.
func (s *Server) handleDebugSignupLimiterPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	disabled := r.FormValue("disabled") == "true"

	// Save the setting
	disabledStr := "false"
	if disabled {
		disabledStr = "true"
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetLoginCreationDisabled, disabledStr); err != nil {
		http.Error(w, fmt.Sprintf("failed to save setting: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "login creation disabled setting updated via debug page", "disabled", disabled)

	http.Redirect(w, r, "/debug/signup-controls", http.StatusSeeOther)
}

// handleDebugNewThrottle displays the new-throttle configuration page.
func (s *Server) handleDebugNewThrottle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	config, err := s.GetNewThrottleConfig(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get throttle config: %v", err), http.StatusInternalServerError)
		return
	}

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(config); err != nil {
			s.slog().InfoContext(ctx, "Failed to encode throttle config", "error", err)
		}
		return
	}

	// HTML output
	data := struct {
		Enabled       bool
		EmailPatterns string
		Message       string
	}{
		Enabled:       config.Enabled,
		EmailPatterns: strings.Join(config.EmailPatterns, "\n"),
		Message:       config.Message,
	}

	s.renderDebugTemplate(ctx, w, "new-throttle.html", data)
}

// handleDebugNewThrottlePost handles saving the new-throttle configuration.
func (s *Server) handleDebugNewThrottlePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	enabled := r.FormValue("enabled") == "true"
	emailPatternsStr := r.FormValue("email_patterns")
	message := r.FormValue("message")

	// Parse email patterns (one per line)
	var emailPatterns []string
	for line := range strings.SplitSeq(emailPatternsStr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Validate the regex
		if _, err := regexp.Compile(line); err != nil {
			http.Error(w, fmt.Sprintf("invalid regex pattern %q: %v", line, err), http.StatusBadRequest)
			return
		}
		emailPatterns = append(emailPatterns, line)
	}

	// Save enabled flag
	enabledStr := "false"
	if enabled {
		enabledStr = "true"
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetNewThrottleEnabled, enabledStr); err != nil {
		http.Error(w, fmt.Sprintf("failed to save enabled flag: %v", err), http.StatusInternalServerError)
		return
	}

	// Save email patterns as JSON
	patternsJSON, err := json.Marshal(emailPatterns)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to encode email patterns: %v", err), http.StatusInternalServerError)
		return
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetNewThrottleEmailPatterns, string(patternsJSON)); err != nil {
		http.Error(w, fmt.Sprintf("failed to save email patterns: %v", err), http.StatusInternalServerError)
		return
	}

	// Save message
	if err := withTx1(s, ctx, (*exedb.Queries).SetNewThrottleMessage, message); err != nil {
		http.Error(w, fmt.Sprintf("failed to save message: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "new-throttle settings updated via debug page",
		"enabled", enabled,
		"email_patterns_count", len(emailPatterns),
		"message", message)

	// Redirect back to the throttle page
	http.Redirect(w, r, "/debug/new-throttle", http.StatusSeeOther)
}

// ipShardEntry represents a single shard's IP configuration across all tables.
type ipShardEntry struct {
	Shard        int    // 1-1016
	ServingIP    string // current ip_shards value (what DNS returns)
	AWSIP        string // aws_ip_shards value
	LatitudeIP   string // latitude_ip_shards value
	NetActuateIP string // netactuate_ip_shards value
	ServingFrom  string // "aws", "latitude", "netactuate", or "unknown"
}

// handleDebugIPShards displays all IP shard tables and allows toggling the serving source.
func (s *Server) handleDebugIPShards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all tables
	servingShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	awsShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListAWSIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list aws_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	latitudeShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListLatitudeIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list latitude_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	netActuateShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListNetActuateIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list netactuate_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}

	// Build lookup maps (indexed by shard number)
	servingByS := make(map[int64]string, len(servingShards))
	for _, row := range servingShards {
		servingByS[row.Shard] = row.PublicIP
	}
	awsByS := make(map[int64]string, len(awsShards))
	for _, row := range awsShards {
		awsByS[row.Shard] = row.PublicIP
	}
	latByS := make(map[int64]string, len(latitudeShards))
	for _, row := range latitudeShards {
		latByS[row.Shard] = row.PublicIP
	}
	naByS := make(map[int64]string, len(netActuateShards))
	for _, row := range netActuateShards {
		naByS[row.Shard] = row.PublicIP
	}

	// Collect all known shard numbers
	shardSet := make(map[int]bool)
	for _, row := range servingShards {
		shardSet[int(row.Shard)] = true
	}
	for _, row := range awsShards {
		shardSet[int(row.Shard)] = true
	}
	for _, row := range latitudeShards {
		shardSet[int(row.Shard)] = true
	}
	for _, row := range netActuateShards {
		shardSet[int(row.Shard)] = true
	}
	shardNums := make([]int, 0, len(shardSet))
	for s := range shardSet {
		shardNums = append(shardNums, s)
	}
	slices.Sort(shardNums)

	// Build unified shard list
	var entries []ipShardEntry
	for _, shard := range shardNums {
		entry := ipShardEntry{
			Shard:        shard,
			ServingIP:    servingByS[int64(shard)],
			AWSIP:        awsByS[int64(shard)],
			LatitudeIP:   latByS[int64(shard)],
			NetActuateIP: naByS[int64(shard)],
			ServingFrom:  "unknown",
		}
		// Determine serving source
		switch entry.ServingIP {
		case "":
			// leave as unknown
		case entry.AWSIP:
			entry.ServingFrom = "aws"
		case entry.LatitudeIP:
			entry.ServingFrom = "latitude"
		case entry.NetActuateIP:
			entry.ServingFrom = "netactuate"
		}
		entries = append(entries, entry)
	}

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			s.slog().InfoContext(ctx, "Failed to encode IP shards", "error", err)
		}
		return
	}

	// HTML output
	data := struct {
		Entries []ipShardEntry
		LobbyIP string
	}{
		Entries: entries,
		LobbyIP: s.LobbyIP.String(),
	}

	s.renderDebugTemplate(ctx, w, "ipshards.html", data)
}

// handleDebugIPShardsToggle switches a shard's serving IP between AWS, Latitude, and NetActuate.
func (s *Server) handleDebugIPShardsToggle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	shardStr := r.FormValue("shard")
	target := r.FormValue("target") // "aws", "latitude", or "netactuate"

	shard, err := strconv.Atoi(shardStr)
	if err != nil || !publicips.ShardIsValid(shard) {
		http.Error(w, "invalid shard number", http.StatusBadRequest)
		return
	}
	if target != "aws" && target != "latitude" && target != "netactuate" {
		http.Error(w, "target must be 'aws', 'latitude', or 'netactuate'", http.StatusBadRequest)
		return
	}

	// Get the IP from the target table
	var newIP string
	switch target {
	case "aws":
		awsShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListAWSIPShards)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to list aws_ip_shards: %v", err), http.StatusInternalServerError)
			return
		}
		for _, row := range awsShards {
			if int(row.Shard) == shard {
				newIP = row.PublicIP
				break
			}
		}
	case "latitude":
		latShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListLatitudeIPShards)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to list latitude_ip_shards: %v", err), http.StatusInternalServerError)
			return
		}
		for _, row := range latShards {
			if int(row.Shard) == shard {
				newIP = row.PublicIP
				break
			}
		}
	case "netactuate":
		naShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListNetActuateIPShards)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to list netactuate_ip_shards: %v", err), http.StatusInternalServerError)
			return
		}
		for _, row := range naShards {
			if int(row.Shard) == shard {
				newIP = row.PublicIP
				break
			}
		}
	}

	if newIP == "" {
		http.Error(w, fmt.Sprintf("no %s IP found for shard %d", target, shard), http.StatusBadRequest)
		return
	}

	// Update the serving table
	err = withTx1(s, ctx, (*exedb.Queries).UpsertIPShard, exedb.UpsertIPShardParams{
		Shard:    int64(shard),
		PublicIP: newIP,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update ip_shards: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "switched shard serving IP",
		"shard", shard,
		"target", target,
		"new_ip", newIP)

	// Redirect back to the ipshards page
	http.Redirect(w, r, "/debug/ipshards", http.StatusSeeOther)
}

// handleDebugIPShardsLatitude handles upsert/delete of Latitude IP addresses.
func (s *Server) handleDebugIPShardsLatitude(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	shardStr := r.FormValue("shard")
	ip := strings.TrimSpace(r.FormValue("ip"))

	shard, err := strconv.Atoi(shardStr)
	if err != nil || !publicips.ShardIsValid(shard) {
		http.Error(w, "invalid shard number", http.StatusBadRequest)
		return
	}

	// Empty IP means delete
	if ip == "" {
		if err := withTx1(s, ctx, (*exedb.Queries).DeleteLatitudeIPShard, int64(shard)); err != nil {
			http.Error(w, fmt.Sprintf("failed to delete latitude_ip_shards: %v", err), http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(ctx, "deleted latitude IP shard", "shard", shard)
		http.Redirect(w, r, "/debug/ipshards", http.StatusSeeOther)
		return
	}

	// Validate IP format
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid IP address: %v", err), http.StatusBadRequest)
		return
	}
	if !addr.Is4() {
		http.Error(w, "must be an IPv4 address", http.StatusBadRequest)
		return
	}

	// Check for duplicates across all IP shard tables
	servingShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	awsShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListAWSIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list aws_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	latitudeShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListLatitudeIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list latitude_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}

	// Check serving shards
	for _, row := range servingShards {
		if row.PublicIP == ip {
			http.Error(w, fmt.Sprintf("IP %s already in use in ip_shards (shard %d)", ip, row.Shard), http.StatusBadRequest)
			return
		}
	}
	// Check AWS shards
	for _, row := range awsShards {
		if row.PublicIP == ip {
			http.Error(w, fmt.Sprintf("IP %s already in use in aws_ip_shards (shard %d)", ip, row.Shard), http.StatusBadRequest)
			return
		}
	}
	// Check Latitude shards (excluding current shard being updated)
	for _, row := range latitudeShards {
		if row.PublicIP == ip && int(row.Shard) != shard {
			http.Error(w, fmt.Sprintf("IP %s already in use in latitude_ip_shards (shard %d)", ip, row.Shard), http.StatusBadRequest)
			return
		}
	}

	// Upsert
	err = withTx1(s, ctx, (*exedb.Queries).UpsertLatitudeIPShard, exedb.UpsertLatitudeIPShardParams{
		Shard:    int64(shard),
		PublicIP: ip,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to upsert latitude_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "upserted latitude IP shard", "shard", shard, "ip", ip)
	http.Redirect(w, r, "/debug/ipshards", http.StatusSeeOther)
}

// handleDebugIPShardsNetActuate handles upsert/delete of NetActuate IP addresses.
func (s *Server) handleDebugIPShardsNetActuate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	shardStr := r.FormValue("shard")
	ip := strings.TrimSpace(r.FormValue("ip"))

	shard, err := strconv.Atoi(shardStr)
	if err != nil || !publicips.ShardIsValid(shard) {
		http.Error(w, "invalid shard number", http.StatusBadRequest)
		return
	}

	// Empty IP means delete
	if ip == "" {
		if err := withTx1(s, ctx, (*exedb.Queries).DeleteNetActuateIPShard, int64(shard)); err != nil {
			http.Error(w, fmt.Sprintf("failed to delete netactuate_ip_shards: %v", err), http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(ctx, "deleted netactuate IP shard", "shard", shard)
		http.Redirect(w, r, "/debug/ipshards", http.StatusSeeOther)
		return
	}

	// Validate IP format
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid IP address: %v", err), http.StatusBadRequest)
		return
	}
	if !addr.Is4() {
		http.Error(w, "must be an IPv4 address", http.StatusBadRequest)
		return
	}

	// Check for duplicates across all IP shard tables
	servingShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	awsShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListAWSIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list aws_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	latitudeShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListLatitudeIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list latitude_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}
	netActuateShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListNetActuateIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list netactuate_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}

	// Check serving shards
	for _, row := range servingShards {
		if row.PublicIP == ip {
			http.Error(w, fmt.Sprintf("IP %s already in use in ip_shards (shard %d)", ip, row.Shard), http.StatusBadRequest)
			return
		}
	}
	// Check AWS shards
	for _, row := range awsShards {
		if row.PublicIP == ip {
			http.Error(w, fmt.Sprintf("IP %s already in use in aws_ip_shards (shard %d)", ip, row.Shard), http.StatusBadRequest)
			return
		}
	}
	// Check Latitude shards
	for _, row := range latitudeShards {
		if row.PublicIP == ip {
			http.Error(w, fmt.Sprintf("IP %s already in use in latitude_ip_shards (shard %d)", ip, row.Shard), http.StatusBadRequest)
			return
		}
	}
	// Check NetActuate shards (excluding current shard being updated)
	for _, row := range netActuateShards {
		if row.PublicIP == ip && int(row.Shard) != shard {
			http.Error(w, fmt.Sprintf("IP %s already in use in netactuate_ip_shards (shard %d)", ip, row.Shard), http.StatusBadRequest)
			return
		}
	}

	// Upsert
	err = withTx1(s, ctx, (*exedb.Queries).UpsertNetActuateIPShard, exedb.UpsertNetActuateIPShardParams{
		Shard:    int64(shard),
		PublicIP: ip,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to upsert netactuate_ip_shards: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "upserted netactuate IP shard", "shard", shard, "ip", ip)
	http.Redirect(w, r, "/debug/ipshards", http.StatusSeeOther)
}

// handleDebugLogForm renders a simple form to log an error message.
func (s *Server) handleDebugLogForm(w http.ResponseWriter, r *http.Request) {
	s.renderDebugTemplate(r.Context(), w, "log-form.html", nil)
}

// handleDebugLog logs an error message provided via POST request.
func (s *Server) handleDebugLog(w http.ResponseWriter, r *http.Request) {
	text := r.FormValue("text")
	if text == "" {
		http.Error(w, "text parameter is required", http.StatusBadRequest)
		return
	}
	s.slog().ErrorContext(r.Context(), text)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "logged: %s\n", text)
}

// handleDebugTestimonials displays all testimonials with their approval status.
func (s *Server) handleDebugTestimonials(w http.ResponseWriter, r *http.Request) {
	testimonials := AllTestimonials()

	type testimonialData struct {
		Number    int
		Approved  bool
		QuoteHTML template.HTML
		Author    string
		Link      string
	}

	var testimonialList []testimonialData
	for i, t := range testimonials {
		testimonialList = append(testimonialList, testimonialData{
			Number:    i + 1,
			Approved:  t.Approved,
			QuoteHTML: template.HTML(strings.ReplaceAll(html.EscapeString(t.Quote), "\n\n", "<br><br>")),
			Author:    t.Author,
			Link:      t.Link,
		})
	}

	data := struct {
		Testimonials []testimonialData
	}{
		Testimonials: testimonialList,
	}

	s.renderDebugTemplate(r.Context(), w, "testimonials.html", data)
}

// IsLoginCreationDisabled reports whether new account creation is disabled.
func (s *Server) IsLoginCreationDisabled(ctx context.Context) bool {
	val, err := withRxRes0(s, ctx, (*exedb.Queries).GetLoginCreationDisabled)
	if err != nil {
		return false
	}
	return val == "true"
}

// handleDebugEmailForm renders a form to send test emails.
func (s *Server) handleDebugEmailForm(w http.ResponseWriter, r *http.Request) {
	postmarkAvailable := s.emailSenders.Postmark != nil
	mailgunAvailable := s.emailSenders.Mailgun != nil

	var result string
	var isError bool
	if res := r.URL.Query().Get("result"); res != "" {
		result = res
		isError = r.URL.Query().Get("error") == "1"
	}

	data := struct {
		PostmarkAvailable bool
		MailgunAvailable  bool
		Result            string
		IsError           bool
	}{
		PostmarkAvailable: postmarkAvailable,
		MailgunAvailable:  mailgunAvailable,
		Result:            result,
		IsError:           isError,
	}

	s.renderDebugTemplate(r.Context(), w, "email-form.html", data)
}

// handleDebugEmailSend sends a test email via the selected provider.
func (s *Server) handleDebugEmailSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	to := r.FormValue("to")
	subject := r.FormValue("subject")
	body := r.FormValue("body")
	provider := r.FormValue("provider")

	if to == "" || subject == "" || body == "" {
		http.Redirect(w, r, "/debug/email?result=Missing+required+fields&error=1", http.StatusSeeOther)
		return
	}

	var sender email.Sender

	switch provider {
	case "postmark":
		if s.emailSenders.Postmark == nil {
			http.Redirect(w, r, "/debug/email?result=Postmark+not+configured&error=1", http.StatusSeeOther)
			return
		}
		sender = s.emailSenders.Postmark
	case "mailgun":
		if s.emailSenders.Mailgun == nil {
			http.Redirect(w, r, "/debug/email?result=Mailgun+not+configured&error=1", http.StatusSeeOther)
			return
		}
		sender = s.emailSenders.Mailgun
	default:
		http.Redirect(w, r, "/debug/email?result=Invalid+provider&error=1", http.StatusSeeOther)
		return
	}

	from := fmt.Sprintf("%s <support@%s>", s.env.WebHost, s.env.WebHost)
	err := sender.Send(ctx, email.Message{
		Type:    email.TypeDebugTest,
		From:    from,
		To:      to,
		Subject: subject,
		Body:    body,
		ReplyTo: "",
		Attrs:   nil,
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "debug email send failed", "provider", provider, "to", to, "error", err)
		http.Redirect(w, r, fmt.Sprintf("/debug/email?result=%s&error=1", html.EscapeString(err.Error())), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/debug/email?result=Email+sent+successfully+via+%s+to+%s", provider, html.EscapeString(to)), http.StatusSeeOther)
}

// IsSignupPOWEnabled reports whether proof-of-work is required for new signups.
func (s *Server) IsSignupPOWEnabled(ctx context.Context) bool {
	val, err := withRxRes0(s, ctx, (*exedb.Queries).GetSignupPOWEnabled)
	if err != nil {
		return false
	}
	return val == "true"
}

// handleDebugSignupPOWPost handles saving the signup POW enabled setting.
func (s *Server) handleDebugSignupPOWPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	enabled := r.FormValue("enabled") == "true"

	enabledStr := "false"
	if enabled {
		enabledStr = "true"
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetSignupPOWEnabled, enabledStr); err != nil {
		http.Error(w, fmt.Sprintf("failed to save setting: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "signup POW setting updated via debug page", "enabled", enabled)

	http.Redirect(w, r, "/debug/signup-controls", http.StatusSeeOther)
}

// handleDebugSignupReject displays the signup rejections and bypass list.
func (s *Server) handleDebugSignupReject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get recent rejections
	rejections, err := withRxRes1(s, ctx, (*exedb.Queries).GetRecentSignupRejections, int64(200))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get rejections: %v", err), http.StatusInternalServerError)
		return
	}

	// Get bypass list
	bypassListDB, err := withRxRes0(s, ctx, (*exedb.Queries).ListEmailQualityBypass)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get bypass list: %v", err), http.StatusInternalServerError)
		return
	}

	// Build bypass set for quick lookup
	bypassSet := make(map[string]bool)
	for _, b := range bypassListDB {
		bypassSet[b.Email] = true
	}

	type bypassInfo struct {
		Email   string
		Reason  string
		AddedAt string
		AddedBy string
	}

	type rejectionInfo struct {
		Email           string
		IP              string
		IPQSDisplay     string
		IPQSJSON        string
		HasIPQS         bool
		Reason          string
		Source          string
		RejectedAt      string
		AlreadyBypassed bool
	}

	var bypassList []bypassInfo
	for _, b := range bypassListDB {
		addedAt := ""
		if b.AddedAt != nil {
			addedAt = b.AddedAt.Format("2006-01-02 15:04:05")
		}
		bypassList = append(bypassList, bypassInfo{
			Email:   b.Email,
			Reason:  b.Reason,
			AddedAt: addedAt,
			AddedBy: b.AddedBy,
		})
	}

	var rejectionList []rejectionInfo
	for _, rej := range rejections {
		rejectedAt := ""
		if rej.RejectedAt != nil {
			rejectedAt = rej.RejectedAt.Format("2006-01-02 15:04:05")
		}

		// Summarize IPQS
		ipqsDisplay := "missing"
		ipqsJSON := ""
		hasIPQS := false
		if rej.IpqsResponseJson != nil {
			rawJSON := strings.TrimSpace(*rej.IpqsResponseJson)
			if rawJSON != "" {
				ipqsJSON = rawJSON
				hasIPQS = true
				ipqsDisplay = "no location data"
				var payload struct {
					CountryCode string `json:"country_code"`
					Region      string `json:"region"`
				}
				if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
					ipqsDisplay = "invalid JSON"
				} else {
					var parts []string
					if payload.CountryCode != "" {
						parts = append(parts, payload.CountryCode)
					}
					if payload.Region != "" {
						parts = append(parts, payload.Region)
					}
					if len(parts) > 0 {
						ipqsDisplay = strings.Join(parts, " / ")
					}
				}
			}
		}

		rejectionList = append(rejectionList, rejectionInfo{
			Email:           rej.Email,
			IP:              rej.Ip,
			IPQSDisplay:     ipqsDisplay,
			IPQSJSON:        ipqsJSON,
			HasIPQS:         hasIPQS,
			Reason:          rej.Reason,
			Source:          rej.Source,
			RejectedAt:      rejectedAt,
			AlreadyBypassed: bypassSet[rej.Email],
		})
	}

	data := struct {
		BypassList []bypassInfo
		Rejections []rejectionInfo
	}{
		BypassList: bypassList,
		Rejections: rejectionList,
	}

	s.renderDebugTemplate(ctx, w, "signup-reject.html", data)
}

// handleDebugSignupRejectPost handles adding/removing emails from the bypass list.
func (s *Server) handleDebugSignupRejectPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	action := r.FormValue("action")
	email := r.FormValue("email")

	if email == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}

	switch action {
	case "add":
		reason := r.FormValue("reason")
		if reason == "" {
			reason = "Added via debug page"
		}
		addedBy := "debug"
		lc := new(local.Client)
		if who, err := lc.WhoIs(ctx, r.RemoteAddr); err == nil && who.UserProfile != nil && who.UserProfile.LoginName != "" {
			addedBy = fmt.Sprintf("debug (%s)", who.UserProfile.LoginName)
		}
		err := withTx1(s, ctx, (*exedb.Queries).InsertEmailQualityBypass, exedb.InsertEmailQualityBypassParams{
			Email:   email,
			Reason:  reason,
			AddedBy: addedBy,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to add bypass: %v", err), http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(ctx, "email added to quality bypass list via debug page", "email", email, "reason", reason)

	case "delete":
		err := withTx1(s, ctx, (*exedb.Queries).DeleteEmailQualityBypass, email)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to remove bypass: %v", err), http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(ctx, "email removed from quality bypass list via debug page", "email", email)

	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/debug/signup-reject", http.StatusSeeOther)
}

// handleDebugInvite displays the invite code management page.
func (s *Server) handleDebugInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get unused system invite codes
	systemCodesDB, err := withRxRes0(s, ctx, (*exedb.Queries).ListUnusedSystemInviteCodes)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list system codes: %v", err), http.StatusInternalServerError)
		return
	}

	type systemCode struct {
		Code        string
		PlanType    string
		AssignedBy  string
		AssignedFor string
		CreatedAt   string
	}

	var systemCodes []systemCode
	for _, code := range systemCodesDB {
		createdAt := "unknown"
		if code.AssignedAt != nil {
			createdAt = code.AssignedAt.Format("2006-01-02 15:04")
		}
		assignedFor := ""
		if code.AssignedFor != nil {
			assignedFor = *code.AssignedFor
		}
		systemCodes = append(systemCodes, systemCode{
			Code:        code.Code,
			PlanType:    code.PlanType,
			AssignedBy:  code.AssignedBy,
			AssignedFor: assignedFor,
			CreatedAt:   createdAt,
		})
	}

	data := struct {
		SystemCodes []systemCode
	}{
		SystemCodes: systemCodes,
	}

	s.renderDebugTemplate(ctx, w, "invite.html", data)
}

// handleDebugInvitePost handles creating a new invite code.
func (s *Server) handleDebugInvitePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	action := r.FormValue("action")

	// Get admin identity from Tailscale
	assignedBy := "debug"
	lc := new(local.Client)
	if who, err := lc.WhoIs(ctx, r.RemoteAddr); err == nil && who.UserProfile != nil && who.UserProfile.LoginName != "" {
		assignedBy = who.UserProfile.LoginName
	}

	switch action {
	case "create":
		s.handleDebugInviteCreate(w, r, ctx, assignedBy)
	case "give_to_user":
		s.handleDebugInviteGiveToUser(w, r, ctx, assignedBy)
	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
	}
}

func (s *Server) handleDebugInviteCreate(w http.ResponseWriter, r *http.Request, ctx context.Context, assignedBy string) {
	planType := r.FormValue("plan_type")
	if planType != "trial" && planType != "free" {
		http.Error(w, "invalid plan_type", http.StatusBadRequest)
		return
	}

	// Get optional "for" field
	assignedFor := r.FormValue("assigned_for")
	var assignedForPtr *string
	if assignedFor != "" {
		assignedForPtr = &assignedFor
	}

	// Generate a unique code
	code, err := withTxRes0(s, ctx, (*exedb.Queries).GenerateUniqueInviteCode)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to generate invite code: %v", err), http.StatusInternalServerError)
		return
	}

	// Create the invite code (system code, so no assigned_to_user_id)
	_, err = withTxRes1(s, ctx, (*exedb.Queries).CreateInviteCode, exedb.CreateInviteCodeParams{
		Code:             code,
		PlanType:         planType,
		AssignedToUserID: nil,
		AssignedBy:       assignedBy,
		AssignedFor:      assignedForPtr,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create invite code: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "invite code created via debug page", "code", code, "plan_type", planType, "assigned_by", assignedBy, "assigned_for", assignedFor)

	// Return JSON if requested (for programmatic access)
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"code": code})
		return
	}

	http.Redirect(w, r, "/debug/invite", http.StatusSeeOther)
}

func (s *Server) handleDebugInviteGiveToUser(w http.ResponseWriter, r *http.Request, ctx context.Context, assignedBy string) {
	userEmail := r.FormValue("email")
	if userEmail == "" {
		http.Error(w, "email is required", http.StatusBadRequest)
		return
	}

	countStr := r.FormValue("count")
	count, err := strconv.Atoi(countStr)
	if err != nil || count < 1 || count > 10 {
		http.Error(w, "count must be between 1 and 10", http.StatusBadRequest)
		return
	}

	planType := r.FormValue("plan_type")
	if planType != "trial" && planType != "free" {
		http.Error(w, "invalid plan_type", http.StatusBadRequest)
		return
	}

	// Look up user by email
	user, err := s.GetUserByEmail(ctx, userEmail)
	if err != nil || user == nil {
		http.Error(w, fmt.Sprintf("user not found: %s", userEmail), http.StatusBadRequest)
		return
	}

	if err := s.giveInvitesToUser(ctx, user, count, planType, assignedBy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/debug/invite", http.StatusSeeOther)
}

func (s *Server) giveInvitesToUser(ctx context.Context, user *exedb.User, count int, planType, assignedBy string) error {
	var planDesc string
	switch planType {
	case "trial":
		planDesc = "1 month free trial"
	case "free":
		planDesc = "free"
	default:
		return fmt.Errorf("invalid plan_type: %s", planType)
	}

	for range count {
		code, err := withTxRes0(s, ctx, (*exedb.Queries).GenerateUniqueInviteCode)
		if err != nil {
			return fmt.Errorf("failed to generate invite code: %w", err)
		}

		_, err = withTxRes1(s, ctx, (*exedb.Queries).CreateInviteCode, exedb.CreateInviteCodeParams{
			Code:             code,
			PlanType:         planType,
			AssignedToUserID: &user.UserID,
			AssignedBy:       assignedBy,
			AssignedFor:      nil,
		})
		if err != nil {
			return fmt.Errorf("failed to create invite code: %w", err)
		}

		s.slog().InfoContext(ctx, "invite code given to user via debug page",
			"code", code,
			"plan_type", planType,
			"assigned_by", assignedBy,
			"user_email", user.Email,
			"user_id", user.UserID)
	}

	inviteWord := "invites"
	if count == 1 {
		inviteWord = "invite"
	}
	subject := fmt.Sprintf("%s: you have %d new %s to share", s.env.WebHost, count, inviteWord)
	body := fmt.Sprintf(`Hi,

You have been given %d new %s.

Each invite code grants the recipient a %s plan.

To allocate and share your invites in your dashboard, log in and visit:
https://%s/

---
%s
`, count, inviteWord, planDesc, s.env.WebHost, s.env.WebHost)

	if err := s.sendEmail(ctx, sendEmailParams{
		emailType: email.TypeInvitesAllocated,
		to:        user.Email,
		subject:   subject,
		body:      body,
		fromName:  "",
		replyTo:   "",
		attrs:     []slog.Attr{slog.String("user_id", user.UserID)},
	}); err != nil {
		s.slog().WarnContext(ctx, "failed to send invites allocated email", "to", user.Email, "error", err)
	}

	return nil
}

func (s *Server) handleDebugInviteBulkPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	planType := r.FormValue("plan_type")
	if planType != "trial" && planType != "free" {
		http.Error(w, "invalid plan_type", http.StatusBadRequest)
		return
	}

	assignedFor := r.FormValue("assigned_for")
	if assignedFor == "" {
		http.Error(w, "assigned_for is required", http.StatusBadRequest)
		return
	}

	countStr := r.FormValue("count")
	count, err := strconv.Atoi(countStr)
	if err != nil || count < 1 || count > 1000 {
		http.Error(w, "count must be between 1 and 1000", http.StatusBadRequest)
		return
	}

	// Get admin identity from Tailscale
	assignedBy := "debug"
	lc := new(local.Client)
	if who, err := lc.WhoIs(ctx, r.RemoteAddr); err == nil && who.UserProfile != nil && who.UserProfile.LoginName != "" {
		assignedBy = who.UserProfile.LoginName
	}

	codes := make([]string, 0, count)
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		for range count {
			code, err := queries.GenerateUniqueInviteCode(ctx)
			if err != nil {
				return fmt.Errorf("generate invite code: %w", err)
			}

			_, err = queries.CreateInviteCode(ctx, exedb.CreateInviteCodeParams{
				Code:        code,
				PlanType:    planType,
				AssignedBy:  assignedBy,
				AssignedFor: &assignedFor,
				IsBatch:     true,
			})
			if err != nil {
				return fmt.Errorf("create invite code: %w", err)
			}

			codes = append(codes, code)
		}
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create bulk invite codes: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "bulk invite codes created via debug page",
		"count", len(codes), "plan_type", planType,
		"assigned_by", assignedBy, "assigned_for", assignedFor)

	// Return JSON if requested (for programmatic access)
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"codes": codes, "count": len(codes)})
		return
	}

	data := struct {
		Codes       []string
		PlanType    string
		AssignedFor string
	}{
		Codes:       codes,
		PlanType:    planType,
		AssignedFor: assignedFor,
	}

	s.renderDebugTemplate(ctx, w, "invite-bulk.html", data)
}

// handleDebugAllInviteCodes displays all invite codes with giver and recipient emails.
func (s *Server) handleDebugAllInviteCodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all invite codes with emails
	codes, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllInviteCodesWithEmails)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list invite codes: %v", err), http.StatusInternalServerError)
		return
	}

	// For JSON format
	if r.URL.Query().Get("format") == "json" {
		type inviteCodeInfo struct {
			ID             int64   `json:"id"`
			Code           string  `json:"code"`
			PlanType       string  `json:"plan_type"`
			GiverUserID    *string `json:"giver_user_id,omitempty"`
			GiverEmail     string  `json:"giver_email"`
			AssignedAt     string  `json:"assigned_at"`
			AssignedBy     string  `json:"assigned_by"`
			AssignedFor    string  `json:"assigned_for,omitempty"`
			RecipientEmail string  `json:"recipient_email,omitempty"`
			UsedAt         string  `json:"used_at,omitempty"`
			AllocatedAt    string  `json:"allocated_at,omitempty"`
			Status         string  `json:"status"`
		}

		result := make([]inviteCodeInfo, 0, len(codes))
		for _, code := range codes {
			info := inviteCodeInfo{
				ID:         code.ID,
				Code:       code.Code,
				PlanType:   code.PlanType,
				AssignedBy: code.AssignedBy,
			}
			if code.AssignedToUserID != nil {
				info.GiverUserID = code.AssignedToUserID
			}
			if code.GiverEmail != nil {
				info.GiverEmail = *code.GiverEmail
			} else {
				info.GiverEmail = "(system)"
			}
			if code.AssignedAt != nil {
				info.AssignedAt = code.AssignedAt.Format("2006-01-02 15:04")
			}
			if code.AssignedFor != nil {
				info.AssignedFor = *code.AssignedFor
			}
			if code.RecipientEmail != nil {
				info.RecipientEmail = *code.RecipientEmail
			}
			if code.UsedAt != nil {
				info.UsedAt = code.UsedAt.Format("2006-01-02 15:04")
			}
			if code.AllocatedAt != nil {
				info.AllocatedAt = code.AllocatedAt.Format("2006-01-02 15:04")
			}
			if code.UsedByUserID != nil {
				info.Status = "used"
			} else if code.AllocatedAt != nil {
				info.Status = "allocated"
			} else {
				info.Status = "unused"
			}
			result = append(result, info)
		}

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			s.slog().InfoContext(ctx, "Failed to encode invite codes", "error", err)
		}
		return
	}

	// HTML format with DataTables
	s.renderDebugTemplate(ctx, w, "all-invite-codes.html", nil)
}

// InviteTreeNode represents a user node in the invite tree for template rendering.
type InviteTreeNode struct {
	ID          string
	Email       string
	Children    []*InviteTreeNode
	DirectCount int // number of direct invites (children)
	TotalCount  int // total descendants in tree
}

// handleDebugInviteTree displays a tree visualization of invite relationships.
func (s *Server) handleDebugInviteTree(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	codes, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllInviteCodesWithEmails)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list invite codes: %v", err), http.StatusInternalServerError)
		return
	}

	// Track users and relationships
	userEmails := make(map[string]string) // user_id -> email
	parentMap := make(map[string]string)  // user_id -> parent_id
	hasInvitedSomeone := make(map[string]bool)

	for _, code := range codes {
		if code.UsedByUserID == nil || code.RecipientEmail == nil {
			continue
		}

		recipientID := *code.UsedByUserID
		recipientEmail := *code.RecipientEmail
		userEmails[recipientID] = recipientEmail

		if code.AssignedToUserID != nil && code.GiverEmail != nil {
			giverID := *code.AssignedToUserID
			userEmails[giverID] = *code.GiverEmail
			hasInvitedSomeone[giverID] = true

			// Only set parent if not already set (first invite wins)
			if _, hasParent := parentMap[recipientID]; !hasParent {
				parentMap[recipientID] = giverID
			}
		}
	}

	// Build tree nodes only for users who were invited OR have invited someone
	nodes := make(map[string]*InviteTreeNode)
	for id, email := range userEmails {
		_, wasInvited := parentMap[id]
		if wasInvited || hasInvitedSomeone[id] {
			nodes[id] = &InviteTreeNode{ID: id, Email: email}
		}
	}

	// Link children to parents
	var roots []*InviteTreeNode
	for id, node := range nodes {
		if parentID, ok := parentMap[id]; ok {
			if parent, exists := nodes[parentID]; exists {
				parent.Children = append(parent.Children, node)
			} else {
				// Parent not in tree (filtered out), this becomes a root
				roots = append(roots, node)
			}
		} else if hasInvitedSomeone[id] {
			// No parent but has invited someone: root node
			roots = append(roots, node)
		}
	}

	// Sort children and roots alphabetically by email
	var sortChildren func(n *InviteTreeNode)
	sortChildren = func(n *InviteTreeNode) {
		sort.Slice(n.Children, func(i, j int) bool {
			return n.Children[i].Email < n.Children[j].Email
		})
		for _, c := range n.Children {
			sortChildren(c)
		}
	}
	for _, root := range roots {
		sortChildren(root)
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].Email < roots[j].Email
	})

	// Compute counts (direct and total descendants)
	var computeCounts func(n *InviteTreeNode) int
	computeCounts = func(n *InviteTreeNode) int {
		n.DirectCount = len(n.Children)
		n.TotalCount = n.DirectCount
		for _, c := range n.Children {
			n.TotalCount += computeCounts(c)
		}
		return n.TotalCount
	}
	for _, root := range roots {
		computeCounts(root)
	}

	s.renderDebugTemplate(ctx, w, "invite-tree.html", roots)
}

// IsIPAbuseFilterDisabled reports whether the IP abuse filter is disabled.
func (s *Server) IsIPAbuseFilterDisabled(ctx context.Context) bool {
	val, err := withRxRes0(s, ctx, (*exedb.Queries).GetIPAbuseFilterDisabled)
	if err != nil {
		return false
	}
	return val == "true"
}

// handleDebugIPAbuseFilterPost handles saving the IP abuse filter disabled setting.
func (s *Server) handleDebugIPAbuseFilterPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	disabled := r.FormValue("disabled") == "true"

	disabledStr := "false"
	if disabled {
		disabledStr = "true"
	}
	if err := withTx1(s, ctx, (*exedb.Queries).SetIPAbuseFilterDisabled, disabledStr); err != nil {
		http.Error(w, fmt.Sprintf("failed to save setting: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "IP abuse filter setting updated via debug page", "disabled", disabled)

	http.Redirect(w, r, "/debug/signup-controls", http.StatusSeeOther)
}

// handleDebugUser displays detailed information about a single user and their boxes.
func (s *Server) handleDebugUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.URL.Query().Get("userId")
	if userID == "" {
		http.Error(w, "userId parameter is required", http.StatusBadRequest)
		return
	}
	invitePostSuccessful := r.URL.Query().Get("invite_posted") == "1"

	// Look up the user
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("user %q not found", userID), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up user: %v", err), http.StatusInternalServerError)
		return
	}

	// Fetch account info
	accounts, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllAccounts)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to list accounts", "error", err)
	}

	type billingAccountInfo struct {
		AccountID    string
		PlanID       string
		LatestStatus string
		BillingURL   string
	}

	var userAccounts []exedb.Account
	for _, a := range accounts {
		if a.CreatedBy == userID {
			userAccounts = append(userAccounts, a)
		}
	}
	sort.Slice(userAccounts, func(i, j int) bool {
		return userAccounts[i].ID < userAccounts[j].ID
	})

	var billingAccounts []billingAccountInfo
	for _, a := range userAccounts {
		status, err := withRxRes1(s, ctx, (*exedb.Queries).GetLatestBillingStatus, a.ID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			status = "pending"
		case err != nil:
			s.slog().WarnContext(
				ctx,
				"failed to get latest billing status for account",
				"error",
				err,
				"account_id",
				a.ID,
				"user_id",
				userID,
			)
			status = "pending"
		}
		if status != "active" && status != "canceled" {
			status = "pending"
		}
		var planID string
		if ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, a.ID); err == nil {
			planID = ap.PlanID
		}
		billingAccounts = append(billingAccounts, billingAccountInfo{
			AccountID:    a.ID,
			PlanID:       planID,
			LatestStatus: status,
			BillingURL:   billing.MakeCustomerDashboardURL(a.ID),
		})
	}

	// Fetch LLM credit info and plan
	credit, creditErr := withRxRes1(s, ctx, (*exedb.Queries).GetUserLLMCredit, userID)
	hasCredit := creditErr == nil

	var plan llmgateway.Plan
	var creditEffective float64
	if hasCredit {
		plan, _ = llmgateway.PlanForUser(ctx, s.db, userID, &credit)
		creditEffective, _ = llmgateway.CalculateRefreshedCredit(
			credit.AvailableCredit,
			plan.MaxCredit,
			plan.RefreshPerHour,
			credit.LastRefreshAt,
			time.Now(),
		)
	}

	// Fetch user's boxes
	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to list boxes for user", "error", err)
		boxes = nil
	}

	inviteStats, err := withRxRes1(s, ctx, (*exedb.Queries).GetInviteCodeStatsForUser, &userID)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to load invite stats for user", "error", err, "user_id", userID)
	}

	// Build template data
	type boxInfo struct {
		Name          string
		Status        string
		Image         string
		Host          string
		CreatedAt     string
		LastStartedAt string
	}

	var boxList []boxInfo
	for _, b := range boxes {
		boxList = append(boxList, boxInfo{
			Name:          b.Name,
			Status:        b.Status,
			Image:         b.Image,
			Host:          b.Ctrhost,
			CreatedAt:     formatTime(b.CreatedAt),
			LastStartedAt: formatTime(b.LastStartedAt),
		})
	}

	data := struct {
		Email                      string
		UserID                     string
		CreatedAt                  string
		CreatedForLoginWithExe     bool
		RootSupport                bool
		VMCreationDisabled         bool
		PlanName                   string
		PlanVersion                string
		DiscordID                  string
		DiscordUsername            string
		BillingExemption           string
		BillingTrialEndsAt         string
		SignedUpWithInviteID       string
		BillingAccounts            []billingAccountInfo
		HasCredit                  bool
		CreditPlanName             string
		CreditAvailableUSD         float64
		CreditEffectiveUSD         float64
		CreditMaxUSD               float64
		CreditMaxUSDOverride       *float64
		CreditBonusRemainingUSD    float64
		CreditRefreshPerHrUSD      float64
		CreditRefreshPerHrOverride *float64
		CreditTotalUsedUSD         float64
		CreditLastRefreshAt        string
		InvitesTotalAllTimeGiven   int64
		InvitesAllocatedCount      int64
		InvitesAcceptedCount       int64
		InvitePostSuccessful       bool
		Boxes                      []boxInfo
		Region                     string
		RegionDisplay              string
		GLBDefault                 string
		AllRegions                 []region.Region
		BoxesOutsideRegion         []struct {
			Name   string
			Region string
		}
	}{
		Email:                    user.Email,
		UserID:                   user.UserID,
		CreatedAt:                formatTime(user.CreatedAt),
		CreatedForLoginWithExe:   user.CreatedForLoginWithExe,
		RootSupport:              user.RootSupport == 1,
		VMCreationDisabled:       user.NewVmCreationDisabled,
		DiscordID:                ptrStr(user.DiscordID),
		DiscordUsername:          ptrStr(user.DiscordUsername),
		BillingExemption:         ptrStr(user.BillingExemption),
		BillingTrialEndsAt:       formatTime(user.BillingTrialEndsAt),
		SignedUpWithInviteID:     formatInt64Ptr(user.SignedUpWithInviteID),
		BillingAccounts:          billingAccounts,
		HasCredit:                hasCredit,
		InvitesTotalAllTimeGiven: inviteStats.TotalAllTimeGiven,
		InvitesAllocatedCount:    inviteStats.AllocatedCount,
		InvitesAcceptedCount:     inviteStats.AcceptedCount,
		InvitePostSuccessful:     invitePostSuccessful,
		Boxes:                    boxList,
		Region:                   user.Region,
		AllRegions:               region.All(),
	}

	if billingRow, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBilling, userID); err == nil {
		inputs := entitlement.UserPlanInputs{
			Category:           billingRow.Category,
			BillingStatus:      billingRow.BillingStatus,
			BillingExemption:   billingRow.BillingExemption,
			CreatedAt:          billingRow.CreatedAt,
			BillingTrialEndsAt: billingRow.BillingTrialEndsAt,
		}
		version := entitlement.GetPlanVersion(inputs)
		data.PlanVersion = string(version)
		data.PlanName = entitlement.PlanName(version)

	}

	if r, err := region.ByCode(user.Region); err == nil {
		data.RegionDisplay = r.Display
	}

	userDefaults, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserDefaults, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.slog().WarnContext(ctx, "failed to fetch user defaults", "error", err, "user_id", userID)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows) || userDefaults.GlobalLoadBalancer == nil:
		data.GLBDefault = "unset"
	case *userDefaults.GlobalLoadBalancer == 1:
		data.GLBDefault = "on"
	default:
		data.GLBDefault = "off"
	}

	for _, b := range boxes {
		ec := s.getExeletClient(b.Ctrhost)
		if ec == nil || ec.region.Code != user.Region {
			r := "unknown"
			if ec != nil {
				r = ec.region.Code
			}
			data.BoxesOutsideRegion = append(data.BoxesOutsideRegion, struct {
				Name   string
				Region string
			}{b.Name, r})
		}
	}

	if hasCredit {
		data.CreditPlanName = plan.Name
		data.CreditAvailableUSD = credit.AvailableCredit
		data.CreditEffectiveUSD = creditEffective
		data.CreditMaxUSD = plan.MaxCredit
		data.CreditMaxUSDOverride = credit.MaxCredit
		if credit.BillingUpgradeBonusGranted == 1 && credit.AvailableCredit > plan.MaxCredit {
			data.CreditBonusRemainingUSD = credit.AvailableCredit - plan.MaxCredit
		}
		data.CreditRefreshPerHrUSD = plan.RefreshPerHour
		data.CreditRefreshPerHrOverride = credit.RefreshPerHour
		data.CreditTotalUsedUSD = credit.TotalUsed
		data.CreditLastRefreshAt = credit.LastRefreshAt.Format(time.RFC3339)
	}

	s.renderDebugTemplate(ctx, w, "user.html", data)
}

func (s *Server) handleDebugBilling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.URL.Query().Get("userId")
	if userID == "" {
		http.Error(w, "userId parameter is required", http.StatusBadRequest)
		return
	}

	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("user %q not found", userID), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up user: %v", err), http.StatusInternalServerError)
		return
	}

	// Find all accounts created by this user.
	allAccounts, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllAccounts)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to list accounts", "error", err)
	}
	var userAccounts []exedb.Account
	for _, a := range allAccounts {
		if a.CreatedBy == userID {
			userAccounts = append(userAccounts, a)
		}
	}

	type eventRow struct {
		ID        int64
		EventType string
		EventAt   string
		CreatedAt string
	}
	type creditRow struct {
		ID            int64
		AmountStr     string
		IsNegative    bool
		IsPositive    bool
		CreditType    string
		HourBucket    string
		StripeEventID string
		ReceiptURL    string
		GiftID        string
		Note          string
		CreatedAt     string
	}
	type planHistoryRow struct {
		PlanID    string
		StartedAt string
		EndedAt   string
		ChangedBy string
	}
	type accountInfo struct {
		AccountID       string
		AccountStatus   string
		LatestStatus    string
		BillingURL      string
		CreditBalance   string
		CurrentPlanID   string
		CurrentPlanAt   string
		TrialExpiresAt  string
		PlanChangedBy   string
		ParentID        string
		ParentCreatedBy string
		ChildAccounts   []struct {
			AccountID string
			UserID    string
			Email     string
			PlanID    string
		}
		PlanHistory []planHistoryRow
		Events      []eventRow
		Credits     []creditRow
	}

	var accounts []accountInfo
	var purchases []PurchaseRow
	cutoff := time.Now().AddDate(0, 0, -30)
	for _, a := range userAccounts {
		info := accountInfo{
			AccountID:     a.ID,
			AccountStatus: a.Status,
			BillingURL:    billing.MakeCustomerDashboardURL(a.ID),
		}
		if a.ParentID != nil {
			info.ParentID = *a.ParentID
			if parentAcct, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccount, *a.ParentID); err == nil {
				info.ParentCreatedBy = parentAcct.CreatedBy
			}
		}

		// Active plan from account_plans.
		if ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, a.ID); err == nil {
			info.CurrentPlanID = ap.PlanID
			info.CurrentPlanAt = ap.StartedAt.Format(time.RFC3339)
			if ap.TrialExpiresAt != nil {
				info.TrialExpiresAt = ap.TrialExpiresAt.Format(time.RFC3339)
			}
			if ap.ChangedBy != nil {
				info.PlanChangedBy = *ap.ChangedBy
			}
		}

		// Plan history.
		if history, err := withRxRes1(s, ctx, (*exedb.Queries).ListAccountPlanHistory, a.ID); err == nil {
			for _, h := range history {
				row := planHistoryRow{
					PlanID:    h.PlanID,
					StartedAt: h.StartedAt.Format(time.RFC3339),
				}
				if h.EndedAt != nil {
					row.EndedAt = h.EndedAt.Format(time.RFC3339)
				}
				if h.ChangedBy != nil {
					row.ChangedBy = *h.ChangedBy
				}
				info.PlanHistory = append(info.PlanHistory, row)
			}
		}

		// Latest billing status.
		status, err := withRxRes1(s, ctx, (*exedb.Queries).GetLatestBillingStatus, a.ID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			info.LatestStatus = "pending"
		case err != nil:
			info.LatestStatus = "error"
		default:
			info.LatestStatus = status
		}

		// Child accounts (team members whose parent_id points to this account).
		allAccounts, _ := withRxRes0(s, ctx, (*exedb.Queries).ListAllAccounts)
		for _, child := range allAccounts {
			if child.ParentID != nil && *child.ParentID == a.ID {
				childEmail := ""
				if u, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, child.CreatedBy); err == nil {
					childEmail = u.Email
				}
				childPlan := ""
				if ap, err := withRxRes1(s, ctx, (*exedb.Queries).GetActiveAccountPlan, child.ID); err == nil {
					childPlan = ap.PlanID
				}
				info.ChildAccounts = append(info.ChildAccounts, struct {
					AccountID string
					UserID    string
					Email     string
					PlanID    string
				}{
					AccountID: child.ID,
					UserID:    child.CreatedBy,
					Email:     childEmail,
					PlanID:    childPlan,
				})
			}
		}

		// Credit balance via billing manager (same as profile page).
		balance, err := s.billing.SpendCredits(ctx, a.ID, 0, tender.Zero())
		if err != nil {
			info.CreditBalance = fmt.Sprintf("error: %v", err)
		} else {
			info.CreditBalance = balance.String()
		}

		// Billing events.
		events, err := withRxRes1(s, ctx, (*exedb.Queries).ListBillingEventsForAccount, a.ID)
		if err != nil {
			s.slog().WarnContext(ctx, "failed to list billing events", "error", err, "account_id", a.ID)
		}
		for _, e := range events {
			info.Events = append(info.Events, eventRow{
				ID:        e.ID,
				EventType: e.EventType,
				EventAt:   e.EventAt.Format(time.RFC3339),
				CreatedAt: e.CreatedAt.Format(time.RFC3339),
			})
		}

		// Credit ledger entries.
		credits, err := withRxRes1(s, ctx, (*exedb.Queries).ListBillingCreditsForAccount, a.ID)
		if err != nil {
			s.slog().WarnContext(ctx, "failed to list billing credits", "error", err, "account_id", a.ID)
		}

		// Fetch receipt URLs from Stripe for credit purchases.
		receiptURLs, err := s.billing.ReceiptURLs(ctx, a.ID)
		if err != nil {
			s.slog().WarnContext(ctx, "failed to fetch receipt URLs", "error", err, "account_id", a.ID)
		}

		for _, c := range credits {
			v := tender.Mint(0, c.Amount)
			cr := creditRow{
				ID:         c.ID,
				AmountStr:  v.String(),
				IsNegative: v.IsNegative(),
				IsPositive: c.Amount > 0,
				CreatedAt:  c.CreatedAt.Format(time.RFC3339),
			}
			if c.CreditType != nil {
				cr.CreditType = *c.CreditType
			}
			if c.HourBucket != nil {
				cr.HourBucket = c.HourBucket.Format(time.RFC3339)
			}
			if c.StripeEventID != nil {
				cr.StripeEventID = *c.StripeEventID
				if receiptURLs != nil {
					cr.ReceiptURL = receiptURLs[*c.StripeEventID]
				}
			}
			if c.GiftID != nil {
				cr.GiftID = *c.GiftID
			}
			if c.Note != nil {
				cr.Note = *c.Note
			}
			info.Credits = append(info.Credits, cr)

			if c.Amount > 0 && c.StripeEventID != nil && c.CreatedAt.After(cutoff) {
				credits := c.Amount / 1_000_000
				p := PurchaseRow{
					Amount: fmt.Sprintf("%d", credits),
					Date:   c.CreatedAt.Format("02 Jan 2006"),
				}
				if receiptURLs != nil {
					p.ReceiptURL = receiptURLs[*c.StripeEventID]
				}
				purchases = append(purchases, p)
			}
		}

		accounts = append(accounts, info)
	}

	// Shelley free credits (monthly credits) — same logic as profile page.
	var shelleyFreeCreditRemainingPct float64
	var shelleyCreditsAvailable float64
	var shelleyCreditsMax float64
	var hasShelleyFreeCreditPct bool
	creditState, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserLLMCredit, userID)
	var creditPtr *exedb.UserLlmCredit
	if err == nil {
		creditPtr = &creditState
	}
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		plan, planErr := llmgateway.PlanForUser(ctx, s.db, userID, creditPtr)
		if planErr != nil {
			s.slog().WarnContext(ctx, "failed to resolve shelley credit plan", "error", planErr, "user_id", userID)
		} else if plan.MaxCredit > 0 {
			effectiveAvailable := creditState.AvailableCredit
			if creditPtr == nil {
				effectiveAvailable = plan.MaxCredit
			} else if plan.Refresh != nil {
				effectiveAvailable, _ = plan.Refresh(creditState.AvailableCredit, creditState.LastRefreshAt, time.Now())
			}
			shelleyFreeCreditRemainingPct = (effectiveAvailable / plan.MaxCredit) * 100
			if shelleyFreeCreditRemainingPct < 0 {
				shelleyFreeCreditRemainingPct = 0
			}
			if shelleyFreeCreditRemainingPct > 100 {
				shelleyFreeCreditRemainingPct = 100
			}
			shelleyCreditsAvailable = effectiveAvailable
			if shelleyCreditsAvailable < 0 {
				shelleyCreditsAvailable = 0
			}
			shelleyCreditsMax = plan.MaxCredit
			hasShelleyFreeCreditPct = true
		}
	}

	// Purchased credit balance (same as profile page "Extra Credits").
	creditBalance := tender.Zero()
	account, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	if err == nil {
		bal, err := s.billing.SpendCredits(ctx, account.ID, 0, tender.Zero())
		if err == nil {
			creditBalance = bal
		}
	}

	// Compute stacked bar percentages (same as profile page).
	var bonusRemaining float64
	var bonusGrantAmount float64
	if creditPtr != nil && creditPtr.BillingUpgradeBonusGranted == 1 {
		bonusGrantAmount = llmgateway.UpgradeBonusCreditUSD
		if shelleyCreditsAvailable > shelleyCreditsMax {
			bonusRemaining = shelleyCreditsAvailable - shelleyCreditsMax
			if bonusRemaining > bonusGrantAmount {
				bonusRemaining = bonusGrantAmount
			}
		}
	}
	// Load gift credits from ledger.
	var giftCreditsUSD float64
	var giftEntries []billing.GiftEntry
	if account.ID != "" {
		giftEntries, err = s.billing.ListGifts(ctx, account.ID)
		if err != nil {
			s.slog().WarnContext(ctx, "failed to list gift credits", "error", err, "account_id", account.ID)
		}
		giftCreditsUSD = giftCreditsUSDFromLedger(giftEntries)
	}
	// If the signup bonus has been migrated to the billing ledger, zero out
	// the old bonus fields to avoid double-counting (the bonus is now counted
	// via giftCreditsUSD).
	if hasSignupGiftInLedger(giftEntries) {
		bonusGrantAmount = 0
		bonusRemaining = 0
		// TODO: use plan.Quotas.SignupBonusCreditUSD instead of hardcoded 100
		shelleyCreditsAvailable = max(shelleyCreditsAvailable-100, 0)
	}
	// Extra credits = total ledger balance minus gift credits (gifts are tracked separately).
	extraCreditsUSD := float64(creditBalance.Microcents())/1_000_000 - giftCreditsUSD
	if extraCreditsUSD < 0 {
		extraCreditsUSD = 0
	}

	bar := computeCreditBar(creditBarInput{
		shelleyCreditsAvailable: shelleyCreditsAvailable,
		planMaxCredit:           shelleyCreditsMax,
		bonusRemaining:          bonusRemaining,
		bonusGrantAmount:        bonusGrantAmount,
		extraCreditsUSD:         extraCreditsUSD,
		giftCreditsUSD:          giftCreditsUSD,
	})
	totalRemainingPct := bar.totalRemainingPct
	usedCreditsUSD := bar.usedCreditsUSD
	usedBarPct := bar.usedBarPct
	totalCapacity := bar.totalCapacity

	// Build gift rows from ledger entries + bonus.
	var giftRows []GiftRow
	if bonusGrantAmount > 0 {
		giftRows = append(giftRows, GiftRow{
			Amount: fmt.Sprintf("%.0f", bonusGrantAmount),
			Reason: "Welcome bonus for upgrading to a paid plan",
		})
	}
	giftRows = append(giftRows, giftsFromLedger(giftEntries)...)
	if len(giftRows) == 0 {
		giftRows = nil
	}

	// LLM gateway credit info (same as debug user page).
	hasCredit := creditPtr != nil
	var plan llmgateway.Plan
	var creditEffective float64
	if hasCredit {
		plan, _ = llmgateway.PlanForUser(ctx, s.db, userID, creditPtr)
		creditEffective, _ = llmgateway.CalculateRefreshedCredit(
			creditState.AvailableCredit,
			plan.MaxCredit,
			plan.RefreshPerHour,
			creditState.LastRefreshAt,
			time.Now(),
		)
	}

	data := struct {
		Email                         string
		UserID                        string
		Accounts                      []accountInfo
		CreditBalance                 string
		HasShelleyFreeCreditPct       bool
		ShelleyFreeCreditRemainingPct float64
		MonthlyCreditsResetAt         string
		TotalRemainingPct             float64
		BonusRemainingUSD             float64
		GiftCreditsUSD                float64
		MonthlyAvailableUSD           float64
		UsedCreditsUSD                float64
		TotalCapacityUSD              float64
		UsedBarPct                    float64
		ExtraCreditsUSD               float64
		LedgerBalanceUSD              float64
		ShelleyCreditsAvailable       float64
		ShelleyCreditsMax             float64
		TotalCreditsUSD               float64
		Purchases                     []PurchaseRow
		Gifts                         []GiftRow
		HasCredit                     bool
		CreditPlanName                string
		CreditAvailableUSD            float64
		CreditEffectiveUSD            float64
		CreditMaxUSD                  float64
		CreditMaxUSDOverride          *float64
		CreditRefreshPerHrUSD         float64
		CreditRefreshPerHrOverride    *float64
		CreditTotalUsedUSD            float64
		CreditLastRefreshAt           string
		IsOnTeam                      bool
		Entitlements                  []struct {
			Name    string
			ID      string
			Granted bool
		}
		CreditLedger []creditRow
	}{
		Email:                         user.Email,
		UserID:                        user.UserID,
		Accounts:                      accounts,
		CreditBalance:                 creditBalance.String(),
		HasShelleyFreeCreditPct:       hasShelleyFreeCreditPct,
		ShelleyFreeCreditRemainingPct: shelleyFreeCreditRemainingPct,
		MonthlyCreditsResetAt:         nextUTCMonthStart().Format("15:04 on 02 Jan"),
		TotalRemainingPct:             totalRemainingPct,
		BonusRemainingUSD:             bar.bonusRemaining,
		GiftCreditsUSD:                bar.giftCreditsUSD,
		MonthlyAvailableUSD:           bar.monthlyAvailable,
		UsedCreditsUSD:                usedCreditsUSD,
		TotalCapacityUSD:              totalCapacity,
		UsedBarPct:                    usedBarPct,
		ExtraCreditsUSD:               extraCreditsUSD,
		LedgerBalanceUSD:              max(float64(creditBalance.Microcents())/1_000_000, 0),
		ShelleyCreditsAvailable:       shelleyCreditsAvailable,
		ShelleyCreditsMax:             shelleyCreditsMax,
		TotalCreditsUSD:               max(shelleyCreditsAvailable+extraCreditsUSD+giftCreditsUSD, 0),
		Purchases:                     purchases,
		Gifts:                         giftRows,
		HasCredit:                     hasCredit,
	}

	if hasCredit {
		data.CreditPlanName = plan.Name
		data.CreditAvailableUSD = creditState.AvailableCredit
		data.CreditEffectiveUSD = creditEffective
		data.CreditMaxUSD = plan.MaxCredit
		data.CreditMaxUSDOverride = creditState.MaxCredit
		data.CreditRefreshPerHrUSD = plan.RefreshPerHour
		data.CreditRefreshPerHrOverride = creditState.RefreshPerHour
		data.CreditTotalUsedUSD = creditState.TotalUsed
		data.CreditLastRefreshAt = creditState.LastRefreshAt.Format(time.RFC3339)
	}

	// Copy credit ledger from first account to top-level for template access.
	if len(accounts) > 0 {
		data.CreditLedger = accounts[0].Credits
	}

	// Check if user is on a team — if so, entitlements are misleading since
	// plan resolves as Individual but they're effectively on the Team plan.
	if team, _ := s.GetTeamForUser(ctx, userID); team != nil {
		data.IsOnTeam = true
	}

	// Resolve entitlements using the same logic as UserHasEntitlement:
	// try account_plans first (walks parent_id for team members), fall back to legacy.
	var version entitlement.PlanVersion
	if planRow, err := withRxRes1(s, ctx, (*exedb.Queries).GetActivePlanForUser, userID); err == nil {
		version = entitlement.PlanVersion(planRow.PlanID)
	} else if billingRow, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBilling, userID); err == nil {
		inputs := entitlement.UserPlanInputs{
			Category:           billingRow.Category,
			BillingStatus:      billingRow.BillingStatus,
			BillingExemption:   billingRow.BillingExemption,
			CreatedAt:          billingRow.CreatedAt,
			BillingTrialEndsAt: billingRow.BillingTrialEndsAt,
		}
		version = entitlement.GetPlanVersion(inputs)
	}
	if version != "" {
		for _, ent := range entitlement.AllEntitlements() {
			data.Entitlements = append(data.Entitlements, struct {
				Name    string
				ID      string
				Granted bool
			}{
				Name:    ent.DisplayName,
				ID:      ent.ID,
				Granted: entitlement.PlanGrants(version, ent),
			})
		}
	}

	tmpl, err := debug_templates.Parse(s.env)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "billing.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute billing template", "error", err)
	}
}

func (s *Server) handleDebugUserGiveInvites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	count, err := strconv.Atoi(r.FormValue("count"))
	if err != nil || count < 1 || count > 10 {
		http.Error(w, "count must be between 1 and 10", http.StatusBadRequest)
		return
	}

	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("user %q not found", userID), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up user: %v", err), http.StatusInternalServerError)
		return
	}

	assignedBy := r.Header.Get("X-Webauth-User")
	if assignedBy == "" {
		assignedBy = r.Header.Get("Tailscale-User-Login")
	}
	if assignedBy == "" {
		assignedBy = "debug-ui"
	}

	if err := s.giveInvitesToUser(ctx, &user, count, "trial", assignedBy); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	params := url.Values{}
	params.Set("userId", userID)
	params.Set("invite_posted", "1")
	http.Redirect(w, r, "/debug/user?"+params.Encode(), http.StatusSeeOther)
}

// restartSourceVM restarts a VM on its source exelet after a failed migration.
// It uses exponential backoff retry in case the exelet is temporarily unavailable.
// If live is true, it first stops the (paused) VM before restarting.
func (s *Server) restartSourceVM(ctx context.Context, source *exeletClient, containerID, boxName, sourceAddr, reason string, wasRunning, live bool, writeProgress func(string, ...any)) {
	if !wasRunning {
		writeProgress("Source VM was already stopped, nothing to restart.")
		return
	}

	// Check if VM is still running (failed before pause/stop)
	inst, err := source.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
	if err == nil && inst.Instance.State == computeapi.VMState_RUNNING {
		writeProgress("VM is still running on source (failed before stop).")
		return
	}

	// For live migration, the VM may be paused — stop it first
	if live {
		writeProgress("Stopping paused VM on source for cold reboot...")
		s.slog().ErrorContext(ctx, "live migration failed, cold rebooting VM on source",
			"box", boxName, "container_id", containerID, "source", sourceAddr, "reason", reason)
		delay := 100 * time.Millisecond
		deadline := time.Now().Add(10 * time.Second)
		for attempt := 1; ; attempt++ {
			if _, err := source.client.StopInstance(ctx, &computeapi.StopInstanceRequest{ID: containerID}); err == nil {
				break
			} else if time.Now().After(deadline) {
				writeProgress("ERROR: failed to stop paused VM on source after %d attempts: %v", attempt, err)
				return
			} else {
				writeProgress("Stop attempt %d failed (%v), retrying...", attempt, err)
			}
			time.Sleep(delay)
			delay *= 2
		}
	}

	writeProgress("Restarting VM on source exelet to restore service...")
	s.slog().ErrorContext(ctx, "migration failed, restarting VM on source",
		"box", boxName, "container_id", containerID, "source", sourceAddr, "reason", reason)
	delay := 100 * time.Millisecond
	deadline := time.Now().Add(10 * time.Second)
	for attempt := 1; ; attempt++ {
		if _, err := source.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: containerID}); err == nil {
			writeProgress("VM restarted on source.")
			return
		} else if time.Now().After(deadline) {
			writeProgress("ERROR: failed to restart VM on source after %d attempts: %v", attempt, err)
			s.slog().ErrorContext(ctx, "failed to restart VM on source after migration failure",
				"box", boxName, "container_id", containerID, "source", sourceAddr, "attempts", attempt, "error", err)
			return
		} else {
			writeProgress("Restart attempt %d failed (%v), retrying...", attempt, err)
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// handleDebugUserMigrateRegion sets the user's region and enables GLB.
func (s *Server) handleDebugUserMigrateRegion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	regionCode := r.FormValue("region")

	if userID == "" || regionCode == "" {
		http.Error(w, "user_id and region are required", http.StatusBadRequest)
		return
	}

	reg, err := region.ByCode(regionCode)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid region: %v", err), http.StatusBadRequest)
		return
	}

	_, err = withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("user %q not found", userID), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up user: %v", err), http.StatusInternalServerError)
		return
	}

	// Set user region and enable GLB default in a single transaction.
	glbOn := int64(1)
	if err := withTx0(s, ctx, func(q *exedb.Queries, ctx context.Context) error {
		if err := q.SetUserRegion(ctx, exedb.SetUserRegionParams{
			Region: reg.Code,
			UserID: userID,
		}); err != nil {
			return fmt.Errorf("set region: %w", err)
		}
		if err := q.UpsertUserDefaultGlobalLoadBalancer(ctx, exedb.UpsertUserDefaultGlobalLoadBalancerParams{
			UserID:             userID,
			GlobalLoadBalancer: &glbOn,
		}); err != nil {
			return fmt.Errorf("enable GLB: %w", err)
		}
		return nil
	}); err != nil {
		http.Error(w, fmt.Sprintf("failed to migrate region: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "migrated user region", "user_id", userID, "region", reg.Code)

	params := url.Values{}
	params.Set("userId", userID)
	http.Redirect(w, r, "/debug/user?"+params.Encode(), http.StatusSeeOther)
}

// handleDebugUserMigrateVMs live-migrates all of a user's boxes into their configured region.
func (s *Server) handleDebugUserMigrateVMs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	confirm := r.FormValue("confirm")
	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	// Set up streaming response.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	writeProgress := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
		flusher.Flush()
	}
	writeError := func(format string, args ...any) {
		writeProgress("ERROR: "+format, args...)
	}

	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		writeError("failed to look up user: %v", err)
		writeProgress("MIGRATION_ERROR")
		return
	}

	writeProgress("User %s, target region: %s", user.Email, user.Region)

	// Find a target exelet in the user's region.
	var targetAddr string
	var targetClient *exeletClient
	for addr, ec := range s.exeletClients {
		if ec.region.Code == user.Region && ec.up.Load() {
			if targetClient == nil || ec.count.Load() < targetClient.count.Load() {
				targetAddr = addr
				targetClient = ec
			}
		}
	}
	if targetClient == nil {
		writeError("no available exelet in region %s", user.Region)
		writeProgress("MIGRATION_ERROR")
		return
	}

	writeProgress("Target exelet: %s", targetAddr)

	// Fetch boxes and filter to those outside the region.
	boxes, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		writeError("failed to list VMs: %v", err)
		writeProgress("MIGRATION_ERROR")
		return
	}

	var toMigrate []exedb.Box
	for _, b := range boxes {
		ec := s.getExeletClient(b.Ctrhost)
		if ec == nil || ec.region.Code != user.Region {
			toMigrate = append(toMigrate, b)
		}
	}

	if len(toMigrate) == 0 {
		writeProgress("All VMs are already in region %s.", user.Region)
		return
	}

	// Verify confirmation matches box count.
	expectedConfirm := strconv.Itoa(len(toMigrate))
	if confirm != expectedConfirm {
		writeError("confirm must be %q (the number of VMs to migrate)", expectedConfirm)
		writeProgress("MIGRATION_ERROR")
		return
	}

	writeProgress("Migrating %d box(es) to %s", len(toMigrate), targetAddr)
	writeProgress("")

	// Use a background context so migrations complete even if the browser disconnects.
	ctx = context.WithoutCancel(ctx)

	// Lock deployments during migration (best-effort).
	prodLocked, err := prodlockSet(ctx, s.env, "lock", fmt.Sprintf("region migration: %d VMs for %s to %s", len(toMigrate), user.Email, targetAddr))
	if err != nil {
		writeProgress("WARNING: failed to lock deployments: %v", err)
	} else if prodLocked {
		writeProgress("Deployments locked.")
	} else {
		writeProgress("Already locked — will not auto-unlock after migration.")
	}

	var succeeded, failed int
	for i, box := range toMigrate {
		boxName := box.Name
		writeProgress("=== [%d/%d] %s ===", i+1, len(toMigrate), boxName)

		if box.ContainerID == nil {
			writeError("box %q has no container_id", boxName)
			failed++
			writeProgress("")
			continue
		}
		containerID := *box.ContainerID

		sourceClient := s.getExeletClient(box.Ctrhost)
		if sourceClient == nil {
			writeError("source exelet %q not available for box %q", box.Ctrhost, boxName)
			failed++
			writeProgress("")
			continue
		}

		sourceInstance, err := sourceClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
		if err != nil {
			writeError("failed to get instance state for %q: %v", boxName, err)
			failed++
			writeProgress("")
			continue
		}
		wasRunning := sourceInstance.Instance.State == computeapi.VMState_RUNNING
		live := wasRunning

		restartSource := func(reason string) {
			s.restartSourceVM(ctx, sourceClient, containerID, boxName, box.Ctrhost, reason, wasRunning, live, writeProgress)
		}

		var sshPort *int64
		var coldBooted bool
		dbStatus := "running"

		if live {
			writeProgress("Live migrating from %s to %s...", box.Ctrhost, targetAddr)
			s.slog().InfoContext(ctx, "starting live migration", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
			liveSshPort, cb, err := s.migrateVMLive(ctx, migrateVMLiveParams{
				source:     sourceClient.client,
				target:     targetClient.client,
				instanceID: containerID,
				box:        box,
				progress:   writeProgress,
			})
			if err != nil {
				writeError("live migration failed: %v", err)
				restartSource(err.Error())
				failed++
				writeProgress("")
				continue
			}
			coldBooted = cb
			sshPort = &liveSshPort
			writeProgress("Live migration complete.")
			if coldBooted {
				writeProgress("WARNING: fell back to cold boot.")

				// Update /etc/hosts — cold boot means the VM restarted with the
				// correct IP on the interface but /etc/hosts still has the old IP.
				if sourceInstance.Instance.VMConfig != nil && sourceInstance.Instance.VMConfig.NetworkInterface != nil {
					targetInstance, err := targetClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
					if err == nil && targetInstance.Instance.VMConfig != nil && targetInstance.Instance.VMConfig.NetworkInterface != nil {
						sourceNet := sourceInstance.Instance.VMConfig.NetworkInterface
						targetNet := targetInstance.Instance.VMConfig.NetworkInterface
						if sourceNet.IP != nil && targetNet.IP != nil {
							targetBox := box
							targetBox.Ctrhost = targetAddr
							targetBox.SSHPort = sshPort
							s.updateVMHostsFile(ctx, &targetBox, sourceNet.IP.IPV4, targetNet.IP.IPV4, writeProgress)
						}
					}
				}
			}
		} else {
			// VM was already stopped (live == wasRunning, so !live implies !wasRunning); leave stopped on target.
			writeProgress("Transferring disk from %s to %s...", box.Ctrhost, targetAddr)
			s.slog().InfoContext(ctx, "starting disk transfer", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
			if err := s.migrateVM(ctx, sourceClient.client, targetClient.client, containerID, true, writeProgress); err != nil {
				writeError("disk transfer failed: %v", err)
				restartSource(err.Error())
				failed++
				writeProgress("")
				continue
			}
			dbStatus = "stopped"
		}

		writeProgress("Updating database...")
		if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxMigration, exedb.UpdateBoxMigrationParams{
			Ctrhost: targetAddr,
			SSHPort: sshPort,
			Status:  dbStatus,
			Region:  targetClient.region.Code,
			ID:      box.ID,
		}); err != nil {
			writeError("failed to update database: %v", err)
			s.slog().ErrorContext(ctx, "failed to update database after migration", "box", boxName, "error", err)
			restartSource(err.Error())
			failed++
			writeProgress("")
			continue
		}

		proxyChangeMovedBox(boxName)
		writeProgress("Proxy caches flushed.")

		if !live || coldBooted {
			go s.sendBoxMaintenanceEmail(context.Background(), boxName)
		}

		writeProgress("Deleting source instance on %s...", box.Ctrhost)
		if _, err := sourceClient.client.DeleteInstance(ctx, &computeapi.DeleteInstanceRequest{ID: containerID}); err != nil {
			writeProgress("WARNING: failed to delete source instance: %v", err)
			s.slog().WarnContext(ctx, "failed to delete source instance after migration",
				"box", boxName, "container_id", containerID, "source", box.Ctrhost, "error", err)
		} else {
			writeProgress("Source instance deleted.")
		}

		writeProgress("Box %s migrated successfully.", boxName)
		s.slog().InfoContext(ctx, "box migrated", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
		succeeded++
		writeProgress("")
	}

	// Unlock if we locked it.
	if prodLocked {
		writeProgress("Unlocking deployments...")
		if _, err := prodlockSet(ctx, s.env, "unlock", "region migration complete"); err != nil {
			writeProgress("WARNING: failed to unlock deployments: %v — manual unlock required", err)
		} else {
			writeProgress("Deployments unlocked.")
		}
	}

	writeProgress("=== Migration complete ===")
	writeProgress("Succeeded: %d, Failed: %d, Total: %d", succeeded, failed, len(toMigrate))

	if failed == 0 {
		writeProgress("MIGRATION_SUCCESS")
	} else {
		writeProgress("MIGRATION_ERROR")
	}
}

// handleDebugUserColdMigrateVM cold-migrates a single box into the user's
// configured region. This is the follow-up step after a bulk live migration
// leaves some VMs behind (e.g. because they aren't live-migratable).
func (s *Server) handleDebugUserColdMigrateVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	boxName := r.FormValue("box_name")
	if userID == "" || boxName == "" {
		http.Error(w, "user_id and box_name are required", http.StatusBadRequest)
		return
	}

	// Set up streaming response.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	writeProgress := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\n", args...)
		flusher.Flush()
	}
	writeError := func(format string, args ...any) {
		writeProgress("ERROR: "+format, args...)
	}

	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		writeError("failed to look up user: %v", err)
		writeProgress("MIGRATION_ERROR")
		return
	}

	writeProgress("User %s, target region: %s", user.Email, user.Region)

	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{Name: boxName, CreatedByUserID: userID})
	if err != nil {
		writeError("box %q not found or does not belong to user %s: %v", boxName, userID, err)
		writeProgress("MIGRATION_ERROR")
		return
	}

	if box.ContainerID == nil {
		writeError("box %q has no container_id", boxName)
		writeProgress("MIGRATION_ERROR")
		return
	}
	containerID := *box.ContainerID

	// Find a target exelet in the user's region.
	var targetAddr string
	var targetClient *exeletClient
	for addr, ec := range s.exeletClients {
		if ec.region.Code == user.Region && ec.up.Load() {
			if targetClient == nil || ec.count.Load() < targetClient.count.Load() {
				targetAddr = addr
				targetClient = ec
			}
		}
	}
	if targetClient == nil {
		writeError("no available exelet in region %s", user.Region)
		writeProgress("MIGRATION_ERROR")
		return
	}

	sourceClient := s.getExeletClient(box.Ctrhost)
	if sourceClient == nil {
		writeError("source exelet %q not available", box.Ctrhost)
		writeProgress("MIGRATION_ERROR")
		return
	}

	if sourceClient.region.Code == user.Region {
		writeProgress("Box %s is already in region %s.", boxName, user.Region)
		return
	}

	// Use a background context so migration completes even if the browser disconnects.
	ctx = context.WithoutCancel(ctx)

	writeProgress("Cold migrating %s from %s to %s...", boxName, box.Ctrhost, targetAddr)

	// Check source VM state.
	sourceInstance, err := sourceClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
	if err != nil {
		writeError("failed to get instance state for %q: %v", boxName, err)
		writeProgress("MIGRATION_ERROR")
		return
	}
	wasRunning := sourceInstance.Instance.State == computeapi.VMState_RUNNING

	s.slog().InfoContext(ctx, "starting cold migration", "box", boxName, "source", box.Ctrhost, "target", targetAddr, "wasRunning", wasRunning)

	// Stop the VM if it's running.
	if wasRunning {
		writeProgress("Stopping VM on source exelet...")
		if _, err := sourceClient.client.StopInstance(ctx, &computeapi.StopInstanceRequest{ID: containerID}); err != nil {
			writeError("failed to stop VM: %v", err)
			writeProgress("MIGRATION_ERROR")
			return
		}
		writeProgress("VM stopped.")
	}

	restartSource := func(reason string) {
		s.restartSourceVM(ctx, sourceClient, containerID, boxName, box.Ctrhost, reason, wasRunning, false, writeProgress)
	}

	// Transfer disk (two-phase=false since VM is stopped).
	writeProgress("Transferring disk from %s to %s...", box.Ctrhost, targetAddr)
	if err := s.migrateVM(ctx, sourceClient.client, targetClient.client, containerID, false, writeProgress); err != nil {
		writeError("disk transfer failed: %v", err)
		restartSource(err.Error())
		writeProgress("MIGRATION_ERROR")
		return
	}
	writeProgress("Disk transfer complete.")

	// Start VM on target if it was running.
	var sshPort *int64
	dbStatus := "stopped"
	if wasRunning {
		writeProgress("Starting VM on target exelet...")
		if _, err := targetClient.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: containerID}); err != nil {
			writeError("failed to start VM on target: %v", err)
			restartSource(err.Error())
			writeProgress("MIGRATION_ERROR")
			return
		}

		instance, err := targetClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
		if err != nil {
			writeError("failed to get instance info from target: %v", err)
			restartSource(err.Error())
			writeProgress("MIGRATION_ERROR")
			return
		}
		newSSHPort := int64(instance.Instance.SSHPort)
		sshPort = &newSSHPort
		dbStatus = "running"
		writeProgress("VM started on target (SSH port: %d).", newSSHPort)

		// Update /etc/hosts to reflect the new IP.
		if sourceInstance.Instance.VMConfig != nil && sourceInstance.Instance.VMConfig.NetworkInterface != nil &&
			instance.Instance.VMConfig != nil && instance.Instance.VMConfig.NetworkInterface != nil {
			sourceNet := sourceInstance.Instance.VMConfig.NetworkInterface
			targetNet := instance.Instance.VMConfig.NetworkInterface
			if sourceNet.IP != nil && targetNet.IP != nil {
				targetBox := box
				targetBox.Ctrhost = targetAddr
				targetBox.SSHPort = sshPort
				s.updateVMHostsFile(ctx, &targetBox, sourceNet.IP.IPV4, targetNet.IP.IPV4, writeProgress)
			}
		}
	}

	// Update database.
	writeProgress("Updating database...")
	if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxMigration, exedb.UpdateBoxMigrationParams{
		Ctrhost: targetAddr,
		SSHPort: sshPort,
		Status:  dbStatus,
		Region:  targetClient.region.Code,
		ID:      box.ID,
	}); err != nil {
		writeError("failed to update database: %v", err)
		s.slog().ErrorContext(ctx, "failed to update database after cold migration", "box", boxName, "error", err)
		restartSource(err.Error())
		writeProgress("MIGRATION_ERROR")
		return
	}

	proxyChangeMovedBox(boxName)
	writeProgress("Proxy caches flushed.")

	if wasRunning {
		go s.sendBoxMaintenanceEmail(context.Background(), boxName)
	}

	// Clean up source.
	writeProgress("Deleting source instance on %s...", box.Ctrhost)
	if _, err := sourceClient.client.DeleteInstance(ctx, &computeapi.DeleteInstanceRequest{ID: containerID}); err != nil {
		writeProgress("WARNING: failed to delete source instance: %v", err)
		s.slog().WarnContext(ctx, "failed to delete source instance after cold migration",
			"box", boxName, "container_id", containerID, "source", box.Ctrhost, "error", err)
	} else {
		writeProgress("Source instance deleted.")
	}

	writeProgress("Box %s cold-migrated successfully.", boxName)
	s.slog().InfoContext(ctx, "box cold-migrated", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
	writeProgress("MIGRATION_SUCCESS")
}

// handleDebugBounces displays the email bounces list.
func (s *Server) handleDebugBounces(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	bounces, err := withRxRes0(s, ctx, (*exedb.Queries).ListEmailBounces)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get bounces: %v", err), http.StatusInternalServerError)
		return
	}

	// Get total count
	totalCount, err := withRxRes0(s, ctx, (*exedb.Queries).CountEmailBounces)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get bounce count: %v", err), http.StatusInternalServerError)
		return
	}

	// Get last poll time
	var lastPollTime string
	lastPoll, err := withRxRes0(s, ctx, (*exedb.Queries).GetLastBouncesPoll)
	if err == nil {
		lastPollTime = lastPoll
	}

	// JSON response
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		type bounceJSON struct {
			Email     string `json:"email"`
			Reason    string `json:"reason"`
			BouncedAt string `json:"bounced_at"`
		}
		var result []bounceJSON
		for _, b := range bounces {
			result = append(result, bounceJSON{
				Email:     b.Email,
				Reason:    b.Reason,
				BouncedAt: formatTime(b.BouncedAt),
			})
		}
		json.NewEncoder(w).Encode(result)
		return
	}

	// Build template data
	type bounceInfo struct {
		Email     string
		Reason    string
		BouncedAt string
	}
	var bounceList []bounceInfo
	for _, b := range bounces {
		bounceList = append(bounceList, bounceInfo{
			Email:     b.Email,
			Reason:    b.Reason,
			BouncedAt: formatTime(b.BouncedAt),
		})
	}

	data := struct {
		Bounces      []bounceInfo
		TotalCount   int64
		LastPollTime string
	}{
		Bounces:      bounceList,
		TotalCount:   totalCount,
		LastPollTime: lastPollTime,
	}

	s.renderDebugTemplate(ctx, w, "bounces.html", data)
}

// handleDebugBouncesPost handles POST actions on the bounces page.
func (s *Server) handleDebugBouncesPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	action := r.FormValue("action")
	email := r.FormValue("email")

	switch action {
	case "delete":
		if email == "" {
			http.Error(w, "email required", http.StatusBadRequest)
			return
		}
		err := withTx1(s, ctx, (*exedb.Queries).DeleteEmailBounce, email)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to delete bounce: %v", err), http.StatusInternalServerError)
			return
		}
		s.slog().InfoContext(ctx, "deleted email bounce via debug", "email", email)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/debug/bounces", http.StatusSeeOther)
}

// handleDebugTeamCreate creates a team and adds a user as owner.
// POST /debug/teams/create with team_id, display_name, owner_user_id (or owner_email)
func (s *Server) handleDebugTeamCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawTeamID := r.FormValue("team_id")
	displayName := r.FormValue("display_name")
	ownerUserID := r.FormValue("owner_user_id")

	// Resolve owner_email to user_id if provided instead
	if ownerUserID == "" {
		if ownerEmail := r.FormValue("owner_email"); ownerEmail != "" {
			ce := canonicalizeEmail(ownerEmail)
			uid, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserIDByEmail, &ce)
			if err != nil {
				http.Error(w, fmt.Sprintf("user not found for email %q: %v", ownerEmail, err), http.StatusBadRequest)
				return
			}
			ownerUserID = uid
		}
	}

	if rawTeamID == "" || displayName == "" || ownerUserID == "" {
		http.Error(w, "team_id, display_name, and owner_user_id (or owner_email) are required", http.StatusBadRequest)
		return
	}

	teamID, err := parseTeamID(rawTeamID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Create the team
	err = withTx1(s, ctx, (*exedb.Queries).InsertTeam, exedb.InsertTeamParams{
		TeamID:      teamID,
		DisplayName: displayName,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create team: %v", err), http.StatusInternalServerError)
		return
	}

	// Add the billing owner
	err = withTx1(s, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: ownerUserID,
		Role:   "billing_owner",
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to add billing owner: %v", err), http.StatusInternalServerError)
		return
	}

	// Set auth_provider if specified
	if authProvider := r.FormValue("auth_provider"); authProvider != "" {
		err = withTx1(s, ctx, (*exedb.Queries).SetTeamAuthProvider, exedb.SetTeamAuthProviderParams{
			AuthProvider: &authProvider,
			TeamID:       teamID,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to set auth provider: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// If the team requires an auth provider, set it on the owner.
	if team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeam, teamID); err == nil && team.AuthProvider != nil {
		_ = withTx1(s, ctx, (*exedb.Queries).SetUserAuthProvider, exedb.SetUserAuthProviderParams{
			AuthProvider: team.AuthProvider,
			UserID:       ownerUserID,
		})
	}

	s.slog().InfoContext(ctx, "created team via debug", "team_id", teamID, "billing_owner", ownerUserID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "created team %s with billing_owner %s", teamID, ownerUserID)
}

// handleDebugTeamAddMember adds a user to an existing team.
// POST /debug/teams/add-member with team_id, user_id (or email), role (billing_owner, admin, or user)
// If email is provided and user doesn't exist, creates a pending invite and sends email.
func (s *Server) handleDebugTeamAddMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.FormValue("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := r.FormValue("user_id")
	addr := r.FormValue("email")
	role := r.FormValue("role")

	// Resolve email to user_id if provided instead
	confirmExisting := r.FormValue("confirm_existing") == "true"
	if userID == "" && addr != "" {
		ce := canonicalizeEmail(addr)
		uid, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserIDByEmail, &ce)
		if err != nil {
			// User doesn't exist — create pending invite
			team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeam, teamID)
			if err != nil {
				http.Error(w, fmt.Sprintf("team not found: %v", err), http.StatusBadRequest)
				return
			}
			// Use first team admin as the inviter
			members, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamMembers, teamID)
			if err != nil || len(members) == 0 {
				http.Error(w, "could not find team admin for invite", http.StatusInternalServerError)
				return
			}
			var inviterID string
			for _, m := range members {
				if m.Role == "billing_owner" || m.Role == "admin" {
					inviterID = m.UserID
					break
				}
			}
			if inviterID == "" {
				inviterID = members[0].UserID
			}
			if err := s.createPendingTeamInvite(ctx, teamID, team.DisplayName, addr, inviterID, false); err != nil {
				http.Error(w, fmt.Sprintf("failed to create pending invite: %v", err), http.StatusInternalServerError)
				return
			}
			s.slog().InfoContext(ctx, "created pending team invite via debug", "team_id", teamID, "email", addr)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "invited %s to team %s (pending signup)", addr, teamID)
			return
		}
		// User exists — require confirmation since their VMs will be folded into the team.
		if !confirmExisting {
			boxCount, _ := withRxRes1(s, ctx, (*exedb.Queries).CountBoxesForUser, uid)
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, "EXISTING_USER: %s already has an account with %d VM(s). Adding them will fold their VMs into the team.", addr, boxCount)
			return
		}
		userID = uid
	}

	if teamID == "" || userID == "" {
		http.Error(w, "team_id and user_id (or email) are required", http.StatusBadRequest)
		return
	}
	if role == "" {
		role = "user"
	}
	if role != "billing_owner" && role != "admin" && role != "user" {
		http.Error(w, "role must be 'billing_owner', 'admin', or 'user'", http.StatusBadRequest)
		return
	}

	err = withTx1(s, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: userID,
		Role:   role,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to add team member: %v", err), http.StatusInternalServerError)
		return
	}

	// If the team requires an auth provider, set it on the user.
	if team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeam, teamID); err == nil && team.AuthProvider != nil {
		_ = withTx1(s, ctx, (*exedb.Queries).SetUserAuthProvider, exedb.SetUserAuthProviderParams{
			AuthProvider: team.AuthProvider,
			UserID:       userID,
		})
	}

	s.slog().InfoContext(ctx, "added team member via debug", "team_id", teamID, "user_id", userID, "role", role)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "added %s to team %s as %s", userID, teamID, role)
}

// handleDebugTeamMembers lists members of a team.
// GET /debug/teams/members?team_id=xxx
func (s *Server) handleDebugTeamMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.URL.Query().Get("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	members, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamMembers, teamID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get team members: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(members); err != nil {
		s.slog().InfoContext(ctx, "Failed to encode team members", "error", err)
	}
}

// handleDebugTeams displays a list of all teams with members.
func (s *Server) handleDebugTeams(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teams, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllTeams)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list teams: %v", err), http.StatusInternalServerError)
		return
	}

	if r.URL.Query().Get("format") == "json" {
		type memberInfo struct {
			UserID       string `json:"user_id"`
			Email        string `json:"email"`
			Role         string `json:"role"`
			JoinedAt     string `json:"joined_at"`
			AuthProvider string `json:"auth_provider,omitempty"`
			VMCount      int64  `json:"vm_count"`
		}
		type ssoInfo struct {
			ProviderID  int64  `json:"provider_id"`
			IssuerURL   string `json:"issuer_url"`
			ClientID    string `json:"client_id"`
			DisplayName string `json:"display_name,omitempty"`
		}
		type teamInfo struct {
			TeamID             string       `json:"team_id"`
			DisplayName        string       `json:"display_name"`
			CreatedAt          string       `json:"created_at"`
			MemberCount        int64        `json:"member_count"`
			VMCount            int64        `json:"vm_count"`
			MaxBoxes           int          `json:"max_boxes"`
			Limits             string       `json:"limits,omitempty"`
			AuthProvider       string       `json:"auth_provider,omitempty"`
			Members            []memberInfo `json:"members"`
			SSO                *ssoInfo     `json:"sso,omitempty"`
			BillingAccountID   string       `json:"billing_account_id,omitempty"`
			BillingOwnerUserID string       `json:"billing_owner_user_id,omitempty"`
		}
		var teamsJSON []teamInfo
		for _, t := range teams {
			ti := teamInfo{
				TeamID:      t.TeamID,
				DisplayName: t.DisplayName,
				CreatedAt:   t.CreatedAt,
				MemberCount: t.MemberCount,
				Members:     []memberInfo{},
			}
			// Fetch limits and auth_provider from full team record
			if team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeam, t.TeamID); err == nil {
				ti.Limits = ptrStr(team.Limits)
				ti.AuthProvider = ptrStr(team.AuthProvider)
				ti.MaxBoxes = GetMaxTeamBoxes(ParseUserLimitsFromJSON(ptrStr(team.Limits)))
			} else {
				ti.MaxBoxes = stage.DefaultMaxTeamBoxes
			}
			// Fetch SSO provider if configured
			if ssoProvider, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamSSOProvider, t.TeamID); err == nil {
				si := ssoInfo{
					ProviderID: ssoProvider.ID,
					IssuerURL:  ssoProvider.IssuerUrl,
					ClientID:   ssoProvider.ClientID,
				}
				if ssoProvider.DisplayName != nil {
					si.DisplayName = *ssoProvider.DisplayName
				}
				ti.SSO = &si
			}
			// Fetch members
			if members, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamMembers, t.TeamID); err == nil {
				for _, m := range members {
					mi := memberInfo{
						UserID:       m.UserID,
						Email:        m.Email,
						Role:         m.Role,
						JoinedAt:     m.JoinedAt,
						AuthProvider: ptrStr(m.AuthProvider),
					}
					if vmCount, err := withRxRes1(s, ctx, (*exedb.Queries).CountBoxesForUser, m.UserID); err == nil {
						mi.VMCount = vmCount
						ti.VMCount += vmCount
					}
					ti.Members = append(ti.Members, mi)
					// Look up the billing owner's account
					if m.Role == "billing_owner" {
						ti.BillingOwnerUserID = m.UserID
						if acct, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccountByUserID, m.UserID); err == nil {
							ti.BillingAccountID = acct.ID
						}
					}
				}
			}
			teamsJSON = append(teamsJSON, ti)
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(teamsJSON); err != nil {
			s.slog().InfoContext(ctx, "Failed to encode teams", "error", err)
		}
		return
	}

	// HTML output
	data := struct {
		TeamCount int
	}{
		TeamCount: len(teams),
	}

	s.renderDebugTemplate(ctx, w, "teams.html", data)
}

// handleDebugTeamRemoveMember removes a member from a team.
// Refuses if the member still has VMs.
// POST /debug/teams/remove-member with team_id, user_id
func (s *Server) handleDebugTeamRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.FormValue("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := r.FormValue("user_id")

	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	confirmUserID := r.FormValue("confirm_user_id")
	if confirmUserID != userID {
		http.Error(w, "confirm_user_id must match user_id", http.StatusBadRequest)
		return
	}

	boxIDs, err := withRxRes1(s, ctx, (*exedb.Queries).ListBoxIDsForUser, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to check member's VMs: %v", err), http.StatusInternalServerError)
		return
	}
	if len(boxIDs) > 0 {
		http.Error(w, fmt.Sprintf("cannot remove %s: they still have %d VM(s)", userID, len(boxIDs)), http.StatusConflict)
		return
	}

	if err := s.deleteTeamMember(ctx, teamID, userID); err != nil {
		http.Error(w, fmt.Sprintf("failed to remove member: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "removed team member via debug", "team_id", teamID, "user_id", userID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "removed %s from team %s", userID, teamID)
}

// handleDebugTeamMemberVMCount returns the number of VMs a user has.
// GET /debug/teams/member-vm-count?user_id=...
func (s *Server) handleDebugTeamMemberVMCount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	count, err := withRxRes1(s, ctx, (*exedb.Queries).CountBoxesForUser, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to count VMs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"count":%d}`, count)
}

// handleDebugTeamUpdateRole changes a team member's role.
// POST /debug/teams/update-role with team_id, user_id, role
func (s *Server) handleDebugTeamUpdateRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.FormValue("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userID := r.FormValue("user_id")
	role := r.FormValue("role")

	if userID == "" || role == "" {
		http.Error(w, "user_id and role are required", http.StatusBadRequest)
		return
	}
	if role != "billing_owner" && role != "admin" && role != "user" {
		http.Error(w, "role must be 'billing_owner', 'admin', or 'user'", http.StatusBadRequest)
		return
	}

	err = withTx1(s, ctx, (*exedb.Queries).UpdateTeamMemberRole, exedb.UpdateTeamMemberRoleParams{
		TeamID: teamID,
		UserID: userID,
		Role:   role,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update role: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "updated team member role via debug", "team_id", teamID, "user_id", userID, "role", role)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "updated %s role to %s in team %s", userID, role, teamID)
}

// handleDebugTeamSetLimits updates a team's resource limits.
// POST /debug/teams/set-limits with team_id, limits (JSON string, empty to clear)
func (s *Server) handleDebugTeamSetLimits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.FormValue("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limits := r.FormValue("limits")

	var limitsPtr *string
	if limits != "" {
		limitsPtr = &limits
	}

	err = withTx1(s, ctx, (*exedb.Queries).UpdateTeamLimits, exedb.UpdateTeamLimitsParams{
		TeamID: teamID,
		Limits: limitsPtr,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to update limits: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "updated team limits via debug", "team_id", teamID, "limits", limits)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "updated limits for team %s", teamID)
}

// handleDebugTeamSetAuthProvider sets the team-level auth_provider.
// POST /debug/teams/set-auth-provider with team_id, auth_provider (empty, "google", or "oidc")
func (s *Server) handleDebugTeamSetAuthProvider(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.FormValue("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	authProvider := r.FormValue("auth_provider")

	switch authProvider {
	case "", "google", "oidc":
		// valid
	default:
		http.Error(w, "auth_provider must be empty, 'google', or 'oidc'", http.StatusBadRequest)
		return
	}

	var apPtr *string
	if authProvider != "" {
		apPtr = &authProvider
	}

	err = withTx1(s, ctx, (*exedb.Queries).SetTeamAuthProvider, exedb.SetTeamAuthProviderParams{
		AuthProvider: apPtr,
		TeamID:       teamID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to set auth provider: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "set team auth_provider via debug", "team_id", teamID, "auth_provider", authProvider)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "set auth_provider=%q for team %s", authProvider, teamID)
}

// handleDebugTeamSetSSO configures an OIDC SSO provider for a team.
// POST /debug/teams/set-sso with team_id, issuer_url, client_id, client_secret, display_name
// Optional: auth_url, token_url, userinfo_url to skip OIDC discovery (for testing).
func (s *Server) handleDebugTeamSetSSO(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.FormValue("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	issuerURL := strings.TrimRight(r.FormValue("issuer_url"), "/")
	clientID := r.FormValue("client_id")
	clientSecret := r.FormValue("client_secret")
	displayName := r.FormValue("display_name")

	if issuerURL == "" || clientID == "" || clientSecret == "" {
		http.Error(w, "issuer_url, client_id, and client_secret are required", http.StatusBadRequest)
		return
	}

	// If auth_url and token_url are provided directly, skip OIDC discovery.
	authURL := r.FormValue("auth_url")
	tokenURL := r.FormValue("token_url")
	userinfoURL := r.FormValue("userinfo_url")
	if authURL != "" && tokenURL != "" {
		s.slog().InfoContext(ctx, "skipping OIDC discovery, using provided endpoints",
			"auth_url", authURL, "token_url", tokenURL)
	} else {
		// Run OIDC discovery to validate and cache endpoints
		doc, err := oidcauth.TestConnectivity(ctx, issuerURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("OIDC discovery failed: %v", err), http.StatusBadRequest)
			return
		}
		authURL = doc.AuthorizationEndpoint
		tokenURL = doc.TokenEndpoint
		userinfoURL = doc.UserinfoEndpoint
	}

	var dnPtr *string
	if displayName != "" {
		dnPtr = &displayName
	}

	// Check if SSO already exists for this team
	existing, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamSSOProvider, teamID)
	if err == nil {
		// Update existing
		// If client_secret is "***" (masked), keep the existing secret
		if clientSecret == "***" {
			clientSecret = existing.ClientSecret
		}
		err = withTx1(s, ctx, (*exedb.Queries).UpdateTeamSSOProvider, exedb.UpdateTeamSSOProviderParams{
			IssuerUrl:    issuerURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			DisplayName:  dnPtr,
			AuthUrl:      &authURL,
			TokenUrl:     &tokenURL,
			UserinfoUrl:  &userinfoURL,
			TeamID:       teamID,
		})
	} else {
		// Insert new
		err = withTx1(s, ctx, (*exedb.Queries).InsertTeamSSOProvider, exedb.InsertTeamSSOProviderParams{
			TeamID:       teamID,
			IssuerUrl:    issuerURL,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			DisplayName:  dnPtr,
			AuthUrl:      &authURL,
			TokenUrl:     &tokenURL,
			UserinfoUrl:  &userinfoURL,
		})
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to save SSO config: %v", err), http.StatusInternalServerError)
		return
	}

	callbackURL := s.webBaseURLNoRequest() + "/oauth/oidc/callback"
	spLoginURL := fmt.Sprintf("%s/oauth/oidc/login?issuer=%s", s.webBaseURLNoRequest(), issuerURL)

	s.slog().InfoContext(ctx, "configured team SSO via debug",
		"team_id", teamID, "issuer", issuerURL)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":       "ok",
		"callback_url": callbackURL,
		"sp_login_url": spLoginURL,
	})
}

// handleDebugTeamDeleteSSO removes the SSO provider from a team.
// POST /debug/teams/delete-sso with team_id
func (s *Server) handleDebugTeamDeleteSSO(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID, err := parseTeamID(r.FormValue("team_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = withTx1(s, ctx, (*exedb.Queries).DeleteTeamSSOProvider, teamID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to delete SSO config: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "deleted team SSO via debug", "team_id", teamID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "deleted SSO for team %s", teamID)
}

// handleDebugTeamTestSSO tests OIDC discovery for the given issuer URL.
// POST /debug/teams/test-sso with issuer_url
func (s *Server) handleDebugTeamTestSSO(w http.ResponseWriter, r *http.Request) {
	issuerURL := strings.TrimRight(r.FormValue("issuer_url"), "/")
	if issuerURL == "" {
		http.Error(w, "issuer_url is required", http.StatusBadRequest)
		return
	}

	doc, err := oidcauth.TestConnectivity(r.Context(), issuerURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":                 "ok",
		"issuer":                 doc.Issuer,
		"authorization_endpoint": doc.AuthorizationEndpoint,
		"token_endpoint":         doc.TokenEndpoint,
		"userinfo_endpoint":      doc.UserinfoEndpoint,
	})
}

// handleDebugDeleteUser deletes a user and all associated data.
// POST /debug/users/delete with user_id
func (s *Server) handleDebugDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.env.AllowDeleteUser {
		http.Error(w, "user deletion is not allowed in this environment", http.StatusForbidden)
		return
	}
	ctx := r.Context()

	lc := new(local.Client)
	who, err := lc.WhoIs(ctx, r.RemoteAddr)
	if err != nil || who.UserProfile == nil || who.UserProfile.LoginName == "" {
		http.Error(w, "user deletion requires a Tailscale user (not a tagged node)", http.StatusForbidden)
		return
	}
	deletedBy := who.UserProfile.LoginName

	userID := r.FormValue("user_id")
	if userID == "" {
		http.Error(w, "user_id is required", http.StatusBadRequest)
		return
	}

	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("user %q not found", userID), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to look up user: %v", err), http.StatusInternalServerError)
		return
	}

	// Delete all user's boxes (handles exelet teardown, IP shards, proxy notify).
	boxIDs, err := withRxRes1(s, ctx, (*exedb.Queries).ListBoxIDsForUser, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to list boxes for user deletion", "error", err)
	}
	for _, boxID := range boxIDs {
		box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByID, boxID)
		if err != nil {
			continue
		}
		if err := s.deleteBox(ctx, box); err != nil {
			s.slog().ErrorContext(ctx, "failed to delete box for user deletion",
				"box_id", boxID, "user_id", userID, "error", err)
		}
	}

	// Remove from team if member of one.
	teamRow, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamForUser, userID)
	if err == nil {
		if err := s.deleteTeamMember(ctx, teamRow.TeamID, userID); err != nil {
			s.slog().ErrorContext(ctx, "failed to delete team membership for user deletion",
				"team_id", teamRow.TeamID, "user_id", userID, "error", err)
		}
	}

	// Delete pending team invites created by this user.
	if err := withTx1(s, ctx, (*exedb.Queries).DeletePendingTeamInvitesByUser, userID); err != nil {
		s.slog().ErrorContext(ctx, "failed to delete pending team invites for user deletion", "user_id", userID, "error", err)
	}

	// Delete accounts (cascades to billing_events).
	if err := withTx1(s, ctx, (*exedb.Queries).DeleteAccountsByUserID, userID); err != nil {
		s.slog().ErrorContext(ctx, "failed to delete accounts for user deletion", "user_id", userID, "error", err)
	}

	// Delete user (cascades to auth_cookies, auth_tokens, ssh_keys, passkeys, box_shares, etc.)
	if err := withTx1(s, ctx, (*exedb.Queries).DeleteUser, userID); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete user: %v", err), http.StatusInternalServerError)
		return
	}

	// Notify proxy so it cleans up any cached state for this user.
	s.sendProxyUserChange(ctx, userID)

	s.slog().InfoContext(ctx, "deleted user via debug",
		"user_id", userID, "email", user.Email, "boxes_deleted", len(boxIDs),
		"deleted_by", deletedBy, "remote_addr", r.RemoteAddr)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "deleted user %s (%s), %d VMs deleted", userID, user.Email, len(boxIDs))
}

// handleDebugRenameUserEmail changes a user's email address.
// POST /debug/users/rename-email with user_id, new_email
func (s *Server) handleDebugRenameUserEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.FormValue("user_id")
	newEmail := strings.TrimSpace(r.FormValue("new_email"))
	if userID == "" || newEmail == "" {
		http.Error(w, "user_id and new_email are required", http.StatusBadRequest)
		return
	}

	newCanonical, err := email.CanonicalizeEmail(newEmail)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid new email: %v", err), http.StatusBadRequest)
		return
	}

	// Verify the source user exists.
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		http.Error(w, fmt.Sprintf("user %q not found: %v", userID, err), http.StatusNotFound)
		return
	}
	oldEmail := user.Email

	// Check if another user already has this email.
	conflictingUser, err := s.GetUserByEmail(ctx, newEmail)
	if err == nil && conflictingUser.UserID != userID {
		http.Error(w, fmt.Sprintf("another user %q already has email %q (canonical: %s) — delete that account first",
			conflictingUser.UserID, conflictingUser.Email, newCanonical), http.StatusConflict)
		return
	}

	// Update the user's email.
	if err := withTx1(s, ctx, (*exedb.Queries).UpdateUserEmail, exedb.UpdateUserEmailParams{
		Email:          newEmail,
		CanonicalEmail: &newCanonical,
		UserID:         userID,
	}); err != nil {
		http.Error(w, fmt.Sprintf("failed to update email: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "renamed user email via debug",
		"user_id", userID, "old_email", oldEmail, "new_email", newEmail)

	http.Redirect(w, r, fmt.Sprintf("/debug/user?userId=%s", url.QueryEscape(userID)), http.StatusSeeOther)
}

func (s *Server) handleDebugRegions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type regionInfo struct {
		Code              string         `json:"code"`
		Display           string         `json:"display"`
		Active            bool           `json:"active"`
		RequiresUserMatch bool           `json:"requires_user_match"`
		ExeletsTotal      int            `json:"exelets_total"`
		ExeletsUp         int            `json:"exelets_up"`
		VMsTotal          int            `json:"vms_total"`
		VMsByStatus       map[string]int `json:"vms_by_status"`
		Users             int            `json:"users"`
		CapacityTotal     int            `json:"capacity_total"`
		CapacityUsed      int            `json:"capacity_used"`
	}

	// Build a map from region code -> regionInfo, seeded from region.All().
	regions := region.All()
	infoByCode := make(map[string]*regionInfo, len(regions))
	for _, reg := range regions {
		infoByCode[reg.Code] = &regionInfo{
			Code:              reg.Code,
			Display:           reg.Display,
			Active:            reg.Active,
			RequiresUserMatch: reg.RequiresUserMatch,
			VMsByStatus:       make(map[string]int),
		}
	}

	// Count exelets per region from live clients.
	for _, ec := range s.exeletClients {
		code := ec.region.Code
		info, ok := infoByCode[code]
		if !ok {
			// Exelet in an unknown region — create an entry for it.
			info = &regionInfo{
				Code:        code,
				Display:     "(unknown)",
				VMsByStatus: make(map[string]int),
			}
			infoByCode[code] = info
		}
		info.ExeletsTotal++
		if ec.up.Load() {
			info.ExeletsUp++
			info.CapacityTotal += int(ec.VMHardLimit())
		}
	}

	// Query VM counts grouped by region and status.
	boxCounts, err := withRxRes0(s, ctx, (*exedb.Queries).CountBoxesByRegionAndStatus)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query box counts: %v", err), http.StatusInternalServerError)
		return
	}
	for _, row := range boxCounts {
		info, ok := infoByCode[row.Region]
		if !ok {
			info = &regionInfo{
				Code:        row.Region,
				Display:     "(unknown)",
				VMsByStatus: make(map[string]int),
			}
			infoByCode[row.Region] = info
		}
		info.VMsByStatus[row.Status] += int(row.Count)
		info.VMsTotal += int(row.Count)
	}

	// Query user counts grouped by region.
	userCounts, err := withRxRes0(s, ctx, (*exedb.Queries).CountUsersByRegion)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to query user counts: %v", err), http.StatusInternalServerError)
		return
	}
	for _, row := range userCounts {
		info, ok := infoByCode[row.Region]
		if !ok {
			info = &regionInfo{
				Code:        row.Region,
				Display:     "(unknown)",
				VMsByStatus: make(map[string]int),
			}
			infoByCode[row.Region] = info
		}
		info.Users = int(row.Count)
	}

	// Compute capacity used and build sorted result.
	for _, info := range infoByCode {
		info.CapacityUsed = info.VMsTotal
	}

	result := make([]regionInfo, 0, len(infoByCode))
	for _, reg := range regions {
		if info, ok := infoByCode[reg.Code]; ok {
			result = append(result, *info)
			delete(infoByCode, reg.Code)
		}
	}
	// Append any unknown regions at the end, in sorted order.
	unknownCodes := make([]string, 0, len(infoByCode))
	for code := range infoByCode {
		unknownCodes = append(unknownCodes, code)
	}
	sort.Strings(unknownCodes)
	for _, code := range unknownCodes {
		result = append(result, *infoByCode[code])
	}

	// Query users with RequiresUserMatch regions who have VMs outside their region.
	type outOfRegionUser struct {
		UserID     string `json:"user_id"`
		Email      string `json:"email"`
		UserRegion string `json:"user_region"`
		BoxCount   int64  `json:"box_count"`
	}
	var outOfRegionUsers []outOfRegionUser
	allOutOfRegion, err := withRxRes0(s, ctx, (*exedb.Queries).GetUsersWithOutOfRegionBoxes)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to query out-of-region users", "error", err)
	} else {
		for _, row := range allOutOfRegion {
			reg, err := region.ByCode(row.UserRegion)
			if err != nil || !reg.RequiresUserMatch {
				continue
			}
			outOfRegionUsers = append(outOfRegionUsers, outOfRegionUser{
				UserID:     row.UserID,
				Email:      row.Email,
				UserRegion: row.UserRegion,
				BoxCount:   row.BoxCount,
			})
		}
	}

	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		jsonData := struct {
			Regions          []regionInfo      `json:"regions"`
			OutOfRegionUsers []outOfRegionUser `json:"out_of_region_users,omitempty"`
		}{
			Regions:          result,
			OutOfRegionUsers: outOfRegionUsers,
		}
		if err := enc.Encode(jsonData); err != nil {
			s.slog().InfoContext(ctx, "failed to encode regions JSON", "error", err)
		}
		return
	}

	data := struct {
		Regions          []regionInfo
		OutOfRegionUsers []outOfRegionUser
	}{
		Regions:          result,
		OutOfRegionUsers: outOfRegionUsers,
	}

	s.renderDebugTemplate(ctx, w, "regions.html", data)
}

func (s *Server) handleDebugJump(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "q parameter is required", http.StatusBadRequest)
		return
	}

	// 1. Box by name.
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, q)
	if err == nil {
		http.Redirect(w, r, "/debug/vms/"+url.PathEscape(box.Name), http.StatusFound)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 2. User by email (if input contains @).
	if strings.Contains(q, "@") {
		canonical, cerr := email.CanonicalizeEmail(q)
		if cerr == nil {
			user, uerr := withRxRes1(s, ctx, (*exedb.Queries).GetUserByEmail, &canonical)
			if uerr == nil {
				http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(user.UserID), http.StatusFound)
				return
			}
			if !errors.Is(uerr, sql.ErrNoRows) {
				http.Error(w, fmt.Sprintf("lookup failed: %v", uerr), http.StatusInternalServerError)
				return
			}
		}
	}

	// 3. User by user_id.
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, q)
	if err == nil {
		http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(user.UserID), http.StatusFound)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 4. Team by ID (try raw, then with tm_ prefix).
	teamQ := q
	if tid, err := parseTeamID(q); err == nil {
		teamQ = tid
	}
	team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeam, teamQ)
	if err == nil {
		http.Redirect(w, r, "/debug/teams/members?team_id="+url.QueryEscape(team.TeamID), http.StatusFound)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 6. Region by name/code.
	for _, reg := range region.All() {
		if strings.EqualFold(q, reg.Code) {
			http.Redirect(w, r, "/debug/regions", http.StatusFound)
			return
		}
	}

	// 7. SSH key by fingerprint.
	if strings.HasPrefix(q, "SHA256:") || strings.HasPrefix(q, "MD5:") {
		sshKey, sshErr := withRxRes1(s, ctx, (*exedb.Queries).GetSSHKeyByFingerprint, q)
		if sshErr == nil {
			http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(sshKey.UserID), http.StatusFound)
			return
		}
		if !errors.Is(sshErr, sql.ErrNoRows) {
			http.Error(w, fmt.Sprintf("lookup failed: %v", sshErr), http.StatusInternalServerError)
			return
		}
	}

	// 8. Invite code.
	inviteCode, err := withRxRes1(s, ctx, (*exedb.Queries).GetInviteCodeByCode, q)
	if err == nil {
		if inviteCode.UsedByUserID != nil {
			http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(*inviteCode.UsedByUserID), http.StatusFound)
			return
		}
		if inviteCode.AssignedToUserID != nil {
			http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(*inviteCode.AssignedToUserID), http.StatusFound)
			return
		}
		fmt.Fprintf(w, "invite code %q (id=%d, plan=%s) — unassigned, unused", inviteCode.Code, inviteCode.ID, inviteCode.PlanType)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 9. User by Discord username.
	discordUser, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserByDiscordUsername, &q)
	if err == nil {
		http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(discordUser.UserID), http.StatusFound)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 11. Account by ID.
	account, err := withRxRes1(s, ctx, (*exedb.Queries).GetAccount, q)
	if err == nil {
		http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(account.CreatedBy), http.StatusFound)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 12. Integration by ID.
	integration, err := withRxRes1(s, ctx, (*exedb.Queries).GetIntegration, q)
	if err == nil {
		http.Redirect(w, r, "/debug/user?userId="+url.QueryEscape(integration.OwnerUserID), http.StatusFound)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 13. Box by container ID.
	boxByCtr, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByContainerID, &q)
	if err == nil {
		http.Redirect(w, r, "/debug/vms/"+url.PathEscape(boxByCtr.Name), http.StatusFound)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, fmt.Sprintf("lookup failed: %v", err), http.StatusInternalServerError)
		return
	}

	// 14. Exelet by hostname.
	for addr := range s.exeletClients {
		if strings.EqualFold(q, addr) {
			http.Redirect(w, r, "/debug/exelets", http.StatusFound)
			return
		}
	}

	http.Error(w, fmt.Sprintf("no match found for %q", q), http.StatusNotFound)
}

// handleDebugIntegrations displays integration counts per user with GitHub usernames.
func (s *Server) handleDebugIntegrations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	integrations, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllIntegrations)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list integrations: %v", err), http.StatusInternalServerError)
		return
	}

	// Group by user, count by type.
	type userRow struct {
		UserID string
		Counts map[string]int
		Total  int
	}
	userMap := map[string]*userRow{}
	typeSet := map[string]bool{}
	for _, ig := range integrations {
		ur, ok := userMap[ig.OwnerUserID]
		if !ok {
			ur = &userRow{UserID: ig.OwnerUserID, Counts: map[string]int{}}
			userMap[ig.OwnerUserID] = ur
		}
		ur.Counts[ig.Type]++
		ur.Total++
		typeSet[ig.Type] = true
	}

	// Sorted type names.
	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, t)
	}
	sort.Strings(types)

	// Collect user IDs and resolve emails + GitHub usernames.
	userIDs := make([]string, 0, len(userMap))
	for uid := range userMap {
		userIDs = append(userIDs, uid)
	}

	emailMap := map[string]string{}
	ghMap := map[string]string{}
	for _, uid := range userIDs {
		if email, err := withRxRes1(s, ctx, (*exedb.Queries).GetEmailByUserID, uid); err == nil {
			emailMap[uid] = email
		}
		if tokens, err := withRxRes1(s, ctx, (*exedb.Queries).ListGitHubUserTokens, uid); err == nil && len(tokens) > 0 {
			ghMap[uid] = tokens[0].GitHubLogin
		}
	}

	if r.URL.Query().Get("format") == "json" {
		type jsonRow struct {
			UserID         string         `json:"user_id"`
			Email          string         `json:"email"`
			GitHubUsername string         `json:"github_username,omitempty"`
			Total          int            `json:"total"`
			Counts         map[string]int `json:"counts"`
		}
		rows := make([]jsonRow, 0, len(userMap))
		for _, uid := range userIDs {
			ur := userMap[uid]
			rows = append(rows, jsonRow{
				UserID:         uid,
				Email:          emailMap[uid],
				GitHubUsername: ghMap[uid],
				Total:          ur.Total,
				Counts:         ur.Counts,
			})
		}
		// Sort by total descending.
		sort.Slice(rows, func(i, j int) bool { return rows[i].Total > rows[j].Total })

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			Types []string  `json:"types"`
			Rows  []jsonRow `json:"rows"`
		}{Types: types, Rows: rows})
		return
	}

	data := struct {
		TotalCount int
		UserCount  int
		Types      []string
	}{
		TotalCount: len(integrations),
		UserCount:  len(userMap),
		Types:      types,
	}
	s.renderDebugTemplate(ctx, w, "integrations.html", data)
}

func (s *Server) handleDebugGitHubIntegrations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	tokens, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllGitHubUserTokens)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list GitHub tokens: %v", err), http.StatusInternalServerError)
		return
	}
	installations, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllGitHubInstallationsWithTokens)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list GitHub installations: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Tokens        []exedb.ListAllGitHubUserTokensRow
		Installations []exedb.ListAllGitHubInstallationsWithTokensRow
	}{
		Tokens:        tokens,
		Installations: installations,
	}
	s.renderDebugTemplate(ctx, w, "github-integrations.html", data)
}

func (s *Server) handleDebugGitHubIntegrationsRefresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := r.FormValue("user_id")
	githubLogin := r.FormValue("github_login")

	tok, err := withRxRes1(s, ctx, (*exedb.Queries).GetGitHubUserToken, exedb.GetGitHubUserTokenParams{
		UserID:      userID,
		GitHubLogin: githubLogin,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("user token not found: %v", err), http.StatusNotFound)
		return
	}
	if tok.RefreshToken == "" {
		http.Error(w, "no refresh token", http.StatusBadRequest)
		return
	}

	// Serialize token refreshes to prevent concurrent refresh token rotation.
	s.githubRefreshMu.Lock()
	defer s.githubRefreshMu.Unlock()

	// Re-read the token under the lock — another refresh may have already
	// given us a fresh token, in which case we can skip the GitHub round-trip.
	tok, err = withRxRes1(s, ctx, (*exedb.Queries).GetGitHubUserToken, exedb.GetGitHubUserTokenParams{
		UserID:      userID,
		GitHubLogin: githubLogin,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("user token not found: %v", err), http.StatusNotFound)
		return
	}
	if tok.AccessTokenExpiresAt != nil {
		if expires, err := parseGitHubTokenExpiry(*tok.AccessTokenExpiresAt); err == nil {
			if time.Until(expires) > 5*time.Minute {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "OK (already fresh)")
				return
			}
		}
	}

	tokenResp, err := s.githubApp.RefreshUserToken(ctx, tok.RefreshToken)
	if err != nil {
		s.slog().ErrorContext(ctx, "debug: GitHub token refresh failed",
			"user_id", tok.UserID,
			"github_login", tok.GitHubLogin,
			"error", err,
		)
		http.Error(w, fmt.Sprintf("refresh failed: %v", err), http.StatusInternalServerError)
		return
	}

	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateGitHubUserToken(ctx, exedb.UpdateGitHubUserTokenParams{
			AccessToken:           tokenResp.AccessToken,
			RefreshToken:          tokenResp.RefreshToken,
			AccessTokenExpiresAt:  tokenResp.AccessTokenExpiresAt(),
			RefreshTokenExpiresAt: tokenResp.RefreshTokenExpiresAt(),
			UserID:                userID,
			GitHubLogin:           githubLogin,
		})
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("save failed: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "debug: refreshed GitHub token",
		"user_id", tok.UserID,
		"github_login", tok.GitHubLogin,
	)
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "OK")
}

// renderDebugTemplate renders a debug template to a browser.
// Some of these print a lot of data, so we stream the result,
// and only report relevant errors.
func (s *Server) renderDebugTemplate(ctx context.Context, w http.ResponseWriter, templateName string, data any) {
	tmpl, err := debug_templates.Parse(s.env)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, templateName, data); err != nil {
		// Don't report errors that indicate that
		// the user closed the web page.
		switch {
		case errors.Is(err, net.ErrClosed):
		case errors.Is(err, syscall.EPIPE):
		case errors.Is(err, syscall.ECONNRESET):
		default:
			s.slog().ErrorContext(ctx, "failed to execute debug template", "templateName", templateName, "error", err)
		}
	}
}
