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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"exe.dev/email"
	"exe.dev/execore/debug_templates"
	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	"exe.dev/llmgateway"
	"exe.dev/logging"
	computeapi "exe.dev/pkg/api/exe/compute/v1"

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
	mux.HandleFunc("/debug/boxes/{name}", s.handleDebugBoxDetails)
	mux.HandleFunc("POST /debug/boxes/delete", s.handleDebugBoxDelete)
	mux.HandleFunc("GET /debug/boxes/migrate", s.handleDebugBoxMigrateForm)
	mux.HandleFunc("POST /debug/boxes/migrate", s.handleDebugBoxMigrate)
	mux.HandleFunc("/debug/users", s.handleDebugUsers)
	mux.HandleFunc("/debug/user", s.handleDebugUser)
	mux.HandleFunc("POST /debug/users/toggle-root-support", s.handleDebugToggleRootSupport)
	mux.HandleFunc("POST /debug/users/toggle-vm-creation", s.handleDebugToggleVMCreation)
	mux.HandleFunc("POST /debug/users/update-credit", s.handleDebugUpdateUserCredit)
	mux.HandleFunc("/debug/exelets", s.handleDebugExelets)
	mux.HandleFunc("POST /debug/exelets/set-preferred", s.handleDebugSetPreferredExelet)
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
	mux.HandleFunc("GET /debug/log", s.handleDebugLogForm)
	mux.HandleFunc("POST /debug/log", s.handleDebugLog)
	mux.HandleFunc("/debug/testimonials", s.handleDebugTestimonials)
	mux.HandleFunc("GET /debug/email", s.handleDebugEmailForm)
	mux.HandleFunc("POST /debug/email", s.handleDebugEmailSend)
	mux.HandleFunc("/debug/invite", s.handleDebugInvite)
	mux.HandleFunc("POST /debug/invite", s.handleDebugInvitePost)
	mux.HandleFunc("/debug/all-invite-codes", s.handleDebugAllInviteCodes)
	mux.HandleFunc("/debug/invite-tree", s.handleDebugInviteTree)

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
		GitCommit  string
		GitHubLink template.HTML
	}{
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
		Host        string `json:"host"`
		ID          string `json:"id,omitempty"`
		Name        string `json:"name"`
		Status      string `json:"status"`
		OwnerUserID string `json:"owner_user_id,omitempty"`
		OwnerEmail  string `json:"owner_email,omitempty"`
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
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return exedb.GetBoxOwnerByContainerIDRow{}, fmt.Errorf("container %q not present in database", containerID)
				}
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
				}
				if owner, err := getOwner(ctx, inst.ID); err == nil {
					info.OwnerUserID = owner.UserID
					info.OwnerEmail = owner.Email
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
				Host:        b.Ctrhost,
				Name:        b.Name,
				Status:      b.Status,
				OwnerUserID: b.OwnerUserID,
				OwnerEmail:  b.OwnerEmail,
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
		s.slog().ErrorContext(ctx, "Failed to encode boxes", "error", err)
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
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
			return
		}
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
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError("box %q not found", boxName)
			return
		}
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

	// Step 1: Stop VM on source
	writeProgress("Stopping VM on source exelet...")
	s.slog().InfoContext(ctx, "stopping VM for migration", "box", boxName, "container_id", containerID, "source", box.Ctrhost)
	if _, err := sourceClient.client.StopInstance(ctx, &computeapi.StopInstanceRequest{ID: containerID}); err != nil {
		writeError("failed to stop VM on source: %v", err)
		return
	}
	writeProgress("VM stopped.")

	// Step 2: Perform migration
	writeProgress("Starting disk transfer from %s to %s...", box.Ctrhost, targetAddr)
	s.slog().InfoContext(ctx, "starting migration", "box", boxName, "source", box.Ctrhost, "target", targetAddr)
	if err := s.migrateVM(ctx, sourceClient.client, targetClient.client, containerID, writeProgress); err != nil {
		writeError("migration failed: %v", err)
		return
	}
	writeProgress("Disk transfer complete.")

	// Step 3: Start VM on target
	writeProgress("Starting VM on target exelet...")
	s.slog().InfoContext(ctx, "starting VM on target", "box", boxName, "target", targetAddr)
	if _, err := targetClient.client.StartInstance(ctx, &computeapi.StartInstanceRequest{ID: containerID}); err != nil {
		writeError("migration succeeded but failed to start VM on target: %v", err)
		return
	}
	writeProgress("VM started on target.")

	// Step 4: Get new SSH port from target
	writeProgress("Getting new SSH port...")
	instance, err := targetClient.client.GetInstance(ctx, &computeapi.GetInstanceRequest{ID: containerID})
	if err != nil {
		writeError("migration succeeded but failed to get instance info: %v", err)
		return
	}
	newSSHPort := int64(instance.Instance.SSHPort)
	writeProgress("New SSH port: %d", newSSHPort)

	// Step 5: Update database with new ctrhost, ssh_port, and status
	writeProgress("Updating database...")
	if err := withTx1(s, ctx, (*exedb.Queries).UpdateBoxMigration, exedb.UpdateBoxMigrationParams{
		Ctrhost: targetAddr,
		SSHPort: &newSSHPort,
		Status:  "running",
		ID:      box.ID,
	}); err != nil {
		writeError("migration succeeded but failed to update database: %v", err)
		return
	}
	writeProgress("Database updated.")

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

// migrateVM performs the SendVM/ReceiveVM streaming between source and target exelets.
// The progress callback is called periodically with status updates.
func (s *Server) migrateVM(ctx context.Context, source, target *exeletclient.Client, instanceID string, progress func(string, ...any)) error {
	// Start SendVM on source
	sendStream, err := source.SendVM(ctx)
	if err != nil {
		return fmt.Errorf("failed to start SendVM: %w", err)
	}

	progress("Requesting VM metadata from source...")

	// First, get metadata from source to learn the base image ID.
	// We tell sender target doesn't have base image initially - we'll handle this below.
	if err := sendStream.Send(&computeapi.SendVMRequest{
		Type: &computeapi.SendVMRequest_Start{
			Start: &computeapi.SendVMStartRequest{
				InstanceID:         instanceID,
				TargetHasBaseImage: true, // Tell sender to send full stream (see comment below)
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

	// NOTE: We always tell sender TargetHasBaseImage=true above, which makes it send
	// a full stream of the instance. This is because ZFS incremental streams require
	// the exact origin snapshot (with matching GUID) to exist on target. Even if target
	// has the base image, it won't have the same origin snapshot GUID. Sending a full
	// stream is less space-efficient but works reliably.
	//
	// If target doesn't have base image at all, the full stream will still work - it
	// creates an independent dataset. The base image can be transferred separately
	// if needed for future clones.

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
				return fmt.Errorf("failed to send to target: %w", err)
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
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
			return
		}
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
		SSHHost:              box.SSHHost(),
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
			CreatedForLoginWithExe bool    `json:"created_for_login_with_exe"`
			AccountID              string  `json:"account_id,omitempty"`
			BillingURL             string  `json:"billing_url,omitempty"`
			CreditAvailableUSD     float64 `json:"credit_available_usd"`
			CreditEffectiveUSD     float64 `json:"credit_effective_usd"`
			CreditMaxUSD           float64 `json:"credit_max_usd"`
			CreditRefreshPerHrUSD  float64 `json:"credit_refresh_per_hr_usd"`
			CreditTotalUsedUSD     float64 `json:"credit_total_used_usd"`
			CreditLastRefreshAt    string  `json:"credit_last_refresh_at,omitempty"`
			DiscordID              string  `json:"discord_id,omitempty"`
			DiscordUsername        string  `json:"discord_username,omitempty"`
			InviteCount            int64   `json:"invite_count"`
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
				billingURL = s.billing.DashboardURL(acctID)
			}
			ui := userInfo{
				UserID:                 u.UserID,
				Email:                  u.Email,
				CreatedAt:              createdAt,
				RootSupport:            u.RootSupport == 1,
				VMCreationDisabled:     u.NewVmCreationDisabled,
				CreatedForLoginWithExe: u.CreatedForLoginWithExe,
				AccountID:              acctID,
				BillingURL:             billingURL,
				DiscordID:              ptrStr(u.DiscordID),
				DiscordUsername:        ptrStr(u.DiscordUsername),
				InviteCount:            invitesByUser[u.UserID],
			}
			if credit, ok := creditByUser[u.UserID]; ok {
				ui.CreditAvailableUSD = credit.AvailableCredit
				ui.CreditMaxUSD = credit.MaxCredit
				ui.CreditRefreshPerHrUSD = credit.RefreshPerHour
				ui.CreditTotalUsedUSD = credit.TotalUsed
				ui.CreditLastRefreshAt = credit.LastRefreshAt.Format(time.RFC3339)
				ui.CreditEffectiveUSD, _ = llmgateway.CalculateRefreshedCredit(
					credit.AvailableCredit,
					credit.MaxCredit,
					credit.RefreshPerHour,
					credit.LastRefreshAt,
					time.Now(),
				)
			}
			usersJSON = append(usersJSON, ui)
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(usersJSON); err != nil {
			s.slog().ErrorContext(ctx, "Failed to encode users", "error", err)
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

// handleDebugUpdateUserCredit updates a user's gateway credit settings.
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

	var availableUSD, maxUSD, refreshUSD float64
	var err error
	if availableStr != "" {
		availableUSD, err = strconv.ParseFloat(availableStr, 64)
		if err != nil {
			http.Error(w, "invalid available value", http.StatusBadRequest)
			return
		}
	}
	if maxStr != "" {
		maxUSD, err = strconv.ParseFloat(maxStr, 64)
		if err != nil {
			http.Error(w, "invalid max value", http.StatusBadRequest)
			return
		}
	}
	if refreshStr != "" {
		refreshUSD, err = strconv.ParseFloat(refreshStr, 64)
		if err != nil {
			http.Error(w, "invalid refresh value", http.StatusBadRequest)
			return
		}
	}

	// Upsert the credit record
	err = s.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		// First ensure record exists
		if err := q.CreateUserLLMCreditIfNotExists(ctx, userID); err != nil {
			return err
		}
		// Then update settings
		return q.UpdateUserLLMCreditSettings(ctx, exedb.UpdateUserLLMCreditSettingsParams{
			AvailableCredit: availableUSD,
			MaxCredit:       maxUSD,
			RefreshPerHour:  refreshUSD,
			UserID:          userID,
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
		Arch          string `json:"arch"`
		Status        string `json:"status"`
		IsPreferred   bool   `json:"is_preferred"`
		InstanceCount int    `json:"instance_count"`
		Error         string `json:"error,omitempty"`
	}

	// Get the preferred exelet setting
	preferredAddr, _ := withRxRes0(s, ctx, (*exedb.Queries).GetPreferredExelet)

	var exelets []exeletInfo

	// Gather info from all exelet clients
	for addr, ec := range s.exeletClients {
		info := exeletInfo{
			Address:     addr,
			Version:     ec.client.Version(),
			Arch:        ec.client.Arch(),
			IsPreferred: addr == preferredAddr,
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
			info.Arch = resp.Arch
		}

		// Count instances
		listCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if count, err := ec.countInstances(listCtx); err == nil {
			info.InstanceCount = count
		}
		cancel()

		exelets = append(exelets, info)
	}

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
			s.slog().ErrorContext(ctx, "Failed to encode exelets", "error", err)
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
		isPaying, err := withRxRes1(s, ctx, (*exedb.Queries).UserIsPaying, userID)
		if err == nil && isPaying {
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
			s.slog().ErrorContext(ctx, "Failed to encode throttle config", "error", err)
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

func checkedAttr(checked bool) string {
	if checked {
		return "checked"
	}
	return ""
}

// handleDebugNewThrottlePost handles saving the new-throttle configuration.
func (s *Server) handleDebugNewThrottlePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	enabled := r.FormValue("enabled") == "true"
	emailPatternsStr := r.FormValue("email_patterns")
	message := r.FormValue("message")

	// Parse email patterns (one per line)
	var emailPatterns []string
	for _, line := range strings.Split(emailPatternsStr, "\n") {
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

// handleDebugIPShards displays the IP shard assignments.
func (s *Server) handleDebugIPShards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type ipShardInfo struct {
		Shard     int    `json:"shard"`
		PublicIP  string `json:"public_ip"`
		PrivateIP string `json:"private_ip,omitempty"`
		Missing   bool   `json:"missing,omitempty"`
	}

	// Get all shards from DB
	dbShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListIPShards)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list IP shards: %v", err), http.StatusInternalServerError)
		return
	}

	// Build a reverse map: shard -> private IP from the server's PublicIPs
	shardToPrivateIP := make(map[int]string)
	for privateIP, info := range s.PublicIPs {
		shardToPrivateIP[info.Shard] = privateIP.String()
	}

	// Build the shard info list
	var shards []ipShardInfo
	for _, dbShard := range dbShards {
		info := ipShardInfo{
			Shard:    int(dbShard.Shard),
			PublicIP: dbShard.PublicIp,
		}
		if privateIP, ok := shardToPrivateIP[int(dbShard.Shard)]; ok {
			info.PrivateIP = privateIP
		} else {
			info.Missing = true
		}
		shards = append(shards, info)
	}

	// Find unmapped IPs (on this machine but not in DB)
	var unmappedIPs []string
	for privateIP, info := range s.PublicIPs {
		// Check if this shard exists in DB
		found := false
		for _, dbShard := range dbShards {
			if int(dbShard.Shard) == info.Shard {
				found = true
				break
			}
		}
		if !found {
			unmappedIPs = append(unmappedIPs, fmt.Sprintf("%s (public: %s, s%03d)", privateIP, info.IP, info.Shard))
		}
	}
	sort.Strings(unmappedIPs)

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		type jsonResponse struct {
			Shards     []ipShardInfo `json:"shards"`
			UnmappedIP []string      `json:"unmapped_ips,omitempty"`
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(jsonResponse{Shards: shards, UnmappedIP: unmappedIPs}); err != nil {
			s.slog().ErrorContext(ctx, "Failed to encode IP shards", "error", err)
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
		Shards         []ipShardInfo
		UnmappedIPs    []string
		UnmappedIPsStr string
	}{
		Shards:         shards,
		UnmappedIPs:    unmappedIPs,
		UnmappedIPsStr: strings.Join(unmappedIPs, ", "),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "ipshards.html", data); err != nil {
		s.slog().ErrorContext(ctx, "failed to execute ipshards template", "error", err)
	}
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

// IsLoginCreationDisabled returns true if new account creation is disabled.
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

func disabledAttr(disabled bool) string {
	if disabled {
		return "disabled"
	}
	return ""
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

// IsSignupPOWEnabled returns true if proof-of-work is required for new signups.
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

	// Create invite codes for the user
	for i := 0; i < count; i++ {
		// Generate a unique code
		code, err := withTxRes0(s, ctx, (*exedb.Queries).GenerateUniqueInviteCode)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to generate invite code: %v", err), http.StatusInternalServerError)
			return
		}

		// Create the invite code assigned to the user
		_, err = withTxRes1(s, ctx, (*exedb.Queries).CreateInviteCode, exedb.CreateInviteCodeParams{
			Code:             code,
			PlanType:         planType,
			AssignedToUserID: &user.UserID,
			AssignedBy:       assignedBy,
			AssignedFor:      nil,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to create invite code: %v", err), http.StatusInternalServerError)
			return
		}

		s.slog().InfoContext(ctx, "invite code given to user via debug page",
			"code", code,
			"plan_type", planType,
			"assigned_by", assignedBy,
			"user_email", userEmail,
			"user_id", user.UserID)
	}

	// Send email notification
	var planDesc string
	if planType == "trial" {
		planDesc = "1 month free trial"
	} else {
		planDesc = "free"
	}
	codeWord := "codes"
	if count == 1 {
		codeWord = "code"
	}
	subject := fmt.Sprintf("%s: you have invite %s to share", s.env.WebHost, codeWord)
	body := fmt.Sprintf(`Hi,

You have been given %d invite %s to share with friends.

Each invite code grants the recipient a %s plan.

To view and share your invite %s, visit:
https://%s/invite

---
%s
`, count, codeWord, planDesc, codeWord, s.env.WebHost, s.env.WebHost)

	if err := s.sendEmail(ctx, email.TypeInvitesAllocated, userEmail, subject, body); err != nil {
		s.slog().WarnContext(ctx, "failed to send invites allocated email", "to", userEmail, "error", err)
	}

	http.Redirect(w, r, "/debug/invite", http.StatusSeeOther)
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
			s.slog().ErrorContext(ctx, "Failed to encode invite codes", "error", err)
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

// IsIPAbuseFilterDisabled returns true if the IP abuse filter is disabled.
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

	// Look up the user
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, fmt.Sprintf("user %q not found", userID), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("failed to look up user: %v", err), http.StatusInternalServerError)
		return
	}

	// Fetch account info
	accounts, err := withRxRes0(s, ctx, (*exedb.Queries).ListAllAccounts)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to list accounts", "error", err)
	}
	var accountID, billingURL string
	for _, a := range accounts {
		if a.CreatedBy == userID {
			accountID = a.ID
			billingURL = s.billing.DashboardURL(accountID)
			break
		}
	}

	// Fetch LLM credit info
	credit, creditErr := withRxRes1(s, ctx, (*exedb.Queries).GetUserLLMCredit, userID)
	hasCredit := creditErr == nil

	var creditEffective float64
	if hasCredit {
		creditEffective, _ = llmgateway.CalculateRefreshedCredit(
			credit.AvailableCredit,
			credit.MaxCredit,
			credit.RefreshPerHour,
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
		Email                  string
		UserID                 string
		CreatedAt              string
		CreatedForLoginWithExe bool
		RootSupport            bool
		VMCreationDisabled     bool
		DiscordID              string
		DiscordUsername        string
		BillingExemption       string
		BillingTrialEndsAt     string
		SignedUpWithInviteID   string
		AccountID              string
		BillingURL             string
		HasCredit              bool
		CreditAvailableUSD     float64
		CreditEffectiveUSD     float64
		CreditMaxUSD           float64
		CreditRefreshPerHrUSD  float64
		CreditTotalUsedUSD     float64
		CreditLastRefreshAt    string
		Boxes                  []boxInfo
	}{
		Email:                  user.Email,
		UserID:                 user.UserID,
		CreatedAt:              formatTime(user.CreatedAt),
		CreatedForLoginWithExe: user.CreatedForLoginWithExe,
		RootSupport:            user.RootSupport == 1,
		VMCreationDisabled:     user.NewVmCreationDisabled,
		DiscordID:              ptrStr(user.DiscordID),
		DiscordUsername:        ptrStr(user.DiscordUsername),
		BillingExemption:       ptrStr(user.BillingExemption),
		BillingTrialEndsAt:     formatTime(user.BillingTrialEndsAt),
		SignedUpWithInviteID:   formatInt64Ptr(user.SignedUpWithInviteID),
		AccountID:              accountID,
		BillingURL:             billingURL,
		HasCredit:              hasCredit,
		Boxes:                  boxList,
	}

	if hasCredit {
		data.CreditAvailableUSD = credit.AvailableCredit
		data.CreditEffectiveUSD = creditEffective
		data.CreditMaxUSD = credit.MaxCredit
		data.CreditRefreshPerHrUSD = credit.RefreshPerHour
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
