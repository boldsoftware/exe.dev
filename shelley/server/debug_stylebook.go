package server

import (
	"net/http"
)

func (s *Server) handleDebugStylebook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write([]byte(debugStylebookHTML))
}

const debugStylebookHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Shelley Stylebook</title>
<link rel="stylesheet" href="/styles.css">
<style>
/* Override app layout so the stylebook scrolls normally */
html, body {
  height: auto !important;
  overflow: auto !important;
}
body {
  padding: 2rem;
  max-width: 960px;
  margin: 0 auto;
  background: var(--bg-base) !important;
}

.sb-section {
  margin-bottom: 3rem;
  border-bottom: 1px solid var(--border);
  padding-bottom: 2rem;
}
.sb-section:last-child { border-bottom: none; }
.sb-section h2 {
  font-size: 1.25rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 1.5rem 0;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
}

.sb-row {
  display: flex;
  align-items: baseline;
  gap: 1.5rem;
  margin-bottom: 1rem;
  padding: 0.5rem 0;
}
.sb-label {
  width: 180px;
  flex-shrink: 0;
  font-size: 0.75rem;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
}
.sb-sample {
  flex: 1;
}
.sb-note {
  font-size: 0.75rem;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  width: 200px;
  flex-shrink: 0;
  text-align: right;
}

.sb-color-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 0.75rem;
}
.sb-color-swatch {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.5rem;
  border-radius: 6px;
  border: 1px solid var(--border);
}
.sb-swatch-box {
  width: 32px;
  height: 32px;
  border-radius: 4px;
  border: 1px solid var(--border);
  flex-shrink: 0;
}
.sb-swatch-info {
  font-size: 0.75rem;
  line-height: 1.4;
}
.sb-swatch-name {
  color: var(--text-primary);
  font-weight: 500;
}
.sb-swatch-value {
  color: var(--text-tertiary);
  font-family: var(--font-mono);
}

.sb-alert-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 0.75rem;
}
.sb-alert {
  padding: 0.75rem 1rem;
  border-radius: 6px;
  border: 1px solid;
  font-size: 0.875rem;
}

.sb-dark-toggle {
  position: fixed;
  top: 1rem;
  right: 1rem;
  padding: 0.5rem 1rem;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg-tertiary);
  color: var(--text-primary);
  cursor: pointer;
  font-family: var(--font-mono);
  font-size: 0.75rem;
  z-index: 100;
}
.sb-dark-toggle:hover {
  background: var(--border);
}

.sb-title {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  font-size: 1.5rem;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0 0 0.5rem 0;
}
.sb-subtitle {
  color: var(--text-secondary);
  font-size: 0.875rem;
  margin: 0 0 2rem 0;
}

/* Unique font size inventory table */
.sb-inventory {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8125rem;
  margin-top: 0.5rem;
}
.sb-inventory th {
  text-align: left;
  padding: 0.5rem;
  font-weight: 500;
  color: var(--text-secondary);
  border-bottom: 2px solid var(--border);
  font-size: 0.75rem;
}
.sb-inventory td {
  padding: 0.5rem;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
</style>
</head>
<body>

<button class="sb-dark-toggle" onclick="document.documentElement.classList.toggle('dark');this.textContent=document.documentElement.classList.contains('dark')?'Light mode':'Dark mode'">Dark mode</button>

<h1 class="sb-title">Shelley Stylebook</h1>
<p class="sb-subtitle">Reference for all typographic sizes, weights, colors, and patterns in the UI.</p>

<!-- TYPE SCALE -->
<div class="sb-section">
<h2>Type Scale (5 sizes)</h2>
<div class="sb-row">
  <span class="sb-label">XS — 0.75rem (12px)</span>
  <span class="sb-sample" style="font-size:0.75rem">Labels, badges, timestamps, metadata, captions</span>
  <span class="sb-note">63 uses</span>
</div>
<div class="sb-row">
  <span class="sb-label">SM — 0.875rem (14px)</span>
  <span class="sb-sample" style="font-size:0.875rem">Body text, tool content, sidebar items, buttons</span>
  <span class="sb-note">82 uses</span>
</div>
<div class="sb-row">
  <span class="sb-label">MD — 1rem (16px)</span>
  <span class="sb-sample" style="font-size:1rem">Message input, section headers, emphasized text</span>
  <span class="sb-note">13 uses</span>
</div>
<div class="sb-row">
  <span class="sb-label">LG — 1.25rem (20px)</span>
  <span class="sb-sample" style="font-size:1.25rem">Page titles, conversation header, markdown h2/h3</span>
  <span class="sb-note">11 uses</span>
</div>
<div class="sb-row">
  <span class="sb-label">XL — 1.5rem (24px)</span>
  <span class="sb-sample" style="font-size:1.5rem">Hero / empty-state headings</span>
  <span class="sb-note">1 use</span>
</div>
</div>

<!-- FONT WEIGHTS -->
<div class="sb-section">
<h2>Font Weights</h2>
<div class="sb-row">
  <span class="sb-label">400 (normal)</span>
  <span class="sb-sample" style="font-weight:400;font-size:0.875rem">Regular body text — the default for most content</span>
  <span class="sb-note">7 uses</span>
</div>
<div class="sb-row">
  <span class="sb-label">500 (medium)</span>
  <span class="sb-sample" style="font-weight:500;font-size:0.875rem">Labels, sidebar items, buttons, active states</span>
  <span class="sb-note">38 uses</span>
</div>
<div class="sb-row">
  <span class="sb-label">600 (semibold)</span>
  <span class="sb-sample" style="font-weight:600;font-size:0.875rem">Headings, conversation titles, emphasis</span>
  <span class="sb-note">14 uses</span>
</div>
</div>

<!-- FONT FAMILIES -->
<div class="sb-section">
<h2>Font Families</h2>
<div class="sb-row">
  <span class="sb-label">Mono (body default)</span>
  <span class="sb-sample" style="font-family:var(--font-mono);font-size:0.875rem">SF Mono, Monaco, Cascadia Code, Roboto Mono, Consolas</span>
</div>
<div class="sb-row">
  <span class="sb-label">var(--font-mono)</span>
  <span class="sb-sample" style="font-family:var(--font-mono);font-size:0.875rem">Used in code blocks, tool output, metadata (34 explicit uses + body default)</span>
</div>
<p style="font-size:0.8125rem;color:var(--text-secondary);margin:0.5rem 0 0">
  The entire UI is monospace. This is intentional and distinctive. No sans-serif needed in the app.
</p>
</div>

<!-- TEXT COLORS -->
<div class="sb-section">
<h2>Text Colors</h2>
<div class="sb-color-grid">
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--text-primary)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">text-primary</div><div class="sb-swatch-value">72 uses</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--text-secondary)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">text-secondary</div><div class="sb-swatch-value">84 uses</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--text-tertiary)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">text-tertiary</div><div class="sb-swatch-value">23 uses</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--primary)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">primary</div><div class="sb-swatch-value">#2563eb — 16 uses</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--blue-text)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">blue-text</div><div class="sb-swatch-value">22 uses</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--error-text)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">error-text</div><div class="sb-swatch-value">27 uses</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--success-text)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">success-text</div><div class="sb-swatch-value">13 uses</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--warning-text)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">warning-text</div><div class="sb-swatch-value">1 use</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--link-color)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">link-color</div><div class="sb-swatch-value">3 uses</div></div>
  </div>
</div>
</div>

<!-- BACKGROUND COLORS -->
<div class="sb-section">
<h2>Background Colors</h2>
<div class="sb-color-grid">
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--bg-base)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">bg-base</div><div class="sb-swatch-value">Main panels</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--bg-secondary)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">bg-secondary</div><div class="sb-swatch-value">App background</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--bg-tertiary)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">bg-tertiary</div><div class="sb-swatch-value">Hover, code blocks</div></div>
  </div>
  <div class="sb-color-swatch">
    <div class="sb-swatch-box" style="background:var(--border)"></div>
    <div class="sb-swatch-info"><div class="sb-swatch-name">border</div><div class="sb-swatch-value">Dividers, borders</div></div>
  </div>
</div>
</div>

<!-- SEMANTIC ALERTS -->
<div class="sb-section">
<h2>Semantic Alert Colors</h2>
<div class="sb-alert-grid">
  <div class="sb-alert" style="background:var(--success-bg);border-color:var(--success-border);color:var(--success-text)">
    <strong>Success:</strong> Operation completed
  </div>
  <div class="sb-alert" style="background:var(--error-bg);border-color:var(--error-border);color:var(--error-text)">
    <strong>Error:</strong> Something went wrong
  </div>
  <div class="sb-alert" style="background:var(--warning-bg);border-color:var(--warning-border);color:var(--warning-text)">
    <strong>Warning:</strong> Proceed with caution
  </div>
  <div class="sb-alert" style="background:var(--blue-bg);border-color:var(--blue-border);color:var(--blue-text)">
    <strong>Info:</strong> Informational message
  </div>
</div>
</div>

<!-- LINE HEIGHTS -->
<div class="sb-section">
<h2>Line Heights</h2>
<p style="font-size:0.8125rem;color:var(--text-secondary);margin:0 0 1rem">
  3 line-heights: 1 (icons/badges), 1.4 (compact), 1.6 (body).
</p>
<div class="sb-row">
  <span class="sb-label">line-height: 1</span>
  <span class="sb-sample" style="line-height:1;font-size:0.875rem;background:var(--bg-tertiary);padding:0.25rem 0.5rem;border-radius:4px">Single-line badges and icons</span>
</div>
<div class="sb-row">
  <span class="sb-label">line-height: 1.4</span>
  <span class="sb-sample" style="line-height:1.4;font-size:0.875rem">Compact text areas like sidebar items and tool blocks where vertical space is at a premium. Multi-line text stays tight.</span>
</div>
<div class="sb-row">
  <span class="sb-label">line-height: 1.6</span>
  <span class="sb-sample" style="line-height:1.6;font-size:0.875rem">Main body text and message content. This is the body default and should be used for anything the user reads at length. Comfortable spacing.</span>
</div>
</div>

<!-- LIVE COMPONENT EXAMPLES -->
<div class="sb-section">
<h2>Spacing &amp; Border Radius Tokens</h2>
<p style="font-size:0.8125rem;color:var(--text-secondary);margin:0 0 1rem">
  Common spacing values used throughout the UI.
</p>
<div style="display:flex;gap:1rem;align-items:end;flex-wrap:wrap">
  <div style="text-align:center">
    <div style="width:4px;height:40px;background:var(--primary);margin:0 auto 0.5rem"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary)">0.25rem</span>
  </div>
  <div style="text-align:center">
    <div style="width:8px;height:40px;background:var(--primary);margin:0 auto 0.5rem"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary)">0.5rem</span>
  </div>
  <div style="text-align:center">
    <div style="width:12px;height:40px;background:var(--primary);margin:0 auto 0.5rem"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary)">0.75rem</span>
  </div>
  <div style="text-align:center">
    <div style="width:16px;height:40px;background:var(--primary);margin:0 auto 0.5rem"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary)">1rem</span>
  </div>
  <div style="text-align:center">
    <div style="width:24px;height:40px;background:var(--primary);margin:0 auto 0.5rem"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary)">1.5rem</span>
  </div>
  <div style="text-align:center">
    <div style="width:32px;height:40px;background:var(--primary);margin:0 auto 0.5rem"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary)">2rem</span>
  </div>
</div>
<div style="display:flex;gap:1rem;align-items:center;flex-wrap:wrap;margin-top:1.5rem">
  <div style="text-align:center">
    <div style="width:48px;height:48px;background:var(--bg-tertiary);border:1px solid var(--border);border-radius:4px"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary);display:block;margin-top:0.25rem">4px</span>
  </div>
  <div style="text-align:center">
    <div style="width:48px;height:48px;background:var(--bg-tertiary);border:1px solid var(--border);border-radius:6px"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary);display:block;margin-top:0.25rem">6px</span>
  </div>
  <div style="text-align:center">
    <div style="width:48px;height:48px;background:var(--bg-tertiary);border:1px solid var(--border);border-radius:8px"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary);display:block;margin-top:0.25rem">8px</span>
  </div>
  <div style="text-align:center">
    <div style="width:48px;height:48px;background:var(--bg-tertiary);border:1px solid var(--border);border-radius:12px"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary);display:block;margin-top:0.25rem">12px</span>
  </div>
  <div style="text-align:center">
    <div style="width:48px;height:48px;background:var(--bg-tertiary);border:1px solid var(--border);border-radius:9999px"></div>
    <span style="font-size:0.75rem;color:var(--text-tertiary);display:block;margin-top:0.25rem">pill</span>
  </div>
</div>
</div>

<!-- SUMMARY -->
<div class="sb-section">
<h2>Summary</h2>
<table class="sb-inventory">
<thead><tr><th>Token</th><th>Values</th></tr></thead>
<tbody>
<tr><td>Font sizes</td><td><strong>5:</strong> 0.75rem, 0.875rem, 1rem, 1.25rem, 1.5rem</td></tr>
<tr><td>Font weights</td><td><strong>3:</strong> 400, 500, 600</td></tr>
<tr><td>Font family</td><td><strong>1:</strong> var(--font-mono)</td></tr>
<tr><td>Line heights</td><td><strong>3:</strong> 1, 1.4, 1.6</td></tr>
<tr><td>Text colors</td><td><strong>9:</strong> semantic CSS variables</td></tr>
</tbody>
</table>
</div>

</body>
</html>
`
