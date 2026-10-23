package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/boldsoftware/exe.dev/exe-tty/exescroll"
	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// web/dist is the built web UI (make web). Only .gitkeep is committed, so the
// server still builds, and answers 503, without it.
//
//go:embed all:web/dist
var webFS embed.FS

const (
	// closeShellExited is the WebSocket close code for "the session ended";
	// any other close tells the browser to reconnect. See API.md.
	closeShellExited websocket.StatusCode = 4001
	maxUpload                             = 1 << 30
)

var (
	sessionName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	// Variables describing the terminal exe-tty itself runs in. Shells run
	// in the browser's terminal instead, so these would only mislead them.
	outerTerminal = regexp.MustCompile(`^(TERM|TERM_PROGRAM|TERM_PROGRAM_VERSION|TERM_SESSION_ID|TERMINFO|TERMINAL_EMULATOR|COLORTERM|COLUMNS|LINES|TMUX|TMUX_PANE|ZELLIJ|STY|WINDOWID|VTE_VERSION|XTERM_VERSION|WT_SESSION|WT_PROFILE_ID|SSH_TTY|INSIDE_EMACS|GIT_ASKPASS|LC_TERMINAL|LC_TERMINAL_VERSION|(KITTY|ITERM|ALACRITTY|WEZTERM|GHOSTTY|KONSOLE|ZELLIJ|GNOME_TERMINAL|VSCODE)_[A-Z0-9_]*)=`)
)

type server struct {
	token  string
	cookie string        // name of the login cookie
	stall  time.Duration // how long a shell may ignore input before we detach
	dir    string        // session sockets
	self   string        // this executable, run as `exe-tty exe-scroll ...`
	home   string
	web    fs.FS
	h      http.Handler
}

func newServer(token string) (*server, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir, err := sessionDir()
	if err != nil {
		return nil, err
	}
	web, err := fs.Sub(webFS, "web/dist")
	if err != nil {
		return nil, err
	}
	// Cookies ignore ports, so each server needs its own cookie name for
	// several of them to share a host.
	sum := sha256.Sum256([]byte(token))
	cookie := "exe-tty-" + hex.EncodeToString(sum[:4])
	s := &server{token: token, cookie: cookie, stall: 10 * time.Second, dir: dir, self: self, home: home, web: web}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /assets/", s.assets)
	mux.HandleFunc("GET /terminal/ws/{id}", s.terminal)
	mux.HandleFunc("GET /terminal/sessions", s.listSessions)
	mux.HandleFunc("POST /terminal/sessions/close", s.closeSession)
	mux.HandleFunc("POST /terminal/upload", s.upload)
	s.h = http.NewCrossOriginProtection().Handler(mux)
	return s, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if r.URL.Path != "/login" && !s.authorized(r) {
		http.Error(w, "exe-tty: open the login link the server printed", http.StatusUnauthorized)
		return
	}
	s.h.ServeHTTP(w, r)
}

func (s *server) validToken(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

func (s *server) authorized(r *http.Request) bool {
	c, err := r.Cookie(s.cookie)
	return err == nil && s.validToken(c.Value)
}

// login trades the magic link's token for a cookie.
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.URL.Query().Get("token")) {
		http.Error(w, "exe-tty: bad login link", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookie,
		Value:    s.token,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.web, "index.html")
	if err != nil {
		http.Error(w, "exe-tty: web UI not built (run make)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

// assets serves the web UI's built files, whose names carry content hashes.
func (s *server) assets(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.FileServerFS(s.web).ServeHTTP(w, r)
}

func loginShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// sessionDir returns this user's private directory of session sockets.
func sessionDir() (string, error) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("exe-tty-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is not a private directory owned by this user", dir)
	}
	return dir, nil
}

func (s *server) socket(name string) string {
	return filepath.Join(s.dir, name+".sock")
}

// expand resolves a client-supplied directory: "" is def, ~ and relative
// paths are under $HOME, and absolute paths are used as is.
func (s *server) expand(d, def string) string {
	switch {
	case d == "":
		return def
	case d == "~":
		return s.home
	case strings.HasPrefix(d, "~/"):
		return filepath.Join(s.home, d[2:])
	case filepath.IsAbs(d):
		return filepath.Clean(d)
	default:
		return filepath.Join(s.home, d)
	}
}

type message struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

func dimension(v string, def uint16) uint16 {
	n, err := strconv.Atoi(v)
	if err != nil || n < 2 || n > 1000 {
		return def
	}
	return uint16(n)
}

// terminal attaches a WebSocket to a session, creating it if need be. Each
// attachment is an exe-scroll attach client on its own PTY; closing the
// socket detaches it and leaves the session running.
func (s *server) terminal(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("name")
	if !sessionName.MatchString(name) {
		http.Error(w, "bad session name", http.StatusBadRequest)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 10) // see "input" in API.md
	ctx := r.Context()

	cmd := exec.Command(s.self, "exe-scroll", s.socket(name), "--", loginShell(), "-l")
	cmd.Dir = s.home
	if d := s.expand(q.Get("d"), s.home); d != s.home {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			cmd.Dir = d
		}
	}
	for _, kv := range os.Environ() {
		if !outerTerminal.MatchString(kv) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	if err := exescroll.ConfigureCommand(cmd); err != nil {
		conn.Close(websocket.StatusInternalError, "exe-scroll unavailable")
		return
	}
	size := &pty.Winsize{Cols: dimension(q.Get("cols"), 80), Rows: dimension(q.Get("rows"), 24)}
	tty, err := pty.StartWithSize(cmd, size)
	if err != nil {
		conn.Close(websocket.StatusInternalError, "could not start session")
		return
	}
	defer tty.Close()
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	// PTY -> browser. Ends when the attach client exits.
	output := make(chan struct{})
	go func() {
		defer close(output)
		buf := make([]byte, 4096)
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				b, _ := json.Marshal(message{Type: "output", Data: base64.StdEncoding.EncodeToString(buf[:n])})
				if conn.Write(ctx, websocket.MessageText, b) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Browser -> PTY. Ends when the browser goes away, which only this loop
	// notices, so keystrokes go through a queue that it gives up on if the
	// shell stops taking them.
	keys := make(chan []byte, 64)
	go func() {
		for b := range keys {
			tty.Write(b)
		}
	}()
	input := make(chan struct{})
	go func() {
		defer close(input)
		defer close(keys)
		pong, _ := json.Marshal(message{Type: "pong"})
		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m message
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			switch m.Type {
			case "input":
				select {
				case keys <- []byte(m.Data):
				case <-time.After(s.stall):
					return
				}
			case "resize":
				if m.Cols > 0 && m.Rows > 0 {
					pty.Setsize(tty, &pty.Winsize{Cols: m.Cols, Rows: m.Rows})
				}
				conn.Write(ctx, websocket.MessageText, pong)
			case "ping":
				conn.Write(ctx, websocket.MessageText, pong)
			}
		}
	}()

	select {
	case <-input:
		// Detach, leaving the session running. An attach client stuck
		// writing to the session can't, but killing it is just as good.
		cmd.Process.Signal(syscall.SIGUSR2)
		kill := time.AfterFunc(time.Second, func() { cmd.Process.Kill() })
		<-exited
		kill.Stop()
	case <-exited:
		// The session ended (or was closed). Flush what it printed last.
		select {
		case <-output:
		case <-time.After(time.Second):
		}
		conn.Close(closeShellExited, "session ended")
	}
}

func (s *server) listSessions(w http.ResponseWriter, r *http.Request) {
	type session struct {
		Name string `json:"name"`
		mod  time.Time
	}
	sessions := []session{}
	socks, _ := filepath.Glob(filepath.Join(s.dir, "*.sock"))
	for _, sock := range socks {
		name := strings.TrimSuffix(filepath.Base(sock), ".sock")
		fi, err := os.Stat(sock)
		if err != nil || !sessionName.MatchString(name) {
			continue
		}
		sessions = append(sessions, session{Name: name, mod: fi.ModTime()})
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].mod.Before(sessions[j].mod) })
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *server) closeSession(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if !sessionName.MatchString(name) {
		http.Error(w, "bad session name", http.StatusBadRequest)
		return
	}
	sock := s.socket(name)
	if pid := sessionPID(sock); pid > 0 {
		syscall.Kill(pid, syscall.SIGTERM)
	} else if c, err := net.Dial("unix", sock); err == nil {
		// Alive, but not found: removing its socket would hide it.
		c.Close()
		http.Error(w, "exe-tty: could not find the session's process", http.StatusInternalServerError)
		return
	}
	os.Remove(sock)
	w.WriteHeader(http.StatusNoContent)
}

// sessionPID finds the exe-scroll session server for sock.
func sessionPID(sock string) int {
	return pidOf("exe-scroll: session " + sock)
}

// pidOf finds an exe-scroll process by the title it gives itself (see
// exe-scroll's README).
func pidOf(title string) int {
	if runtime.GOOS == "linux" {
		// Not every ps (BusyBox's, say) takes the flags below.
		dirs, _ := os.ReadDir("/proc")
		for _, d := range dirs {
			b, _ := os.ReadFile("/proc/" + d.Name() + "/cmdline")
			if cmd, _, _ := strings.Cut(string(b), "\x00"); cmd == title {
				pid, _ := strconv.Atoi(d.Name())
				return pid
			}
		}
		return 0
	}
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		pid, command, _ := strings.Cut(strings.TrimSpace(line), " ")
		if strings.TrimSpace(command) == title {
			n, _ := strconv.Atoi(pid)
			return n
		}
	}
	return 0
}

// upload writes the request body to ?dir= (default /tmp) as ?filename=,
// never replacing an existing file.
func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir := s.expand(q.Get("dir"), "/tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	f, err := createUnique(dir, cleanFilename(q.Get("filename")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		code := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			code = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, code, map[string]string{"error": "upload failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": f.Name()})
}

func cleanFilename(name string) string {
	name = name[strings.LastIndexAny(name, `/\`)+1:]
	name = strings.Trim(unsafeChars.ReplaceAllString(name, "_"), "._")
	if name == "" {
		name = "upload_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return name
}

func createUnique(dir, name string) (*os.File, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; ; i++ {
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) || i == 10 {
			return f, err
		}
		name = base + "_" + strconv.FormatInt(time.Now().UnixNano(), 36) + ext
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
