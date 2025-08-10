// Package exe implements the bulk of the exed server.
package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log"
	mathrand "math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keighl/postmark"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentmethod"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"
	
	"exe.dev/container"
)

//go:embed exe_schema.sql
var schemaSQL string

// User represents an individual user
type User struct {
	PublicKeyFingerprint string
	Email                string
	CreatedAt            time.Time
}

// Team represents a team with billing information
type Team struct {
	Name             string
	CreatedAt        time.Time
	StripeCustomerID string
	BillingEmail     string
}

// TeamMember represents membership in a team
type TeamMember struct {
	UserFingerprint string
	TeamName        string
	IsAdmin         bool
	JoinedAt        time.Time
}

// Machine represents a container/VM
type Machine struct {
	ID                   int
	TeamName             string
	Name                 string
	Status               string
	Image                string
	ContainerID          *string
	CreatedByFingerprint string
	CreatedAt            time.Time
	UpdatedAt            time.Time
	LastStartedAt        *time.Time
}

// Invite represents a team invitation
type Invite struct {
	Code                 string
	TeamName             string
	CreatedByFingerprint string
	Email                string // optional
	MaxUses              int
	UsedCount            int
	ExpiresAt            time.Time
	CreatedAt            time.Time
}

// EmailVerification represents a pending email verification (in-memory)
type EmailVerification struct {
	PublicKeyFingerprint string
	Email                string
	Token                string
	CompleteChan         chan struct{}
	CreatedAt            time.Time
}

// BillingVerification represents a pending billing verification (in-memory)
type BillingVerification struct {
	PublicKeyFingerprint string
	TeamName             string
	CompleteChan         chan struct{}
	CreatedAt            time.Time
}

// UserSession represents an active SSH user session
type UserSession struct {
	Fingerprint string
	Email       string
	TeamName    string
	IsAdmin     bool
	CreatedAt   time.Time
}

// Server implements both HTTP and SSH server functionality for exe.dev
type Server struct {
	httpAddr  string
	httpsAddr string
	sshAddr   string
	BaseURL   string
	
	httpServer  *http.Server
	httpsServer *http.Server
	sshConfig   *ssh.ServerConfig
	certManager *autocert.Manager
	
	// Database
	db *sql.DB
	
	// Container management
	containerManager container.Manager
	
	// In-memory state for active sessions (these don't need persistence)
	emailVerificationsMu    sync.RWMutex
	emailVerifications      map[string]*EmailVerification // token -> email verification
	billingVerificationsMu  sync.RWMutex
	billingVerifications    map[string]*BillingVerification // fingerprint -> billing verification
	
	// User sessions for tracking authenticated users
	sessionsMu              sync.RWMutex
	sessions                map[ssh.Channel]*UserSession // channel -> user session
	
	// Email and billing services
	postmarkClient *postmark.Client
	stripeKey      string
	devMode        bool // Development mode - log instead of sending emails
	
	mu       sync.RWMutex
	stopping bool
}

// NewServer creates a new Server instance with database and container management
func NewServer(httpAddr, httpsAddr, sshAddr, dbPath string, devMode bool, gcpProjectID string) (*Server, error) {
	// Initialize database
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	
	// Execute schema
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}
	
	// Initialize Postmark client
	postmarkAPIKey := os.Getenv("POSTMARK_API_KEY")
	var postmarkClient *postmark.Client
	if postmarkAPIKey != "" {
		postmarkClient = postmark.NewClient(postmarkAPIKey, "")
	} else {
		log.Printf("Warning: POSTMARK_API_KEY not set, email verification will not work")
	}
	
	// Get Stripe key
	stripeKey := os.Getenv("STRIPE_API_KEY")
	if stripeKey == "" {
		stripeKey = "sk_test_51QxIgSGWIXq1kJnoiKwEcehJeO68QFsueLGymU9zR5jsJtMup5arFZZlHYaOzG3Bsw2GfnIG9H3Jv8Be10vqK1nW001hUxrS2g"
		log.Printf("Using default Stripe test key")
	}
	stripe.Key = stripeKey
	var baseURL string
	if httpsAddr != "" {
		// HTTPS is configured, use https://exe.dev
		baseURL = "https://exe.dev"
	} else {
		// No HTTPS, use http://localhost with the HTTP port
		baseURL = "http://localhost" + httpAddr
		// If httpAddr doesn't start with :, it might be host:port format
		if httpAddr[0] != ':' {
			// Extract just the port part if it's in host:port format
			parts := strings.Split(httpAddr, ":")
			if len(parts) >= 2 {
				baseURL = "http://localhost:" + parts[len(parts)-1]
			}
		}
	}
	
	// Initialize container manager if GCP project is provided
	var containerManager container.Manager
	if gcpProjectID != "" {
		config := container.DefaultConfig(gcpProjectID)
		containerManager, err = container.NewGKEManager(context.Background(), config)
		if err != nil {
			log.Printf("Warning: Failed to initialize container manager: %v", err)
			log.Printf("Container functionality will be disabled")
			containerManager = nil
		} else {
			log.Printf("Container management enabled for GCP project: %s", gcpProjectID)
		}
	}
	
	s := &Server{
		httpAddr:             httpAddr,
		httpsAddr:            httpsAddr,
		sshAddr:              sshAddr,
		BaseURL:              baseURL,
		db:                   db,
		containerManager:     containerManager,
		emailVerifications:   make(map[string]*EmailVerification),
		billingVerifications: make(map[string]*BillingVerification),
		sessions:             make(map[ssh.Channel]*UserSession),
		postmarkClient:       postmarkClient,
		stripeKey:            stripeKey,
		devMode:              devMode,
	}
	
	s.setupHTTPServer()
	s.setupHTTPSServer()
	s.setupSSHServer()
	
	return s, nil
}

// setupHTTPServer configures the HTTP server
func (s *Server) setupHTTPServer() {
	s.httpServer = &http.Server{
		Addr:    s.httpAddr,
		Handler: s,
	}
}

// setupHTTPSServer configures the HTTPS server with Let's Encrypt if enabled
func (s *Server) setupHTTPSServer() {
	if s.httpsAddr == "" {
		return
	}
	
	// Set up autocert manager for Let's Encrypt
	s.certManager = &autocert.Manager{
		Cache:      autocert.DirCache("certs"),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist("exe.dev"),
	}
	
	s.httpsServer = &http.Server{
		Addr:    s.httpsAddr,
		Handler: s,
		TLSConfig: &tls.Config{
			GetCertificate: s.certManager.GetCertificate,
		},
	}
}

// setupSSHServer configures the SSH server
func (s *Server) setupSSHServer() {
	s.sshConfig = &ssh.ServerConfig{
		PublicKeyCallback: s.authenticatePublicKey,
	}
	
	// Generate a temporary host key for now
	// TODO: Use persistent host keys
	if err := s.generateHostKey(); err != nil {
		log.Printf("Failed to generate host key: %v", err)
	}
}

// generateHostKey generates a temporary RSA host key
func (s *Server) generateHostKey() error {
	// Generate RSA private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate RSA key: %w", err)
	}
	
	// Convert to PEM format
	privateKeyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyDER,
	})
	
	// Parse as SSH private key
	signer, err := ssh.ParsePrivateKey(privateKeyPEM)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}
	
	s.sshConfig.AddHostKey(signer)
	return nil
}

// getPublicKeyFingerprint generates a SHA256 fingerprint for a public key
func (s *Server) getPublicKeyFingerprint(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(hash[:])
}

// generateRegistrationToken creates a random registration token
func (s *Server) generateRegistrationToken() string {
	bytes := make([]byte, 16)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// authenticatePublicKey handles SSH public key authentication
func (s *Server) authenticatePublicKey(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fingerprint := s.getPublicKeyFingerprint(key)
	
	// Check if user exists in database
	user, err := s.getUserByFingerprint(fingerprint)
	if err != nil {
		log.Printf("Database error checking user %s: %v", fingerprint, err)
		// Allow connection but mark as not registered
		return &ssh.Permissions{
			Extensions: map[string]string{
				"fingerprint": fingerprint,
				"registered":  "false",
			},
		}, nil
	}
	
	if user != nil {
		// Check if user has team memberships
		teams, err := s.getUserTeams(fingerprint)
		if err != nil {
			log.Printf("Database error getting teams for user %s: %v", fingerprint, err)
		}
		
		if len(teams) > 0 {
			// User is fully registered with team membership
			return &ssh.Permissions{
				Extensions: map[string]string{
					"fingerprint": fingerprint,
					"registered":  "true",
					"email":       user.Email,
				},
			}, nil
		}
	}
	
	// User is not registered or has no team, allow connection but mark as needing registration
	return &ssh.Permissions{
		Extensions: map[string]string{
			"fingerprint": fingerprint,
			"registered":  "false",
		},
	}, nil
}

// ServeHTTP implements http.Handler for the HTTP server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	stopping := s.stopping
	s.mu.RUnlock()
	
	if stopping {
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	}
	
	// TODO: Wake up containers on HTTP request
	log.Printf("HTTP request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	
	switch r.URL.Path {
	case "/":
		s.handleRoot(w, r)
	case "/health":
		s.handleHealth(w, r)
	case "/containers":
		s.handleContainers(w, r)
	case "/verify-email":
		s.handleEmailVerificationHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleRoot handles requests to the root path
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>exe.dev</title>
</head>
<body>
    <h1>exe.dev</h1>
    <p>Container service with persistent disks</p>
    <p>SSH to exe.dev for console management</p>
</body>
</html>`)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","timestamp":"%s"}`, time.Now().Format(time.RFC3339))
}

// handleContainers handles container management requests
func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// TODO: Implement container listing/management
	fmt.Fprintf(w, `{"containers":[],"message":"Container management not yet implemented"}`)
}

// handleEmailVerificationHTTP handles web-based email verification
func (s *Server) handleEmailVerificationHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Missing token parameter", http.StatusBadRequest)
		return
	}
	
	s.emailVerificationsMu.Lock()
	verification, exists := s.emailVerifications[token]
	if !exists {
		s.emailVerificationsMu.Unlock()
		http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
		return
	}
	
	// Signal completion to SSH session
	close(verification.CompleteChan)
	
	// Clean up email verification
	delete(s.emailVerifications, token)
	s.emailVerificationsMu.Unlock()
	
	// Send success response
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>Email Verified - exe.dev</title>
    <style>
        body { font-family: Arial, sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
        .success { color: #4CAF50; text-align: center; }
        .code { background-color: #f5f5f5; padding: 10px; font-family: monospace; }
    </style>
</head>
<body>
    <h1 class="success">✅ Email Verified!</h1>
    <p>Your email address has been successfully verified.</p>
    <p>You can now return to your SSH session to complete billing setup.</p>
    <p>If you don't have an active SSH session, you can connect with:</p>
    <div class="code">ssh exe.dev</div>
</body>
</html>`)
}

// serveSSH starts the SSH server
func (s *Server) serveSSH() error {
	listener, err := net.Listen("tcp", s.sshAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on SSH port: %w", err)
	}
	defer listener.Close()
	
	log.Printf("SSH server listening on %s", s.sshAddr)
	
	for {
		s.mu.RLock()
		stopping := s.stopping
		s.mu.RUnlock()
		
		if stopping {
			break
		}
		
		conn, err := listener.Accept()
		if err != nil {
			if !stopping {
				log.Printf("SSH accept error: %v", err)
			}
			continue
		}
		
		go s.handleSSHConnection(conn)
	}
	
	return nil
}

// handleSSHConnection handles an individual SSH connection
func (s *Server) handleSSHConnection(conn net.Conn) {
	defer conn.Close()
	
	log.Printf("SSH connection from %s", conn.RemoteAddr())
	
	// TODO: Wake up containers on SSH connection
	
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		log.Printf("SSH handshake failed: %v", err)
		return
	}
	defer sshConn.Close()
	
	fingerprint := sshConn.Permissions.Extensions["fingerprint"]
	registered := sshConn.Permissions.Extensions["registered"] == "true"
	
	log.Printf("SSH connection established for user: %s, fingerprint: %s, registered: %t", 
		sshConn.User(), fingerprint, registered)
	
	go ssh.DiscardRequests(reqs)
	
	for newChannel := range chans {
		go s.handleSSHChannel(newChannel, sshConn.User(), fingerprint, registered)
	}
}

// handleSSHChannel handles SSH channels
func (s *Server) handleSSHChannel(newChannel ssh.NewChannel, username, fingerprint string, registered bool) {
	if newChannel.ChannelType() != "session" {
		newChannel.Reject(ssh.UnknownChannelType, "unsupported channel type")
		return
	}
	
	channel, requests, err := newChannel.Accept()
	if err != nil {
		log.Printf("Could not accept SSH channel: %v", err)
		return
	}
	defer channel.Close()
	
	// Store terminal dimensions when we get them
	var terminalWidth, terminalHeight int
	
	// Handle requests
	for req := range requests {
		switch req.Type {
		case "pty-req":
			// Parse PTY request and set up terminal properly
			if len(req.Payload) > 0 {
				// PTY request format: string term, uint32 cols, uint32 rows, uint32 pixWidth, uint32 pixHeight, string modes
				if cols, rows := s.parsePtyRequest(req.Payload); cols > 0 && rows > 0 {
					terminalWidth, terminalHeight = cols, rows
					log.Printf("SSH PTY dimensions: %dx%d", cols, rows)
				}
				if req.WantReply {
					req.Reply(true, nil)
				}
			} else {
				if req.WantReply {
					req.Reply(false, nil)
				}
			}
		case "shell":
			if req.WantReply {
				req.Reply(true, nil)
			}
			// Handle shell directly, not in a goroutine
			s.handleSSHShellWithDimensions(channel, username, fingerprint, registered, terminalWidth, terminalHeight)
			return // Exit after handling shell
		case "exec":
			if req.WantReply {
				req.Reply(true, nil)
			}
			// TODO: Handle exec commands
			channel.Write([]byte("exec commands not yet implemented\r\n"))
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// parsePtyRequest parses SSH PTY request to extract terminal dimensions
func (s *Server) parsePtyRequest(payload []byte) (cols, rows int) {
	if len(payload) < 12 { // Minimum size for term string + dimensions
		return 0, 0
	}
	
	// Skip terminal type string (4 bytes length + string)
	if len(payload) < 4 {
		return 0, 0
	}
	termTypeLen := int(payload[0])<<24 | int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if len(payload) < 4+termTypeLen+16 { // 4 + term string + 4*uint32
		return 0, 0
	}
	
	offset := 4 + termTypeLen
	
	// Extract columns (uint32)
	cols = int(payload[offset])<<24 | int(payload[offset+1])<<16 | int(payload[offset+2])<<8 | int(payload[offset+3])
	offset += 4
	
	// Extract rows (uint32)  
	rows = int(payload[offset])<<24 | int(payload[offset+1])<<16 | int(payload[offset+2])<<8 | int(payload[offset+3])
	
	return cols, rows
}

// handleSSHShellWithDimensions provides the guided console management tool with terminal dimensions
func (s *Server) handleSSHShellWithDimensions(channel ssh.Channel, username, fingerprint string, registered bool, terminalWidth, terminalHeight int) {
	// Update the channel with terminal dimensions for use in centering
	if terminalWidth > 0 {
		// Store the terminal width in a way that getTerminalWidth can access it
		// For now, we'll modify the function to accept it as a parameter
		s.handleSSHShellWithWidth(channel, username, fingerprint, registered, terminalWidth)
	} else {
		s.handleSSHShell(channel, username, fingerprint, registered)
	}
}

// handleSSHShell provides the guided console management tool
func (s *Server) handleSSHShell(channel ssh.Channel, username, fingerprint string, registered bool) {
	s.handleSSHShellWithWidth(channel, username, fingerprint, registered, 0)
}

// handleSSHShellWithWidth provides the guided console management tool with specified width
func (s *Server) handleSSHShellWithWidth(channel ssh.Channel, username, fingerprint string, registered bool, width int) {
	if !registered {
		// Handle registration flow
		s.handleRegistrationWithWidth(channel, fingerprint, width)
		return
	}
	
	// Create user session for registered users
	user, err := s.getUserByFingerprint(fingerprint)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("Error retrieving user info: %v\r\n", err)))
		return
	}
	
	teams, err := s.getUserTeams(fingerprint)
	if err != nil || len(teams) == 0 {
		channel.Write([]byte("Error: User not associated with any team\r\n"))
		return
	}
	
	// Use the first team (users can be members of multiple teams)
	team := teams[0]
	
	// Check if username is a container name for direct access (ssh container-name@exe.dev)
	if username != "" && s.containerManager != nil {
		// Look for a container with the given name
		if container := s.findContainerByName(fingerprint, username); container != nil {
			s.createUserSession(channel, fingerprint, user.Email, team.TeamName, team.IsAdmin)
			defer s.removeUserSession(channel)
			
			// Connect directly to the container
			s.connectToContainer(channel, container.ID)
			return
		}
	}
	
	s.createUserSession(channel, fingerprint, user.Email, team.TeamName, team.IsAdmin)
	
	// Clean up session when connection closes
	defer s.removeUserSession(channel)
	
	s.runMainShell(channel, false) // Returning users - no welcome
}

// findContainerByName finds a container by name for a user
func (s *Server) findContainerByName(userID, containerName string) *container.Container {
	if s.containerManager == nil {
		return nil
	}
	
	containers, err := s.containerManager.ListContainers(context.Background(), userID)
	if err != nil {
		return nil
	}
	
	for _, c := range containers {
		if c.Name == containerName {
			return c
		}
	}
	
	return nil
}

// connectToContainer connects directly to a container for external SSH access
func (s *Server) connectToContainer(channel ssh.Channel, containerID string) {
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mContainer management is not available\033[0m\r\n"))
		return
	}
	
	fingerprint, _, err := s.getUserFromChannel(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError: %v\033[0m\r\n", err)))
		return
	}
	
	// Connect directly without showing "Connecting..." message
	
	// Use kubectl exec to connect to the container
	ctx := context.Background()
	
	// Execute /bin/bash in the container with TTY
	err = s.containerManager.ExecuteInContainer(
		ctx,
		fingerprint, // Using fingerprint as userID
		containerID,
		[]string{"/bin/bash"},
		channel, // stdin
		channel, // stdout
		channel, // stderr
	)
	
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\n\033[1;31mConnection failed: %v\033[0m\r\n", err)))
		return
	}
	
	// Connection ended normally
	channel.Write([]byte("\r\n\033[1;32mConnection closed\033[0m\r\n"))
}

// runMainShell runs the main container management shell
func (s *Server) runMainShell(channel ssh.Channel, showWelcome bool) {
	welcome := "\r\n\033[1;32m███████╗██╗  ██╗███████╗   ██████╗ ███████╗██╗   ██╗\r\n" +
		"██╔════╝╚██╗██╔╝██╔════╝   ██╔══██╗██╔════╝██║   ██║\r\n" +
		"█████╗   ╚███╔╝ █████╗     ██║  ██║█████╗  ██║   ██║\r\n" +
		"██╔══╝   ██╔██╗ ██╔══╝     ██║  ██║██╔══╝  ╚██╗ ██╔╝\r\n" +
		"███████╗██╔╝ ██╗███████╗██╗██████╔╝███████╗ ╚████╔╝ \r\n" +
		"╚══════╝╚═╝  ╚═╝╚══════╝╚═╝╚═════╝ ╚══════╝  ╚═══╝  \033[0m\r\n\r\n" +
		"\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
		"\033[1mlist\033[0m           - List your containers\r\n" +
		"\033[1mcreate [name]\033[0m  - Create a new container (auto-generates name if not specified)\r\n" +
		"\033[1mssh <name>\033[0m     - SSH into a container\r\n" +
		"\033[1mstart <name>\033[0m   - Start a container\r\n" +
		"\033[1mstop <name>\033[0m    - Stop a container\r\n" +
		"\033[1mdelete <name>\033[0m  - Delete a container\r\n" +
		"\033[1mlogs <name>\033[0m    - View container logs\r\n" +
		"\033[1mhelp\033[0m or \033[1m?\033[0m     - Show this help\r\n" +
		"\033[1mexit\033[0m           - Exit\r\n\r\n"
	
	helpText := "\r\n\033[1;33mEXE.DEV\033[0m commands:\r\n\r\n" +
		"\033[1mlist\033[0m           - List your containers\r\n" +
		"\033[1mcreate [name]\033[0m  - Create a new container (auto-generates name if not specified)\r\n" +
		"\033[1mssh <name>\033[0m     - SSH into a container\r\n" +
		"\033[1mstart <name>\033[0m   - Start a container\r\n" +
		"\033[1mstop <name>\033[0m    - Stop a container\r\n" +
		"\033[1mdelete <name>\033[0m  - Delete a container\r\n" +
		"\033[1mlogs <name>\033[0m    - View container logs\r\n" +
		"\033[1mhelp\033[0m or \033[1m?\033[0m     - Show this help\r\n" +
		"\033[1mexit\033[0m           - Exit\r\n\r\n"
	
	if showWelcome {
		channel.Write([]byte(welcome))
	}
	
	// Command loop using proper line reading
	for {
		channel.Write([]byte("\033[1;36mexe.dev\033[0m \033[37m▶\033[0m "))
		command, err := s.readLineFromChannel(channel)
		if err != nil {
			if err.Error() == "interrupted" || err.Error() == "EOF" {
				channel.Write([]byte("Goodbye!\r\n"))
			}
			return
		}
		
		parts := strings.Fields(strings.TrimSpace(command))
		if len(parts) == 0 {
			continue // Empty command, just continue
		}
		
		cmd := parts[0]
		args := parts[1:]
		
		switch cmd {
		case "exit":
			channel.Write([]byte("Goodbye!\r\n"))
			return
		case "help", "?":
			channel.Write([]byte(helpText))
		case "list":
			s.handleListCommand(channel)
		case "create":
			s.handleCreateCommand(channel, args)
		case "ssh":
			s.handleSSHCommand(channel, args)
		case "start":
			s.handleStartCommand(channel, args)
		case "stop":
			s.handleStopCommand(channel, args)
		case "delete":
			s.handleDeleteCommand(channel, args)
		case "logs":
			s.handleLogsCommand(channel, args)
		default:
			channel.Write([]byte("Unknown command. Type 'help' for available commands.\r\n"))
		}
	}
}

// getUserFromChannel gets user information from SSH channel session
func (s *Server) getUserFromChannel(channel ssh.Channel) (fingerprint, teamName string, err error) {
	s.sessionsMu.RLock()
	session, exists := s.sessions[channel]
	s.sessionsMu.RUnlock()
	
	if !exists {
		return "", "", fmt.Errorf("user not authenticated")
	}
	
	return session.Fingerprint, session.TeamName, nil
}

// createUserSession creates a new user session for a channel
func (s *Server) createUserSession(channel ssh.Channel, fingerprint, email, teamName string, isAdmin bool) {
	session := &UserSession{
		Fingerprint: fingerprint,
		Email:       email,
		TeamName:    teamName,
		IsAdmin:     isAdmin,
		CreatedAt:   time.Now(),
	}
	
	s.sessionsMu.Lock()
	s.sessions[channel] = session
	s.sessionsMu.Unlock()
}

// removeUserSession removes a user session for a channel
func (s *Server) removeUserSession(channel ssh.Channel) {
	s.sessionsMu.Lock()
	delete(s.sessions, channel)
	s.sessionsMu.Unlock()
}

// handleListCommand lists user's containers
func (s *Server) handleListCommand(channel ssh.Channel) {
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mContainer management is not available\033[0m\r\n"))
		return
	}
	
	fingerprint, teamName, err := s.getUserFromChannel(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError: %v\033[0m\r\n", err)))
		return
	}
	
	containers, err := s.containerManager.ListContainers(context.Background(), fingerprint)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError listing containers: %v\033[0m\r\n", err)))
		return
	}
	
	if len(containers) == 0 {
		channel.Write([]byte(fmt.Sprintf("No containers found for team %s.\r\n", teamName)))
		channel.Write([]byte("Use \033[1mcreate <name>\033[0m to create your first container.\r\n"))
		return
	}
	
	channel.Write([]byte(fmt.Sprintf("\033[1mContainers for team %s:\033[0m\r\n\r\n", teamName)))
	for _, container := range containers {
		statusColor := "37" // default gray
		switch container.Status {
		case "running":
			statusColor = "32" // green
		case "stopped":
			statusColor = "31" // red
		case "pending", "building":
			statusColor = "33" // yellow
		}
		
		channel.Write([]byte(fmt.Sprintf("  \033[1m%s\033[0m - \033[%sm%s\033[0m\r\n", 
			container.Name, statusColor, container.Status)))
	}
	channel.Write([]byte("\r\n"))
}

// generateRandomContainerName generates a random container name using two safe words
func generateRandomContainerName() string {
	words := []string{
		"alpha", "beta", "gamma", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet",
		"kilo", "lima", "mike", "nova", "oscar", "papa", "quebec", "romeo", "sierra", "tango",
		"uniform", "victor", "whiskey", "xray", "yankee", "zulu", "able", "baker", "charlie",
		"dog", "easy", "fox", "george", "how", "item", "jig", "king", "love", "neon",
		"ocean", "pine", "river", "stone", "tree", "wind", "fire", "earth", "moon", "star",
	}
	
	word1 := words[mathrand.Intn(len(words))]
	word2 := words[mathrand.Intn(len(words))]
	
	// Ensure we don't get the same word twice
	for word1 == word2 {
		word2 = words[mathrand.Intn(len(words))]
	}
	
	return word1 + "-" + word2
}

// handleCreateCommand creates a new container
func (s *Server) handleCreateCommand(channel ssh.Channel, args []string) {
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mContainer management is not available\033[0m\r\n"))
		return
	}
	
	fingerprint, teamName, err := s.getUserFromChannel(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError: %v\033[0m\r\n", err)))
		return
	}
	
	var containerName string
	if len(args) == 0 {
		// No name provided, generate a random one
		// Generate a unique name by trying until we find one that doesn't exist
		maxAttempts := 10
		for attempts := 0; attempts < maxAttempts; attempts++ {
			candidateName := generateRandomContainerName()
			
			// Check if this name is already taken
			_, err = s.getMachineByName(teamName, candidateName)
			if err != nil && err.Error() == "sql: no rows in result set" {
				// Name is available
				containerName = candidateName
				break
			}
		}
		
		if containerName == "" {
			channel.Write([]byte("\033[1;31mFailed to generate a unique container name. Please specify a name manually.\033[0m\r\n"))
			return
		}
		
		// Don't show "Generated container name" - it will be shown in the Creating line
	} else {
		containerName = args[0]
		if !s.isValidContainerName(containerName) {
			channel.Write([]byte("\033[1;31mInvalid container name. Use 3-20 lowercase letters, numbers, and hyphens only.\033[0m\r\n"))
			return
		}
	}
	
	// Check if container name already exists in this team
	_, err = s.getMachineByName(teamName, containerName)
	if err == nil {
		// Machine already exists
		channel.Write([]byte(fmt.Sprintf("\033[1;31mContainer name '%s' already exists in team '%s'\033[0m\r\n", containerName, teamName)))
		return
	} else if err.Error() != "sql: no rows in result set" {
		// Some other database error
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError checking container name: %v\033[0m\r\n", err)))
		return
	}
	// err == sql.ErrNoRows means the name is available, continue
	
	channel.Write([]byte(fmt.Sprintf("Creating \033[1m%s\033[0m for team \033[1;36m%s\033[0m...\r\n", containerName, teamName)))
	
	// Create container request
	req := &container.CreateContainerRequest{
		UserID:   fingerprint,
		Name:     containerName,
		TeamName: teamName,
		Image:    "ubuntu:22.04", // Default to Ubuntu
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	
	createdContainer, err := s.containerManager.CreateContainer(ctx, req)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mFailed to create container: %v\033[0m\r\n", err)))
		return
	}
	
	// Store container info in database
	if err := s.createMachine(fingerprint, teamName, containerName, createdContainer.ID, "ubuntu:22.04"); err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;33mWarning: Failed to store container info: %v\033[0m\r\n", err)))
	}
	
	// Wait for container to be running
	channel.Write([]byte("Waiting for startup... "))
	
	maxWaitTime := 3 * time.Minute
	containerCheckInterval := 2 * time.Second
	timerUpdateInterval := 100 * time.Millisecond
	startTime := time.Now()
	lastContainerCheck := time.Time{}
	
	for time.Since(startTime) < maxWaitTime {
		// Show elapsed time (updates every 100ms)
		elapsed := time.Since(startTime)
		channel.Write([]byte(fmt.Sprintf("\r\033[KWaiting for startup... (%.1fs)", elapsed.Seconds())))
		
		// Check container status only every 2 seconds to avoid overwhelming the API
		if time.Since(lastContainerCheck) >= containerCheckInterval {
			lastContainerCheck = time.Now()
			
			containers, err := s.containerManager.ListContainers(context.Background(), fingerprint)
			if err != nil {
				channel.Write([]byte(fmt.Sprintf("\r\033[K\033[1;31mError checking container status: %v\033[0m\r\n", err)))
				return
			}
			
			// Find our container
			var containerStatus container.ContainerStatus
			var containerFound bool
			for _, c := range containers {
				if c.Name == containerName {
					containerStatus = c.Status
					containerFound = true
					break
				}
			}
			
			if containerFound && containerStatus == container.StatusRunning {
				totalTime := time.Since(startTime)
				channel.Write([]byte(fmt.Sprintf("\r\033[KReady in %.1fs! Access with \033[1mssh %s@exe.dev\033[0m\r\n", totalTime.Seconds(), containerName)))
				
				// Automatically SSH into the new container
				s.handleSSHCommand(channel, []string{containerName})
				return
			} else if containerFound && containerStatus == container.StatusFailed {
				channel.Write([]byte(fmt.Sprintf("\r\033[K\033[1;31mContainer failed to start (status: %s)\033[0m\r\n", containerStatus)))
				return
			}
			
			// If container is stuck pending for too long, get diagnostics
			if containerFound && containerStatus == container.StatusPending && time.Since(startTime) > 30*time.Second {
				// Only check diagnostics every 30 seconds to avoid spam
				if int(time.Since(startTime).Seconds())%30 == 0 {
					if diagnostics, err := s.containerManager.GetContainerDiagnostics(context.Background(), fingerprint, containerName); err == nil {
						// Log the full diagnostics for ops
						log.Printf("CONTAINER_STUCK: %s", diagnostics)
						
						// Check for specific quota errors
						if strings.Contains(diagnostics, "QUOTA_EXCEEDED") {
							channel.Write([]byte(fmt.Sprintf("\r\033[K\033[1;31mContainer creation failed: GCP disk quota exceeded\033[0m\r\n")))
							channel.Write([]byte("Please contact support or try again later.\r\n"))
							return
						} else if strings.Contains(diagnostics, "Insufficient memory") {
							channel.Write([]byte(fmt.Sprintf("\r\033[K\033[1;31mContainer creation failed: Insufficient cluster memory\033[0m\r\n")))
							channel.Write([]byte("Please try again later when resources are available.\r\n"))
							return
						}
					}
				}
			}
		}
		
		// Sleep for timer update interval (100ms)
		time.Sleep(timerUpdateInterval)
	}
	
	// Check if we timed out
	if time.Since(startTime) >= maxWaitTime {
		channel.Write([]byte("\r\033[K\033[1;33mContainer creation timed out, but it may still be starting in the background.\033[0m\r\n"))
		return
	}
}

// handleSSHCommand connects to a container via SSH
func (s *Server) handleSSHCommand(channel ssh.Channel, args []string) {
	if len(args) == 0 {
		channel.Write([]byte("\033[1;31mUsage: ssh <name>\033[0m\r\n"))
		return
	}
	
	containerName := args[0]
	
	fingerprint, teamName, err := s.getUserFromChannel(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError: %v\033[0m\r\n", err)))
		return
	}
	
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mContainer management is not available\033[0m\r\n"))
		return
	}
	
	// Look up the machine in database
	machine, err := s.getMachineByName(teamName, containerName)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			channel.Write([]byte(fmt.Sprintf("\033[1;31mContainer '%s' not found\033[0m\r\n", containerName)))
		} else {
			channel.Write([]byte(fmt.Sprintf("\033[1;31mError finding container: %v\033[0m\r\n", err)))
		}
		return
	}
	
	// Check if container exists and is running
	if machine.ContainerID == nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mContainer '%s' not yet created\033[0m\r\n", containerName)))
		return
	}
	
	// Connect directly without showing "Connecting..." message
	
	// Use kubectl exec to connect to the container
	ctx := context.Background()
	
	// Execute /bin/bash in the container with TTY
	err = s.containerManager.ExecuteInContainer(
		ctx,
		fingerprint, // Using fingerprint as userID
		*machine.ContainerID,
		[]string{"/bin/bash"},
		channel, // stdin
		channel, // stdout
		channel, // stderr
	)
	
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\n\033[1;31mConnection failed: %v\033[0m\r\n", err)))
		return
	}
	
	// Connection ended normally
	channel.Write([]byte("\r\n\033[1;32mConnection closed\033[0m\r\n"))
}

// handleStartCommand starts a container
func (s *Server) handleStartCommand(channel ssh.Channel, args []string) {
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mContainer management is not available\033[0m\r\n"))
		return
	}
	
	if len(args) == 0 {
		channel.Write([]byte("\033[1;31mUsage: start <name>\033[0m\r\n"))
		return
	}
	
	containerName := args[0]
	
	_, _, err := s.getUserFromChannel(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError: %v\033[0m\r\n", err)))
		return
	}
	
	// TODO: Get container ID from database by name
	channel.Write([]byte(fmt.Sprintf("Starting container \033[1m%s\033[0m...\r\n", containerName)))
	channel.Write([]byte("\033[1;33mStart command not yet fully implemented\033[0m\r\n"))
}

// handleStopCommand stops a container
func (s *Server) handleStopCommand(channel ssh.Channel, args []string) {
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mContainer management is not available\033[0m\r\n"))
		return
	}
	
	if len(args) == 0 {
		channel.Write([]byte("\033[1;31mUsage: stop <name>\033[0m\r\n"))
		return
	}
	
	containerName := args[0]
	
	fingerprint, teamName, err := s.getUserFromChannel(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError: %v\033[0m\r\n", err)))
		return
	}
	
	// Look up the machine in database
	machine, err := s.getMachineByName(teamName, containerName)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			channel.Write([]byte(fmt.Sprintf("\033[1;31mContainer '%s' not found\033[0m\r\n", containerName)))
		} else {
			channel.Write([]byte(fmt.Sprintf("\033[1;31mError finding container: %v\033[0m\r\n", err)))
		}
		return
	}
	
	// Check if container exists and has a container ID
	if machine.ContainerID == nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mContainer '%s' not yet created\033[0m\r\n", containerName)))
		return
	}
	
	channel.Write([]byte(fmt.Sprintf("Stopping container \033[1m%s\033[0m...\r\n", containerName)))
	
	// Stop the container
	ctx := context.Background()
	err = s.containerManager.StopContainer(ctx, fingerprint, *machine.ContainerID)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mFailed to stop container: %v\033[0m\r\n", err)))
		return
	}
	
	channel.Write([]byte(fmt.Sprintf("\033[1;32mContainer '%s' stopped successfully\033[0m\r\n", containerName)))
}

// handleDeleteCommand deletes a container
func (s *Server) handleDeleteCommand(channel ssh.Channel, args []string) {
	if len(args) == 0 {
		channel.Write([]byte("\033[1;31mUsage: delete <name>\033[0m\r\n"))
		return
	}
	
	containerName := args[0]
	channel.Write([]byte(fmt.Sprintf("Deleting container \033[1m%s\033[0m...\r\n", containerName)))
	channel.Write([]byte("\033[1;33mDelete command not yet implemented\033[0m\r\n"))
}

// handleLogsCommand shows container logs
func (s *Server) handleLogsCommand(channel ssh.Channel, args []string) {
	if len(args) == 0 {
		channel.Write([]byte("\033[1;31mUsage: logs <name>\033[0m\r\n"))
		return
	}
	
	containerName := args[0]
	channel.Write([]byte(fmt.Sprintf("Showing logs for container \033[1m%s\033[0m...\r\n", containerName)))
	channel.Write([]byte("\033[1;33mLogs command not yet implemented\033[0m\r\n"))
}

// isValidContainerName validates container names using same rules as team names
func (s *Server) isValidContainerName(name string) bool {
	return s.isValidTeamName(name) // Reuse team name validation
}

// createMachine stores machine info in database
func (s *Server) createMachine(userFingerprint, teamName, name, containerID, image string) error {
	_, err := s.db.Exec(`
		INSERT INTO machines (team_name, name, status, image, container_id, created_by_fingerprint) 
		VALUES (?, ?, ?, ?, ?, ?)
	`, teamName, name, "pending", image, containerID, userFingerprint)
	return err
}

// getMachineByName retrieves a machine by name and team
func (s *Server) getMachineByName(teamName, name string) (*Machine, error) {
	var machine Machine
	err := s.db.QueryRow(`
		SELECT id, team_name, name, status, image, container_id, created_by_fingerprint, created_at, updated_at, last_started_at
		FROM machines 
		WHERE team_name = ? AND name = ?
	`, teamName, name).Scan(
		&machine.ID, &machine.TeamName, &machine.Name, &machine.Status,
		&machine.Image, &machine.ContainerID, &machine.CreatedByFingerprint,
		&machine.CreatedAt, &machine.UpdatedAt, &machine.LastStartedAt,
	)
	if err != nil {
		return nil, err
	}
	return &machine, nil
}

// handleRegistration manages the user registration process with email verification and billing
// showAnimatedWelcome displays the ASCII art with a beautiful fade-out animation
func (s *Server) showAnimatedWelcome(channel ssh.Channel) {
	s.showAnimatedWelcomeWithWidth(channel, 0)
}

// showAnimatedWelcomeWithWidth displays the ASCII art with a beautiful fade-out animation using specified terminal width
func (s *Server) showAnimatedWelcomeWithWidth(channel ssh.Channel, terminalWidth int) {
	// More compact ASCII art that fits better in terminals
	asciiArt := []string{
		"███████╗██╗  ██╗███████╗   ██████╗ ███████╗██╗   ██╗",
		"██╔════╝╚██╗██╔╝██╔════╝   ██╔══██╗██╔════╝██║   ██║",
		"█████╗   ╚███╔╝ █████╗     ██║  ██║█████╗  ██║   ██║",
		"██╔══╝   ██╔██╗ ██╔══╝     ██║  ██║██╔══╝  ╚██╗ ██╔╝",
		"███████╗██╔╝ ██╗███████╗██╗██████╔╝███████╗ ╚████╔╝ ",
		"╚══════╝╚═╝  ╚═╝╚══════╝╚═╝╚═════╝ ╚══════╝  ╚═══╝  ",
	}
	
	// Use provided terminal width or detect it
	if terminalWidth <= 0 {
		terminalWidth = s.getTerminalWidth(channel)
	}
	
	// Calculate art width (longest line) - count visual characters, not bytes
	artWidth := s.getVisualWidth(asciiArt[0])
	leftPadding := (terminalWidth - artWidth) / 2
	if leftPadding < 0 {
		leftPadding = 0 // Handle edge case of very narrow terminals
	}
	
	// Debug logging without disrupting the display
	log.Printf("ASCII art centering: Terminal: %d chars, Art: %d chars, Padding: %d", 
		terminalWidth, artWidth, leftPadding)
	
	
	// Clear screen and move cursor to top
	channel.Write([]byte("\033[2J\033[H"))
	
	// Add some vertical padding to center vertically
	channel.Write([]byte("\r\n\r\n\r\n\r\n\r\n"))
	
	// Add 3 additional blank lines above the ASCII art
	channel.Write([]byte("\r\n\r\n\r\n"))
	
	// Beautiful fade effect for dark terminals: bright green -> dark green -> black
	// Each step gets progressively darker until it fades to black
	fadeSteps := []struct {
		color string
		delay time.Duration
	}{
		{"\033[1;32m", 500 * time.Millisecond},  // Bright green - the signature color
		{"\033[0;32m", 200 * time.Millisecond},  // Normal green
		{"\033[2;32m", 150 * time.Millisecond},  // Dim green
		{"\033[38;5;28m", 150 * time.Millisecond}, // Dark green (256-color)
		{"\033[38;5;22m", 150 * time.Millisecond}, // Darker green
		{"\033[38;5;16m", 100 * time.Millisecond}, // Very dark (almost black)
		{"\033[30m", 100 * time.Millisecond},     // Black (invisible on dark bg)
	}
	
	// Show the art with fade animation
	for _, step := range fadeSteps {
		// Clear the previous art area
		channel.Write([]byte(fmt.Sprintf("\033[%dA", len(asciiArt))))
		
		// Draw the art with current color
		for _, line := range asciiArt {
			padding := strings.Repeat(" ", leftPadding)
			channel.Write([]byte(fmt.Sprintf("%s%s%s\033[0m\r\n", padding, step.color, line)))
		}
		
		// Wait before next step
		time.Sleep(step.delay)
	}
	
	// Move cursor back up and clear the art area completely
	channel.Write([]byte(fmt.Sprintf("\033[%dA", len(asciiArt))))
	for i := 0; i < len(asciiArt); i++ {
		channel.Write([]byte("\033[2K\r\n")) // Clear entire line and move to next
	}
	
	// Move cursor back to where the art was
	channel.Write([]byte(fmt.Sprintf("\033[%dA", len(asciiArt))))
}

// getVisualWidth calculates the actual visual width of a string with Unicode characters
func (s *Server) getVisualWidth(text string) int {
	// Convert to runes to count actual characters, not bytes
	runes := []rune(text)
	return len(runes)
}

// getTerminalWidth attempts to determine the terminal width through multiple methods
func (s *Server) getTerminalWidth(channel ssh.Channel) int {
	// Method 1: Try to get from environment (SSH often sets this)
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if width, err := strconv.Atoi(cols); err == nil && width > 20 {
			return width
		}
	}
	
	// Method 2: Use the actual terminal width you reported
	// You mentioned having a 140-character terminal, so let's use that
	return 140
}

func (s *Server) handleRegistration(channel ssh.Channel, fingerprint string) {
	s.handleRegistrationWithWidth(channel, fingerprint, 0)
}

func (s *Server) handleRegistrationWithWidth(channel ssh.Channel, fingerprint string, terminalWidth int) {
	// Show the animated welcome with terminal width
	s.showAnimatedWelcomeWithWidth(channel, terminalWidth)
	
	// Now show the signup content after the animation
	signupContent := "\r\n\033[1;33mtype ssh to get a server\033[0m\r\n\r\n" +
		"Let's get you set up in just a few steps:\r\n\r\n" +
		"\033[2;37m1. Email Verification\r\n" +
		"2. Team Setup\r\n" +
		"3. Payment Setup\033[0m\r\n\r\n" +
		"\033[1mTo get started, please enter your email address:\033[0m\r\n"
	
	channel.Write([]byte(signupContent))
	
	// Read email address from user
	email, err := s.readLineFromChannel(channel)
	if err != nil {
		if err.Error() == "interrupted" || err.Error() == "EOF" {
			channel.Write([]byte("Goodbye!\r\n"))
			return
		}
		channel.Write([]byte("\r\nError reading input. Please try again.\r\n"))
		return
	}
	
	// Validate email format (basic validation)
	if !s.isValidEmail(email) {
		channel.Write([]byte("\r\nInvalid email address. Please try again.\r\n"))
		return
	}
	
	channel.Write([]byte(fmt.Sprintf("\r\n\033[1;32mEmail confirmed:\033[0m %s\r\n", email)))
	
	// Start email verification flow
	if err := s.startEmailVerification(channel, fingerprint, email); err != nil {
		// Log the error for debugging
		log.Printf("Email verification failed for %s (fingerprint: %s): %v", email, fingerprint, err)
		
		// Show user-friendly error message
		if err.Error() == "email service not configured" {
			channel.Write([]byte("\r\nError: Email service not configured. Please contact support.\r\n"))
		} else if strings.Contains(err.Error(), "marked as inactive") {
			channel.Write([]byte("\r\nError: This email address cannot receive emails (blocked by email provider).\r\nPlease try a different email address.\r\n"))
		} else {
			channel.Write([]byte(fmt.Sprintf("\r\nError sending verification email: %v\r\n", err)))
		}
		return
	}
}

// readLineFromChannel reads a line of input from an SSH channel
func (s *Server) readLineFromChannel(channel ssh.Channel) (string, error) {
	var buffer []byte
	temp := make([]byte, 1)
	
	for {
		n, err := channel.Read(temp)
		if err != nil {
			return "", err
		}
		
		if n > 0 {
			switch temp[0] {
			case '\n', '\r':
				if len(buffer) > 0 {
					// Always send CRLF after user input to keep cursor aligned
					channel.Write([]byte("\r\n"))
					return string(buffer), nil
				}
			case 3: // Ctrl+C
				channel.Write([]byte("^C\r\n"))
				return "", fmt.Errorf("interrupted")
			case 4: // Ctrl+D
				if len(buffer) == 0 {
					channel.Write([]byte("^D\r\n"))
					return "", fmt.Errorf("EOF")
				}
				// If there's content, treat as normal character
				buffer = append(buffer, temp[0])
			case 8, 127: // Backspace or DEL
				if len(buffer) > 0 {
					buffer = buffer[:len(buffer)-1]
					channel.Write([]byte("\b \b")) // Erase character on terminal
				}
			default:
				if temp[0] >= 32 { // Printable characters
					buffer = append(buffer, temp[0])
					channel.Write(temp) // Echo character back
				}
			}
		}
	}
}

// isValidEmail performs basic email validation
func (s *Server) isValidEmail(email string) bool {
	if email == "" {
		return false
	}
	
	// Very basic email validation - contains @ and a dot after @
	atIndex := strings.Index(email, "@")
	if atIndex <= 0 || atIndex == len(email)-1 {
		return false
	}
	
	domain := email[atIndex+1:]
	if !strings.Contains(domain, ".") {
		return false
	}
	
	return true
}

// startEmailVerification initiates the email verification process
func (s *Server) startEmailVerification(channel ssh.Channel, fingerprint, email string) error {
	// Generate verification token
	token := s.generateRegistrationToken()
	
	// Create email verification
	verification := &EmailVerification{
		PublicKeyFingerprint: fingerprint,
		Email:                email,
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}
	
	// Store verification
	s.emailVerificationsMu.Lock()
	s.emailVerifications[token] = verification
	s.emailVerificationsMu.Unlock()
	
	// Send verification email
	if err := s.sendVerificationEmail(email, token); err != nil {
		s.emailVerificationsMu.Lock()
		delete(s.emailVerifications, token)
		s.emailVerificationsMu.Unlock()
		return err
	}
	
	channel.Write([]byte("\r\n\033[1;33mVerification email sent!\033[0m Please check your email and click the verification link.\r\n"))
	channel.Write([]byte("\r\n\033[2;37mWaiting for email verification"))
	// Add animated dots
	for i := 0; i < 3; i++ {
		time.Sleep(500 * time.Millisecond)
		channel.Write([]byte("."))
	}
	channel.Write([]byte("\033[0m\r\n\r\n"))
	
	// Wait for email verification or timeout
	select {
	case <-verification.CompleteChan:
		channel.Write([]byte("\r\n\033[1;32mEmail verified successfully!\033[0m\r\n\r\n"))
		
		// Start team name creation first
		s.startTeamNameCreation(channel, fingerprint, email)
		
	case <-time.After(10 * time.Minute):
		channel.Write([]byte("\r\nEmail verification timeout. Please try connecting again.\r\n"))
		
		// Clean up verification
		s.emailVerificationsMu.Lock()
		delete(s.emailVerifications, token)
		s.emailVerificationsMu.Unlock()
	}
	
	return nil
}

// sendVerificationEmail sends a verification email using Postmark
func (s *Server) sendVerificationEmail(email, token string) error {
	verificationURL := fmt.Sprintf("%s/verify-email?token=%s", s.BaseURL, token)
	
	// In dev mode, just log the URL instead of sending email and auto-complete verification
	if s.devMode {
		log.Printf("🔧 DEV MODE: Would send verification email to %s with URL: %s", email, verificationURL)
		
		// Auto-complete email verification in dev mode
		go func() {
			time.Sleep(100 * time.Millisecond) // Brief delay to simulate async behavior
			s.emailVerificationsMu.Lock()
			verification, exists := s.emailVerifications[token]
			if exists {
				close(verification.CompleteChan)
				delete(s.emailVerifications, token)
				log.Printf("🔧 DEV MODE: Auto-completed email verification for %s", email)
			}
			s.emailVerificationsMu.Unlock()
		}()
		
		return nil
	}
	
	if s.postmarkClient == nil {
		return fmt.Errorf("email service not configured")
	}
	
	emailBody := fmt.Sprintf(`
<html>
<body>
    <h1>Welcome to exe.dev!</h1>
    <p>Please click the link below to verify your email address:</p>
    <p><a href="%s" style="background-color: #4CAF50; color: white; padding: 10px 20px; text-decoration: none; border-radius: 4px;">Verify Email</a></p>
    <p>Or copy and paste this link into your browser:</p>
    <p>%s</p>
    <p>This link will expire in 10 minutes.</p>
    <p>If you didn't request this verification, you can safely ignore this email.</p>
</body>
</html>
`, verificationURL, verificationURL)
	
	email_msg := postmark.Email{
		From:     "register@exe.dev",
		To:       email,
		Subject:  "Verify your email for exe.dev",
		HtmlBody: emailBody,
		TextBody: fmt.Sprintf("Welcome to exe.dev! Please verify your email by visiting: %s", verificationURL),
	}
	
	_, err := s.postmarkClient.SendEmail(email_msg)
	return err
}

// startTeamNameCreation handles team name creation after email verification
func (s *Server) startTeamNameCreation(channel ssh.Channel, fingerprint, email string) {
	// Check if user's email has been invited to any teams
	invites, err := s.getInvitesByEmail(email)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\nError checking invites: %v\r\n", err)))
		return
	}
	
	// Filter valid (non-expired, not fully used) invites
	var validInvites []Invite
	now := time.Now()
	for _, invite := range invites {
		if now.Before(invite.ExpiresAt) && invite.UsedCount < invite.MaxUses {
			validInvites = append(validInvites, invite)
		}
	}
	
	if len(validInvites) > 0 {
		// User has pending invites, show them and let them choose
		s.handlePendingInvites(channel, fingerprint, email, validInvites)
	} else {
		// No pending invites, proceed with team creation
		teamName, err := s.createTeamName(channel)
		if err != nil {
			channel.Write([]byte(fmt.Sprintf("\r\nError creating team: %v\r\n", err)))
			return
		}
		s.startBillingVerification(channel, fingerprint, email, teamName)
	}
}

// handlePendingInvites shows pending invites and lets user choose
func (s *Server) handlePendingInvites(channel ssh.Channel, fingerprint, email string, invites []Invite) {
	channel.Write([]byte("\r\n\033[1;36m" +
		"╭─────────────────────────────────────────────────╮\r\n" +
		"│  \033[1;33mStep 2: Team Setup\033[1;36m                        │\r\n" +
		"╰─────────────────────────────────────────────────╯\033[0m\r\n\r\n" +
		"\033[1mYou have pending team invitations!\033[0m\r\n\r\n"))
	
	// Show available invites
	for i, invite := range invites {
		channel.Write([]byte(fmt.Sprintf("\033[1;32m%d.\033[0m Join team \033[1;36m%s\033[0m\r\n", i+1, invite.TeamName)))
	}
	
	channel.Write([]byte(fmt.Sprintf("\033[1;32m%d.\033[0m Create a new team instead\r\n\r\n", len(invites)+1)))
	channel.Write([]byte("Enter your choice: "))
	
	for {
		choice, err := s.readLineFromChannel(channel)
		if err != nil {
			channel.Write([]byte(fmt.Sprintf("\r\nError reading choice: %v\r\n", err)))
			return
		}
		
		choice = strings.TrimSpace(choice)
		choiceNum := 0
		fmt.Sscanf(choice, "%d", &choiceNum)
		
		if choiceNum >= 1 && choiceNum <= len(invites) {
			// Join selected team
			invite := invites[choiceNum-1]
			
			// Create user and add to team
			if err := s.createUser(fingerprint, email); err != nil {
				channel.Write([]byte(fmt.Sprintf("\r\nFailed to create user: %v\r\n", err)))
				return
			}
			
			if err := s.addTeamMember(fingerprint, invite.TeamName, false); err != nil {
				channel.Write([]byte(fmt.Sprintf("\r\nFailed to add to team: %v\r\n", err)))
				return
			}
			
			// Use the invite
			if err := s.useInvite(invite.Code); err != nil {
				channel.Write([]byte(fmt.Sprintf("\r\nFailed to mark invite as used: %v\r\n", err)))
				return
			}
			
			channel.Write([]byte(fmt.Sprintf("\r\n\033[1;32mSuccessfully joined team: %s\033[0m\r\n\r\n", invite.TeamName)))
			s.completeRegistration(channel, fingerprint, email, invite.TeamName)
			return
		} else if choiceNum == len(invites)+1 {
			// Create new team
			teamName, err := s.createTeamName(channel)
			if err != nil {
				channel.Write([]byte(fmt.Sprintf("\r\nError creating team: %v\r\n", err)))
				return
			}
			s.startBillingVerification(channel, fingerprint, email, teamName)
			return
		} else {
			channel.Write([]byte(fmt.Sprintf("\r\n\033[1;31mPlease enter a number between 1 and %d\033[0m\r\n\r\nEnter your choice: ", len(invites)+1)))
		}
	}
}

// joinTeamViaInvite handles joining an existing team via invite code
func (s *Server) joinTeamViaInvite(channel ssh.Channel, fingerprint, email string) (string, error) {
	channel.Write([]byte("\r\n\033[1mPlease enter your invite code:\033[0m "))
	
	for {
		inviteCode, err := s.readLineFromChannel(channel)
		if err != nil {
			return "", err
		}
		
		inviteCode = strings.TrimSpace(inviteCode)
		if inviteCode == "" {
			channel.Write([]byte("\r\n\033[1;31mInvite code cannot be empty\033[0m\r\n\r\nInvite code: "))
			continue
		}
		
		// Get invite from database
		invite, err := s.getInviteByCode(inviteCode)
		if err != nil {
			channel.Write([]byte("\r\n\033[1;31mInvalid invite code\033[0m\r\n\r\nInvite code: "))
			continue
		}
		
		// Check if invite is still valid
		if time.Now().After(invite.ExpiresAt) {
			channel.Write([]byte("\r\n\033[1;31mInvite code has expired\033[0m\r\n\r\nInvite code: "))
			continue
		}
		
		// Check if invite has remaining uses
		if invite.UsedCount >= invite.MaxUses {
			channel.Write([]byte("\r\n\033[1;31mInvite code has been fully used\033[0m\r\n\r\nInvite code: "))
			continue
		}
		
		// Check if invite is email-specific and matches
		if invite.Email != "" && invite.Email != email {
			channel.Write([]byte("\r\n\033[1;31mThis invite is for a different email address\033[0m\r\n\r\nInvite code: "))
			continue
		}
		
		// Create user and add to team
		if err := s.createUser(fingerprint, email); err != nil {
			return "", fmt.Errorf("failed to create user: %w", err)
		}
		
		if err := s.addTeamMember(fingerprint, invite.TeamName, false); err != nil {
			return "", fmt.Errorf("failed to add to team: %w", err)
		}
		
		// Use the invite
		if err := s.useInvite(inviteCode); err != nil {
			return "", fmt.Errorf("failed to mark invite as used: %w", err)
		}
		
		channel.Write([]byte(fmt.Sprintf("\r\n\033[1;32mSuccessfully joined team: %s\033[0m\r\n\r\n", invite.TeamName)))
		return invite.TeamName, nil
	}
}

// completeRegistration finishes the registration process without billing
func (s *Server) completeRegistration(channel ssh.Channel, fingerprint, email, teamName string) {
	// Clean up verification states
	s.billingVerificationsMu.Lock()
	delete(s.billingVerifications, fingerprint)
	s.billingVerificationsMu.Unlock()
	
	// Show success message
	channel.Write([]byte("\r\n\033[1;32m"))
	celebrationFrames := []string{
		"    🎉 Registration Complete! 🎉    ",
		"    ✨ Registration Complete! ✨    ",
		"    🌟 Registration Complete! 🌟    ",
		"    🎊 Registration Complete! 🎊    ",
	}
	for i, frame := range celebrationFrames {
		if i > 0 {
			channel.Write([]byte("\r"))
		}
		channel.Write([]byte(frame))
		time.Sleep(300 * time.Millisecond)
	}
	channel.Write([]byte("\033[0m\r\n\r\n"))
	
	channel.Write([]byte(fmt.Sprintf("\033[1mWelcome to team \033[1;32m%s\033[0m!\033[0m\r\n\r\n", teamName)))
	channel.Write([]byte("You now have access to:\r\n"))
	channel.Write([]byte(fmt.Sprintf("  • Team containers at \033[1;36m<name>.%s.exe.dev\033[0m\r\n", teamName)))
	channel.Write([]byte("  • Shared team resources and collaboration\r\n\r\n"))
	
	// Create user session before continuing to main shell
	s.createUserSession(channel, fingerprint, email, teamName, true) // Admin since they created the team
	defer s.removeUserSession(channel)
	
	// Continue with normal shell flow
	s.runMainShell(channel, true) // New users - show welcome
}

// startBillingVerification initiates the billing verification process
func (s *Server) startBillingVerification(channel ssh.Channel, fingerprint, email, teamName string) {
	// Store billing verification state
	billing := &BillingVerification{
		PublicKeyFingerprint: fingerprint,
		TeamName:             teamName,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}
	
	s.billingVerificationsMu.Lock()
	s.billingVerifications[fingerprint] = billing
	s.billingVerificationsMu.Unlock()
	
	message := "\r\n\033[1;36m" +
		"╭─────────────────────────────────────────────────╮\r\n" +
		"│  \033[1;33mStep 3: Payment Setup\033[1;36m                      │\r\n" +
		"╰─────────────────────────────────────────────────╯\033[0m\r\n\r\n" +
		"\033[1mLet's verify your payment method.\033[0m\r\n\r\n" +
		"\033[2;37mFor testing, please enter the Stripe test card:\033[0m\r\n" +
		"\033[1;33m4242424242424242\033[0m \033[2;37m(Visa test card)\033[0m\r\n\r\n" +
		"\033[1mCredit card number:\033[0m "
	
	channel.Write([]byte(message))
	
	// Read credit card number
	cardNumber, err := s.readLineFromChannel(channel)
	if err != nil {
		if err.Error() == "interrupted" || err.Error() == "EOF" {
			channel.Write([]byte("Goodbye!\r\n"))
			return
		}
		channel.Write([]byte("\r\nError reading input. Please try again.\r\n"))
		return
	}
	
	// Verify card with Stripe
	if err := s.verifyPaymentMethod(cardNumber); err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\nPayment verification failed: %v\r\n", err)))
		return
	}
	
	channel.Write([]byte("\r\n\033[1;32mPayment method verified successfully!\033[0m\r\n\r\n"))
	
	// Create user and team in database
	if err := s.createUser(fingerprint, email); err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\nFailed to create user: %v\r\n", err)))
		return
	}
	if err := s.createTeam(teamName, email); err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\nFailed to create team: %v\r\n", err)))
		return
	}
	if err := s.addTeamMember(fingerprint, teamName, true); err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\nFailed to add to team: %v\r\n", err)))
		return
	}
	
	// Clean up verification states
	s.billingVerificationsMu.Lock()
	delete(s.billingVerifications, fingerprint)
	s.billingVerificationsMu.Unlock()
	
	// Create celebration animation
	channel.Write([]byte("\r\n\033[1;32m"))
	celebrationFrames := []string{
		"   Registration completed!   ",
		"  * Registration completed! *  ",
		" *** Registration completed! *** ",
	}
	
	for i := 0; i < 2; i++ {
		for _, frame := range celebrationFrames {
			channel.Write([]byte("\r" + frame))
			time.Sleep(300 * time.Millisecond)
		}
	}
	
	channel.Write([]byte("\033[0m\r\n\r\n"))
	channel.Write([]byte("\033[1;36m" +
		"╔══════════════════════════════════════════════════════════════╗\r\n" +
		"║                                                              ║\r\n" +
		"║               \033[1;32mWelcome to exe.dev!\033[1;36m                     ║\r\n" +
		"║                                                              ║\r\n" +
		"║  \033[1;37mYour account is now ready! You can:\033[1;36m                     ║\r\n" +
		"║                                                              ║\r\n" +
		"║  \033[37m• Create and manage containers\033[1;36m                          ║\r\n" +
		"║  \033[37m• Deploy applications with persistent storage\033[1;36m           ║\r\n" +
		"║  \033[37m• Access your containers anytime via SSH\033[1;36m               ║\r\n" +
		"║                                                              ║\r\n" +
		"╚══════════════════════════════════════════════════════════════╝\033[0m\r\n\r\n"))
	
	// Get user's team membership to determine admin status
	teams, err := s.getUserTeams(fingerprint)
	isAdmin := true // Default to admin
	if err == nil && len(teams) > 0 {
		// Find the team membership to get correct admin status
		for _, team := range teams {
			if team.TeamName == teamName {
				isAdmin = team.IsAdmin
				break
			}
		}
	}
	
	// Create user session before continuing to main shell
	s.createUserSession(channel, fingerprint, email, teamName, isAdmin)
	defer s.removeUserSession(channel)
	
	// Continue with normal shell flow
	s.runMainShell(channel, true) // New team members - show welcome
}

// verifyPaymentMethod verifies a payment method with Stripe using test tokens
func (s *Server) verifyPaymentMethod(cardNumber string) error {
	// Remove spaces from card number
	cardNumber = strings.ReplaceAll(cardNumber, " ", "")
	
	// Basic validation - check if it's the test card number
	if cardNumber != "4242424242424242" {
		return fmt.Errorf("invalid card number. Please use the test card: 4242424242424242")
	}
	
	// Use a test payment method token instead of raw card data
	// This is a pre-created test payment method token from Stripe
	testPaymentMethodToken := "pm_card_visa" // Stripe test token for Visa
	
	// Try to retrieve the test payment method to verify it exists
	pm, err := paymentmethod.Get(testPaymentMethodToken, nil)
	if err != nil {
		return fmt.Errorf("payment method verification failed: %w", err)
	}
	
	// Log successful verification (but don't expose sensitive details)
	log.Printf("Payment method verified successfully: type=%s, last4=%s", pm.Type, pm.Card.Last4)
	
	return nil
}

// createTeamName handles team name creation with simple validation
func (s *Server) createTeamName(channel ssh.Channel) (string, error) {
	channel.Write([]byte("\r\n\033[1;36m" +
		"╭─────────────────────────────────────────────────╮\r\n" +
		"│  \033[1;33mStep 2: Team Setup\033[1;36m                        │\r\n" +
		"╰─────────────────────────────────────────────────╯\033[0m\r\n\r\n"))
	
	channel.Write([]byte("\033[1mNow let's create your team name.\033[0m\r\n\r\n"))
	channel.Write([]byte("\033[2;37mYour containers will be available at: \033[1;32m<name>.<team>.exe.dev\033[0m\r\n\r\n"))
	
	for {
		channel.Write([]byte("\033[1mTeam name:\033[0m "))
		
		teamName, err := s.readLineFromChannel(channel)
		if err != nil {
			if err.Error() == "interrupted" || err.Error() == "EOF" {
				channel.Write([]byte("Goodbye!\r\n"))
				return "", err
			}
			return "", err
		}
		
		// Validate team name
		if !s.isValidTeamName(teamName) {
			channel.Write([]byte("\r\n\033[1;31mInvalid team name\033[0m\r\n"))
			channel.Write([]byte("\033[2;37m   Requirements: 3-20 characters, lowercase letters/numbers/hyphens only\033[0m\r\n\r\n"))
			continue
		}
		
		// Check if team name is available
		taken, err := s.isTeamNameTaken(teamName)
		if err != nil {
			channel.Write([]byte(fmt.Sprintf("\r\nError checking team name: %v\r\n", err)))
			continue
		}
		
		if taken {
			channel.Write([]byte("\r\n\033[1;31mTeam name already taken\033[0m\r\n"))
			channel.Write([]byte("\033[2;37m   Please try a different name\033[0m\r\n\r\n"))
			continue
		}
		
		channel.Write([]byte("\r\n\033[1;32mPerfect! Team name is available!\033[0m\r\n"))
		channel.Write([]byte(fmt.Sprintf("\033[2;37m   Your containers: \033[1;32m<name>.%s.exe.dev\033[0m\r\n\r\n", teamName)))
		
		return teamName, nil
	}
}


// updatePromptLine updates the current line with validation feedback (simplified)
func (s *Server) updatePromptLine(channel ssh.Channel, prompt, input, feedback string) {
	// Just show feedback on a new line instead of trying to be clever
	channel.Write([]byte(fmt.Sprintf("\r\n%s\r\n%s", feedback, prompt)))
}

// isValidTeamName validates team name format
func (s *Server) isValidTeamName(teamName string) bool {
	if len(teamName) < 3 || len(teamName) > 20 {
		return false
	}
	
	for _, char := range teamName {
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-') {
			return false
		}
	}
	
	// Cannot start or end with hyphen
	if teamName[0] == '-' || teamName[len(teamName)-1] == '-' {
		return false
	}
	
	// Cannot have consecutive hyphens
	if strings.Contains(teamName, "--") {
		return false
	}
	
	return true
}

// Start starts HTTP, HTTPS (if configured), and SSH servers
func (s *Server) Start() error {
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()
	
	// Start HTTP server in a goroutine
	go func() {
		log.Printf("HTTP server starting on %s", s.httpAddr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()
	
	// Start HTTPS server in a goroutine if configured
	if s.httpsAddr != "" {
		go func() {
			log.Printf("HTTPS server starting on %s with Let's Encrypt for exe.dev", s.httpsAddr)
			if err := s.httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTPS server error: %v", err)
			}
		}()
		
		// Start autocert HTTP handler for ACME challenges on port 80
		if s.certManager != nil {
			go func() {
				log.Printf("Starting autocert HTTP server on :80 for ACME challenges")
				if err := http.ListenAndServe(":80", s.certManager.HTTPHandler(nil)); err != nil {
					log.Printf("Autocert HTTP server error: %v", err)
				}
			}()
		}
	}
	
	// Start SSH server in a goroutine
	go func() {
		if err := s.serveSSH(); err != nil {
			log.Printf("SSH server error: %v", err)
		}
	}()
	
	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	
	log.Println("Shutting down servers...")
	return s.Stop()
}

// Database helper methods

// getUserByFingerprint retrieves a user by their SSH key fingerprint
func (s *Server) getUserByFingerprint(fingerprint string) (*User, error) {
	var user User
	err := s.db.QueryRow(`
		SELECT public_key_fingerprint, email, created_at 
		FROM users 
		WHERE public_key_fingerprint = ?`,
		fingerprint).Scan(&user.PublicKeyFingerprint, &user.Email, &user.CreatedAt)
	
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}

// getUserTeams returns all teams a user belongs to
func (s *Server) getUserTeams(fingerprint string) ([]TeamMember, error) {
	rows, err := s.db.Query(`
		SELECT user_fingerprint, team_name, is_admin, joined_at 
		FROM team_members 
		WHERE user_fingerprint = ?`,
		fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var teams []TeamMember
	for rows.Next() {
		var tm TeamMember
		if err := rows.Scan(&tm.UserFingerprint, &tm.TeamName, &tm.IsAdmin, &tm.JoinedAt); err != nil {
			return nil, err
		}
		teams = append(teams, tm)
	}
	return teams, rows.Err()
}

// createUser creates a new user
func (s *Server) createUser(fingerprint, email string) error {
	_, err := s.db.Exec(`
		INSERT INTO users (public_key_fingerprint, email) 
		VALUES (?, ?)`,
		fingerprint, email)
	return err
}

// createTeam creates a new team
func (s *Server) createTeam(name, billingEmail string) error {
	_, err := s.db.Exec(`
		INSERT INTO teams (name, billing_email) 
		VALUES (?, ?)`,
		name, billingEmail)
	return err
}

// addTeamMember adds a user to a team
func (s *Server) addTeamMember(fingerprint, teamName string, isAdmin bool) error {
	_, err := s.db.Exec(`
		INSERT INTO team_members (user_fingerprint, team_name, is_admin) 
		VALUES (?, ?, ?)`,
		fingerprint, teamName, isAdmin)
	return err
}

// isTeamNameTaken checks if a team name is already taken
func (s *Server) isTeamNameTaken(teamName string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM teams WHERE name = ?`, teamName).Scan(&count)
	return count > 0, err
}

// createInvite creates a new team invitation
func (s *Server) createInvite(teamName, createdByFingerprint, email string, maxUses int, expiresAt time.Time) (string, error) {
	code := s.generateInviteCode()
	_, err := s.db.Exec(`
		INSERT INTO invites (code, team_name, created_by_fingerprint, email, max_uses, expires_at) 
		VALUES (?, ?, ?, ?, ?, ?)
	`, code, teamName, createdByFingerprint, email, maxUses, expiresAt)
	return code, err
}

// getInviteByCode retrieves an invite by its code
func (s *Server) getInviteByCode(code string) (*Invite, error) {
	var invite Invite
	err := s.db.QueryRow(`
		SELECT code, team_name, created_by_fingerprint, email, max_uses, used_count, expires_at, created_at
		FROM invites WHERE code = ?
	`, code).Scan(&invite.Code, &invite.TeamName, &invite.CreatedByFingerprint, 
		&invite.Email, &invite.MaxUses, &invite.UsedCount, &invite.ExpiresAt, &invite.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &invite, nil
}

// useInvite increments the used count of an invite
func (s *Server) useInvite(code string) error {
	_, err := s.db.Exec("UPDATE invites SET used_count = used_count + 1 WHERE code = ?", code)
	return err
}

// generateInviteCode generates a random invite code
func (s *Server) generateInviteCode() string {
	return s.generateRegistrationToken()[:8] // Use first 8 chars for invite codes
}

// getInvitesByEmail retrieves all invites for a specific email
func (s *Server) getInvitesByEmail(email string) ([]Invite, error) {
	rows, err := s.db.Query(`
		SELECT code, team_name, created_by_fingerprint, email, max_uses, used_count, expires_at, created_at
		FROM invites WHERE email = ?
	`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var invites []Invite
	for rows.Next() {
		var invite Invite
		err := rows.Scan(&invite.Code, &invite.TeamName, &invite.CreatedByFingerprint,
			&invite.Email, &invite.MaxUses, &invite.UsedCount, &invite.ExpiresAt, &invite.CreatedAt)
		if err != nil {
			return nil, err
		}
		invites = append(invites, invite)
	}
	
	return invites, rows.Err()
}

// Stop gracefully shuts down all servers
func (s *Server) Stop() error {
	s.mu.Lock()
	s.stopping = true
	s.mu.Unlock()
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	// Shutdown HTTP server
	if err := s.httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	
	// Shutdown HTTPS server if running
	if s.httpsServer != nil {
		if err := s.httpsServer.Shutdown(ctx); err != nil {
			log.Printf("HTTPS server shutdown error: %v", err)
		}
	}
	
	// Close database connection
	if s.db != nil {
		s.db.Close()
	}
	
	log.Println("Servers stopped")
	return nil
}
