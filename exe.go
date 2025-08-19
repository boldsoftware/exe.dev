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
	"log/slog"
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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/keighl/postmark"
	"github.com/lmittmann/tint"

	"github.com/stripe/stripe-go/v76"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"

	"exe.dev/container"
	"exe.dev/porkbun"
	"exe.dev/sshbuf"
)

//go:embed exe_schema.sql
var schemaSQL string

// SetupLogger configures slog based on the LOG_FORMAT environment variable.
// LOG_FORMAT can be "json", "text", "tint", or "" (defaults: tint in dev, text in prod)
// LOG_LEVEL can be "debug", "info", "warn", "error" (default: info)
func SetupLogger(devMode string) {
	logFormat := strings.ToLower(os.Getenv("LOG_FORMAT"))
	logLevel := strings.ToLower(os.Getenv("LOG_LEVEL"))

	// Set default format based on dev mode if not explicitly set
	if logFormat == "" {
		if devMode != "" {
			logFormat = "tint" // Use tint in dev mode
		} else {
			logFormat = "text" // Use text in production
		}
	}

	// Parse log level
	var level slog.Level
	switch logLevel {
	case "debug":
		level = slog.LevelDebug
	case "info", "":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// Create handler based on format
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: level,
	}

	switch logFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case "tint":
		handler = tint.NewHandler(os.Stdout, &tint.Options{
			Level: level,
		})
	default: // "text" and any unknown format
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	// Set as default logger
	slog.SetDefault(slog.New(handler))
}

//go:embed welcome.html
var welcomeHTML []byte

//go:embed exe.dev.png
var exeDevPNG []byte

//go:embed browser-woodcut.png
var browserWoodcutPNG []byte

//go:embed favicon.ico
var faviconICO []byte

// SSHMetrics holds SSH server metrics
type SSHMetrics struct {
	connectionsTotal   *prometheus.CounterVec
	connectionsCurrent prometheus.Gauge
	authAttempts       *prometheus.CounterVec
	sessionDuration    *prometheus.HistogramVec
}

// NewSSHMetrics creates and registers SSH metrics
func NewSSHMetrics(registry *prometheus.Registry) *SSHMetrics {
	metrics := &SSHMetrics{
		connectionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ssh_connections_total",
				Help: "Total number of SSH connections.",
			},
			[]string{"result"},
		),
		connectionsCurrent: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "ssh_connections_current",
				Help: "Current number of active SSH connections.",
			},
		),
		authAttempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ssh_auth_attempts_total",
				Help: "Total number of SSH authentication attempts.",
			},
			[]string{"result", "method"},
		),
		sessionDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "ssh_session_duration_seconds",
				Help:    "Duration of SSH sessions in seconds.",
				Buckets: []float64{1, 10, 60, 300, 600, 1800, 3600, 7200}, // 1s to 2h
			},
			[]string{"reason"},
		),
	}

	registry.MustRegister(
		metrics.connectionsTotal,
		metrics.connectionsCurrent,
		metrics.authAttempts,
		metrics.sessionDuration,
	)

	return metrics
}

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
	DockerHost           *string // DOCKER_HOST value where this container runs
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
	TeamName             string // Team name selected by user
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
	piperAddr string
	BaseURL   string

	httpServer          *http.Server
	httpsServer         *http.Server
	sshConfig           *ssh.ServerConfig
	certManager         *autocert.Manager
	wildcardCertManager *porkbun.WildcardCertManager

	// Piper plugin for SSH proxy authentication
	piperPlugin *PiperPlugin

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

	// Metrics
	metricsRegistry *prometheus.Registry
	sshMetrics      *SSHMetrics

	mu       sync.RWMutex
	stopping bool
}

// NewServer creates a new Server instance with database and container management
func NewServer(httpAddr, httpsAddr, sshAddr, piperAddr, dbPath string, devMode string, dockerHosts []string) (*Server, error) {
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
		slog.Warn("POSTMARK_API_KEY not set, email verification will not work")
	}

	// Get Stripe key
	stripeKey := os.Getenv("STRIPE_API_KEY")
	if stripeKey == "" {
		stripeKey = "sk_test_51QxIgSGWIXq1kJnoiKwEcehJeO68QFsueLGymU9zR5jsJtMup5arFZZlHYaOzG3Bsw2GfnIG9H3Jv8Be10vqK1nW001hUxrS2g"
		if !quietMode {
			slog.Info("Using default Stripe test key")
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
				slog.Warn("Failed to initialize container manager, functionality will be disabled", "error", managerErr)
			}
			containerManager = nil
		} else {
			if !quietMode {
				slog.Info("Machine management enabled", "docker_hosts", dockerHosts)
			}
		}
	} else {
		if !quietMode {
			slog.Info("No Docker hosts configured, container functionality disabled")
		}
	}

	// Initialize metrics
	metricsRegistry := prometheus.NewRegistry()
	sshMetrics := NewSSHMetrics(metricsRegistry)

	s := &Server{
		httpAddr:             httpAddr,
		httpsAddr:            httpsAddr,
		sshAddr:              sshAddr,
		piperAddr:            piperAddr,
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
		metricsRegistry:      metricsRegistry,
		sshMetrics:           sshMetrics,
	}

	s.setupHTTPServer()
	s.setupHTTPSServer()
	s.setupSSHServer()

	return s, nil
}

// setupHTTPServer configures the HTTP server
func (s *Server) setupHTTPServer() {
	// Use standard promhttp instrumentation
	instrumentedHandler := promhttp.InstrumentMetricHandler(
		s.metricsRegistry,
		s)

	s.httpServer = &http.Server{
		Addr:    s.httpAddr,
		Handler: instrumentedHandler,
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
		slog.Info("Using Porkbun DNS provider for wildcard TLS certificates")
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
			slog.Info("Using standard autocert (no wildcard support)", "note", "Set PORKBUN_API_KEY and PORKBUN_SECRET_API_KEY for wildcard certificates")
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
		PublicKeyCallback: s.AuthenticatePublicKey,
		AuthLogCallback:   s.logAuthAttempt,
		MaxAuthTries:      6, // Limit authentication attempts to prevent brute force
	}

	// Load or generate persistent host keys
	if err := s.generateHostKey(); err != nil {
		slog.Error("Failed to generate host key", "error", err)
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
		fingerprint := s.GetPublicKeyFingerprint(signer.PublicKey())

		// Store in database
		_, err = s.db.Exec(`
			INSERT INTO ssh_host_key (id, private_key, public_key, fingerprint, created_at, updated_at)
			VALUES (1, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			privateKeyPEM, publicKeyPEM, fingerprint)
		if err != nil {
			return fmt.Errorf("failed to store host key: %w", err)
		}

		if !s.quietMode {
			slog.Info("Generated and stored new SSH host key", "fingerprint", fingerprint)
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

		fingerprint := s.GetPublicKeyFingerprint(signer.PublicKey())
		if !s.quietMode {
			slog.Info("Loaded existing SSH host key", "fingerprint", fingerprint)
		}
		s.sshConfig.AddHostKey(signer)
	}

	return nil
}

// getPublicKeyFingerprint generates a SHA256 fingerprint for a public key
func (s *Server) GetPublicKeyFingerprint(key ssh.PublicKey) string {
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
			slog.Error("Error scanning team membership", "error", err)
			continue
		}

		// Insert the new fingerprint into the same team with the same admin status
		_, err = s.db.Exec(`
			INSERT INTO team_members (user_fingerprint, team_name, is_admin)
			VALUES (?, ?, ?)
			ON CONFLICT(user_fingerprint, team_name) DO UPDATE SET is_admin = ?`,
			newFingerprint, teamName, isAdmin, isAdmin)

		if err != nil {
			slog.Error("Failed to add fingerprint to team", "fingerprint", newFingerprint, "team", teamName, "error", err)
		} else {
			slog.Info("Added new SSH key to team", "team", teamName, "admin", isAdmin, "user", userEmail)
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
		port := strings.TrimPrefix(s.httpAddr, ":")
		return fmt.Sprintf("http://localhost:%s", port)
	}
	return "https://exe.dev"
}

// sendEmail sends an email using the configured email service
func (s *Server) sendEmail(to, subject, body string) error {
	// In dev mode, always just log the email
	if s.devMode != "" {
		if !s.quietMode {
			slog.Info("📧 DEV MODE: Would send email", "to", to, "subject", subject, "body", body)
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
		slog.Error("📧 Failed to send email", "to", to, "subject", subject, "error", err)
	} else {
		slog.Info("📧 Email sent successfully", "to", to, "subject", subject)
	}
	return err
}

// sendVerificationEmail sends an email verification link
func (s *Server) sendVerificationEmail(email, token string) error {
	subject := "Verify your email - exe.dev"
	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", s.getBaseURL(), token)

	body := fmt.Sprintf(`Hello,

Please click the link below to verify your email address:

%s

This link will expire in 15 minutes.

Best regards,
The exe.dev team`, verifyURL)

	return s.sendEmail(email, subject, body)
}

// logAuthAttempt logs all SSH authentication attempts for debugging
func (s *Server) logAuthAttempt(conn ssh.ConnMetadata, method string, err error) {
	if s.testMode {
		return // Skip auth logging in test mode to reduce noise
	}

	var user, remoteAddr, clientVersion string
	if conn != nil {
		user = conn.User()
		remoteAddr = conn.RemoteAddr().String()
		clientVersion = string(conn.ClientVersion())
	}

	if err != nil {
		// Log failed authentication attempts with more detail for security monitoring
		slog.Warn("SSH auth failed", "method", method, "user", user, "remote_addr", remoteAddr, "client_version", clientVersion, "error", err)
	} else {
		// Log successful authentication
		slog.Info("SSH auth success", "method", method, "user", user, "remote_addr", remoteAddr, "client_version", clientVersion)
	}
}

// authenticatePublicKey handles SSH public key authentication
func (s *Server) AuthenticatePublicKey(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fingerprint := s.GetPublicKeyFingerprint(key)
	publicKeyStr := string(ssh.MarshalAuthorizedKey(key))

	var user, remoteAddr string
	if conn != nil {
		user = conn.User()
		remoteAddr = conn.RemoteAddr().String()
	} else {
		user = "<nil>"
		remoteAddr = "<nil>"
	}
	slog.Debug("Authentication request", "user", user, "remote_addr", remoteAddr, "key_type", key.Type(), "fingerprint", fingerprint[:16])

	// Check if this is a proxy connection from sshpiper
	slog.Debug("Checking if key is a proxy key", "fingerprint", fingerprint[:16])
	if originalUserKey := s.lookupEphemeralProxyKey(key); originalUserKey != nil {
		slog.Debug("Ephemeral proxy authentication detected", "user", user)
		return s.authenticateProxyUser(user, originalUserKey)
	} else {
		slog.Debug("Not a proxy key, treating as direct user connection")
	}
	// Log non-proxy connections for monitoring - in production, all connections should come via proxy
	slog.Warn("Direct connection to exed - should come via proxy", "remote_addr", remoteAddr, "fingerprint", fingerprint)

	// First check if this key is already registered in ssh_keys table
	email, verified, err := s.GetEmailBySSHKey(fingerprint)
	if err != nil {
		slog.Error("Database error checking SSH key", "fingerprint", fingerprint, "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has team memberships
		teams, err := s.getUserTeamsByEmail(email)
		if err != nil {
			slog.Error("Database error getting teams for user", "email", email, "error", err)
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
		slog.Debug("HTTP request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
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
	case "/metrics":
		s.handleMetrics(w, r)
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

// handleMetrics serves Prometheus metrics
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	handler := promhttp.HandlerFor(s.metricsRegistry, promhttp.HandlerOpts{})
	handler.ServeHTTP(w, r)
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
		slog.Error("Database error during device verification check", "error", err)
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
		slog.Error("Database error during device verification", "error", err)
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
		slog.Error("Failed to add SSH key", "error", err)
		http.Error(w, "Failed to verify device", http.StatusInternalServerError)
		return
	}

	// Automatically add the new SSH key to all teams the user is already a member of
	err = s.inheritUserTeamMemberships(fingerprint, email)
	if err != nil {
		slog.Error("Failed to inherit team memberships", "email", email, "error", err)
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
		teamName := verification.TeamName

		// Create the user if they don't exist
		user, err := s.getUserByFingerprint(fingerprint)
		if err != nil || user == nil {
			slog.Info("User doesn't exist for fingerprint, creating", "fingerprint", fingerprint)
			// User doesn't exist - create them with their team
			if teamName != "" {
				// Use the team name selected during registration
				if err := s.createUserWithTeam(fingerprint, email, teamName); err != nil {
					slog.Error("Failed to create user with team during email verification", "error", err)
					s.emailVerificationsMu.Unlock()
					// Clean up pending registration on failure
					s.db.Exec("DELETE FROM pending_registrations WHERE token = ?", token)
					http.Error(w, "Failed to create user account", http.StatusInternalServerError)
					return
				}
				// Clean up pending registration on success
				s.db.Exec("DELETE FROM pending_registrations WHERE token = ?", token)
			} else {
				// Fallback to auto-generated team name for existing flow
				if err := s.createUser(fingerprint, email); err != nil {
					slog.Error("Failed to create user during email verification", "error", err)
					s.emailVerificationsMu.Unlock()
					http.Error(w, "Failed to create user account", http.StatusInternalServerError)
					return
				}
			}
			slog.Info("Created new user", "email", email, "fingerprint", fingerprint, "team", teamName)
		} else {
			slog.Debug("User already exists for fingerprint", "fingerprint", fingerprint)
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
				slog.Error("Error storing SSH key during verification", "error", err)
			}
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
		if err != nil {
			slog.Error("Failed to create auth cookie during SSH email verification", "error", err)
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
			slog.Error("Invalid email verification token", "error", err)
			http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
		if err != nil {
			slog.Error("Failed to create auth cookie during HTTP email verification", "error", err)
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
			slog.Error("Failed to cleanup email verification token", "error", err)
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
		slog.Info("Container proxy request", "container", containerName, "team", teamName, "port", port, "path", r.URL.Path)
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
		slog.Error("Invalid auth cookie", "error", err)
		// Invalid cookie, redirect to auth
		authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
		http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
		return
	}

	// Check if user has access to this team/container
	hasAccess, err := s.userHasTeamAccess(fingerprint, teamName)
	if err != nil {
		slog.Error("Error checking team access", "error", err)
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
		slog.Error("Container not found", "error", err)
		http.Error(w, "Container not found", http.StatusNotFound)
		return
	}

	// TODO: Wake up container if it's sleeping
	containerID := ""
	if machine.ContainerID != nil {
		containerID = *machine.ContainerID
	}
	slog.Info("Proxying to container", "name", machine.Name, "id", containerID)

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
		slog.Error("Invalid auth token", "error", err)
		http.Error(w, "Invalid or expired auth token", http.StatusUnauthorized)
		return
	}

	// Create authentication cookie for this team
	cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
	if err != nil {
		slog.Error("Failed to create auth cookie", "error", err)
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
		slog.Error("Database error checking user", "error", err)
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
		slog.Error("Failed to store email verification", "error", err)
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
		slog.Error("Failed to send auth email", "error", err)
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
			slog.Error("Invalid email verification token", "error", err)
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
			slog.Error("Invalid auth token in callback", "error", err)
			http.Error(w, "Invalid or expired authentication token", http.StatusUnauthorized)
			return
		}
	}

	// Create main domain auth cookie
	cookieValue, err := s.createAuthCookie(fingerprint, r.Host)
	if err != nil {
		slog.Error("Failed to create main auth cookie", "error", err)
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
		slog.Error("Failed to mark token as used", "error", err)
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
				slog.Error("Failed to create auth token", "error", err)
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
		slog.Error("Failed to connect to container", "error", err)
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
		slog.Error("Proxy error", "machine", machine.Name, "error", err)
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
func (s *Server) FindMachineByNameForUser(fingerprint, machineName string) *Machine {
	slog.Debug("FindMachineByNameForUser", "fingerprint", fingerprint[:16], "machine_name", machineName)
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
	slog.Debug("Default team for key", "team", defaultTeam, "error", err)
	if err == nil && defaultTeam != "" {
		machine, err := s.getMachineByName(defaultTeam, machineNameOnly)
		slog.Debug("Checked default team for machine", "team", defaultTeam, "machine", machineNameOnly, "found", machine != nil, "error", err)
		if err == nil {
			return machine
		}
	}

	// Get user's teams and search all of them
	teams, err := s.getUserTeams(fingerprint)
	slog.Debug("User teams", "count", len(teams), "error", err)
	if err != nil || len(teams) == 0 {
		return nil
	}

	// Check each team for a machine with this name
	for _, team := range teams {
		machine, err := s.getMachineByName(team.TeamName, machineNameOnly)
		slog.Debug("Checked team for machine", "team", team.TeamName, "machine", machineNameOnly, "found", machine != nil, "error", err)
		if err == nil {
			return machine
		}
	}

	slog.Debug("Machine not found in any team for user", "machine", machineName, "user", fingerprint[:16])
	return nil
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
	channel.Write([]byte("\r\n\033[1;36mUser Information:\033[0m\r\n\r\n"))
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
		return fmt.Sprintf("ssh -p 2222 %s@localhost", machineName)
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

// createMachineWithDockerHost stores machine info including docker host in database
func (s *Server) createMachineWithDockerHost(userFingerprint, teamName, name, containerID, image, dockerHost string) error {
	_, err := s.db.Exec(`
		INSERT INTO machines (team_name, name, status, image, container_id, created_by_fingerprint, docker_host)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, teamName, name, "pending", image, containerID, userFingerprint, dockerHost)
	return err
}

// createMachineWithSSH stores machine info including SSH keys in database
func (s *Server) createMachineWithSSH(userFingerprint, teamName, name, containerID, image string, sshKeys *container.ContainerSSHKeys, sshPort int) error {
	_, err := s.db.Exec(`
		INSERT INTO machines (
			team_name, name, status, image, container_id, created_by_fingerprint,
			ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key,
			ssh_host_certificate, ssh_client_private_key, ssh_port
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, teamName, name, "running", image, containerID, userFingerprint,
		sshKeys.ServerIdentityKey, sshKeys.AuthorizedKeys, sshKeys.CAPublicKey,
		sshKeys.HostCertificate, sshKeys.ClientPrivateKey, sshPort)
	return err
}

// createMachineWithSSHAndDockerHost stores machine info including SSH keys and docker host in database
func (s *Server) createMachineWithSSHAndDockerHost(userFingerprint, teamName, name, containerID, image, dockerHost string, sshKeys *container.ContainerSSHKeys, sshPort int) error {
	_, err := s.db.Exec(`
		INSERT INTO machines (
			team_name, name, status, image, container_id, created_by_fingerprint,
			ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key,
			ssh_host_certificate, ssh_client_private_key, ssh_port, docker_host
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, teamName, name, "running", image, containerID, userFingerprint,
		sshKeys.ServerIdentityKey, sshKeys.AuthorizedKeys, sshKeys.CAPublicKey,
		sshKeys.HostCertificate, sshKeys.ClientPrivateKey, sshPort, dockerHost)
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
		SELECT id, team_name, name, status, image, container_id, created_by_fingerprint, created_at, updated_at, last_started_at, docker_host
		FROM machines
		WHERE team_name = ? AND name = ?
	`, teamName, name).Scan(
		&machine.ID, &machine.TeamName, &machine.Name, &machine.Status,
		&machine.Image, &machine.ContainerID, &machine.CreatedByFingerprint,
		&machine.CreatedAt, &machine.UpdatedAt, &machine.LastStartedAt, &machine.DockerHost,
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
		slog.Debug("ASCII art centering", "terminal_width", terminalWidth, "art_width", artWidth, "padding", leftPadding, "mode", terminalMode)
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
				slog.Info("HTTP server starting", "addr", s.httpAddr)
			}
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP server error", "error", err)
			}
		}()
	}

	// Start HTTPS server in a goroutine if configured
	if s.httpsAddr != "" {
		go func() {
			slog.Info("HTTPS server starting with Let's Encrypt for exe.dev", "addr", s.httpsAddr)
			if err := s.httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTPS server error", "error", err)
			}
		}()

		// Start autocert HTTP handler for ACME challenges on port 80 (only for regular autocert)
		// Note: DNS challenge for wildcard certs doesn't need HTTP-01 challenge handler
		if s.certManager != nil {
			go func() {
				slog.Info("Starting autocert HTTP server on :80 for ACME challenges")
				if err := http.ListenAndServe(":80", s.certManager.HTTPHandler(nil)); err != nil {
					slog.Error("Autocert HTTP server error", "error", err)
				}
			}()
		} else if s.wildcardCertManager != nil {
			slog.Info("Using DNS challenges for wildcard certificates - port 80 not required for ACME")
		}
	}

	// Start piper plugin server in a goroutine
	// Set the plugin reference before starting the server to avoid race conditions
	s.piperPlugin = NewPiperPlugin(s, s.piperAddr)
	go func() {
		if err := s.piperPlugin.Serve(); err != nil {
			slog.Error("Piper plugin server error", "error", err)
		}
	}()

	// Start SSH server in a goroutine
	go func() {
		sshServer := NewSSHServer(s)
		if err := sshServer.Start(s.sshAddr); err != nil {
			slog.Error("SSH server error", "error", err)
		}
	}()

	// Print SSH connection command for local dev mode
	if s.devMode == "local" {
		// Extract just the port number from the address
		sshPort := strings.TrimPrefix(s.sshAddr, ":")
		slog.Info("SSH server started in local dev mode. Connect with:")
		slog.Info("  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %s localhost", "port", sshPort)
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
func (s *Server) GetEmailBySSHKey(fingerprint string) (email string, verified bool, err error) {
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

// createUserWithTeam creates a new user with a specific team name
func (s *Server) createUserWithTeam(fingerprint, email, teamName string) error {
	// Start a transaction
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Create user
	_, err = tx.Exec(`
		INSERT INTO users (public_key_fingerprint, email)
		VALUES (?, ?)`,
		fingerprint, email)
	if err != nil {
		return err
	}

	// Create personal team
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

	// Set this as the default team for the SSH key
	_, err = tx.Exec(`
		UPDATE ssh_keys SET default_team = ? WHERE fingerprint = ?`,
		teamName, fingerprint)
	if err != nil {
		return err
	}

	// Commit transaction
	return tx.Commit()
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

// isTeamNameTakenOrReserved checks if a team name already exists or is reserved in pending registrations
func (s *Server) isTeamNameTakenOrReserved(teamName string) (bool, error) {
	// Check existing teams
	taken, err := s.isTeamNameTaken(teamName)
	if err != nil || taken {
		return taken, err
	}

	// Check pending registrations (reserved team names)
	var count int
	currentTime := time.Now().Format(time.RFC3339)
	err = s.db.QueryRow(`
		SELECT COUNT(*) FROM pending_registrations 
		WHERE team_name = ? AND expires_at > ?`,
		teamName, currentTime).Scan(&count)
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
		slog.Error("HTTP server shutdown error", "error", err)
	}

	// Shutdown HTTPS server if running
	if s.httpsServer != nil {
		if err := s.httpsServer.Shutdown(ctx); err != nil {
			slog.Error("HTTPS server shutdown error", "error", err)
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

// lookupEphemeralProxyKey checks if the given key is an ephemeral proxy key
// and if so, returns the original user's public key by asking the piper plugin.
//
// EPHEMERAL PROXY KEY FLOW:
// 1. User connects to sshpiper with their key
// 2. Piper plugin generates ephemeral proxy key and stores mapping
// 3. Piper sends proxy key to exed for authentication
// 4. Exed recognizes proxy key and asks piper plugin for original user key
// 5. Exed authenticates based on original user key
func (s *Server) lookupEphemeralProxyKey(proxyKey ssh.PublicKey) []byte {
	// Get the original user key from the piper plugin
	// The piper plugin is always configured when SSH proxy is enabled
	if s.piperPlugin == nil {
		slog.Error("Piper plugin not configured but proxy key received")
		return nil
	}

	proxyFingerprint := s.GetPublicKeyFingerprint(proxyKey)
	slog.Debug("Looking up proxy key", "fingerprint", proxyFingerprint[:16])

	originalUserKey, exists := s.piperPlugin.lookupOriginalUserKey(proxyFingerprint)
	if !exists {
		slog.Debug("Proxy key not found or expired", "fingerprint", proxyFingerprint[:16])
		return nil // Not a proxy key or expired
	}

	slog.Debug("Found original user key for proxy key", "key_length", len(originalUserKey), "proxy_fingerprint", proxyFingerprint[:16])
	return originalUserKey
}

// authenticateProxyUser authenticates a user through an ephemeral proxy connection
func (s *Server) authenticateProxyUser(username string, originalUserKeyBytes []byte) (*ssh.Permissions, error) {
	// Parse the original user's public key
	originalUserKey, err := ssh.ParsePublicKey(originalUserKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse original user key: %v", err)
	}

	originalFingerprint := s.GetPublicKeyFingerprint(originalUserKey)
	originalKeyStr := string(ssh.MarshalAuthorizedKey(originalUserKey))

	slog.Debug("Authenticating original user", "fingerprint", originalFingerprint, "username", username)

	// Look up the user by their original fingerprint
	email, verified, err := s.GetEmailBySSHKey(originalFingerprint)
	if err != nil {
		slog.Error("Database error checking SSH key", "fingerprint", originalFingerprint, "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has team memberships
		teams, err := s.getUserTeamsByEmail(email)
		if err != nil {
			slog.Error("Database error getting teams for user", "email", email, "error", err)
		}

		if len(teams) > 0 {
			// User is fully registered with team membership
			return &ssh.Permissions{
				Extensions: map[string]string{
					"fingerprint": originalFingerprint,
					"registered":  "true",
					"email":       email,
					"public_key":  originalKeyStr,
					"proxy_user":  username,
				},
			}, nil
		}
	}

	// Handle unregistered or unverified users
	if email != "" && !verified {
		return &ssh.Permissions{
			Extensions: map[string]string{
				"fingerprint":        originalFingerprint,
				"registered":         "false",
				"email":              email,
				"public_key":         originalKeyStr,
				"needs_verification": "true",
				"proxy_user":         username,
			},
		}, nil
	}

	// User is not registered
	return &ssh.Permissions{
		Extensions: map[string]string{
			"fingerprint": originalFingerprint,
			"registered":  "false",
			"public_key":  originalKeyStr,
			"proxy_user":  username,
		},
	}, nil
}
