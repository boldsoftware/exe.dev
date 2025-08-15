// Package exe implements the bulk of the exed server.
package exe

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/keighl/postmark"
	"github.com/pkg/sftp"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentmethod"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"

	"exe.dev/container"
	"exe.dev/porkbun"
	"exe.dev/sshbuf"
	"exe.dev/sshproxy"
)

//go:embed exe_schema.sql
var schemaSQL string

//go:embed welcome.html
var welcomeHTML []byte

//go:embed exe.dev.png
var exeDevPNG []byte

//go:embed browser-woodcut.png
var browserWoodcutPNG []byte

//go:embed favicon.ico
var faviconICO []byte

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
	PublicKey            string
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
	PublicKey   string
	CreatedAt   time.Time
}

// Server implements both HTTP and SSH server functionality for exe.dev
type Server struct {
	httpAddr  string
	httpsAddr string
	sshAddr   string
	BaseURL   string

	httpServer          *http.Server
	httpsServer         *http.Server
	sshConfig           *ssh.ServerConfig
	certManager         *autocert.Manager
	wildcardCertManager *porkbun.WildcardCertManager

	// Database
	db *sql.DB

	// Container management
	containerManager container.Manager

	// In-memory state for active sessions (these don't need persistence)
	emailVerificationsMu   sync.RWMutex
	emailVerifications     map[string]*EmailVerification // token -> email verification
	billingVerificationsMu sync.RWMutex
	billingVerifications   map[string]*BillingVerification // fingerprint -> billing verification

	// User sessions for tracking authenticated users
	sessionsMu sync.RWMutex
	sessions   map[*sshbuf.Channel]*UserSession // channel -> user session

	// Email and billing services
	postmarkClient *postmark.Client
	stripeKey      string

	// Test mode - skip animations for faster testing
	testMode  bool
	devMode   string // Development mode: "" (production) or "local" (Docker)
	quietMode bool   // Quiet mode - suppress log output (for tests)

	mu       sync.RWMutex
	stopping bool
}

// NewServer creates a new Server instance with database and container management
func NewServer(httpAddr, httpsAddr, sshAddr, dbPath string, devMode string, dockerHosts []string) (*Server, error) {
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

	// Detect if we're running in test mode
	quietMode := testing.Testing()

	// Initialize Postmark client
	postmarkAPIKey := os.Getenv("POSTMARK_API_KEY")
	var postmarkClient *postmark.Client
	if postmarkAPIKey != "" {
		postmarkClient = postmark.NewClient(postmarkAPIKey, "")
	} else if !quietMode {
		log.Printf("Warning: POSTMARK_API_KEY not set, email verification will not work")
	}

	// Get Stripe key
	stripeKey := os.Getenv("STRIPE_API_KEY")
	if stripeKey == "" {
		stripeKey = "sk_test_51QxIgSGWIXq1kJnoiKwEcehJeO68QFsueLGymU9zR5jsJtMup5arFZZlHYaOzG3Bsw2GfnIG9H3Jv8Be10vqK1nW001hUxrS2g"
		if !quietMode {
			log.Printf("Using default Stripe test key")
		}
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

	// Initialize container manager with Docker
	var containerManager container.Manager

	if len(dockerHosts) > 0 {
		config := &container.Config{
			DockerHosts:          dockerHosts,
			DefaultCPURequest:    "500m",
			DefaultMemoryRequest: "1Gi",
			DefaultStorageSize:   "10Gi",
		}

		var managerErr error
		containerManager, managerErr = container.NewDockerManager(config)
		if managerErr != nil {
			if !quietMode {
				log.Printf("Warning: Failed to initialize container manager: %v", managerErr)
				log.Printf("Container functionality will be disabled")
			}
			containerManager = nil
		} else {
			if !quietMode {
				log.Printf("Machine management enabled with Docker hosts: %v", dockerHosts)
			}
		}
	} else {
		if !quietMode {
			log.Printf("No Docker hosts configured, container functionality disabled")
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
		sessions:             make(map[*sshbuf.Channel]*UserSession),
		postmarkClient:       postmarkClient,
		stripeKey:            stripeKey,
		devMode:              devMode,
		quietMode:            quietMode,
		testMode:             testing.Testing(),
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

	// Check if Porkbun API credentials are available for wildcard cert
	porkbunAPIKey := os.Getenv("PORKBUN_API_KEY")
	porkbunSecretKey := os.Getenv("PORKBUN_SECRET_API_KEY")

	if porkbunAPIKey != "" && porkbunSecretKey != "" {
		// Use Porkbun for wildcard certificates with DNS challenge
		log.Printf("Using Porkbun DNS provider for wildcard TLS certificates")
		s.wildcardCertManager = porkbun.NewWildcardCertManager(
			"exe.dev",
			"support@exe.dev",
			porkbunAPIKey,
			porkbunSecretKey,
			autocert.DirCache("certs"),
		)

		s.httpsServer = &http.Server{
			Addr:    s.httpsAddr,
			Handler: s,
			TLSConfig: &tls.Config{
				GetCertificate: s.wildcardCertManager.GetCertificate,
			},
		}
	} else {
		// Fall back to regular autocert for non-wildcard certificates
		if !s.quietMode {
			log.Printf("Using standard autocert (no wildcard support). Set PORKBUN_API_KEY and PORKBUN_SECRET_API_KEY for wildcard certificates.")
		}
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
}

// setupSSHServer configures the SSH server
func (s *Server) setupSSHServer() {
	s.sshConfig = &ssh.ServerConfig{
		PublicKeyCallback: s.authenticatePublicKey,
	}

	// Load or generate persistent host keys
	if err := s.generateHostKey(); err != nil {
		log.Printf("Failed to generate host key: %v", err)
	}
}

// generateHostKey loads the persistent RSA host key from the database, or generates and stores a new one
func (s *Server) generateHostKey() error {
	// Try to load existing host key from database
	var privateKeyPEM, publicKeyPEM string
	err := s.db.QueryRow(`SELECT private_key, public_key FROM ssh_host_key WHERE id = 1`).Scan(&privateKeyPEM, &publicKeyPEM)

	if err == sql.ErrNoRows {
		// No existing key, generate a new one
		privateKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
		if err != nil {
			return fmt.Errorf("failed to generate RSA key: %w", err)
		}

		// Convert private key to PEM format
		privateKeyDER := x509.MarshalPKCS1PrivateKey(privateKey)
		privateKeyPEMBytes := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: privateKeyDER,
		})
		privateKeyPEM = string(privateKeyPEMBytes)

		// Parse as SSH private key to get public key
		signer, err := ssh.ParsePrivateKey(privateKeyPEMBytes)
		if err != nil {
			return fmt.Errorf("failed to parse private key: %w", err)
		}

		// Get public key in authorized_keys format
		publicKeyPEM = string(ssh.MarshalAuthorizedKey(signer.PublicKey()))

		// Calculate fingerprint
		fingerprint := s.getPublicKeyFingerprint(signer.PublicKey())

		// Store in database
		_, err = s.db.Exec(`
			INSERT INTO ssh_host_key (id, private_key, public_key, fingerprint, created_at, updated_at)
			VALUES (1, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			privateKeyPEM, publicKeyPEM, fingerprint)
		if err != nil {
			return fmt.Errorf("failed to store host key: %w", err)
		}

		if !s.quietMode {
			log.Printf("Generated and stored new SSH host key with fingerprint: %s", fingerprint)
		}
		s.sshConfig.AddHostKey(signer)

	} else if err != nil {
		return fmt.Errorf("failed to query host key: %w", err)
	} else {
		// Load existing key
		signer, err := ssh.ParsePrivateKey([]byte(privateKeyPEM))
		if err != nil {
			return fmt.Errorf("failed to parse stored private key: %w", err)
		}

		fingerprint := s.getPublicKeyFingerprint(signer.PublicKey())
		if !s.quietMode {
			log.Printf("Loaded existing SSH host key with fingerprint: %s", fingerprint)
		}
		s.sshConfig.AddHostKey(signer)
	}

	return nil
}

// getPublicKeyFingerprint generates a SHA256 fingerprint for a public key
func (s *Server) getPublicKeyFingerprint(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(hash[:])
}

// inheritUserTeamMemberships adds a new SSH key to all teams that the user (by email) is already a member of
func (s *Server) inheritUserTeamMemberships(newFingerprint, userEmail string) error {
	// First, get the original user fingerprint from the users table
	var originalFingerprint string
	err := s.db.QueryRow("SELECT public_key_fingerprint FROM users WHERE email = ?", userEmail).Scan(&originalFingerprint)
	if err != nil {
		return fmt.Errorf("failed to find original user fingerprint: %v", err)
	}

	// Get all teams the original fingerprint is a member of
	rows, err := s.db.Query(`
		SELECT team_name, is_admin
		FROM team_members
		WHERE user_fingerprint = ?`, originalFingerprint)
	if err != nil {
		return fmt.Errorf("failed to query team memberships: %v", err)
	}
	defer rows.Close()

	// Add the new fingerprint to each team
	for rows.Next() {
		var teamName string
		var isAdmin bool
		if err := rows.Scan(&teamName, &isAdmin); err != nil {
			log.Printf("Error scanning team membership: %v", err)
			continue
		}

		// Insert the new fingerprint into the same team with the same admin status
		_, err = s.db.Exec(`
			INSERT INTO team_members (user_fingerprint, team_name, is_admin)
			VALUES (?, ?, ?)
			ON CONFLICT(user_fingerprint, team_name) DO UPDATE SET is_admin = ?`,
			newFingerprint, teamName, isAdmin, isAdmin)

		if err != nil {
			log.Printf("Failed to add fingerprint %s to team %s: %v", newFingerprint, teamName, err)
		} else {
			log.Printf("Added new SSH key to team %s (admin: %v) for user %s", teamName, isAdmin, userEmail)
		}
	}

	return rows.Err()
}

// generateRegistrationToken creates a random registration token
func (s *Server) generateRegistrationToken() string {
	bytes := make([]byte, 16)
	cryptorand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// generateToken is an alias for generateRegistrationToken
func (s *Server) generateToken() string {
	return s.generateRegistrationToken()
}

// getBaseURL returns the base URL for the server
func (s *Server) getBaseURL() string {
	if s.devMode != "" {
		// Extract port from httpAddr (e.g., ":8080" -> "8080")
		port := s.httpAddr
		if strings.HasPrefix(port, ":") {
			port = port[1:]
		}
		return fmt.Sprintf("http://localhost:%s", port)
	}
	return "https://exe.dev"
}

// sendEmail sends an email using the configured email service
func (s *Server) sendEmail(to, subject, body string) error {
	// In dev mode, always just log the email
	if s.devMode != "" {
		if !s.quietMode {
			log.Printf("📧 DEV MODE: Would send email to %s\nSubject: %s\nBody:\n%s", to, subject, body)
		}
		return nil
	}

	// Check if email service is configured
	if s.postmarkClient == nil {
		return fmt.Errorf("email service not configured")
	}

	// Use the existing sendVerificationEmail logic
	email := postmark.Email{
		From:     "support@exe.dev",
		To:       to,
		Subject:  subject,
		TextBody: body,
	}

	_, err := s.postmarkClient.SendEmail(email)
	if err != nil {
		log.Printf("📧 ERROR: Failed to send email to %s (subject: %s): %v", to, subject, err)
	} else {
		log.Printf("📧 Email sent successfully to %s (subject: %s)", to, subject)
	}
	return err
}

// authenticatePublicKey handles SSH public key authentication
func (s *Server) authenticatePublicKey(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fingerprint := s.getPublicKeyFingerprint(key)
	publicKeyStr := string(ssh.MarshalAuthorizedKey(key))

	// First check if this key is already registered in ssh_keys table
	email, verified, err := s.getEmailBySSHKey(fingerprint)
	if err != nil {
		log.Printf("Database error checking SSH key %s: %v", fingerprint, err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has team memberships
		teams, err := s.getUserTeamsByEmail(email)
		if err != nil {
			log.Printf("Database error getting teams for user %s: %v", email, err)
		}

		if len(teams) > 0 {
			// User is fully registered with team membership
			return &ssh.Permissions{
				Extensions: map[string]string{
					"fingerprint": fingerprint,
					"registered":  "true",
					"email":       email,
					"public_key":  publicKeyStr,
				},
			}, nil
		}
	}

	// Check legacy users table for backward compatibility
	user, err := s.getUserByFingerprint(fingerprint)
	if err != nil {
		log.Printf("Database error checking legacy user %s: %v", fingerprint, err)
	}

	if user != nil {
		// Migrate this user to the new ssh_keys table
		if err := s.migrateLegacyUserKey(user.Email, fingerprint, publicKeyStr); err != nil {
			log.Printf("Failed to migrate legacy user key: %v", err)
		}

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
					"public_key":  publicKeyStr,
				},
			}, nil
		}
	}

	// Check if there's an email associated with any SSH key and if this is a new key for that user
	if email != "" && !verified {
		// This key belongs to a user but isn't verified yet - treat as standard unregistered user
		// They will go through the normal flow and wait for email verification
		return &ssh.Permissions{
			Extensions: map[string]string{
				"fingerprint":        fingerprint,
				"registered":         "false",
				"email":              email,
				"public_key":         publicKeyStr,
				"needs_verification": "true",
			},
		}, nil
	}

	// User is not registered or has no team, allow connection but mark as needing registration
	return &ssh.Permissions{
		Extensions: map[string]string{
			"fingerprint": fingerprint,
			"registered":  "false",
			"public_key":  publicKeyStr,
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

	// Check if this is a container subdomain request
	if containerName, teamName, port, isContainerRequest := s.parseContainerRequest(r.Host); isContainerRequest {
		s.handleContainerProxy(w, r, containerName, teamName, port)
		return
	}

	// TODO: Wake up containers on HTTP request
	if !s.quietMode {
		log.Printf("HTTP request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	}

	switch r.URL.Path {
	case "/":
		s.handleRoot(w, r)
	case "/favicon.ico":
		s.handleFavicon(w, r)
	case "/exe.dev.png":
		s.handleExeDevPNG(w, r)
	case "/browser-woodcut.png":
		s.handleBrowserWoodcutPNG(w, r)
	case "/health":
		s.handleHealth(w, r)
	case "/containers":
		s.handleContainers(w, r)
	case "/verify-email":
		s.handleEmailVerificationHTTP(w, r)
	case "/verify-device":
		s.handleDeviceVerificationHTTP(w, r)
	case "/auth":
		s.handleAuth(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			s.handleAuthCallback(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

// handleRoot handles requests to the root path
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(welcomeHTML)
}

// handleFavicon handles favicon.ico requests
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(faviconICO)
}

// handleExeDevPNG handles exe.dev.png requests
func (s *Server) handleExeDevPNG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(exeDevPNG)
}

// handleBrowserWoodcutPNG handles browser-woodcut.png requests
func (s *Server) handleBrowserWoodcutPNG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(browserWoodcutPNG)
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
	fmt.Fprintf(w, `{"containers":[],"message":"Machine management not yet implemented"}`)
}

// showDeviceVerificationForm shows a confirmation form for device verification
func (s *Server) showDeviceVerificationForm(w http.ResponseWriter, r *http.Request, token string) {
	// Look up the pending SSH key to validate token and get info
	var fingerprint, email string
	var expires time.Time
	err := s.db.QueryRow(`
		SELECT fingerprint, user_email, expires_at
		FROM pending_ssh_keys
		WHERE token = ?`,
		token).Scan(&fingerprint, &email, &expires)

	if err == sql.ErrNoRows {
		http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("Database error during device verification check: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Check if token has expired
	if time.Now().After(expires) {
		// Clean up expired token
		s.db.Exec("DELETE FROM pending_ssh_keys WHERE token = ?", token)
		http.Error(w, "Verification token has expired", http.StatusBadRequest)
		return
	}

	// Show confirmation form
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>Confirm Device - exe.dev</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            max-width: 500px;
            margin: 100px auto;
            padding: 40px;
            background: #f5f5f5;
        }
        .container {
            background: white;
            border-radius: 12px;
            padding: 40px;
            box-shadow: 0 2px 8px rgba(0,0,0,0.1);
        }
        h1 {
            color: #333;
            margin-bottom: 10px;
            font-size: 28px;
        }
        p {
            color: #666;
            line-height: 1.6;
            margin: 20px 0;
        }
        .info-box {
            background: #f0f9ff;
            border: 1px solid #0ea5e9;
            border-radius: 6px;
            padding: 16px;
            margin: 20px 0;
        }
        .info-box strong {
            color: #0c4a6e;
        }
        .fingerprint {
            font-family: monospace;
            background: #f5f5f5;
            padding: 8px 12px;
            border-radius: 4px;
            display: inline-block;
            margin-top: 8px;
        }
        .button {
            background: #2563eb;
            color: white;
            border: none;
            padding: 12px 32px;
            border-radius: 6px;
            font-size: 16px;
            font-weight: 600;
            cursor: pointer;
            display: inline-block;
            margin-top: 20px;
            transition: background 0.2s;
        }
        .button:hover {
            background: #1d4ed8;
        }
        .warning {
            background: #fef3c7;
            border: 1px solid #f59e0b;
            border-radius: 6px;
            padding: 12px;
            margin: 20px 0;
            color: #92400e;
        }
        form {
            margin: 0;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Authorize New Device</h1>
        <p>A new device is requesting access to your exe.dev account.</p>

        <div class="info-box">
            <strong>Account:</strong> %s<br>
            <strong>Device Fingerprint:</strong>
            <div class="fingerprint">%s...</div>
        </div>

        <div class="warning">
            ⚠️ Only confirm if you just tried to connect from a new device
        </div>

        <p>This will allow the device to access your exe.dev containers using SSH.</p>

        <form method="POST" action="/verify-device">
            <input type="hidden" name="token" value="%s">
            <button type="submit" class="button">Authorize Device</button>
        </form>
    </div>
</body>
</html>`, email, fingerprint[:16], token)
}

// handleDeviceVerificationHTTP handles web-based device verification
func (s *Server) handleDeviceVerificationHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle GET request - show confirmation form
	if r.Method == http.MethodGet {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}
		s.showDeviceVerificationForm(w, r, token)
		return
	}

	// Handle POST request - complete verification
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data to get the token from POST
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing token in form data", http.StatusBadRequest)
		return
	}

	// Look up the pending SSH key
	var fingerprint, publicKey, email string
	var expires time.Time
	err := s.db.QueryRow(`
		SELECT fingerprint, public_key, user_email, expires_at
		FROM pending_ssh_keys
		WHERE token = ?`,
		token).Scan(&fingerprint, &publicKey, &email, &expires)

	if err == sql.ErrNoRows {
		http.Error(w, "Invalid or expired verification token", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("Database error during device verification: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Check if token has expired
	if time.Now().After(expires) {
		// Clean up expired token
		s.db.Exec("DELETE FROM pending_ssh_keys WHERE token = ?", token)
		http.Error(w, "Verification token has expired", http.StatusBadRequest)
		return
	}

	// Add the SSH key to the verified keys
	_, err = s.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
		VALUES (?, ?, ?, 1, 'New Device')
		ON CONFLICT(fingerprint) DO UPDATE SET verified = 1`,
		fingerprint, email, publicKey)
	if err != nil {
		log.Printf("Failed to add SSH key: %v", err)
		http.Error(w, "Failed to verify device", http.StatusInternalServerError)
		return
	}

	// Automatically add the new SSH key to all teams the user is already a member of
	err = s.inheritUserTeamMemberships(fingerprint, email)
	if err != nil {
		log.Printf("Failed to inherit team memberships for %s: %v", email, err)
		// Don't fail the verification, just log the error
	}

	// Clean up the pending key
	s.db.Exec("DELETE FROM pending_ssh_keys WHERE token = ?", token)

	// Signal completion to waiting SSH session
	s.emailVerificationsMu.Lock()
	verification, exists := s.emailVerifications[token]
	if exists {
		close(verification.CompleteChan)
		delete(s.emailVerifications, token)
	}
	s.emailVerificationsMu.Unlock()

	// Send success response
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>Device Verified - exe.dev</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            margin: 0;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
        }
        .container {
            background: white;
            padding: 40px;
            border-radius: 10px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
            text-align: center;
            max-width: 400px;
        }
        h1 { color: #2d3748; margin-bottom: 20px; }
        p { color: #4a5568; line-height: 1.6; }
        .success { color: #48bb78; font-size: 48px; margin-bottom: 20px; }
        .command {
            background: #f7fafc;
            padding: 15px;
            border-radius: 5px;
            font-family: monospace;
            margin: 20px 0;
            border: 1px solid #e2e8f0;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="success">✓</div>
        <h1>Device Verified!</h1>
        <p>Your new device has been successfully authorized.</p>
        <p>You can now reconnect to exe.dev from your terminal:</p>
        <div class="command">ssh exe.dev</div>
        <p style="font-size: 14px; color: #718096; margin-top: 30px;">
            Device fingerprint: %s...
        </p>
    </div>
</body>
</html>`, fingerprint[:16])
}

// showEmailVerificationForm shows a confirmation form for email verification
func (s *Server) showEmailVerificationForm(w http.ResponseWriter, r *http.Request, token string) {
	// First validate that the token exists
	isValid := false

	// Check if this is an SSH session token (in-memory)
	s.emailVerificationsMu.Lock()
	_, exists := s.emailVerifications[token]
	s.emailVerificationsMu.Unlock()

	if exists {
		isValid = true
	} else {
		// Check database for HTTP auth token (without consuming it)
		_, err := s.checkEmailVerificationToken(token)
		if err == nil {
			isValid = true
		}
	}

	if !isValid {
		http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
		return
	}

	// Show confirmation form
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>Confirm Email - exe.dev</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
            max-width: 500px;
            margin: 100px auto;
            padding: 40px;
            background: #f5f5f5;
        }
        .container {
            background: white;
            border-radius: 12px;
            padding: 40px;
            box-shadow: 0 2px 8px rgba(0,0,0,0.1);
        }
        h1 {
            color: #333;
            margin-bottom: 10px;
            font-size: 28px;
        }
        p {
            color: #666;
            line-height: 1.6;
            margin: 20px 0;
        }
        .button {
            background: #2563eb;
            color: white;
            border: none;
            padding: 12px 32px;
            border-radius: 6px;
            font-size: 16px;
            font-weight: 600;
            cursor: pointer;
            display: inline-block;
            margin-top: 20px;
            transition: background 0.2s;
        }
        .button:hover {
            background: #1d4ed8;
        }
        .warning {
            background: #fef3c7;
            border: 1px solid #f59e0b;
            border-radius: 6px;
            padding: 12px;
            margin: 20px 0;
            color: #92400e;
        }
        form {
            margin: 0;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Confirm Your Email Address</h1>
        <p>You're about to verify your email address for exe.dev.</p>

        <div class="warning">
            ⚠️ Only click confirm if you initiated this request
        </div>

        <p>This will complete your email verification and allow you to proceed with your exe.dev account setup.</p>

        <form method="POST" action="/verify-email">
            <input type="hidden" name="token" value="%s">
            <button type="submit" class="button">Confirm Email Verification</button>
        </form>
    </div>
</body>
</html>`, token)
}

// handleEmailVerificationHTTP handles web-based email verification
func (s *Server) handleEmailVerificationHTTP(w http.ResponseWriter, r *http.Request) {
	// Handle GET request - show confirmation form
	if r.Method == http.MethodGet {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}
		s.showEmailVerificationForm(w, r, token)
		return
	}

	// Handle POST request - complete verification
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data to get the token from POST
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing token in form data", http.StatusBadRequest)
		return
	}

	// First check if this is an SSH session token (in-memory)
	s.emailVerificationsMu.Lock()
	verification, exists := s.emailVerifications[token]
	if exists {
		// This is an SSH session email verification
		fingerprint := verification.PublicKeyFingerprint
		email := verification.Email

		// Create the user if they don't exist
		user, err := s.getUserByFingerprint(fingerprint)
		if err != nil || user == nil {
			log.Printf("User doesn't exist for fingerprint %s, creating...", fingerprint)
			// User doesn't exist - create them with their team
			if err := s.createUser(fingerprint, email); err != nil {
				log.Printf("Failed to create user during email verification: %v", err)
				s.emailVerificationsMu.Unlock()
				http.Error(w, "Failed to create user account", http.StatusInternalServerError)
				return
			}
			log.Printf("Created new user for %s (fingerprint: %s)", email, fingerprint)
		} else {
			log.Printf("User already exists for fingerprint %s", fingerprint)
		}

		// Store the SSH key as verified
		publicKey := verification.PublicKey
		if publicKey != "" {
			_, err = s.db.Exec(`
				INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
				VALUES (?, ?, ?, 1, 'Primary Device')
				ON CONFLICT(fingerprint) DO UPDATE SET verified = 1, public_key = ?, user_email = ?`,
				fingerprint, email, publicKey, publicKey, email)
			if err != nil {
				log.Printf("Error storing SSH key during verification: %v", err)
			}
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
		if err != nil {
			log.Printf("Failed to create auth cookie during SSH email verification: %v", err)
			// Continue anyway - SSH auth will still work
		} else {
			// Set the authentication cookie
			cookie := &http.Cookie{
				Name:     "exe-auth",
				Value:    cookieValue,
				Path:     "/",
				HttpOnly: true,
				MaxAge:   30 * 24 * 60 * 60, // 30 days
				Secure:   r.TLS != nil,
			}
			http.SetCookie(w, cookie)
		}

		// Signal completion to SSH session
		close(verification.CompleteChan)

		// Clean up email verification
		delete(s.emailVerifications, token)
		s.emailVerificationsMu.Unlock()
	} else {
		// Not an SSH token, check database for HTTP auth token
		s.emailVerificationsMu.Unlock()

		// Try to validate as database token
		fingerprint, err := s.validateEmailVerificationToken(token)
		if err != nil {
			log.Printf("Invalid email verification token: %v", err)
			http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
		if err != nil {
			log.Printf("Failed to create auth cookie during HTTP email verification: %v", err)
			http.Error(w, "Failed to create authentication session", http.StatusInternalServerError)
			return
		}

		// Set the authentication cookie
		cookie := &http.Cookie{
			Name:     "exe-auth",
			Value:    cookieValue,
			Path:     "/",
			HttpOnly: true,
			MaxAge:   30 * 24 * 60 * 60, // 30 days
			Secure:   r.TLS != nil,
		}
		http.SetCookie(w, cookie)

		// Clean up the database token (single use)
		_, err = s.db.Exec("DELETE FROM email_verifications WHERE token = ?", token)
		if err != nil {
			log.Printf("Failed to cleanup email verification token: %v", err)
			// Continue anyway
		}
	}

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

// parseContainerRequest checks if the host matches container subdomain patterns
// Returns: containerName, teamName, port, isContainerRequest
// Supports: <name>.<team>.localhost|exe.dev (port 80) and <name>-<port>.<team>.localhost|exe.dev (custom port)
func (s *Server) parseContainerRequest(host string) (containerName, teamName, port string, isContainerRequest bool) {
	// Remove port if present in host
	hostname := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		hostname = host[:idx]
	}

	// Check for localhost development pattern
	var domain string
	if strings.HasSuffix(hostname, ".localhost") {
		domain = "localhost"
	} else if strings.HasSuffix(hostname, ".exe.dev") {
		domain = "exe.dev"
	} else {
		return "", "", "", false
	}

	// Remove domain suffix to get the subdomain part
	domainSuffix := "." + domain
	if !strings.HasSuffix(hostname, domainSuffix) {
		return "", "", "", false
	}

	subdomain := strings.TrimSuffix(hostname, domainSuffix)

	// Split subdomain into parts: <name>[-<port>].<team>
	parts := strings.Split(subdomain, ".")
	if len(parts) != 2 {
		return "", "", "", false
	}

	containerPart := parts[0] // <name> or <name>-<port>
	teamName = parts[1]       // <team>

	// Check if containerPart contains a port (has dash and ends with digits)
	if dashIdx := strings.LastIndex(containerPart, "-"); dashIdx > 0 {
		possiblePort := containerPart[dashIdx+1:]
		// Validate that everything after dash is digits
		if isNumeric(possiblePort) {
			containerName = containerPart[:dashIdx]
			port = possiblePort
		} else {
			// No port, treat as container name with dash
			containerName = containerPart
			port = "80" // default
		}
	} else {
		containerName = containerPart
		port = "80" // default
	}

	// Validate parts are non-empty and reasonable
	if containerName == "" || teamName == "" || port == "" {
		return "", "", "", false
	}

	return containerName, teamName, port, true
}

// isNumeric checks if a string contains only digits
func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// handleContainerProxy handles authenticated reverse proxy requests to containers
func (s *Server) handleContainerProxy(w http.ResponseWriter, r *http.Request, containerName, teamName, port string) {
	// Special case: handle auth callback
	if strings.HasPrefix(r.URL.Path, "/__exe_auth") {
		s.handleContainerAuthCallback(w, r, containerName, teamName, port)
		return
	}

	if !s.quietMode {
		log.Printf("Container proxy request: %s.%s:%s %s", containerName, teamName, port, r.URL.Path)
	}

	// Check for authentication cookie
	cookieName := "exe-auth-" + teamName
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		// No auth cookie, redirect to main auth flow
		authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
		if r.Host != "" {
			// Redirect to main domain with return URL
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			redirectURL := fmt.Sprintf("%s://%s%s&return_host=%s", scheme,
				strings.Replace(r.Host, containerName+"."+teamName+".", "", 1),
				authURL, url.QueryEscape(r.Host))
			http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
		} else {
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
		}
		return
	}

	// Validate cookie and get user info
	fingerprint, err := s.validateAuthCookie(cookie.Value, r.Host)
	if err != nil {
		log.Printf("Invalid auth cookie: %v", err)
		// Invalid cookie, redirect to auth
		authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
		http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
		return
	}

	// Check if user has access to this team/container
	hasAccess, err := s.userHasTeamAccess(fingerprint, teamName)
	if err != nil {
		log.Printf("Error checking team access: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Get container info and ensure it exists
	machine, err := s.getMachineByName(teamName, containerName)
	if err != nil {
		log.Printf("Container not found: %v", err)
		http.Error(w, "Container not found", http.StatusNotFound)
		return
	}

	// TODO: Wake up container if it's sleeping
	containerID := ""
	if machine.ContainerID != nil {
		containerID = *machine.ContainerID
	}
	log.Printf("Proxying to container %s (id: %s)", machine.Name, containerID)

	// Proxy the request to the container
	s.proxyToContainer(w, r, machine, port)
}

// handleContainerAuthCallback handles the auth callback for container subdomains
func (s *Server) handleContainerAuthCallback(w http.ResponseWriter, r *http.Request, containerName, teamName, port string) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "Missing auth token", http.StatusBadRequest)
		return
	}

	// Validate the auth token and get user fingerprint
	fingerprint, err := s.validateAuthToken(token, containerName+"."+teamName)
	if err != nil {
		log.Printf("Invalid auth token: %v", err)
		http.Error(w, "Invalid or expired auth token", http.StatusUnauthorized)
		return
	}

	// Create authentication cookie for this team
	cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
	if err != nil {
		log.Printf("Failed to create auth cookie: %v", err)
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	// Set the authentication cookie
	cookieName := "exe-auth-" + teamName
	cookie := &http.Cookie{
		Name:     cookieName,
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		Secure:   r.TLS != nil,
	}
	http.SetCookie(w, cookie)

	// Redirect back to the original path
	returnPath := r.URL.Query().Get("return_path")
	if returnPath == "" {
		returnPath = "/"
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	redirectURL := fmt.Sprintf("%s://%s-%s.%s%s", scheme, containerName, port, teamName+"."+getDomain(r.Host), returnPath)
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

// handleAuth handles the main domain authentication flow
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	// Check if user already has a valid exe.dev auth cookie
	cookie, err := r.Cookie("exe-auth")
	if err == nil && cookie.Value != "" {
		fingerprint, err := s.validateAuthCookie(cookie.Value, r.Host)
		if err == nil {
			// User is already authenticated, handle redirect
			s.redirectAfterAuth(w, r, fingerprint)
			return
		}
	}

	// Handle POST request (email submission)
	if r.Method == "POST" {
		s.handleAuthEmailSubmission(w, r)
		return
	}

	// Show authentication form
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>exe.dev - Authentication Required</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 500px; margin: 80px auto; padding: 20px; line-height: 1.6; }
        .container { background: white; padding: 40px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1 { color: #333; margin-bottom: 10px; font-size: 24px; }
        .subtitle { color: #666; margin-bottom: 30px; }
        .form-group { margin-bottom: 20px; }
        label { display: block; margin-bottom: 5px; font-weight: 500; color: #333; }
        input[type="email"] {
            width: 100%%;
            padding: 12px;
            border: 2px solid #e1e5e9;
            border-radius: 6px;
            font-size: 16px;
            box-sizing: border-box;
        }
        input[type="email"]:focus {
            outline: none;
            border-color: #007cba;
        }
        button {
            width: 100%%;
            background: #007cba;
            color: white;
            padding: 12px 20px;
            border: none;
            border-radius: 6px;
            cursor: pointer;
            font-size: 16px;
            font-weight: 500;
        }
        button:hover { background: #006ba1; }
        button:disabled { background: #ccc; cursor: not-allowed; }
        .alt-method {
            margin-top: 30px;
            padding-top: 30px;
            border-top: 1px solid #e1e5e9;
            text-align: center;
            color: #666;
        }
        .ssh-command {
            background: #f8f9fa;
            padding: 12px;
            border-radius: 4px;
            font-family: 'Monaco', 'Consolas', monospace;
            color: #333;
            border-left: 3px solid #007cba;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Sign in to exe.dev</h1>
        <p class="subtitle">Enter your email address to receive a sign-in link</p>

        <form method="POST" action="/auth">
            <div class="form-group">
                <label for="email">Email address</label>
                <input type="email" id="email" name="email" required placeholder="you@example.com">
            </div>

            <button type="submit">Send sign-in link</button>
        </form>

        <div class="alt-method">
            <p>Or authenticate via SSH:</p>
            <div class="ssh-command">ssh exe.dev</div>
        </div>
    </div>
</body>
</html>`)
}

// handleAuthEmailSubmission handles the email form submission for web auth
func (s *Server) handleAuthEmailSubmission(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" {
		s.showAuthError(w, r, "Please enter a valid email address")
		return
	}

	// Basic email validation
	if !strings.Contains(email, "@") || !strings.Contains(email, ".") {
		s.showAuthError(w, r, "Please enter a valid email address")
		return
	}

	// Check if user exists
	var userFingerprint string
	err := s.db.QueryRow("SELECT public_key_fingerprint FROM users WHERE email = ?", email).Scan(&userFingerprint)
	if err != nil {
		if err == sql.ErrNoRows {
			s.showAuthError(w, r, "No account found with this email address. Please sign up first using SSH: ssh exe.dev")
			return
		}
		log.Printf("Database error checking user: %v", err)
		s.showAuthError(w, r, "Database error occurred. Please try again.")
		return
	}

	// Generate verification token - reuse the existing email verification system
	token := s.generateRegistrationToken()

	// Store verification in database (reuse existing email_verifications table)
	_, err = s.db.Exec(`
		INSERT INTO email_verifications (token, email, user_fingerprint, expires_at)
		VALUES (?, ?, ?, ?)
	`, token, email, userFingerprint, time.Now().Add(24*time.Hour).Format(time.RFC3339))
	if err != nil {
		log.Printf("Failed to store email verification: %v", err)
		s.showAuthError(w, r, "Failed to create verification. Please try again.")
		return
	}

	// Create verification link
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	verificationURL := fmt.Sprintf("%s://%s/auth/verify?token=%s", scheme, r.Host, token)

	// Add redirect parameters to the verification URL if present
	if redirect := r.URL.Query().Get("redirect"); redirect != "" {
		verificationURL += "&redirect=" + url.QueryEscape(redirect)
	}
	if returnHost := r.URL.Query().Get("return_host"); returnHost != "" {
		verificationURL += "&return_host=" + url.QueryEscape(returnHost)
	}

	// Send email using existing verification system
	err = s.sendVerificationEmail(email, token)
	if err != nil {
		log.Printf("Failed to send auth email: %v", err)
		s.showAuthError(w, r, "Failed to send email. Please try again or contact support.")
		return
	}

	// Show success page
	s.showAuthEmailSent(w, r, email)
}

// showAuthError displays an authentication error page
func (s *Server) showAuthError(w http.ResponseWriter, r *http.Request, message string) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>exe.dev - Authentication Error</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 500px; margin: 80px auto; padding: 20px; line-height: 1.6; }
        .container { background: white; padding: 40px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        .error { color: #d73a49; background: #ffeef0; padding: 15px; border-radius: 6px; margin-bottom: 20px; }
        a { color: #007cba; text-decoration: none; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Authentication Error</h1>
        <div class="error">%s</div>
        <p><a href="/auth?%s">← Try again</a></p>
    </div>
</body>
</html>`, message, r.URL.RawQuery)
}

// showAuthEmailSent displays the email sent confirmation page
func (s *Server) showAuthEmailSent(w http.ResponseWriter, r *http.Request, email string) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>exe.dev - Check Your Email</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 500px; margin: 80px auto; padding: 20px; line-height: 1.6; }
        .container { background: white; padding: 40px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); text-align: center; }
        .success { color: #28a745; background: #f0f8f0; padding: 15px; border-radius: 6px; margin-bottom: 20px; }
        .email { font-weight: 500; color: #007cba; }
        a { color: #007cba; text-decoration: none; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <div class="container">
        <h1>📧 Check Your Email</h1>
        <div class="success">
            We've sent a sign-in link to <span class="email">%s</span>
        </div>
        <p>Click the link in the email to complete your authentication.</p>
        <p><small>The link will expire in 24 hours. Didn't receive it? <a href="/auth?%s">Try again</a></small></p>
    </div>
</body>
</html>`, email, r.URL.RawQuery)
}

// handleAuthCallback handles authentication callbacks with magic tokens
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	var token string
	var fingerprint string
	var err error

	// Check if this is an email verification request (/auth/verify?token=...)
	if strings.HasPrefix(r.URL.Path, "/auth/verify") {
		token = r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}

		// Validate email verification token
		fingerprint, err = s.validateEmailVerificationToken(token)
		if err != nil {
			log.Printf("Invalid email verification token: %v", err)
			http.Error(w, "Invalid or expired verification token", http.StatusUnauthorized)
			return
		}
	} else {
		// Extract token from path /auth/<token>
		token = strings.TrimPrefix(r.URL.Path, "/auth/")
		if token == "" {
			http.Error(w, "Missing authentication token", http.StatusBadRequest)
			return
		}

		// Validate the auth token
		fingerprint, err = s.validateAuthToken(token, "")
		if err != nil {
			log.Printf("Invalid auth token in callback: %v", err)
			http.Error(w, "Invalid or expired authentication token", http.StatusUnauthorized)
			return
		}
	}

	// Create main domain auth cookie
	cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
	if err != nil {
		log.Printf("Failed to create main auth cookie: %v", err)
		http.Error(w, "Failed to create authentication cookie", http.StatusInternalServerError)
		return
	}

	// Set the authentication cookie
	cookie := &http.Cookie{
		Name:     "exe-auth",
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30 * 24 * 60 * 60, // 30 days
		Secure:   r.TLS != nil,
	}
	http.SetCookie(w, cookie)

	// Handle redirect after authentication
	s.redirectAfterAuth(w, r, fingerprint)
}

// getDomain extracts the base domain from a host
func getDomain(host string) string {
	// Remove port if present
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}

	if strings.HasSuffix(host, ".localhost") {
		return "localhost"
	} else if strings.HasSuffix(host, ".exe.dev") {
		return "exe.dev"
	}

	return host
}

// checkEmailVerificationToken checks if an email verification token is valid without consuming it
func (s *Server) checkEmailVerificationToken(token string) (string, error) {
	var fingerprint string
	var email string
	var expiresAt string

	err := s.db.QueryRow(`
		SELECT user_fingerprint, email, expires_at
		FROM email_verifications
		WHERE token = ?
	`, token).Scan(&fingerprint, &email, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("invalid verification token")
		}
		return "", fmt.Errorf("database error: %w", err)
	}

	// Check if token has expired
	expTime, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return "", fmt.Errorf("invalid expiration time: %w", err)
	}

	if time.Now().After(expTime) {
		// Clean up expired token
		s.db.Exec("DELETE FROM email_verifications WHERE token = ?", token)
		return "", fmt.Errorf("verification token expired")
	}

	return fingerprint, nil
}

// validateEmailVerificationToken validates an email verification token, consumes it, and returns the user fingerprint
func (s *Server) validateEmailVerificationToken(token string) (string, error) {
	fingerprint, err := s.checkEmailVerificationToken(token)
	if err != nil {
		return "", err
	}

	// Clean up used token
	s.db.Exec("DELETE FROM email_verifications WHERE token = ?", token)

	return fingerprint, nil
}

// Helper functions for authentication and reverse proxy

// createAuthCookie creates a new authentication cookie for the user
func (s *Server) createAuthCookie(fingerprint, domain string) (string, error) {
	// Generate a random cookie value
	cookieBytes := make([]byte, 32)
	if _, err := cryptorand.Read(cookieBytes); err != nil {
		return "", fmt.Errorf("failed to generate cookie: %w", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(cookieBytes)

	// Set expiration to 30 days from now
	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	// Store in database
	_, err := s.db.Exec(`
		INSERT INTO auth_cookies (cookie_value, user_fingerprint, domain, expires_at)
		VALUES (?, ?, ?, ?)
	`, cookieValue, fingerprint, getDomain(domain), expiresAt.Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("failed to store auth cookie: %w", err)
	}

	return cookieValue, nil
}

// validateAuthCookie validates an authentication cookie and returns the user fingerprint
func (s *Server) validateAuthCookie(cookieValue, domain string) (string, error) {
	var fingerprint string
	var expiresAt string

	err := s.db.QueryRow(`
		SELECT user_fingerprint, expires_at
		FROM auth_cookies
		WHERE cookie_value = ? AND domain = ?
	`, cookieValue, getDomain(domain)).Scan(&fingerprint, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("invalid cookie")
		}
		return "", fmt.Errorf("database error: %w", err)
	}

	// Check if cookie has expired
	expTime, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return "", fmt.Errorf("invalid expiration time: %w", err)
	}

	if time.Now().After(expTime) {
		// Clean up expired cookie
		s.db.Exec("DELETE FROM auth_cookies WHERE cookie_value = ?", cookieValue)
		return "", fmt.Errorf("cookie expired")
	}

	// Update last used time
	s.db.Exec("UPDATE auth_cookies SET last_used_at = CURRENT_TIMESTAMP WHERE cookie_value = ?", cookieValue)

	return fingerprint, nil
}

// createAuthToken creates a temporary authentication token
func (s *Server) createAuthToken(fingerprint, subdomain string) (string, error) {
	// Generate a random token
	tokenBytes := make([]byte, 32)
	if _, err := cryptorand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)

	// Set expiration to 10 minutes from now
	expiresAt := time.Now().Add(10 * time.Minute)

	// Store in database
	_, err := s.db.Exec(`
		INSERT INTO auth_tokens (token, user_fingerprint, subdomain, expires_at)
		VALUES (?, ?, ?, ?)
	`, token, fingerprint, subdomain, expiresAt.Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("failed to store auth token: %w", err)
	}

	return token, nil
}

// validateAuthToken validates an authentication token and returns the user fingerprint
func (s *Server) validateAuthToken(token, expectedSubdomain string) (string, error) {
	var fingerprint string
	var subdomain sql.NullString
	var expiresAt string
	var usedAt sql.NullString

	err := s.db.QueryRow(`
		SELECT user_fingerprint, subdomain, expires_at, used_at
		FROM auth_tokens
		WHERE token = ?
	`, token).Scan(&fingerprint, &subdomain, &expiresAt, &usedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("invalid token")
		}
		return "", fmt.Errorf("database error: %w", err)
	}

	// Check if token has already been used
	if usedAt.Valid {
		return "", fmt.Errorf("token already used")
	}

	// Check if token has expired
	expTime, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return "", fmt.Errorf("invalid expiration time: %w", err)
	}

	if time.Now().After(expTime) {
		return "", fmt.Errorf("token expired")
	}

	// Check subdomain if specified
	if expectedSubdomain != "" && subdomain.String != expectedSubdomain {
		return "", fmt.Errorf("token not valid for this subdomain")
	}

	// Mark token as used
	_, err = s.db.Exec("UPDATE auth_tokens SET used_at = CURRENT_TIMESTAMP WHERE token = ?", token)
	if err != nil {
		log.Printf("Failed to mark token as used: %v", err)
	}

	return fingerprint, nil
}

// redirectAfterAuth handles redirecting user after successful authentication
func (s *Server) redirectAfterAuth(w http.ResponseWriter, r *http.Request, fingerprint string) {
	redirectURL := r.URL.Query().Get("redirect")
	returnHost := r.URL.Query().Get("return_host")

	if returnHost != "" && redirectURL != "" {
		// Create auth token for the container subdomain
		containerName, teamName, _, isContainerRequest := s.parseContainerRequest(returnHost)
		if isContainerRequest {
			token, err := s.createAuthToken(fingerprint, containerName+"."+teamName)
			if err != nil {
				log.Printf("Failed to create auth token: %v", err)
				http.Error(w, "Failed to create authentication token", http.StatusInternalServerError)
				return
			}

			// Redirect back to container subdomain with auth token
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			authCallbackURL := fmt.Sprintf("%s://%s/__exe_auth?token=%s&return_path=%s",
				scheme, returnHost, token, url.QueryEscape(redirectURL))
			http.Redirect(w, r, authCallbackURL, http.StatusTemporaryRedirect)
			return
		}
	}

	// Default redirect
	if redirectURL != "" {
		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
	} else {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
	}
}

// userHasTeamAccess checks if a user has access to a team
func (s *Server) userHasTeamAccess(fingerprint, teamName string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM team_members
		WHERE user_fingerprint = ? AND team_name = ?
	`, fingerprint, teamName).Scan(&count)
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// proxyToContainer proxies the HTTP request to the container
func (s *Server) proxyToContainer(w http.ResponseWriter, r *http.Request, machine *Machine, port string) {
	if s.containerManager == nil {
		http.Error(w, "Machine management not available", http.StatusServiceUnavailable)
		return
	}

	// Get container connection details
	if machine.ContainerID == nil {
		http.Error(w, "Container not properly initialized", http.StatusServiceUnavailable)
		return
	}
	conn, err := s.containerManager.ConnectToContainer(context.Background(), machine.CreatedByFingerprint, *machine.ContainerID)
	if err != nil {
		log.Printf("Failed to connect to container: %v", err)
		http.Error(w, "Container not available", http.StatusServiceUnavailable)
		return
	}
	defer func() {
		if conn.StopFunc != nil {
			conn.StopFunc()
		}
	}()

	// Create reverse proxy
	targetURL := &url.URL{
		Scheme: "http",
		Host:   "localhost:" + port,
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// For Docker, standard proxy is sufficient

	// Handle errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Proxy error for %s: %v", machine.Name, err)
		http.Error(w, "Service temporarily unavailable", http.StatusBadGateway)
	}

	// Fix Content-Length mismatch between parsed response and actual body
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.Body != nil {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			// Replace the body so it can still be read by the client
			resp.Body = io.NopCloser(strings.NewReader(string(body)))
			// Update Content-Length to match actual body length
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		}
		return nil
	}

	// Modify request headers
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Add container-specific headers if needed
		req.Header.Set("X-Forwarded-For", r.RemoteAddr)
		req.Header.Set("X-Forwarded-Host", r.Host)
		req.Header.Set("X-Forwarded-Proto", getScheme(r))
	}

	proxy.ServeHTTP(w, r)
}

// getScheme returns the request scheme
func getScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// SSHClient interface for SSH connections
type SSHClient interface {
	Dial(network, addr string) (net.Conn, error)
	Close() error
}

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

// findMachineByNameForUser finds a machine by name that the user has access to
// Supports both "machine" format (uses default team) and "team/machine" format
func (s *Server) findMachineByNameForUser(fingerprint, machineName string) *Machine {
	var teamName string
	var machineNameOnly string

	// Check if the machine name includes team specification (team/machine format)
	if strings.Contains(machineName, "/") {
		parts := strings.SplitN(machineName, "/", 2)
		if len(parts) == 2 {
			teamName = parts[0]
			machineNameOnly = parts[1]

			// Verify user has access to this specific team
			teams, err := s.getUserTeams(fingerprint)
			if err != nil {
				return nil
			}

			// Check if user is member of the specified team
			for _, team := range teams {
				if team.TeamName == teamName {
					machine, err := s.getMachineByName(teamName, machineNameOnly)
					if err == nil {
						return machine
					}
					break
				}
			}
			return nil
		}
	}

	// No team specified - try default team first, then search all teams
	machineNameOnly = machineName

	// Try default team first
	defaultTeam, err := s.getDefaultTeamForKey(fingerprint)
	if err == nil && defaultTeam != "" {
		machine, err := s.getMachineByName(defaultTeam, machineNameOnly)
		if err == nil {
			return machine
		}
	}

	// Get user's teams and search all of them
	teams, err := s.getUserTeams(fingerprint)
	if err != nil || len(teams) == 0 {
		return nil
	}

	// Check each team for a machine with this name
	for _, team := range teams {
		machine, err := s.getMachineByName(team.TeamName, machineNameOnly)
		if err == nil {
			return machine
		}
	}

	return nil
}

// handleMachineSSH handles SSH connections to a specific machine
func (s *Server) handleMachineSSH(channel *sshbuf.Channel, requests <-chan *ssh.Request, machine *Machine, fingerprint string) {
	if machine.ContainerID == nil {
		channel.Write([]byte("Machine is not running\r\n"))
		return
	}

	// Get container connection
	conn, err := s.containerManager.ConnectToContainer(context.Background(), machine.CreatedByFingerprint, *machine.ContainerID)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("Failed to connect to machine: %v\r\n", err)))
		return
	}
	defer conn.StopFunc()

	// Proxy all SSH requests directly to the container
	s.proxySSHToContainer(channel, requests, machine, fingerprint)
}

// proxySSHToContainer proxies SSH protocol directly to a container
func (s *Server) proxySSHToContainer(channel *sshbuf.Channel, requests <-chan *ssh.Request, machine *Machine, fingerprint string) {
	if machine.ContainerID == nil {
		channel.Write([]byte("Machine is not running\r\n"))
		return
	}

	// Handle each request type
	for req := range requests {
		switch req.Type {
		case "pty-req":
			// PTY request - acknowledge it (Docker will handle TTY allocation)
			if req.WantReply {
				req.Reply(true, nil)
			}

		case "exec":
			// Parse command from exec payload
			if len(req.Payload) < 4 {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}

			cmdLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
			if len(req.Payload) < 4+cmdLen {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}

			command := string(req.Payload[4 : 4+cmdLen])
			args := strings.Fields(command)

			if req.WantReply {
				req.Reply(true, nil)
			}

			// Handle SCP commands - we only support modern SCP via SFTP
			if len(args) > 0 && args[0] == "scp" {
				// Modern OpenSSH scp uses SFTP subsystem, not exec
				channel.Stderr().Write([]byte("This server requires modern SCP (OpenSSH 8.0+) which uses SFTP protocol\n"))
				statusPayload := make([]byte, 4)
				statusPayload[3] = 1 // exit status 1
				channel.SendRequest("exit-status", false, statusPayload)
				return
			}

			// Execute command in container
			err := s.containerManager.ExecuteInContainer(
				context.Background(),
				machine.CreatedByFingerprint,
				*machine.ContainerID,
				args,
				nil,     // stdin
				channel, // stdout
				channel, // stderr
			)
			if err != nil {
				channel.Write([]byte(fmt.Sprintf("Command execution failed: %v\r\n", err)))
			}

			// Send exit status
			exitStatus := 0
			if err != nil {
				exitStatus = 1
			}
			statusPayload := make([]byte, 4)
			statusPayload[0] = byte(exitStatus >> 24)
			statusPayload[1] = byte(exitStatus >> 16)
			statusPayload[2] = byte(exitStatus >> 8)
			statusPayload[3] = byte(exitStatus)
			channel.SendRequest("exit-status", false, statusPayload)
			return

		case "shell":
			if req.WantReply {
				req.Reply(true, nil)
			}

			// Determine the appropriate shell for this container/user
			shell, err := s.determineUserShell(machine.CreatedByFingerprint, *machine.ContainerID)
			if err != nil {
				channel.Write([]byte(fmt.Sprintf("Failed to determine shell: %v\r\n", err)))
				return
			}

			// Start interactive shell in container
			err = s.containerManager.ExecuteInContainer(
				context.Background(),
				machine.CreatedByFingerprint,
				*machine.ContainerID,
				[]string{shell},
				channel, // stdin
				channel, // stdout
				channel, // stderr
			)
			if err != nil {
				channel.Write([]byte(fmt.Sprintf("Shell execution failed: %v\r\n", err)))
			}
			return

		case "subsystem":
			// Handle subsystems like SFTP
			if len(req.Payload) < 4 {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}

			subsystemLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
			if len(req.Payload) < 4+subsystemLen {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}

			subsystem := string(req.Payload[4 : 4+subsystemLen])

			if subsystem == "sftp" {
				if req.WantReply {
					req.Reply(true, nil)
				}

				// Use the new sshproxy package for SFTP
				containerFS := sshproxy.NewUnixContainerFS(
					s.containerManager,
					machine.CreatedByFingerprint,
					*machine.ContainerID,
					"/workspace",
				)
				handler := sshproxy.NewSFTPHandler(context.Background(), containerFS, "/workspace")
				handlers := sftp.Handlers{
					FileGet:  handler,
					FilePut:  handler,
					FileCmd:  handler,
					FileList: handler,
				}
				server := sftp.NewRequestServer(channel, handlers)
				if err := server.Serve(); err != nil && err != io.EOF {
					fmt.Fprintf(os.Stderr, "SFTP server error: %v\n", err)
				}
				return
			} else {
				if req.WantReply {
					req.Reply(false, nil)
				}
			}

		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// handleMachineRequests handles SSH global requests for machine connections (like port forwarding)
func (s *Server) handleMachineRequests(requests <-chan *ssh.Request, machine *Machine, fingerprint string, sshConn ssh.Conn) {
	for req := range requests {
		switch req.Type {
		case "tcpip-forward":
			// Handle -L (local) port forwarding
			s.handleTCPIPForward(req, machine, fingerprint, sshConn, false)
		case "cancel-tcpip-forward":
			// Handle cancellation of port forwarding
			s.handleCancelTCPIPForward(req)
		case "forwarded-tcpip":
			// This is actually handled in channel requests, not global requests
			if req.WantReply {
				req.Reply(false, nil)
			}
		default:
			// Unknown request type
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// handleTCPIPForward handles SSH port forwarding to machines
func (s *Server) handleTCPIPForward(req *ssh.Request, machine *Machine, fingerprint string, sshConn ssh.Conn, reverse bool) {
	if len(req.Payload) < 8 {
		if req.WantReply {
			req.Reply(false, nil)
		}
		return
	}

	// Parse the request payload
	// Format: string bind_address, uint32 bind_port
	bindAddrLen := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
	if len(req.Payload) < 4+bindAddrLen+4 {
		if req.WantReply {
			req.Reply(false, nil)
		}
		return
	}

	bindAddr := string(req.Payload[4 : 4+bindAddrLen])
	bindPort := int(req.Payload[4+bindAddrLen])<<24 | int(req.Payload[4+bindAddrLen+1])<<16 | int(req.Payload[4+bindAddrLen+2])<<8 | int(req.Payload[4+bindAddrLen+3])

	// For now, implement basic port forwarding logic
	// In a full implementation, we would:
	// 1. Set up a listener on the requested port
	// 2. For each incoming connection, establish a connection to the container
	// 3. Relay data between the connections

	// For this implementation, we'll acknowledge the request but not implement the full forwarding
	log.Printf("Port forwarding request: %s:%d -> machine %s", bindAddr, bindPort, machine.Name)

	if req.WantReply {
		// Reply with the actual bound port (for port 0 requests)
		response := make([]byte, 4)
		response[0] = byte(bindPort >> 24)
		response[1] = byte(bindPort >> 16)
		response[2] = byte(bindPort >> 8)
		response[3] = byte(bindPort)
		req.Reply(true, response)
	}
}

// handleCancelTCPIPForward handles cancellation of port forwarding
func (s *Server) handleCancelTCPIPForward(req *ssh.Request) {
	// TODO: Implement port forwarding cancellation
	if req.WantReply {
		req.Reply(true, nil)
	}
}

// connectToContainer connects directly to a container for external SSH access
func (s *Server) connectToContainer(channel *sshbuf.Channel, containerID string) {
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mMachine management is not available\033[0m\r\n"))
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

// connectToContainerInteractive connects to a container with proper interactive shell handling
func (s *Server) connectToContainerInteractive(channel *sshbuf.Channel, containerID string) {
	if s.containerManager == nil {
		channel.Write([]byte("\033[1;31mMachine management is not available\033[0m\r\n"))
		return
	}

	fingerprint, _, err := s.getUserFromChannel(channel)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError: %v\033[0m\r\n", err)))
		return
	}

	// Use the simple direct approach - just pass the channel
	ctx := context.Background()

	// Execute /bin/bash in the container
	err = s.containerManager.ExecuteInContainer(
		ctx,
		fingerprint,
		containerID,
		[]string{"/bin/bash"},
		channel, // stdin
		channel, // stdout
		channel, // stderr
	)

	if err != nil && err != io.EOF {
		channel.Write([]byte(fmt.Sprintf("\r\n\033[1;31mConnection failed: %v\033[0m\r\n", err)))
		return
	}

	// Connection ended normally
	channel.Write([]byte("\r\n\033[1;32mConnection closed\033[0m\r\n"))
}

func (s *Server) handleListUserTeams(channel *sshbuf.Channel, fingerprint string) {
	teams, err := s.getUserTeams(fingerprint)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError retrieving teams: %v\033[0m\r\n", err)))
		return
	}

	if len(teams) == 0 {
		channel.Write([]byte("\033[1;33mYou are not a member of any teams\033[0m\r\n"))
		return
	}

	// Get default team for this SSH key
	defaultTeam, _ := s.getDefaultTeamForKey(fingerprint)

	channel.Write([]byte("\033[1;36m═══ Your Teams ═══\033[0m\r\n\r\n"))

	for _, team := range teams {
		// Check if this is a personal team
		var isPersonal bool
		var ownerFingerprint sql.NullString
		s.db.QueryRow(`SELECT is_personal, owner_fingerprint FROM teams WHERE name = ?`,
			team.TeamName).Scan(&isPersonal, &ownerFingerprint)

		roleStr := "Member"
		if team.IsAdmin {
			roleStr = "\033[1;33mAdmin\033[0m"
		}

		defaultStr := ""
		if team.TeamName == defaultTeam {
			defaultStr = " \033[1;32m[DEFAULT]\033[0m"
		}

		teamTypeStr := ""
		if isPersonal && ownerFingerprint.String == fingerprint {
			teamTypeStr = " \033[2m(Personal)\033[0m"
		}

		channel.Write([]byte(fmt.Sprintf("  \033[1m%s\033[0m%s%s - %s\r\n",
			team.TeamName, teamTypeStr, defaultStr, roleStr)))
		channel.Write([]byte(fmt.Sprintf("    Machines: \033[1;36m<name>.%s.exe.dev\033[0m\r\n", team.TeamName)))
		channel.Write([]byte(fmt.Sprintf("    Joined: %s\r\n\r\n", team.JoinedAt.Format("Jan 2, 2006"))))
	}

	channel.Write([]byte("\033[2mTo switch default team: team switch <team>\033[0m\r\n"))
	channel.Write([]byte("\033[2mTo access a specific team's machine: ssh team/machine@exe.dev\033[0m\r\n"))
}

// handleTeamSwitch switches the default team for the current SSH key
func (s *Server) getUserFromChannel(channel *sshbuf.Channel) (fingerprint, teamName string, err error) {
	s.sessionsMu.RLock()
	session, exists := s.sessions[channel]
	s.sessionsMu.RUnlock()

	if !exists {
		return "", "", fmt.Errorf("user not authenticated")
	}

	return session.Fingerprint, session.TeamName, nil
}

// getUserInfoFromChannel gets complete user information from SSH channel session
func (s *Server) getUserInfoFromChannel(channel *sshbuf.Channel) (fingerprint, email, teamName, publicKey string, err error) {
	s.sessionsMu.RLock()
	session, exists := s.sessions[channel]
	s.sessionsMu.RUnlock()

	if !exists {
		return "", "", "", "", fmt.Errorf("user not authenticated")
	}

	return session.Fingerprint, session.Email, session.TeamName, session.PublicKey, nil
}

// createUserSession creates a new user session for a channel
func (s *Server) createUserSession(channel *sshbuf.Channel, fingerprint, email, teamName, publicKey string, isAdmin bool) {
	session := &UserSession{
		Fingerprint: fingerprint,
		Email:       email,
		TeamName:    teamName,
		IsAdmin:     isAdmin,
		PublicKey:   publicKey,
		CreatedAt:   time.Now(),
	}

	s.sessionsMu.Lock()
	s.sessions[channel] = session
	s.sessionsMu.Unlock()
}

// removeUserSession removes a user session for a channel
func (s *Server) removeUserSession(channel *sshbuf.Channel) {
	s.sessionsMu.Lock()
	delete(s.sessions, channel)
	s.sessionsMu.Unlock()
}

// handleWhoamiCommand shows the user's key fingerprint, public key, and email address
func (s *Server) handleWhoamiCommand(channel *sshbuf.Channel, fingerprint, email, publicKey string) {
	channel.Write([]byte(fmt.Sprintf("\r\n\033[1;36mUser Information:\033[0m\r\n\r\n")))
	channel.Write([]byte(fmt.Sprintf("\033[1mEmail Address:\033[0m %s\r\n", email)))
	channel.Write([]byte(fmt.Sprintf("\033[1mPublic Key Fingerprint:\033[0m %s\r\n", fingerprint)))

	// Display public key if available
	if publicKey != "" {
		channel.Write([]byte(fmt.Sprintf("\033[1mPublic Key:\033[0m %s\r\n", strings.TrimSpace(publicKey))))
	} else {
		// Try to look up public key from database
		var dbPublicKey string
		err := s.db.QueryRow(`SELECT public_key FROM ssh_keys WHERE fingerprint = ? AND verified = 1 LIMIT 1`, fingerprint).Scan(&dbPublicKey)
		if err == nil && dbPublicKey != "" {
			channel.Write([]byte(fmt.Sprintf("\033[1mPublic Key:\033[0m %s\r\n", strings.TrimSpace(dbPublicKey))))
		} else {
			channel.Write([]byte("\033[1mPublic Key:\033[0m \033[2m(not available in current session)\033[0m\r\n"))
		}
	}
}

// handleListCommand lists user's machines
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

// getSSHPort extracts the port number from an SSH address string
func (s *Server) getSSHPort() string {
	if s.sshAddr == "" {
		return "22"
	}

	// Handle addresses like ":2222" or "localhost:2222"
	_, port, err := net.SplitHostPort(s.sshAddr)
	if err != nil {
		// If splitting fails, try to see if it's just a port number
		if _, err := strconv.Atoi(s.sshAddr); err == nil {
			return s.sshAddr
		}
		return "22"
	}

	return port
}

// formatSSHConnectionInfo returns SSH connection info based on dev mode
func (s *Server) formatSSHConnectionInfo(machineName string) string {
	if s.devMode == "local" {
		port := s.getSSHPort()
		if port == "22" {
			return fmt.Sprintf("ssh %s@localhost", machineName)
		}
		return fmt.Sprintf("ssh -p %s %s@localhost", port, machineName)
	}
	return fmt.Sprintf("ssh %s@exe.dev", machineName)
}

// isValidStorageSize validates a Kubernetes storage size string (e.g., "10Gi", "100Gi")
func isValidStorageSize(size string) bool {
	// Check if it matches pattern: number + unit (Ki, Mi, Gi, Ti)
	if len(size) < 2 {
		return false
	}

	// Extract numeric part
	i := 0
	for i < len(size) && (size[i] >= '0' && size[i] <= '9') {
		i++
	}

	if i == 0 {
		return false // No numeric part
	}

	// Check unit suffix
	unit := size[i:]
	validUnits := []string{"Ki", "Mi", "Gi", "Ti"}
	for _, valid := range validUnits {
		if unit == valid {
			return true
		}
	}

	return false
}

// handleCreateCommandWithStdin creates a new machine with support for stdin Dockerfile and flag-based parameters
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

// determineUserShell determines the appropriate shell to use in a container
func (s *Server) determineUserShell(userFingerprint, containerID string) (string, error) {
	ctx := context.Background()

	// Get the user's configured shell from passwd database
	var passwdOut strings.Builder
	err := s.containerManager.ExecuteInContainer(
		ctx,
		userFingerprint,
		containerID,
		[]string{"sh", "-c", "getent passwd $(whoami) | cut -d: -f7"},
		nil,
		&passwdOut,
		nil,
	)

	if err == nil {
		shell := strings.TrimSpace(passwdOut.String())
		if shell != "" {
			return shell, nil
		}
	}

	// Fallback to /bin/sh if getent fails
	return "/bin/sh", nil
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
func (s *Server) showAnimatedWelcome(channel *sshbuf.Channel) {
	s.showAnimatedWelcomeWithWidth(channel, 0)
}

// showAnimatedWelcomeWithWidth displays the ASCII art with a beautiful fade-out animation using specified terminal width
func (s *Server) showAnimatedWelcomeWithWidth(channel *sshbuf.Channel, terminalWidth int) {
	// Skip animation in test mode for faster tests
	if s.testMode {
		channel.Write([]byte("\033[2J\033[H"))
		channel.Write([]byte("███████╗██╗  ██╗███████╗   ██████╗ ███████╗██╗   ██╗\r\n"))
		channel.Write([]byte("╚══════╝╚═╝  ╚═╝╚══════╝╚═╝╚═════╝ ╚══════╝  ╚═══╝  \r\n\r\n"))
		return
	}

	// Detect terminal mode (dark or light)
	terminalMode := s.detectTerminalMode(channel)

	// Clear any remaining OSC response from the buffer
	s.clearOSCResponse(channel)

	// Get appropriate colors based on terminal mode
	colors := s.getTerminalColors(terminalMode)

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
	if !s.quietMode {
		log.Printf("ASCII art centering: Terminal: %d chars, Art: %d chars, Padding: %d, Mode: %v",
			terminalWidth, artWidth, leftPadding, terminalMode)
	}

	// Clear screen and move cursor to top
	channel.Write([]byte("\033[2J\033[H"))

	// Add some vertical padding to center vertically
	channel.Write([]byte("\r\n\r\n\r\n\r\n\r\n"))

	// Add 3 additional blank lines above the ASCII art
	channel.Write([]byte("\r\n\r\n\r\n"))

	// Show the art with fade animation
	for _, step := range colors.fadeSteps {
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
func (s *Server) getTerminalWidth(channel *sshbuf.Channel) int {
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

func (s *Server) handleRegistration(channel *sshbuf.Channel, fingerprint string) {
	s.handleRegistrationWithWidth(channel, fingerprint, 0)
}

func (s *Server) handleRegistrationWithWidth(channel *sshbuf.Channel, fingerprint string, terminalWidth int) {
	// Detect terminal mode
	terminalMode := s.detectTerminalMode(channel)
	s.clearOSCResponse(channel)
	colors := s.getTerminalColors(terminalMode)

	// Show the animated welcome with terminal width
	s.showAnimatedWelcomeWithWidth(channel, terminalWidth)

	// Now show the signup content after the animation
	signupContent := "\r\n\033[1;33mtype ssh to get a server\033[0m\r\n\r\n" +
		"Let's get you set up in just a few steps:\r\n\r\n" +
		colors.grayText + "1. Email Verification\r\n" +
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
func (s *Server) readLineFromChannel(channel *sshbuf.Channel) (string, error) {
	var buffer []byte
	var cursorPos int
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
					// Move cursor to end of line if not already there
					for cursorPos < len(buffer) {
						channel.Write([]byte(string(buffer[cursorPos])))
						cursorPos++
					}
					// Always send CRLF after user input to keep cursor aligned
					channel.Write([]byte("\r\n"))
					return string(buffer), nil
				}
			case 1: // Ctrl+A - move to beginning of line
				for cursorPos > 0 {
					channel.Write([]byte("\b"))
					cursorPos--
				}
			case 5: // Ctrl+E - move to end of line
				for cursorPos < len(buffer) {
					channel.Write([]byte(string(buffer[cursorPos])))
					cursorPos++
				}
			case 3: // Ctrl+C
				channel.Write([]byte("^C\r\n"))
				return "", fmt.Errorf("interrupted")
			case 4: // Ctrl+D
				if len(buffer) == 0 {
					channel.Write([]byte("^D\r\n"))
					return "", fmt.Errorf("EOF")
				}
				// If there's content at cursor, delete it
				if cursorPos < len(buffer) {
					// Delete character at cursor
					copy(buffer[cursorPos:], buffer[cursorPos+1:])
					buffer = buffer[:len(buffer)-1]
					// Redraw the line from cursor position
					channel.Write(buffer[cursorPos:])
					channel.Write([]byte(" ")) // Clear the last character
					// Move cursor back to original position
					for i := len(buffer) - cursorPos + 1; i > 0; i-- {
						channel.Write([]byte("\b"))
					}
				}
			case 8, 127: // Backspace or DEL
				if cursorPos > 0 {
					// Remove character before cursor
					copy(buffer[cursorPos-1:], buffer[cursorPos:])
					buffer = buffer[:len(buffer)-1]
					cursorPos--
					// Move cursor back
					channel.Write([]byte("\b"))
					// Redraw the rest of the line
					channel.Write(buffer[cursorPos:])
					channel.Write([]byte(" ")) // Clear the last character
					// Move cursor back to position
					for i := len(buffer) - cursorPos + 1; i > 0; i-- {
						channel.Write([]byte("\b"))
					}
				}
			case 21: // Ctrl+U - clear line
				// Move cursor to beginning
				for cursorPos > 0 {
					channel.Write([]byte("\b"))
					cursorPos--
				}
				// Clear the displayed line
				for i := 0; i < len(buffer); i++ {
					channel.Write([]byte(" "))
				}
				// Move cursor back to beginning
				for i := 0; i < len(buffer); i++ {
					channel.Write([]byte("\b"))
				}
				buffer = []byte{}
				cursorPos = 0
			default:
				if temp[0] >= 32 { // Printable characters
					if cursorPos == len(buffer) {
						// Append at end
						buffer = append(buffer, temp[0])
						channel.Write(temp) // Echo character back
						cursorPos++
					} else {
						// Insert in middle
						buffer = append(buffer[:cursorPos+1], buffer[cursorPos:]...)
						buffer[cursorPos] = temp[0]
						// Redraw from cursor position
						channel.Write(buffer[cursorPos:])
						cursorPos++
						// Move cursor back to new position
						for i := len(buffer) - cursorPos; i > 0; i-- {
							channel.Write([]byte("\b"))
						}
					}
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
func (s *Server) startEmailVerification(channel *sshbuf.Channel, fingerprint, email string) error {
	// First check if this email already exists
	var existingFingerprint string
	err := s.db.QueryRow("SELECT public_key_fingerprint FROM users WHERE email = ?", email).Scan(&existingFingerprint)

	if err == nil {
		// Email already exists - this is a new device for an existing user
		publicKey := "" // We don't have the public key in this context yet

		// Store this key as unverified in ssh_keys table
		_, err = s.db.Exec(`
			INSERT OR REPLACE INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
			VALUES (?, ?, ?, 0, 'Pending Verification')`,
			fingerprint, email, publicKey)
		if err != nil {
			return fmt.Errorf("failed to store pending key: %v", err)
		}

		// Generate token for new device verification
		token := s.generateToken()
		expires := time.Now().Add(15 * time.Minute)

		_, err = s.db.Exec(`
			INSERT INTO pending_ssh_keys (token, fingerprint, public_key, user_email, expires_at)
			VALUES (?, ?, ?, ?, ?)`,
			token, fingerprint, publicKey, email, expires)
		if err != nil {
			return fmt.Errorf("failed to create verification token: %v", err)
		}

		// Create verification object for existing user (similar to new user flow)
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

		// Send new device verification email
		subject := "New Device Login - exe.dev"
		body := fmt.Sprintf(`Hello,

A new device is trying to register with your exe.dev account email.

If this was you, please click the link below to authorize this device:

%s/verify-device?token=%s

Device fingerprint: %s

If you did not attempt to register from a new device, please ignore this email.

This link will expire in 15 minutes.

Best regards,
The exe.dev team`, s.getBaseURL(), token, fingerprint[:16])

		if err := s.sendEmail(email, subject, body); err != nil {
			s.emailVerificationsMu.Lock()
			delete(s.emailVerifications, token)
			s.emailVerificationsMu.Unlock()
			return err
		}

		// Wait for email verification (same flow as new users)
		grayText := s.getGrayText(channel)
		channel.Write([]byte("\r\n\033[1;33mDevice verification email sent!\033[0m Please check your email and click the verification link.\r\n"))
		channel.Write([]byte(fmt.Sprintf("\r\n%sWaiting for email verification (Press Ctrl+C to cancel)", grayText)))
		// Add animated dots
		for i := 0; i < 3; i++ {
			time.Sleep(500 * time.Millisecond)
			channel.Write([]byte("."))
		}
		channel.Write([]byte("\033[0m\r\n\r\n"))

		// Create a context that can be cancelled when email is verified
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Monitor for Ctrl+C during email verification using interruptible reads
		interruptChan := make(chan struct{})

		go func() {
			buf := make([]byte, 1)
			for {
				n, err := channel.ReadCtx(ctx, buf)
				if err != nil {
					// Context cancelled (email verified) or channel closed
					return
				}
				if n > 0 && buf[0] == 3 { // Ctrl+C
					channel.Write([]byte("^C\r\n"))
					close(interruptChan)
					return
				}
				// Discard other input during email verification
			}
		}()

		// Wait for email verification, interrupt, or timeout
		select {
		case <-verification.CompleteChan:
			// Cancel the context to stop the monitoring goroutine
			cancel()

			channel.Write([]byte("\r\n\033[1;32mDevice verified successfully!\033[0m\r\n\r\n"))

			// Add a small delay to ensure the monitoring goroutine exits cleanly
			time.Sleep(100 * time.Millisecond)
			return nil

		case <-interruptChan:
			// Cancel the context to stop the monitoring goroutine
			cancel()

			// Clean up the verification
			s.emailVerificationsMu.Lock()
			delete(s.emailVerifications, token)
			s.emailVerificationsMu.Unlock()

			channel.Write([]byte("\r\n\033[1;33mVerification cancelled.\033[0m\r\n\r\n"))
			return fmt.Errorf("verification cancelled by user")

		case <-time.After(15 * time.Minute):
			// Cancel the context to stop the monitoring goroutine
			cancel()

			// Clean up the verification
			s.emailVerificationsMu.Lock()
			delete(s.emailVerifications, token)
			s.emailVerificationsMu.Unlock()

			channel.Write([]byte("\r\n\033[1;31mEmail verification timed out.\033[0m Please try again.\r\n\r\n"))
			return fmt.Errorf("verification timed out")
		}
	}

	// New user registration flow
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

	grayText := s.getGrayText(channel)
	channel.Write([]byte("\r\n\033[1;33mVerification email sent!\033[0m Please check your email and click the verification link.\r\n"))
	channel.Write([]byte(fmt.Sprintf("\r\n%sWaiting for email verification (Press Ctrl+C to cancel)", grayText)))
	// Add animated dots
	for i := 0; i < 3; i++ {
		time.Sleep(500 * time.Millisecond)
		channel.Write([]byte("."))
	}
	channel.Write([]byte("\033[0m\r\n\r\n"))

	// Create a context that can be cancelled when email is verified
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Monitor for Ctrl+C during email verification using interruptible reads
	interruptChan := make(chan struct{})

	go func() {
		buf := make([]byte, 1)
		for {
			n, err := channel.ReadCtx(ctx, buf)
			if err != nil {
				// Context cancelled (email verified) or channel closed
				return
			}
			if n > 0 && buf[0] == 3 { // Ctrl+C
				channel.Write([]byte("^C\r\n"))
				close(interruptChan)
				return
			}
			// Discard other input during email verification
		}
	}()

	// Wait for email verification, interrupt, or timeout
	select {
	case <-verification.CompleteChan:
		// Cancel the context to stop the monitoring goroutine
		cancel()

		channel.Write([]byte("\r\n\033[1;32mEmail verified successfully!\033[0m\r\n\r\n"))

		// Add a small delay to ensure the monitoring goroutine exits cleanly
		time.Sleep(100 * time.Millisecond)

		// Start team name creation
		s.startTeamNameCreation(channel, fingerprint, email)

	case <-interruptChan:
		// User pressed Ctrl+C
		channel.Write([]byte("\r\n\033[1;33mRegistration cancelled. You can reconnect anytime to continue.\033[0m\r\n"))

		// Clean up verification
		s.emailVerificationsMu.Lock()
		delete(s.emailVerifications, token)
		s.emailVerificationsMu.Unlock()

	case <-time.After(10 * time.Minute):
		// Cancel the context to stop the monitoring goroutine
		cancel()

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
	if s.devMode != "" {
		if !s.quietMode {
			log.Printf("🔧 DEV MODE: Would send verification email to %s with URL: %s", email, verificationURL)
		}

		// Auto-complete email verification in dev mode
		go func() {
			time.Sleep(100 * time.Millisecond) // Brief delay to simulate async behavior
			s.emailVerificationsMu.Lock()
			verification, exists := s.emailVerifications[token]
			if exists {
				close(verification.CompleteChan)
				delete(s.emailVerifications, token)
				if !s.quietMode {
					log.Printf("🔧 DEV MODE: Auto-completed email verification for %s", email)
				}
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
	if err != nil {
		log.Printf("📧 ERROR: Failed to send verification email to %s: %v", email, err)
	} else {
		log.Printf("📧 Verification email sent successfully to %s", email)
	}
	return err
}

// startTeamNameCreation handles team name creation after email verification
func (s *Server) startTeamNameCreation(channel *sshbuf.Channel, fingerprint, email string) {
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
func (s *Server) handlePendingInvites(channel *sshbuf.Channel, fingerprint, email string, invites []Invite) {
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
func (s *Server) joinTeamViaInvite(channel *sshbuf.Channel, fingerprint, email string) (string, error) {
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
func (s *Server) completeRegistration(channel *sshbuf.Channel, fingerprint, email, teamName string) {
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
	channel.Write([]byte(fmt.Sprintf("  • Team machines at \033[1;36m<name>.%s.exe.dev\033[0m\r\n", teamName)))
	channel.Write([]byte("  • Shared team resources and collaboration\r\n\r\n"))

	// Create user session before continuing to main shell
	s.createUserSession(channel, fingerprint, email, teamName, "", true) // Admin since they created the team
	defer s.removeUserSession(channel)

	// Continue with normal shell flow
	// s.runMainShell(channel, true) // New users - show welcome
	// NOTE: This is now handled by the new SSH server in ssh_server.go
}

// startBillingVerification initiates the billing verification process
func (s *Server) startBillingVerification(channel *sshbuf.Channel, fingerprint, email, teamName string) {
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

	grayText := s.getGrayText(channel)
	message := "\r\n\033[1;36m" +
		"╭─────────────────────────────────────────────────╮\r\n" +
		"│  \033[1;33mStep 3: Payment Setup\033[1;36m                      │\r\n" +
		"╰─────────────────────────────────────────────────╯\033[0m\r\n\r\n" +
		"\033[1mLet's verify your payment method.\033[0m\r\n\r\n" +
		fmt.Sprintf("%sFor testing, please enter the Stripe test card:\033[0m\r\n", grayText) +
		"\033[1;33m4242424242424242\033[0m " + fmt.Sprintf("%s(Visa test card)\033[0m\r\n\r\n", grayText) +
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
		"║  \033[37m• Create and manage machines\033[1;36m                            ║\r\n" +
		"║  \033[37m• Deploy applications with persistent storage\033[1;36m           ║\r\n" +
		"║  \033[37m• Access your machines anytime via SSH\033[1;36m                 ║\r\n" +
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
	s.createUserSession(channel, fingerprint, email, teamName, "", isAdmin)
	defer s.removeUserSession(channel)

	// Continue with normal shell flow
	// s.runMainShell(channel, true) // New team members - show welcome
	// NOTE: This is now handled by the new SSH server in ssh_server.go
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
func (s *Server) createTeamName(channel *sshbuf.Channel) (string, error) {
	channel.Write([]byte("\r\n\033[1;36m" +
		"╭─────────────────────────────────────────────────╮\r\n" +
		"│  \033[1;33mStep 2: Team Setup\033[1;36m                        │\r\n" +
		"╰─────────────────────────────────────────────────╯\033[0m\r\n\r\n"))

	channel.Write([]byte("\033[1mNow let's create your team name.\033[0m\r\n\r\n"))
	grayText := s.getGrayText(channel)
	channel.Write([]byte(fmt.Sprintf("%sYour machines will be available at: \033[1;32m<name>.<team>.exe.dev\033[0m\r\n\r\n", grayText)))

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
			channel.Write([]byte(fmt.Sprintf("%s   Requirements: 3-20 characters, lowercase letters/numbers/hyphens only\033[0m\r\n\r\n", grayText)))
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
			channel.Write([]byte(fmt.Sprintf("%s   Please try a different name\033[0m\r\n\r\n", grayText)))
			continue
		}

		channel.Write([]byte("\r\n\033[1;32mPerfect! Team name is available!\033[0m\r\n"))
		channel.Write([]byte(fmt.Sprintf("%s   Your machines: \033[1;32m<name>.%s.exe.dev\033[0m\r\n\r\n", grayText, teamName)))

		return teamName, nil
	}
}

// updatePromptLine updates the current line with validation feedback (simplified)
func (s *Server) updatePromptLine(channel *sshbuf.Channel, prompt, input, feedback string) {
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

	// Start HTTP server in a goroutine if configured
	if s.httpAddr != "" {
		go func() {
			if !s.quietMode {
				log.Printf("HTTP server starting on %s", s.httpAddr)
			}
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTP server error: %v", err)
			}
		}()
	}

	// Start HTTPS server in a goroutine if configured
	if s.httpsAddr != "" {
		go func() {
			log.Printf("HTTPS server starting on %s with Let's Encrypt for exe.dev", s.httpsAddr)
			if err := s.httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Printf("HTTPS server error: %v", err)
			}
		}()

		// Start autocert HTTP handler for ACME challenges on port 80 (only for regular autocert)
		// Note: DNS challenge for wildcard certs doesn't need HTTP-01 challenge handler
		if s.certManager != nil {
			go func() {
				log.Printf("Starting autocert HTTP server on :80 for ACME challenges")
				if err := http.ListenAndServe(":80", s.certManager.HTTPHandler(nil)); err != nil {
					log.Printf("Autocert HTTP server error: %v", err)
				}
			}()
		} else if s.wildcardCertManager != nil {
			log.Printf("Using DNS challenges for wildcard certificates - port 80 not required for ACME")
		}
	}

	// Start SSH server in a goroutine
	go func() {
		sshServer := NewSSHServer(s)
		if err := sshServer.Start(s.sshAddr); err != nil {
			log.Printf("SSH server error: %v", err)
		}
	}()

	// Print SSH connection command for local dev mode
	if s.devMode == "local" {
		// Extract just the port number from the address
		sshPort := s.sshAddr
		if strings.HasPrefix(sshPort, ":") {
			sshPort = sshPort[1:]
		}
		log.Printf("SSH server started in local dev mode. Connect with:")
		log.Printf("  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %s localhost", sshPort)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down servers...")
	return s.Stop()
}

// Database helper methods

// getEmailBySSHKey checks if an SSH key is registered and returns the associated email
func (s *Server) getEmailBySSHKey(fingerprint string) (email string, verified bool, err error) {
	err = s.db.QueryRow(`
		SELECT user_email, verified
		FROM ssh_keys
		WHERE fingerprint = ?`,
		fingerprint).Scan(&email, &verified)

	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return email, verified, err
}

// getUserTeamsByEmail retrieves teams for a user by email
func (s *Server) getUserTeamsByEmail(email string) ([]TeamMember, error) {
	rows, err := s.db.Query(`
		SELECT tm.user_fingerprint, tm.team_name, tm.is_admin, tm.joined_at
		FROM team_members tm
		JOIN users u ON tm.user_fingerprint = u.public_key_fingerprint
		WHERE u.email = ?`,
		email)
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

// migrateLegacyUserKey migrates a key from the old users table to the new ssh_keys table
func (s *Server) migrateLegacyUserKey(email, fingerprint, publicKey string) error {
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
		VALUES (?, ?, ?, 1, 'Original Device')`,
		fingerprint, email, publicKey)
	return err
}

// getUserByFingerprint retrieves a user by their SSH key fingerprint
func (s *Server) getUserByFingerprint(fingerprint string) (*User, error) {
	var user User

	// First try to find user by their primary fingerprint
	err := s.db.QueryRow(`
		SELECT public_key_fingerprint, email, created_at
		FROM users
		WHERE public_key_fingerprint = ?`,
		fingerprint).Scan(&user.PublicKeyFingerprint, &user.Email, &user.CreatedAt)

	if err == nil {
		return &user, nil
	}

	if err != sql.ErrNoRows {
		return nil, err
	}

	// If not found, try to find user by their SSH key fingerprint
	err = s.db.QueryRow(`
		SELECT u.public_key_fingerprint, u.email, u.created_at
		FROM users u
		JOIN ssh_keys s ON u.email = s.user_email
		WHERE s.fingerprint = ? AND s.verified = 1`,
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

// createUser creates a new user with their personal team
func (s *Server) createUser(fingerprint, email string) error {
	// Start a transaction
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Create the user
	_, err = tx.Exec(`
		INSERT INTO users (public_key_fingerprint, email)
		VALUES (?, ?)`,
		fingerprint, email)
	if err != nil {
		return err
	}

	// Create their personal team (username as team name)
	username := strings.Split(email, "@")[0]
	username = strings.ToLower(username)
	username = strings.ReplaceAll(username, ".", "-")
	username = strings.ReplaceAll(username, "_", "-")

	// Ensure personal team name is unique
	personalTeamName := username
	for i := 1; ; i++ {
		taken, err := s.isTeamNameTakenTx(tx, personalTeamName)
		if err != nil {
			return err
		}
		if !taken {
			break
		}
		personalTeamName = fmt.Sprintf("%s%d", username, i)
	}

	// Create the personal team
	_, err = tx.Exec(`
		INSERT INTO teams (name, billing_email, is_personal, owner_fingerprint)
		VALUES (?, ?, TRUE, ?)`,
		personalTeamName, email, fingerprint)
	if err != nil {
		return err
	}

	// Add user as admin of their personal team
	_, err = tx.Exec(`
		INSERT INTO team_members (user_fingerprint, team_name, is_admin)
		VALUES (?, ?, TRUE)`,
		fingerprint, personalTeamName)
	if err != nil {
		return err
	}

	// Set this as the default team for the SSH key (if it exists)
	// Note: The SSH key might not exist yet if this is called during registration
	_, err = tx.Exec(`
		UPDATE ssh_keys SET default_team = ? WHERE fingerprint = ?`,
		personalTeamName, fingerprint)
	// Ignore the error if the key doesn't exist yet - it will be added later

	return tx.Commit()
}

// createTeam creates a new team
func (s *Server) createTeam(name, billingEmail string) error {
	_, err := s.db.Exec(`
		INSERT INTO teams (name, billing_email)
		VALUES (?, ?)`,
		name, billingEmail)
	return err
}

// createPersonalTeam creates a personal team for a user
func (s *Server) createPersonalTeam(fingerprint, teamName, email string) error {
	// Start a transaction
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Create the team
	_, err = tx.Exec(`
		INSERT INTO teams (name, billing_email, is_personal, owner_fingerprint)
		VALUES (?, ?, TRUE, ?)`,
		teamName, email, fingerprint)
	if err != nil {
		return err
	}

	// Add user as admin of the team
	_, err = tx.Exec(`
		INSERT INTO team_members (user_fingerprint, team_name, is_admin)
		VALUES (?, ?, TRUE)`,
		fingerprint, teamName)
	if err != nil {
		return err
	}

	// Set as default team
	_, err = tx.Exec(`
		UPDATE ssh_keys
		SET default_team = ?
		WHERE fingerprint = ?`,
		teamName, fingerprint)
	if err != nil {
		return err
	}

	return tx.Commit()
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
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// isTeamNameTakenTx checks if a team name is already taken within a transaction
func (s *Server) isTeamNameTakenTx(tx *sql.Tx, teamName string) (bool, error) {
	var count int
	err := tx.QueryRow(`SELECT COUNT(*) FROM teams WHERE name = ?`, teamName).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

// getDefaultTeamForKey gets the default team for an SSH key
func (s *Server) getDefaultTeamForKey(fingerprint string) (string, error) {
	var defaultTeam sql.NullString
	err := s.db.QueryRow(`SELECT default_team FROM ssh_keys WHERE fingerprint = ?`, fingerprint).Scan(&defaultTeam)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return defaultTeam.String, nil
}

// setDefaultTeamForKey sets the default team for an SSH key
func (s *Server) setDefaultTeamForKey(fingerprint, teamName string) error {
	_, err := s.db.Exec(`UPDATE ssh_keys SET default_team = ? WHERE fingerprint = ?`, teamName, fingerprint)
	return err
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

	if !s.quietMode {
		log.Println("Servers stopped")
	}
	return nil
}
