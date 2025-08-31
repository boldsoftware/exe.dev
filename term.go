package exe

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"exe.dev/sqlite"
	"github.com/creack/pty"
)

// TerminalSession represents a terminal session with its PTY and event channels
type TerminalSession struct {
	pty               *os.File
	Cmd               *exec.Cmd
	EventsClients     map[chan []byte]bool
	LastEventClientID int
	EventsMutex       sync.Mutex
	LastActivity      time.Time
	MachineName       string
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

	// Close all client channels
	session.EventsMutex.Lock()
	for ch := range session.EventsClients {
		delete(session.EventsClients, ch)
		close(ch)
	}
	session.EventsMutex.Unlock()
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

	// Get machine name from subdomain
	machineName, err := s.parseTerminalHostname(r.Host)
	if err != nil {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Create session key combining user, machine, and terminal ID
	sessionKey := fmt.Sprintf("%s:%s:%s", userID, machineName, terminalID)

	// Get or create terminal session
	terminalSessionsMutex.Lock()
	session, exists := terminalSessions[sessionKey]
	if !exists {
		// Create new terminal session
		session, err = s.createTerminalSession(r.Context(), userID, machineName, terminalID)
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

	// Get machine name from subdomain
	machineName, err := s.parseTerminalHostname(r.Host)
	if err != nil {
		http.Error(w, "Invalid hostname", http.StatusBadRequest)
		return
	}

	// Create session key
	sessionKey := fmt.Sprintf("%s:%s:%s", userID, machineName, terminalID)

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
				// Handle PTY resize
				if session.pty != nil {
					pty.Setsize(session.pty, &pty.Winsize{
						Cols: msg.Cols,
						Rows: msg.Rows,
					})
				}
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}

	// Regular terminal input - send to PTY
	if session.pty != nil {
		_, err = session.pty.Write(body)
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

// createTerminalSession creates a new terminal session for a user's machine
func (s *Server) createTerminalSession(ctx context.Context, userID, machineName, terminalID string) (*TerminalSession, error) {
	session := &TerminalSession{
		EventsClients:     make(map[chan []byte]bool),
		LastEventClientID: 0,
		LastActivity:      time.Now(),
		MachineName:       machineName,
		UserID:            userID,
	}

	// Get machine information
	machine, err := s.getMachineForUserByID(ctx, userID, machineName)
	if err != nil {
		return nil, fmt.Errorf("failed to get machine: %w", err)
	}

	// Check if machine is running
	if machine.Status != "running" {
		// Try to start the machine if it's stopped
		if machine.Status == "exited" || machine.Status == "paused" {
			err = s.startMachine(ctx, machine)
			if err != nil {
				return nil, fmt.Errorf("failed to start machine: %w", err)
			}
			// Wait a moment for the machine to start
			time.Sleep(2 * time.Second)
		} else {
			return nil, fmt.Errorf("machine is in state %s and cannot be accessed", machine.Status)
		}
	}

	// For now, we'll use the container manager's connection interface
	// In a real implementation, you'd set up SSH keys and connect directly
	// This is a simplified version that creates a pseudo-terminal session

	// Use docker exec to create a shell in the container
	err = s.createContainerExecSession(session, machine)
	if err != nil {
		return nil, fmt.Errorf("failed to create container session: %w", err)
	}

	// Start goroutine to read from process and broadcast to clients
	go s.readFromPtyAndBroadcast(session)

	return session, nil
}

// createContainerExecSession creates a docker exec session for the terminal
func (s *Server) createContainerExecSession(session *TerminalSession, machine *Machine) error {
	if machine.ContainerID == nil {
		return fmt.Errorf("machine has no container ID")
	}

	// Create docker exec command
	cmd := exec.Command("docker", "exec", "-it", *machine.ContainerID, "/bin/sh")

	// Create PTY for the command
	ptyFile, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("failed to start pty: %w", err)
	}

	session.pty = ptyFile
	session.Cmd = cmd

	return nil
}

// readFromPtyAndBroadcast reads output from PTY and broadcasts to all connected clients
func (s *Server) readFromPtyAndBroadcast(session *TerminalSession) {
	buf := make([]byte, 4096)
	defer func() {
		// Cleanup when done
		cleanupTerminalSession(session)
	}()

	for {
		n, err := session.pty.Read(buf)
		if err != nil {
			if err != io.EOF {
				slog.Error("Failed to read from pty", "error", err)
			}
			break
		}

		// Update activity time
		session.LastActivity = time.Now()

		// Make copy of data for broadcasting
		data := make([]byte, n)
		copy(data, buf[:n])

		// Broadcast to all connected clients
		session.EventsMutex.Lock()
		for ch := range session.EventsClients {
			select {
			case ch <- data:
			default:
				// Channel is full, drop the message
			}
		}
		session.EventsMutex.Unlock()
	}
}

// Helper functions for machine management

// getMachineForUserByID gets a machine for a user using user ID
func (s *Server) getMachineForUserByID(ctx context.Context, userID, machineName string) (*Machine, error) {
	// Get user's alloc
	alloc, err := s.getUserAlloc(ctx, userID)
	if err != nil || alloc == nil {
		return nil, fmt.Errorf("user has no allocation")
	}

	// Get the machine using the same pattern as existing code
	var machine Machine
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
			SELECT id, alloc_id, name, status, image, container_id,
			       created_by_user_id, created_at, updated_at,
			       last_started_at, docker_host, routes,
			       ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key,
			       ssh_host_certificate, ssh_client_private_key, ssh_port
			FROM machines
			WHERE name = ? AND alloc_id = ?`, machineName, alloc.AllocID).Scan(
			&machine.ID, &machine.AllocID, &machine.Name, &machine.Status,
			&machine.Image, &machine.ContainerID, &machine.CreatedByUserID,
			&machine.CreatedAt, &machine.UpdatedAt, &machine.LastStartedAt,
			&machine.DockerHost, &machine.Routes,
			&machine.SSHServerIdentityKey, &machine.SSHAuthorizedKeys, &machine.SSHCAPublicKey,
			&machine.SSHHostCertificate, &machine.SSHClientPrivateKey, &machine.SSHPort)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("machine '%s' not found or access denied", machineName)
		}
		return nil, fmt.Errorf("database error: %v", err)
	}

	return &machine, nil
}

// startMachine starts a stopped machine
func (s *Server) startMachine(ctx context.Context, machine *Machine) error {
	// Use the container management system to start the machine
	if s.containerManager == nil {
		return fmt.Errorf("container manager not available")
	}

	if machine.ContainerID == nil {
		return fmt.Errorf("machine has no container ID")
	}

	err := s.containerManager.StartContainer(ctx, machine.AllocID, *machine.ContainerID)
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Update machine status in database
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec("UPDATE machines SET status = 'running', updated_at = ? WHERE id = ?",
			time.Now(), machine.ID)
		return err
	})
	return err
}

// getUserIDFromRequest extracts user ID from auth cookie
func (s *Server) getUserIDFromRequest(r *http.Request) (string, error) {
	cookie, err := r.Cookie("exe-auth")
	if err != nil {
		return "", fmt.Errorf("no auth cookie")
	}

	userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
	if err != nil {
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
		// Development mode: machine.xterm.localhost
		return strings.HasSuffix(hostname, ".xterm.localhost")
	} else {
		// Production mode: machine.xterm.exe.dev
		return strings.HasSuffix(hostname, ".xterm.exe.dev")
	}
}

// handleTerminalRequest handles requests to terminal subdomains
func (s *Server) handleTerminalRequest(w http.ResponseWriter, r *http.Request) {
	if !s.quietMode {
		slog.Info("[TERMINAL] Terminal request", "host", r.Host, "path", r.URL.Path)
	}

	// Handle magic auth URL first (before authentication check)
	if r.URL.Path == "/__exe.dev/auth" {
		s.handleMagicAuth(w, r)
		return
	}

	// Check authentication for other paths
	cookie, err := r.Cookie("exe-auth")
	if err != nil || cookie.Value == "" {
		// Not authenticated, redirect to auth with return URL
		scheme := getScheme(r)
		returnURL := fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.String())
		mainDomain := s.getMainDomainWithPort()
		authURL := fmt.Sprintf("%s://%s/auth?redirect=%s&return_host=%s", scheme, mainDomain, url.QueryEscape(returnURL), url.QueryEscape(r.Host))
		http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
		return
	}

	// Validate auth cookie
	_, err = s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
	if err != nil {
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
	case strings.HasPrefix(path, "/static/"):
		// Serve static files using existing method
		filename := strings.TrimPrefix(path, "/static/")
		s.serveStaticFile(w, r, filename)
	default:
		http.NotFound(w, r)
	}
}

// parseTerminalHostname extracts machine name from terminal hostname
func (s *Server) parseTerminalHostname(hostname string) (string, error) {
	// Remove port if present
	if idx := strings.LastIndex(hostname, ":"); idx > 0 {
		hostname = hostname[:idx]
	}

	// Extract machine name from hostname
	if s.devMode != "" {
		// Development: machine.xterm.localhost
		if strings.HasSuffix(hostname, ".xterm.localhost") {
			machineName := strings.TrimSuffix(hostname, ".xterm.localhost")
			if machineName == "" || strings.Contains(machineName, ".") {
				return "", fmt.Errorf("invalid machine name")
			}
			return machineName, nil
		}
	} else {
		// Production: machine.xterm.exe.dev
		if strings.HasSuffix(hostname, ".xterm.exe.dev") {
			machineName := strings.TrimSuffix(hostname, ".xterm.exe.dev")
			if machineName == "" || strings.Contains(machineName, ".") {
				return "", fmt.Errorf("invalid machine name")
			}
			return machineName, nil
		}
	}

	return "", fmt.Errorf("not a terminal hostname")
}
