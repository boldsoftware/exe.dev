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

	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
)

// Registration represents a pending user registration
type Registration struct {
	PublicKeyFingerprint string
	Token                string
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
	
	// In-memory databases
	publicKeysMu    sync.RWMutex
	publicKeys      map[string]bool // fingerprint -> registered
	registrationsMu sync.RWMutex
	registrations   map[string]*Registration // token -> registration
	
	mu       sync.RWMutex
	stopping bool
}

// NewServer creates a new Server instance
func NewServer(httpAddr, httpsAddr, sshAddr string) *Server {
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
		httpAddr:      httpAddr,
		httpsAddr:     httpsAddr,
		sshAddr:       sshAddr,
		BaseURL:       baseURL,
		publicKeys:    make(map[string]bool),
		registrations: make(map[string]*Registration),
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
	
	s.publicKeysMu.RLock()
	registered, exists := s.publicKeys[fingerprint]
	s.publicKeysMu.RUnlock()
	
	if exists && registered {
		// Key is registered, allow authentication
		return &ssh.Permissions{
			Extensions: map[string]string{
				"fingerprint": fingerprint,
				"registered":  "true",
			},
		}, nil
	}
	
	// Key is not registered, allow connection but mark as needing registration
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
	case "/register":
		s.handleRegistrationHTTP(w, r)
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

// handleRegistrationHTTP handles web-based registration completion
func (s *Server) handleRegistrationHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Missing token parameter", http.StatusBadRequest)
		return
	}
	
	s.registrationsMu.Lock()
	reg, exists := s.registrations[token]
	if !exists {
		s.registrationsMu.Unlock()
		http.Error(w, "Invalid or expired registration token", http.StatusNotFound)
		return
	}
	
	// Mark the public key as registered
	s.publicKeysMu.Lock()
	s.publicKeys[reg.PublicKeyFingerprint] = true
	s.publicKeysMu.Unlock()
	
	// Signal completion to SSH session
	close(reg.CompleteChan)
	
	// Clean up registration
	delete(s.registrations, token)
	s.registrationsMu.Unlock()
	
	// Send success response
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>Registration Complete - exe.dev</title>
    <style>
        body { font-family: Arial, sans-serif; max-width: 600px; margin: 50px auto; padding: 20px; }
        .success { color: #4CAF50; text-align: center; }
        .code { background-color: #f5f5f5; padding: 10px; font-family: monospace; }
    </style>
</head>
<body>
    <h1 class="success">✅ Registration Completed!</h1>
    <p>Your SSH public key has been successfully registered with exe.dev.</p>
    <p>You can now return to your SSH session - it should continue automatically.</p>
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
	welcome := `
Welcome to exe.dev!

Container Management Console
============================

Available commands:
  list      - List your containers
  create    - Create a new container  
  start     - Start a container
  stop      - Stop a container
  delete    - Delete a container
  logs      - View container logs
  help      - Show this help
  exit      - Exit

> `
	
	channel.Write([]byte(welcome))
	
	// Simple command loop - TODO: Implement proper shell with readline
	buffer := make([]byte, 1024)
	for {
		n, err := channel.Read(buffer)
		if err != nil {
			break
		}
		
		command := string(buffer[:n])
		command = string([]rune(command)) // Handle UTF-8
		
		switch command {
		case "exit\n", "exit\r\n":
			channel.Write([]byte("Goodbye!\r\n"))
			return
		case "help\n", "help\r\n":
			channel.Write([]byte(welcome))
		case "list\n", "list\r\n":
			channel.Write([]byte("No containers found.\r\n> "))
		default:
			channel.Write([]byte("Unknown command. Type 'help' for available commands.\r\n> "))
		}
	}
}

// handleRegistration manages the user registration process
func (s *Server) handleRegistration(channel ssh.Channel, fingerprint string) {
	// Generate registration token
	token := s.generateRegistrationToken()
	
	// Create registration
	reg := &Registration{
		PublicKeyFingerprint: fingerprint,
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}
	
	// Store registration
	s.registrationsMu.Lock()
	s.registrations[token] = reg
	s.registrationsMu.Unlock()
	
	// Generate registration URL
	registrationURL := fmt.Sprintf("%s/register?token=%s", s.BaseURL, token)
	
	message := fmt.Sprintf(`
Welcome to exe.dev!

Your SSH public key is not registered yet.
To complete registration, please visit:

%s

Waiting for registration to complete...
(This session will continue automatically once registration is done)

`, registrationURL)
	
	channel.Write([]byte(message))
	
	// Wait for registration completion or timeout
	select {
	case <-reg.CompleteChan:
		channel.Write([]byte("\r\nRegistration completed! Welcome to exe.dev!\r\n\r\n"))
		
		// Continue with normal shell flow
		s.runMainShell(channel)
		
	case <-time.After(10 * time.Minute):
		channel.Write([]byte("\r\nRegistration timeout. Please try connecting again.\r\n"))
		
		// Clean up registration
		s.registrationsMu.Lock()
		delete(s.registrations, token)
		s.registrationsMu.Unlock()
	}
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
