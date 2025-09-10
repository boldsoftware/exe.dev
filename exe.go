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
	"errors"
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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mathrand "math/rand"

	"exe.dev/billing"
	"exe.dev/container"
	"exe.dev/exedb"
	"exe.dev/porkbun"
	"exe.dev/sqlite"
	"exe.dev/sshbuf"
	"exe.dev/tagresolver"
	"github.com/keighl/postmark"
	"github.com/lmittmann/tint"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stripe/stripe-go/v76"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"
)

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
			Level:      level,
			TimeFormat: "15:04:05",
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
	Ctrhost          string // Container host where this alloc's resources are
	CreatedAt        time.Time
	StripeCustomerID sql.NullString
	BillingEmail     sql.NullString
}

// UserPageData represents the data for the user dashboard page
type UserPageData struct {
	User    User
	SSHKeys []SSHKey
	Boxes   []exedb.Box
}

// SSHKey represents an SSH key for the user page
type SSHKey struct {
	UserID    string
	PublicKey string
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
	BoxName     string // Direct box name instead of team
	RedirectURL string
	ExpiresAt   time.Time
	CreatedAt   time.Time
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
	httpLn     *listener
	httpsLn    *listener
	sshLn      *listener
	pluginLn   *listener
	piperdPort int // what port sshpiperd is listening on, typically 2222
	BaseURL    string

	// ready indicates that the server is fully ready and serving.
	// ready must not be waited on prior to calling Start.
	// it's not 100% perfect--of necessity, we must call it before actually calling start on the various blocking servers--
	// but it's close, and it's much better than time.Sleep+hope.
	ready sync.WaitGroup

	httpServer          *http.Server
	httpsServer         *http.Server
	sshConfig           *ssh.ServerConfig
	sshHostKey          ssh.Signer
	certManager         *autocert.Manager
	wildcardCertManager *porkbun.WildcardCertManager

	// Piper plugin for SSH proxy authentication
	piperPlugin *PiperPlugin

	// Database
	db     *sqlite.DB
	dbPath string

	// Container management
	containerManager *container.NerdctlManager
	tagResolver      *tagresolver.TagResolver
	hostUpdater      *tagresolver.HostUpdater

	// In-memory state for active sessions (these don't need persistence)
	emailVerificationsMu sync.RWMutex
	emailVerifications   map[string]*EmailVerification // token -> email verification
	magicSecretsMu       sync.RWMutex
	magicSecrets         map[string]*MagicSecret // secret -> magic secret with expiration

	// User sessions for tracking authenticated users
	sessions map[*sshbuf.Channel]*UserSession // channel -> user session

	// Email and billing services
	postmarkClient *postmark.Client
	stripeKey      string
	fakeHTTPEmail  string // fake HTTP email server URL for sending emails (for e2e tests)

	testMode bool   // Test mode - skip animations for faster testing
	devMode  string // Development mode: "" (production) or "local" (Docker) or "test" for test mode

	// Metrics
	metricsRegistry *prometheus.Registry
	sshMetrics      *SSHMetrics

	// Data isolation
	dataSubdir string // subdirectory under /data for container isolation

	mu       sync.RWMutex
	stopping bool
}

// A listener is a listening port, along with address information.
// It exists to do the bookkeeping, particularly when starting a server with an address of :0.
type listener struct {
	origAddr string       // original requested listening address
	ln       net.Listener // listener (nil if not started yet)
	addr     string       // resolved listening address (e.g. if origAddr was :0)
	tcp      *net.TCPAddr // resolved TCP listening address
}

func unusedListener(addr string) *listener {
	return &listener{origAddr: addr}
}

func startListener(typ, addr string) (*listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	// this log line is important for e2e tests, they parse it to get port numbers!
	slog.Info("listening", "type", typ, "addr", tcpAddr.String(), "port", tcpAddr.Port)
	return &listener{
		origAddr: addr,
		ln:       ln,
		addr:     tcpAddr.String(),
		tcp:      tcpAddr,
	}, nil
}

var setStripeKey = sync.OnceFunc(func() {
	stripeKey := os.Getenv("STRIPE_API_KEY")
	if stripeKey == "" {
		stripeKey = "sk_test_51QxIgSGWIXq1kJnoiKwEcehJeO68QFsueLGymU9zR5jsJtMup5arFZZlHYaOzG3Bsw2GfnIG9H3Jv8Be10vqK1nW001hUxrS2g"
		if testing.Testing() {
			slog.Info("using default Stripe test key")
		}
	}
	stripe.Key = stripeKey
})

func runMigrations(dbPath string) error {
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database for migrations: %w", err)
	}
	defer rawDB.Close()
	if err := exedb.RunMigrations(rawDB); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	slog.Debug("database migrations complete")
	return nil
}

// NewServer creates a new Server instance with database and container management
func NewServer(httpAddr, httpsAddr, sshAddr, pluginAddr, dbPath, devMode, fakeEmailServer string, piperdPort int, containerdAddresses []string) (*Server, error) {
	// Run db migrations with a raw connection (not a pool).
	err := runMigrations(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	const nReaders = 16
	db, err := sqlite.New(dbPath, nReaders)
	if err != nil {
		return nil, fmt.Errorf("failed to create sqlite connection pool: %w", err)
	}

	slog.Debug("opened database connection pool", "dbPath", dbPath, "nReaders", nReaders)

	// Initialize data subdirectory for container isolation
	dataSubdir, err := exedb.InitDataSubdir(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize data subdir: %w", err)
	}

	// Detect if we're running in test mode

	// Initialize Postmark client
	postmarkAPIKey := os.Getenv("POSTMARK_API_KEY")
	var postmarkClient *postmark.Client
	if postmarkAPIKey != "" {
		postmarkClient = postmark.NewClient(postmarkAPIKey, "")
	} else {
		slog.Info("POSTMARK_API_KEY not set, email verification will not work")
	}

	setStripeKey()
	var baseURL string
	var httpLn *listener
	var httpsLn *listener
	if httpsAddr != "" {
		// HTTPS is configured, use https://exe.dev
		baseURL = "https://exe.dev"
		httpLn = unusedListener(httpAddr)
		httpsLn, err = startListener("https", httpsAddr)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to listen on HTTPS address %q: %w", httpsAddr, err)
		}
	} else {
		httpsLn = unusedListener(httpsAddr)
		httpLn, err = startListener("http", httpAddr)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to listen on HTTP address %q: %w", httpAddr, err)
		}
		// No HTTPS, use http://localhost with the HTTP port
		baseURL = fmt.Sprintf("http://localhost:%d", httpLn.tcp.Port)
		slog.Info("http server listening", "addr", httpLn.tcp.String(), "port", httpLn.tcp.Port)
	}

	sshLn, err := startListener("ssh", sshAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on SSH address %q: %w", sshAddr, err)
	}

	pluginLn, err := startListener("plugin", pluginAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on piper plugin address %q: %w", pluginAddr, err)
	}

	// Initialize container manager with containerd
	var containerManager *container.NerdctlManager

	// Check if we have valid containerd addresses (not just empty strings)
	hasValidAddresses := false
	for _, addr := range containerdAddresses {
		if addr != "" {
			hasValidAddresses = true
			break
		}
	}

	if hasValidAddresses {
		config := &container.Config{
			ContainerdAddresses:  containerdAddresses,
			DefaultCPURequest:    "500m",
			DefaultMemoryRequest: "1Gi",
			DefaultStorageSize:   "10Gi",
			DataSubdir:           dataSubdir,
		}

		// Optional: load OCI/Kata annotations from environment as JSON
		// Example: EXE_KATA_ANNOTATIONS='{"io.katacontainers.config.hypervisor.restore_snapshot":"/var/lib/cloud-hypervisor/snapshots/base.snapshot","io.katacontainers.config.hypervisor.restore_memory":"/var/lib/cloud-hypervisor/snapshots/base.mem"}'
		if annJSON := os.Getenv("EXE_KATA_ANNOTATIONS"); annJSON != "" {
			var ann map[string]string
			if err := json.Unmarshal([]byte(annJSON), &ann); err != nil {
				db.Close()
				return nil, fmt.Errorf("invalid EXE_KATA_ANNOTATIONS JSON: %w", err)
			}
			config.KataAnnotations = ann
			slog.Info("Kata annotations configured", "count", len(ann))
		} else {
			slog.Debug("No Kata annotations configured (EXE_KATA_ANNOTATIONS is empty)")
		}

		var managerErr error
		containerManager, managerErr = container.NewNerdctlManager(config)
		if managerErr != nil {
			// Container manager initialization failure is now fatal - security critical
			slog.Error("Failed to initialize container manager", "error", managerErr)
			// If it's a Kata-related error, provide specific guidance
			if strings.Contains(managerErr.Error(), "Kata runtime") {
				slog.Error("Kata runtime is required for container security",
					"details", "All containers must run in Kata VMs for proper isolation",
					"fix", "Ensure Kata is installed and configured in containerd")
			}
			return nil, managerErr
		}
		slog.Info("Container manager initialized successfully")
	} else {
		slog.Debug("No containerd addresses configured, container functionality disabled")
	}

	// Initialize metrics
	metricsRegistry := prometheus.NewRegistry()
	sshMetrics := NewSSHMetrics(metricsRegistry)
	sqlite.RegisterSQLiteMetrics(metricsRegistry)

	// Initialize tag resolver and host updater for container image management
	var tagResolverInstance *tagresolver.TagResolver
	var hostUpdaterInstance *tagresolver.HostUpdater
	if containerManager != nil && len(containerdAddresses) > 0 {
		tagResolverInstance = tagresolver.New(db)
		hostUpdaterInstance = tagresolver.NewHostUpdater(db, tagResolverInstance, containerdAddresses)

		// Set tag resolver on the container manager
		containerManager.SetTagResolver(tagResolverInstance)
		containerManager.SetHostUpdater(hostUpdaterInstance)
		slog.Info("Tag resolver configured for image freshness management")
	}

	s := &Server{
		httpLn:             httpLn,
		httpsLn:            httpsLn,
		sshLn:              sshLn,
		pluginLn:           pluginLn,
		piperdPort:         piperdPort,
		BaseURL:            baseURL,
		db:                 db,
		dbPath:             dbPath,
		containerManager:   containerManager,
		tagResolver:        tagResolverInstance,
		hostUpdater:        hostUpdaterInstance,
		emailVerifications: make(map[string]*EmailVerification),
		magicSecrets:       make(map[string]*MagicSecret),
		sessions:           make(map[*sshbuf.Channel]*UserSession),
		postmarkClient:     postmarkClient,
		fakeHTTPEmail:      fakeEmailServer,
		stripeKey:          stripe.Key,
		devMode:            devMode,

		testMode:        testing.Testing() || devMode == "test",
		metricsRegistry: metricsRegistry,
		sshMetrics:      sshMetrics,
		dataSubdir:      dataSubdir,
	}

	s.setupHTTPServer()
	s.setupHTTPSServer()
	s.setupSSHServer()

	// Prepare RovolFS on all hosts during server setup
	for _, host := range containerdAddresses {
		if host != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := containerManager.PrepareRovol(ctx, host); err != nil {
				cancel()
				slog.Error("Failed to prepare RovolFS on host", "host", host, "error", err)
				return nil, fmt.Errorf("failed to prepare RovolFS on host %s: %w", host, err)
			}
			cancel()
			slog.Info("Successfully prepared RovolFS on host", "host", host)
		}
	}

	s.ready.Add(1) // matched with final done at bottom of Start
	go func() {
		s.ready.Wait()
		// The following log line signals to e2e tests that they may proceed with using the server (better than sleeps!)
		slog.Info("server started")
	}()

	return s, nil
}

// DataPath returns a path under /data with the server's isolation subdirectory
func (s *Server) DataPath(path string) string {
	return fmt.Sprintf("/data/%s/%s", s.dataSubdir, strings.TrimPrefix(path, "/"))
}

// setupHTTPServer configures the HTTP server
func (s *Server) setupHTTPServer() {
	// Use standard promhttp instrumentation
	instrumentedHandler := promhttp.InstrumentMetricHandler(
		s.metricsRegistry,
		s)

	s.httpServer = &http.Server{
		Addr:    s.httpLn.addr,
		Handler: instrumentedHandler,
	}
}

// setupHTTPSServer configures the HTTPS server with Let's Encrypt if enabled
func (s *Server) setupHTTPSServer() {
	if s.httpsLn.ln == nil {
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
			Addr:    s.httpsLn.addr,
			Handler: s,
			TLSConfig: &tls.Config{
				GetCertificate: s.wildcardCertManager.GetCertificate,
			},
		}
	} else {
		// Fall back to regular autocert for non-wildcard certificates
		slog.Info("Using standard autocert (no wildcard support)", "note", "Set PORKBUN_API_KEY and PORKBUN_SECRET_API_KEY for wildcard certificates")
		s.certManager = &autocert.Manager{
			Cache:      autocert.DirCache("certs"),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist("exe.dev"),
		}

		s.httpsServer = &http.Server{
			Addr:    s.httpsLn.addr,
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
	if err := s.generateHostKey(context.Background()); err != nil {
		slog.Error("Failed to generate host key", "error", err)
	}
}

// generateHostKey loads the persistent RSA host key from the database, or generates and stores a new one
func (s *Server) generateHostKey(ctx context.Context) error {
	// Try to load existing host key from database
	var privateKeyPEM, publicKeyPEM string
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT private_key, public_key FROM ssh_host_key WHERE id = 1`).Scan(&privateKeyPEM, &publicKeyPEM)
	})

	if errors.Is(err, sql.ErrNoRows) {
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
		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO ssh_host_key (id, private_key, public_key, fingerprint, created_at, updated_at)
				VALUES (1, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
				privateKeyPEM, publicKeyPEM, fingerprint)
			return err
		})
		if err != nil {
			return fmt.Errorf("failed to store host key: %w", err)
		}

		slog.Debug("Generated and stored new SSH host key", "fingerprint", fingerprint)
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
		slog.Debug("Loaded existing SSH host key", "fingerprint", fingerprint)
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
		return fmt.Sprintf("http://localhost:%v", s.httpLn.tcp.Port)
	}
	return "https://exe.dev"
}

// sendEmail sends an email using the configured email service
func (s *Server) sendEmail(to, subject, body string) error {
	// Check if HTTP email server is configured first
	if s.fakeHTTPEmail != "" {
		err := s.sendFakeEmail(to, subject, body)
		if err != nil {
			slog.Warn("failed to send fake email", "to", to, "subject", subject, "error", err)
		}
	}

	// In dev mode, always just log the email
	if s.devMode != "" {
		slog.Info("📧 DEV MODE: Would send email", "to", to, "subject", subject, "body", body)
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

// sendFakeEmail sends an email to the fake HTTP email server
func (s *Server) sendFakeEmail(to, subject, body string) error {
	emailData := map[string]string{
		"to":      to,
		"subject": subject,
		"body":    body,
	}

	jsonData, err := json.Marshal(emailData)
	if err != nil {
		return fmt.Errorf("failed to marshal email data: %w", err)
	}

	resp, err := http.Post(s.fakeHTTPEmail, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("failed to send fake email via HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("fake email server returned error: %s", resp.Status)
	}

	slog.Info("fake email sent successfully via HTTP", "to", to, "subject", subject)
	return nil
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

// logAuthAttempt logs all SSH authentication attempts for debugging
func (s *Server) logAuthAttempt(conn ssh.ConnMetadata, method string, err error) {
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
	// Create a 3-second timeout context for authentication operations
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

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
	if originalUserKey, localAddress, isProxy := s.lookupEphemeralProxyKey(key); isProxy {
		slog.Debug("Ephemeral proxy authentication detected", "user", user, "local_address", localAddress)
		return s.authenticateProxyUserWithLocalAddress(ctx, user, originalUserKey, localAddress)
	} else {
		slog.Debug("Not a proxy key, treating as direct user connection")
	}
	// Log non-proxy connections for monitoring - in production, all connections should come via proxy
	slog.Warn("Direct connection to exed - should come via proxy", "remote_addr", remoteAddr)

	// First check if this key is already registered in ssh_keys table
	email, verified, err := s.GetEmailBySSHKey(ctx, publicKeyStr)
	if err != nil {
		slog.Error("Database error checking SSH key", "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has an alloc
		var userID string
		err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow("SELECT user_id FROM users WHERE email = ?", email).Scan(&userID)
		})
		if err == nil {
			// Check if user has an alloc
			var allocExists bool
			err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
				return rx.QueryRow("SELECT EXISTS(SELECT 1 FROM allocs WHERE user_id = ?)", userID).Scan(&allocExists)
			})
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
	slog.Debug("HTTP request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr, "host", r.Host)

	// Check if this should be handled by the proxy handler
	isProxy := s.isProxyRequest(r.Host)
	isTerminal := s.isTerminalRequest(r.Host)
	slog.Debug("[REDIRECT] Main handler routing check", "host", r.Host, "isProxy", isProxy, "isTerminal", isTerminal)
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
			if userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host); err == nil {
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
		userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
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
		// Handle mobile UI routes
		if strings.HasPrefix(path, "/m") {
			s.handleMobile(w, r)
			return
		}

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
	fmt.Fprintf(w, `{"containers":[],"message":"Box management not yet implemented"}`)
}

// showDeviceVerificationForm shows a confirmation form for device verification
func (s *Server) showDeviceVerificationForm(w http.ResponseWriter, r *http.Request, token string) {
	// Look up the pending SSH key to validate token and get info
	var publicKey, email string
	var expires time.Time
	err := s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT public_key, user_email, expires_at
		FROM pending_ssh_keys
		WHERE token = ?`,
			token).Scan(&publicKey, &email, &expires)
	})

	if errors.Is(err, sql.ErrNoRows) {
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
		// Clean up expired token - use context.Background() to ensure cleanup completes even if client disconnects
		s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec("DELETE FROM pending_ssh_keys WHERE token = ?", token)
			return err
		})
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
	err := s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT public_key, user_email, expires_at
		FROM pending_ssh_keys
		WHERE token = ?`,
			token).Scan(&publicKey, &email, &expires)
	})

	if errors.Is(err, sql.ErrNoRows) {
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
		s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec("DELETE FROM pending_ssh_keys WHERE token = ?", token)
			return err
		})
		http.Error(w, "Verification token has expired", http.StatusBadRequest)
		return
	}

	// Add the SSH key to the verified keys and clean up pending key
	err = s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		// Add SSH key
		_, err := tx.Exec(`
			INSERT INTO ssh_keys (user_id, public_key)
			VALUES ((SELECT user_id FROM users WHERE email = ?), ?)
			ON CONFLICT(public_key) DO NOTHING`,
			email, publicKey)
		if err != nil {
			return err
		}

		// Clean up the pending key
		_, err = tx.Exec("DELETE FROM pending_ssh_keys WHERE token = ?", token)
		return err
	})
	if err != nil {
		slog.Error("Failed to add SSH key", "error", err)
		http.Error(w, "Failed to verify device", http.StatusInternalServerError)
		return
	}

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
		_, err := s.checkEmailVerificationToken(r.Context(), token)
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
	v, exists := s.emailVerifications[token]
	var verification EmailVerification
	if exists {
		verification = *v
	}
	s.emailVerificationsMu.Unlock()

	if exists {
		// This is an SSH session email verification
		email := verification.Email

		// Create the user if they don't exist
		user, err := s.getUserByPublicKey(r.Context(), verification.PublicKey)
		if err != nil || user == nil {
			slog.Info("User doesn't exist, creating", "email", email, "token", token)
			// User doesn't exist - create them with their alloc
			if err := s.createUserWithAlloc(context.Background(), verification.PublicKey, email); err != nil {
				slog.Error("failed to create user with alloc during email verification", "error", err, "token", token)
				// Clean up pending registration on failure
				err := s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
					_, err := tx.Exec("DELETE FROM pending_registrations WHERE token = ?", token)
					return err
				})
				if err != nil {
					slog.Error("failed to clean up pending registration", "error", err)
				}
				http.Error(w, "Failed to create user account", http.StatusInternalServerError)
				return
			}
			// Clean up pending registration on success
			s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
				_, err := tx.Exec("DELETE FROM pending_registrations WHERE token = ?", token)
				return err
			})
			slog.Info("Created new user", "email", email)
		} else {
			slog.Debug("User already exists", "email", email)
		}

		// Store the SSH key as verified
		publicKey := verification.PublicKey
		if publicKey != "" {
			err = s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
				_, err := tx.Exec(`
					INSERT INTO ssh_keys (user_id, public_key)
					VALUES ((SELECT user_id FROM users WHERE email = ?), ?)
					ON CONFLICT(public_key) DO UPDATE SET user_id = (SELECT user_id FROM users WHERE email = ?)`,
					email, publicKey, email)
				return err
			})
			if err != nil {
				slog.Error("Error storing SSH key during verification", "error", err)
			}
		}

		// Create HTTP auth cookie for this user
		var userID string
		err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID)
		})
		if err != nil {
			slog.Error("Failed to get user ID by email during SSH email verification", "error", err)
		} else {
			cookieValue, err := s.createAuthCookie(context.Background(), userID, r.Host)
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
		s.emailVerificationsMu.Lock()
		delete(s.emailVerifications, token)
		s.emailVerificationsMu.Unlock()
	} else {
		// Not an SSH token, check database for HTTP auth token
		// Try to validate as database token
		userID, err := s.validateEmailVerificationToken(r.Context(), token)
		if err != nil {
			slog.Error("Invalid email verification token", "error", err)
			http.Error(w, "Invalid or expired verification token", http.StatusNotFound)
			return
		}

		// Create HTTP auth cookie for this user
		cookieValue, err := s.createAuthCookie(context.Background(), userID, r.Host)
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
		err = s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec("DELETE FROM email_verifications WHERE token = ?", token)
			return err
		})
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
	slog.Debug("[REDIRECT] handleAuth called", "method", r.Method, "url", r.URL.String(), "host", r.Host)
	// Check if user already has a valid exe.dev auth cookie
	cookie, err := r.Cookie("exe-auth")
	if err == nil && cookie.Value != "" {
		userID, err := s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
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
	err := s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow("SELECT user_id FROM users WHERE email = ?", email).Scan(&userID)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
	err = s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO email_verifications (token, email, user_id, expires_at)
			VALUES (?, ?, ?, ?)
		`, token, email, userID, time.Now().Add(24*time.Hour).Format(time.RFC3339))
		return err
	})
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
		userID, err = s.validateEmailVerificationToken(r.Context(), token)
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
		userID, err = s.validateAuthToken(r.Context(), token, "")
		if err != nil {
			slog.Error("Invalid auth token in callback", "error", err)
			http.Error(w, "Invalid or expired authentication token", http.StatusUnauthorized)
			return
		}
	}

	// Create main domain auth cookie
	cookieValue, err := s.createAuthCookie(context.Background(), userID, r.Host)
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

	// Parse hostname to get box name
	boxName, err := s.parseProxyHostname(hostname)
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
		TeamName:   boxName,
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
func (s *Server) checkEmailVerificationToken(ctx context.Context, token string) (string, error) {
	var userID string
	var email string
	var expiresAt string

	// Get verification info and return user_id directly
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT e.user_id, e.email, e.expires_at
		FROM email_verifications e
		WHERE e.token = ?
	`, token).Scan(&userID, &email, &expiresAt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
		s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec("DELETE FROM email_verifications WHERE token = ?", token)
			return err
		})
		return "", fmt.Errorf("verification token expired")
	}

	return userID, nil
}

// validateEmailVerificationToken validates an email verification token, consumes it, and returns the user ID
func (s *Server) validateEmailVerificationToken(ctx context.Context, token string) (string, error) {
	userID, err := s.checkEmailVerificationToken(ctx, token)
	if err != nil {
		return "", err
	}

	// Clean up used token - use context.Background() to ensure cleanup completes even if client disconnects
	s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec("DELETE FROM email_verifications WHERE token = ?", token)
		return err
	})

	return userID, nil
}

// storeEmailVerification stores an email verification token
func (s *Server) storeEmailVerification(ctx context.Context, email, token string) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		// Check if user exists, create if not
		var userID string
		err := tx.QueryRow("SELECT user_id FROM users WHERE email = ?", email).Scan(&userID)
		if err != nil {
			// User doesn't exist, create them
			userID, err = generateUserID()
			if err != nil {
				return fmt.Errorf("failed to generate user ID: %w", err)
			}

			_, err = tx.Exec("INSERT INTO users (user_id, email) VALUES (?, ?)", userID, email)
			if err != nil {
				return fmt.Errorf("failed to create user: %w", err)
			}

			// Create user allocation
			allocID, err := generateAllocID()
			if err != nil {
				return fmt.Errorf("failed to generate alloc ID: %w", err)
			}

			ctrhost := s.selectCtrhostForNewAlloc()
			_, err = tx.Exec(`
				INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost)
				VALUES (?, ?, 'shared', 'default', ?)
			`, allocID, userID, ctrhost)
			if err != nil {
				return fmt.Errorf("failed to create allocation: %w", err)
			}
		}

		// Store verification token
		expiresAt := time.Now().Add(24 * time.Hour)
		_, err = tx.Exec(`
			INSERT OR REPLACE INTO email_verifications (token, user_id, email, expires_at)
			VALUES (?, ?, ?, ?)
		`, token, userID, email, expiresAt)
		return err
	})
}

// validateEmailVerificationByCode validates verification using short code
func (s *Server) validateEmailVerificationByCode(ctx context.Context, code string) (string, error) {
	var userID string
	var token string

	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
			SELECT user_id, token
			FROM email_verifications
			WHERE token LIKE ? AND expires_at > datetime('now')
			LIMIT 1
		`, code+"%").Scan(&userID, &token)
	})
	if err != nil {
		return "", fmt.Errorf("invalid or expired code")
	}

	// Consume the token
	s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec("DELETE FROM email_verifications WHERE token = ?", token)
		return err
	})

	return userID, nil
}

// Helper functions for authentication and reverse proxy

// createAuthCookie creates a new authentication cookie for the user
func (s *Server) createAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	// Generate a random cookie value
	cookieBytes := make([]byte, 32)
	if _, err := cryptorand.Read(cookieBytes); err != nil {
		return "", fmt.Errorf("failed to generate cookie: %w", err)
	}
	cookieValue := base64.URLEncoding.EncodeToString(cookieBytes)

	// Set expiration to 30 days from now
	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	// Store in database
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO auth_cookies (cookie_value, user_id, domain, expires_at)
			VALUES (?, ?, ?, ?)
		`, cookieValue, userID, getDomain(domain), expiresAt.Format(time.RFC3339))
		return err
	})
	if err != nil {
		return "", fmt.Errorf("failed to store auth cookie: %w", err)
	}

	return cookieValue, nil
}

// validateAuthCookie validates an authentication cookie and returns the user_id
func (s *Server) validateAuthCookie(ctx context.Context, cookieValue, domain string) (string, error) {
	var userID string
	var expiresAt string

	// Get auth cookie info
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT ac.user_id, ac.expires_at
		FROM auth_cookies ac
		WHERE ac.cookie_value = ? AND ac.domain = ?
	`, cookieValue, getDomain(domain)).Scan(&userID, &expiresAt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
		// Clean up expired cookie - use context.Background() to ensure cleanup completes even if client disconnects
		s.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec("DELETE FROM auth_cookies WHERE cookie_value = ?", cookieValue)
			return err
		})
		return "", fmt.Errorf("cookie expired")
	}

	// Update last used time
	s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec("UPDATE auth_cookies SET last_used_at = CURRENT_TIMESTAMP WHERE cookie_value = ?", cookieValue)
		return err
	})

	return userID, nil
}

// createMagicSecret creates a temporary magic secret for proxy authentication
func (s *Server) createMagicSecret(userID, boxName, redirectURL string) (string, error) {
	// Generate a random secret
	secret := cryptorand.Text()

	// Clean up expired secrets while we're here
	s.cleanupExpiredMagicSecrets()

	// Store in memory with 2-minute expiration
	s.magicSecretsMu.Lock()
	defer s.magicSecretsMu.Unlock()

	s.magicSecrets[secret] = &MagicSecret{
		UserID:      userID,
		BoxName:     boxName,
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
func (s *Server) validateAuthToken(ctx context.Context, token, expectedSubdomain string) (string, error) {
	var userID string
	var subdomain sql.NullString
	var expiresAt string
	var usedAt sql.NullString

	// Get auth token info and return user_id directly
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT at.user_id, at.subdomain, at.expires_at, at.used_at
		FROM auth_tokens at
		WHERE at.token = ?
	`, token).Scan(&userID, &subdomain, &expiresAt, &usedAt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec("UPDATE auth_tokens SET used_at = CURRENT_TIMESTAMP WHERE token = ?", token)
		return err
	})
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

	slog.Debug("[REDIRECT] redirectAfterAuth called", "redirectURL", redirectURL, "returnHost", returnHost, "user_id", userID)

	if returnHost != "" && redirectURL != "" {
		if s.isTerminalRequest(returnHost) {
			slog.Debug("[REDIRECT] redirectAfterAuth: detected terminal request", "returnHost", returnHost)
			// Parse hostname to extract box name
			hostname := returnHost
			if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
				hostname = returnHost[:idx]
			}

			boxName, err := s.parseTerminalHostname(hostname)
			if err != nil {
				slog.Error("Failed to parse terminal hostname", "hostname", hostname, "error", err)
				http.Error(w, "Invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the terminal subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
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
			slog.Debug("[REDIRECT] redirectAfterAuth: detected proxy request", "returnHost", returnHost)
			// Parse hostname to extract box and team names
			hostname := returnHost
			if idx := strings.LastIndex(returnHost, ":"); idx > 0 {
				hostname = returnHost[:idx]
			}

			boxName, err := s.parseProxyHostname(hostname)
			if err != nil {
				slog.Error("Failed to parse proxy hostname", "hostname", hostname, "error", err)
				http.Error(w, "Invalid hostname format", http.StatusBadRequest)
				return
			}

			// Create magic secret for the proxy subdomain
			secret, err := s.createMagicSecret(userID, boxName, redirectURL)
			if err != nil {
				slog.Error("Failed to create magic secret", "error", err)
				http.Error(w, "Failed to create authentication secret", http.StatusInternalServerError)
				return
			}

			// Redirect to confirmation page with magic secret
			confirmURL := fmt.Sprintf("/auth/confirm?secret=%s&return_host=%s", secret, url.QueryEscape(returnHost))
			slog.Debug("[REDIRECT] redirectAfterAuth creating confirmation URL", "confirmURL", confirmURL)
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
	err := s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT user_id, email, created_at
		FROM users
		WHERE user_id = ?
	`, userID).Scan(&user.UserID, &user.Email, &user.CreatedAt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User not found", http.StatusNotFound)
		} else {
			slog.Error("Failed to get user info for dashboard", "error", err, "user_id", userID)
			http.Error(w, "Failed to load user information", http.StatusInternalServerError)
		}
		return
	}

	// Get user's SSH keys
	sshKeys := []SSHKey{}
	err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		rows, err := rx.Query(`
			SELECT public_key
			FROM ssh_keys
			WHERE user_id = ?
			ORDER BY added_at DESC
		`, user.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key SSHKey
			err := rows.Scan(&key.PublicKey)
			if err != nil {
				slog.Error("Error scanning SSH key", "error", err)
				continue
			}
			sshKeys = append(sshKeys, key)
		}
		return nil
	})
	if err != nil {
		slog.Error("Failed to get SSH keys for dashboard", "error", err, "email", user.Email)
	}

	// Get user's boxes from all teams they belong to
	boxes := []exedb.Box{}
	err = s.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		boxRows, err := rx.Query(`
			SELECT m.id, m.alloc_id, m.name, m.status, COALESCE(m.image, ''),
			       COALESCE(m.container_id, ''), m.created_by_user_id,
			       m.created_at, m.updated_at, m.last_started_at
			FROM boxes m
			JOIN allocs a ON m.alloc_id = a.alloc_id
			WHERE a.user_id = ?
			ORDER BY m.updated_at DESC
		`, user.UserID)
		if err != nil {
			return err
		}
		defer boxRows.Close()
		for boxRows.Next() {
			var box exedb.Box
			var containerID, image sql.NullString
			var lastStartedAt sql.NullTime
			err := boxRows.Scan(&box.ID, &box.AllocID, &box.Name,
				&box.Status, &image, &containerID, &box.CreatedByUserID,
				&box.CreatedAt, &box.UpdatedAt, &lastStartedAt)
			if err != nil {
				slog.Error("Error scanning box", "error", err)
				continue
			}
			if containerID.Valid {
				box.ContainerID = &containerID.String
			}
			if image.Valid {
				box.Image = image.String
			}
			if lastStartedAt.Valid {
				box.LastStartedAt = &lastStartedAt.Time
			}
			boxes = append(boxes, box)
		}
		return nil
	})
	if err != nil {
		slog.Error("Failed to get boxes for dashboard", "error", err, "user_id", userID)
	}

	// Prepare template data
	data := UserPageData{
		User:    user,
		SSHKeys: sshKeys,
		Boxes:   boxes,
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
		userID, _ = s.validateAuthCookie(r.Context(), cookie.Value, r.Host)
	}

	// Clear ALL auth cookies for this user across all domains
	if userID != "" {
		err := s.db.Tx(r.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				DELETE FROM auth_cookies
				WHERE user_id = ?
			`, userID)
			return err
		})
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

// findBoxByNameForUser finds a box by name that the user has access to
func (s *Server) FindBoxByNameForUser(ctx context.Context, userID, boxName string) *exedb.Box {
	slog.Debug("FindBoxByNameForUser", "user_id", userID, "box_name", boxName)

	// Box names are now globally unique, no team prefix
	if strings.Contains(boxName, ".") {
		// Legacy format not supported
		return nil
	}

	// Get user's alloc to verify access
	alloc, err := s.getUserAlloc(ctx, userID)
	if err != nil || alloc == nil {
		slog.Debug("FindBoxByNameForUser no alloc found", "user_id", userID)
		return nil
	}

	// Check if box exists and belongs to user's alloc
	box, err := s.getBoxByName(ctx, boxName)
	if err != nil {
		slog.Debug("Box not found", "box", boxName, "error", err)
		return nil
	}

	// Verify the box belongs to the user's alloc
	if box.AllocID != alloc.AllocID {
		slog.Debug("Box belongs to different alloc", "box", boxName, "box_alloc", box.AllocID, "user_alloc", alloc.AllocID)
		return nil
	}

	return box
}

// handleListCommand lists user's boxes
func generateRandomBoxName() string {
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

// formatSSHConnectionInfo returns SSH connection info based on dev mode
func (s *Server) formatSSHConnectionInfo(allocID, boxName string) string {
	if s.devMode != "" {
		var dashP string
		if s.piperdPort != 22 {
			dashP = fmt.Sprintf("-p %v ", s.piperdPort)
		}
		return fmt.Sprintf("ssh %s%s@localhost", dashP, boxName)
	}
	return fmt.Sprintf("ssh %s@exe.dev", boxName)
}

// denylistedBoxNames contains common computer-related five+ letter words that are not allowed as box names
var denylistedBoxNames = map[string]bool{
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
	"vibes": true, "awesome": true,
	"panel": true, "adminpanel": true, "console": true, "dashboard": true,
	"settings": true, "config": true, "preferences": true, "options": true,
	"management": true, "control": true, "monitor": true,
	"viewer": true, "preview": true, "observability": true,
	"report": true, "analytics": true, "metric": true, "metrics": true, "stats": true,
	"endpoint": true, "identity": true, "oauth": true, "whoami": true,
	"profile": true, "username": true, "password": true, "passkey": true,
	"gitlab": true, "githost": true, "gitty": true,
	"jupyter": true, "notebook": true,
	"gerrit": true, "reviewboard": true,
	"zulip": true, "jitsi": true, "mastodon": true,
	"nextcloud": true, "owncloud": true, "seafile": true, "alertmanager": true,
	"jenkins": true, "philz": true, "buildbot": true, "drone": true,
	"gitea": true, "forgejo": true, "sourcehut": true,
	"mattermost": true, "rocketchat": true, "element": true,
	"discourse": true, "flarum": true, "nodebb": true,
	"wikijs": true, "bookstack": true, "outline": true,
	"jellyfin": true, "plex": true, "emby": true,
	"homeassistant": true, "openhab": true, "domoticz": true,
	"bitwarden": true, "vaultwarden": true, "keepass": true,
	"immich": true, "photoprism": true, "piwigo": true,
	"pihole": true, "adguard": true, "unbound": true,
	"wireguard": true, "openvpn": true, "tailscale": true,
	"caddy": true, "haproxy": true,
	"portainer": true, "rancher": true, "k3s": true,
	"minio": true, "rclone": true, "syncthing": true,
	"ghost": true, "strapi": true, "directus": true,
	"supabase": true, "appwrite": true, "pocketbase": true,
	"invoiceninja": true, "crater": true, "akaunting": true,
	"nodered": true, "huginn": true,
	"box-name": true, "new-link": true, "test-name": true,
	"invite": true, "unlink": true, "source-port": true,
	"target-port": true, "ssh-port": true,
	"admin-user": true, "admin-name": true, "admin-login": true,
	"user-name": true, "user-login": true, "user-pass": true,
	"dev-user": true, "dev-name": true, "dev-login": true,
	"dev-pass": true, "demo-user": true, "demo-name": true, "demo-login": true,
	"demo-pass": true, "test-user": true, "test-login": true, "test-pass": true,
	"example": true, "examples": true, "sample": true, "samples": true,
	"foobar": true, "foo-bar": true, "bar-foo": true,
	"hello": true, "world": true, "hello-world": true,
	"lorem": true, "ipsum": true, "lorem-ipsum": true,
	"access-level": true, "priority": true, "read-only": true, "readwrite": true,
	"path-prefix": true, "subdomain": true, "two-factor": true, "twofactor": true,
	"multi-factor": true, "multifactor": true, "mfa-required": true,
	"ssh-key": true, "ssh-keys": true, "sshkey": true, "sshkeys": true,
	"ssh-access": true, "sshaccess": true,
	"ssh-login": true, "sshlogin": true, "sshport": true,
	"ssh-user": true, "sshuser": true,
	"ssh-host": true, "sshhost": true,
	"ssh-hostname": true, "sshhostname": true,
	"ssh-identity": true, "sshidentity": true,
	"ssh-auth": true, "sshauth": true,
	"ssh-authentication": true, "sshauthentication": true,
	"ssh-agent": true, "sshagent": true,
	"ssh-config": true, "sshconfig": true,
	"ssh-command": true, "sshcommand": true,
	"ssh-connection": true, "sshconnection": true,
	"ssh-tunnel": true, "sshtunnel": true,
	"ssh-forward": true, "sshforward": true,
	"ssh-forwarding": true, "sshforwarding": true,
	"ssh-session": true, "sshsession": true,
	"ssh-socket": true, "sshsocket": true,
	"ssh-agent-forward": true, "sshagentforward": true,
	"ssh-agent-forwarding": true, "sshagentforwarding": true,
	"ssh-keygen": true, "sshkeygen": true,
	"ssh-copy-id": true, "sshcopyid": true,
	"ssh-add": true, "sshadd": true,
}

// isValidBoxName validates box name format
func (s *Server) isValidBoxName(name string) bool {
	// Must be at least 5 characters and at most 64 characters
	if len(name) < 5 || len(name) > 64 {
		return false
	}

	// Check if name is in denylist
	withoutHyphens := strings.ReplaceAll(name, "-", "")
	if denylistedBoxNames[withoutHyphens] {
		return false
	}

	// Check pattern: starts with letter, contains only lowercase letters/numbers/hyphens, no consecutive hyphens, doesn't end with hyphen
	matched, _ := regexp.MatchString(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`, name)
	return matched
}

// getDefaultRouteJSON returns the default route as a JSON string
func getDefaultRouteJSON() string {
	var box exedb.Box
	route := box.GetDefaultRoute()
	data, err := json.Marshal(route)
	if err != nil {
		log.Fatalf("Failed to marshal default route: %v", err)
	}
	return string(data)
}

// preCreateBox creates a box entry before the container is created, returns the box ID
func (s *Server) preCreateBox(ctx context.Context, userID, allocID, name, image string) (int, error) {
	// Validate box name
	if !s.isValidBoxName(name) {
		return 0, fmt.Errorf("invalid box name: %s", name)
	}

	routes := getDefaultRouteJSON()
	var boxID int
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		result, err := tx.Exec(`
			INSERT INTO boxes (
				alloc_id, name, status, image, container_id, created_by_user_id, routes
			) VALUES (?, ?, ?, ?, NULL, ?, ?)
		`, allocID, name, "creating", image, userID, routes)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		boxID = int(id)
		s.recordUserEventTx(tx, userID, userEventCreatedBox)
		return nil
	})
	if err != nil {
		return 0, err
	}

	return boxID, nil
}

// updateBoxWithContainer updates a box with container info and SSH keys after container creation
func (s *Server) updateBoxWithContainer(ctx context.Context, boxID int, containerID, sshUser string, sshKeys *container.ContainerSSHKeys, sshPort int) error {
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			UPDATE boxes SET
				container_id = ?,
				status = ?,
				ssh_server_identity_key = ?,
				ssh_authorized_keys = ?,
				ssh_ca_public_key = ?,
				ssh_host_certificate = ?,
				ssh_client_private_key = ?,
				ssh_port = ?,
				ssh_user = ?
			WHERE id = ?
		`, containerID, "running",
			sshKeys.ServerIdentityKey, sshKeys.AuthorizedKeys, sshKeys.CAPublicKey,
			sshKeys.HostCertificate, sshKeys.ClientPrivateKey, sshPort, sshUser,
			boxID)
		return err
	})
	return err
}

// isBoxNameAvailable checks if a box name is available for use.
// Errors are translated into false (unavailability).
func (s *Server) isBoxNameAvailable(ctx context.Context, name string) bool {
	box, err := s.getBoxByName(ctx, name)
	return box == nil && errors.Is(err, sql.ErrNoRows)
}

// getBoxByName retrieves a box by name and team
func (s *Server) getBoxByName(ctx context.Context, name string) (*exedb.Box, error) {
	var box exedb.Box
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT id, alloc_id, name, status, image, container_id, created_by_user_id, created_at, updated_at, last_started_at, routes,
		       ssh_server_identity_key, ssh_authorized_keys, ssh_ca_public_key, ssh_host_certificate, ssh_client_private_key, ssh_port, ssh_user
		FROM boxes
		WHERE name = ?
	`, name).Scan(
			&box.ID, &box.AllocID, &box.Name, &box.Status,
			&box.Image, &box.ContainerID, &box.CreatedByUserID,
			&box.CreatedAt, &box.UpdatedAt, &box.LastStartedAt, &box.Routes,
			&box.SSHServerIdentityKey, &box.SSHAuthorizedKeys, &box.SSHCAPublicKey, &box.SSHHostCertificate, &box.SSHClientPrivateKey, &box.SSHPort, &box.SSHUser,
		)
	})
	if err != nil {
		return nil, err
	}
	return &box, nil
}

// getBoxesForAlloc gets all boxes for an allocation
func (s *Server) getBoxesForAlloc(ctx context.Context, allocID string) ([]exedb.Box, error) {
	var boxes []exedb.Box
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		exedbBoxes, err := queries.GetBoxesForAlloc(ctx, allocID)
		if err != nil {
			return err
		}
		boxes = exedbBoxes
		return nil
	})
	if err != nil {
		return nil, err
	}
	return boxes, nil
}

// getAllocByID gets an allocation by its ID
func (s *Server) getAllocByID(ctx context.Context, allocID string) (*Alloc, error) {
	var alloc Alloc
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		err := rx.QueryRow(`
			SELECT alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email
			FROM allocs
			WHERE alloc_id = ?
		`, allocID).Scan(&alloc.AllocID, &alloc.UserID, &alloc.AllocType, &alloc.Region, &alloc.Ctrhost, &alloc.CreatedAt, &alloc.StripeCustomerID, &alloc.BillingEmail)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &alloc, nil
}

// getAllocsByHost gets all allocations assigned to a specific docker host
func (s *Server) getAllocsByHost(ctx context.Context, ctrhost string) ([]*Alloc, error) {
	var allocs []*Alloc
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		rows, err := rx.Query(`
			SELECT alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email
			FROM allocs
			WHERE ctrhost = ?
		`, ctrhost)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var a Alloc
			err := rows.Scan(
				&a.AllocID, &a.UserID, &a.AllocType, &a.Region, &a.Ctrhost,
				&a.CreatedAt, &a.StripeCustomerID, &a.BillingEmail,
			)
			if err != nil {
				return err
			}
			allocs = append(allocs, &a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return allocs, nil
}

// getBoxesByHost gets all boxes (machines) that should be on a specific ctrhost
func (s *Server) getBoxesByHost(ctx context.Context, ctrhost string) ([]*exedb.Box, error) {
	var boxes []*exedb.Box
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		// Join boxes with allocs to find boxes on this host
		rows, err := rx.Query(`
			SELECT
				b.id, b.alloc_id, b.name, b.status, b.image, b.container_id,
				b.created_by_user_id, b.created_at, b.updated_at, b.last_started_at,
				b.routes, b.ssh_server_identity_key, b.ssh_authorized_keys,
				b.ssh_ca_public_key, b.ssh_host_certificate, b.ssh_client_private_key,
				b.ssh_port, b.ssh_user
			FROM boxes b
			INNER JOIN allocs a ON b.alloc_id = a.alloc_id
			WHERE a.ctrhost = ? AND b.status != 'failed'
		`, ctrhost)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var b exedb.Box
			err := rows.Scan(
				&b.ID, &b.AllocID, &b.Name, &b.Status, &b.Image, &b.ContainerID,
				&b.CreatedByUserID, &b.CreatedAt, &b.UpdatedAt, &b.LastStartedAt,
				&b.Routes, &b.SSHServerIdentityKey, &b.SSHAuthorizedKeys,
				&b.SSHCAPublicKey, &b.SSHHostCertificate, &b.SSHClientPrivateKey,
				&b.SSHPort, &b.SSHUser,
			)
			if err != nil {
				return err
			}
			boxes = append(boxes, &b)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return boxes, nil
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
	return strings.Contains(domain, ".")
}

// Start starts HTTP, HTTPS (if configured), and SSH servers
// syncAllocsWithHosts synchronizes allocations between the database and container hosts
// This ensures that:
// 1. All allocations in the database have their networks created on hosts
// 2. Any allocations not in the database are removed from hosts
func (s *Server) syncAllocsWithHosts(ctx context.Context) error {
	// Get the list of container hosts
	hosts := s.containerManager.GetHosts()
	if len(hosts) == 0 {
		slog.Warn("No container hosts available for alloc sync")
		return nil
	}

	slog.Info("Starting allocation sync with container hosts", "hostCount", len(hosts))

	// Process each host
	for _, host := range hosts {
		if err := s.syncAllocsForHost(ctx, host); err != nil {
			slog.Error("Failed to sync allocations for host", "host", host, "error", err)
			// Continue with other hosts even if one fails
		}
	}

	slog.Info("Allocation sync completed")
	return nil
}

// syncAllocsForHost synchronizes allocations for a specific container host
func (s *Server) syncAllocsForHost(ctx context.Context, host string) error {
	// Get allocations from the database that should be on this host
	dbAllocs, err := s.getAllocsByHost(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to get allocations from database: %w", err)
	}

	// Get allocations currently on the host
	hostAllocIDs, err := s.containerManager.ListAllocs(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to list allocations on host: %w", err)
	}

	// Create maps for easier lookup
	dbAllocMap := make(map[string]*Alloc)
	for _, alloc := range dbAllocs {
		// Truncate allocID to match network naming (max 12 chars)
		nameLen := len(alloc.AllocID)
		if nameLen > 12 {
			nameLen = 12
		}
		truncatedID := alloc.AllocID[:nameLen]
		dbAllocMap[truncatedID] = alloc
	}

	hostAllocMap := make(map[string]bool)
	for _, allocID := range hostAllocIDs {
		hostAllocMap[allocID] = true
	}

	// Create allocations that are in DB but not on host
	for truncatedID, alloc := range dbAllocMap {
		if !hostAllocMap[truncatedID] {
			// Create the allocation on the host (now a no-op but kept for compatibility)
			slog.Info("Creating missing allocation on host", "allocID", alloc.AllocID, "host", host)
			if err := s.containerManager.CreateAlloc(ctx, alloc.AllocID); err != nil {
				slog.Error("Failed to create allocation on host", "allocID", alloc.AllocID, "host", host, "error", err)
				// Continue with other allocations
			}
		}
	}

	// Delete allocations that are on host but not in DB
	for allocID := range hostAllocMap {
		if _, exists := dbAllocMap[allocID]; !exists {
			slog.Info("Removing orphaned allocation from host", "allocID", allocID, "host", host)
			if err := s.containerManager.DeleteAlloc(ctx, allocID, host); err != nil {
				slog.Error("Failed to delete allocation from host", "allocID", allocID, "host", host, "error", err)
				// Continue with other allocations
			}
		}
	}

	return nil
}

// syncContainersWithHosts synchronizes containers between the database and container hosts
// This ensures that:
// 1. All boxes in the database have their containers running on hosts
// 2. Containers are restarted if they exist but aren't running
// 3. Broken containers are marked as failed if their disk is missing
// 4. Any containers not in the database are removed from hosts
func (s *Server) syncContainersWithHosts(ctx context.Context) error {
	// Get the list of container hosts
	hosts := s.containerManager.GetHosts()
	if len(hosts) == 0 {
		slog.Warn("No container hosts available for container sync")
		return nil
	}

	slog.Info("Starting container sync with container hosts", "hostCount", len(hosts))

	// Process each host
	for _, host := range hosts {
		if err := s.syncContainersForHost(ctx, host); err != nil {
			slog.Error("Failed to sync containers for host", "host", host, "error", err)
			// Continue with other hosts even if one fails
		}
	}

	slog.Info("Container sync completed")
	return nil
}

// syncContainersForHost synchronizes containers for a specific container host
func (s *Server) syncContainersForHost(ctx context.Context, host string) error {
	// Get boxes from the database that should be on this host
	dbBoxes, err := s.getBoxesByHost(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to get boxes from database: %w", err)
	}

	// Get containers currently on the host
	hostContainers, err := s.containerManager.ListAllContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers on host: %w", err)
	}

	// Create maps for easier lookup
	dbBoxMap := make(map[string]*exedb.Box)
	for _, box := range dbBoxes {
		if box.ContainerID != nil && *box.ContainerID != "" {
			dbBoxMap[*box.ContainerID] = box
		}
	}

	// Map of container names to containers for orphan detection
	hostContainerMap := make(map[string]*container.Container)
	for _, c := range hostContainers {
		if c.DockerHost == host {
			hostContainerMap[c.ID] = c
		}
	}

	// Check each box that should have a container
	for _, box := range dbBoxes {
		// Skip boxes without container IDs (not yet created)
		if box.ContainerID == nil || *box.ContainerID == "" {
			continue
		}

		containerID := *box.ContainerID
		hostContainer, exists := hostContainerMap[containerID]

		if !exists {
			// Container doesn't exist on host but should - check for persistent disk
			diskPath := s.DataPath(fmt.Sprintf("exed/containers/box-%d", box.ID))

			// Use the VerifyDisk method for proper disk validation
			nerdctlMgr := s.containerManager
			{
				diskExists, err := nerdctlMgr.VerifyDisk(ctx, host, box.ID)
				if err != nil {
					slog.Error("Failed to verify disk", "box", box.Name, "error", err)
					continue
				}

				if !diskExists {
					// Disk missing - mark box as broken
					slog.Error("Container disk missing, marking as failed", "box", box.Name, "diskPath", diskPath)
					if err := s.updateBoxStatus(ctx, box.ID, "failed"); err != nil {
						slog.Error("Failed to mark box as failed", "box", box.Name, "error", err)
					}
				} else {
					// Disk exists - recreate container
					slog.Info("Recreating container from persistent disk", "box", box.Name, "boxID", box.ID)

					// Reconstruct SSH keys from the box record
					var existingSSHKeys *container.ContainerSSHKeys
					if len(box.SSHServerIdentityKey) > 0 {
						existingSSHKeys = &container.ContainerSSHKeys{
							ServerIdentityKey: string(box.SSHServerIdentityKey),
							AuthorizedKeys:    *box.SSHAuthorizedKeys,
							CAPublicKey:       *box.SSHCAPublicKey,
							HostCertificate:   *box.SSHHostCertificate,
							ClientPrivateKey:  string(box.SSHClientPrivateKey),
							SSHPort:           int(*box.SSHPort),
						}
						slog.Info("Using existing SSH keys from database for container recreation", "boxID", box.ID)
					}

					// Create a new container using the existing disk
					req := &container.CreateContainerRequest{
						AllocID: box.AllocID,
						Name:    box.Name,
						BoxID:   box.ID,
						Image:   box.Image,
						// We don't have size info stored, use default
						Size:            "small",
						ExistingSSHKeys: existingSSHKeys,
					}

					// Recreate the container (CreateContainer will reuse the existing disk)
					slog.Info("Calling CreateContainer to recreate from disk", "boxID", box.ID, "oldContainerID", containerID)
					newContainer, err := nerdctlMgr.CreateContainer(ctx, req)
					if err != nil {
						slog.Error("Failed to recreate container from disk", "box", box.Name, "error", err)
						if err := s.updateBoxStatus(ctx, box.ID, "failed"); err != nil {
							slog.Error("Failed to mark box as failed", "box", box.Name, "error", err)
						}
					} else {
						// Update box with new container ID
						if err := s.db.Exec(ctx, `UPDATE boxes SET container_id = ?, status = 'running' WHERE id = ?`,
							newContainer.ID, box.ID); err != nil {
							slog.Error("Failed to update box with new container ID", "box", box.Name, "error", err)
						} else {
							slog.Info("Successfully recreated container from disk",
								"box", box.Name,
								"oldContainerID", containerID,
								"newContainerID", newContainer.ID)
						}
					}
				}
			}
		} else if hostContainer.Status != "running" && box.Status == "running" {
			// Container exists but isn't running when it should be
			slog.Info("Restarting stopped container", "box", box.Name, "containerID", containerID)
			if err := s.containerManager.StartContainer(ctx, box.AllocID, containerID); err != nil {
				slog.Error("Failed to restart container", "box", box.Name, "error", err)
				if err := s.updateBoxStatus(ctx, box.ID, "stopped"); err != nil {
					slog.Error("Failed to update box status", "box", box.Name, "error", err)
				}
			}
		} else if hostContainer.Status == "running" && box.Status != "running" {
			// Update database to reflect actual container state
			slog.Info("Updating box status to match container", "box", box.Name, "status", hostContainer.Status)
			if err := s.updateBoxStatus(ctx, box.ID, string(hostContainer.Status)); err != nil {
				slog.Error("Failed to update box status", "box", box.Name, "error", err)
			}
		}

		// Remove from map to track orphans
		delete(hostContainerMap, containerID)
	}

	// Handle containers that are on host but not in DB (potential orphans)
	for containerID, c := range hostContainerMap {
		// Only process containers managed by exe (check label)
		if c.AllocID != "" {
			// Extract box ID from container name if possible for additional verification
			// Container names are in format: exe-<allocID>-<boxName>
			// We could potentially recover these if we can find the box ID

			// For now, just log orphaned containers but don't delete immediately
			// This provides a grace period and allows for manual investigation
			slog.Warn("Found potentially orphaned container on host - NOT deleting automatically",
				"containerID", containerID,
				"name", c.Name,
				"allocID", c.AllocID,
				"host", host,
				"status", c.Status)

			// TODO: In the future, we could:
			// 1. Track when orphans were first detected
			// 2. Only delete after a grace period (e.g., 24 hours)
			// 3. Try to match with recently deleted boxes in deleted_boxes table
			// 4. Send alerts about orphaned containers
		}
	}

	return nil
}

// updateBoxStatus updates the status of a box in the database
func (s *Server) updateBoxStatus(ctx context.Context, boxID int, status string) error {
	return s.db.Exec(ctx, `
		UPDATE boxes
		SET status = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, status, boxID)
}

// syncHost performs complete host synchronization including both allocs and containers
func (s *Server) syncHost(ctx context.Context, host string) error {
	// First sync allocations
	if err := s.syncAllocsForHost(ctx, host); err != nil {
		return fmt.Errorf("failed to sync allocations: %w", err)
	}

	// Then sync containers
	if err := s.syncContainersForHost(ctx, host); err != nil {
		return fmt.Errorf("failed to sync containers: %w", err)
	}

	return nil
}

func (s *Server) Start() error {
	s.mu.Lock()
	s.stopping = false
	s.mu.Unlock()

	// Create a cancellable context for startup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start HTTP server in a goroutine if configured
	if s.httpLn.ln != nil {
		go func() {
			slog.Debug("HTTP server starting", "addr", s.httpLn)
			if err := s.httpServer.Serve(s.httpLn.ln); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP server startup failed", "error", err)
				cancel()
			}
		}()
	}

	// Start HTTPS server in a goroutine if configured
	if s.httpsLn.ln != nil {
		go func() {
			slog.Info("HTTPS server starting with Let's Encrypt for exe.dev", "addr", s.httpsLn)
			if err := s.httpsServer.ServeTLS(s.httpsLn.ln, "", ""); err != nil && err != http.ErrServerClosed {
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

	// Start SSH server in a goroutine
	go func() {
		billing := billing.New(s.db)
		sshServer := NewSSHServer(s, billing)
		if err := sshServer.Start(s.sshLn.ln); err != nil {
			slog.Error("SSH server startup failed", "error", err)
			cancel()
		}
	}()

	// Start piper plugin server in a goroutine
	slog.Info("piper plugin server listening", "addr", s.pluginLn.addr, "port", s.pluginLn.tcp.Port)
	s.piperPlugin = NewPiperPlugin(s, s.sshLn.tcp.Port)
	go func() {
		if err := s.piperPlugin.Serve(s.pluginLn.ln); err != nil {
			slog.Error("Piper plugin server startup failed", "error", err)
			cancel()
		}
	}()

	if s.devMode == "local" {
		// In dev mode, automatically start sshpiper if not already running
		go s.autoStartSSHPiper(ctx)

		slog.Info("SSH server started in local dev mode. Connect with:")
		slog.Info(fmt.Sprintf("  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %v localhost", s.sshLn.tcp.Port))
	}

	// Sync allocations and containers with container hosts before accepting connections
	if s.containerManager != nil {
		// First sync allocations (networks)
		if err := s.syncAllocsWithHosts(ctx); err != nil {
			slog.Error("Failed to sync allocations with container hosts", "error", err)
			// Continue anyway - we can sync later
		}

		// Then sync containers
		if err := s.syncContainersWithHosts(ctx); err != nil {
			slog.Error("Failed to sync containers with container hosts", "error", err)
			// Continue anyway - we can sync later
		}
	}

	// Start tag resolver and host updater for keeping container images fresh
	if s.tagResolver != nil && s.hostUpdater != nil {
		slog.Info("Starting tag resolver for image freshness management")
		s.tagResolver.Start(ctx)
		s.hostUpdater.Start(ctx)
	}

	// Wait for interrupt signal or startup failure
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	s.ready.Done()

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

// autoStartSSHPiper automatically starts sshpiper.sh in dev mode if that port isn't listening
func (s *Server) autoStartSSHPiper(ctx context.Context) {
	// Check if sshpiper is already running on the specified port
	if s.isPortListening(fmt.Sprintf("localhost:%d", s.piperdPort)) {
		slog.Info("sshpiper already running", "port", s.piperdPort)
		return
	}

	// Use the actual piper TCP address
	if s.pluginLn.tcp == nil {
		slog.Error("Piper TCP address not available")
		return
	}

	piperPluginAddr := fmt.Sprintf("localhost:%d", s.pluginLn.tcp.Port)

	// First, wait for the piper plugin to be ready
	if !s.waitForPort(ctx, piperPluginAddr, 30*time.Second) {
		slog.Error("Timed out waiting for piper plugin to start", "addr", piperPluginAddr)
		return
	}

	// Start sshpiper.sh with the piper plugin port
	slog.Info("Starting sshpiper.sh automatically in dev mode", "piperPluginPort", s.pluginLn.tcp.Port)

	cmd := exec.CommandContext(ctx, "./sshpiper.sh", fmt.Sprint(s.pluginLn.tcp.Port))
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
	conn, err := net.DialTimeout("tcp", address, 10*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Database helper methods

// getEmailBySSHKey checks if an SSH key is registered and returns the associated email
func (s *Server) GetEmailBySSHKey(ctx context.Context, publicKeyStr string) (email string, verified bool, err error) {
	// Check if key exists in ssh_keys (all keys there are verified)
	err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT u.email
		FROM ssh_keys s
		JOIN users u ON s.user_id = u.user_id
		WHERE s.public_key = ?`,
			publicKeyStr).Scan(&email)
	})

	if errors.Is(err, sql.ErrNoRows) {
		// Check if key exists in pending_ssh_keys (unverified)
		err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow(`
			SELECT user_email
			FROM pending_ssh_keys
			WHERE public_key = ?`,
				publicKeyStr).Scan(&email)
		})
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return email, false, err // Key exists in pending_ssh_keys, so not verified
	}
	return email, true, err // Key exists in ssh_keys, so verified
}

// getUserByPublicKey retrieves a user by their SSH public key
func (s *Server) getUserByPublicKey(ctx context.Context, publicKeyStr string) (*User, error) {
	var user User

	// Find user by their SSH public key
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT u.user_id, u.email, u.created_at
		FROM users u
		JOIN ssh_keys s ON u.user_id = s.user_id
		WHERE s.public_key = ?`,
			publicKeyStr).Scan(&user.UserID, &user.Email, &user.CreatedAt)
	})

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &user, err
}

// Note: allocateIPRange function has been removed since we no longer use per-allocation IP ranges.
// All containers now use the default bridge network with port isolation.

// createUserWithAlloc creates a new user with their resource allocation
func (s *Server) createUserWithAlloc(ctx context.Context, publicKey, email string) error {
	var allocID string

	// First create the user and allocation in the database
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
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
		allocID, err = generateAllocID()
		if err != nil {
			return err
		}

		// Select a container host for this alloc
		ctrhost := s.selectCtrhostForNewAlloc()

		// Create alloc for the user (no longer needs IP range)
		_, err = tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, billing_email)
			VALUES (?, ?, ?, ?, ?, ?)`,
			allocID, userID, AllocTypeMedium, RegionAWSUSWest2, ctrhost, email)
		return err
	})
	if err != nil {
		return err
	}

	// After successful database creation, notify the container manager
	// (CreateAlloc is now a no-op but kept for compatibility)
	if s.containerManager != nil {
		if err := s.containerManager.CreateAlloc(ctx, allocID); err != nil {
			// Log the error but don't fail user creation
			slog.Error("failed to create allocation", "allocID", allocID, "error", err)
		}
	}

	return nil
}

// getUserAlloc gets the alloc for a user (creates one if it doesn't exist)
func (s *Server) getUserAlloc(ctx context.Context, userID string) (*Alloc, error) {
	var alloc Alloc
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT alloc_id, user_id, alloc_type, region, ctrhost, created_at, stripe_customer_id, billing_email
		FROM allocs
		WHERE user_id = ?
		LIMIT 1`,
			userID).Scan(&alloc.AllocID, &alloc.UserID, &alloc.AllocType, &alloc.Region,
			&alloc.Ctrhost, &alloc.CreatedAt, &alloc.StripeCustomerID, &alloc.BillingEmail)
	})

	if errors.Is(err, sql.ErrNoRows) {
		// User exists but has no alloc yet - create one
		allocID, err := generateAllocID()
		if err != nil {
			return nil, err
		}

		// Get user's email for billing
		var email string
		err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow(`SELECT email FROM users WHERE user_id = ?`, userID).Scan(&email)
		})
		if err != nil {
			return nil, err
		}

		ctrhost := s.selectCtrhostForNewAlloc()

		err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			_, err = tx.Exec(`
				INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, billing_email)
				VALUES (?, ?, ?, ?, ?, ?)`,
				allocID, userID, AllocTypeMedium, RegionAWSUSWest2, ctrhost, email)
			return err
		})
		if err != nil {
			return nil, err
		}

		return s.getUserAlloc(ctx, userID)
	}

	if err != nil {
		return nil, err
	}

	return &alloc, nil
}

// selectCtrhostForNewAlloc selects the best container host for a new alloc
func (s *Server) selectCtrhostForNewAlloc() string {
	// Get the list of available hosts from the container manager
	if s.containerManager != nil {
		hosts := s.containerManager.GetHosts()
		if len(hosts) > 0 {
			// For now, just use the first available host
			// In the future, this could do load balancing
			return hosts[0]
		}
	}
	// Fallback to "local" if no container manager or no hosts
	return "local"
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
func (s *Server) createUser(ctx context.Context, publicKey, email string) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
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

		ctrhost := s.selectCtrhostForNewAlloc()

		// Create the allocation (no longer needs IP range)
		_, err = tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, billing_email)
			VALUES (?, ?, ?, ?, ?, ?)`,
			allocID, userID, AllocTypeMedium, RegionAWSUSWest2, ctrhost, email)
		return err
	})
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

	// Stop tag resolver and host updater
	if s.tagResolver != nil {
		s.tagResolver.Stop()
	}
	if s.hostUpdater != nil {
		s.hostUpdater.Stop()
	}

	// Close database connection
	if s.db != nil {
		s.db.Close()
	}

	slog.Debug("Servers stopped")
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
func (s *Server) lookupEphemeralProxyKey(proxyKey ssh.PublicKey) ([]byte, string, bool) {
	// Get the original user key from the piper plugin
	// The piper plugin is always configured when SSH proxy is enabled
	if s.piperPlugin == nil {
		slog.Error("Piper plugin not configured but proxy key received")
		return nil, "", false
	}

	proxyFingerprint := s.GetPublicKeyFingerprint(proxyKey)
	slog.Debug("Looking up proxy key", "fingerprint", proxyFingerprint[:16])

	originalUserKey, localAddress, exists := s.piperPlugin.lookupOriginalUserKey(proxyFingerprint)
	if !exists {
		slog.Debug("Proxy key not found or expired", "fingerprint", proxyFingerprint[:16])
		return nil, "", false // Not a proxy key or expired
	}

	slog.Debug("Found original user key for proxy key", "key_length", len(originalUserKey), "local_address", localAddress, "proxy_fingerprint", proxyFingerprint[:16])
	return originalUserKey, localAddress, true
}

// authenticateProxyUser authenticates a user through an ephemeral proxy connection
func (s *Server) authenticateProxyUser(ctx context.Context, username string, originalUserKeyBytes []byte) (*ssh.Permissions, error) {
	// Parse the original user's public key
	originalUserKey, err := ssh.ParsePublicKey(originalUserKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse original user key: %v", err)
	}

	originalFingerprint := s.GetPublicKeyFingerprint(originalUserKey)
	originalKeyStr := string(ssh.MarshalAuthorizedKey(originalUserKey))

	slog.Debug("Authenticating original user", "fingerprint", originalFingerprint, "username", username)

	// Look up the user by their original public key
	email, verified, err := s.GetEmailBySSHKey(ctx, originalKeyStr)
	if err != nil {
		slog.Error("Database error checking SSH key", "fingerprint", originalFingerprint, "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has an alloc
		user, err := s.GetUserByEmail(ctx, email)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.Error("Database error getting user", "email", email, "error", err)
		}

		if user != nil {
			alloc, err := s.getUserAlloc(ctx, user.UserID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
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
func (s *Server) authenticateProxyUserWithLocalAddress(ctx context.Context, username string, originalUserKeyBytes []byte, localAddress string) (*ssh.Permissions, error) {
	slog.Info("authenticateProxyUserWithLocalAddress", "username", username, "localAddress", localAddress, "keyBytes", len(originalUserKeyBytes))

	// Check for special container-logs username format
	if strings.HasPrefix(username, "container-logs:") {
		slog.Info("Detected special container-logs username, bypassing normal auth", "username", username)
		// This is a special request to show container logs
		// We don't need to authenticate the user normally, just pass through
		// The SSH server will handle this specially
		return &ssh.Permissions{
			Extensions: map[string]string{
				"registered": "true",
				"proxy_user": username,
				"public_key": "", // Empty key for special log display
			},
		}, nil
	}

	return s.authenticateProxyUser(ctx, username, originalUserKeyBytes)
}

// generateUserID creates a new user ID with "usr" prefix + 13 random characters
func generateUserID() (string, error) {
	randomPart := cryptorand.Text()
	if len(randomPart) < 13 {
		return "", fmt.Errorf("random text too short: %d", len(randomPart))
	}
	return "usr" + randomPart[:13], nil
}

// getUserIDByPublicKey gets user_id from an SSH public key
func (s *Server) getUserIDByPublicKey(ctx context.Context, publicKey ssh.PublicKey) (string, error) {
	var userID string
	publicKeyStr := string(ssh.MarshalAuthorizedKey(publicKey))
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT user_id FROM ssh_keys
		WHERE public_key = ?
		LIMIT 1
	`, publicKeyStr).Scan(&userID)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("user not found for public key")
		}
		return "", fmt.Errorf("database error: %w", err)
	}
	return userID, nil
}

// GetUserByEmail retrieves a user by their email address
func (s *Server) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	var user User
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
		SELECT user_id, email, created_at
		FROM users
		WHERE email = ?
	`, email).Scan(&user.UserID, &user.Email, &user.CreatedAt)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("database error: %w", err)
	}
	return &user, nil
}

// GetBoxSSHDetails retrieves SSH connection details from the boxes table
func (s *Server) GetBoxSSHDetails(ctx context.Context, boxID int) (*exedb.SSHDetails, error) {
	var port sql.NullInt64
	var privateKey sql.NullString
	var serverIdentityKey sql.NullString
	var ctrhost sql.NullString
	var sshUser sql.NullString

	query := `SELECT m.ssh_port, m.ssh_client_private_key, m.ssh_server_identity_key, a.ctrhost, m.ssh_user
		FROM boxes m
		JOIN allocs a ON m.alloc_id = a.alloc_id
		WHERE m.id = ?`
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(query, boxID).Scan(&port, &privateKey, &serverIdentityKey, &ctrhost, &sshUser)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query box SSH details: %v", err)
	}

	if !port.Valid || port.Int64 == 0 || !privateKey.Valid || privateKey.String == "" {
		// SSH not set up for this box - this is for containers created before SSH support
		// TODO: Remove this code once all legacy containers are migrated
		log.Printf("Box %d missing SSH setup, initializing SSH on container", boxID)
		err := s.setupContainerSSH(ctx, boxID)
		if err != nil {
			return nil, fmt.Errorf("failed to setup SSH on legacy container: %v", err)
		}

		// Re-query after setup
		err = s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.QueryRow(query, boxID).Scan(&port, &privateKey, &serverIdentityKey, &ctrhost)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to re-query box SSH details after setup: %v", err)
		}
	}

	sshPort := int(port.Int64)
	if sshPort <= 0 {
		return nil, fmt.Errorf("invalid SSH port for box: %d", sshPort)
	}

	if privateKey.String == "" {
		return nil, fmt.Errorf("no SSH private key available for box after setup")
	}

	// Derive host public key from server identity key if available
	var hostKey string
	if serverIdentityKey.Valid && serverIdentityKey.String != "" {
		// Parse the server identity private key and extract the public key
		privKey, err := ssh.ParsePrivateKey([]byte(serverIdentityKey.String))
		if err == nil {
			hostKey = string(ssh.MarshalAuthorizedKey(privKey.PublicKey()))
		}
		// If parsing fails, we'll just use empty host key (fallback to no validation)
	}

	var ctrhostPtr *string
	if ctrhost.Valid && ctrhost.String != "" {
		ctrhostPtr = &ctrhost.String
	}

	// Default to root user if not specified
	user := "root"
	if sshUser.Valid && sshUser.String != "" {
		user = sshUser.String
	}

	return &exedb.SSHDetails{
		Port:       sshPort,
		PrivateKey: privateKey.String,
		HostKey:    hostKey,
		Ctrhost:    ctrhostPtr,
		User:       user,
	}, nil
}

// SSHIdentityKeyForBox implements boxKeyAuthority interface for llmgateway
func (s *Server) SSHIdentityKeyForBox(ctx context.Context, name string) (string, error) {
	box, err := s.getBoxByName(ctx, name)
	if err != nil {
		return "", fmt.Errorf("failed to find box %s: %w", name, err)
	}
	if len(box.SSHServerIdentityKey) == 0 {
		return "", fmt.Errorf("box %s has no SSH server identity key", name)
	}
	// Parse the private key to extract the public key
	privateKey, err := ssh.ParsePrivateKey(box.SSHServerIdentityKey)
	if err != nil {
		return "", fmt.Errorf("failed to parse SSH server identity key for box %s: %w", name, err)
	}
	// Return the public key in authorized_keys format
	return string(ssh.MarshalAuthorizedKey(privateKey.PublicKey())), nil
}

// setupContainerSSH sets up SSH on a legacy container that was created before SSH support
// TODO: Remove this method once all legacy containers are migrated to have SSH
func (s *Server) setupContainerSSH(ctx context.Context, boxID int) error {
	// Get box details
	var containerID, userFingerprint, teamName, boxName, image string
	err := s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(
			`SELECT container_id, created_by_user_id, team_name, name, image FROM boxes WHERE id = ?`,
			boxID,
		).Scan(&containerID, &userFingerprint, &teamName, &boxName, &image)
	})
	if err != nil {
		return fmt.Errorf("failed to get box details: %v", err)
	}

	if containerID == "" {
		return fmt.Errorf("box has no container ID")
	}

	// Generate SSH keys for this container
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		return fmt.Errorf("failed to generate SSH keys: %v", err)
	}

	// Update database with SSH keys
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			UPDATE boxes SET
				ssh_server_identity_key = ?, ssh_authorized_keys = ?, ssh_ca_public_key = ?,
				ssh_host_certificate = ?, ssh_client_private_key = ?, ssh_port = ?
			WHERE id = ?
		`, sshKeys.ServerIdentityKey, sshKeys.AuthorizedKeys, sshKeys.CAPublicKey,
			sshKeys.HostCertificate, sshKeys.ClientPrivateKey, sshKeys.SSHPort, boxID)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to update box SSH keys: %v", err)
	}

	log.Printf("SSH setup completed for box %d", boxID)
	return nil
}
