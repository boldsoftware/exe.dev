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
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"exe.dev/email"
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
    <li><a href="/debug/signup-pow">signup-pow</a></li>
    <li><a href="/debug/signup-reject">signup-reject</a></li>
    <li><a href="/debug/ipshards">ipshards</a> (<a href="/debug/ipshards?format=json">json</a>)</li>
    <li><a href="/debug/log">/debug/log</a> (POST text=... to log an error)</li>
    <li><a href="/debug/testimonials">testimonials</a></li>
    <li><a href="/debug/email">email</a> (send test emails)</li>
    <li><a href="/debug/invite">invite</a> (manage invite codes)</li>
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

	// source=db (default) or source=exelets
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "db"
	}

	// For HTML requests, return the page shell immediately.
	// DataTables will load data via AJAX from the JSON endpoint.
	if r.URL.Query().Get("format") != "json" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// Build the navigation links
		var sourceNav string
		if source == "exelets" {
			sourceNav = `<strong>exelets</strong> | <a href="/debug/boxes?source=db">db</a>`
		} else {
			sourceNav = `<a href="/debug/boxes?source=exelets">exelets</a> | <strong>db</strong>`
		}

		fmt.Fprintf(w, `<!doctype html>
<html><head><title>Boxes/VMs</title>
<link rel="stylesheet" href="/static/datatables.min.css">
<script src="/static/jquery.min.js"></script>
<script src="/static/datatables.min.js"></script>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 20px; }
h1 { margin-bottom: 10px; }
#boxesTable { width: 100%%; }
#boxesTable td, #boxesTable th { font-size: 13px; }
#boxesTable thead tr.filters th { padding: 4px; }
#boxesTable thead tr.filters input { width: 100%%; box-sizing: border-box; font-size: 11px; padding: 4px; }
.migrate-btn { display: inline-block; background: #007bff; color: white; padding: 4px 8px; border-radius: 3px; font-size: 12px; margin-right: 4px; text-decoration: none; }
.migrate-btn:hover { background: #0056b3; text-decoration: none; }
.delete-btn { background: #dc3545; color: white; border: none; padding: 4px 8px; cursor: pointer; border-radius: 3px; font-size: 12px; }
.delete-btn:hover { background: #c82333; }
dialog { padding: 20px; border: 1px solid #ccc; border-radius: 5px; }
dialog::backdrop { background: rgba(0,0,0,0.5); }
dialog input[type="text"] { width: 100%%; padding: 8px; margin: 10px 0; box-sizing: border-box; }
dialog button { margin-right: 10px; padding: 8px 16px; }
dialog .confirm-btn { background: #dc3545; color: white; border: none; cursor: pointer; }
dialog .confirm-btn:disabled { background: #ccc; cursor: not-allowed; }
dialog .cancel-btn { background: #6c757d; color: white; border: none; cursor: pointer; }
a { color: #007bff; text-decoration: none; }
a:hover { text-decoration: underline; }
</style>
</head><body>
<h1>Boxes/VMs</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/boxes?format=json&source=%s">json</a></p>
<p>Source: %s</p>

<table id="boxesTable" class="display stripe hover">
<thead>
<tr>
<th>Name</th>
<th>Exelet</th>
<th>Status</th>
<th>Owner</th>
<th>Actions</th>
</tr>
<tr class="filters">
<th>Name</th>
<th>Exelet</th>
<th>Status</th>
<th>Owner</th>
<th></th>
</tr>
</thead>
</table>

<dialog id="deleteDialog">
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
$(document).ready(function() {
    // Add column filter inputs to header filter row (except Actions column)
    $('#boxesTable thead tr.filters th').each(function(idx) {
        var title = $(this).text();
        if (title) {
            $(this).html('<input type="text" placeholder="' + title + '">');
        }
    });

    var table = $('#boxesTable').DataTable({
        ajax: {
            url: '/debug/boxes?format=json&source=%s',
            dataSrc: ''
        },
        pageLength: 100,
        lengthMenu: [[25, 50, 100, 250, -1], [25, 50, 100, 250, "All"]],
        order: [[0, 'asc']],
        orderCellsTop: true,
        columns: [
            { data: 'name', render: function(d) {
                return '<a href="/debug/boxes/' + d + '">' + d + '</a>';
            }},
            { data: 'host' },
            { data: 'status' },
            { data: 'owner_email', defaultContent: '' },
            { data: 'name', orderable: false, render: function(d) {
                return '<a href="/debug/boxes/migrate?box_name=' + encodeURIComponent(d) + '" class="migrate-btn">Migrate</a> ' +
                       '<button class="delete-btn" data-box="' + d + '">Delete</button>';
            }}
        ],
        initComplete: function() {
            this.api().columns().every(function(idx) {
                var column = this;
                $('input', $('#boxesTable thead tr.filters th').eq(idx)).on('keyup change clear', function() {
                    if (column.search() !== this.value) {
                        column.search(this.value).draw();
                    }
                });
            });
        }
    });
});

// Delete dialog
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
`, source, sourceNav, source)
		return
	}

	// JSON format requested
	type boxInfo struct {
		Host       string `json:"host"`
		ID         string `json:"id,omitempty"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		OwnerEmail string `json:"owner_email,omitempty"`
	}

	var boxes []boxInfo

	if source == "exelets" {
		// Fetch from exelet hosts
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
				if ownerEmail, err := getOwnerEmail(ctx, inst.ID); err == nil {
					info.OwnerEmail = ownerEmail
				} else {
					s.slog().WarnContext(ctx, "failed to resolve box owner email", "boxName", inst.Name, "instanceID", inst.ID, "error", err)
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
				Host:       b.Ctrhost,
				Name:       b.Name,
				Status:     b.Status,
				OwnerEmail: b.OwnerEmail,
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

	var exeletOptions string
	for _, addr := range addrs {
		exeletOptions += fmt.Sprintf(`<option value="%s">%s</option>`, html.EscapeString(addr), html.EscapeString(addr))
	}

	// Escape box name for safe HTML interpolation
	escapedBoxName := html.EscapeString(boxName)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Migrate Box: %s</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 20px; max-width: 600px; }
h1 { margin-bottom: 10px; }
label { display: block; margin-top: 15px; font-weight: bold; }
select, input[type="text"] { width: 100%%; padding: 8px; margin-top: 5px; box-sizing: border-box; font-size: 14px; }
button { margin-top: 20px; padding: 10px 20px; font-size: 14px; cursor: pointer; }
button[type="submit"] { background: #007bff; color: white; border: none; }
button[type="submit"]:disabled { background: #ccc; cursor: not-allowed; }
a { color: #007bff; text-decoration: none; }
a:hover { text-decoration: underline; }
#progress { margin-top: 20px; padding: 15px; background: #f8f9fa; border: 1px solid #dee2e6; border-radius: 4px; display: none; white-space: pre-wrap; font-family: monospace; font-size: 13px; max-height: 400px; overflow-y: auto; }
#progress.active { display: block; }
#progress.error { background: #f8d7da; border-color: #f5c6cb; }
#progress.success { background: #d4edda; border-color: #c3e6cb; }
.warning { background: #fff3cd; border: 1px solid #ffc107; border-radius: 4px; padding: 12px; margin: 15px 0; }
.warning strong { color: #856404; }
</style>
</head><body>
<h1>Migrate Box</h1>
<p><a href="/debug/boxes">&larr; Back to boxes</a></p>

<div class="warning"><strong>Warning:</strong> The VM will be stopped during migration and restarted on the target host.</div>

<form id="migrateForm" method="post" action="/debug/boxes/migrate">
<p>Box: <strong>%s</strong></p>
<input type="hidden" name="box_name" value="%s">

<label for="target">Target exelet:</label>
<select name="target" id="target" required>
<option value="">Select target exelet...</option>
%s
</select>

<label for="confirm_name">Type box name to confirm:</label>
<input type="text" name="confirm_name" id="confirm_name" autocomplete="off" placeholder="%s">

<p>
<button type="submit" id="submitBtn" disabled>Migrate</button>
<a href="/debug/boxes">Cancel</a>
</p>
</form>

<div id="progress"></div>

<script>
var expectedName = %q;
document.getElementById('confirm_name').addEventListener('input', function() {
    var target = document.getElementById('target').value;
    document.getElementById('submitBtn').disabled = (this.value !== expectedName || !target);
});
document.getElementById('target').addEventListener('change', function() {
    var confirmed = document.getElementById('confirm_name').value;
    document.getElementById('submitBtn').disabled = (confirmed !== expectedName || !this.value);
});

document.getElementById('migrateForm').addEventListener('submit', function(e) {
    e.preventDefault();
    var form = this;
    var progress = document.getElementById('progress');
    var submitBtn = document.getElementById('submitBtn');

    submitBtn.disabled = true;
    submitBtn.textContent = 'Migrating...';
    progress.className = 'active';
    progress.textContent = 'Starting migration...\n';

    fetch(form.action, {
        method: 'POST',
        body: new FormData(form)
    }).then(function(response) {
        var reader = response.body.getReader();
        var decoder = new TextDecoder();

        function read() {
            reader.read().then(function(result) {
                if (result.done) {
                    return;
                }
                var text = decoder.decode(result.value, {stream: true});
                progress.textContent += text;
                progress.scrollTop = progress.scrollHeight;

                // Check for completion markers
                if (text.includes('MIGRATION_SUCCESS:')) {
                    progress.className = 'active success';
                    var match = text.match(/MIGRATION_SUCCESS:(\S+)/);
                    if (match) {
                        // Add a clickable link instead of auto-redirecting
                        var link = document.createElement('a');
                        link.href = '/debug/boxes/' + match[1];
                        link.textContent = 'Go to box details \u2192';
                        link.style.cssText = 'display:inline-block;margin-top:15px;padding:10px 20px;background:#28a745;color:white;text-decoration:none;border-radius:4px;';
                        progress.parentNode.insertBefore(link, progress.nextSibling);
                    }
                } else if (text.includes('MIGRATION_ERROR:')) {
                    progress.className = 'active error';
                    submitBtn.textContent = 'Migrate';
                    submitBtn.disabled = false;
                }

                read();
            });
        }
        read();
    }).catch(function(err) {
        progress.className = 'active error';
        progress.textContent += '\nFetch error: ' + err;
        submitBtn.textContent = 'Migrate';
        submitBtn.disabled = false;
    });
});
</script>
</body></html>
`, escapedBoxName, escapedBoxName, escapedBoxName, exeletOptions, escapedBoxName, boxName)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Users</title>
<link rel="stylesheet" href="/static/datatables.min.css">
<script src="/static/jquery.min.js"></script>
<script src="/static/datatables.min.js"></script>
<style>
.toggle-btn { padding: 4px 8px; cursor: pointer; border-radius: 3px; border: 1px solid #ccc; font-size: 11px; }
.toggle-btn.enabled { background: #28a745; color: white; border-color: #28a745; }
.toggle-btn.disabled { background: #6c757d; color: white; border-color: #6c757d; }
.edit-btn { padding: 2px 6px; cursor: pointer; border-radius: 3px; border: 1px solid #007bff; background: #007bff; color: white; font-size: 11px; }
dialog { padding: 20px; border: 1px solid #ccc; border-radius: 5px; }
dialog::backdrop { background: rgba(0,0,0,0.5); }
dialog input[type="text"], dialog input[type="number"] { width: 100%%; padding: 8px; margin: 5px 0; box-sizing: border-box; }
dialog button { margin-right: 10px; padding: 8px 16px; }
dialog .confirm-btn { background: #28a745; color: white; border: none; cursor: pointer; }
dialog .confirm-btn:disabled { background: #ccc; cursor: not-allowed; }
dialog .cancel-btn { background: #6c757d; color: white; border: none; cursor: pointer; }
#usersTable { width: 100%%; }
#usersTable td, #usersTable th { font-size: 13px; }
#usersTable thead tr.filters th { padding: 4px; }
#usersTable thead tr.filters input { width: 100%%; box-sizing: border-box; font-size: 11px; padding: 4px; }
.credit-cell { text-align: right; font-family: monospace; }
.negative { color: red; }
</style>
</head><body>
<h1>Users</h1>
<p><a href="/debug">/debug</a> | <a href="/debug/users?format=json">json</a></p>
<p>Regular users: %d | Login-with-exe users: %d | Total: %d</p>

<table id="usersTable" class="display stripe hover">
<thead>
<tr>
<th>Email</th>
<th>User ID</th>
<th>Created At</th>
<th>Login-only</th>
<th>Billing</th>
<th>DB Credit ($)</th>
<th>Effective ($)</th>
<th>Max ($)</th>
<th>Refresh/hr ($)</th>
<th>Total Used ($)</th>
<th>Last Refresh</th>
<th>VM Creation Disabled</th>
<th>Root Support</th>
<th>Discord</th>
<th>Invites</th>
</tr>
<tr class="filters">
<th>Email</th>
<th>User ID</th>
<th>Created At</th>
<th>Login-only</th>
<th>Billing</th>
<th>DB Credit ($)</th>
<th>Effective ($)</th>
<th>Max ($)</th>
<th>Refresh/hr ($)</th>
<th>Total Used ($)</th>
<th>Last Refresh</th>
<th>VM Creation Disabled</th>
<th>Root Support</th>
<th>Discord</th>
<th>Invites</th>
</tr>
</thead>
</table>

<dialog id="toggleDialog">
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

<dialog id="creditDialog">
<form method="post" action="/debug/users/update-credit">
<h3>Edit Gateway Credit</h3>
<input type="hidden" name="user_id" id="creditUserIdInput">
<p><label>Available Credit ($):<br><input type="number" name="available" id="creditAvailInput" step="0.01"></label></p>
<p><label>Max Credit ($):<br><input type="number" name="max" id="creditMaxInput" step="0.01"></label></p>
<p><label>Refresh per Hour ($):<br><input type="number" name="refresh" id="creditRefreshInput" step="0.01"></label></p>
<p>
<button type="submit" class="confirm-btn">Save</button>
<button type="button" class="cancel-btn" id="creditCancelBtn">Cancel</button>
</p>
</form>
</dialog>

<script>
var usersTable;

$(document).ready(function() {
    // Add column filter inputs to header filter row
    $('#usersTable thead tr.filters th').each(function() {
        var title = $(this).text();
        $(this).html('<input type="text" placeholder="' + title + '">');
    });

    usersTable = $('#usersTable').DataTable({
        ajax: {
            url: '/debug/users?format=json',
            dataSrc: ''
        },
        pageLength: 100,
        lengthMenu: [[25, 50, 100, 250, -1], [25, 50, 100, 250, "All"]],
        order: [[2, 'desc']],
        orderCellsTop: true,
        columns: [
            { data: 'email' },
            { data: 'user_id' },
            { data: 'created_at', defaultContent: '-' },
            { data: 'created_for_login_with_exe', render: function(d) { return d ? '✓' : ''; } },
            { data: null, render: function(d) {
                if (d.billing_url) return '<a href="' + d.billing_url + '" target="_blank">' + d.account_id + '</a>';
                return '-';
            }},
            { data: null, className: 'credit-cell', render: function(d) {
                var val = d.credit_available_usd ? d.credit_available_usd.toFixed(2) : '-';
                var cls = d.credit_available_usd < 0 ? 'negative' : '';
                return '<span class="' + cls + '">' + val + '</span> ' +
                    '<button class="edit-btn" data-userid="' + d.user_id + '" ' +
                    'data-avail="' + (d.credit_available_usd||0) + '" ' +
                    'data-max="' + (d.credit_max_usd||100) + '" ' +
                    'data-refresh="' + (d.credit_refresh_per_hr_usd||10) + '">✎</button>';
            }},
            { data: 'credit_effective_usd', className: 'credit-cell', render: function(d) {
                if (!d && d !== 0) return '-';
                var cls = d < 0 ? 'negative' : '';
                return '<span class="' + cls + '">' + d.toFixed(2) + '</span>';
            }},
            { data: 'credit_max_usd', className: 'credit-cell', render: function(d) { return d ? d.toFixed(2) : '-'; } },
            { data: 'credit_refresh_per_hr_usd', className: 'credit-cell', render: function(d) { return d ? d.toFixed(2) : '-'; } },
            { data: 'credit_total_used_usd', className: 'credit-cell', render: function(d) { return d ? d.toFixed(2) : '-'; } },
            { data: 'credit_last_refresh_at', defaultContent: '-' },
            { data: 'vm_creation_disabled', render: function(d, type, row) {
                var isDisabled = !!d;
                var status = isDisabled ? 'Yes' : 'No';
                var btnClass = isDisabled ? 'disabled' : 'enabled';
                var btnText = isDisabled ? 'Enable' : 'Disable';
                return status + ' <button class="toggle-btn vm-toggle-btn ' + btnClass + '" ' +
                    'data-userid="' + row.user_id + '" data-disabled="' + isDisabled + '">' + btnText + '</button>';
            }},
            { data: null, render: function(d) {
                var status = d.root_support ? 'Yes' : 'No';
                var btnClass = d.root_support ? 'enabled' : 'disabled';
                var btnText = d.root_support ? 'Disable' : 'Enable';
                return status + ' <button class="toggle-btn root-toggle-btn ' + btnClass + '" ' +
                    'data-email="' + d.email + '" data-userid="' + d.user_id + '" ' +
                    'data-enabled="' + d.root_support + '">' + btnText + '</button>';
            }},
            { data: 'discord_id', defaultContent: '-' },
            { data: null, render: function(d) {
                return d.invite_count + ' <button class="toggle-btn add-invite-btn" ' +
                    'data-email="' + d.email + '">+1</button>';
            }}
        ],
        initComplete: function() {
            this.api().columns().every(function(idx) {
                var column = this;
                $('input', $('#usersTable thead tr.filters th').eq(idx)).on('keyup change clear', function() {
                    if (column.search() !== this.value) {
                        column.search(this.value).draw();
                    }
                });
            });
        }
    });
});

// Toggle root support dialog
document.addEventListener('click', function(e) {
    if (e.target.classList.contains('root-toggle-btn')) {
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
            document.getElementById('dialogMessage').textContent = 'Enable root support access for this user?';
            confirmSection.style.display = 'block';
            confirmInput.value = '';
            confirmBtn.disabled = true;
        } else {
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

// Toggle VM creation disabled
document.addEventListener('click', function(e) {
    if (e.target.classList.contains('vm-toggle-btn')) {
        var userId = e.target.dataset.userid;
        var currentlyDisabled = e.target.dataset.disabled === 'true';
        var body = new URLSearchParams();
        body.append('user_id', userId);
        body.append('disable', currentlyDisabled ? '0' : '1');

        fetch('/debug/users/toggle-vm-creation', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded'
            },
            body: body.toString()
        }).then(function(resp) {
            if (!resp.ok) {
                alert('Failed to update VM creation flag');
                return;
            }
            if (usersTable) {
                usersTable.ajax.reload(null, false);
            }
        }).catch(function(err) {
            console.error('VM creation toggle failed', err);
            alert('Failed to update VM creation flag');
        });
    }
});

// Credit edit dialog
document.addEventListener('click', function(e) {
    if (e.target.classList.contains('edit-btn')) {
        var userId = e.target.dataset.userid;
        var avail = parseFloat(e.target.dataset.avail) || 0;
        var max = parseFloat(e.target.dataset.max) || 100;
        var refresh = parseFloat(e.target.dataset.refresh) || 10;
        document.getElementById('creditUserIdInput').value = userId;
        document.getElementById('creditAvailInput').value = avail.toFixed(2);
        document.getElementById('creditMaxInput').value = max.toFixed(2);
        document.getElementById('creditRefreshInput').value = refresh.toFixed(2);
        document.getElementById('creditDialog').showModal();
    }
});
document.getElementById('creditCancelBtn').addEventListener('click', function() {
    document.getElementById('creditDialog').close();
});

// Add invite button
document.addEventListener('click', function(e) {
    if (e.target.classList.contains('add-invite-btn')) {
        var email = e.target.dataset.email;
        var body = new URLSearchParams();
        body.append('action', 'give_to_user');
        body.append('email', email);
        body.append('count', '1');
        body.append('plan_type', 'trial');

        fetch('/debug/invite', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded'
            },
            body: body.toString()
        }).then(function(resp) {
            if (!resp.ok) {
                alert('Failed to add invite');
                return;
            }
            if (usersTable) {
                usersTable.ajax.reload(null, false);
            }
        }).catch(function(err) {
            console.error('Add invite failed', err);
            alert('Failed to add invite');
        });
    }
});
</script>
</body></html>
`, regularCount, loginWithExeCount, len(users))
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

// handleDebugSignupLimiter displays the signup rate limiter state and login creation settings.
func (s *Server) handleDebugSignupLimiter(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	loginDisabled := s.IsLoginCreationDisabled(ctx)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Signup Limiter</title>
<style>
table { border-collapse: collapse; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
.section { margin: 20px 0; }
h2 { border-bottom: 1px solid #ccc; padding-bottom: 5px; }
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
</style>
</head><body>
<h1>Signup Limiter</h1>
<p><a href="/debug">/debug</a></p>

<div class="section">
<h2>Block New Account Creation</h2>
<div class="warning">
<strong>Warning:</strong> When enabled, users with unrecognized email addresses cannot create new accounts.
</div>
<form method="post" action="/debug/signup-limiter">
<p>When enabled, users trying to login with an email we haven't seen before will be blocked. Existing users can still log in and add new SSH keys.</p>
<label class="toggle-switch">
<input type="checkbox" name="disabled" value="true" %s>
<span class="toggle-slider"></span>
<span class="toggle-label">Block new account creation</span>
</label>
<p style="margin-top: 10px;">
<button type="submit" class="save-btn">Save Settings</button>
</p>
</form>
</div>

<div class="section">
<h2>Rate Limiter</h2>
<p>Rate limit: 5 requests per minute per IP address.</p>
<h3>Currently Rate-Limited IPs</h3>
`, checkedAttr(loginDisabled))
	if s.signupLimiter != nil {
		s.signupLimiter.Allow(netip.Addr{}) // ensure internal cache is initialized
		s.signupLimiter.DumpHTML(w, true)   // onlyLimited=true to show only rate-limited IPs
	} else {
		fmt.Fprintf(w, "<p>No rate limiter configured.</p>\n")
	}
	fmt.Fprintf(w, `
<h3>All Tracked IPs</h3>
`)
	if s.signupLimiter != nil {
		s.signupLimiter.DumpHTML(w, false) // show all tracked IPs
	} else {
		fmt.Fprintf(w, "<p>No rate limiter configured.</p>\n")
	}
	fmt.Fprintf(w, `</div>
</body></html>`)
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
			linkHTML := ""
			if t.Link != "" {
				linkHTML = fmt.Sprintf(` <a href="%s" target="_blank">[link]</a>`, t.Link)
			}
			fmt.Fprintf(w, `<div class="testimonial %s">
<div class="status %s">#%d - %s</div>
<div class="content">%s<br><br>&mdash; %s%s</div>
</div>
`, class, statusClass, i+1, statusText, strings.ReplaceAll(t.Quote, "\n\n", "<br><br>"), t.Author, linkHTML)
		}
	}

	fmt.Fprintf(w, `</body></html>
`)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Send Test Email</title>
<style>
.section { margin: 20px 0; }
input[type="text"], input[type="email"] { width: 300px; padding: 8px; }
textarea { width: 400px; height: 100px; }
.send-btn { background: #007bff; color: white; border: none; padding: 10px 20px; cursor: pointer; border-radius: 5px; }
.send-btn:hover { background: #0056b3; }
.send-btn:disabled { background: #ccc; cursor: not-allowed; }
.provider-status { margin: 10px 0; }
.available { color: green; }
.unavailable { color: red; }
.result { margin: 20px 0; padding: 15px; border-radius: 5px; }
.result.success { background: #d4edda; border: 1px solid #c3e6cb; color: #155724; }
.result.error { background: #f8d7da; border: 1px solid #f5c6cb; color: #721c24; }
</style>
</head><body>
<h1>Send Test Email</h1>
<p><a href="/debug">/debug</a></p>

<div class="provider-status">
<strong>Provider Status:</strong><br>
<span class="%s">Postmark: %s</span><br>
<span class="%s">Mailgun: %s</span>
</div>
`,
		availableClass(postmarkAvailable), availableText(postmarkAvailable),
		availableClass(mailgunAvailable), availableText(mailgunAvailable))

	// Show result if present
	if result := r.URL.Query().Get("result"); result != "" {
		resultClass := "success"
		if r.URL.Query().Get("error") == "1" {
			resultClass = "error"
		}
		fmt.Fprintf(w, `<div class="result %s">%s</div>`, resultClass, html.EscapeString(result))
	}

	fmt.Fprintf(w, `
<form method="post">
<div class="section">
<label><strong>To:</strong></label><br>
<input type="email" name="to" required placeholder="recipient@example.com">
</div>

<div class="section">
<label><strong>Subject:</strong></label><br>
<input type="text" name="subject" value="Test email from exe.dev debug" required>
</div>

<div class="section">
<label><strong>Body:</strong></label><br>
<textarea name="body" required>This is a test email sent from the exe.dev debug page.</textarea>
</div>

<div class="section">
<label><strong>Provider:</strong></label><br>
<select name="provider">
<option value="postmark" %s>Postmark</option>
<option value="mailgun" %s>Mailgun</option>
</select>
</div>

<div class="section">
<button type="submit" class="send-btn">Send Test Email</button>
</div>
</form>
</body></html>
`, disabledAttr(!postmarkAvailable), disabledAttr(!mailgunAvailable))
}

func availableClass(available bool) string {
	if available {
		return "available"
	}
	return "unavailable"
}

func availableText(available bool) string {
	if available {
		return "Available"
	}
	return "Not configured"
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Signup Proof-of-Work</title>
<style>
.section { margin: 20px 0; }
h2 { border-bottom: 1px solid #ccc; padding-bottom: 5px; }
.toggle-switch { display: inline-flex; align-items: center; cursor: pointer; }
.toggle-switch input { display: none; }
.toggle-slider { width: 50px; height: 26px; background: #ccc; border-radius: 13px; position: relative; transition: 0.3s; }
.toggle-slider:before { content: ""; position: absolute; width: 22px; height: 22px; background: white; border-radius: 50%%; top: 2px; left: 2px; transition: 0.3s; }
.toggle-switch input:checked + .toggle-slider { background: #28a745; }
.toggle-switch input:checked + .toggle-slider:before { left: 26px; }
.toggle-label { margin-left: 10px; font-weight: bold; }
.save-btn { background: #007bff; color: white; border: none; padding: 10px 20px; cursor: pointer; border-radius: 5px; font-size: 16px; }
.save-btn:hover { background: #0056b3; }
.info { background: #e7f3ff; border: 1px solid #b6d4fe; padding: 10px; border-radius: 5px; margin: 10px 0; }
code { background: #f5f5f5; padding: 2px 6px; border-radius: 3px; }
</style>
</head><body>
<h1>Signup Proof-of-Work</h1>
<p><a href="/debug">/debug</a></p>

<div class="info">
<strong>Info:</strong> When enabled, new users must complete a proof-of-work challenge before creating an account.
This helps prevent automated signups. Difficulty is currently set to <code>%d</code> leading zero bits (~%d hashes average).
</div>

<div class="section">
<h2>Enable POW for New Signups</h2>
<form method="post" action="/debug/signup-pow">
<p>When enabled, new users will see a "Verifying..." interstitial while their browser solves a cryptographic puzzle.</p>
<label class="toggle-switch">
<input type="checkbox" name="enabled" value="true" %s>
<span class="toggle-slider"></span>
<span class="toggle-label">Require POW for new signups</span>
</label>
<p style="margin-top: 10px;">
<button type="submit" class="save-btn">Save Settings</button>
</p>
</form>
</div>

</body></html>
`, s.signupPOW.GetDifficulty(), 1<<s.signupPOW.GetDifficulty(), checkedAttr(enabled))
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
	bypassList, err := withRxRes0(s, ctx, (*exedb.Queries).ListEmailQualityBypass)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get bypass list: %v", err), http.StatusInternalServerError)
		return
	}

	type ipqsSummary struct {
		display string
		rawJSON string
		hasJSON bool
	}

	summarizeIPQS := func(raw *string) ipqsSummary {
		if raw == nil {
			return ipqsSummary{display: "missing"}
		}
		rawJSON := strings.TrimSpace(*raw)
		if rawJSON == "" {
			return ipqsSummary{display: "missing"}
		}

		summary := ipqsSummary{
			display: "no location data",
			rawJSON: rawJSON,
			hasJSON: true,
		}

		var payload struct {
			CountryCode string `json:"country_code"`
			Region      string `json:"region"`
		}
		if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
			summary.display = "invalid JSON"
			return summary
		}

		parts := make([]string, 0, 2)
		if payload.CountryCode != "" {
			parts = append(parts, payload.CountryCode)
		}
		if payload.Region != "" {
			parts = append(parts, payload.Region)
		}
		if len(parts) > 0 {
			summary.display = strings.Join(parts, " / ")
		}

		return summary
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Signup Rejections</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 20px; }
table { border-collapse: collapse; width: 100%%; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
.section { margin: 20px 0; }
h2 { border-bottom: 1px solid #ccc; padding-bottom: 5px; }
.add-form { background: #f9f9f9; padding: 15px; border-radius: 5px; margin: 10px 0; }
.add-form input[type="text"] { padding: 8px; width: 300px; }
.add-form input[type="submit"] { padding: 8px 16px; background: #007bff; color: white; border: none; cursor: pointer; border-radius: 3px; }
.add-form input[type="submit"]:hover { background: #0056b3; }
.delete-btn { background: #dc3545; color: white; border: none; padding: 4px 8px; cursor: pointer; border-radius: 3px; }
.delete-btn:hover { background: #c82333; }
.json-btn { background: none; border: none; color: #007bff; cursor: pointer; padding: 0; font: inherit; text-decoration: underline; }
.json-btn:hover { color: #0056b3; }
.json-btn:disabled { color: #6c757d; cursor: default; text-decoration: none; }
</style>
</head><body>
<h1>Signup Rejections & Bypass</h1>
<p><a href="/debug">/debug</a></p>

<div class="section">
<h2>Email Quality Bypass List</h2>
<p>Emails in this list bypass IP abuse checks and email quality checks.</p>

<div class="add-form">
<form method="post" action="/debug/signup-reject">
<input type="hidden" name="action" value="add">
<input type="text" name="email" placeholder="email@example.com" required>
<input type="text" name="reason" placeholder="Reason for bypass" required>
<input type="submit" value="Add to Bypass List">
</form>
</div>

<table>
<tr><th>Email</th><th>Reason</th><th>Added At</th><th>Added By</th><th>Action</th></tr>
`)

	for _, b := range bypassList {
		addedAt := ""
		if b.AddedAt != nil {
			addedAt = b.AddedAt.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, `<tr>
<td>%s</td>
<td>%s</td>
<td>%s</td>
<td>%s</td>
<td>
<form method="post" action="/debug/signup-reject" style="display:inline;">
<input type="hidden" name="action" value="delete">
<input type="hidden" name="email" value="%s">
<button type="submit" class="delete-btn" onclick="return confirm('Remove %s from bypass list?')">Remove</button>
</form>
</td>
</tr>
`, html.EscapeString(b.Email), html.EscapeString(b.Reason), addedAt, html.EscapeString(b.AddedBy),
			html.EscapeString(b.Email), html.EscapeString(b.Email))
	}

	if len(bypassList) == 0 {
		fmt.Fprintf(w, "<tr><td colspan='5'>No emails in bypass list</td></tr>\n")
	}

	fmt.Fprintf(w, `</table>
</div>

<div class="section">
<h2>Recent Signup Rejections (last 200)</h2>
<table>
<tr><th>Email</th><th>IP</th><th>Country/Region</th><th>Reason</th><th>Source</th><th>Rejected At</th><th>Action</th></tr>
`)

	for _, r := range rejections {
		rejectedAt := ""
		if r.RejectedAt != nil {
			rejectedAt = r.RejectedAt.Format("2006-01-02 15:04:05")
		}
		ipqs := summarizeIPQS(r.IpqsResponseJson)
		ipqsCell := html.EscapeString(ipqs.display)
		if ipqs.hasJSON {
			ipqsCell = fmt.Sprintf(`<button type="button" class="json-btn" data-json="%s">%s</button>`,
				html.EscapeString(ipqs.rawJSON), html.EscapeString(ipqs.display))
		}
		// Check if this email is already in the bypass list
		alreadyBypassed := false
		for _, b := range bypassList {
			if b.Email == r.Email {
				alreadyBypassed = true
				break
			}
		}
		actionCell := ""
		if !alreadyBypassed {
			actionCell = fmt.Sprintf(`<form method="post" action="/debug/signup-reject" style="display:inline;">
<input type="hidden" name="action" value="add">
<input type="hidden" name="email" value="%s">
<input type="hidden" name="reason" value="Added from rejection list">
<button type="submit" style="padding: 4px 8px; cursor: pointer;">Bypass</button>
</form>`, html.EscapeString(r.Email))
		} else {
			actionCell = "<em>bypassed</em>"
		}
		fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>\n",
			html.EscapeString(r.Email), html.EscapeString(r.Ip), ipqsCell, html.EscapeString(r.Reason),
			html.EscapeString(r.Source), rejectedAt, actionCell)
	}

	if len(rejections) == 0 {
		fmt.Fprintf(w, "<tr><td colspan='7'>No rejections recorded</td></tr>\n")
	}

	fmt.Fprintf(w, `</table>
</div>

<script>
document.addEventListener('DOMContentLoaded', function() {
  document.querySelectorAll('.json-btn').forEach(function(btn) {
    btn.addEventListener('click', function() {
      var raw = btn.getAttribute('data-json');
      if (!raw) {
        return;
      }
      try {
        var parsed = JSON.parse(raw);
        alert(JSON.stringify(parsed, null, 2));
      } catch (err) {
        alert(raw);
      }
    });
  });
});
</script>

</body></html>
`)
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
	systemCodes, err := withRxRes0(s, ctx, (*exedb.Queries).ListUnusedSystemInviteCodes)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list system codes: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>Invite Codes</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 20px; }
table { border-collapse: collapse; margin: 10px 0; }
th, td { border: 1px solid #ccc; padding: 8px; text-align: left; }
th { background: #f5f5f5; }
.section { margin: 20px 0; }
h2 { border-bottom: 1px solid #ccc; padding-bottom: 5px; }
.code { font-family: monospace; background: #f5f5f5; padding: 2px 6px; border-radius: 3px; }
.create-btn { background: #28a745; color: white; border: none; padding: 10px 20px; cursor: pointer; border-radius: 5px; font-size: 14px; }
.create-btn:hover { background: #218838; }
select, input[type="number"] { padding: 8px; margin: 5px; }
</style>
</head><body>
<h1>Invite Codes</h1>
<p><a href="/debug">/debug</a></p>

<div class="section">
<h2>Create System Invite Code</h2>
<form method="post" action="/debug/invite">
<input type="hidden" name="action" value="create">
<p>
<label>Plan type:
<select name="plan_type">
<option value="trial">Trial (1 month free)</option>
<option value="free">Free forever</option>
</select>
</label>
</p>
<p>
<label>For (optional):
<input type="text" name="assigned_for" placeholder="e.g. John Doe, friend referral">
</label>
</p>
<p><button type="submit" class="create-btn">Create Invite Code</button></p>
</form>
</div>

<div class="section">
<h2>Give Invites to User</h2>
<form method="post" action="/debug/invite">
<input type="hidden" name="action" value="give_to_user">
<p>
<label>Email:
<input type="email" name="email" placeholder="user@example.com" required>
</label>
</p>
<p>
<label>Number of invites:
<input type="number" name="count" value="3" min="1" max="10" required>
</label>
</p>
<p>
<label>Plan type:
<select name="plan_type">
<option value="trial">Trial (1 month free)</option>
<option value="free">Free forever</option>
</select>
</label>
</p>
<p><button type="submit" class="create-btn">Give Invites</button></p>
</form>
</div>

<div class="section">
<h2>Unused System Invite Codes (%d)</h2>
`, len(systemCodes))

	if len(systemCodes) == 0 {
		fmt.Fprintf(w, "<p>No unused system invite codes.</p>\n")
	} else {
		fmt.Fprintf(w, `<table>
<tr><th>Code</th><th>Plan Type</th><th>Created By</th><th>For</th><th>Created</th></tr>
`)
		for _, code := range systemCodes {
			createdAt := "unknown"
			if code.AssignedAt != nil {
				createdAt = code.AssignedAt.Format("2006-01-02 15:04")
			}
			assignedFor := ""
			if code.AssignedFor != nil {
				assignedFor = *code.AssignedFor
			}
			fmt.Fprintf(w, `<tr><td class="code">%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>
`, html.EscapeString(code.Code), html.EscapeString(code.PlanType), html.EscapeString(code.AssignedBy), html.EscapeString(assignedFor), createdAt)
		}
		fmt.Fprintf(w, "</table>\n")
	}

	fmt.Fprintf(w, `</div>
</body></html>
`)
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
	email := r.FormValue("email")
	if email == "" {
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
	user, err := s.GetUserByEmail(ctx, email)
	if err != nil || user == nil {
		http.Error(w, fmt.Sprintf("user not found: %s", email), http.StatusBadRequest)
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
			"user_email", email,
			"user_id", user.UserID)
	}

	http.Redirect(w, r, "/debug/invite", http.StatusSeeOther)
}
