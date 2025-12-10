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
	"runtime/debug"
	"time"

	"exe.dev/exedb"
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
	commit := gitCommit()
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
</ul>
<p>Git version: %s</p>
</body></html>
`, commit)
}

func (s *Server) handleDebugGitsha(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, gitCommit())
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
		email, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
			return queries.GetBoxOwnerEmailByContainerID(ctx, &containerID)
		})
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
<p><a href="/debug/boxes?format=json">View as JSON</a></p>
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
<p><a href="/debug">Back to debug index</a></p>
</body></html>
`)
}

// gitCommit extracts the git SHA from build info for version identification.
func gitCommit() string {
	bi, _ := debug.ReadBuildInfo()
	if bi != nil {
		for _, setting := range bi.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
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
	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxNamed(ctx, boxName)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("failed to look up box: %v", err), http.StatusInternalServerError)
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
	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxNamed(ctx, boxName)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, fmt.Sprintf("box %q not found", boxName), http.StatusNotFound)
			return
		}
		http.Error(w, fmt.Sprintf("failed to look up box: %v", err), http.StatusInternalServerError)
		return
	}

	// Look up owner email
	ownerEmail, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
		return queries.GetEmailByUserID(ctx, box.CreatedByUserID)
	})
	if err != nil {
		ownerEmail = box.CreatedByUserID // fallback to user ID
	}

	// Get sharing info
	pendingShares, _ := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.PendingBoxShare, error) {
		return queries.GetPendingBoxSharesByBoxID(ctx, int64(box.ID))
	})
	activeShares, _ := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.GetBoxSharesByBoxIDRow, error) {
		return queries.GetBoxSharesByBoxID(ctx, int64(box.ID))
	})
	shareLinks, _ := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.GetAllBoxShareLinksByBoxIDRow, error) {
		return queries.GetAllBoxShareLinksByBoxID(ctx, int64(box.ID))
	})

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
<p><a href="/debug/boxes">&larr; Back to boxes</a></p>
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

	fmt.Fprintf(w, `<p><a href="/debug/boxes">&larr; Back to boxes</a></p>
</body></html>
`)
}

// handleDebugUsers displays a list of all users with their root support status.
func (s *Server) handleDebugUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	users, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.User, error) {
		return queries.ListAllUsers(ctx)
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list users: %v", err), http.StatusInternalServerError)
		return
	}

	// Check if JSON format is requested
	if r.URL.Query().Get("format") == "json" {
		type userInfo struct {
			UserID      string `json:"user_id"`
			Email       string `json:"email"`
			CreatedAt   string `json:"created_at,omitempty"`
			RootSupport bool   `json:"root_support"`
		}
		var usersJSON []userInfo
		for _, u := range users {
			createdAt := ""
			if u.CreatedAt != nil {
				createdAt = u.CreatedAt.Format(time.RFC3339)
			}
			usersJSON = append(usersJSON, userInfo{
				UserID:      u.UserID,
				Email:       u.Email,
				CreatedAt:   createdAt,
				RootSupport: u.RootSupport == 1,
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
<p><a href="/debug/users?format=json">View as JSON</a></p>
`)

	if len(users) == 0 {
		fmt.Fprintf(w, "<p>No users found.</p>\n")
	} else {
		fmt.Fprintf(w, "<table border='1' cellpadding='5' cellspacing='0'>\n")
		fmt.Fprintf(w, "<tr><th>Email</th><th>User ID</th><th>Created At</th><th>Root Support</th></tr>\n")
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
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s <button class='toggle-btn %s' data-email='%s' data-userid='%s' data-enabled='%v'>%s</button></td></tr>\n",
				html.EscapeString(u.Email),
				html.EscapeString(u.UserID),
				html.EscapeString(createdAt),
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
<p><a href="/debug">Back to debug index</a></p>
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
		user, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.User, error) {
			return queries.GetUserWithDetails(ctx, userID)
		})
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

	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.SetUserRootSupport(ctx, exedb.SetUserRootSupportParams{
			RootSupport: newValue,
			UserID:      userID,
		})
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
