package execore

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// startupProfileInfo is the metadata for a single startup CPU profile.
// The profile bytes themselves live on disk at Path (see the -profile-startup
// flag in cmd/exed); the debug handlers stream from there.
type startupProfileInfo struct {
	Path      string
	StartedAt time.Time
	Duration  time.Duration
	Done      bool
	ErrMsg    string
}

var (
	startupProfileMu sync.Mutex
	currentProfile   startupProfileInfo // guarded by startupProfileMu; zero-value == no profile
)

// BeginStartupProfile is called by cmd/exed when -profile-startup starts a
// CPU profile. The given path must be the file that pprof.StartCPUProfile
// is writing to.
func BeginStartupProfile(path string, startedAt time.Time, duration time.Duration) {
	startupProfileMu.Lock()
	defer startupProfileMu.Unlock()
	currentProfile = startupProfileInfo{
		Path:      path,
		StartedAt: startedAt,
		Duration:  duration,
	}
}

// FinishStartupProfile is called by cmd/exed after pprof.StopCPUProfile and
// the file has been closed.
func FinishStartupProfile(err error) {
	startupProfileMu.Lock()
	defer startupProfileMu.Unlock()
	currentProfile.Done = true
	if err != nil {
		currentProfile.ErrMsg = err.Error()
	}
}

// getStartupProfile returns a copy of the current startup profile info, and
// ok=false if no profile has been started.
func getStartupProfile() (startupProfileInfo, bool) {
	startupProfileMu.Lock()
	defer startupProfileMu.Unlock()
	if currentProfile.StartedAt.IsZero() {
		return startupProfileInfo{}, false
	}
	return currentProfile, true
}

func (s *Server) handleDebugStartupProfile(w http.ResponseWriter, r *http.Request) {
	info, ok := getStartupProfile()
	switch {
	case !ok:
		http.Error(w, "no startup profile captured (start exed with -profile-startup=<duration> to enable)", http.StatusNotFound)
		return
	case !info.Done:
		remaining := time.Until(info.StartedAt.Add(info.Duration))
		if remaining < 0 {
			remaining = 0
		}
		http.Error(w, fmt.Sprintf("startup profile still in progress; try again in %s", remaining.Round(time.Second)), http.StatusServiceUnavailable)
		return
	case info.ErrMsg != "":
		http.Error(w, "startup profile error: "+info.ErrMsg, http.StatusInternalServerError)
		return
	}
	f, err := os.Open(info.Path)
	if err != nil {
		http.Error(w, "open startup profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "stat startup profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="exed-startup-%s.pprof"`, info.StartedAt.UTC().Format("20060102T150405Z")))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", st.Size()))
	_, _ = io.Copy(w, f)
}

// handleDebugStartupProfileView renders an HTML page that embeds speedscope
// (loaded from jsdelivr's CDN) visualizing the captured startup profile.
func (s *Server) handleDebugStartupProfileView(w http.ResponseWriter, r *http.Request) {
	info, ok := getStartupProfile()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	switch {
	case !ok:
		fmt.Fprint(w, `<!doctype html><meta charset=utf-8><title>startup profile</title>
<h1>No startup profile</h1>
<p>Restart <code>exed</code> with <code>-profile-startup=20s</code> to capture a startup CPU profile.</p>`)
	case !info.Done:
		remaining := time.Until(info.StartedAt.Add(info.Duration)).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><meta http-equiv=refresh content=2><title>startup profile (in progress)</title>
<h1>Startup profile in progress</h1>
<p>Started %s, duration %s, %s remaining. This page auto-refreshes.</p>`,
			info.StartedAt.Format(time.RFC3339), info.Duration, remaining)
	case info.ErrMsg != "":
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>startup profile error</title><h1>Startup profile error</h1><pre>%s</pre>`, info.ErrMsg)
	default:
		fmt.Fprint(w, startupProfileViewerHTML)
	}
}

// startupProfileViewerHTML fetches speedscope's prebuilt SPA from jsdelivr,
// rewrites its asset URLs via <base>, pre-seeds location.hash with a
// profileURL pointing back at /debug/startup-profile, and embeds it via
// iframe srcdoc so the iframe is same-origin with this page (and can
// therefore fetch the profile file).
const startupProfileViewerHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>exed startup profile</title>
<style>
  html, body { margin: 0; padding: 0; height: 100%; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
  header { padding: 8px 12px; background: #222; color: #eee; display: flex; gap: 16px; align-items: center; font-size: 13px; }
  header a { color: #8cf; }
  iframe { width: 100%; height: calc(100% - 42px); border: 0; }
</style>
</head>
<body>
<header>
  <strong>exed startup profile</strong>
  <a href="/debug/startup-profile" download>download .pprof</a>
  <span id="status">loading speedscope…</span>
</header>
<iframe id="viewer" title="speedscope"></iframe>
<script>
(async () => {
  const status = document.getElementById('status');
  const iframe = document.getElementById('viewer');
  try {
    const base = 'https://cdn.jsdelivr.net/npm/speedscope@1.22.2/dist/release/';
    const htmlResp = await fetch(base + 'index.html');
    if (!htmlResp.ok) throw new Error('fetch speedscope: ' + htmlResp.status);
    let html = await htmlResp.text();
    html = html.replace('<head>', '<head><base href="' + base + '">');
    const profileURL = new URL('/debug/startup-profile', location.href).toString();
    const hash = '#profileURL=' + encodeURIComponent(profileURL);
    const injection = '<scr' + 'ipt>location.hash = ' + JSON.stringify(hash) + ';</scr' + 'ipt>';
    html = html.replace('<body>', '<body>' + injection);
    iframe.addEventListener('load', () => { status.textContent = 'profile URL: ' + profileURL; }, { once: true });
    iframe.srcdoc = html;
  } catch (e) {
    status.textContent = 'error: ' + e.message;
    console.error(e);
  }
})();
</script>
</body>
</html>
`
