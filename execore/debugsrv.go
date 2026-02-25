package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"html"
	"html/template"
	"io"
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
	"time"

	"exe.dev/billing"
	"exe.dev/email"
	"exe.dev/execore/debug_templates"
	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	"exe.dev/exeweb"
	"exe.dev/llmgateway"
	"exe.dev/logging"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
	"exe.dev/publicips"

	"tailscale.com/client/local"
)

// debugHandler constructs and returns a handler with Go-standard debug endpoints
// (pprof, expvar). Creating this handler is cheap and avoids global state.
func (s *Server) debugHandler() http.Handler {
	mux := http.NewServeMux()

	// index & aux
	mux.HandleFunc("/debug$", s.handleDebugIndex)
	mux.HandleFunc("/debug/", s.handleDebugIndex)
	mux.HandleFunc("/debug/gitsha", s.handleDebugGitsha)
	mux.HandleFunc("/debug/boxes", s.handleDebugBoxes)
	mux.HandleFunc("GET /debug/vmlist", s.handleDebugVMList)
	mux.HandleFunc("/debug/boxes/{name}", s.handleDebugBoxDetails)
	mux.HandleFunc("GET /debug/boxes/{name}/logs", s.handleDebugBoxLogs)
	mux.HandleFunc("POST /debug/boxes/delete", s.handleDebugBoxDelete)
	mux.HandleFunc("POST /debug/boxes/stop", s.handleDebugBoxStop)
	mux.HandleFunc("POST /debug/boxes/start", s.handleDebugBoxStart)
	mux.HandleFunc("GET /debug/boxes/migrate", s.handleDebugBoxMigrateForm)
	mux.HandleFunc("POST /debug/boxes/migrate", s.handleDebugBoxMigrate)
	mux.HandleFunc("GET /debug/migrate", s.handleDebugMassMigrateForm)
	mux.HandleFunc("GET /debug/migrate/boxes", s.handleDebugMassMigrateBoxes)
	mux.HandleFunc("POST /debug/migrate", s.handleDebugMassMigrate)
	mux.HandleFunc("/debug/users", s.handleDebugUsers)
	mux.HandleFunc("/debug/user", s.handleDebugUser)
	mux.HandleFunc("POST /debug/user/give-invites", s.handleDebugUserGiveInvites)
	mux.HandleFunc("POST /debug/users/toggle-root-support", s.handleDebugToggleRootSupport)
	mux.HandleFunc("POST /debug/users/toggle-vm-creation", s.handleDebugToggleVMCreation)
	mux.HandleFunc("POST /debug/users/toggle-lockout", s.handleDebugToggleLockout)
	mux.HandleFunc("POST /debug/users/update-credit", s.handleDebugUpdateUserCredit)
	mux.HandleFunc("POST /debug/users/set-limits", s.handleDebugSetUserLimits)
	mux.HandleFunc("/debug/exelets", s.handleDebugExelets)
	mux.HandleFunc("POST /debug/exelets/set-preferred", s.handleDebugSetPreferredExelet)
	mux.HandleFunc("POST /debug/exelets/recover", s.handleDebugExeletRecover)
	mux.HandleFunc("/debug/new-throttle", s.handleDebugNewThrottle)
	mux.HandleFunc("POST /debug/new-throttle", s.handleDebugNewThrottlePost)
	mux.HandleFunc("/debug/signup-limiter", s.handleDebugSignupLimiter)
	mux.HandleFunc("POST /debug/signup-limiter", s.handleDebugSignupLimiterPost)
	mux.HandleFunc("/debug/signup-pow", s.handleDebugSignupPOW)
	mux.HandleFunc("POST /debug/signup-pow", s.handleDebugSignupPOWPost)
	mux.HandleFunc("/debug/ip-abuse-filter", s.handleDebugIPAbuseFilter)
	mux.HandleFunc("POST /debug/ip-abuse-filter", s.handleDebugIPAbuseFilterPost)
	mux.HandleFunc("/debug/signup-reject", s.handleDebugSignupReject)
	mux.HandleFunc("POST /debug/signup-reject", s.handleDebugSignupRejectPost)
	mux.HandleFunc("/debug/ipshards", s.handleDebugIPShards)
	mux.HandleFunc("POST /debug/ipshards/toggle", s.handleDebugIPShardsToggle)
	mux.HandleFunc("POST /debug/ipshards/latitude", s.handleDebugIPShardsLatitude)
	mux.HandleFunc("GET /debug/log", s.handleDebugLogForm)
	mux.HandleFunc("POST /debug/log", s.handleDebugLog)
	mux.HandleFunc("/debug/testimonials", s.handleDebugTestimonials)
	mux.HandleFunc("GET /debug/email", s.handleDebugEmailForm)
	mux.HandleFunc("POST /debug/email", s.handleDebugEmailSend)
	mux.HandleFunc("/debug/invite", s.handleDebugInvite)
	mux.HandleFunc("POST /debug/invite", s.handleDebugInvitePost)
	mux.HandleFunc("/debug/all-invite-codes", s.handleDebugAllInviteCodes)
	mux.HandleFunc("/debug/invite-tree", s.handleDebugInviteTree)
	mux.HandleFunc("/debug/bounces", s.handleDebugBounces)
	mux.HandleFunc("POST /debug/bounces", s.handleDebugBouncesPost)
	mux.HandleFunc("GET /debug/teams", s.handleDebugTeams)
	mux.HandleFunc("POST /debug/teams/create", s.handleDebugTeamCreate)
	mux.HandleFunc("POST /debug/teams/add-member", s.handleDebugTeamAddMember)
	mux.HandleFunc("GET /debug/teams/members", s.handleDebugTeamMembers)
	mux.HandleFunc("POST /debug/teams/remove-member", s.handleDebugTeamRemoveMember)
	mux.HandleFunc("POST /debug/teams/update-role", s.handleDebugTeamUpdateRole)
	mux.HandleFunc("POST /debug/teams/set-limits", s.handleDebugTeamSetLimits)
	mux.HandleFunc("GET /debug/ideas", s.handleDebugTemplateReview)
	mux.HandleFunc("POST /debug/ideas", s.handleDebugTemplateReviewPost)

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
	if commit == "" {
		commit = "unknown"
	}

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Stage      string
		GitCommit  string
		GitHubLink template.HTML
	}{
		Stage:      s.env.String(),
		GitCommit:  commit,
		GitHubLink: template.HTML(gitHubLink(commit)),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.slog().ErrorContext(r.Context(), "failed to execute index template", "error", err)
	}
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
		tmpl, err := debug_templates.Parse()
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
			return
		}

		// Build the navigation links
		var sourceNav template.HTML
		if source == "exelets" {
			sourceNav = `<strong>exelets</strong> | <a href="/debug/boxes?source=db">db</a>`
		} else {
			sourceNav = `<a href="/debug/boxes?source=exelets">exelets</a> | <strong>db</strong>`
		}

		data := struct {
			Source    string
			SourceNav template.HTML
		}{
			Source:    source,
			SourceNav: sourceNav,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "boxes.html", data); err != nil {
			s.slog().ErrorContext(ctx, "failed to execute boxes template", "error", err)
		}
		return
	}

	// JSON format requested
	type boxInfo struct {
		Host             string `json:"host"`
		ID               string `json:"id,omitempty"`
		Name             string `json:"name"`
		Status           string `json:"status"`
		OwnerUserID      string `json:"owner_user_id,omitempty"`
		OwnerEmail       string `json:"owner_email,omitempty"`
		Region           string `json:"region"`
		OwnerRootSupport bool   `json:"owner_root_support"`
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
					info.OwnerRootSupport = owner.RootSupport == 1
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
			http.Error(w, "failed to list boxes", http.StatusInternalServerError)
			return
		}
		for _, b := range dbBoxes {
			info := boxInfo{
				Host:             b.Ctrhost,
				Name:             b.Name,
				Status:           b.Status,
				OwnerUserID:      b.OwnerUserID,
				OwnerEmail:       b.OwnerEmail,
				Region:           b.Region,
				OwnerRootSupport: b.OwnerRootSupport == 1,
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

// gitHubLink returns an HTML link to the GitHub commit history starting at the given SHA.
func gitHubLink(commit string) string {
	if commit == "" || commit == "unknown" {
		return ""
	}
	return fmt.Sprintf(`(<a href="https://github.com/boldsoftware/exe/commits/%s">gh</a>)`, commit)
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
		http.Error(w, fmt.Sprintf("/debug/boxes: failed to look up box by name: %v", err), http.StatusInternalServerError)
		return
	}

	// Delete the box using the same logic as the REPL `rm` command
	if err := s.deleteBox(ctx, box); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete box: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "box deleted via debug page", "box", boxName)

	// Redirect back to the boxes page
	http.Redirect(w, r, "/debug/boxes", http.StatusSeeOther)
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
	http.Redirect(w, r, "/debug/boxes", http.StatusSeeOther)
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
		http.Error(w, fmt.Sprintf("failed to list boxes: %v", err), http.StatusInternalServerError)
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
	http.Redirect(w, r, "/debug/boxes", http.StatusSeeOther)
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "box-migrate.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute box-migrate template", "error", err)
	}
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

	// Step 1: Stop VM on source (skip for two-phase - SendVM handles it)
	if !twoPhase && wasRunning {
		writeProgress("Stopping VM on source exelet...")
		s.slog().InfoContext(ctx, "stopping VM for migration", "box", boxName, "container_id", containerID, "source", box.Ctrhost)
		if _, err := sourceClient.client.StopInstance(ctx, &computeapi.StopInstanceRequest{ID: containerID}); err != nil {
			writeError("failed to stop VM on source: %v", err)
			return
		}
		writeProgress("VM stopped.")
	}

	// restartSource restarts the VM on source if migration fails after stopping it.
	// For two-phase, the VM may or may not have been stopped depending on which phase failed.
	// Uses exponential backoff retry in case the exelet is temporarily unavailable.
	restartSource := func(reason string) {
		if !wasRunning {
			writeProgress("Source VM was already stopped, nothing to restart.")
			return
		}
		// Check if VM is already running (e.g. two-phase failed during phase 1)
		if twoPhase {
			inst, err := sourceClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
			if err == nil && inst.Instance.State == computeapi.VMState_RUNNING {
				writeProgress("VM is still running on source (failed before stop).")
				return
			}
		}
		writeProgress("Restarting VM on source exelet to restore service...")
		s.slog().ErrorContext(ctx, "migration failed, restarting VM on source",
			"box", boxName, "container_id", containerID, "source", box.Ctrhost, "reason", reason)
		delay := 100 * time.Millisecond
		deadline := time.Now().Add(10 * time.Second)
		for attempt := 1; ; attempt++ {
			if _, err := sourceClient.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: containerID}); err == nil {
				writeProgress("VM restarted on source.")
				return
			} else if time.Now().After(deadline) {
				writeProgress("ERROR: failed to restart VM on source after %d attempts: %v", attempt, err)
				s.slog().ErrorContext(ctx, "failed to restart VM on source after migration failure",
					"box", boxName, "container_id", containerID, "source", box.Ctrhost, "attempts", attempt, "error", err)
				return
			} else {
				writeProgress("Restart attempt %d failed (%v), retrying...", attempt, err)
			}
			time.Sleep(delay)
			delay *= 2
		}
	}

	// Step 2: Perform migration
	writeProgress("Starting disk transfer from %s to %s...", box.Ctrhost, targetAddr)
	s.slog().InfoContext(ctx, "starting migration", "box", boxName, "source", box.Ctrhost, "target", targetAddr, "two_phase", twoPhase)
	if err := s.migrateVM(ctx, sourceClient.client, targetClient.client, containerID, twoPhase, writeProgress); err != nil {
		writeError("migration failed: %v", err)
		restartSource(err.Error())
		return
	}
	writeProgress("Disk transfer complete.")

	// Step 3: Start VM on target (skip if source was stopped)
	var sshPort *int64
	dbStatus := "running"
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
	} else {
		writeProgress("Source VM was stopped, leaving stopped on target.")
		dbStatus = "stopped"
	}

	// Step 5: Update database with new ctrhost, ssh_port, and status
	writeProgress("Updating database...")
	if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxMigration, exedb.UpdateBoxMigrationParams{
		Ctrhost: targetAddr,
		SSHPort: sshPort,
		Status:  dbStatus,
		ID:      box.ID,
	}); err != nil {
		writeError("failed to update database: %v", err)
		restartSource(err.Error())
		return
	}
	writeProgress("Database updated.")

	go s.sendBoxMaintenanceEmail(context.Background(), boxName)

	// Log warning about source cleanup
	s.slog().WarnContext(ctx, "VM migrated - source instance needs manual cleanup",
		"box_name", boxName,
		"container_id", containerID,
		"source_host", box.Ctrhost,
		"target_host", targetAddr,
	)

	writeProgress("")
	writeProgress("=== Migration complete! ===")
	writeProgress("")
	writeProgress("To verify the target instance:")
	writeProgress("  1. Check boot logs:")
	writeProgress("     ./exelet-ctl -a %s compute instances logs %s", targetAddr, containerID)
	writeProgress("  2. Verify SSH connectivity to the box")
	writeProgress("")
	writeProgress("After confirming the target instance is working correctly,")
	writeProgress("remove the old instance from the source exelet:")
	writeProgress("  ./exelet-ctl -a %s compute instances rm %s", box.Ctrhost, containerID)
	writeProgress("")
	writeProgress("View box details: /debug/boxes/%s", boxName)
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
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to send start request: %w", err)
	}

	// Receive metadata from source
	resp, err := sendStream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive metadata: %w", err)
	}
	metadata := resp.GetMetadata()
	if metadata == nil {
		return fmt.Errorf("expected metadata, got %T", resp.Type)
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

			// Report progress every 10MB
			currentMB := totalBytes / (1024 * 1024)
			if currentMB >= lastReportedMB+10 {
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

// handleDebugMassMigrateForm shows the migration form for multiple boxes.
func (s *Server) handleDebugMassMigrateForm(w http.ResponseWriter, r *http.Request) {
	var addrs []string
	for addr := range s.exeletClients {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Exelets []string
	}{
		Exelets: addrs,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "mass-migrate.html", data); err != nil {
		s.slog().ErrorContext(r.Context(), "failed to execute mass-migrate template", "error", err)
	}
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
	}

	if len(boxNames) == 0 || targetAddr == "" {
		writeError("box_names and target are required")
		writeProgress("MIGRATION_ERROR")
		return
	}

	expectedConfirm := strconv.Itoa(len(boxNames))
	if confirm != expectedConfirm {
		writeError("confirm must be %q (the number of boxes to migrate)", expectedConfirm)
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

	if twoPhase {
		writeProgress("Starting two-phase migration of %d boxes to %s", len(boxNames), targetAddr)
	} else {
		writeProgress("Starting migration of %d boxes to %s", len(boxNames), targetAddr)
	}
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

		// Stop VM on source (skip for two-phase - SendVM handles it)
		if !twoPhase && wasRunning {
			writeProgress("Stopping VM on %s...", box.Ctrhost)
			s.slog().InfoContext(ctx, "migration: stopping VM", "box", boxName, "container_id", containerID, "source", box.Ctrhost)
			if _, err := sourceClient.client.StopInstance(ctx, &computeapi.StopInstanceRequest{ID: containerID}); err != nil {
				writeError("failed to stop VM: %v", err)
				failed++
				writeProgress("")
				continue
			}
			writeProgress("VM stopped.")
		}

		// restartSource restarts the VM on source if migration fails.
		// For two-phase, the VM may or may not have been stopped depending on which phase failed.
		// Uses exponential backoff retry in case the exelet is temporarily unavailable.
		restartSource := func(reason string) {
			if !wasRunning {
				writeProgress("Source VM was already stopped, nothing to restart.")
				return
			}
			if twoPhase {
				inst, err := sourceClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
				if err == nil && inst.Instance.State == computeapi.VMState_RUNNING {
					writeProgress("VM is still running on source (failed before stop).")
					return
				}
			}
			writeProgress("Restarting VM on source exelet to restore service...")
			s.slog().ErrorContext(ctx, "migration failed, restarting VM on source",
				"box", boxName, "container_id", containerID, "source", box.Ctrhost, "reason", reason)
			delay := 100 * time.Millisecond
			deadline := time.Now().Add(10 * time.Second)
			for attempt := 1; ; attempt++ {
				if _, err := sourceClient.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: containerID}); err == nil {
					writeProgress("VM restarted on source.")
					return
				} else if time.Now().After(deadline) {
					writeProgress("ERROR: failed to restart VM on source after %d attempts: %v", attempt, err)
					s.slog().ErrorContext(ctx, "failed to restart VM on source after migration failure",
						"box", boxName, "container_id", containerID, "source", box.Ctrhost, "attempts", attempt, "error", err)
					return
				} else {
					writeProgress("Restart attempt %d failed (%v), retrying...", attempt, err)
				}
				time.Sleep(delay)
				delay *= 2
			}
		}

		writeProgress("Transferring disk from %s to %s...", box.Ctrhost, targetAddr)
		s.slog().InfoContext(ctx, "migration: starting disk transfer", "box", boxName, "source", box.Ctrhost, "target", targetAddr, "two_phase", twoPhase)
		if err := s.migrateVM(ctx, sourceClient.client, targetClient.client, containerID, twoPhase, writeProgress); err != nil {
			writeError("disk transfer failed: %v", err)
			restartSource(err.Error())
			failed++
			writeProgress("")
			continue
		}
		writeProgress("Disk transfer complete.")

		var sshPort *int64
		dbStatus := "running"
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
		} else {
			writeProgress("Source VM was stopped, leaving stopped on target.")
			dbStatus = "stopped"
		}

		writeProgress("Updating database...")
		if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxMigration, exedb.UpdateBoxMigrationParams{
			Ctrhost: targetAddr,
			SSHPort: sshPort,
			Status:  dbStatus,
			ID:      box.ID,
		}); err != nil {
			writeError("failed to update database: %v", err)
			restartSource(err.Error())
			failed++
			writeProgress("")
			continue
		}
		writeProgress("Database updated.")

		s.slog().WarnContext(ctx, "VM migrated via bulk migration - source instance needs manual cleanup",
			"box_name", boxName,
			"container_id", containerID,
			"source_host", box.Ctrhost,
			"target_host", targetAddr,
		)

		go s.sendBoxMaintenanceEmail(context.Background(), boxName)

		writeProgress("Box %s migrated successfully.", boxName)
		succeeded++
		writeProgress("")
	}

	writeProgress("=== Migration complete ===")
	writeProgress("Succeeded: %d, Failed: %d, Total: %d", succeeded, failed, len(boxNames))
	writeProgress("")
	writeProgress("After confirming target instances are working correctly,")
	writeProgress("remove old instances from source exelets.")

	if failed == 0 {
		writeProgress("MIGRATION_SUCCESS")
	} else {
		writeProgress("MIGRATION_ERROR")
	}
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
		http.Error(w, fmt.Sprintf("/debug/boxes/detail: failed to look up box by name: %v", err), http.StatusInternalServerError)
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

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
		HasServerIdentityKey: len(box.SSHServerIdentityKey) > 0,
		HasClientPrivateKey:  len(box.SSHClientPrivateKey) > 0,
		HasAuthorizedKeys:    box.SSHAuthorizedKeys != nil && *box.SSHAuthorizedKeys != "",
		ActiveShares:         activeShareList,
		PendingShares:        pendingShareList,
		ShareLinks:           shareLinkList,
		CreationLog:          creationLog,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "box-details.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute box-details template", "error", err)
	}
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
	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		RegularCount      int
		LoginWithExeCount int
		TotalCount        int
	}{
		RegularCount:      regularCount,
		LoginWithExeCount: loginWithExeCount,
		TotalCount:        len(users),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "users.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute users template", "error", err)
	}
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
			http.Error(w, fmt.Sprintf("user locked out but failed to stop boxes: %v", err), http.StatusInternalServerError)
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

			info.InstanceLimit = int(ec.region.VMHardLimit)

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
	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Exelets []exeletInfo
	}{
		Exelets: exelets,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "exelets.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute exelets template", "error", err)
	}
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
		http.Error(w, fmt.Sprintf("failed to list boxes: %v", err), http.StatusInternalServerError)
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
		// Check whether billing is enabled--don't throttle people
		// who have valid billing information.
		billingStatus, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserBillingStatus, userID)
		if err == nil && userIsPaying(&billingStatus) {
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
func (s *Server) handleDebugSignupLimiter(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	loginDisabled := s.IsLoginCreationDisabled(ctx)

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

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
		RateLimitedHTML template.HTML
		AllTrackedHTML  template.HTML
	}{
		LoginDisabled:   loginDisabled,
		RateLimitedHTML: template.HTML(rateLimitedBuf.String()),
		AllTrackedHTML:  template.HTML(allTrackedBuf.String()),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "signup-limiter.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute signup-limiter template", "error", err)
	}
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

	// Redirect back to the signup limiter page
	http.Redirect(w, r, "/debug/signup-limiter", http.StatusSeeOther)
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
	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Enabled       bool
		EmailPatterns string
		Message       string
	}{
		Enabled:       config.Enabled,
		EmailPatterns: strings.Join(config.EmailPatterns, "\n"),
		Message:       config.Message,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "new-throttle.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute new-throttle template", "error", err)
	}
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
	Shard       int    // 1-25
	ServingIP   string // current ip_shards value (what DNS returns)
	AWSIP       string // aws_ip_shards value
	LatitudeIP  string // latitude_ip_shards value
	ServingFrom string // "aws", "latitude", or "unknown"
}

// handleDebugIPShards displays all IP shard tables and allows toggling the serving source.
func (s *Server) handleDebugIPShards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all three tables
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
	shardNums := make([]int, 0, len(shardSet))
	for s := range shardSet {
		shardNums = append(shardNums, s)
	}
	slices.Sort(shardNums)

	// Build unified shard list
	var entries []ipShardEntry
	for _, shard := range shardNums {
		entry := ipShardEntry{
			Shard:       shard,
			ServingIP:   servingByS[int64(shard)],
			AWSIP:       awsByS[int64(shard)],
			LatitudeIP:  latByS[int64(shard)],
			ServingFrom: "unknown",
		}
		// Determine serving source
		switch entry.ServingIP {
		case "":
			// leave as unknown
		case entry.AWSIP:
			entry.ServingFrom = "aws"
		case entry.LatitudeIP:
			entry.ServingFrom = "latitude"
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
	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Entries []ipShardEntry
		LobbyIP string
	}{
		Entries: entries,
		LobbyIP: s.LobbyIP.String(),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "ipshards.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute ipshards template", "error", err)
	}
}

// handleDebugIPShardsToggle switches a shard's serving IP between AWS and Latitude.
func (s *Server) handleDebugIPShardsToggle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	shardStr := r.FormValue("shard")
	target := r.FormValue("target") // "aws" or "latitude"

	shard, err := strconv.Atoi(shardStr)
	if err != nil || !publicips.ShardIsValid(shard) {
		http.Error(w, "invalid shard number", http.StatusBadRequest)
		return
	}
	if target != "aws" && target != "latitude" {
		http.Error(w, "target must be 'aws' or 'latitude'", http.StatusBadRequest)
		return
	}

	// Get the IP from the target table
	var newIP string
	if target == "aws" {
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
	} else {
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

// handleDebugLogForm renders a simple form to log an error message.
func (s *Server) handleDebugLogForm(w http.ResponseWriter, r *http.Request) {
	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "log-form.html", nil); err != nil {
		s.slog().ErrorContext(r.Context(), "failed to execute log-form template", "error", err)
	}
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "testimonials.html", data); err != nil {
		s.slog().ErrorContext(r.Context(), "failed to execute testimonials template", "error", err)
	}
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "email-form.html", data); err != nil {
		s.slog().ErrorContext(r.Context(), "failed to execute email-form template", "error", err)
	}
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
	err := sender.Send(ctx, email.TypeDebugTest, from, to, subject, body)
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

// handleDebugSignupPOW displays the signup POW configuration page.
func (s *Server) handleDebugSignupPOW(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	enabled := s.IsSignupPOWEnabled(ctx)

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	difficulty := s.signupPOW.GetDifficulty()

	data := struct {
		Enabled    bool
		Difficulty int
		AvgHashes  int
	}{
		Enabled:    enabled,
		Difficulty: difficulty,
		AvgHashes:  1 << difficulty,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "signup-pow.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute signup-pow template", "error", err)
	}
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

	http.Redirect(w, r, "/debug/signup-pow", http.StatusSeeOther)
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "signup-reject.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute signup-reject template", "error", err)
	}
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "invite.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute invite template", "error", err)
	}
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

	if err := s.sendEmail(ctx, email.TypeInvitesAllocated, user.Email, subject, body); err != nil {
		s.slog().WarnContext(ctx, "failed to send invites allocated email", "to", user.Email, "error", err)
	}

	return nil
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
	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "all-invite-codes.html", nil); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute all-invite-codes template", "error", err)
	}
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "invite-tree.html", roots); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute invite-tree template", "error", err)
	}
}

// IsIPAbuseFilterDisabled reports whether the IP abuse filter is disabled.
func (s *Server) IsIPAbuseFilterDisabled(ctx context.Context) bool {
	val, err := withRxRes0(s, ctx, (*exedb.Queries).GetIPAbuseFilterDisabled)
	if err != nil {
		return false
	}
	return val == "true"
}

// handleDebugIPAbuseFilter displays the IP abuse filter configuration page.
func (s *Server) handleDebugIPAbuseFilter(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	disabled := s.IsIPAbuseFilterDisabled(ctx)

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Disabled bool
	}{
		Disabled: disabled,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "ip-abuse-filter.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute ip-abuse-filter template", "error", err)
	}
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

	http.Redirect(w, r, "/debug/ip-abuse-filter", http.StatusSeeOther)
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
		billingAccounts = append(billingAccounts, billingAccountInfo{
			AccountID:    a.ID,
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
		CreditRefreshPerHrUSD      float64
		CreditRefreshPerHrOverride *float64
		CreditTotalUsedUSD         float64
		CreditLastRefreshAt        string
		InvitesTotalAllTimeGiven   int64
		InvitesAllocatedCount      int64
		InvitesAcceptedCount       int64
		InvitePostSuccessful       bool
		Boxes                      []boxInfo
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
	}

	if hasCredit {
		data.CreditPlanName = plan.Name
		data.CreditAvailableUSD = credit.AvailableCredit
		data.CreditEffectiveUSD = creditEffective
		data.CreditMaxUSD = plan.MaxCredit
		data.CreditMaxUSDOverride = credit.MaxCredit
		data.CreditRefreshPerHrUSD = plan.RefreshPerHour
		data.CreditRefreshPerHrOverride = credit.RefreshPerHour
		data.CreditTotalUsedUSD = credit.TotalUsed
		data.CreditLastRefreshAt = credit.LastRefreshAt.Format(time.RFC3339)
	}

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "user.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute user template", "error", err)
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

	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "bounces.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute bounces template", "error", err)
	}
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

	teamID := r.FormValue("team_id")
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

	if teamID == "" || displayName == "" || ownerUserID == "" {
		http.Error(w, "team_id, display_name, and owner_user_id (or owner_email) are required", http.StatusBadRequest)
		return
	}

	// Create the team
	err := withTx1(s, ctx, (*exedb.Queries).InsertTeam, exedb.InsertTeamParams{
		TeamID:      teamID,
		DisplayName: displayName,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create team: %v", err), http.StatusInternalServerError)
		return
	}

	// Add the owner
	err = withTx1(s, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: ownerUserID,
		Role:   "owner",
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to add owner: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "created team via debug", "team_id", teamID, "owner", ownerUserID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "created team %s with owner %s", teamID, ownerUserID)
}

// handleDebugTeamAddMember adds a user to an existing team.
// POST /debug/teams/add-member with team_id, user_id (or email), role (owner or user)
// If email is provided and user doesn't exist, creates a pending invite and sends email.
func (s *Server) handleDebugTeamAddMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID := r.FormValue("team_id")
	userID := r.FormValue("user_id")
	addr := r.FormValue("email")
	role := r.FormValue("role")

	// Resolve email to user_id if provided instead
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
			// Use first team owner as the inviter
			members, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamMembers, teamID)
			if err != nil || len(members) == 0 {
				http.Error(w, "could not find team owner for invite", http.StatusInternalServerError)
				return
			}
			var inviterID string
			for _, m := range members {
				if m.Role == "owner" {
					inviterID = m.UserID
					break
				}
			}
			if inviterID == "" {
				inviterID = members[0].UserID
			}
			if err := s.createPendingTeamInvite(ctx, teamID, team.DisplayName, addr, inviterID); err != nil {
				http.Error(w, fmt.Sprintf("failed to create pending invite: %v", err), http.StatusInternalServerError)
				return
			}
			s.slog().InfoContext(ctx, "created pending team invite via debug", "team_id", teamID, "email", addr)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "invited %s to team %s (pending signup)", addr, teamID)
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
	if role != "owner" && role != "user" {
		http.Error(w, "role must be 'owner' or 'user'", http.StatusBadRequest)
		return
	}

	err := withTx1(s, ctx, (*exedb.Queries).InsertTeamMember, exedb.InsertTeamMemberParams{
		TeamID: teamID,
		UserID: userID,
		Role:   role,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to add team member: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "added team member via debug", "team_id", teamID, "user_id", userID, "role", role)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "added %s to team %s as %s", userID, teamID, role)
}

// handleDebugTeamMembers lists members of a team.
// GET /debug/teams/members?team_id=xxx
func (s *Server) handleDebugTeamMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID := r.URL.Query().Get("team_id")
	if teamID == "" {
		http.Error(w, "team_id is required", http.StatusBadRequest)
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
			UserID   string `json:"user_id"`
			Email    string `json:"email"`
			Role     string `json:"role"`
			JoinedAt string `json:"joined_at"`
		}
		type teamInfo struct {
			TeamID      string       `json:"team_id"`
			DisplayName string       `json:"display_name"`
			CreatedAt   string       `json:"created_at"`
			MemberCount int64        `json:"member_count"`
			Limits      string       `json:"limits,omitempty"`
			Members     []memberInfo `json:"members"`
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
			// Fetch limits from full team record
			if team, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeam, t.TeamID); err == nil {
				ti.Limits = ptrStr(team.Limits)
			}
			// Fetch members
			if members, err := withRxRes1(s, ctx, (*exedb.Queries).GetTeamMembers, t.TeamID); err == nil {
				for _, m := range members {
					ti.Members = append(ti.Members, memberInfo{
						UserID:   m.UserID,
						Email:    m.Email,
						Role:     m.Role,
						JoinedAt: m.JoinedAt,
					})
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
	tmpl, err := debug_templates.Parse()
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to parse templates: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		TeamCount int
	}{
		TeamCount: len(teams),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "teams.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute teams template", "error", err)
	}
}

// handleDebugTeamRemoveMember removes a member from a team, deleting their boxes.
// POST /debug/teams/remove-member with team_id, user_id
func (s *Server) handleDebugTeamRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID := r.FormValue("team_id")
	userID := r.FormValue("user_id")

	if teamID == "" || userID == "" {
		http.Error(w, "team_id and user_id are required", http.StatusBadRequest)
		return
	}

	// Delete the member's boxes (full cascade like SSH command)
	boxIDs, err := withRxRes1(s, ctx, (*exedb.Queries).ListBoxIDsForUser, userID)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to list boxes for removed member", "error", err)
	}
	for _, boxID := range boxIDs {
		box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByID, boxID)
		if err != nil {
			continue
		}
		if err := s.deleteBox(ctx, box); err != nil {
			s.slog().ErrorContext(ctx, "failed to delete box for removed member",
				"box_id", boxID, "user_id", userID, "error", err)
		}
	}

	if err := s.deleteTeamMember(ctx, teamID, userID); err != nil {
		http.Error(w, fmt.Sprintf("failed to remove member: %v", err), http.StatusInternalServerError)
		return
	}

	s.slog().InfoContext(ctx, "removed team member via debug", "team_id", teamID, "user_id", userID, "boxes_deleted", len(boxIDs))
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "removed %s from team %s (%d boxes deleted)", userID, teamID, len(boxIDs))
}

// handleDebugTeamUpdateRole changes a team member's role.
// POST /debug/teams/update-role with team_id, user_id, role
func (s *Server) handleDebugTeamUpdateRole(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	teamID := r.FormValue("team_id")
	userID := r.FormValue("user_id")
	role := r.FormValue("role")

	if teamID == "" || userID == "" || role == "" {
		http.Error(w, "team_id, user_id, and role are required", http.StatusBadRequest)
		return
	}
	if role != "owner" && role != "user" {
		http.Error(w, "role must be 'owner' or 'user'", http.StatusBadRequest)
		return
	}

	err := withTx1(s, ctx, (*exedb.Queries).UpdateTeamMemberRole, exedb.UpdateTeamMemberRoleParams{
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

	teamID := r.FormValue("team_id")
	limits := r.FormValue("limits")

	if teamID == "" {
		http.Error(w, "team_id is required", http.StatusBadRequest)
		return
	}

	var limitsPtr *string
	if limits != "" {
		limitsPtr = &limits
	}

	err := withTx1(s, ctx, (*exedb.Queries).UpdateTeamLimits, exedb.UpdateTeamLimitsParams{
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
