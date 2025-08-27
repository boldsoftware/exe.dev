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
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mathrand "math/rand"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/keighl/postmark"
	"github.com/lmittmann/tint"

	"github.com/stripe/stripe-go/v76"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"

	"exe.dev/container"
	"exe.dev/ipallocator"
	"exe.dev/porkbun"
	"exe.dev/sqlite"
	"exe.dev/sshbuf"
)

//go:embed schema/*.sql
var migrationFS embed.FS

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

//go:embed static
var staticFS embed.FS

//go:embed templates
var templatesFS embed.FS

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
	UserID    string
	Email     string
	CreatedAt time.Time
}

// AllocType defines the resource allocation tier
type AllocType string

const (
	AllocTypeMedium AllocType = "medium" // Default allocation type
)

// Region represents a geographical region where resources are allocated
type Region string

const (
	RegionAWSUSWest2 Region = "aws-us-west-2" // Default and only region for now
)

// Alloc represents an allocation of resources for a user
type Alloc struct {
	AllocID          string
	UserID           string
	AllocType        AllocType
	Region           Region
	DockerHost       sql.NullString // Docker host where this alloc's containers run
	CreatedAt        time.Time
	StripeCustomerID sql.NullString
	BillingEmail     sql.NullString
}

// PathMatcher defines how to match request paths
type PathMatcher struct {
	Prefix string `json:"prefix,omitempty"` // Match paths with this prefix
}

// Route defines access rules for HTTP requests
type Route struct {
	Name     string      `json:"name"`     // Unique name for this route
	Priority int         `json:"priority"` // Lower numbers = higher priority
	Methods  []string    `json:"methods"`  // HTTP methods ("*" for all)
	Paths    PathMatcher `json:"paths"`    // Path matching rules
	Policy   string      `json:"policy"`   // "public" or "private"
	Ports    []int       `json:"ports"`    // Allowed destination ports. We try all of them until success.
}

// MachineRoutes represents the complete routing configuration for a machine
type MachineRoutes []Route

// Machine represents a container/VM
type Machine struct {
	ID              int
	AllocID         string
	Name            string
	Status          string
	Image           string
	ContainerID     *string
	CreatedByUserID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastStartedAt   *time.Time
	DockerHost      *string // DOCKER_HOST value where this container runs
	Routes          *string // JSON-encoded routing configuration
	// SSH fields for container access
	SSHServerIdentityKey *string // SSH server private key (PEM format)
	SSHAuthorizedKeys    *string // User certificate for authorized_keys (client auth)
	SSHCAPublicKey       *string // CA public key for mutual auth
	SSHHostCertificate   *string // Host certificate for host key validation
	SSHClientPrivateKey  *string // Private key for connecting to container (PEM format)
	SSHPort              *int    // SSH port for this container
}

// GetRoutes parses and returns the machine's routing configuration
func (m *Machine) GetRoutes() (MachineRoutes, error) {
	if m.Routes == nil || *m.Routes == "" {
		return m.getDefaultRoutes(), nil
	}

	var routes MachineRoutes
	err := json.Unmarshal([]byte(*m.Routes), &routes)
	if err != nil {
		return m.getDefaultRoutes(), err
	}

	return routes, nil
}

// SetRoutes sets the machine's routing configuration
func (m *Machine) SetRoutes(routes MachineRoutes) error {
	data, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	routesStr := string(data)
	m.Routes = &routesStr
	return nil
}

// getDefaultRoutes returns the default routing configuration
func (m *Machine) getDefaultRoutes() MachineRoutes {
	return MachineRoutes{
		{
			Name:     "default",
			Priority: 10,
			Methods:  []string{"*"},
			Paths:    PathMatcher{Prefix: "/"},
			Policy:   "private",
			Ports:    []int{80, 8000, 8080, 8888},
		},
	}
}

// UserPageData represents the data for the user dashboard page
type UserPageData struct {
	User     User
	SSHKeys  []SSHKey
	Machines []Machine
}

// SSHKey represents an SSH key for the user page
type SSHKey struct {
	UserID    string
	PublicKey string
	Verified  bool
}

// EmailVerification represents a pending email verification (in-memory)
type EmailVerification struct {
	PublicKey    string
	Email        string
	Token        string
	CompleteChan chan struct{}
	CreatedAt    time.Time
}

// MagicSecret represents a temporary authentication secret for proxy magic URLs
type MagicSecret struct {
	UserID      string
	MachineName string // Direct machine name instead of team
	RedirectURL string
	ExpiresAt   time.Time
	CreatedAt   time.Time
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
	UserID    string
	Email     string
	TeamName  string
	IsAdmin   bool
	PublicKey string
	CreatedAt time.Time
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
	sshHostKey          ssh.Signer
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
	billingVerifications   map[string]*BillingVerification // user_id -> billing verification
	magicSecretsMu         sync.RWMutex
	magicSecrets           map[string]*MagicSecret // secret -> magic secret with expiration

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

	// IP allocation strategy for dev/production modes
	ipAllocator ipallocator.IPAllocator

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

	slog.Debug("Opened database", "dbPath", dbPath)

	// Run database migrations
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
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
	sqlite.RegisterSQLiteMetrics(metricsRegistry)

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
		magicSecrets:         make(map[string]*MagicSecret),
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

// SetIPAllocator enables or disables mDNS functionality for the server
func (s *Server) SetIPAllocator(allocator ipallocator.IPAllocator) {
	s.ipAllocator = allocator
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
		s.sshHostKey = signer

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
		s.sshHostKey = signer
	}

	return nil
}

// getPublicKeyFingerprint generates a SHA256 fingerprint for a public key
func (s *Server) GetPublicKeyFingerprint(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(hash[:])
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

// renderTemplate is a helper method that handles template parsing and execution
func (s *Server) renderTemplate(w http.ResponseWriter, templateName string, data interface{}) error {
	// Parse template
	tmplFS, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		slog.Error("Failed to access templates filesystem", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}

	tmpl, err := template.ParseFS(tmplFS, templateName)
	if err != nil {
		slog.Error("Failed to parse template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}

	// Render template
	w.Header().Set("Content-Type", "text/html")
	err = tmpl.Execute(w, data)
	if err != nil {
		slog.Error("Failed to execute template", "error", err, "template", templateName)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return err
	}

	return nil
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

	if s.devMode != "" && !s.quietMode {
		fmt.Printf("Verification Link: \n%s\n\n", verifyURL)
	}
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
	publicKeyStr := string(ssh.MarshalAuthorizedKey(key))

	var user, remoteAddr string
	if conn != nil {
		user = conn.User()
		remoteAddr = conn.RemoteAddr().String()
	} else {
		user = "<nil>"
		remoteAddr = "<nil>"
	}
	slog.Debug("Authentication request", "user", user, "remote_addr", remoteAddr, "key_type", key.Type())

	// Check if this is a proxy connection from sshpiper
	slog.Debug("Checking if key is a proxy key")
	if originalUserKey, localAddress := s.lookupEphemeralProxyKey(key); originalUserKey != nil {
		slog.Debug("Ephemeral proxy authentication detected", "user", user, "local_address", localAddress)
		return s.authenticateProxyUserWithLocalAddress(user, originalUserKey, localAddress)
	} else {
		slog.Debug("Not a proxy key, treating as direct user connection")
	}
	// Log non-proxy connections for monitoring - in production, all connections should come via proxy
	slog.Warn("Direct connection to exed - should come via proxy", "remote_addr", remoteAddr)

	// First check if this key is already registered in ssh_keys table
	email, verified, err := s.GetEmailBySSHKey(publicKeyStr)
	if err != nil {
		slog.Error("Database error checking SSH key", "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has an alloc
		var userID string
		err = s.db.QueryRow("SELECT user_id FROM users WHERE email = ?", email).Scan(&userID)
		if err == nil {
			// Check if user has an alloc
			var allocExists bool
			err = s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM allocs WHERE user_id = ?)", userID).Scan(&allocExists)
			if err == nil && allocExists {
				// User is fully registered with an allocation
				return &ssh.Permissions{
					Extensions: map[string]string{
						"registered": "true",
						"email":      email,
						"public_key": publicKeyStr,
					},
				}, nil
			}
		}
	}

	// Check if there's an email associated with any SSH key and if this is a new key for that user
	if email != "" && !verified {
		// This key belongs to a user but isn't verified yet - treat as standard unregistered user
		// They will go through the normal flow and wait for email verification
		return &ssh.Permissions{
			Extensions: map[string]string{
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
			"registered": "false",
			"public_key": publicKeyStr,
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
	if !s.quietMode {
		slog.Debug("HTTP request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr, "host", r.Host)
	}

	// Check if this should be handled by the proxy handler
	isProxy := s.isProxyRequest(r.Host)
	isTerminal := s.isTerminalRequest(r.Host)
	if !s.quietMode {
		slog.Info("[REDIRECT] Main handler routing check", "host", r.Host, "isProxy", isProxy, "isTerminal", isTerminal)
	}
	if isTerminal {
		s.handleTerminalRequest(w, r)
		return
	}
	if isProxy {
		s.handleProxyRequest(w, r)
		return
	}

	// Handle root path and user dashboard
	path := r.URL.Path
	switch path {
	case "/":
		// In production mode, require basic auth for home page
		if s.devMode == "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != "comingsoon" || pass != "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="exe.dev"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		// Check if user is authenticated
		if cookie, err := r.Cookie("exe-auth"); err == nil && cookie.Value != "" {
			if userID, err := s.validateAuthCookie(cookie.Value, r.Host); err == nil {
				// User is authenticated, redirect to user dashboard
				s.handleUserDashboard(w, r, userID)
				return
			}
		}
		// User not authenticated, serve welcome page
		s.serveStaticFile(w, r, "welcome.html")
		return
	case "/~", "/~/":
		// User dashboard - require authentication
		cookie, err := r.Cookie("exe-auth")
		if err != nil || cookie.Value == "" {
			// Not authenticated, redirect to auth
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		userID, err := s.validateAuthCookie(cookie.Value, r.Host)
		if err != nil {
			// Invalid cookie, redirect to auth
			authURL := fmt.Sprintf("/auth?redirect=%s", url.QueryEscape(r.URL.String()))
			http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
			return
		}
		s.handleUserDashboard(w, r, userID)
		return
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
	case "/auth/confirm":
		s.handleAuthConfirm(w, r)
	case "/logout":
		s.handleLogout(w, r)
	default:
		if strings.HasPrefix(path, "/auth/") {
			s.handleAuthCallback(w, r)
			return
		}

		// Try to serve static file if GET request
		if r.Method == "GET" && len(path) > 1 {
			filename := path[1:] // Remove leading slash
			// Security check: ensure filename doesn't contain path traversal
			if !strings.Contains(filename, "..") && !strings.Contains(filename, "/") {
				s.serveStaticFile(w, r, filename)
				return
			}
		}
		http.NotFound(w, r)
	}
}

// handleRoot handles requests to the root path
// serveStaticFile serves a file from the embedded static directory using http.FileServer
func (s *Server) serveStaticFile(w http.ResponseWriter, r *http.Request, filename string) {
	// Create a sub-filesystem from the static directory
	staticSubFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Check if file exists
	if _, err := staticSubFS.Open(filename); err != nil {
		http.NotFound(w, r)
		return
	}

	// Create a temporary request with the filename as path
	tempReq := r.Clone(r.Context())
	tempReq.URL.Path = "/" + filename

	// Use http.FileServer to serve the file
	http.FileServer(http.FS(staticSubFS)).ServeHTTP(w, tempReq)
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
	var publicKey, email string
	var expires time.Time
	err := s.db.QueryRow(`
		SELECT public_key, user_email, expires_at
		FROM pending_ssh_keys
		WHERE token = ?`,
		token).Scan(&publicKey, &email, &expires)

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
	// Use public key preview for verification display
	publicKeyPreview := publicKey
	if len(publicKey) > 32 {
		publicKeyPreview = publicKey[:32] + "..."
	}

	data := struct {
		Email     string
		PublicKey string
		Token     string
	}{
		Email:     email,
		PublicKey: publicKeyPreview,
		Token:     token,
	}

	s.renderTemplate(w, "device-verification.html", data)
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
	var publicKey, email string
	var expires time.Time
	err := s.db.QueryRow(`
		SELECT public_key, user_email, expires_at
		FROM pending_ssh_keys
		WHERE token = ?`,
		token).Scan(&publicKey, &email, &expires)

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
		INSERT INTO ssh_keys (user_id, public_key)
		VALUES ((SELECT user_id FROM users WHERE email = ?), ?)
		ON CONFLICT(public_key) DO NOTHING`,
		email, publicKey)
	if err != nil {
		slog.Error("Failed to add SSH key", "error", err)
		http.Error(w, "Failed to verify device", http.StatusInternalServerError)
		return
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

	// Send success response with public key preview for verification
	// Use first 32 characters of public key as display identifier
	publicKeyPreview := publicKey
	if len(publicKey) > 32 {
		publicKeyPreview = publicKey[:32] + "..."
	}

	data := struct {
		PublicKey string
	}{
		PublicKey: publicKeyPreview,
	}

	s.renderTemplate(w, "device-verified.html", data)
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

	// Prepare template data
	data := struct {
		Token       string
		RedirectURL string
		ReturnHost  string
	}{
		Token:       token,
		RedirectURL: r.URL.Query().Get("redirect"),
		ReturnHost:  r.URL.Query().Get("return_host"),
	}

	// Render template
	s.renderTemplate(w, "email-verification-form.html", data)
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
		email := verification.Email

		// Create the user if they don't exist
		user, err := s.getUserByPublicKey(verification.PublicKey)
		if err != nil || user == nil {
			slog.Info("User doesn't exist, creating", "email", email)
			// User doesn't exist - create them with their alloc
			if err := s.createUserWithAlloc(verification.PublicKey, email); err != nil {
				slog.Error("Failed to create user with alloc during email verification", "error", err)
				s.emailVerificationsMu.Unlock()
				// Clean up pending registration on failure
				s.db.Exec("DELETE FROM pending_registrations WHERE token = ?", token)
				http.Error(w, "Failed to create user account", http.StatusInternalServerError)
				return
			}
			// Clean up pending registration on success
			s.db.Exec("DELETE FROM pending_registrations WHERE token = ?", token)
			slog.Info("Created new user", "email", email)
		} else {
			slog.Debug("User already exists", "email", email)
		}

		// Store the SSH key as verified
		publicKey := verification.PublicKey
		if publicKey != "" {
			_, err = s.db.Exec(`
				INSERT INTO ssh_keys (user_id, public_key)
				VALUES ((SELECT user_id FROM users WHERE email = ?), ?)
				ON CONFLICT(public_key) DO UPDATE SET user_id = (SELECT user_id FROM users WHERE email = ?)`,
				email, publicKey, email)
			if err != nil {
				slog.Error("Error storing SSH key during verification", "error", err)
			}
		}

		// Create HTTP auth cookie for this user
		var userID string
		err = s.db.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID)
		if err != nil {
			slog.Error("Failed to get user ID by email during SSH email verification", "error", err)
		} else {
			cookieValue, err := s.createAuthCookie(userID, r.Host)
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
		userID, err := s.validateEmailVerificationToken(token)
		if err != nil {
			slog.Error("Invalid email verification token", "error", err)
			http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(userID, r.Host)
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

		// Check if this is part of a web auth flow with redirect parameters (from form for POST)
		redirectURL := r.FormValue("redirect")
		returnHost := r.FormValue("return_host")
		if redirectURL != "" || returnHost != "" {
			// This is a web auth flow, perform redirect after authentication
			s.redirectAfterAuth(w, r, userID)
			return
		}
	}

	// Send success response (for SSH registrations or standalone verifications)
	s.renderTemplate(w, "email-verified.html", nil)
}

// handleAuth handles the main domain authentication flow
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if !s.quietMode {
		slog.Info("[REDIRECT] handleAuth called", "method", r.Method, "url", r.URL.String(), "host", r.Host)
	}
	// Check if user already has a valid exe.dev auth cookie
	cookie, err := r.Cookie("exe-auth")
	if err == nil && cookie.Value != "" {
		userID, err := s.validateAuthCookie(cookie.Value, r.Host)
		if err == nil {
			// User is already authenticated, handle redirect
			s.redirectAfterAuth(w, r, userID)
			return
		}
	}

	// Handle POST request (email submission)
	if r.Method == "POST" {
		s.handleAuthEmailSubmission(w, r)
		return
	}

	// Show authentication form with query parameters
	data := map[string]interface{}{
		"RedirectURL": r.URL.Query().Get("redirect"),
		"ReturnHost":  r.URL.Query().Get("return_host"),
	}
	s.renderTemplate(w, "auth-form.html", data)
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
	var userID string
	err := s.db.QueryRow("SELECT user_id FROM users WHERE email = ?", email).Scan(&userID)
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
		INSERT INTO email_verifications (token, email, user_id, expires_at)
		VALUES (?, ?, ?, ?)
	`, token, email, userID, time.Now().Add(24*time.Hour).Format(time.RFC3339))
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

	// Add redirect parameters to the verification URL if present (from form values for POST)
	if redirect := r.FormValue("redirect"); redirect != "" {
		verificationURL += "&redirect=" + url.QueryEscape(redirect)
	}
	if returnHost := r.FormValue("return_host"); returnHost != "" {
		verificationURL += "&return_host=" + url.QueryEscape(returnHost)
	}

	// Send email with proper verification URL that includes redirect params
	scheme2 := "http"
	if r.TLS != nil {
		scheme2 = "https"
	}
	verifyEmailURL := fmt.Sprintf("%s://%s/verify-email?token=%s", scheme2, r.Host, token)

	// Add redirect parameters to the verify-email URL if present (from form values for POST)
	// Both params needed: redirect=path, return_host=subdomain for cross-domain auth flow
	if redirect := r.FormValue("redirect"); redirect != "" {
		verifyEmailURL += "&redirect=" + url.QueryEscape(redirect)
	}
	if returnHost := r.FormValue("return_host"); returnHost != "" {
		verifyEmailURL += "&return_host=" + url.QueryEscape(returnHost)
	}

	// Send custom email for web auth with the proper URL
	subject := "Verify your email - exe.dev"
	body := fmt.Sprintf(`Hello,

Please click the link below to verify your email address:

%s

This link will expire in 24 hours.

Best regards,
The exe.dev team`, verifyEmailURL)

	err = s.sendEmail(email, subject, body)
	if err != nil {
		slog.Error("Failed to send auth email", "error", err)
		s.showAuthError(w, r, "Failed to send email. Please try again or contact support.")
		return
	}

	// Show success page
	var devURL string
	if s.devMode != "" && (strings.Contains(r.Host, "localhost") || strings.Contains(r.Host, "127.0.0.1")) {
		devURL = verifyEmailURL
	}
	s.showAuthEmailSent(w, r, email, devURL)
}

// showAuthError displays an authentication error page
func (s *Server) showAuthError(w http.ResponseWriter, r *http.Request, message string) {
	data := struct {
		Message     string
		QueryString string
	}{
		Message:     message,
		QueryString: r.URL.RawQuery,
	}

	w.WriteHeader(http.StatusBadRequest)
	s.renderTemplate(w, "auth-error.html", data)
}

// showAuthEmailSent displays the email sent confirmation page
func (s *Server) showAuthEmailSent(w http.ResponseWriter, r *http.Request, email string, devURL string) {
	data := struct {
		Email       string
		QueryString string
		DevURL      string // Development-only URL for easy testing
	}{
		Email:       email,
		QueryString: r.URL.RawQuery,
		DevURL:      devURL,
	}

	s.renderTemplate(w, "email-sent.html", data)
}

// handleAuthCallback handles authentication callbacks with magic tokens
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	var token string
	var userID string
	var err error

	// Check if this is an email verification request (/auth/verify?token=...)
	if strings.HasPrefix(r.URL.Path, "/auth/verify") {
		token = r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "Missing token parameter", http.StatusBadRequest)
			return
		}

		// Validate email verification token
		userID, err = s.validateEmailVerificationToken(token)
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
		userID, err = s.validateAuthToken(token, "")
		if err != nil {
			slog.Error("Invalid auth token in callback", "error", err)
			http.Error(w, "Invalid or expired authentication token", http.StatusUnauthorized)
			return
		}
	}

	// Create main domain auth cookie
	cookieValue, err := s.createAuthCookie(userID, r.Host)
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
	s.redirectAfterAuth(w, r, userID)
}

// handleAuthConfirm handles the interstitial confirmation page for magic auth
func (s *Server) handleAuthConfirm(w http.ResponseWriter, r *http.Request) {
	// Get magic secret from query parameter
	secret := r.URL.Query().Get("secret")
	if secret == "" {
		http.Error(w, "Missing secret parameter", http.StatusBadRequest)
		return
	}

	// Validate the magic secret WITHOUT consuming it (peek only)
	s.magicSecretsMu.RLock()
	magicSecret, exists := s.magicSecrets[secret]
	s.magicSecretsMu.RUnlock()

	if !exists {
		http.Error(w, "Invalid secret", http.StatusUnauthorized)
		return
	}

	// Check expiration
	if time.Now().After(magicSecret.ExpiresAt) {
		http.Error(w, "Secret expired", http.StatusUnauthorized)
		return
	}

	// Check for confirmation or cancellation
	action := r.URL.Query().Get("action")
	if action == "confirm" {
		// User confirmed, redirect to magic auth handler
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
			scheme, r.URL.Query().Get("return_host"), secret, url.QueryEscape(magicSecret.RedirectURL))
		http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
		return
	}
	if action == "cancel" {
		// User canceled, clean up the secret and redirect to main domain
		s.magicSecretsMu.Lock()
		delete(s.magicSecrets, secret)
		s.magicSecretsMu.Unlock()
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	// Show confirmation page
	returnHost := r.URL.Query().Get("return_host")
	if returnHost == "" {
		http.Error(w, "Missing return_host parameter", http.StatusBadRequest)
		return
	}

	// Extract hostname without port for display
	hostname := returnHost
	if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
		hostname = returnHost[:idx]
	}

	// Parse hostname to get machine name
	machineName, err := s.parseProxyHostname(hostname)
	if err != nil {
		http.Error(w, "Invalid hostname format", http.StatusBadRequest)
		return
	}

	// Prepare template data
	currentURL := r.URL.String()
	confirmURL := strings.ReplaceAll(currentURL, "action=", "unused=") + "&action=confirm"
	cancelURL := strings.ReplaceAll(currentURL, "action=", "unused=") + "&action=cancel"

	data := struct {
		TeamName   string
		SiteDomain string
		ConfirmURL string
		CancelURL  string
	}{
		TeamName:   machineName,
		SiteDomain: hostname,
		ConfirmURL: confirmURL,
		CancelURL:  cancelURL,
	}

	s.renderTemplate(w, "login-confirmation.html", data)
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
	var userID string
	var email string
	var expiresAt string

	// Get verification info and return user_id directly
	err := s.db.QueryRow(`
		SELECT e.user_id, e.email, e.expires_at
		FROM email_verifications e
		WHERE e.token = ?
	`, token).Scan(&userID, &email, &expiresAt)
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

	return userID, nil
}

// validateEmailVerificationToken validates an email verification token, consumes it, and returns the user ID
func (s *Server) validateEmailVerificationToken(token string) (string, error) {
	userID, err := s.checkEmailVerificationToken(token)
	if err != nil {
		return "", err
	}

	// Clean up used token
	s.db.Exec("DELETE FROM email_verifications WHERE token = ?", token)

	return userID, nil
}

// Helper functions for authentication and reverse proxy

// createAuthCookie creates a new authentication cookie for the user
func (s *Server) createAuthCookie(userID, domain string) (string, error) {
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
		INSERT INTO auth_cookies (cookie_value, user_id, domain, expires_at)
		VALUES (?, ?, ?, ?)
	`, cookieValue, userID, getDomain(domain), expiresAt.Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("failed to store auth cookie: %w", err)
	}

	return cookieValue, nil
}

// validateAuthCookie validates an authentication cookie and returns the user_id
func (s *Server) validateAuthCookie(cookieValue, domain string) (string, error) {
	var userID string
	var expiresAt string

	// Get auth cookie info
	err := s.db.QueryRow(`
		SELECT ac.user_id, ac.expires_at
		FROM auth_cookies ac
		WHERE ac.cookie_value = ? AND ac.domain = ?
	`, cookieValue, getDomain(domain)).Scan(&userID, &expiresAt)
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

	return userID, nil
}

// createMagicSecret creates a temporary magic secret for proxy authentication
func (s *Server) createMagicSecret(userID, machineName, redirectURL string) (string, error) {
	// Generate a random secret
	secret := cryptorand.Text()

	// Clean up expired secrets while we're here
	s.cleanupExpiredMagicSecrets()

	// Store in memory with 2-minute expiration
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	s.magicSecrets[secret] = &MagicSecret{
		UserID:      userID,
		MachineName: machineName,
		RedirectURL: redirectURL,
		ExpiresAt:   time.Now().Add(2 * time.Minute),
		CreatedAt:   time.Now(),
	}

	return secret, nil
}

// validateMagicSecret validates and consumes a magic secret
func (s *Server) validateMagicSecret(secret string) (*MagicSecret, error) {
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	magicSecret, exists := s.magicSecrets[secret]
	if !exists {
		return nil, fmt.Errorf("invalid secret")
	}

	// Check expiration
	if time.Now().After(magicSecret.ExpiresAt) {
		// Clean up expired secret
		delete(s.magicSecrets, secret)
		return nil, fmt.Errorf("secret expired")
	}

	// Secret is valid, consume it (single use)
	result := *magicSecret // Copy the struct
	delete(s.magicSecrets, secret)

	return &result, nil
}

// cleanupExpiredMagicSecrets removes expired magic secrets from memory
func (s *Server) cleanupExpiredMagicSecrets() {
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	now := time.Now()
	for secret, magicSecret := range s.magicSecrets {
		if now.After(magicSecret.ExpiresAt) {
			delete(s.magicSecrets, secret)
		}
	}
}

// validateAuthToken validates an authentication token and returns the user ID
func (s *Server) validateAuthToken(token, expectedSubdomain string) (string, error) {
	var userID string
	var subdomain sql.NullString
	var expiresAt string
	var usedAt sql.NullString

	// Get auth token info and return user_id directly
	err := s.db.QueryRow(`
		SELECT at.user_id, at.subdomain, at.expires_at, at.used_at
		FROM auth_tokens at
		WHERE at.token = ?
	`, token).Scan(&userID, &subdomain, &expiresAt, &usedAt)
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

	return userID, nil
}

// redirectAfterAuth handles redirecting user after successful authentication
func (s *Server) redirectAfterAuth(w http.ResponseWriter, r *http.Request, userID string) {
	// Check both URL query params (for GET) and form values (for POST)
	redirectURL := r.URL.Query().Get("redirect")
	if redirectURL == "" {
		redirectURL = r.FormValue("redirect")
	}
	returnHost := r.URL.Query().Get("return_host")
	if returnHost == "" {
		returnHost = r.FormValue("return_host")
	}

	if !s.quietMode {
		slog.Info("[REDIRECT] redirectAfterAuth called", "redirectURL", redirectURL, "returnHost", returnHost, "user_id", userID)
	}

	if returnHost != "" && redirectURL != "" {
		if s.isTerminalRequest(returnHost) {
			if !s.quietMode {
				slog.Info("[REDIRECT] redirectAfterAuth: detected terminal request", "returnHost", returnHost)
			}
			// Parse hostname to extract machine name
			hostname := returnHost
			if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
				hostname = returnHost[:idx]
			}

			machineName, err := s.parseTerminalHostname(hostname)
			if err != nil {
				slog.Error("Failed to parse terminal hostname", "hostname", hostname, "error", err)
				http.Error(w, "Invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the terminal subdomain
			secret, err := s.createMagicSecret(userID, machineName, redirectURL)
			if err != nil {
				slog.Error("Failed to create magic secret", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}

			// Redirect to terminal subdomain with magic secret
			magicURL := fmt.Sprintf("%s://%s/__exe.dev/auth?secret=%s&redirect=%s",
				getScheme(r), returnHost, secret, url.QueryEscape(redirectURL))
			http.Redirect(w, r, magicURL, http.StatusTemporaryRedirect)
			return
		} else if s.isProxyRequest(returnHost) {
			if !s.quietMode {
				slog.Info("[REDIRECT] redirectAfterAuth: detected proxy request", "returnHost", returnHost)
			}
			// Parse hostname to extract machine and team names
			hostname := returnHost
			if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
				hostname = returnHost[:idx]
			}

			machineName, err := s.parseProxyHostname(hostname)
			if err != nil {
				slog.Error("Failed to parse proxy hostname", "hostname", hostname, "error", err)
				http.Error(w, "Invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the proxy subdomain
			secret, err := s.createMagicSecret(userID, machineName, redirectURL)
			if err != nil {
				slog.Error("Failed to create magic secret", "error", err)
				http.Error(w, "Failed to create authentication secret", http.StatusInternalServerError)
				return
			}

			// Redirect to confirmation page with magic secret
			confirmURL := fmt.Sprintf("/auth/confirm?secret=%s&return_host=%s", secret, url.QueryEscape(returnHost))
			if !s.quietMode {
				slog.Info("[REDIRECT] redirectAfterAuth creating confirmation URL", "confirmURL", confirmURL)
			}
			http.Redirect(w, r, confirmURL, http.StatusTemporaryRedirect)
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

// handleUserDashboard renders the user dashboard page
func (s *Server) handleUserDashboard(w http.ResponseWriter, r *http.Request, userID string) {
	// Get user info
	var user User
	err := s.db.QueryRow(`
		SELECT user_id, email, created_at
		FROM users
		WHERE user_id = ?
	`, userID).Scan(&user.UserID, &user.Email, &user.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			slog.Error("Failed to get user info for dashboard", "error", err, "user_id", userID)
			http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		}
		return
	}

	// Get user's SSH keys
	sshKeys := []SSHKey{}
	rows, err := s.db.Query(`
		SELECT public_key, verified
		FROM ssh_keys
		WHERE user_id = ?
		ORDER BY added_at DESC
	`, user.UserID)
	if err != nil {
		slog.Error("Failed to get SSH keys for dashboard", "error", err, "email", user.Email)
	} else {
		defer rows.Close()
		for rows.Next() {
			var key SSHKey
			err := rows.Scan(&key.PublicKey, &key.Verified)
			if err != nil {
				slog.Error("Error scanning SSH key", "error", err)
				continue
			}
			sshKeys = append(sshKeys, key)
		}
	}

	// Get user's machines from all teams they belong to
	machines := []Machine{}
	machineRows, err := s.db.Query(`
		SELECT m.id, m.alloc_id, m.name, m.status, COALESCE(m.image, ''),
		       COALESCE(m.container_id, ''), m.created_by_user_id,
		       m.created_at, m.updated_at, m.last_started_at, m.docker_host
		FROM machines m
		JOIN allocs a ON m.alloc_id = a.alloc_id
		WHERE a.user_id = ?
		ORDER BY m.updated_at DESC
	`, user.UserID)
	if err != nil {
		slog.Error("Failed to get machines for dashboard", "error", err, "user_id", userID)
	} else {
		defer machineRows.Close()
		for machineRows.Next() {
			var machine Machine
			var containerID, image, dockerHost sql.NullString
			var lastStartedAt sql.NullTime
			err := machineRows.Scan(&machine.ID, &machine.AllocID, &machine.Name,
				&machine.Status, &image, &containerID, &machine.CreatedByUserID,
				&machine.CreatedAt, &machine.UpdatedAt, &lastStartedAt, &dockerHost)
			if err != nil {
				slog.Error("Error scanning machine", "error", err)
				continue
			}
			if containerID.Valid {
				machine.ContainerID = &containerID.String
			}
			if image.Valid {
				machine.Image = image.String
			}
			if lastStartedAt.Valid {
				machine.LastStartedAt = &lastStartedAt.Time
			}
			if dockerHost.Valid {
				machine.DockerHost = &dockerHost.String
			}
			machines = append(machines, machine)
		}
	}

	// Prepare template data
	data := UserPageData{
		User:     user,
		SSHKeys:  sshKeys,
		Machines: machines,
	}

	// Render template
	s.renderTemplate(w, "user.html", data)
}

// handleLogout logs out the user by clearing their auth cookie
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Get the current user's ID from the main auth cookie
	var userID string
	cookie, err := r.Cookie("exe-auth")
	if err == nil && cookie.Value != "" {
		// Get the user ID before deleting
		userID, _ = s.validateAuthCookie(cookie.Value, r.Host)
	}

	// Clear ALL auth cookies for this user across all domains
	if userID != "" {
		_, err := s.db.Exec(`
			DELETE FROM auth_cookies
			WHERE user_id = ?
		`, userID)
		if err != nil {
			slog.Error("Failed to delete user's auth cookies from database", "error", err)
		}
	}

	// Clear both cookies in the browser
	http.SetCookie(w, &http.Cookie{
		Name:     "exe-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "exe-proxy-auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Redirect to home page
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

// userHasTeamAccess checks if a user has access to a team
func (s *Server) userHasTeamAccess(userID, teamName string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM team_members tm
		WHERE tm.user_id = ? AND tm.team_name = ?
	`, userID, teamName).Scan(&count)
	if err != nil {
		return false, err
	}

	return count > 0, nil
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

// FindMachineByNameForUserAndIP returns the machine associated with the userID, ip pair.
// If the user is somehow associated with more than a single team, this function will return
// nil, due to the ambiuguity of which of those teams' maps of IP address->machine to
// index with the supplied ip address.
// See https://docs.google.com/document/d/1WF_Z4F4_viN5Abhxhus6IwOC33yClt7BViA3kNA4GPE/edit?tab=t.0
// for an more detailed explanation of why this is the case.
func (s *Server) FindMachineByNameForUserAndIP(userID, ip string) *Machine {
	slog.Debug("FindMachineByNameForUserAndIP", "user_id", userID, "ip", ip)

	// Get user's alloc
	alloc, err := s.getUserAlloc(userID)
	if err != nil || alloc == nil {
		slog.Debug("FindMachineByNameForUserAndIP no alloc found", "user_id", userID)
		return nil
	}

	if s.ipAllocator == nil {
		return nil
	}

	machineName, found := s.ipAllocator.LookupMachine(alloc.AllocID, ip)
	if !found {
		slog.Debug("FindMachineByNameForUserAndIP machine not found for alloc", "alloc", alloc.AllocID, "ip", ip)
		return nil
	}

	slog.Debug("FindMachineByNameForUserAndIP found machine", "machine", machineName)
	machine, err := s.getMachineByName(machineName)
	if err == nil {
		return machine
	}

	return nil
}

// findMachineByNameForUser finds a machine by name that the user has access to
func (s *Server) FindMachineByNameForUser(userID, machineName string) *Machine {
	slog.Debug("FindMachineByNameForUser", "user_id", userID, "machine_name", machineName)

	// Machine names are now globally unique, no team prefix
	if strings.Contains(machineName, ".") {
		// Legacy format not supported
		return nil
	}

	// Get user's alloc to verify access
	alloc, err := s.getUserAlloc(userID)
	if err != nil || alloc == nil {
		slog.Debug("FindMachineByNameForUser no alloc found", "user_id", userID)
		return nil
	}

	// Check if machine exists and belongs to user's alloc
	machine, err := s.getMachineByName(machineName)
	if err != nil {
		slog.Debug("Machine not found", "machine", machineName, "error", err)
		return nil
	}

	// Verify the machine belongs to the user's alloc
	if machine.AllocID != alloc.AllocID {
		slog.Debug("Machine belongs to different alloc", "machine", machineName, "machine_alloc", machine.AllocID, "user_alloc", alloc.AllocID)
		return nil
	}

	return machine
}

// handleListUserAlloc shows the user's allocation info
func (s *Server) handleListUserAlloc(channel *sshbuf.Channel, publicKey string) {
	user, err := s.getUserByPublicKey(publicKey)
	if err != nil || user == nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError retrieving user: %v\033[0m\r\n", err)))
		return
	}

	alloc, err := s.getUserAlloc(user.UserID)
	if err != nil {
		channel.Write([]byte(fmt.Sprintf("\033[1;31mError retrieving allocation: %v\033[0m\r\n", err)))
		return
	}

	if alloc == nil {
		channel.Write([]byte("\033[1;33mNo allocation found\033[0m\r\n"))
		return
	}

	channel.Write([]byte("\033[1;36m═══ Your Allocation ═══\033[0m\r\n\r\n"))
	channel.Write([]byte(fmt.Sprintf("  Type: \033[1m%s\033[0m\r\n", alloc.AllocType)))
	channel.Write([]byte(fmt.Sprintf("  Region: \033[1m%s\033[0m\r\n", alloc.Region)))
	channel.Write([]byte(fmt.Sprintf("  Created: %s\r\n", alloc.CreatedAt.Format("Jan 2, 2006"))))
	channel.Write([]byte(fmt.Sprintf("  Machines: \033[1;36m<name>@exe.dev\033[0m\r\n")))

	// List machines in this alloc
	machines, err := s.getMachinesForAlloc(alloc.AllocID)
	if err == nil && len(machines) > 0 {
		channel.Write([]byte("\r\n  \033[1mMachines:\033[0m\r\n"))
		for _, m := range machines {
			statusColor := "\033[1;31m" // red for stopped
			if m.Status == "running" {
				statusColor = "\033[1;32m" // green for running
			}
			channel.Write([]byte(fmt.Sprintf("    - %s %s[%s]\033[0m\r\n", m.Name, statusColor, m.Status)))
		}
	}

	channel.Write([]byte("\r\n\033[2mTo access a machine: ssh <machine>@exe.dev\033[0m\r\n"))
}

// createUserSession creates a new user session for a channel
func (s *Server) createUserSession(channel *sshbuf.Channel, userID string, email, teamName, publicKey string, isAdmin bool) {
	session := &UserSession{
		UserID:    userID,
		Email:     email,
		TeamName:  teamName,
		IsAdmin:   isAdmin,
		PublicKey: publicKey,
		CreatedAt: time.Now(),
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

// handleListCommand lists user's machines
func generateRandomContainerName() string {
	words := []string{
		// NATO phonetic + military
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet",
		"kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango",
		"uniform", "victor", "whiskey", "xray", "yankee", "zulu",

		// WWII / older phonetics
		"able", "baker", "dog", "easy", "fox", "george", "how", "item", "jig", "king", "love", "nan",
		"oboe", "prep", "queen", "roger", "sugar", "tare", "uncle", "victory", "william", "xray",
		"yoke", "zebra",

		// Nature & elements
		"earth", "wind", "fire", "water", "stone", "tree", "river", "mountain", "cloud", "storm",
		"rain", "snow", "ice", "sun", "moon", "star", "comet", "nova", "eclipse", "ocean", "tide",

		// Animals
		"lion", "tiger", "bear", "wolf", "eagle", "hawk", "falcon", "owl", "otter", "seal", "whale",
		"shark", "orca", "salmon", "trout", "crane", "heron", "sparrow", "crow", "raven", "fox",
		"badger", "ferret", "mole", "lynx", "cougar", "panther", "cobra", "viper", "python", "gecko",

		// Colors
		"red", "blue", "green", "yellow", "purple", "violet", "indigo", "orange", "white", "black",
		"gray", "silver", "gold", "bronze", "scarlet", "crimson", "azure", "emerald", "jade", "amber",

		// Space & science
		"asteroid", "nebula", "quasar", "galaxy", "pulsar", "orbit", "photon", "quantum", "fusion",
		"plasma", "nova", "eclipse", "meteor", "cosmos", "ion", "neutron", "proton", "electron",

		// Tools, tech & retro computing
		"format", "fdisk", "edit", "tree", "paint", "minesweeper", "fortune", "lynx", "telnet",
		"gopher", "ping", "traceroute", "router", "switch", "ethernet", "socket", "kernel", "patch",
		"compile", "linker", "loader", "buffer", "cache", "cookie", "daemon", "kernel", "driver",

		// Random objects
		"anchor", "beacon", "bridge", "compass", "harbor", "island", "lagoon", "mesa", "valley",
		"desert", "canyon", "fjord", "reef", "delta", "dune", "grove", "peak", "ridge", "plateau",

		// Misc “fun” filler
		"sphinx", "obelisk", "phoenix", "griffin", "hydra", "kraken", "unicorn", "pegasus", "chimera",
		"golem", "djinn", "troll", "sprite", "fairy", "dragon", "wyvern", "cyclops", "satyr", "nymph",
		"centaur", "minotaur", "harpy", "basilisk", "leviathan",
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
func (s *Server) formatSSHConnectionInfo(allocID, machineName string) string {
	if s.ipAllocator != nil {
		allocation, err := s.ipAllocator.Allocate(allocID, machineName)
		if err == nil && allocation != nil {
			return fmt.Sprintf("ssh -p 2222 -o ConnectTimeout=1 %s", allocation.Hostname)
		}
	}
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

// denylistedMachineNames contains common computer-related five+ letter words that are not allowed as machine names
var denylistedMachineNames = map[string]bool{
	"teams": true,
	"abort": true, "admin": true, "allow": true, "array": true, "async": true,
	"audit": true, "block": true, "board": true, "boost": true, "break": true,
	"build": true, "bytes": true, "cable": true, "cache": true, "catch": true,
	"chain": true, "check": true, "chips": true, "class": true, "clock": true,
	"cloud": true, "codec": true, "codes": true, "const": true, "cores": true,
	"crawl": true, "crypt": true, "debug": true, "drive": true, "email": true,
	"entry": true, "error": true, "event": true, "fetch": true, "fiber": true,
	"field": true, "flash": true, "frame": true, "games": true, "grant": true,
	"guard": true, "guest": true, "https": true, "image": true, "index": true,
	"input": true, "laser": true, "links": true, "logic": true, "login": true,
	"macro": true, "match": true, "merge": true, "modem": true, "mount": true,
	"nodes": true, "parse": true, "paste": true, "patch": true, "pixel": true,
	"ports": true, "power": true, "print": true, "proxy": true, "query": true,
	"radio": true, "regex": true, "reset": true, "route": true, "scope": true,
	"serve": true, "setup": true, "share": true, "shell": true, "solid": true,
	"sound": true, "speed": true, "spell": true, "stack": true, "start": true,
	"store": true, "style": true, "table": true, "theme": true, "throw": true,
	"timer": true, "token": true, "tower": true, "trace": true, "trash": true,
	"trust": true, "users": true, "video": true, "virus": true, "watts": true,
	"agent": true, "agents": true, "claude": true, "openai": true, "jules": true,
	"cursor": true, "cline": true, "qwencode": true, "claudecode": true,
	"editor": true, "terminal": true, "sketch": true, "webterm": true,
	"daemon": true, "server": true, "client": true, "remote": true, "session": true,
	"tunnel": true, "bridge": true, "exedev": true,
	"gateway": true, "router": true, "switch": true, "firewall": true, "cluster": true,
	"docker": true, "podman": true, "kubernetes": true, "helm": true, "ansible": true,
	"terraform": true, "vagrant": true, "puppet": true, "consul": true, "vault": true,
	"nomad": true, "etcd": true, "redis": true, "nginx": true, "apache": true,
	"traefik": true, "envoy": true, "istio": true, "linkerd": true, "cilium": true,
	"weave": true, "calico": true, "flannel": true, "zookeeper": true,
	"kafka": true, "rabbit": true, "zeromq": true,
	"websocket": true, "telnet": true, "rsync": true, "netcat": true,
	"socat": true, "screen": true, "byobu": true, "mosh": true,
	"tmate": true, "gotty": true, "ttyd": true, "shellinabox": true, "wetty": true,
	"xterm": true, "xtermjs": true, "monaco": true, "codemirror": true, "ace": true,
	"vscode": true, "neovim": true, "emacs": true, "sublime": true, "atom": true,
	"bracket": true, "theia": true, "gitpod": true, "codespace": true, "replit": true,
	"sandbox": true, "container": true, "chroot": true, "namespace": true, "cgroup": true,
	"systemd": true, "upstart": true, "supervisor": true, "monit": true, "circus": true,
	"gunicorn": true, "uwsgi": true, "passenger": true, "puma": true, "unicorn": true,
	"process": true, "thread": true, "worker": true, "queue": true, "scheduler": true,
	"crontab": true, "systemctl": true, "service": true, "socket": true, "target": true,
	"volume": true, "overlay": true, "union": true, "btrfs": true,
	"iptables": true, "netfilter": true, "fail2ban": true, "selinux": true,
	"apparmor": true, "grsec": true, "hardening": true, "syslog": true,
	"journald": true, "rsyslog": true, "fluentd": true, "logstash": true, "filebeat": true,
	"prometheus": true, "grafana": true, "influx": true, "telegraf": true, "collectd": true,
	"nagios": true, "zabbix": true, "sensu": true, "datadog": true, "newrelic": true,
	"splunk": true, "elastic": true, "kibana": true, "jaeger": true, "zipkin": true,
	"opentracing": true, "honeycomb": true, "lightstep": true, "wavefront": true, "signalfx": true,
}

// isValidMachineName validates machine name format
func (s *Server) isValidMachineName(name string) bool {
	// Must be at least 5 characters and at most 32 characters
	if len(name) < 5 || len(name) > 32 {
		return false
	}

	// Check if name is in denylist
	withoutHyphens := strings.ReplaceAll(name, "-", "")
	if denylistedMachineNames[withoutHyphens] {
		return false
	}

	// Check pattern: starts with letter, contains only lowercase letters/numbers/hyphens, no consecutive hyphens, doesn't end with hyphen
	matched, _ := regexp.MatchString(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`, name)
	return matched
}

// handleCreateCommandWithStdin creates a new machine with support for stdin Dockerfile and flag-based parameters
func (s *Server) isValidContainerName(name string) bool {
	return s.isValidTeamName(name) // Reuse team name validation
}

// getDefaultRoutesJSON returns the default routes as a JSON string
func getDefaultRoutesJSON() string {
	var machine Machine
	routes := machine.getDefaultRoutes()
	data, err := json.Marshal(routes)
	if err != nil {
		log.Fatalf("Failed to marshal default routes: %v", err)
	}
	return string(data)
}

// createMachine stores machine info in database
func (s *Server) createMachine(userID, allocID, name, containerID, image string) error {
	// Validate machine name
	if !s.isValidMachineName(name) {
		return fmt.Errorf("invalid machine name: %s", name)
	}

	routes := getDefaultRoutesJSON()
	_, err := s.db.Exec(`
		INSERT INTO machines (alloc_id, name, status, image, container_id, created_by_user_id, routes,
		                     ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key,
		                     ssh_host_certificate, ssh_client_private_key, ssh_port)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, allocID, name, "pending", image, containerID, userID, routes,
		"test-identity-key", "test-authorized-keys", "test-ca-key",
		"test-host-cert", "test-client-key", 2222)
	return err
}

// createMachineWithDockerHost stores machine info including docker host in database
func (s *Server) createMachineWithDockerHost(userID, allocID, name, containerID, image, dockerHost string) error {
	// Validate machine name
	if !s.isValidMachineName(name) {
		return fmt.Errorf("invalid machine name: %s", name)
	}

	routes := getDefaultRoutesJSON()
	_, err := s.db.Exec(`
		INSERT INTO machines (alloc_id, name, status, image, container_id, created_by_user_id, docker_host, routes,
		                     ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key,
		                     ssh_host_certificate, ssh_client_private_key, ssh_port)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, allocID, name, "pending", image, containerID, userID, dockerHost, routes,
		"test-identity-key", "test-authorized-keys", "test-ca-key",
		"test-host-cert", "test-client-key", 2222)
	return err
}

// createMachineWithSSH stores machine info including SSH keys in database
func (s *Server) createMachineWithSSH(userID, allocID, name, containerID, image string, sshKeys *container.ContainerSSHKeys, sshPort int) error {
	// Validate machine name
	if !s.isValidMachineName(name) {
		return fmt.Errorf("invalid machine name: %s", name)
	}

	routes := getDefaultRoutesJSON()
	_, err := s.db.Exec(`
		INSERT INTO machines (
			alloc_id, name, status, image, container_id, created_by_user_id,
			ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key,
			ssh_host_certificate, ssh_client_private_key, ssh_port, routes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, allocID, name, "running", image, containerID, userID,
		sshKeys.ServerIdentityKey, sshKeys.AuthorizedKeys, sshKeys.CAPublicKey,
		sshKeys.HostCertificate, sshKeys.ClientPrivateKey, sshPort, routes)
	return err
}

// createMachineWithSSHAndDockerHost stores machine info including SSH keys and docker host in database
func (s *Server) createMachineWithSSHAndDockerHost(userID, allocID, name, containerID, image, dockerHost string, sshKeys *container.ContainerSSHKeys, sshPort int) error {
	// Validate machine name
	if !s.isValidMachineName(name) {
		return fmt.Errorf("invalid machine name: %s", name)
	}

	routes := getDefaultRoutesJSON()
	_, err := s.db.Exec(`
		INSERT INTO machines (
			alloc_id, name, status, image, container_id, created_by_user_id,
			ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key,
			ssh_host_certificate, ssh_client_private_key, ssh_port, docker_host, routes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, allocID, name, "running", image, containerID, userID,
		sshKeys.ServerIdentityKey, sshKeys.AuthorizedKeys, sshKeys.CAPublicKey,
		sshKeys.HostCertificate, sshKeys.ClientPrivateKey, sshPort, dockerHost, routes)
	if err != nil {
		return err
	}

	// Register machine with IP allocation strategy if enabled
	if s.ipAllocator != nil {
		if _, allocErr := s.ipAllocator.Allocate(allocID, name); allocErr != nil {
			slog.Warn("Failed to register machine with IP allocation", "alloc", allocID, "machine", name, "error", allocErr)
			// Don't fail the whole operation if IP allocation fails
		}
	}

	return nil
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
func (s *Server) getMachineByName(name string) (*Machine, error) {
	var machine Machine
	err := s.db.QueryRow(`
		SELECT id, alloc_id, name, status, image, container_id, created_by_user_id, created_at, updated_at, last_started_at, docker_host, routes,
		       ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, ssh_host_certificate, ssh_client_private_key, ssh_port
		FROM machines
		WHERE name = ?
	`, name).Scan(
		&machine.ID, &machine.AllocID, &machine.Name, &machine.Status,
		&machine.Image, &machine.ContainerID, &machine.CreatedByUserID,
		&machine.CreatedAt, &machine.UpdatedAt, &machine.LastStartedAt, &machine.DockerHost, &machine.Routes,
		&machine.SSHServerIdentityKey, &machine.SSHAuthorizedKeys, &machine.SSHCAPublicKey, &machine.SSHHostCertificate, &machine.SSHClientPrivateKey, &machine.SSHPort,
	)
	if err != nil {
		return nil, err
	}
	return &machine, nil
}

// getMachinesForAlloc gets all machines for an allocation
func (s *Server) getMachinesForAlloc(allocID string) ([]*Machine, error) {
	rows, err := s.db.Query(`
		SELECT id, alloc_id, name, status, image, container_id, created_by_user_id, created_at, updated_at, last_started_at, docker_host, routes,
		       ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, ssh_host_certificate, ssh_client_private_key, ssh_port
		FROM machines
		WHERE alloc_id = ?
		ORDER BY name
	`, allocID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var machines []*Machine
	for rows.Next() {
		var m Machine
		err := rows.Scan(
			&m.ID, &m.AllocID, &m.Name, &m.Status, &m.Image, &m.ContainerID, &m.CreatedByUserID,
			&m.CreatedAt, &m.UpdatedAt, &m.LastStartedAt, &m.DockerHost, &m.Routes,
			&m.SSHServerIdentityKey, &m.SSHAuthorizedKeys, &m.SSHCAPublicKey, &m.SSHHostCertificate, &m.SSHClientPrivateKey, &m.SSHPort,
		)
		if err != nil {
			return nil, err
		}
		machines = append(machines, &m)
	}
	return machines, nil
}

// getMachinesForUser gets all machines for a user across all their allocs
func (s *Server) getMachinesForUser(userID string) ([]*Machine, error) {
	rows, err := s.db.Query(`
		SELECT m.id, m.alloc_id, m.name, m.status, m.image, m.container_id, m.created_by_user_id,
		       m.created_at, m.updated_at, m.last_started_at, m.docker_host, m.routes,
		       m.ssh_server_identity_key, m.ssh_authorized_keys, m.ssh_ca_public_key,
		       m.ssh_host_certificate, m.ssh_client_private_key, m.ssh_port
		FROM machines m
		JOIN allocs a ON m.alloc_id = a.alloc_id
		WHERE a.user_id = ?
		ORDER BY m.name
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var machines []*Machine
	for rows.Next() {
		var m Machine
		err := rows.Scan(
			&m.ID, &m.AllocID, &m.Name, &m.Status, &m.Image, &m.ContainerID, &m.CreatedByUserID,
			&m.CreatedAt, &m.UpdatedAt, &m.LastStartedAt, &m.DockerHost, &m.Routes,
			&m.SSHServerIdentityKey, &m.SSHAuthorizedKeys, &m.SSHCAPublicKey, &m.SSHHostCertificate, &m.SSHClientPrivateKey, &m.SSHPort,
		)
		if err != nil {
			return nil, err
		}
		machines = append(machines, &m)
	}
	return machines, nil
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

	// Create a cancellable context for startup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start HTTP server in a goroutine if configured
	if s.httpAddr != "" {
		go func() {
			if !s.quietMode {
				slog.Info("HTTP server starting", "addr", s.httpAddr)
			}
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP server startup failed", "error", err)
				cancel()
			}
		}()
	}

	// Start HTTPS server in a goroutine if configured
	if s.httpsAddr != "" {
		go func() {
			slog.Info("HTTPS server starting with Let's Encrypt for exe.dev", "addr", s.httpsAddr)
			if err := s.httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTPS server startup failed", "error", err)
				cancel()
			}
		}()

		// Start autocert HTTP handler for ACME challenges on port 80 (only for regular autocert)
		// Note: DNS challenge for wildcard certs doesn't need HTTP-01 challenge handler
		if s.certManager != nil {
			go func() {
				slog.Info("Starting autocert HTTP server on :80 for ACME challenges")
				if err := http.ListenAndServe(":80", s.certManager.HTTPHandler(nil)); err != nil {
					slog.Error("Autocert HTTP server startup failed", "error", err)
					cancel()
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
			slog.Error("Piper plugin server startup failed", "error", err)
			cancel()
		}
	}()

	// In dev mode, automatically start sshpiper if not already running
	if s.devMode != "" {
		go s.autoStartSSHPiper(ctx)
	}

	// Start SSH server in a goroutine
	go func() {
		sshServer := NewSSHServer(s)
		if err := sshServer.Start(s.sshAddr); err != nil {
			slog.Error("SSH server startup failed", "error", err)
			cancel()
		}
	}()

	// Print SSH connection command for local dev mode
	if s.devMode == "local" {
		// Extract just the port number from the address
		sshPort := strings.TrimPrefix(s.sshAddr, ":")
		slog.Info("SSH server started in local dev mode. Connect with:")
		slog.Info("  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %s localhost", "port", sshPort)
	}

	// Register all existing machines with IP allocation strategy
	if s.ipAllocator != nil {
		slog.Info("Starting IP allocator...")
		// Start IP allocation strategy if enabled
		if err := s.ipAllocator.Start(); err != nil {
			return fmt.Errorf("failed to start IP allocator: %v", err)
		}
		if err := s.allocateIPsForExistingMachines(); err != nil {
			slog.Warn("Failed to register existing machines with ipAllocator", "error", err)
		}
	}

	// Wait for interrupt signal or startup failure
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigChan:
		slog.Info("Shutting down servers...")
		return s.Stop()
	case <-ctx.Done():
		slog.Error("Server startup failed, shutting down")
		s.Stop()
		return fmt.Errorf("server startup failed")
	}
}

// autoStartSSHPiper automatically starts sshpiper.sh in dev mode if port 2222 isn't listening
func (s *Server) autoStartSSHPiper(ctx context.Context) {
	// Check if sshpiper is already running on port 2222
	if s.isPortListening("localhost:2222") {
		slog.Info("sshpiper already running on port 2222")
		return
	}

	// First, wait for the piper plugin to be ready (listening on port 2224)
	if !s.waitForPort(ctx, "localhost:2224", 30*time.Second) {
		slog.Error("Timed out waiting for piper plugin to start on port 2224")
		return
	}

	// Start sshpiper.sh
	slog.Info("Starting sshpiper.sh automatically in dev mode")

	cmd := exec.CommandContext(ctx, "./sshpiper.sh")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		slog.Error("Failed to start sshpiper.sh", "error", err)
		return
	}

	// Wait for the process in a separate goroutine
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Error("sshpiper.sh exited with error", "error", err)
		} else {
			slog.Info("sshpiper.sh exited normally")
		}
	}()
}

// waitForPort waits for a port to become available with a timeout
func (s *Server) waitForPort(ctx context.Context, address string, timeout time.Duration) bool {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return false
		case <-ticker.C:
			if s.isPortListening(address) {
				return true
			}
		}
	}
}

// isPortListening checks if a port is currently listening
func (s *Server) isPortListening(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Database helper methods

// getEmailBySSHKey checks if an SSH key is registered and returns the associated email
func (s *Server) GetEmailBySSHKey(publicKeyStr string) (email string, verified bool, err error) {
	// Check if key exists in ssh_keys (all keys there are verified)
	err = s.db.QueryRow(`
		SELECT u.email
		FROM ssh_keys s
		JOIN users u ON s.user_id = u.user_id
		WHERE s.public_key = ?`,
		publicKeyStr).Scan(&email)

	if err == sql.ErrNoRows {
		// Check if key exists in pending_ssh_keys (unverified)
		err = s.db.QueryRow(`
			SELECT user_email
			FROM pending_ssh_keys
			WHERE public_key = ?`,
			publicKeyStr).Scan(&email)
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return email, false, err // Key exists in pending_ssh_keys, so not verified
	}
	return email, true, err // Key exists in ssh_keys, so verified
}

// getUserByPublicKey retrieves a user by their SSH public key
func (s *Server) getUserByPublicKey(publicKeyStr string) (*User, error) {
	var user User

	// Find user by their SSH public key
	err := s.db.QueryRow(`
		SELECT u.user_id, u.email, u.created_at
		FROM users u
		JOIN ssh_keys s ON u.user_id = s.user_id
		WHERE s.public_key = ?`,
		publicKeyStr).Scan(&user.UserID, &user.Email, &user.CreatedAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &user, err
}

// getUserTeams returns all teams a user belongs to

// createUserWithAlloc creates a new user with their resource allocation
func (s *Server) createUserWithAlloc(publicKey, email string) error {
	// Start a transaction
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Generate user ID
	userID, err := generateUserID()
	if err != nil {
		return err
	}

	// Create user
	_, err = tx.Exec(`
		INSERT INTO users (user_id, email)
		VALUES (?, ?)`,
		userID, email)
	if err != nil {
		return err
	}

	// Add the SSH key to ssh_keys table
	_, err = tx.Exec(`
		INSERT INTO ssh_keys (user_id, public_key)
		VALUES (?, ?)`,
		userID, publicKey)
	if err != nil {
		return err
	}

	// Generate alloc ID
	allocID, err := generateAllocID()
	if err != nil {
		return err
	}

	// Select a docker host for this alloc
	dockerHost := s.selectDockerHostForNewAlloc()

	// Create alloc for the user
	_, err = tx.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, billing_email)
		VALUES (?, ?, ?, ?, ?, ?)`,
		allocID, userID, AllocTypeMedium, RegionAWSUSWest2, dockerHost, email)
	if err != nil {
		return err
	}

	// Commit transaction
	return tx.Commit()
}

// getUserAlloc gets the alloc for a user (creates one if it doesn't exist)
func (s *Server) getUserAlloc(userID string) (*Alloc, error) {
	var alloc Alloc
	err := s.db.QueryRow(`
		SELECT alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email
		FROM allocs
		WHERE user_id = ?
		LIMIT 1`,
		userID).Scan(&alloc.AllocID, &alloc.UserID, &alloc.AllocType, &alloc.Region,
		&alloc.DockerHost, &alloc.CreatedAt, &alloc.StripeCustomerID, &alloc.BillingEmail)

	if err == sql.ErrNoRows {
		// User exists but has no alloc yet - create one
		allocID, err := generateAllocID()
		if err != nil {
			return nil, err
		}

		// Get user's email for billing
		var email string
		err = s.db.QueryRow(`SELECT email FROM users WHERE user_id = ?`, userID).Scan(&email)
		if err != nil {
			return nil, err
		}

		dockerHost := s.selectDockerHostForNewAlloc()

		_, err = s.db.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, billing_email)
			VALUES (?, ?, ?, ?, ?, ?)`,
			allocID, userID, AllocTypeMedium, RegionAWSUSWest2, dockerHost, email)
		if err != nil {
			return nil, err
		}

		return s.getUserAlloc(userID)
	}

	if err != nil {
		return nil, err
	}

	return &alloc, nil
}

// selectDockerHostForNewAlloc selects the best docker host for a new alloc
func (s *Server) selectDockerHostForNewAlloc() string {
	// For now, just use the first configured docker host
	// In the future, this could do load balancing
	// For now, just return empty string (local docker)
	// In future, could query the container manager for available hosts
	return "" // Local docker
}

// generateAllocID generates a unique allocation ID
func generateAllocID() (string, error) {
	// Generate a random ID with "alloc_" prefix
	bytes := make([]byte, 12)
	if _, err := cryptorand.Read(bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("alloc_%s", hex.EncodeToString(bytes)), nil
}

// createUser creates a new user (deprecated - use createUserWithAlloc)
func (s *Server) createUser(publicKey, email string) error {
	// Start a transaction
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Generate user ID
	userID, err := generateUserID()
	if err != nil {
		return err
	}

	// Create the user
	_, err = tx.Exec(`
		INSERT INTO users (user_id, email)
		VALUES (?, ?)`,
		userID, email)
	if err != nil {
		return err
	}

	// Add the SSH key to ssh_keys table if provided
	if publicKey != "" {
		_, err = tx.Exec(`
			INSERT INTO ssh_keys (user_id, public_key)
			VALUES (?, ?)`,
			userID, publicKey)
		if err != nil {
			return err
		}
	}

	// Create an alloc for the user
	allocID, err := generateAllocID()
	if err != nil {
		return err
	}

	dockerHost := s.selectDockerHostForNewAlloc()

	_, err = tx.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, billing_email)
		VALUES (?, ?, ?, ?, ?, ?)`,
		allocID, userID, AllocTypeMedium, RegionAWSUSWest2, dockerHost, email)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// Team-related functions have been removed as teams are no longer user-facing.
// Users now have direct resource allocations (allocs) instead.

// getDefaultTeamForUser - deprecated, teams no longer exist
func (s *Server) getDefaultTeamForUser(userID string) (string, error) {
	// Teams no longer exist - return empty string
	return "", nil
}

// setDefaultTeamForKey - deprecated, teams no longer exist
func (s *Server) setDefaultTeamForKey(userID, teamName string) error {
	// Teams no longer exist - no-op
	return nil
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
// and if so, returns the original user's public key and local IP address they
// connected to on sshpiperd by asking the piper plugin.
//
// EPHEMERAL PROXY KEY FLOW:
// 1. User connects to sshpiper with their key
// 2. Piper plugin generates ephemeral proxy key and stores mapping
// 3. Piper sends proxy key to exed for authentication
// 4. Exed recognizes proxy key and asks piper plugin for original user key
// 5. Exed authenticates based on original user key
func (s *Server) lookupEphemeralProxyKey(proxyKey ssh.PublicKey) ([]byte, string) {
	// Get the original user key from the piper plugin
	// The piper plugin is always configured when SSH proxy is enabled
	if s.piperPlugin == nil {
		slog.Error("Piper plugin not configured but proxy key received")
		return nil, ""
	}

	proxyFingerprint := s.GetPublicKeyFingerprint(proxyKey)
	slog.Debug("Looking up proxy key", "fingerprint", proxyFingerprint[:16])

	originalUserKey, localAddress, exists := s.piperPlugin.lookupOriginalUserKey(proxyFingerprint)
	if !exists {
		slog.Debug("Proxy key not found or expired", "fingerprint", proxyFingerprint[:16])
		return nil, "" // Not a proxy key or expired
	}

	slog.Debug("Found original user key for proxy key", "key_length", len(originalUserKey), "local_address", localAddress, "proxy_fingerprint", proxyFingerprint[:16])
	return originalUserKey, localAddress
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

	// Look up the user by their original public key
	email, verified, err := s.GetEmailBySSHKey(originalKeyStr)
	if err != nil {
		slog.Error("Database error checking SSH key", "fingerprint", originalFingerprint, "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has an alloc
		user, err := s.GetUserByEmail(email)
		if err != nil && err != sql.ErrNoRows {
			slog.Error("Database error getting user", "email", email, "error", err)
		}

		if user != nil {
			alloc, err := s.getUserAlloc(user.UserID)
			if err != nil && err != sql.ErrNoRows {
				slog.Error("Database error getting alloc for user", "userID", user.UserID, "error", err)
			}

			if alloc != nil {
				// User is fully registered with an alloc
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

// authenticateProxyUserWithLocalAddress authenticates a user through an ephemeral proxy connection
// and includes the local address for ipAllocator routing
func (s *Server) authenticateProxyUserWithLocalAddress(username string, originalUserKeyBytes []byte, localAddress string) (*ssh.Permissions, error) {
	// Check if this is an IP allocation strategy-based machine access request
	// TODO: clean up the "127.0.0.1" check here - it's only there to distinguish between exe.local and *.*.exe.local, and Server
	// shouldn't have to know anything about that.
	if s.ipAllocator == nil || localAddress == "" || localAddress == "127.0.0.1" {
		// Fall back to normal proxy authentication
		return s.authenticateProxyUser(username, originalUserKeyBytes)
	}

	// Parse the local address to see if it's a machine IP and try to route it based on ipAllocator
	localIP := net.ParseIP(localAddress)
	if localIP == nil {
		return nil, fmt.Errorf("could't parse IP address: %q", localAddress)
	}

	// Get user's alloc to check machine IPs
	originalUserKey, _, _, _, err := ssh.ParseAuthorizedKey(originalUserKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}
	originalKeyStr := string(ssh.MarshalAuthorizedKey(originalUserKey))

	email, _, err := s.GetEmailBySSHKey(originalKeyStr)
	if err != nil {
		return nil, err
	}
	if email == "" {
		return nil, fmt.Errorf("user not found")
	}

	user, err := s.GetUserByEmail(email)
	if err != nil {
		return nil, err
	}

	alloc, err := s.getUserAlloc(user.UserID)
	if err != nil {
		return nil, err
	}

	if mn, ok := s.ipAllocator.LookupMachine(alloc.AllocID, localIP.String()); ok {
		machineName := mn

		// This is a request to access a specific machine with an address known to ipAllocator
		slog.Debug("ipAllocator-based machine access detected", "alloc", alloc.AllocID, "machine", machineName, "local_address", localAddress)

		perms, err := s.authenticateProxyUser(""+machineName, originalUserKeyBytes)
		if err != nil {
			return nil, err
		}

		return perms, nil
	}

	// Fall back to normal proxy authentication
	return s.authenticateProxyUser(username, originalUserKeyBytes)
}

// generateUserID creates a new user ID with "usr" prefix + 13 random characters
func generateUserID() (string, error) {
	randomPart := cryptorand.Text()
	if len(randomPart) < 13 {
		return "", fmt.Errorf("random text too short: %d", len(randomPart))
	}
	return "usr" + randomPart[:13], nil
}

// createTestUserWithID is a helper function for tests to create a user with proper user_id
func (s *Server) createTestUserWithID(email string) (string, error) {
	userID, err := generateUserID()
	if err != nil {
		return "", err
	}

	// Create user
	_, err = s.db.Exec(`
		INSERT INTO users (user_id, email)
		VALUES (?, ?)`,
		userID, email)
	if err != nil {
		return "", err
	}

	// Add the SSH key to ssh_keys table with unique key per user
	_, err = s.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key)
		VALUES (?, ?)`,
		userID, fmt.Sprintf("ssh-rsa dummy-test-key-%s %s", userID, email))
	if err != nil {
		return "", err
	}

	return userID, nil
}

// getUserIDByPublicKey gets user_id from an SSH public key
func (s *Server) getUserIDByPublicKey(publicKey ssh.PublicKey) (string, error) {
	var userID string
	publicKeyStr := string(ssh.MarshalAuthorizedKey(publicKey))
	err := s.db.QueryRow(`
		SELECT user_id FROM ssh_keys
		WHERE public_key = ?
		LIMIT 1
	`, publicKeyStr).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("user not found for public key")
		}
		return "", fmt.Errorf("database error: %w", err)
	}
	return userID, nil
}

// getUserBySSHKey retrieves a user by their SSH public key
func (s *Server) getUserBySSHKey(publicKey ssh.PublicKey) (*User, error) {
	publicKeyStr := string(ssh.MarshalAuthorizedKey(publicKey))
	return s.getUserByPublicKey(publicKeyStr)
}

// GetUserByEmail retrieves a user by their email address
func (s *Server) GetUserByEmail(email string) (*User, error) {
	var user User
	err := s.db.QueryRow(`
		SELECT user_id, email, created_at
		FROM users
		WHERE email = ?
	`, email).Scan(&user.UserID, &user.Email, &user.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("database error: %w", err)
	}
	return &user, nil
}

// allocateIPsForExistingMachines registers all existing machines with the ipAllocator
func (s *Server) allocateIPsForExistingMachines() error {
	rows, err := s.db.Query(`
		SELECT id, alloc_id, name
		FROM machines
		ORDER BY alloc_id, name
	`)
	if err != nil {
		return fmt.Errorf("failed to query existing machines: %v", err)
	}
	defer rows.Close()

	var machines []*Machine
	for rows.Next() {
		machine := &Machine{}
		err := rows.Scan(&machine.ID, &machine.AllocID, &machine.Name)
		if err != nil {
			return fmt.Errorf("failed to scan machine row: %v", err)
		}
		machines = append(machines, machine)
	}

	if err = rows.Err(); err != nil {
		return fmt.Errorf("error iterating machine rows: %v", err)
	}
	for _, machine := range machines {
		_, err := s.ipAllocator.Allocate(machine.AllocID, machine.Name)
		if err != nil {
			return fmt.Errorf("failed to register machine %s (alloc %s): %v", machine.Name, machine.AllocID, err)
		}
	}
	return nil
}
