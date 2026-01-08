package execore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/pprof"
	"regexp"
	"sort"
	"strings"
	"time"

	"exe.dev/exedb"
	"exe.dev/logging"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
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
	mux.HandleFunc("/debug/users", s.handleDebugUsers)
	mux.HandleFunc("POST /debug/users/toggle-root-support", s.handleDebugToggleRootSupport)
	mux.HandleFunc("POST /debug/users/toggle-vm-creation", s.handleDebugToggleVMCreation)
	mux.HandleFunc("/debug/exelets", s.handleDebugExelets)
	mux.HandleFunc("POST /debug/exelets/set-preferred", s.handleDebugSetPreferredExelet)
	mux.HandleFunc("/debug/new-throttle", s.handleDebugNewThrottle)
	mux.HandleFunc("POST /debug/new-throttle", s.handleDebugNewThrottlePost)
	mux.HandleFunc("/debug/signup-limiter", s.handleDebugSignupLimiter)
	mux.HandleFunc("/debug/ipshards", s.handleDebugIPShards)
	mux.HandleFunc("GET /debug/log", s.handleDebugLogForm)
	mux.HandleFunc("POST /debug/log", s.handleDebugLog)
	mux.HandleFunc("/debug/testimonials", s.handleDebugTestimonials)

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>exed debug</title></head><body>
<h1>exed debug</h1>
<ul>
    <li><a href="/debug/pprof/">pprof</a></li>
    <li><a href="/debug/pprof/cmdline">pprof/cmdline</a></li>
    <li><a href="/debug/pprof/profile">pprof/profile</a></li>
    <li><a href="/debug/pprof/symbol">pprof/symbol</a></li>
    <li><a href="/debug/pprof/trace">pprof/trace</a></li>
    <li><a href="/debug/pprof/goroutine?debug=1">pprof/goroutine?debug=1</a></li>
    <li><a href="/metrics">metrics</a></li>
    <li><a href="/debug/gitsha">gitsha</a></li>
    <li><a href="/debug/boxes">boxes</a> (<a href="/debug/boxes?format=json">json</a>)</li>
    <li><a href="/debug/users">users</a> (<a href="/debug/users?format=json">json</a>)</li>
    <li><a href="/debug/exelets">exelets</a> (<a href="/debug/exelets?format=json">json</a>)</li>
    <li><a href="/debug/new-throttle">new-throttle</a> (<a href="/debug/new-throttle?format=json">json</a>)</li>
    <li><a href="/debug/signup-limiter">signup-limiter</a></li>
    <li><a href="/debug/ipshards">ipshards</a> (<a href="/debug/ipshards?format=json">json</a>)</li>
    <li><a href="/debug/log">/debug/log</a> (POST text=... to log an error)</li>
    <li><a href="/debug/testimonials">testimonials</a></li>
</ul>
<p>Git version: %s %s</p>
</body></html>
`, commit, gitHubLink(commit))
}

func (s *Server) handleDebugGitsha(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, logging.GitCommit())
}

// handleDebugBoxes returns the list of container hosts and their containers
func (s *Server) handleDebugBoxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	type containerInfo struct {
		Host       string `json:"host"`
		ID         string `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		OwnerEmail string `json:"owner_email,omitempty"`
	}

	type hostInfo struct {
		Host       string
		Containers []containerInfo
		Error      string
	}

	var hosts []hostInfo
	var flatContainers []containerInfo

	emailCache := make(map[string]string)
	getOwnerEmail := func(ctx context.Context, containerID string) (string, error) {
		if containerID == "" {
			return "", fmt.Errorf("empty container ID")
		}
		if email, ok := emailCache[containerID]; ok {
			return email, nil
		}
		email, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxOwnerEmailByContainerID, &containerID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("container %q not present in database", containerID)
			}
			return "", fmt.Errorf("failed to look up owner for container %q: %w", containerID, err)
		}
		emailCache[containerID] = email
		return email, nil
	}

	// List instances from exelet hosts
	for addr, ec := range s.exeletClients {
		info := hostInfo{Host: addr}
		stream, err := ec.client.ListInstances(ctx, &computeapi.ListInstancesRequest{})
		if err != nil {
			info.Error = err.Error()
		} else {
			// Collect all instances from the stream
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					info.Error = err.Error()
					break
				}
				inst := resp.Instance
				cInfo := containerInfo{
					Host:   addr,
					ID:     inst.ID,
					Name:   inst.Name,
					Status: inst.State.String(),
				}
				if ownerEmail, err := getOwnerEmail(ctx, inst.ID); err == nil {
					cInfo.OwnerEmail = ownerEmail
				} else {
					s.slog().WarnContext(ctx, "failed to resolve box owner email", "boxName", inst.Name, "instanceID", inst.ID, "error", err)
				}
				info.Containers = append(info.Containers, cInfo)
				flatContainers = append(flatContainers, cInfo)
			}
		}
		hosts = append(hosts, info)
	}

	// Sort hosts lexicographically for consistent display
	sort.Slice(hosts, func(i, j int) bool {
		return hosts[i].Host < hosts[j].Host
	})

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(flatContainers); err != nil {
			s.slog().ErrorContext(ctx, "Failed to encode containers", "error", err)
		}
		return
	}

	// HTML output
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Boxes by Host</title>
<style>
.delete-btn { background: #dc3545; color: white; border: none; padding: 4px 8px; cursor: pointer; border-radius: 3px; }
.delete-btn:hover { background: #c82333; }
dialog { padding: 20px; border: 1px solid #ccc; border-radius: 5px; }
dialog::backdrop { background: rgba(0,0,0,0.5); }
dialog input[type="text"] { width: 100%%; padding: 8px; margin: 10px 0; box-sizing: border-box; }
dialog button { margin-right: 10px; padding: 8px 16px; }
dialog .confirm-btn { background: #dc3545; color: white; border: none; cursor: pointer; }
dialog .confirm-btn:disabled { background: #ccc; cursor: not-allowed; }
dialog .cancel-btn { background: #6c757d; color: white; border: none; cursor: pointer; }
</style>
</head><body>
<h1>Boxes by Host</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/boxes?format=json">json</a></p>
`)

	if len(hosts) == 0 {
		fmt.Fprintf(w, "<p>No container hosts configured.</p>\n")
	} else {
		for _, host := range hosts {
			fmt.Fprintf(w, "<h2>%s</h2>\n", html.EscapeString(host.Host))
			if host.Error != "" {
				fmt.Fprintf(w, "<p style='color: red;'>Error: %s</p>\n", html.EscapeString(host.Error))
			} else if len(host.Containers) == 0 {
				fmt.Fprintf(w, "<p>No containers running.</p>\n")
			} else {
				fmt.Fprintf(w, "<table border='1' cellpadding='5' cellspacing='0'>\n")
				fmt.Fprintf(w, "<tr><th>Name</th><th>ID</th><th>Status</th><th>Owner</th><th>Actions</th></tr>\n")
				for _, c := range host.Containers {
					fmt.Fprintf(w, "<tr><td><a href='/debug/boxes/%s'>%s</a></td><td>%s</td><td>%s</td><td>%s</td><td><button class='delete-btn' data-box='%s'>Delete</button></td></tr>\n",
						html.EscapeString(c.Name),
						html.EscapeString(c.Name),
						html.EscapeString(c.ID),
						html.EscapeString(c.Status),
						html.EscapeString(c.OwnerEmail),
						html.EscapeString(c.Name),
					)
				}
				fmt.Fprintf(w, "</table>\n")
			}
		}
	}

	fmt.Fprintf(w, `<dialog id="deleteDialog">
<form method="post" action="/debug/boxes/delete">
<p>To delete this box, type its name to confirm:</p>
<p><strong id="boxNameDisplay"></strong></p>
<input type="hidden" name="box_name" id="boxNameInput">
<input type="text" name="confirm_name" id="confirmInput" autocomplete="off" placeholder="Type box name to confirm">
<p>
<button type="submit" class="confirm-btn" id="confirmBtn" disabled>Delete</button>
<button type="button" class="cancel-btn" id="cancelBtn">Cancel</button>
</p>
</form>
</dialog>
<script>
document.addEventListener('click', function(e) {
    if (e.target.classList.contains('delete-btn')) {
        var boxName = e.target.dataset.box;
        document.getElementById('boxNameDisplay').textContent = boxName;
        document.getElementById('boxNameInput').value = boxName;
        document.getElementById('confirmInput').value = '';
        document.getElementById('confirmBtn').disabled = true;
        document.getElementById('deleteDialog').showModal();
    }
});
document.getElementById('cancelBtn').addEventListener('click', function() {
    document.getElementById('deleteDialog').close();
});
document.getElementById('confirmInput').addEventListener('input', function() {
    var expected = document.getElementById('boxNameInput').value;
    document.getElementById('confirmBtn').disabled = (this.value !== expected);
});
</script>
</body></html>
`)
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

	// Render HTML
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Box: %s</title>
<style>
table { border-collapse: collapse; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
.section { margin: 20px 0; }
h2 { border-bottom: 1px solid #ccc; padding-bottom: 5px; }
pre { background: #f5f5f5; padding: 10px; overflow-x: auto; }
</style>
</head><body>
<h1>Box: %s</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/boxes">/debug/boxes</a> </p>
`, html.EscapeString(box.Name), html.EscapeString(box.Name))

	// Basic info
	fmt.Fprintf(w, `<div class="section">
<h2>Basic Information</h2>
<table>
<tr><th>Name</th><td>%s</td></tr>
<tr><th>ID</th><td>%d</td></tr>
<tr><th>Status</th><td>%s</td></tr>
<tr><th>Image</th><td>%s</td></tr>
<tr><th>Container Host</th><td>%s</td></tr>
<tr><th>Container ID</th><td>%s</td></tr>
<tr><th>Owner</th><td>%s</td></tr>
<tr><th>Owner User ID</th><td>%s</td></tr>
<tr><th>Created At</th><td>%s</td></tr>
<tr><th>Updated At</th><td>%s</td></tr>
<tr><th>Last Started At</th><td>%s</td></tr>
</table>
</div>
`,
		html.EscapeString(box.Name),
		box.ID,
		html.EscapeString(box.Status),
		html.EscapeString(box.Image),
		html.EscapeString(box.Ctrhost),
		html.EscapeString(ptrStr(box.ContainerID)),
		html.EscapeString(ownerEmail),
		html.EscapeString(box.CreatedByUserID),
		formatTime(box.CreatedAt),
		formatTime(box.UpdatedAt),
		formatTime(box.LastStartedAt),
	)

	// Route/sharing config
	fmt.Fprintf(w, `<div class="section">
<h2>Routing Configuration</h2>
<table>
<tr><th>Proxy Port</th><td>%d</td></tr>
<tr><th>Share Mode</th><td>%s</td></tr>
</table>
</div>
`, route.Port, html.EscapeString(route.Share))

	// SSH info
	fmt.Fprintf(w, `<div class="section">
<h2>SSH Configuration</h2>
<table>
<tr><th>SSH Port</th><td>%s</td></tr>
<tr><th>SSH User</th><td>%s</td></tr>
<tr><th>SSH Host</th><td>%s</td></tr>
<tr><th>Has Server Identity Key</th><td>%v</td></tr>
<tr><th>Has Client Private Key</th><td>%v</td></tr>
<tr><th>Has Authorized Keys</th><td>%v</td></tr>
</table>
</div>
`,
		formatInt64Ptr(box.SSHPort),
		html.EscapeString(ptrStr(box.SSHUser)),
		html.EscapeString(box.SSHHost()),
		len(box.SSHServerIdentityKey) > 0,
		len(box.SSHClientPrivateKey) > 0,
		box.SSHAuthorizedKeys != nil && *box.SSHAuthorizedKeys != "",
	)

	// Active shares
	fmt.Fprintf(w, `<div class="section">
<h2>Active Shares (%d)</h2>
`, len(activeShares))
	if len(activeShares) == 0 {
		fmt.Fprintf(w, "<p>No active shares.</p>\n")
	} else {
		fmt.Fprintf(w, "<table>\n<tr><th>Email</th><th>Shared By</th><th>Message</th><th>Created At</th></tr>\n")
		for _, share := range activeShares {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
				html.EscapeString(share.SharedWithUserEmail),
				html.EscapeString(share.SharedByUserID),
				html.EscapeString(ptrStr(share.Message)),
				formatTime(share.CreatedAt),
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}
	fmt.Fprintf(w, "</div>\n")

	// Pending shares
	fmt.Fprintf(w, `<div class="section">
<h2>Pending Shares (%d)</h2>
`, len(pendingShares))
	if len(pendingShares) == 0 {
		fmt.Fprintf(w, "<p>No pending shares.</p>\n")
	} else {
		fmt.Fprintf(w, "<table>\n<tr><th>Email</th><th>Shared By</th><th>Message</th><th>Created At</th></tr>\n")
		for _, share := range pendingShares {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
				html.EscapeString(share.SharedWithEmail),
				html.EscapeString(share.SharedByUserID),
				html.EscapeString(ptrStr(share.Message)),
				formatTime(share.CreatedAt),
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}
	fmt.Fprintf(w, "</div>\n")

	// Share links
	fmt.Fprintf(w, `<div class="section">
<h2>Share Links (%d)</h2>
`, len(shareLinks))
	if len(shareLinks) == 0 {
		fmt.Fprintf(w, "<p>No share links.</p>\n")
	} else {
		fmt.Fprintf(w, "<table>\n<tr><th>Token</th><th>Created By</th><th>Created At</th><th>Last Used</th><th>Use Count</th></tr>\n")
		for _, link := range shareLinks {
			fmt.Fprintf(w, "<tr><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
				html.EscapeString(link.ShareToken),
				html.EscapeString(link.CreatedByEmail),
				formatTime(link.CreatedAt),
				formatTime(link.LastUsedAt),
				formatInt64Ptr(link.UseCount),
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}
	fmt.Fprintf(w, "</div>\n")

	// Creation log
	if box.CreationLog != nil && *box.CreationLog != "" {
		fmt.Fprintf(w, `<div class="section">
<h2>Creation Log</h2>
<pre>%s</pre>
</div>
`, html.EscapeString(*box.CreationLog))
	}

	fmt.Fprintf(w, `</body></html>
`)
}

// handleDebugUsers displays a list of all users with their root support status.
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
			UserID                 string `json:"user_id"`
			Email                  string `json:"email"`
			CreatedAt              string `json:"created_at,omitempty"`
			RootSupport            bool   `json:"root_support"`
			CreatedForLoginWithExe bool   `json:"created_for_login_with_exe"`
			AccountID              string `json:"account_id,omitempty"`
		}
		var usersJSON []userInfo
		for _, u := range users {
			createdAt := ""
			if u.CreatedAt != nil {
				createdAt = u.CreatedAt.Format(time.RFC3339)
			}
			usersJSON = append(usersJSON, userInfo{
				UserID:                 u.UserID,
				Email:                  u.Email,
				CreatedAt:              createdAt,
				RootSupport:            u.RootSupport == 1,
				CreatedForLoginWithExe: u.CreatedForLoginWithExe,
				AccountID:              accountByUser[u.UserID],
			})
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Users</title>
<style>
.toggle-btn { padding: 4px 8px; cursor: pointer; border-radius: 3px; border: 1px solid #ccc; }
.toggle-btn.enabled { background: #28a745; color: white; border-color: #28a745; }
.toggle-btn.disabled { background: #6c757d; color: white; border-color: #6c757d; }
dialog { padding: 20px; border: 1px solid #ccc; border-radius: 5px; }
dialog::backdrop { background: rgba(0,0,0,0.5); }
dialog input[type="text"] { width: 100%%; padding: 8px; margin: 10px 0; box-sizing: border-box; }
dialog button { margin-right: 10px; padding: 8px 16px; }
dialog .confirm-btn { background: #28a745; color: white; border: none; cursor: pointer; }
dialog .confirm-btn:disabled { background: #ccc; cursor: not-allowed; }
dialog .cancel-btn { background: #6c757d; color: white; border: none; cursor: pointer; }
</style>
</head><body>
<h1>Users</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/users?format=json">json</a></p>
<p>Regular users: %d | Login-with-exe users: %d | Total: %d</p>
`, regularCount, loginWithExeCount, len(users))

	if len(users) == 0 {
		fmt.Fprintf(w, "<p>No users found.</p>\n")
	} else {
		fmt.Fprintf(w, "<table border='1' cellpadding='5' cellspacing='0'>\n")
		fmt.Fprintf(w, "<tr><th>Email</th><th>User ID</th><th>Created At</th><th>Login-only</th><th>Billing</th><th>Root Support</th></tr>\n")
		for _, u := range users {
			createdAt := "-"
			if u.CreatedAt != nil {
				createdAt = u.CreatedAt.Format(time.RFC3339)
			}
			rootSupportStatus := "No"
			btnClass := "disabled"
			btnText := "Enable"
			if u.RootSupport == 1 {
				rootSupportStatus = "Yes"
				btnClass = "enabled"
				btnText = "Disable"
			}
			loginWithExe := ""
			if u.CreatedForLoginWithExe {
				loginWithExe = "✓"
			}
			billingCell := "-"
			if acctID, ok := accountByUser[u.UserID]; ok {
				billingCell = fmt.Sprintf("<a href='%s' target='_blank'>%s</a>",
					html.EscapeString(s.billing.DashboardURL(acctID)),
					html.EscapeString(acctID))
			}
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s <button class='toggle-btn %s' data-email='%s' data-userid='%s' data-enabled='%v'>%s</button></td></tr>\n",
				html.EscapeString(u.Email),
				html.EscapeString(u.UserID),
				html.EscapeString(createdAt),
				loginWithExe,
				billingCell,
				html.EscapeString(rootSupportStatus),
				btnClass,
				html.EscapeString(u.Email),
				html.EscapeString(u.UserID),
				u.RootSupport == 1,
				btnText,
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}

	fmt.Fprintf(w, `<dialog id="toggleDialog">
<form method="post" action="/debug/users/toggle-root-support">
<p id="dialogMessage"></p>
<p><strong id="emailDisplay"></strong></p>
<input type="hidden" name="user_id" id="userIdInput">
<input type="hidden" name="enable" id="enableInput">
<div id="confirmSection" style="display:none;">
<p>Type the email address to confirm:</p>
<input type="text" name="confirm_email" id="confirmInput" autocomplete="off" placeholder="Type email to confirm">
</div>
<p>
<button type="submit" class="confirm-btn" id="confirmBtn">Confirm</button>
<button type="button" class="cancel-btn" id="cancelBtn">Cancel</button>
</p>
</form>
</dialog>
<script>
document.addEventListener('click', function(e) {
    if (e.target.classList.contains('toggle-btn')) {
        var email = e.target.dataset.email;
        var userId = e.target.dataset.userid;
        var isEnabled = e.target.dataset.enabled === 'true';
        var enabling = !isEnabled;

        document.getElementById('emailDisplay').textContent = email;
        document.getElementById('userIdInput').value = userId;
        document.getElementById('enableInput').value = enabling ? '1' : '0';

        var confirmSection = document.getElementById('confirmSection');
        var confirmInput = document.getElementById('confirmInput');
        var confirmBtn = document.getElementById('confirmBtn');

        if (enabling) {
            // Enabling: require email confirmation
            document.getElementById('dialogMessage').textContent = 'Enable root support access for this user?';
            confirmSection.style.display = 'block';
            confirmInput.value = '';
            confirmBtn.disabled = true;
        } else {
            // Disabling: no confirmation needed
            document.getElementById('dialogMessage').textContent = 'Disable root support access for this user?';
            confirmSection.style.display = 'none';
            confirmBtn.disabled = false;
        }

        document.getElementById('toggleDialog').showModal();
    }
});
document.getElementById('cancelBtn').addEventListener('click', function() {
    document.getElementById('toggleDialog').close();
});
document.getElementById('confirmInput').addEventListener('input', function() {
    var expected = document.getElementById('emailDisplay').textContent;
    document.getElementById('confirmBtn').disabled = (this.value !== expected);
});
</script>
</body></html>
`)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Exelets</title>
<style>
table { border-collapse: collapse; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
.status-healthy { color: green; font-weight: bold; }
.status-error { color: red; font-weight: bold; }
.preferred { background: #d4edda; }
.set-btn { padding: 4px 12px; cursor: pointer; border-radius: 3px; border: 1px solid #007bff; background: #007bff; color: white; }
.set-btn:hover { background: #0056b3; }
.clear-btn { padding: 4px 12px; cursor: pointer; border-radius: 3px; border: 1px solid #dc3545; background: #dc3545; color: white; }
.clear-btn:hover { background: #c82333; }
</style>
</head><body>
<h1>Exelets</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/exelets?format=json">json</a></p>
`)

	if len(exelets) == 0 {
		fmt.Fprintf(w, "<p>No exelets configured.</p>\n")
	} else {
		fmt.Fprintf(w, "<table>\n")
		fmt.Fprintf(w, "<tr><th>Address</th><th>Status</th><th>Version</th><th>Arch</th><th>Instances</th><th>Actions</th></tr>\n")
		for _, e := range exelets {
			rowClass := ""
			if e.IsPreferred {
				rowClass = " class='preferred'"
			}
			statusClass := "status-healthy"
			statusText := e.Status
			if e.Status == "error" {
				statusClass = "status-error"
				statusText = fmt.Sprintf("error: %s", e.Error)
			}

			fmt.Fprintf(w, "<tr%s>", rowClass)
			fmt.Fprintf(w, "<td><code>%s</code></td>", html.EscapeString(e.Address))
			fmt.Fprintf(w, "<td class='%s'>%s</td>", statusClass, html.EscapeString(statusText))
			fmt.Fprintf(w, "<td>%s</td>", html.EscapeString(e.Version))
			fmt.Fprintf(w, "<td>%s</td>", html.EscapeString(e.Arch))
			fmt.Fprintf(w, "<td>%d</td>", e.InstanceCount)
			fmt.Fprintf(w, "<td>")
			if !e.IsPreferred {
				fmt.Fprintf(w, `<form method="post" action="/debug/exelets/set-preferred" style="display: inline;" onsubmit="return confirm('Set %s as the preferred exelet?');">
<input type="hidden" name="address" value="%s">
<button type="submit" class="set-btn">Set as Preferred</button>
</form>`, html.EscapeString(e.Address), html.EscapeString(e.Address))
			} else {
				fmt.Fprintf(w, `⭐ <form method="post" action="/debug/exelets/set-preferred" style="display: inline;" onsubmit="return confirm('Clear preferred exelet?');">
<input type="hidden" name="address" value="">
<button type="submit" class="clear-btn">Clear Preference</button>
</form>`)
			}
			fmt.Fprintf(w, "</td>")
			fmt.Fprintf(w, "</tr>\n")
		}
		fmt.Fprintf(w, "</table>\n")
	}

	fmt.Fprintf(w, `</body></html>
`)
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

// handleDebugSignupLimiter displays the signup rate limiter state.
func (s *Server) handleDebugSignupLimiter(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Signup Rate Limiter</title>
<style>
table { border-collapse: collapse; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
</style>
</head><body>
<h1>Signup Rate Limiter</h1>
<p>Rate limit: 5 requests per minute per IP address.</p>
<h2>Currently Rate-Limited IPs</h2>
`)
	s.signupLimiter.DumpHTML(w, true) // onlyLimited=true to show only rate-limited IPs
	fmt.Fprintf(w, `
<h2>All Tracked IPs</h2>
`)
	s.signupLimiter.DumpHTML(w, false) // show all tracked IPs
	fmt.Fprintf(w, `</body></html>`)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>New Throttle Settings</title>
<style>
table { border-collapse: collapse; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
.section { margin: 20px 0; }
h2 { border-bottom: 1px solid #ccc; padding-bottom: 5px; }
textarea { width: 100%%; height: 150px; font-family: monospace; }
input[type="text"] { width: 100%%; padding: 8px; box-sizing: border-box; }
.toggle-switch { display: inline-flex; align-items: center; cursor: pointer; }
.toggle-switch input { display: none; }
.toggle-slider { width: 50px; height: 26px; background: #ccc; border-radius: 13px; position: relative; transition: 0.3s; }
.toggle-slider:before { content: ""; position: absolute; width: 22px; height: 22px; background: white; border-radius: 50%%; top: 2px; left: 2px; transition: 0.3s; }
.toggle-switch input:checked + .toggle-slider { background: #dc3545; }
.toggle-switch input:checked + .toggle-slider:before { left: 26px; }
.toggle-label { margin-left: 10px; font-weight: bold; }
.save-btn { background: #007bff; color: white; border: none; padding: 10px 20px; cursor: pointer; border-radius: 5px; font-size: 16px; }
.save-btn:hover { background: #0056b3; }
.warning { background: #fff3cd; border: 1px solid #ffc107; padding: 10px; border-radius: 5px; margin: 10px 0; }
.error-list { color: red; margin: 5px 0; }
</style>
</head><body>
<h1>New Throttle Settings</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/new-throttle?format=json">json</a></p>

<div class="warning">
<strong>Warning:</strong> These settings control who can create new VMs. Enable with caution.
</div>

<form method="post" action="/debug/new-throttle" id="throttleForm">

<div class="section">
<h2>Global Throttle</h2>
<p>When enabled, ALL users are blocked from creating new VMs.</p>
<label class="toggle-switch">
<input type="checkbox" name="enabled" value="true" %s>
<span class="toggle-slider"></span>
<span class="toggle-label">Block all new VM creation</span>
</label>
</div>

<div class="section">
<h2>Email Pattern Throttle</h2>
<p>Enter email patterns (regular expressions) to block, one per line. Users whose email matches any pattern will be blocked.</p>
<p>Examples: <code>.*@example\.com$</code> (block all example.com), <code>^test@</code> (block emails starting with test@)</p>
<textarea name="email_patterns" placeholder="Enter email regex patterns, one per line...">%s</textarea>
<div id="patternErrors" class="error-list"></div>
</div>

<div class="section">
<h2>Denial Message</h2>
<p>Message shown to users when they are blocked from creating VMs. Leave empty for default message.</p>
<input type="text" name="message" value="%s" placeholder="VM creation is temporarily unavailable.">
</div>

<div class="section">
<button type="submit" class="save-btn">Save Settings</button>
</div>

</form>

<script>
document.getElementById('throttleForm').addEventListener('submit', function(e) {
    var patterns = document.querySelector('textarea[name="email_patterns"]').value;
    var lines = patterns.split('\n');
    var errors = [];

    for (var i = 0; i < lines.length; i++) {
        var line = lines[i].trim();
        if (line === '') continue;
        try {
            new RegExp(line);
        } catch (err) {
            errors.push('Line ' + (i + 1) + ': ' + err.message);
        }
    }

    var errorDiv = document.getElementById('patternErrors');
    if (errors.length > 0) {
        errorDiv.innerHTML = errors.join('<br>');
        e.preventDefault();
        return false;
    }
    errorDiv.innerHTML = '';
    return true;
});
</script>

</body></html>
`, checkedAttr(config.Enabled), html.EscapeString(strings.Join(config.EmailPatterns, "\n")), html.EscapeString(config.Message))
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>IP Shards</title>
<style>
table { border-collapse: collapse; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
.missing { background: #f8d7da; color: #721c24; }
.unmapped { background: #fff3cd; padding: 10px; border-radius: 5px; margin: 10px 0; }
</style>
</head><body>
<h1>IP Shards</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/ipshards?format=json">json</a></p>
`)

	if len(shards) == 0 {
		fmt.Fprintf(w, "<p>No IP shards in database.</p>\n")
	} else {
		fmt.Fprintf(w, "<table>\n")
		fmt.Fprintf(w, "<tr><th>Shard</th><th>Public IP</th><th>Private IP</th></tr>\n")
		for _, shard := range shards {
			rowClass := ""
			privateIP := shard.PrivateIP
			if shard.Missing {
				rowClass = " class='missing'"
				privateIP = "(not on this machine)"
			}
			fmt.Fprintf(w, "<tr%s><td>s%03d</td><td>%s</td><td>%s</td></tr>\n",
				rowClass,
				shard.Shard,
				html.EscapeString(shard.PublicIP),
				html.EscapeString(privateIP),
			)
		}
		fmt.Fprintf(w, "</table>\n")
	}

	if len(unmappedIPs) > 0 {
		fmt.Fprintf(w, "<div class='unmapped'>\n")
		fmt.Fprintf(w, "<strong>IPs on this machine not in DB:</strong> %s\n", html.EscapeString(strings.Join(unmappedIPs, ", ")))
		fmt.Fprintf(w, "</div>\n")
	}

	fmt.Fprintf(w, `</body></html>
`)
}

// handleDebugLogForm renders a simple form to log an error message.
func (s *Server) handleDebugLogForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Log Error</title></head><body>
<h1>Log Error</h1>
<p><a href="/debug">/debug</a></p>
<form method="post">
<input type="text" name="text" value="testing" size="40">
<button type="submit">Log That</button>
</form>
</body></html>
`)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Testimonials</title>
<style>
.testimonial { border: 1px solid #ccc; padding: 15px; margin: 10px 0; border-radius: 5px; }
.testimonial.approved { border-left: 4px solid #28a745; }
.testimonial.unapproved { border-left: 4px solid #dc3545; opacity: 0.6; }
.status { font-weight: bold; margin-bottom: 10px; }
.status.approved { color: #28a745; }
.status.unapproved { color: #dc3545; }
</style>
</head><body>
<h1>Testimonials</h1>
<p><a href="/debug">/debug</a></p>
<p>Testimonials are stored in code (execore/testimonials.go). Edit that file to add or modify testimonials.</p>
`)

	if len(testimonials) == 0 {
		fmt.Fprintf(w, "<p>No testimonials configured.</p>\n")
	} else {
		for i, t := range testimonials {
			class := "unapproved"
			statusClass := "unapproved"
			statusText := "Not Approved"
			if t.Approved {
				class = "approved"
				statusClass = "approved"
				statusText = "Approved"
			}
			fmt.Fprintf(w, `<div class="testimonial %s">
<div class="status %s">#%d - %s</div>
<div class="content">%s</div>
</div>
`, class, statusClass, i+1, statusText, t.HTML)
		}
	}

	fmt.Fprintf(w, `</body></html>
`)
}
