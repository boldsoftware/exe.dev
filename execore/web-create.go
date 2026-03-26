package execore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"exe.dev/billing/entitlement"

	"exe.dev/boxname"
	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/stage"
)

// creationStreamIdleTimeout is how long to keep a creation stream after last access
const creationStreamIdleTimeout = 10 * time.Minute

// creationStreamKey uniquely identifies a creation stream
//
//exe:completeinit
type creationStreamKey struct {
	userID   string
	hostname string
}

// CreationStream holds the output stream for a box being created
type CreationStream struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	logBuf     bytes.Buffer // Complete log for database storage
	done       bool
	err        error
	waiters    []chan struct{}
	hostname   string
	lastAccess time.Time
	key        creationStreamKey
	server     *Server
}

// Write implements io.Writer
func (cs *CreationStream) Write(p []byte) (n int, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.lastAccess = time.Now()
	n, err = cs.buf.Write(p)
	// Also write to log buffer for database storage
	cs.logBuf.Write(p)
	// Notify all waiters that new data is available
	for _, ch := range cs.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return n, err
}

// MarkDone marks the stream as complete
func (cs *CreationStream) MarkDone(err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.done = true
	cs.err = err
	cs.lastAccess = time.Now()
	// Notify all waiters
	for _, ch := range cs.waiters {
		close(ch)
	}
	cs.waiters = nil
}

// startCleanupTimer starts a goroutine that removes this stream after idle timeout
func (cs *CreationStream) startCleanupTimer() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cs.mu.Lock()
			idle := time.Since(cs.lastAccess)
			done := cs.done
			cs.mu.Unlock()

			// Clean up if done and idle for long enough.
			// Check pointer identity so we don't remove a replacement stream.
			if done && idle > creationStreamIdleTimeout {
				cs.server.removeCreationStreamIfMatch(cs.key.userID, cs.key.hostname, cs)
				return
			}
		}
	}()
}

// Read reads available data and waits for more if not done
func (cs *CreationStream) Read(p []byte) (n int, err error) {
	cs.mu.Lock()
	cs.lastAccess = time.Now()
	n, err = cs.buf.Read(p)
	if n > 0 || (err != nil && err != io.EOF) {
		cs.mu.Unlock()
		return n, err
	}
	if cs.done {
		err := cs.err
		cs.mu.Unlock()
		if err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	// No data available and not done - wait
	waitCh := make(chan struct{}, 1)
	cs.waiters = append(cs.waiters, waitCh)
	cs.mu.Unlock()

	<-waitCh
	return 0, nil // Signal to retry
}

// getOrCreateCreationStream gets or creates a creation stream for a user's hostname
func (s *Server) getOrCreateCreationStream(userID, hostname string) *CreationStream {
	s.creationStreamsMu.Lock()
	defer s.creationStreamsMu.Unlock()
	key := creationStreamKey{userID: userID, hostname: hostname}
	if cs, ok := s.creationStreams[key]; ok {
		return cs
	}
	cs := &CreationStream{
		hostname:   hostname,
		key:        key,
		server:     s,
		lastAccess: time.Now(),
	}
	s.creationStreams[key] = cs
	cs.startCleanupTimer()
	return cs
}

// getCreationStream gets an existing creation stream
func (s *Server) getCreationStream(userID, hostname string) *CreationStream {
	s.creationStreamsMu.Lock()
	defer s.creationStreamsMu.Unlock()
	return s.creationStreams[creationStreamKey{userID: userID, hostname: hostname}]
}

// removeCreationStreamIfMatch removes the creation stream only if it matches the given pointer.
// This prevents a cleanup timer from removing a replacement stream created after the original.
func (s *Server) removeCreationStreamIfMatch(userID, hostname string, cs *CreationStream) {
	s.creationStreamsMu.Lock()
	defer s.creationStreamsMu.Unlock()
	key := creationStreamKey{userID: userID, hostname: hostname}
	if s.creationStreams[key] == cs {
		delete(s.creationStreams, key)
	}
}

// getActiveCreationHostnames returns the hostnames of active (non-done) creation streams for a user.
// Lock ordering note: this acquires cs.mu while holding creationStreamsMu.
// That is safe because startBoxCreation releases creationStreamsMu (via getCreationStream)
// before acquiring cs.mu, so the two locks are never held in opposing order.
func (s *Server) getActiveCreationHostnames(userID string) []string {
	s.creationStreamsMu.Lock()
	defer s.creationStreamsMu.Unlock()
	var hostnames []string
	for key, cs := range s.creationStreams {
		if key.userID != userID || cs == nil {
			continue
		}
		cs.mu.Lock()
		done := cs.done
		cs.mu.Unlock()
		if !done {
			hostnames = append(hostnames, key.hostname)
		}
	}
	return hostnames
}

// startBoxCreation starts creating a box in the background
func (s *Server) startBoxCreation(ctx context.Context, hostname, prompt, image, userID string) {
	// Check if already creating
	if cs := s.getCreationStream(userID, hostname); cs != nil {
		cs.mu.Lock()
		done := cs.done
		cs.mu.Unlock()
		if !done {
			s.slog().InfoContext(ctx, "Box creation already in progress", "hostname", hostname, "user_id", userID)
			return
		}
		// Previous creation stream finished; remove it so we can start fresh.
		s.removeCreationStreamIfMatch(userID, hostname, cs)
	}

	// Create the stream first so errors can be written to it
	cs := s.getOrCreateCreationStream(userID, hostname)

	// Check if hostname is available for this user
	if !s.isBoxNameAvailableForUser(ctx, hostname, userID) {
		s.slog().ErrorContext(ctx, "Box name not available", "hostname", hostname)
		cs.MarkDone(fmt.Errorf("VM name %q is not available", hostname))
		return
	}

	// Start creation in background
	go func() {
		// Create a context for the creation (separate from request context)
		createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), longOperationTimeout)
		defer cancel()

		// Set up the command context
		ss := NewSSHServer(s)
		fs := newCommandFlags()
		_ = fs.Set("name", hostname)
		if prompt != "" {
			_ = fs.Set("prompt", prompt)
		}
		if image != "" {
			_ = fs.Set("image", image)
		}
		userEmail, _ := withRxRes1(s, createCtx, (*exedb.Queries).GetEmailByUserID, userID)

		cc := &exemenu.CommandContext{
			User:         &exemenu.UserInfo{ID: userID, Email: userEmail},
			FlagSet:      fs,
			Output:       cs,
			Logger:       s.slog(),
			ForceSpinner: true,
		}

		// Run the creation through executeCommandWithLogging so
		// the canonical "ssh command completed" line is emitted.
		cl := NewCommandLog(time.Now())
		createCtx = WithCommandLog(createCtx, cl)
		err := ss.handleNewCommand(createCtx, cc)

		// Emit the canonical log line for the web-initiated creation.
		logAttrs := []any{
			"log_type", "ssh_command",
			"command", "new --name " + hostname,
			"image", image,
			"source", "web",
			"duration", cl.Duration(),
			"user_id", userID,
		}
		for _, attr := range cl.Attrs() {
			logAttrs = append(logAttrs, attr.Key, attr.Value.Any())
		}
		if err != nil {
			logAttrs = append(logAttrs, "rc", 1, "error", err.Error())
		} else {
			logAttrs = append(logAttrs, "rc", 0)
		}
		s.slog().InfoContext(createCtx, "ssh command completed", logAttrs...)

		// Shelley can run for a long time.
		// When this happens, createCtx may be exhausted.
		// Make a fresh context for this cleanup work.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()

		cs.mu.Lock()
		creationLog := cs.logBuf.String()
		cs.mu.Unlock()

		if saveErr := withTx1(s, cleanupCtx, (*exedb.Queries).UpdateBoxCreationLog, exedb.UpdateBoxCreationLogParams{
			CreationLog: &creationLog,
			Name:        hostname,
		}); saveErr != nil {
			s.slog().ErrorContext(ctx, "Failed to save creation log", "error", saveErr, "hostname", hostname)
		}

		if err != nil {
			log := s.slog().ErrorContext
			if exemenu.IsCommandClientError(err) {
				// "normal" error with designed UX
				log = s.slog().InfoContext
			}
			log(ctx, "Box creation failed", "hostname", hostname, "email", userEmail, "error", err)
			cs.MarkDone(err)
			return
		}

		// Clean up pending VM entry if it exists
		if err := withTx1(s, cleanupCtx, (*exedb.Queries).DeleteMobilePendingVMByUserAndHostname, exedb.DeleteMobilePendingVMByUserAndHostnameParams{
			UserID:   userID,
			Hostname: hostname,
		}); err != nil {
			s.slog().ErrorContext(ctx, "Failed to delete pending VM", "error", err, "user_id", userID, "hostname", hostname)
		}

		cs.MarkDone(nil)
		s.slog().InfoContext(ctx, "Box creation completed", "hostname", hostname)
	}()
}

// handleAPICheckoutParams returns the stored VM creation parameters for a checkout params token.
func (s *Server) handleAPICheckoutParams(w http.ResponseWriter, r *http.Request, userID string) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Missing token parameter", http.StatusBadRequest)
		return
	}

	cp, err := withRxRes1(s, r.Context(), (*exedb.Queries).GetCheckoutParams, exedb.GetCheckoutParamsParams{
		Token:  token,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Checkout params not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSONOK(w, map[string]string{
		"name":   cp.VMName,
		"prompt": cp.VMPrompt,
		"image":  cp.VMImage,
	})
}

// handleHostnameCheck checks if a hostname is available
func (s *Server) handleHostnameCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		Hostname string `json:"hostname"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	name := strings.ToLower(strings.TrimSpace(request.Hostname))
	if name == "" {
		http.Error(w, "VM name is required", http.StatusBadRequest)
		return
	}

	// Check if hostname is valid and available
	validErr := boxname.Valid(name)
	isAvailable := true

	if validErr == nil {
		userID, _ := s.validateAuthCookie(r)
		isAvailable = s.isBoxNameAvailableForUser(r.Context(), name, userID)
	}

	response := struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}{
		Valid:     validErr == nil,
		Available: isAvailable,
	}

	switch {
	case validErr != nil:
		response.Message = validErr.Error()
	case !isAvailable:
		response.Message = "That VM name is not available."
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleCreateVM handles VM creation request
func (s *Server) handleCreateVM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	image := strings.TrimSpace(r.FormValue("image"))
	inviteCodeStr := strings.TrimSpace(r.FormValue("invite"))

	s.slog().InfoContext(r.Context(), "Web VM creation request", "hostname", hostname, "prompt", prompt, "image", image)

	// If user is logged in, check entitlements before proceeding
	if userID, err := s.validateAuthCookie(r); err == nil {
		// Check if user's plan grants VM creation
		if !s.UserHasEntitlement(r.Context(), entitlement.SourceWeb, entitlement.VMCreate, userID) {
			billingURL := "/billing/update?name=" + url.QueryEscape(hostname)
			if prompt != "" {
				billingURL += "&prompt=" + url.QueryEscape(prompt)
			}
			if image != "" {
				billingURL += "&image=" + url.QueryEscape(image)
			}
			http.Redirect(w, r, billingURL, http.StatusSeeOther)
			return
		}

		// Check if user has reached their VM limit before starting async creation
		boxCount, err := s.CountBoxesForLimitCheck(r.Context(), userID)
		if err == nil {
			effectiveLimits, _ := s.GetEffectiveLimits(r.Context(), userID)
			team, _ := s.GetTeamForUser(r.Context(), userID)
			var maxBoxes int
			if team != nil {
				maxBoxes = GetMaxTeamBoxes(effectiveLimits)
			} else {
				maxBoxes = GetMaxBoxes(effectiveLimits)
			}
			if int(boxCount) >= maxBoxes {
				redirectURL := "/new?name=" + url.QueryEscape(hostname) + "&error=vm_limit"
				if prompt != "" {
					redirectURL += "&prompt=" + url.QueryEscape(prompt)
				}
				if image != "" {
					redirectURL += "&image=" + url.QueryEscape(image)
				}
				http.Redirect(w, r, redirectURL, http.StatusSeeOther)
				return
			}
		}

		// Increment deploy count if this VM was created from an idea template
		if ideaSlug := strings.TrimSpace(r.FormValue("idea_slug")); ideaSlug != "" {
			if err := withTx1(s, r.Context(), (*exedb.Queries).IncrementTemplateDeployCount, ideaSlug); err != nil {
				s.slog().ErrorContext(r.Context(), "Failed to increment template deploy count", "slug", ideaSlug, "error", err)
			}
		}

		// Start box creation in background
		s.startBoxCreation(r.Context(), hostname, prompt, image, userID)
		http.Redirect(w, r, "/?filter="+urlQueryEscape(hostname), http.StatusSeeOther)
		return
	}

	// Validate invite code if provided
	var inviteCodeValid, inviteCodeInvalid bool
	var invitePlanType string
	if inviteCodeStr != "" {
		if invite := s.lookupUnusedInviteCode(r.Context(), inviteCodeStr); invite != nil {
			inviteCodeValid = true
			invitePlanType = invite.PlanType
		} else {
			inviteCodeInvalid = true
		}
	}

	// Otherwise, proceed to email auth, carrying the VM details as hidden fields
	data := authFormData{
		Env:               s.env,
		SSHCommand:        s.replSSHConnectionCommand(),
		BoxName:           hostname,
		Prompt:            prompt,
		Image:             image,
		InviteCode:        inviteCodeStr,
		InviteCodeValid:   inviteCodeValid,
		InviteCodeInvalid: inviteCodeInvalid,
		InvitePlanType:    invitePlanType,
	}

	s.renderTemplate(r.Context(), w, "auth-form.html", data)
}

type authFormData struct {
	stage.Env
	RedirectURL       string
	ReturnHost        string
	LoginWithExe      bool
	SSHCommand        string
	BoxName           string
	Prompt            string
	Image             string
	InviteCode        string
	InviteCodeValid   bool   // true if invite code is valid and unused
	InviteCodeInvalid bool   // true if invite code was provided but is invalid or already used
	InvitePlanType    string // "free" or "trial" if valid
	TeamInvite        string // team invite token (passed as hidden field)
	TeamInviteName    string // team display name (shown in UI)
	TeamInviteEmail   string // email from the invite (pre-fills input)
	ResponseMode      string // app_token for iOS auth flow
	CallbackURI       string // custom scheme callback URI for app_token flow
}

// sseEvent writes a named SSE event with data
func sseEvent(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// handleCreatingStream streams progress from an in-memory creation stream
func (s *Server) handleCreatingStream(w http.ResponseWriter, r *http.Request) {
	// Require auth
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Read hostname from query parameter
	hostname := strings.TrimSpace(r.URL.Query().Get("hostname"))
	if hostname == "" || !boxname.IsValid(hostname) {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Look up the creation stream
	cs := s.getCreationStream(userID, hostname)
	if cs == nil {
		// No stream, just return empty - it's fine
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		return
	}

	// Set up SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Read and stream the creation output as raw bytes
	buf := make([]byte, 4096)
	for {
		n, err := cs.Read(buf)
		if n > 0 {
			// Send base64-encoded terminal output as SSE data event
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			fmt.Fprintf(w, "data: %s\n\n", encoded)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err == io.EOF {
			// Creation completed successfully
			httpsURL := s.boxProxyAddress(hostname)
			sseEvent(w, "done", fmt.Sprintf("%s|%s/", httpsURL, s.xtermURL(hostname, r.TLS != nil)))
			return
		}
		if err != nil {
			// Creation failed
			sseEvent(w, "fail", err.Error())
			return
		}
		// No data yet, read will have waited and returned 0, nil - try again
	}
}

// handleBoxCreationLog returns the stored creation log for a box
func (s *Server) handleBoxCreationLog(w http.ResponseWriter, r *http.Request) {
	// Require auth
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Read hostname from query parameter
	hostname := strings.TrimSpace(r.URL.Query().Get("hostname"))
	if hostname == "" || !boxname.IsValid(hostname) {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Get the box
	box, err := s.getBoxForUserByUserID(r.Context(), userID, hostname)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Return the creation log (raw terminal output)
	w.Header().Set("Content-Type", "application/octet-stream")
	if box.CreationLog != nil && *box.CreationLog != "" {
		w.Write([]byte(*box.CreationLog))
	} else {
		w.Write([]byte(""))
	}
}

// getBoxForUserByUserID fetches a box for a user by userID and name
func (s *Server) getBoxForUserByUserID(ctx context.Context, userID, boxName string) (*exedb.Box, error) {
	b, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{Name: boxName, CreatedByUserID: userID})
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// urlQueryEscape escapes a string for URL query
func urlQueryEscape(sv string) string {
	return strings.ReplaceAll(strings.ReplaceAll(sv, " ", "+"), "\n", " ")
}
