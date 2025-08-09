// Package exe implements the bulk of the exed server.
package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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
)

// EmailVerification represents a pending email verification
type EmailVerification struct {
	PublicKeyFingerprint string
	Email                string
	Token                string
	CompleteChan         chan struct{}
	CreatedAt            time.Time
}

// BillingVerification represents a pending billing verification
type BillingVerification struct {
	PublicKeyFingerprint string
	Email                string
	CompleteChan         chan struct{}
	CreatedAt            time.Time
}

// User represents a fully registered user
type User struct {
	PublicKeyFingerprint string
	Email                string
	TeamName             string
	StripeCustomerID     string
	RegisteredAt         time.Time
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
	
	// In-memory databases
	usersMu                 sync.RWMutex
	users                   map[string]*User // fingerprint -> user
	teamNamesMu             sync.RWMutex
	teamNames               map[string]bool // team name -> taken
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

// NewServer creates a new Server instance
func NewServer(httpAddr, httpsAddr, sshAddr string) *Server {
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
		users:                make(map[string]*User),
		teamNames:            make(map[string]bool),
		emailVerifications:   make(map[string]*EmailVerification),
		billingVerifications: make(map[string]*BillingVerification),
		postmarkClient:       postmarkClient,
		stripeKey:            stripeKey,
	}
	
	s.setupHTTPServer()
	s.setupHTTPSServer()
	s.setupSSHServer()
	
	return s
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
	
	s.usersMu.RLock()
	user, exists := s.users[fingerprint]
	s.usersMu.RUnlock()
	
	if exists {
		// User is fully registered, allow authentication
		return &ssh.Permissions{
			Extensions: map[string]string{
				"fingerprint": fingerprint,
				"registered":  "true",
				"email":       user.Email,
			},
		}, nil
	}
	
	// User is not registered, allow connection but mark as needing registration
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
	welcome := "Welcome to exe.dev!\r\n\r\nContainer Management Console\r\n============================\r\n\r\nAvailable commands:\r\n  list      - List your containers\r\n  create    - Create a new container\r\n  start     - Start a container\r\n  stop      - Stop a container\r\n  delete    - Delete a container\r\n  logs      - View container logs\r\n  help      - Show this help\r\n  exit      - Exit\r\n\r\n"
	
	channel.Write([]byte(welcome))
	
	// Command loop using proper line reading
	for {
		channel.Write([]byte("> "))
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
	welcome := "Welcome to exe.dev!\r\n\r\nTo get started, we need to verify your email address.\r\n\r\nPlease enter your email address: "
	
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
	
	channel.Write([]byte(fmt.Sprintf("\r\nEmail: %s\r\n", email)))
	
	// Start email verification flow
	if err := s.startEmailVerification(channel, fingerprint, email); err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\nError sending verification email: %v\r\n", err)))
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
	
	channel.Write([]byte("\r\nVerification email sent! Please check your email and click the verification link.\r\n"))
	channel.Write([]byte("Waiting for email verification...\r\n\r\n"))
	
	// Wait for email verification or timeout
	select {
	case <-verification.CompleteChan:
		channel.Write([]byte("\r\n✅ Email verified!\r\n\r\n"))
		
		// Start billing verification
		s.startBillingVerification(channel, fingerprint, email)
		
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

// startBillingVerification initiates the billing verification process
func (s *Server) startBillingVerification(channel ssh.Channel, fingerprint, email string) {
	// Store billing verification state
	billing := &BillingVerification{
		PublicKeyFingerprint: fingerprint,
		Email:                email,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}
	
	s.billingVerificationsMu.Lock()
	s.billingVerifications[fingerprint] = billing
	s.billingVerificationsMu.Unlock()
	
	message := "Now we need to verify your billing information.\r\n\r\nPlease enter a test credit card number to verify your payment method.\r\nYou can use: 4242424242424242 (Visa test card)\r\n\r\nCredit card number: "
	
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
	
	channel.Write([]byte("\r\n✅ Payment method verified!\r\n\r\n"))
	
	// Start team name creation
	teamName, err := s.createTeamName(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\r\nError creating team: %v\r\n", err)))
		return
	}
	
	// Create user account
	user := &User{
		PublicKeyFingerprint: fingerprint,
		Email:                email,
		TeamName:             teamName,
		StripeCustomerID:     "", // TODO: Create actual Stripe customer
		RegisteredAt:         time.Now(),
	}
	
	s.usersMu.Lock()
	s.users[fingerprint] = user
	s.usersMu.Unlock()
	
	// Clean up verification states
	s.billingVerificationsMu.Lock()
	delete(s.billingVerifications, fingerprint)
	s.billingVerificationsMu.Unlock()
	
	channel.Write([]byte("🎉 Registration completed! Welcome to exe.dev!\r\n\r\n"))
	
	// Continue with normal shell flow
	s.runMainShell(channel)
}

// verifyPaymentMethod verifies a payment method with Stripe
func (s *Server) verifyPaymentMethod(cardNumber string) error {
	// Remove spaces from card number
	cardNumber = strings.ReplaceAll(cardNumber, " ", "")
	
	// Basic validation - check if it's the test card number
	if cardNumber != "4242424242424242" {
		return fmt.Errorf("invalid card number. Please use the test card: 4242424242424242")
	}
	
	// Create a payment method with Stripe (simplified)
	params := &stripe.PaymentMethodParams{
		Type: stripe.String(string(stripe.PaymentMethodTypeCard)),
		Card: &stripe.PaymentMethodCardParams{
			Number:   stripe.String(cardNumber),
			ExpMonth: stripe.Int64(12),
			ExpYear:  stripe.Int64(2025),
			CVC:      stripe.String("123"),
		},
	}
	
	_, err := paymentmethod.New(params)
	if err != nil {
		return fmt.Errorf("payment method creation failed: %w", err)
	}
	
	return nil
}

// createTeamName handles team name creation with simple validation
func (s *Server) createTeamName(channel ssh.Channel) (string, error) {
	channel.Write([]byte("Now let's create your team name.\r\n\r\n"))
	channel.Write([]byte("By default, containers will start as <name>.<team>.exe.dev\r\n\r\n"))
	
	for {
		channel.Write([]byte("Team name: "))
		
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
			channel.Write([]byte("❌ Invalid team name (must be 3-20 lowercase letters/numbers/hyphens)\r\n\r\n"))
			continue
		}
		
		// Check if team name is available
		s.teamNamesMu.RLock()
		taken := s.teamNames[teamName]
		s.teamNamesMu.RUnlock()
		
		if taken {
			channel.Write([]byte("❌ Team name already taken\r\n\r\n"))
			continue
		}
		
		// Team name is valid and available
		s.teamNamesMu.Lock()
		s.teamNames[teamName] = true
		s.teamNamesMu.Unlock()
		
		channel.Write([]byte("✅ Team name available!\r\n"))
		channel.Write([]byte(fmt.Sprintf("Your containers will be available at: <name>.%s.exe.dev\r\n\r\n", teamName)))
		
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
	
	log.Println("Servers stopped")
	return nil
}
