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
	background: #1a1a1a;
	color: #e0e0e0;
}
h1 { margin: 0 0 20px 0; font-size: 24px; color: #fff; }
table {
	width: 100%;
	border-collapse: collapse;
	font-size: 13px;
}
th, td {
	padding: 8px 12px;
	text-align: left;
	border-bottom: 1px solid #333;
}
th {
	background: #252525;
	font-weight: 600;
	position: sticky;
	top: 0;
}
tr:hover { background: #252525; }
.mono { font-family: 'SF Mono', Monaco, monospace; font-size: 12px; }
.error { color: #ff6b6b; }
.success { color: #69db7c; }
.btn {
	background: #333;
	border: 1px solid #444;
	color: #e0e0e0;
	padding: 4px 8px;
	border-radius: 4px;
	cursor: pointer;
	font-size: 12px;
}
.btn:hover { background: #444; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }
.json-viewer {
	background: #1e1e1e;
	border: 1px solid #333;
	border-radius: 4px;
	padding: 12px;
	margin-top: 8px;
	overflow-x: auto;
	max-height: 400px;
	overflow-y: auto;
}
.json-viewer pre {
	margin: 0;
	font-family: 'SF Mono', Monaco, monospace;
	font-size: 12px;
	white-space: pre-wrap;
	word-wrap: break-word;
}
.collapsed { display: none; }
.size { color: #888; font-size: 11px; }
.prefix { color: #ffd43b; }
.dedup-info { color: #74c0fc; font-size: 11px; }
.loading { color: #888; font-style: italic; }
.expand-row { background: #1e1e1e; }
.expand-row td { padding: 0; }
.expand-content { padding: 12px; }
.expand-tabs {
	display: flex;
	gap: 8px;
	margin-bottom: 12px;
}
.tab-btn {
	background: transparent;
	border: 1px solid #444;
	color: #888;
	padding: 6px 12px;
	border-radius: 4px;
	cursor: pointer;
}
.tab-btn.active {
	background: #333;
	color: #fff;
	border-color: #555;
}
.tab-content { display: none; }
.tab-content.active { display: block; }
.model-display { color: #a5d6ff; }
.model-id { color: #888; font-size: 11px; }
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
			<td><button class="btn" onclick="toggleExpand(${req.id})">Expand</button></td>
		` + "`" + `;
		tbody.appendChild(tr);
	}
}

async function toggleExpand(id) {
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
				<div class="expand-tabs">
					<button class="tab-btn active" onclick="showTab(${id}, 'request')">Request</button>
					<button class="tab-btn" onclick="showTab(${id}, 'response')">Response</button>
				</div>
				<div id="tab-request-${id}" class="tab-content active">
					<div class="json-viewer"><pre class="loading">Loading request...</pre></div>
				</div>
				<div id="tab-response-${id}" class="tab-content">
					<div class="json-viewer"><pre class="loading">Loading response...</pre></div>
				</div>
			</div>
		</td>
	` + "`" + `;
	row.after(expandRow);

	// Load request body
	loadBody(id, 'request');
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
		const container = document.querySelector('#tab-' + type + '-' + id + ' pre');
		if (container) {
			container.className = 'error';
			container.textContent = 'Error loading: ' + e.message;
		}
	}
}

function renderBody(id, type, data) {
	const container = document.querySelector('#tab-' + type + '-' + id + ' pre');
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

function showTab(id, tab) {
	// Update tab buttons
	const expandRow = document.getElementById('expand-' + id);
	if (!expandRow) return;

	expandRow.querySelectorAll('.tab-btn').forEach(btn => {
		btn.classList.remove('active');
		if (btn.textContent.toLowerCase() === tab) {
			btn.classList.add('active');
		}
	});

	// Update tab content
	expandRow.querySelectorAll('.tab-content').forEach(content => {
		content.classList.remove('active');
	});
	const activeTab = document.getElementById('tab-' + tab + '-' + id);
	if (activeTab) {
		activeTab.classList.add('active');
		loadBody(id, tab);
	}
}

// Add syntax highlighting styles
const style = document.createElement('style');
style.textContent = ` + "`" + `
	.string { color: #98c379; }
	.number { color: #d19a66; }
	.boolean { color: #56b6c2; }
	.null { color: #c678dd; }
	.key { color: #e06c75; }
` + "`" + `;
document.head.appendChild(style);

loadRequests();
</script>
</body>
</html>
`
