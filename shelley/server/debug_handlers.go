package server

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// handleDebugLLMRequests serves the debug page for LLM requests
func (s *Server) handleDebugLLMRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(debugLLMRequestsHTML))
}

// handleDebugLLMRequestsAPI returns recent LLM requests as JSON
func (s *Server) handleDebugLLMRequestsAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := int64(100)
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.ParseInt(limitStr, 10, 64); err == nil && l > 0 {
			limit = l
		}
	}

	requests, err := s.db.ListRecentLLMRequests(ctx, limit)
	if err != nil {
		s.logger.Error("Failed to list LLM requests", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(requests)
}

// handleDebugLLMRequestBody returns the request body for a specific LLM request
func (s *Server) handleDebugLLMRequestBody(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	body, err := s.db.GetLLMRequestBody(ctx, id)
	if err != nil {
		s.logger.Error("Failed to get LLM request body", "error", err, "id", id)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if body == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("null"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(*body))
}

// handleDebugLLMResponseBody returns the response body for a specific LLM request
func (s *Server) handleDebugLLMResponseBody(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	body, err := s.db.GetLLMResponseBody(ctx, id)
	if err != nil {
		s.logger.Error("Failed to get LLM response body", "error", err, "id", id)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if body == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("null"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(*body))
}

// handleDebugLLMRequestBodyFull returns the full reconstructed request body,
// including prefix data from the prefix chain.
func (s *Server) handleDebugLLMRequestBodyFull(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	// Use the existing DB method to reconstruct the full body
	fullBody, err := s.db.GetFullLLMRequestBody(ctx, id)
	if err != nil {
		s.logger.Error("Failed to get full LLM request body", "error", err, "id", id)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if fullBody == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("null"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fullBody))
}

const debugLLMRequestsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Debug: LLM Requests</title>
<style>
* { box-sizing: border-box; }
body {
	font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
	margin: 0;
	padding: 20px;
	background: #fff;
	color: #1a1a1a;
}
h1 { margin: 0 0 20px 0; font-size: 24px; color: #000; }
table {
	width: 100%;
	border-collapse: collapse;
	font-size: 13px;
}
th, td {
	padding: 8px 12px;
	text-align: left;
	border-bottom: 1px solid #e0e0e0;
}
th {
	background: #f5f5f5;
	font-weight: 600;
	position: sticky;
	top: 0;
}
tr:hover { background: #f8f8f8; }
.mono { font-family: 'SF Mono', Monaco, monospace; font-size: 12px; }
.error { color: #d32f2f; }
.success { color: #2e7d32; }
.btn {
	background: #f5f5f5;
	border: 1px solid #ccc;
	color: #333;
	padding: 4px 8px;
	border-radius: 4px;
	cursor: pointer;
	font-size: 12px;
}
.btn:hover { background: #e8e8e8; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }
.btn.active { background: #1976d2; color: #fff; border-color: #1565c0; }
.json-viewer {
	background: #fafafa;
	border: 1px solid #e0e0e0;
	border-radius: 4px;
	padding: 12px;
	overflow-x: auto;
	max-height: 600px;
	overflow-y: auto;
	flex: 1;
	min-width: 0;
}
.json-viewer pre {
	margin: 0;
	font-family: 'SF Mono', Monaco, monospace;
	font-size: 12px;
	white-space: pre-wrap;
	word-wrap: break-word;
}
.collapsed { display: none; }
.size { color: #666; font-size: 11px; }
.prefix { color: #f57c00; }
.dedup-info { color: #1976d2; font-size: 11px; }
.loading { color: #666; font-style: italic; }
.expand-row { background: #fafafa; }
.expand-row td { padding: 0; }
.expand-content { padding: 12px; }
.panels {
	display: flex;
	gap: 16px;
}
.panel {
	flex: 1;
	min-width: 0;
	display: flex;
	flex-direction: column;
}
.panel-header {
	font-weight: 600;
	margin-bottom: 8px;
	color: #333;
	display: flex;
	align-items: center;
	gap: 8px;
}
.panel-header .btn {
	font-size: 11px;
	padding: 2px 6px;
}
.model-display { color: #1976d2; }
.model-id { color: #666; font-size: 11px; }
.string { color: #2e7d32; }
.number { color: #e65100; }
.boolean { color: #0097a7; }
.null { color: #7b1fa2; }
.key { color: #c62828; }
</style>
</head>
<body>
<h1>LLM Requests</h1>
<table id="requests-table">
<thead>
<tr>
	<th>ID</th>
	<th>Time</th>
	<th>Model</th>
	<th>Provider</th>
	<th>Status</th>
	<th>Duration</th>
	<th>Request Size</th>
	<th>Response Size</th>
	<th>Prefix Info</th>
	<th>Actions</th>
</tr>
</thead>
<tbody id="requests-body">
<tr><td colspan="10" class="loading">Loading...</td></tr>
</tbody>
</table>

<script>
const expandedRows = new Set();
const loadedData = {};

function formatSize(bytes) {
	if (bytes === null || bytes === undefined) return '-';
	if (bytes < 1024) return bytes + ' B';
	if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
	return (bytes / (1024 * 1024)).toFixed(2) + ' MB';
}

function formatDate(dateStr) {
	const d = new Date(dateStr);
	return d.toLocaleString();
}

function formatDuration(ms) {
	if (ms === null || ms === undefined) return '-';
	if (ms < 1000) return ms + 'ms';
	return (ms / 1000).toFixed(2) + 's';
}

function formatModel(model, displayName) {
	if (displayName) {
		return '<span class="model-display">' + displayName + '</span> <span class="model-id">(' + model + ')</span>';
	}
	return model;
}

function syntaxHighlight(json) {
	if (typeof json !== 'string') json = JSON.stringify(json, null, 2);
	json = json.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
	return json.replace(/("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g, function (match) {
		let cls = 'number';
		if (/^"/.test(match)) {
			if (/:$/.test(match)) {
				cls = 'key';
			} else {
				cls = 'string';
			}
		} else if (/true|false/.test(match)) {
			cls = 'boolean';
		} else if (/null/.test(match)) {
			cls = 'null';
		}
		return '<span class="' + cls + '">' + match + '</span>';
	});
}

async function loadRequests() {
	try {
		const resp = await fetch('/debug/llm_requests/api?limit=100');
		const data = await resp.json();
		renderTable(data);
	} catch (e) {
		document.getElementById('requests-body').innerHTML =
			'<tr><td colspan="10" class="error">Error loading requests: ' + e.message + '</td></tr>';
	}
}

function renderTable(requests) {
	const tbody = document.getElementById('requests-body');
	if (!requests || requests.length === 0) {
		tbody.innerHTML = '<tr><td colspan="10">No requests found</td></tr>';
		return;
	}
	tbody.innerHTML = '';
	for (const req of requests) {
		const tr = document.createElement('tr');
		tr.id = 'row-' + req.id;

		const statusClass = req.status_code && req.status_code >= 200 && req.status_code < 300 ? 'success' :
			(req.status_code ? 'error' : '');

		let prefixInfo = '-';
		if (req.prefix_request_id) {
			prefixInfo = '<span class="dedup-info">prefix from #' + req.prefix_request_id +
				' (' + formatSize(req.prefix_length) + ')</span>';
		}

		tr.innerHTML = ` + "`" + `
			<td class="mono">${req.id}</td>
			<td>${formatDate(req.created_at)}</td>
			<td>${formatModel(req.model, req.model_display_name)}</td>
			<td>${req.provider}</td>
			<td class="${statusClass}">${req.status_code || '-'}${req.error ? ' ⚠' : ''}</td>
			<td>${formatDuration(req.duration_ms)}</td>
			<td class="size">${formatSize(req.request_body_length)}</td>
			<td class="size">${formatSize(req.response_body_length)}</td>
			<td>${prefixInfo}</td>
			<td><button class="btn" onclick="toggleExpand(${req.id}, ${req.prefix_request_id !== null})">Expand</button></td>
		` + "`" + `;
		tbody.appendChild(tr);
	}
}

async function toggleExpand(id, hasPrefix) {
	const existingExpand = document.getElementById('expand-' + id);
	if (existingExpand) {
		existingExpand.remove();
		expandedRows.delete(id);
		return;
	}

	expandedRows.add(id);
	const row = document.getElementById('row-' + id);
	const expandRow = document.createElement('tr');
	expandRow.id = 'expand-' + id;
	expandRow.className = 'expand-row';
	expandRow.innerHTML = ` + "`" + `
		<td colspan="10">
			<div class="expand-content">
				<div class="panels">
					<div class="panel">
						<div class="panel-header">
							Request
							${hasPrefix ? '<button class="btn" id="toggle-full-' + id + '" onclick="toggleFullRequest(' + id + ')">Show Full</button>' : ''}
						</div>
						<div class="json-viewer" id="request-panel-${id}"><pre class="loading">Loading request...</pre></div>
					</div>
					<div class="panel">
						<div class="panel-header">Response</div>
						<div class="json-viewer" id="response-panel-${id}"><pre class="loading">Loading response...</pre></div>
					</div>
				</div>
			</div>
		</td>
	` + "`" + `;
	row.after(expandRow);

	// Load both request and response
	loadBody(id, 'request');
	loadBody(id, 'response');
}

async function loadBody(id, type) {
	const key = id + '-' + type;
	if (loadedData[key]) {
		renderBody(id, type, loadedData[key]);
		return;
	}

	try {
		const url = type === 'request'
			? '/debug/llm_requests/' + id + '/request'
			: '/debug/llm_requests/' + id + '/response';
		const resp = await fetch(url);
		const text = await resp.text();
		let data;
		try {
			data = JSON.parse(text);
		} catch {
			data = text;
		}
		loadedData[key] = data;
		renderBody(id, type, data);
	} catch (e) {
		const panelId = type === 'request' ? 'request-panel-' + id : 'response-panel-' + id;
		const container = document.querySelector('#' + panelId + ' pre');
		if (container) {
			container.className = 'error';
			container.textContent = 'Error loading: ' + e.message;
		}
	}
}

async function loadFullBody(id) {
	const key = id + '-request-full';
	if (loadedData[key]) {
		return loadedData[key];
	}

	try {
		const resp = await fetch('/debug/llm_requests/' + id + '/request_full');
		const text = await resp.text();
		let data;
		try {
			data = JSON.parse(text);
		} catch {
			data = text;
		}
		loadedData[key] = data;
		return data;
	} catch (e) {
		throw e;
	}
}

async function toggleFullRequest(id) {
	const btn = document.getElementById('toggle-full-' + id);
	if (!btn) return;

	const isShowingFull = btn.classList.contains('active');

	if (isShowingFull) {
		// Switch back to suffix-only
		btn.classList.remove('active');
		btn.textContent = 'Show Full';
		renderBody(id, 'request', loadedData[id + '-request']);
	} else {
		// Load and show full request
		btn.textContent = 'Loading...';
		try {
			const fullData = await loadFullBody(id);
			btn.classList.add('active');
			btn.textContent = 'Show Suffix Only';
			renderBody(id, 'request', fullData);
		} catch (e) {
			btn.textContent = 'Error';
			setTimeout(() => { btn.textContent = 'Show Full'; }, 2000);
		}
	}
}

function renderBody(id, type, data) {
	const panelId = type === 'request' ? 'request-panel-' + id : 'response-panel-' + id;
	const container = document.querySelector('#' + panelId + ' pre');
	if (!container) return;

	if (data === null) {
		container.className = '';
		container.textContent = '(empty)';
		return;
	}

	container.className = '';
	if (typeof data === 'object') {
		container.innerHTML = syntaxHighlight(JSON.stringify(data, null, 2));
	} else {
		container.textContent = data;
	}
}

loadRequests();
</script>
</body>
</html>
`
