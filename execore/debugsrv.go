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
	mux.HandleFunc("POST /debug/boxes/delete", s.handleDebugBoxDelete)

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
					fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td><button class='delete-btn' data-box='%s'>Delete</button></td></tr>\n",
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
