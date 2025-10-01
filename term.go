package exe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"exe.dev/container"
	"exe.dev/ctrhosttest"
	"exe.dev/exedb"
	"github.com/creack/pty"
)

// TerminalSession represents a terminal session with its PTY and event channels
type TerminalSession struct {
	pty               *os.File
	Cmd               *exec.Cmd
	sshClient         *ssh.Client
	sshSession        *ssh.Session
	sshStdin          io.WriteCloser
	EventsClients     map[chan []byte]bool
	LastEventClientID int
	EventsMutex       sync.Mutex
	LastActivity      time.Time
	BoxName           string
	UserID            string
}

// TerminalMessage represents a message sent from the client for terminal resize events
type TerminalMessage struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Global terminal session storage
var (
	terminalSessions      = make(map[string]*TerminalSession)
	terminalSessionsMutex sync.RWMutex
	cleanupTicker         *time.Ticker
	cleanupDone           chan bool
)

// Initialize terminal cleanup on package init
func init() {
	// Start cleanup goroutine that runs every minute
	cleanupTicker = time.NewTicker(1 * time.Minute)
	cleanupDone = make(chan bool)
	go terminalCleanupLoop()
}

// terminalCleanupLoop removes inactive terminal sessions
func terminalCleanupLoop() {
	for {
		select {
		case <-cleanupTicker.C:
			cleanupInactiveTerminals()
		case <-cleanupDone:
			return
		}
	}
}

// cleanupInactiveTerminals removes terminals that have been inactive for more than 10 minutes
func cleanupInactiveTerminals() {
	terminalSessionsMutex.Lock()
	defer terminalSessionsMutex.Unlock()

	cutoff := time.Now().Add(-10 * time.Minute)
	for sessionID, session := range terminalSessions {
		if session.LastActivity.Before(cutoff) {
			slog.Info("Cleaning up inactive terminal session", "session_id", sessionID)
			cleanupTerminalSession(session)
			delete(terminalSessions, sessionID)
		}
	}
}

// cleanupTerminalSession properly closes all resources for a terminal session
func cleanupTerminalSession(session *TerminalSession) {
	// Close PTY if it exists
	if session.pty != nil {
		session.pty.Close()
	}

	// Kill process if it exists
	if session.Cmd != nil && session.Cmd.Process != nil {
		session.Cmd.Process.Kill()
		session.Cmd.Wait()
	}

	// Close SSH session and client if they exist
	if session.sshSession != nil {
		session.sshSession.Close()
	}
	if session.sshClient != nil {
		session.sshClient.Close()
	}

	// Client channels are closed when the client HTTP connection goes away,
	// though maybe we should send them a little signal...
}

// handleTerminalPage serves the terminal HTML page
func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Serve the terminal HTML page
	s.serveStaticFile(w, r, "terminal.html")
}

// handleTerminalEvents handles SSE connections for terminal output
func (s *Server) handleTerminalEvents(w http.ResponseWriter, r *http.Request) {
	// Extract terminal ID from URL path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid terminal ID", http.StatusBadRequest)
		return
	}
	terminalID := parts[3]

	// Get user authentication from auth cookie
	userID, err := s.getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Get box name from subdomain
	boxName, err := s.parseTerminalHostname(r.Host)
	if err != nil {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Create session key combining user, box, and terminal ID
	sessionKey := fmt.Sprintf("%s:%s:%s", userID, boxName, terminalID)

	// Get or create terminal session
	terminalSessionsMutex.Lock()
	session, exists := terminalSessions[sessionKey]
	if !exists {
		// Create new terminal session
		// TODO(philip): Don't hold the lock while creating this!
		session, err = s.createTerminalSession(r.Context(), userID, boxName)
		if err != nil {
			terminalSessionsMutex.Unlock()
			http.Error(w, fmt.Sprintf("Failed to create terminal: %v", err), http.StatusInternalServerError)
			return
		}
		terminalSessions[sessionKey] = session
	}
	session.LastActivity = time.Now()
	terminalSessionsMutex.Unlock()

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create channel for this client
	events := make(chan []byte, 4096)

	// Register client
	session.EventsMutex.Lock()
	clientID := session.LastEventClientID + 1
	session.LastEventClientID = clientID
	session.EventsClients[events] = true
	session.EventsMutex.Unlock()

	// Cleanup when client disconnects
	defer func() {
		session.EventsMutex.Lock()
		delete(session.EventsClients, events)
		close(events)
		session.EventsMutex.Unlock()
	}()

	// Flush headers immediately
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Send events to client
	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-events:
			// Send as base64 encoded SSE event
			fmt.Fprintf(w, "data: %s\n\n", base64.StdEncoding.EncodeToString(data))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}

// handleTerminalInput processes input to the terminal
func (s *Server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract terminal ID from URL path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid terminal ID", http.StatusBadRequest)
		return
	}
	terminalID := parts[3]

	// Get user authentication
	userID, err := s.getUserIDFromRequest(r)
	if err != nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Get box name from subdomain
	boxName, err := s.parseTerminalHostname(r.Host)
	if err != nil {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Create session key
	sessionKey := fmt.Sprintf("%s:%s:%s", userID, boxName, terminalID)

	// Find terminal session
	terminalSessionsMutex.RLock()
	session, exists := terminalSessions[sessionKey]
	terminalSessionsMutex.RUnlock()

	if !exists {
		http.Error(w, "Terminal session not found", http.StatusNotFound)
		return
	}

	// Update activity time
	session.LastActivity = time.Now()

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// Check if it's a resize message
	if len(body) > 0 && body[0] == '{' {
		var msg TerminalMessage
		if err := json.Unmarshal(body, &msg); err == nil && msg.Type == "resize" {
			if msg.Cols > 0 && msg.Rows > 0 {
				// Handle PTY resize or SSH window change
				if session.pty != nil {
					pty.Setsize(session.pty, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
				} else if session.sshSession != nil {
					_ = session.sshSession.WindowChange(int(msg.Rows), int(msg.Cols))
				}
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}

	// Regular terminal input - send to PTY or SSH stdin
	if session.pty != nil {
		_, err = session.pty.Write(body)
	} else if session.sshStdin != nil {
		_, err = session.sshStdin.Write(body)
	} else {
		err = fmt.Errorf("no active terminal session")
	}

	if err != nil {
		slog.Error("Failed to write to terminal", "error", err)
		http.Error(w, "Failed to write to terminal", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// createTerminalSession creates a new terminal session for a user's box
func (s *Server) createTerminalSession(ctx context.Context, userID, boxName string) (*TerminalSession, error) {
	session := &TerminalSession{
		EventsClients:     make(map[chan []byte]bool),
		LastEventClientID: 0,
		LastActivity:      time.Now(),
		BoxName:           boxName,
		UserID:            userID,
	}

	// Get box information
	box, err := s.boxForNameUserID(ctx, boxName, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get box: %w", err)
	}

	// Check if box is running
	if box.Status != "running" {
		return nil, fmt.Errorf("box is not running (status: %s)", box.Status)
	}

	// Establish SSH shell session
	err = s.createContainerExecSession(session, box)
	if err != nil {
		return nil, fmt.Errorf("failed to create container session: %w", err)
	}

	return session, nil
}

func (s *Server) createContainerExecSession(session *TerminalSession, box *exedb.Box) error {
	// Replaced nerdctl exec with SSH session creation
	if len(box.SSHClientPrivateKey) == 0 || box.SSHPort == nil || box.SSHUser == nil {
		return fmt.Errorf("box missing SSH credentials")
	}
	sshKey, err := container.CreateSSHSigner(string(box.SSHClientPrivateKey))
	if err != nil {
		return fmt.Errorf("failed to parse SSH private key: %w", err)
	}
	sshHost := "localhost"
	ctrhost, err := withRxRes(s, context.Background(), func(ctx context.Context, q *exedb.Queries) (string, error) {
		return q.GetCtrhostByAllocID(ctx, box.AllocID)
	})
	if err == nil && ctrhost != "" {
		if strings.Contains(ctrhost, "://") {
			if u, perr := url.Parse(ctrhost); perr == nil && u.Host != "" {
				if host, _, herr := net.SplitHostPort(u.Host); herr == nil {
					sshHost = host
				} else {
					sshHost = u.Host
				}
			}
		} else {
			sshHost = ctrhost
		}
	}
	if s.devMode != "" {
		if _, herr := net.LookupHost(sshHost); herr != nil {
			if ip := ctrhosttest.ResolveHostFromSSHConfig(sshHost); ip != "" {
				slog.Debug("[TERMINAL] Resolved host via SSH config", "alias", sshHost, "ip", ip)
				sshHost = ip
			}
		}
	}
	sshConfig := &ssh.ClientConfig{
		User: *box.SSHUser, Auth: []ssh.AuthMethod{
			ssh.PublicKeys(sshKey),
		},
		HostKeyCallback: box.CreateHostKeyCallback(),
		Timeout:         10 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", sshHost, *box.SSHPort)
	client, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect via SSH: %w", err)
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return fmt.Errorf("failed to create SSH session: %w", err)
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		sess.Close()
		client.Close()
		return fmt.Errorf("failed to request PTY: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}
	if err := sess.Shell(); err != nil {
		sess.Close()
		client.Close()
		return fmt.Errorf("failed to start remote shell: %w", err)
	}
	session.sshClient = client
	session.sshSession = sess
	session.sshStdin = stdin
	go s.readFromSSHSessionAndBroadcast(session, stdout, stderr)
	return nil
}

func (s *Server) readFromSSHSessionAndBroadcast(session *TerminalSession, stdout io.Reader, stderr io.Reader) {
	defer func() {
		cleanupTerminalSession(session)
	}()
	var wg sync.WaitGroup
	fn := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				session.LastActivity = time.Now()
				session.EventsMutex.Lock()
				for ch := range session.EventsClients {
					select {
					case ch <- data:
					default:
					}
				}
				session.EventsMutex.Unlock()
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					slog.Error("Failed reading SSH stream", "error", err)
				}
				return
			}
		}
	}
	wg.Add(2)
	go fn(stdout)
	go fn(stderr)
	wg.Wait()
}

// Helper functions for box management

// getUserIDFromRequest extracts user ID from auth cookie
func (s *Server) getUserIDFromRequest(r *http.Request) (string, error) {
	userID, err := s.validateAuthCookie(r)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return "", fmt.Errorf("no auth cookie")
		}
		return "", fmt.Errorf("invalid auth cookie")
	}

	return userID, nil
}

// isTerminalRequest determines if a request is for a terminal subdomain
func (s *Server) isTerminalRequest(host string) bool {
	// Remove port if present
	hostname := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		hostname = host[:idx]
	}

	// Check for terminal patterns
	if s.devMode != "" {
		// Development mode: box.xterm.localhost
		return strings.HasSuffix(hostname, ".xterm.localhost")
	} else {
		// Production mode: box.xterm.exe.dev
		return strings.HasSuffix(hostname, ".xterm.exe.dev")
	}
}

// handleTerminalRequest handles requests to terminal subdomains
func (s *Server) handleTerminalRequest(w http.ResponseWriter, r *http.Request) {
	slog.Debug("[TERMINAL] Terminal request", "host", r.Host, "path", r.URL.Path)

	// Handle magic auth URL first (before authentication check)
	if r.URL.Path == "/__exe.dev/auth" {
		s.handleMagicAuth(w, r)
		return
	}

	// Check authentication for other paths
	if _, err := s.validateAuthCookie(r); err != nil {
		// Invalid cookie, redirect to auth
		scheme := getScheme(r)
		returnURL := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.String())
		mainDomain := s.getMainDomainWithPort()
		authURL := fmt.Sprintf("%s://%s/auth?redirect=%s&return_host=%s", scheme, mainDomain, url.QueryEscape(returnURL), url.QueryEscape(r.Host))
		http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
		return
	}

	// Route based on path
	path := r.URL.Path
	switch {
	case path == "/":
		// Serve terminal HTML page
		s.handleTerminalPage(w, r)
	case path == "/":
		// Serve terminal HTML page
		s.handleTerminalPage(w, r)
	case strings.HasPrefix(path, "/terminal/events/"):
		// Handle SSE events
		s.handleTerminalEvents(w, r)
	case strings.HasPrefix(path, "/terminal/input/"):
		// Handle terminal input
		s.handleTerminalInput(w, r)
	case path == "/favicon.ico":
		s.serveStaticFile(w, r, "favicon.ico")
	case strings.HasPrefix(path, "/static/"):
		// Serve static files using existing method
		filename := strings.TrimPrefix(path, "/static/")
		s.serveStaticFile(w, r, filename)
	default:
		http.NotFound(w, r)
	}
}

// parseTerminalHostname extracts box name from terminal hostname
func (s *Server) parseTerminalHostname(hostname string) (string, error) {
	// Remove port if present
	if idx := strings.LastIndex(hostname, ":"); idx > 0 {
		hostname = hostname[:idx]
	}

	// Extract box name from hostname
	if s.devMode != "" {
		// Development: box.xterm.localhost
		if strings.HasSuffix(hostname, ".xterm.localhost") {
			boxName := strings.TrimSuffix(hostname, ".xterm.localhost")
			if boxName == "" || strings.Contains(boxName, ".") {
				return "", fmt.Errorf("invalid box name")
			}
			return boxName, nil
		}
	} else {
		// Production: box.xterm.exe.dev
		if strings.HasSuffix(hostname, ".xterm.exe.dev") {
			boxName := strings.TrimSuffix(hostname, ".xterm.exe.dev")
			if boxName == "" || strings.Contains(boxName, ".") {
				return "", fmt.Errorf("invalid box name")
			}
			return boxName, nil
		}
	}

	return "", fmt.Errorf("not a terminal hostname")
}
