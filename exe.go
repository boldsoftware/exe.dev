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
	"net"
	"net/http"
	"os"
	"os/signal"
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
	CreatedByFingerprint string
	CreatedAt            time.Time
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
	
	// In-memory state for active sessions (these don't need persistence)
	emailVerificationsMu    sync.RWMutex
	emailVerifications      map[string]*EmailVerification // token -> email verification
	billingVerificationsMu  sync.RWMutex
	billingVerifications    map[string]*BillingVerification // fingerprint -> billing verification
	
	// Email and billing services
	postmarkClient *postmark.Client
	stripeKey      string
	
	mu       sync.RWMutex
	stopping bool
}

// NewServer creates a new Server instance with database
func NewServer(httpAddr, httpsAddr, sshAddr, dbPath string) (*Server, error) {
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
	
	s := &Server{
		httpAddr:             httpAddr,
		httpsAddr:            httpsAddr,
		sshAddr:              sshAddr,
		BaseURL:              baseURL,
		db:                   db,
		emailVerifications:   make(map[string]*EmailVerification),
		billingVerifications: make(map[string]*BillingVerification),
		postmarkClient:       postmarkClient,
		stripeKey:            stripeKey,
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
		go s.handleSSHChannel(newChannel, fingerprint, registered)
	}
}

// handleSSHChannel handles SSH channels
func (s *Server) handleSSHChannel(newChannel ssh.NewChannel, fingerprint string, registered bool) {
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
	
	// Handle requests
	for req := range requests {
		switch req.Type {
		case "pty-req":
			// Parse PTY request and set up terminal properly
			if len(req.Payload) > 0 {
				// PTY request format: string term, uint32 cols, uint32 rows, uint32 pixWidth, uint32 pixHeight, string modes
				// For now, just accept it - we could parse terminal modes here if needed
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
			s.handleSSHShell(channel, fingerprint, registered)
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

// handleSSHShell provides the guided console management tool
func (s *Server) handleSSHShell(channel ssh.Channel, fingerprint string, registered bool) {
	if !registered {
		// Handle registration flow
		s.handleRegistration(channel, fingerprint)
		return
	}
	
	s.runMainShell(channel)
}

// runMainShell runs the main container management shell
func (s *Server) runMainShell(channel ssh.Channel) {
	welcome := "\r\n\033[1;32m███████╗██╗  ██╗███████╗   ██████╗ ███████╗██╗   ██╗\r\n" +
		"██╔════╝╚██╗██╔╝██╔════╝   ██╔══██╗██╔════╝██║   ██║\r\n" +
		"█████╗   ╚███╔╝ █████╗     ██║  ██║█████╗  ██║   ██║\r\n" +
		"██╔══╝   ██╔██╗ ██╔══╝     ██║  ██║██╔══╝  ╚██╗ ██╔╝\r\n" +
		"███████╗██╔╝ ██╗███████╗██╗██████╔╝███████╗ ╚████╔╝ \r\n" +
		"╚══════╝╚═╝  ╚═╝╚══════╝╚═╝╚═════╝ ╚══════╝  ╚═══╝  \033[0m\r\n\r\n" +
		"\033[1;33mContainer Management Console\033[0m\r\n\r\n" +
		"\033[1mAvailable commands:\033[0m\r\n\r\n" +
		"\033[1mlist\033[0m      - List your containers\r\n" +
		"\033[1mcreate\033[0m    - Create a new container\r\n" +
		"\033[1mstart\033[0m     - Start a container\r\n" +
		"\033[1mstop\033[0m      - Stop a container\r\n" +
		"\033[1mdelete\033[0m    - Delete a container\r\n" +
		"\033[1mlogs\033[0m      - View container logs\r\n" +
		"\033[1mhelp\033[0m      - Show this help\r\n" +
		"\033[1mexit\033[0m      - Exit\r\n\r\n"
	
	channel.Write([]byte(welcome))
	
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
		
		switch strings.TrimSpace(command) {
		case "exit":
			channel.Write([]byte("Goodbye!\r\n"))
			return
		case "help":
			channel.Write([]byte(welcome))
		case "list":
			channel.Write([]byte("No containers found.\r\n"))
		case "":
			// Empty command, just continue
		default:
			channel.Write([]byte("Unknown command. Type 'help' for available commands.\r\n"))
		}
	}
}

// handleRegistration manages the user registration process with email verification and billing
func (s *Server) handleRegistration(channel ssh.Channel, fingerprint string) {
	welcome := "\r\n\033[1;32m███████╗██╗  ██╗███████╗   ██████╗ ███████╗██╗   ██╗\r\n" +
		"██╔════╝╚██╗██╔╝██╔════╝   ██╔══██╗██╔════╝██║   ██║\r\n" +
		"█████╗   ╚███╔╝ █████╗     ██║  ██║█████╗  ██║   ██║\r\n" +
		"██╔══╝   ██╔██╗ ██╔══╝     ██║  ██║██╔══╝  ╚██╗ ██╔╝\r\n" +
		"███████╗██╔╝ ██╗███████╗██╗██████╔╝███████╗ ╚████╔╝ \r\n" +
		"╚══════╝╚═╝  ╚═╝╚══════╝╚═╝╚═════╝ ╚══════╝  ╚═══╝  \033[0m\r\n\r\n" +
		"\033[1;33mtype ssh to get a server\033[0m\r\n\r\n" +
		"Let's get you set up in just a few steps:\r\n\r\n" +
		"\033[2;37m1. Email Verification\r\n" +
		"2. Team Setup\r\n" +
		"3. Payment Setup\033[0m\r\n\r\n" +
		"\033[1mTo get started, please enter your email address:\033[0m\r\n" +
		""
	
	channel.Write([]byte(welcome))
	
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
	if s.postmarkClient == nil {
		return fmt.Errorf("email service not configured")
	}
	
	verificationURL := fmt.Sprintf("%s/verify-email?token=%s", s.BaseURL, token)
	
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
	
	// Continue with normal shell flow
	s.runMainShell(channel)
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
	
	// Continue with normal shell flow
	s.runMainShell(channel)
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
