package execore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/crypto/ssh"

	"exe.dev/boxname"
	"exe.dev/container"
	"exe.dev/domz"
	"exe.dev/exedb"
)

// TerminalSession represents a terminal session with its event channels
type TerminalSession struct {
	Cmd               *exec.Cmd
	sshClient         *ssh.Client
	sshSession        *ssh.Session
	sshStdin          io.WriteCloser
	EventsClients     map[chan []byte]bool
	LastEventClientID int
	EventsMutex       sync.Mutex
	LastActivity      atomic.Pointer[time.Time]
	BoxName           string
	UserID            string
}

func (ts *TerminalSession) LastActivityTime() time.Time {
	t := ts.LastActivity.Load()
	if t != nil {
		return *t
	}
	return time.Time{}
}

func (ts *TerminalSession) UpdateLastActivity() {
	now := time.Now()
	ts.LastActivity.Store(&now)
}

// TerminalMessage represents a message sent from the client for terminal operations
type TerminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"` // For input messages
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// Global terminal session storage
var (
	cleanupTicker *time.Ticker
	cleanupDone   chan bool

	terminalSessionsMutex sync.RWMutex // protects terminalSessions map
	terminalSessions      = make(map[string]*TerminalSession)
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
		if session.LastActivityTime().Before(cutoff) {
			slog.Info("Cleaning up inactive terminal session", "session_id", sessionID)
			cleanupTerminalSession(session)
			delete(terminalSessions, sessionID)
		}
	}
}

// cleanupTerminalSession properly closes all resources for a terminal session
func cleanupTerminalSession(session *TerminalSession) {
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

func (s *Server) xtermAuthURL(r *http.Request) string {
	returnURL := fmt.Sprintf("%s://%s%s", getScheme(r), r.Host, r.URL.String())
	// Use webBaseURLNoRequest to get the main domain URL without copying the request's port.
	// Terminal requests may come in on non-standard ports, but the main domain always uses default ports.
	authURL := fmt.Sprintf("%s/auth?redirect=%s&return_host=%s", s.webBaseURLNoRequest(), url.QueryEscape(returnURL), url.QueryEscape(r.Host))
	return authURL
}

// withTerminalAuth is middleware that checks authentication and authorization for terminal access
// If successful, it adds auth info to the request context and calls the next handler
// Otherwise, it handles redirects and error pages
func (s *Server) withTerminalAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get box name from subdomain
		boxName, err := s.parseTerminalHostname(r.Host)
		if err != nil {
			http.Error(w, "Invalid hostname", http.StatusBadRequest)
			return
		}

		// Check authentication - terminal requests use exe-auth cookie
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			// Not authenticated - redirect to login
			http.Redirect(w, r, s.xtermAuthURL(r), http.StatusTemporaryRedirect)
			return
		}

		// Check authorization - verify user has access to this box
		_, err = s.boxForNameUserID(r.Context(), boxName, userID)
		if err != nil {
			// User doesn't have access to this box (or it doesn't exist)
			// Show access denied page
			dashboardURL := s.webBaseURLNoRequest()
			data := struct {
				BoxName      string
				DashboardURL string
			}{
				BoxName:      boxName,
				DashboardURL: dashboardURL,
			}
			s.renderTemplate(r.Context(), w, "terminal-access-denied.html", data)
			return
		}

		// Add auth info to context
		ctx := context.WithValue(r.Context(), terminalAuthKey{}, &terminalAuthInfo{
			UserID:  userID,
			BoxName: boxName,
		})
		next(w, r.WithContext(ctx))
	}
}

// getTerminalAuthInfo retrieves terminal auth info from the request context
func getTerminalAuthInfo(r *http.Request) *terminalAuthInfo {
	if info, ok := r.Context().Value(terminalAuthKey{}).(*terminalAuthInfo); ok {
		return info
	}
	return nil
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

// handleTerminalWebSocket handles WebSocket connections for terminal sessions
func (s *Server) handleTerminalWebSocket(w http.ResponseWriter, r *http.Request) {
	// URL path validation (ensure we have /terminal/ws/<anything>)
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "Invalid terminal path", http.StatusBadRequest)
		return
	}

	// Get session name from query parameter
	sessionName := r.URL.Query().Get("name")
	if sessionName == "" {
		http.Error(w, "Missing session name", http.StatusBadRequest)
		return
	}

	// Validate session name (only alphanumeric and dashes)
	if !regexp.MustCompile(`^[a-zA-Z0-9-]+$`).MatchString(sessionName) {
		http.Error(w, "Invalid session name", http.StatusBadRequest)
		return
	}

	// Get auth info from context
	authInfo := getTerminalAuthInfo(r)
	if authInfo == nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	userID := authInfo.UserID
	boxName := authInfo.BoxName

	// Upgrade to websocket
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "Failed to upgrade websocket", "error", err)
		return
	}
	defer conn.Close(websocket.StatusInternalError, "internal error")

	// Create session key combining user, box, and session name
	sessionKey := fmt.Sprintf("%s:%s:%s", userID, boxName, sessionName)

	// Get or create terminal session
	terminalSessionsMutex.RLock()
	session, exists := terminalSessions[sessionKey]
	terminalSessionsMutex.RUnlock()

	needsExtraResize := false
	if !exists {
		// Create new terminal session without holding the lock
		newSession, err := s.createTerminalSession(r.Context(), userID, boxName, sessionName)
		if err != nil {
			conn.Close(websocket.StatusInternalError, fmt.Sprintf("Failed to create terminal: %v", err))
			return
		}

		// Now acquire write lock to store the session
		terminalSessionsMutex.Lock()
		// Check again in case another goroutine created it
		if existingSession, exists := terminalSessions[sessionKey]; exists {
			// Someone else created it, close our new one and use existing
			go cleanupTerminalSession(newSession)
			session = existingSession
		} else {
			terminalSessions[sessionKey] = newSession
			session = newSession
		}
		terminalSessionsMutex.Unlock()
	} else {
		needsExtraResize = true
	}

	session.UpdateLastActivity()

	// Create context for this WebSocket connection
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Create channel for this client's output
	events := make(chan []byte, 4096)
	// These are related to "needsExtraResize"; it's hacky.
	seenFirstEvent := make(chan struct{})
	seenFirstEventClosed := false

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

	// Start goroutine to read output and send to WebSocket
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case data := <-events:
				if !seenFirstEventClosed {
					seenFirstEventClosed = true
					close(seenFirstEvent)
				}
				msg := map[string]interface{}{
					"type": "output",
					"data": base64.StdEncoding.EncodeToString(data),
				}
				err := wsjson.Write(ctx, conn, msg)
				if err != nil {
					if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
						slog.DebugContext(ctx, "Websocket write error", "error", err)
					}
					cancel()
					return
				}
			}
		}
	}()

	// Read messages from WebSocket
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		default:
		}

		var msg TerminalMessage
		err := wsjson.Read(ctx, conn, &msg)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				slog.DebugContext(ctx, "Websocket read error", "error", err)
			}
			return
		}

		session.UpdateLastActivity()

		switch msg.Type {
		case "resize":
			s.slog().InfoContext(ctx, "Terminal resize", "cols", msg.Cols, "rows", msg.Rows)
			if needsExtraResize {
				// This is subtle AND hacky. Empirically, if we're re-connecting
				// (like, the user did a reload), the resize that the UI sends
				// doesn't work, because I think it's the same size as the
				// original.  So let's send an extra resize, with a different size,
				// and that should tickle the underlying thing. Unfortunately, if we don't
				// wait around to see this take effect, something combines the resizes and
				// it doesn't work. So we wait, either for 100ms, or until the first traffic
				// comes over the network.
				err = session.sshSession.WindowChange(int(msg.Rows)+1, int(msg.Cols)+1)
				if err != nil {
					s.slog().WarnContext(ctx, "Failed extra resize", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				case <-seenFirstEvent:
				}
				needsExtraResize = false
			}

			if msg.Cols > 0 && msg.Rows > 0 {
				// Handle SSH window change
				if session.sshSession != nil {
					err = session.sshSession.WindowChange(int(msg.Rows), int(msg.Cols))
					if err != nil {
						s.slog().WarnContext(ctx, "Failed resize", "error", err)
					}
				}
			}
		case "input":
			// Regular terminal input - send to SSH stdin
			if session.sshStdin != nil && msg.Data != "" {
				_, err := session.sshStdin.Write([]byte(msg.Data))
				if err != nil {
					if errors.Is(err, io.EOF) {
						// Session closed, stop processing
						return
					}
					slog.InfoContext(ctx, "Failed to write to terminal", "error", err)
				}
			}
		}
	}
}

// createTerminalSession creates a new terminal session for a user's box
func (s *Server) createTerminalSession(ctx context.Context, userID, boxName, sessionName string) (*TerminalSession, error) {
	session := &TerminalSession{
		EventsClients: make(map[chan []byte]bool),
		BoxName:       boxName,
		UserID:        userID,
	}
	session.UpdateLastActivity()

	// Get box information
	box, err := s.boxForNameUserID(ctx, boxName, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get box: %w", err)
	}

	// Check if box is running
	if box.Status != "running" {
		return nil, fmt.Errorf("VM is not running (status: %s)", box.Status)
	}

	// Establish SSH shell session with dtach
	err = s.createContainerExecSession(session, box, sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to create container session: %w", err)
	}

	return session, nil
}

func (s *Server) createContainerExecSession(session *TerminalSession, box *exedb.Box, sessionName string) error {
	if len(box.SSHClientPrivateKey) == 0 || box.SSHPort == nil || box.SSHUser == nil {
		return fmt.Errorf("VM missing SSH credentials")
	}
	sshKey, err := container.CreateSSHSigner(string(box.SSHClientPrivateKey))
	if err != nil {
		return fmt.Errorf("failed to parse SSH private key: %w", err)
	}
	sshHost := box.SSHHost()
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
	// Run dtach for persistent session
	dtachPath := "/exe.dev/bin/dtach"
	socketPath := fmt.Sprintf("/tmp/xterm-exe.dev-%s.sock", sessionName)
	dtachCmd := fmt.Sprintf("%s -A %s -z -E /bin/bash -i", dtachPath, socketPath)

	if err := sess.Start(dtachCmd); err != nil {
		sess.Close()
		client.Close()
		return fmt.Errorf("failed to start dtach session: %w", err)
	}
	session.sshClient = client
	session.sshSession = sess
	session.sshStdin = stdin
	go s.readFromSSHSessionAndBroadcast(session, stdout, stderr)
	return nil
}

func (s *Server) readFromSSHSessionAndBroadcast(session *TerminalSession, stdout, stderr io.Reader) {
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
				session.UpdateLastActivity()
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

// terminalAuthInfo holds authenticated user and box information
//
//exe:completeinit
type terminalAuthInfo struct {
	UserID  string
	BoxName string
}

// terminalAuthKey is the context key for terminal authentication info
type terminalAuthKey struct{}

// Helper functions for box management

// isTerminalRequest determines if a request is for a terminal subdomain
func (s *Server) isTerminalRequest(host string) bool {
	_, err := s.parseTerminalHostname(host)
	return err == nil
}

// handleTerminalRequest handles requests to terminal subdomains
func (s *Server) handleTerminalRequest(w http.ResponseWriter, r *http.Request) {
	// Handle magic auth URL first (before authentication check)
	if r.URL.Path == "/__exe.dev/auth" {
		s.handleMagicAuth(w, r)
		return
	}

	// Check authentication for other paths
	if _, err := s.validateAuthCookie(r); err != nil {
		// Invalid cookie, redirect to auth
		http.Redirect(w, r, s.xtermAuthURL(r), http.StatusTemporaryRedirect)
		return
	}

	// Route based on path
	path := r.URL.Path
	switch {
	case path == "/":
		// Serve terminal HTML page - requires auth
		s.withTerminalAuth(s.handleTerminalPage)(w, r)
	case strings.HasPrefix(path, "/terminal/ws/"):
		// Handle WebSocket connections - requires auth
		s.withTerminalAuth(s.handleTerminalWebSocket)(w, r)
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
	hostname = domz.Canonicalize(domz.StripPort(hostname))
	if box, ok := s.terminalBoxForBase(hostname); ok {
		return box, nil
	}
	return "", fmt.Errorf("not a terminal hostname")
}

func (s *Server) terminalBoxForBase(hostname string) (string, bool) {
	if hostname == "" {
		return "", false
	}
	boxName, ok := domz.CutBase(hostname, s.env.BoxSub("xterm"))
	if !ok {
		return "", false
	}
	if !boxname.IsValid(boxName) {
		return "", false
	}
	return boxName, true
}
