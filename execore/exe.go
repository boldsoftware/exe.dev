// Package exe implements the bulk of the exed server.
package execore

import (
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html/template"
	"log"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"exe.dev/boxname"
	"exe.dev/container"
	"exe.dev/ctrhosttest"
	docspkg "exe.dev/docs"
	"exe.dev/exedb"
	"exe.dev/ghuser"
	"exe.dev/route53"
	"exe.dev/sqlite"
	"exe.dev/sshpool2"
	"exe.dev/tagresolver"
	templatespkg "exe.dev/templates"
	"github.com/keighl/postmark"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"
)

//go:embed static
var staticFS embed.FS

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

const (
	// Timeout for long-running operations like box creation and Shelley prompts
	longOperationTimeout = 30 * time.Minute
	// IP shards are 1-based. (The zero value is intentionally invalid.)
	// maxIPShard is the largest available shard IDs.
	// Shards map to IP public IP addresses: sNNN.exe.dev, so ranging from s001.exe.dev to s025.exe.dev.
	// maxIPShard must match the DB CHECK constraint.
	maxIPShard = 25
)

// BoxDisplayInfo represents a box with additional display information
type BoxDisplayInfo struct {
	exedb.Box
	SSHCommand      string
	ProxyURL        string
	TerminalURL     string
	ShelleyURL      string
	VSCodeURL       template.URL
	ProxyPort       int
	ProxyShare      string
	SharedUserCount int64              // Number of users box is shared with (pending + active)
	ShareLinkCount  int64              // Number of active share links
	TotalShareCount int64              // Total shares (users + links)
	SharedEmails    []string           // List of emails box is shared with
	ShareLinks      []BoxShareLinkInfo // List of share links with URLs
}

type BoxShareLinkInfo struct {
	Token string
	URL   string
}

// UserPageData represents the data for the user dashboard page
type UserPageData struct {
	User        exedb.User
	SSHKeys     []SSHKey
	Boxes       []BoxDisplayInfo
	SharedBoxes []SharedBoxDisplayInfo
	ActivePage  string
	IsLoggedIn  bool
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
	PairingCode  string
	CompleteChan chan struct{}
	CreatedAt    time.Time
	IsNewAccount bool
}

// MagicSecret represents a temporary authentication secret for proxy magic URLs
type MagicSecret struct {
	UserID      string
	BoxName     string // Direct box name instead of team
	RedirectURL string
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// Server implements both HTTP and SSH server functionality for exe.dev
type Server struct {
	httpLn     *listener
	proxyLns   []*listener // Additional listeners for proxy ports
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

	httpServer  *http.Server
	httpsServer *http.Server
	sshConfig   *ssh.ServerConfig
	sshHostKey  ssh.Signer
	sshServer   *SSHServer

	certManager         *autocert.Manager
	wildcardCertManager *route53.WildcardCertManager
	dnsProvider         *route53.DNSProvider
	lookupCNAMEFunc     func(context.Context, string) (string, error) // for tests
	stopCobble          func()

	// Tailscale HTTPS (preloaded at startup)
	tsCert   *tls.Certificate
	tsDomain string

	// Piper plugin for SSH proxy authentication
	piperPlugin *PiperPlugin

	// Database
	db *sqlite.DB

	// Container management
	containerManager *container.NerdctlManager
	tagResolver      *tagresolver.TagResolver
	hostUpdater      *tagresolver.HostUpdater

	// SSH connection pooling for HTTP proxying
	sshPool *sshpool2.Pool

	// In-memory state for active sessions (these don't need persistence)
	emailVerificationsMu sync.RWMutex
	emailVerifications   map[string]*EmailVerification // token -> email verification
	magicSecretsMu       sync.RWMutex
	magicSecrets         map[string]*MagicSecret // secret -> magic secret with expiration
	creationStreamsMu    sync.Mutex
	creationStreams      map[creationStreamKey]*CreationStream // (userID, hostname) -> creation stream

	// GitHub keys -> GitHub user info client
	// For expedited onboarding for existing GitHub users who show up with their GitHub SSH key
	githubUser *ghuser.Client

	// Email service
	postmarkClient *postmark.Client
	fakeHTTPEmail  string // fake HTTP email server URL for sending emails (for e2e tests)

	devMode string // Development mode: "" (production) or "local" (Docker) or "test" for test mode

	// Metrics
	metricsRegistry *prometheus.Registry
	sshMetrics      *SSHMetrics

	// Data isolation
	dataSubdir string // subdirectory under /data for container isolation

	docs *docspkg.Handler

	// HTML templates (parsed at startup)
	templates *template.Template

	stopping atomic.Bool

	log *slog.Logger
}

func (s *Server) slog() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

// A listener is a listening port, along with address information.
// It exists to do the bookkeeping, particularly when starting a server with an address of :0.
type listener struct {
	origAddr string       // original requested listening address
	ln       net.Listener // listener (nil if not started yet)
	addr     string       // resolved listening address (e.g. if origAddr was :0)
	tcp      *net.TCPAddr // resolved TCP listening address
}

func (l *listener) String() string {
	if l == nil {
		return "<nil>"
	}
	return fmt.Sprintf("<tcp %sstarted addr=%q orig=%q>",
		func() string {
			if l.ln != nil {
				return ""
			}
			return "un"
		}(),
		l.addr,
		l.origAddr,
	)
}

func unusedListener(addr string) *listener {
	return &listener{origAddr: addr}
}

func startListener(slog *slog.Logger, typ, addr string) (*listener, error) {
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

func runMigrations(slog *slog.Logger, dbPath string) error {
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database for migrations: %w", err)
	}
	defer rawDB.Close()
	// Set busy_timeout to handle database lock contention during restarts
	if _, err := rawDB.Exec("PRAGMA busy_timeout=1000"); err != nil {
		return fmt.Errorf("failed to set busy_timeout: %w", err)
	}
	if err := exedb.RunMigrations(slog, rawDB); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	slog.Debug("database migrations complete")
	return nil
}

// NewServer creates a new Server instance with database and container management
func NewServer(slog *slog.Logger, httpAddr, httpsAddr, sshAddr, pluginAddr, dbPath, devMode, fakeEmailServer string, piperdPort int, ghWhoAmIPath string, containerdAddresses []string) (*Server, error) {
	// Run db migrations with a raw connection (not a pool).
	if err := runMigrations(slog, dbPath); err != nil {
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	const nReaders = 16
	db, err := sqlite.New(dbPath, nReaders)
	if err != nil {
		return nil, fmt.Errorf("failed to create sqlite connection pool: %w", err)
	}

	slog.Debug("opened database connection pool", "dbPath", dbPath, "nReaders", nReaders)

	// Initialize data subdirectory for container isolation
	dataSubdir, err := exedb.InitDataSubdir(slog, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize data subdir: %w", err)
	}

	// Initialize Postmark client
	postmarkAPIKey := os.Getenv("POSTMARK_API_KEY")
	var postmarkClient *postmark.Client
	if postmarkAPIKey != "" {
		postmarkClient = postmark.NewClient(postmarkAPIKey, "")
	} else {
		slog.Info("POSTMARK_API_KEY not set, email verification will not work")
	}

	// Initialize GitHub User lookup client
	ghu, err := ghuser.New(os.Getenv("GITHUB_TOKEN"), ghWhoAmIPath)
	if err != nil {
		slog.Warn("failed to create GitHub user key lookup client", "error", err)
	}

	var baseURL string
	var httpLn *listener
	var httpsLn *listener
	if httpsAddr != "" {
		// HTTPS is configured, use https://exe.dev
		baseURL = "https://exe.dev"
		httpsLn, err = startListener(slog, "https", httpsAddr)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to listen on HTTPS address %q: %w", httpsAddr, err)
		}
	} else {
		httpsLn = unusedListener(httpsAddr)
	}

	httpLn, err = startListener(slog, "http", httpAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on HTTP address %q: %w", httpAddr, err)
	}
	// No HTTPS, use http://localhost with the HTTP port
	baseURL = fmt.Sprintf("http://localhost:%d", httpLn.tcp.Port)
	slog.Info("http server listening", "addr", httpLn.tcp.String(), "port", httpLn.tcp.Port)

	sshLn, err := startListener(slog, "ssh", sshAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on SSH address %q: %w", sshAddr, err)
	}

	pluginLn, err := startListener(slog, "plugin", pluginAddr)
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
			IsProduction:         devMode == "", // Production when devMode is empty
		}
		if httpLn != nil && httpLn.tcp != nil {
			config.ExedListeningPort = httpLn.tcp.Port
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

	includeUnpublishedDocs := devMode != ""
	docsStore, err := docspkg.Load(includeUnpublishedDocs)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("loading docs: %w", err)
	}
	docsHandler := docspkg.NewHandler(docsStore, includeUnpublishedDocs)

	// Parse all HTML templates at startup
	tmpl, err := templatespkg.Parse()
	if err != nil {
		db.Close()
		return nil, err
	}

	s := &Server{
		httpLn:             httpLn,
		httpsLn:            httpsLn,
		sshLn:              sshLn,
		pluginLn:           pluginLn,
		piperdPort:         piperdPort,
		BaseURL:            baseURL,
		db:                 db,
		containerManager:   containerManager,
		tagResolver:        tagResolverInstance,
		hostUpdater:        hostUpdaterInstance,
		sshPool:            &sshpool2.Pool{TTL: 10 * time.Minute},
		emailVerifications: make(map[string]*EmailVerification),
		magicSecrets:       make(map[string]*MagicSecret),
		creationStreams:    make(map[creationStreamKey]*CreationStream),
		githubUser:         ghu,
		postmarkClient:     postmarkClient,
		fakeHTTPEmail:      fakeEmailServer,
		devMode:            devMode,

		metricsRegistry: metricsRegistry,
		sshMetrics:      sshMetrics,
		dataSubdir:      dataSubdir,

		docs:      docsHandler,
		templates: tmpl,
		log:       slog,
	}

	if devMode == "" {
		s.dnsProvider = route53.NewDNSProvider()
	}

	s.setupHTTPServer()
	s.setupHTTPSServer()
	s.setupProxyServers()
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
		s.slog().Info("server started", "url", s.BaseURL)
	}()

	return s, nil
}

// withRx executes a function with a read-only database transaction and exedb queries
func (s *Server) withRx(ctx context.Context, fn func(context.Context, *exedb.Queries) error) error {
	return s.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		return fn(ctx, queries)
	})
}

// withTx executes a function with a read-write database transaction and exedb queries
func (s *Server) withTx(ctx context.Context, fn func(context.Context, *exedb.Queries) error) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return fn(ctx, queries)
	})
}

// withRxRes executes a function with a read-only database transaction and exedb queries, returning a value
func withRxRes[T any](s *Server, ctx context.Context, fn func(context.Context, *exedb.Queries) (T, error)) (T, error) {
	var result T
	err := s.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		result, err = fn(ctx, queries)
		return err
	})
	return result, err
}

// DataPath returns a path under /data with the server's isolation subdirectory
func (s *Server) DataPath(path string) string {
	return fmt.Sprintf("/data/%s/%s", s.dataSubdir, strings.TrimPrefix(path, "/"))
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

func (s *Server) installSSHHostKey(signer ssh.Signer, certSig *string) error {
	if certSig != nil {
		certData := strings.TrimSpace(*certSig)
		if certData != "" {
			pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(certData))
			if err != nil {
				return fmt.Errorf("failed to parse stored host certificate: %w", err)
			}
			cert, ok := pubKey.(*ssh.Certificate)
			if !ok {
				return fmt.Errorf("stored host certificate is not an SSH certificate")
			}
			certSigner, err := ssh.NewCertSigner(cert, signer)
			if err != nil {
				return fmt.Errorf("failed to construct host certificate signer: %w", err)
			}
			s.sshConfig.AddHostKey(certSigner)
			s.sshHostKey = certSigner
			s.slog().Debug("Loaded SSH host certificate",
				"key_id", cert.KeyId,
				"principals", cert.ValidPrincipals,
				"valid_after", time.Unix(int64(cert.ValidAfter), 0),
				"valid_before", func() any {
					if cert.ValidBefore == ssh.CertTimeInfinity {
						return "infinite"
					}
					return time.Unix(int64(cert.ValidBefore), 0)
				}(),
			)
			return nil
		}
	}

	s.sshConfig.AddHostKey(signer)
	s.sshHostKey = signer
	return nil
}

// generateHostKey loads the persistent RSA host key from the database, or generates and stores a new one
func (s *Server) generateHostKey(ctx context.Context) error {
	// Try to load existing host key from database
	hostKey, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.GetSSHHostKeyRow, error) {
		return queries.GetSSHHostKey(ctx)
	})
	privateKeyPEM := hostKey.PrivateKey
	publicKeyPEM := hostKey.PublicKey

	if errors.Is(err, sql.ErrNoRows) {
		// No existing key, generate a new one
		privateKey, err := rsa.GenerateKey(crand.Reader, 2048)
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
		err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			return queries.UpsertSSHHostKey(ctx, exedb.UpsertSSHHostKeyParams{
				PrivateKey:  privateKeyPEM,
				PublicKey:   publicKeyPEM,
				Fingerprint: fingerprint,
			})
		})
		if err != nil {
			return fmt.Errorf("failed to store host key: %w", err)
		}

		if err := s.installSSHHostKey(signer, nil); err != nil {
			return err
		}
		s.slog().Debug("Generated and stored new SSH host key", "fingerprint", fingerprint)

	} else if err != nil {
		return fmt.Errorf("failed to query host key: %w", err)
	} else {
		// Load existing key
		signer, err := ssh.ParsePrivateKey([]byte(privateKeyPEM))
		if err != nil {
			return fmt.Errorf("failed to parse stored private key: %w", err)
		}

		fingerprint := s.GetPublicKeyFingerprint(signer.PublicKey())
		if err := s.installSSHHostKey(signer, hostKey.CertSig); err != nil {
			return err
		}
		s.slog().Debug("Loaded existing SSH host key", "fingerprint", fingerprint)
	}

	return nil
}

// getPublicKeyFingerprint generates a SHA256 fingerprint for a public key
func (s *Server) GetPublicKeyFingerprint(key ssh.PublicKey) string {
	hash := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(hash[:])
}

// generateRegistrationToken creates a random registration token
func generateRegistrationToken() string {
	txt := crand.Text()
	return txt[:len(txt)/2] // we don't need a super long token, no birthday attacks here, 64 bits is plenty
}

// generatePairingCode returns a zero-padded six digit string for anti-phishing checks.
func generatePairingCode() string {
	max := big.NewInt(1_000_000)
	n, err := crand.Int(crand.Reader, max)
	if err != nil {
		// crand.Reader is now documented never to fail, so panic if it does
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%06d", n.Int64())
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
			s.slog().Warn("failed to send fake email", "to", to, "subject", subject, "error", err)
		}
	}

	// In dev mode, always just log the email
	if s.devMode != "" {
		s.slog().Info("DEV MODE: Would send email", "to", to, "subject", subject, "body", body)
		return nil
	}

	// Check if email service is configured
	if s.postmarkClient == nil {
		return fmt.Errorf("email service not configured")
	}

	// Use the existing sendVerificationEmail logic
	email := postmark.Email{
		From:     "exe.dev <support@exe.dev>",
		To:       to,
		Subject:  subject,
		TextBody: body,
	}

	_, err := s.postmarkClient.SendEmail(email)
	if err != nil {
		s.slog().Error("failed to send email", "to", to, "subject", subject, "error", err)
	} else {
		s.slog().Info("email sent successfully", "to", to, "subject", subject)
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

	s.slog().Info("fake email sent successfully via HTTP", "to", to, "subject", subject)
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
		s.slog().Warn("SSH auth failed", "method", method, "user", user, "remote_addr", remoteAddr, "client_version", clientVersion, "error", err)
	} else {
		// Log successful authentication
		s.slog().Info("SSH auth success", "method", method, "user", user, "remote_addr", remoteAddr, "client_version", clientVersion)
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
	s.slog().Debug("Authentication request", "user", user, "remote_addr", remoteAddr, "key_type", key.Type())

	// Check if this is a proxy connection from sshpiper
	s.slog().Debug("Checking if key is a proxy key")
	if originalUserKey, localAddress, isProxy := s.lookupEphemeralProxyKey(key); isProxy {
		s.slog().Debug("Ephemeral proxy authentication detected", "user", user, "local_address", localAddress)
		return s.authenticateProxyUserWithLocalAddress(ctx, user, originalUserKey, localAddress)
	} else {
		s.slog().Debug("Not a proxy key, treating as direct user connection")
	}
	// Log non-proxy connections for monitoring - in production, all connections should come via proxy
	s.slog().Warn("Direct connection to exed - should come via proxy", "remote_addr", remoteAddr)

	// First check if this key is already registered in ssh_keys table
	email, verified, err := s.GetEmailBySSHKey(ctx, publicKeyStr)
	if err != nil {
		s.slog().Error("Database error checking SSH key", "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has an alloc
		userID, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
			return queries.GetUserIDByEmail(ctx, email)
		})
		if err == nil {
			// Check if user has an alloc
			allocExists, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (int64, error) {
				return queries.AllocExistsForUser(ctx, userID)
			})
			if err == nil && allocExists > 0 {
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

// getDomain extracts the base domain from a host
func getDomain(host string) string {
	host = stripPort(host)

	// Check for localhost-based domains (dev mode)
	if strings.HasSuffix(host, ".localhost") || host == "localhost" {
		return "localhost"
	}
	// Check for exe.dev-based domains (production)
	if strings.HasSuffix(host, ".exe.dev") || host == "exe.dev" {
		return "exe.dev"
	}

	// Return as-is for custom domains
	return host
}

// checkEmailVerificationToken checks if an email verification token is valid without consuming it
func (s *Server) checkEmailVerificationToken(ctx context.Context, token string) (exedb.GetEmailVerificationByTokenRow, error) {
	var row exedb.GetEmailVerificationByTokenRow

	// Get verification info and return user_id directly
	err := s.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		row, err = queries.GetEmailVerificationByToken(ctx, token)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exedb.GetEmailVerificationByTokenRow{}, fmt.Errorf("invalid verification token")
		}
		return exedb.GetEmailVerificationByTokenRow{}, fmt.Errorf("database error: %w", err)
	}

	// Check if token has expired
	if time.Now().After(row.ExpiresAt) {
		// Clean up expired token
		s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
			return queries.DeleteEmailVerificationByToken(ctx, token)
		})
		return exedb.GetEmailVerificationByTokenRow{}, fmt.Errorf("verification token expired")
	}

	return row, nil
}

// validateEmailVerificationToken validates an email verification token, consumes it, and returns the user ID
func (s *Server) validateEmailVerificationToken(ctx context.Context, token string) (string, error) {
	row, err := s.checkEmailVerificationToken(ctx, token)
	if err != nil {
		return "", err
	}

	// Clean up used token - use context.Background() to ensure cleanup completes even if client disconnects
	s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		return queries.DeleteEmailVerificationByToken(ctx, token)
	})

	return row.UserID, nil
}

// storeEmailVerification stores an email verification token
func (s *Server) storeEmailVerification(ctx context.Context, email, token string) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		// Check if user exists, create if not
		userID, err := queries.GetUserIDByEmail(ctx, email)
		if errors.Is(err, sql.ErrNoRows) {
			// User doesn't exist, create them
			userID, err = s.createUserRecord(ctx, queries, email)
			if err != nil {
				return err
			}

			if _, err := s.createAllocForUser(ctx, queries, userID); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("failed to check user: %w", err)
		}

		// Store verification token
		expiresAt := time.Now().Add(24 * time.Hour)
		return queries.InsertOrReplaceEmailVerification(ctx, exedb.InsertOrReplaceEmailVerificationParams{
			Token:     token,
			UserID:    userID,
			Email:     email,
			ExpiresAt: expiresAt,
		})
	})
}

// validateEmailVerificationByToken validates verification using a token
func (s *Server) validateEmailVerificationByToken(ctx context.Context, token string) (string, error) {
	userID, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
		return queries.GetEmailVerificationByPartialToken(ctx, token)
	})
	if err != nil {
		return "", fmt.Errorf("invalid or expired token")
	}

	// Consume the token
	err = s.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		return queries.DeleteEmailVerificationByToken(ctx, token)
	})
	if err != nil {
		s.slog().Error("Failed to delete email verification token", "error", err)
	}

	return userID, nil
}

// validateAuthToken validates an authentication token and returns the user ID
func (s *Server) validateAuthToken(ctx context.Context, token, expectedSubdomain string) (string, error) {
	authToken, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.AuthToken, error) {
		return queries.GetAuthTokenInfo(ctx, token)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("invalid token")
		}
		return "", fmt.Errorf("database error: %w", err)
	}

	// Check if token has already been used
	if authToken.UsedAt != nil {
		return "", fmt.Errorf("token already used")
	}

	// Check if token has expired
	if time.Now().After(authToken.ExpiresAt) {
		return "", fmt.Errorf("token expired")
	}

	// Check machine name if specified (equivalent to subdomain check)
	if expectedSubdomain != "" && authToken.MachineName != nil && *authToken.MachineName != expectedSubdomain {
		return "", fmt.Errorf("token not valid for this subdomain")
	}

	// Mark token as used
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateAuthTokenUsedAt(ctx, token)
	})
	if err != nil {
		s.slog().Error("Failed to mark token as used", "error", err)
	}

	return authToken.UserID, nil
}

// SSHClient interface for SSH connections
type SSHClient interface {
	Dial(network, addr string) (net.Conn, error)
	Close() error
}

// findBoxByNameForUser finds a box by name that the user has access to
func (s *Server) FindBoxByNameForUser(ctx context.Context, userID, boxName string) *exedb.Box {
	// s.slog().Debug("FindBoxByNameForUser", "user_id", userID, "box_name", boxName)
	if !boxname.Valid(boxName) {
		// s.slog().Info("invalid box name format", "box", boxName)
		return nil
	}

	// Check if box exists and belongs to the user
	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   boxName,
			UserID: userID,
		})
	})
	if err != nil {
		s.slog().Info("FindBoxByNameForUser: box not found", "box", boxName, "error", err)
		return nil
	}

	return &box
}

// formatSSHConnectionInfo returns SSH connection info for box boxName based on dev mode.
// sshConnectionString returns the SSH connection string (without the "ssh" command prefix)
func (s *Server) sshConnectionString(boxName string) string {
	if s.devMode != "" {
		if s.piperdPort != 22 {
			return fmt.Sprintf("%s@localhost:%d", boxName, s.piperdPort)
		}
		return fmt.Sprintf("%s@localhost", boxName)
	}
	return fmt.Sprintf("%s@exe.dev", boxName)
}

func (s *Server) formatSSHConnectionInfo(boxName string) string {
	connStr := s.sshConnectionString(boxName)
	if s.devMode != "" && s.piperdPort != 22 {
		return fmt.Sprintf("ssh -p %d %s@localhost", s.piperdPort, boxName)
	}
	if s.devMode != "" {
		return fmt.Sprintf("ssh %s@localhost", boxName)
	}
	return fmt.Sprintf("ssh %s", connStr)
}

// formatExeDevConnectionInfo returns SSH connection info for the exe.dev server based on dev mode.
func (s *Server) formatExeDevConnectionInfo() string {
	if s.devMode != "" {
		var dashP string
		if s.piperdPort != 22 {
			dashP = fmt.Sprintf("-p %v ", s.piperdPort)
		}
		return fmt.Sprintf("ssh %slocalhost", dashP)
	}
	return "ssh exe.dev"
}

// httpsProxyAddress returns the HTTPS proxy address for a box.
func (s *Server) httpsProxyAddress(boxName string) string {
	if s.devMode != "" {
		return fmt.Sprintf("http://%s.localhost:%d", boxName, s.httpLn.tcp.Port)
	}
	return fmt.Sprintf("https://%s.exe.dev", boxName)
}

// terminalURL returns the terminal URL for a box.
func (s *Server) terminalURL(boxName string) string {
	if s.devMode != "" {
		return fmt.Sprintf("http://%s.xterm.localhost:%d", boxName, s.httpLn.tcp.Port)
	}
	return fmt.Sprintf("https://%s.xterm.exe.dev", boxName)
}

// shelleyURL returns the Shelley agent URL for a box (port 9999).
func (s *Server) shelleyURL(boxName string) string {
	if s.devMode != "" {
		return fmt.Sprintf("http://%s.localhost:%d", boxName, 9999)
	}
	return fmt.Sprintf("https://%s.exe.dev:9999", boxName)
}

// vscodeURL returns the VSCode remote SSH URL for a box.
func (s *Server) vscodeURL(boxName string) string {
	connStr := s.sshConnectionString(boxName)
	return fmt.Sprintf("vscode://vscode-remote/ssh-remote+%s/home/exedev/src?windowId=_blank", connStr)
}

// preCreateBox creates a box entry before the container is created, returns the box ID
func (s *Server) preCreateBox(ctx context.Context, userID, allocID, name, image string) (int, error) {
	// Validate box name
	if !boxname.Valid(name) {
		return 0, fmt.Errorf("invalid box name: %s", name)
	}

	routes := exedb.DefaultRouteJSON()
	var boxID int
	var assignedShard int
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		id, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			AllocID:         allocID,
			Name:            name,
			Status:          "creating",
			Image:           image,
			CreatedByUserID: userID,
			Routes:          &routes,
		})
		if err != nil {
			return err
		}
		boxID = int(id)

		shard, err := s.allocateIPShard(ctx, queries, userID, boxID)
		if err != nil {
			return err
		}
		assignedShard = shard

		return nil
	})
	if err != nil {
		return 0, err
	}

	if s.devMode == "" {
		if err := s.createBoxCNAME(ctx, name, assignedShard); err != nil {
			cleanupErr := s.rollbackBoxPreCreation(ctx, boxID)
			if cleanupErr != nil {
				s.slog().Error("failed to roll back box after DNS error", "box_id", boxID, "cleanup_error", cleanupErr, "dns_error", err)
			}
			return 0, err
		}
	}

	s.recordUserEventBestEffort(ctx, userID, userEventCreatedBox)
	return boxID, nil
}

func (s *Server) allocateIPShard(ctx context.Context, queries *exedb.Queries, userID string, boxID int) (int, error) {
	shards, err := queries.ListIPShardsForUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("failed to list IP shards for user %s: %w", userID, err)
	}

	used := make([]bool, maxIPShard+1)
	for _, shard := range shards {
		if shard < 1 || shard > maxIPShard {
			continue
		}
		used[int(shard)] = true
	}

	var assigned int
	for candidate := 1; candidate <= maxIPShard; candidate++ {
		if !used[candidate] {
			assigned = candidate
			break
		}
	}

	if assigned == 0 {
		return 0, fmt.Errorf("no IP shards available for user %s", userID)
	}

	if err := queries.InsertBoxIPShard(ctx, exedb.InsertBoxIPShardParams{
		BoxID:   int64(boxID),
		UserID:  userID,
		IPShard: int64(assigned),
	}); err != nil {
		return 0, fmt.Errorf("failed to assign IP shard for box %d: %w", boxID, err)
	}
	return assigned, nil
}

func (s *Server) createBoxCNAME(ctx context.Context, boxName string, shard int) error {
	if shard < 1 || shard > maxIPShard {
		return fmt.Errorf("invalid IP shard %d for box %s", shard, boxName)
	}
	if s.dnsProvider == nil {
		return fmt.Errorf("route53 DNS provider not configured")
	}

	dnsCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	target := fmt.Sprintf("s%03d", shard)
	_, err := s.dnsProvider.CreateCNAMERecord(dnsCtx, s.getMainDomain(), boxName, target, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to create Route53 CNAME for box %s: %w", boxName, err)
	}
	return nil
}

func (s *Server) rollbackBoxPreCreation(ctx context.Context, boxID int) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`DELETE FROM box_ip_shard WHERE box_id = ?`, boxID); err != nil {
			return err
		}
		queries := exedb.New(tx.Conn())
		if err := queries.DeleteBox(ctx, boxID); err != nil {
			return err
		}
		return nil
	})
}

// updateBoxWithContainer updates a box with container info and SSH keys after container creation
func (s *Server) updateBoxWithContainer(ctx context.Context, boxID int, containerID, sshUser string, sshKeys *container.ContainerSSHKeys, sshPort int) error {
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ContainerID:          &containerID,
			Status:               "running",
			SSHServerIdentityKey: []byte(sshKeys.ServerIdentityKey),
			SSHAuthorizedKeys:    &sshKeys.AuthorizedKeys,
			SSHClientPrivateKey:  []byte(sshKeys.ClientPrivateKey),
			SSHPort:              func() *int64 { p := int64(sshPort); return &p }(),
			SSHUser:              &sshUser,
			ID:                   boxID,
		})
	})
}

// isBoxNameAvailable checks if a box name is available for use.
// Errors are translated into false (unavailability).
func (s *Server) isBoxNameAvailable(ctx context.Context, name string) bool {
	// Check if name is in denylist
	withoutHyphens := strings.ReplaceAll(name, "-", "")
	if boxname.Denylisted(withoutHyphens) {
		return false
	}

	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (int64, error) {
		return queries.BoxWithNameExists(ctx, name)
	})
	if err != nil {
		s.slog().Warn("failed to check box name availability", "error", err, "box_name", name)
		return false
	}
	return box == 0
}

func (s *Server) boxByNameExists(ctx context.Context, name string) bool {
	box, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (int64, error) {
		return queries.BoxWithNameExists(ctx, name)
	})
	if err != nil {
		s.slog().Warn("failed to check box name existence", "error", err, "box_name", name)
		return false
	}
	return box > 0
}

// getBoxesByHost gets all boxes (machines) that should be on a specific ctrhost
func (s *Server) getBoxesByHost(ctx context.Context, ctrhost string) ([]*exedb.Box, error) {
	boxResults, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.Box, error) {
		return queries.GetBoxesByHost(ctx, ctrhost)
	})
	if err != nil {
		return nil, err
	}

	// Convert results to Box pointers
	boxes := make([]*exedb.Box, len(boxResults))
	for i := range boxResults {
		boxes[i] = &boxResults[i]
	}
	return boxes, nil
}

func (s *Server) getBoxesByHostVariants(ctx context.Context, host string) ([]*exedb.Box, error) {
	var allBoxes []*exedb.Box
	seen := make(map[int]bool)
	for _, key := range s.hostLookupKeys(host) {
		boxes, err := s.getBoxesByHost(ctx, key)
		if err != nil {
			s.slog().Debug("getBoxesByHost lookup failed", "hostKey", key, "error", err)
			continue
		}
		for _, box := range boxes {
			if !seen[box.ID] {
				allBoxes = append(allBoxes, box)
				seen[box.ID] = true
			}
		}
	}
	return allBoxes, nil
}

func (s *Server) hostLookupKeys(host string) []string {
	keys := []string{host}
	if strings.HasPrefix(host, "ssh://") {
		alias := strings.TrimPrefix(host, "ssh://")
		if alias != "" {
			keys = append(keys, alias)
			if ip := ctrhosttest.ResolveHostFromSSHConfig(alias); ip != "" {
				keys = append(keys, "tcp://"+ip)
			}
		}
	}
	return dedupeStrings(keys)
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	j := 0
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		values[j] = v
		j++
	}
	return values[:j]
}

// isValidEmail performs basic email validation
func isValidEmail(email string) bool {
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

const (
	containerListRetryInitialDelay = 2 * time.Second
	containerListRetryMaxDelay     = 20 * time.Second
	containerListRetryTimeout      = 3 * time.Minute
)

func (s *Server) listContainersWithRetry(ctx context.Context, host string) ([]*container.Container, error) {
	delay := containerListRetryInitialDelay
	deadline := time.Now().Add(containerListRetryTimeout)
	attempt := 0
	var lastErr error

	for {
		attempt++
		containers, err := s.containerManager.ListContainersOnHost(ctx, host)
		if err == nil {
			if attempt > 1 {
				s.slog().Info("Successfully listed containers on host after retry", "host", host, "attempts", attempt)
			}
			return containers, nil
		}

		lastErr = err
		s.slog().Warn("Failed to list containers on host", "host", host, "attempt", attempt, "error", err)

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out listing containers on host %s after %d attempts: %w", host, attempt, lastErr)
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while waiting for host %s containers: %w", host, ctx.Err())
		}

		if delay < containerListRetryMaxDelay {
			delay *= 2
			if delay > containerListRetryMaxDelay {
				delay = containerListRetryMaxDelay
			}
		}
	}
}

// syncAllocsWithHosts synchronizes allocations between the database and container hosts
// This ensures that:
// 1. All allocations in the database have their networks created on hosts
// 2. Any allocations not in the database are removed from hosts
func (s *Server) syncAllocsWithHosts(ctx context.Context) error {
	// Get the list of container hosts
	hosts := s.containerManager.GetHosts()
	if len(hosts) == 0 {
		s.slog().Warn("No container hosts available for alloc sync")
		return nil
	}

	s.slog().Info("Starting allocation sync with container hosts", "hostCount", len(hosts))

	// Process each host
	for _, host := range hosts {
		if err := s.syncAllocsForHost(ctx, host); err != nil {
			s.slog().Error("Failed to sync allocations for host", "host", host, "error", err)
			// Continue with other hosts even if one fails
		}
	}

	s.slog().Info("Allocation sync completed")
	return nil
}

// syncAllocsForHost synchronizes allocations for a specific container host
func (s *Server) syncAllocsForHost(ctx context.Context, host string) error {
	// Get allocations from the database that should be on this host
	dbAllocs, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]exedb.Alloc, error) {
		return queries.GetAllocsByHost(ctx, host)
	})
	if err != nil {
		return fmt.Errorf("failed to get allocations from database: %w", err)
	}

	// Get allocations currently on the host
	hostAllocIDs, err := s.containerManager.ListAllocs(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to list allocations on host: %w", err)
	}

	// Create maps for easier lookup
	dbAllocMap := make(map[string]*exedb.Alloc)
	for _, alloc := range dbAllocs {
		// Truncate allocID to match network naming (max 12 chars)
		nameLen := len(alloc.AllocID)
		if nameLen > 12 {
			nameLen = 12
		}
		truncatedID := alloc.AllocID[:nameLen]
		dbAllocMap[truncatedID] = &alloc
	}

	hostAllocMap := make(map[string]bool)
	for _, allocID := range hostAllocIDs {
		hostAllocMap[allocID] = true
	}

	// Create allocations that are in DB but not on host
	for truncatedID, alloc := range dbAllocMap {
		if !hostAllocMap[truncatedID] {
			// Create the allocation on the host (now a no-op but kept for compatibility)
			s.slog().Info("Creating missing allocation on host", "allocID", alloc.AllocID, "host", host)
			if err := s.containerManager.CreateAlloc(ctx, alloc.AllocID); err != nil {
				s.slog().Error("Failed to create allocation on host", "allocID", alloc.AllocID, "host", host, "error", err)
				// Continue with other allocations
			}
		}
	}

	// Delete allocations that are on host but not in DB
	for allocID := range hostAllocMap {
		if _, exists := dbAllocMap[allocID]; !exists {
			s.slog().Info("Removing orphaned allocation from host", "allocID", allocID, "host", host)
			if err := s.containerManager.DeleteAlloc(ctx, allocID, host); err != nil {
				s.slog().Error("Failed to delete allocation from host", "allocID", allocID, "host", host, "error", err)
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
		s.slog().Warn("No container hosts available for container sync")
		return nil
	}

	s.slog().Info("Starting container sync with container hosts", "hostCount", len(hosts))

	// Process each host
	for _, host := range hosts {
		if err := s.syncContainersForHost(ctx, host); err != nil {
			s.slog().Error("Failed to sync containers for host", "host", host, "error", err)
			// Continue with other hosts even if one fails
		}
	}

	s.slog().Info("Container sync completed")
	return nil
}

// syncContainersForHost synchronizes containers for a specific container host
func (s *Server) syncContainersForHost(ctx context.Context, host string) error {
	// Get boxes from the database that should be on this host
	dbBoxes, err := s.getBoxesByHostVariants(ctx, host)
	if err != nil {
		return fmt.Errorf("failed to get boxes from database: %w", err)
	}

	// Get containers currently on the host, retrying while the host is restarting
	hostContainers, err := s.listContainersWithRetry(ctx, host)
	if err != nil {
		return err
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
		hostContainerMap[c.ID] = c
	}

	// Check each box that should have a container
	for _, box := range dbBoxes {
		// Skip boxes without container IDs (not yet created)
		if box.ContainerID == nil || *box.ContainerID == "" {
			continue
		}

		containerID := *box.ContainerID
		hostContainer, exists := hostContainerMap[containerID]

		containerStatus := "missing"
		if hostContainer != nil {
			containerStatus = hostContainer.Status.String()
		}
		s.slog().Debug("syncContainersForHost status",
			"host", host,
			"box", box.Name,
			"boxStatus", box.Status,
			"containerID", containerID,
			"containerFound", exists,
			"containerStatus", containerStatus,
		)

		if !exists {
			// Container doesn't exist on host but should - check for persistent disk
			diskPath := s.DataPath(fmt.Sprintf("exed/containers/box-%d", box.ID))

			// Use the VerifyDisk method for proper disk validation
			nerdctlMgr := s.containerManager
			{
				diskExists, err := nerdctlMgr.VerifyDisk(ctx, host, box.ID)
				if err != nil {
					s.slog().Error("Failed to verify disk", "box", box.Name, "error", err)
					continue
				}

				if !diskExists {
					// Disk missing - mark box as broken
					s.slog().Error("Container disk missing, marking as failed", "box", box.Name, "diskPath", diskPath)
					if err := s.updateBoxStatus(ctx, box.ID, "failed"); err != nil {
						s.slog().Error("Failed to mark box as failed", "box", box.Name, "error", err)
					}
				} else {
					// Disk exists - recreate container
					s.slog().Info("Recreating container from persistent disk", "box", box.Name, "boxID", box.ID)

					// Reconstruct SSH keys from the box record
					var existingSSHKeys *container.ContainerSSHKeys
					if len(box.SSHServerIdentityKey) > 0 {
						existingSSHKeys = &container.ContainerSSHKeys{
							ServerIdentityKey: string(box.SSHServerIdentityKey),
							AuthorizedKeys:    *box.SSHAuthorizedKeys,
							ClientPrivateKey:  string(box.SSHClientPrivateKey),
							SSHPort:           int(*box.SSHPort),
						}
						s.slog().Info("Using existing SSH keys from database for container recreation", "boxID", box.ID)
					}

					// Create a new container using the existing disk
					req := &container.CreateContainerRequest{
						AllocID: box.AllocID,
						Name:    box.Name,
						BoxID:   box.ID,
						Image:   box.Image,
						Host:    host,
						// We don't have size info stored, use default
						Size:            "small",
						ExistingSSHKeys: existingSSHKeys,
					}

					// Recreate the container (CreateContainer will reuse the existing disk)
					s.slog().Info("Calling CreateContainer to recreate from disk", "boxID", box.ID, "oldContainerID", containerID)
					newContainer, err := nerdctlMgr.CreateContainer(ctx, req)
					if err != nil {
						s.slog().Error("Failed to recreate container from disk", "box", box.Name, "error", err)
						if err := s.updateBoxStatus(ctx, box.ID, "failed"); err != nil {
							s.slog().Error("Failed to mark box as failed", "box", box.Name, "error", err)
						}
					} else {
						// Update box with new container ID
						if err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
							return queries.UpdateBoxContainerIDAndStatus(ctx, exedb.UpdateBoxContainerIDAndStatusParams{
								ContainerID: &newContainer.ID,
								ID:          box.ID,
							})
						}); err != nil {
							s.slog().Error("Failed to update box with new container ID", "box", box.Name, "error", err)
						} else {
							s.slog().Info("Successfully recreated container from disk",
								"box", box.Name,
								"oldContainerID", containerID,
								"newContainerID", newContainer.ID)
						}
					}
				}
			}
		} else if hostContainer.Status != "running" && box.Status == "running" {
			// Container exists but isn't running when it should be
			s.slog().Info("Restarting stopped container", "box", box.Name, "containerID", containerID)
			if err := s.containerManager.StartContainer(ctx, box.AllocID, containerID); err != nil {
				s.slog().Error("Failed to restart container", "box", box.Name, "error", err)
				if err := s.updateBoxStatus(ctx, box.ID, "stopped"); err != nil {
					s.slog().Error("Failed to update box status", "box", box.Name, "error", err)
				}
			}
		} else if hostContainer.Status == "running" && box.Status != "running" {
			// Update database to reflect actual container state
			s.slog().Info("Updating box status to match container", "box", box.Name, "status", hostContainer.Status)
			if err := s.updateBoxStatus(ctx, box.ID, string(hostContainer.Status)); err != nil {
				s.slog().Error("Failed to update box status", "box", box.Name, "error", err)
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
			s.slog().Warn("Found potentially orphaned container on host - NOT deleting automatically",
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
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateBoxStatus(ctx, exedb.UpdateBoxStatusParams{
			Status: status,
			ID:     boxID,
		})
	})
}

// Start starts HTTP, HTTPS (if configured), and SSH servers
func (s *Server) Start() error {
	if s.stopping.Load() {
		return fmt.Errorf("illegal start after stop")
	}

	// Create a cancellable context for startup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start HTTP server in a goroutine if configured
	if s.httpLn.ln != nil {
		go func() {
			s.slog().Debug("HTTP server starting", "addr", s.httpLn)
			if err := s.httpServer.Serve(s.httpLn.ln); err != nil && err != http.ErrServerClosed {
				s.slog().Error("HTTP server startup failed", "error", err)
				cancel()
			}
		}()
	}

	// Start HTTPS server in a goroutine if configured
	if s.httpsLn.ln != nil {
		go func() {
			s.slog().Info("HTTPS server starting with Let's Encrypt for exe.dev", "addr", s.httpsLn)
			if err := s.httpsServer.ServeTLS(s.httpsLn.ln, "", ""); err != nil && err != http.ErrServerClosed {
				s.slog().Error("HTTPS server startup failed", "error", err)
				cancel()
			}
		}()

		if s.wildcardCertManager != nil {
			s.slog().Info("Using DNS challenges for wildcard main domain certificate")
		}
	}

	// Start proxy listeners with the same handlers. Prefer https if it's available
	for _, proxyLn := range s.proxyLns {
		go func(ln *listener) {
			if s.httpsLn.ln != nil {
				// s.slog().Info("Proxy listener starting with HTTPS handler", "addr", ln.tcp.String())
				if err := s.httpsServer.ServeTLS(proxyLn.ln, "", ""); err != nil && err != http.ErrServerClosed {
					s.slog().Error("Proxy listener startup failed (HTTPS)", "error", err, "addr", ln)
					cancel()
				}
			} else {
				s.slog().Info("Proxy listener starting with HTTP handler", "addr", ln.tcp.String())
				if err := s.httpServer.Serve(ln.ln); err != nil && err != http.ErrServerClosed {
					s.slog().Error("Proxy listener startup failed (HTTP)", "error", err, "addr", ln)
					cancel()
				}
			}
		}(proxyLn)
	}

	// Start SSH server in a goroutine
	go func() {
		s.sshServer = NewSSHServer(s)
		if err := s.sshServer.Start(s.sshLn.ln); err != nil {
			s.slog().Error("SSH server startup failed", "error", err)
			cancel()
		}
	}()

	// Start piper plugin server in a goroutine
	s.slog().Info("piper plugin server listening", "addr", s.pluginLn.addr, "port", s.pluginLn.tcp.Port)
	s.piperPlugin = NewPiperPlugin(s, s.sshLn.tcp.Port)
	go func() {
		if err := s.piperPlugin.Serve(s.pluginLn.ln); err != nil {
			s.slog().Error("Piper plugin server startup failed", "error", err)
			cancel()
		}
	}()

	if s.devMode == "local" {
		// In dev mode, automatically start sshpiper if not already running
		go s.autoStartSSHPiper(ctx)

		s.slog().Info("SSH server started in local dev mode. Connect with:")
		s.slog().Info(fmt.Sprintf("  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %v localhost", s.sshLn.tcp.Port))
	}

	// Sync allocations and containers with container hosts before accepting connections
	if s.containerManager != nil {
		// First sync allocations (networks)
		if err := s.syncAllocsWithHosts(ctx); err != nil {
			s.slog().Error("Failed to sync allocations with container hosts", "error", err)
			// Continue anyway - we can sync later
		}

		// Then sync containers
		if err := s.syncContainersWithHosts(ctx); err != nil {
			s.slog().Error("Failed to sync containers with container hosts", "error", err)
			// Continue anyway - we can sync later
		}
	}

	// Start tag resolver and host updater for keeping container images fresh
	if s.tagResolver != nil && s.hostUpdater != nil {
		s.slog().Info("Starting tag resolver for image freshness management")
		s.tagResolver.Start(ctx)
		s.hostUpdater.Start(ctx)
	}

	// Wait for interrupt signal or startup failure
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	s.ready.Done()

	select {
	case <-sigChan:
		s.slog().Info("Shutting down servers...")
		return s.Stop()
	case <-ctx.Done():
		s.slog().Error("Server startup failed, shutting down")
		s.Stop()
		return fmt.Errorf("server startup failed")
	}
}

// autoStartSSHPiper automatically starts sshpiper.sh in dev mode if that port isn't listening
func (s *Server) autoStartSSHPiper(ctx context.Context) {
	// Check if sshpiper is already running on the specified port
	if s.isPortListening(fmt.Sprintf("localhost:%d", s.piperdPort)) {
		s.slog().Info("sshpiper already running", "port", s.piperdPort)
		return
	}

	// Use the actual piper TCP address
	if s.pluginLn.tcp == nil {
		s.slog().Error("Piper TCP address not available")
		return
	}

	piperPluginAddr := fmt.Sprintf("localhost:%d", s.pluginLn.tcp.Port)

	// First, wait for the piper plugin to be ready
	if !s.waitForPort(ctx, piperPluginAddr, 30*time.Second) {
		s.slog().Error("Timed out waiting for piper plugin to start", "addr", piperPluginAddr)
		return
	}

	// Start sshpiper.sh with the piper plugin port
	s.slog().Info("Starting sshpiper.sh automatically in dev mode", "piperPluginPort", s.pluginLn.tcp.Port)

	cmd := exec.CommandContext(ctx, "./sshpiper.sh", fmt.Sprint(s.pluginLn.tcp.Port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		s.slog().Error("Failed to start sshpiper.sh", "error", err)
		return
	}

	// Wait for the process in a separate goroutine
	go func() {
		if err := cmd.Wait(); err != nil {
			s.slog().Error("sshpiper.sh exited with error", "error", err)
		} else {
			s.slog().Info("sshpiper.sh exited normally")
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
	err = s.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		email, err = queries.GetEmailBySSHKey(ctx, publicKeyStr)
		return err
	})

	if errors.Is(err, sql.ErrNoRows) {
		// Check if key exists in pending_ssh_keys (unverified)
		email, err = withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
			return queries.GetPendingSSHKeyEmailByPublicKey(ctx, publicKeyStr)
		})
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return email, false, err // Key exists in pending_ssh_keys, so not verified
	}
	return email, true, err // Key exists in ssh_keys, so verified
}

// getUserByPublicKey retrieves a user by their SSH public key
func (s *Server) getUserByPublicKey(ctx context.Context, publicKeyStr string) (*exedb.User, error) {
	user, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.User, error) {
		return queries.GetUserWithSSHKey(ctx, publicKeyStr)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// createUserRecord creates a user record and returns the new user ID.
func (s *Server) createUserRecord(ctx context.Context, queries *exedb.Queries, email string) (string, error) {
	userID, err := generateUserID()
	if err != nil {
		return "", fmt.Errorf("failed to generate user ID: %w", err)
	}

	if err := queries.InsertUser(ctx, exedb.InsertUserParams{
		UserID: userID,
		Email:  email,
	}); err != nil {
		return "", fmt.Errorf("failed to create user: %w", err)
	}

	return userID, nil
}

// createAllocForUser provisions an allocation for the given user and returns the new alloc ID.
func (s *Server) createAllocForUser(ctx context.Context, queries *exedb.Queries, userID string) (string, error) {
	allocID, err := generateAllocID()
	if err != nil {
		return "", fmt.Errorf("failed to generate alloc ID: %w", err)
	}

	ctrhost, err := s.selectCtrhostForNewAlloc(allocID)
	if err != nil {
		return "", err
	}

	if err := queries.InsertAlloc(ctx, exedb.InsertAllocParams{
		AllocID:   allocID,
		UserID:    userID,
		AllocType: string(AllocTypeMedium),
		Region:    string(RegionAWSUSWest2),
		Ctrhost:   ctrhost,
	}); err != nil {
		return "", fmt.Errorf("failed to create allocation: %w", err)
	}

	return allocID, nil
}

// createUser creates a new user with their resource allocation.
func (s *Server) createUser(ctx context.Context, publicKey, email string) (*exedb.User, error) {
	var user exedb.User

	// First create the user and allocation in the database
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())

		userID, err := s.createUserRecord(ctx, queries, email)
		if err != nil {
			return err
		}

		// Add the SSH key to ssh_keys table
		if err := queries.InsertSSHKey(ctx, exedb.InsertSSHKeyParams{
			UserID:    userID,
			PublicKey: publicKey,
		}); err != nil {
			return err
		}

		if _, err := s.createAllocForUser(ctx, queries, userID); err != nil {
			return err
		}

		user, err = queries.GetUserWithDetails(ctx, userID)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Resolve any pending shares for this email
	if err := s.resolvePendingShares(ctx, email, user.UserID); err != nil {
		return nil, fmt.Errorf("failed to resolve pending shares: %w", err)
	}

	return &user, nil
}

// getUserAlloc gets the alloc for a user (creates one if it doesn't exist)
func (s *Server) getUserAlloc(ctx context.Context, userID string) (*exedb.Alloc, error) {
	alloc, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.Alloc, error) {
		return queries.GetAllocByUserID(ctx, userID)
	})

	if errors.Is(err, sql.ErrNoRows) {
		// User exists but has no alloc yet - create one
		if _, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.User, error) {
			return queries.GetUserWithDetails(ctx, userID)
		}); err != nil {
			return nil, err
		}

		if err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			_, err := s.createAllocForUser(ctx, queries, userID)
			return err
		}); err != nil {
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
func (s *Server) selectCtrhostForNewAlloc(allocID string) (string, error) {
	if s.containerManager == nil {
		// This is a right mess.
		// Our container manager is too expensive to spin up for a bunch of unit tests, so we don't.
		// But then we end up with a bunch of special cases to allow other code to work without it.
		// Co-pilot keeps feeding me ghost text about how we should use mocks and/or dependency injection.
		// Codex and Claude are keen to as well.
		// I refuse. This is a symptom of bad (missing) layering, and extra abstractions will not improve matters.
		// TODO: find a path out of this misery.
		if s.devMode == "" {
			// should be impossible
			return "", fmt.Errorf("container manager not configured")
		}
		// Unit tests, just return obviously fake host.
		return "fake_ctrhost", nil
	}

	chosen, err := s.containerManager.SelectHost(allocID)
	if err != nil {
		return "", fmt.Errorf("failed to select container host: %w", err)
	}

	// In dev/test, store a direct TCP/IP dial address so piper/proxy can reach
	// the Lima VM without relying on SSH alias DNS.
	if s.devMode != "" {
		alias := strings.TrimPrefix(chosen, "ssh://")
		if alias == "" {
			alias = chosen
		}
		if dial := ctrhosttest.DetectDialAddr(); dial != "" {
			return dial, nil
		}
		if ip := ctrhosttest.ResolveHostFromSSHConfig(alias); ip != "" {
			return "tcp://" + ip, nil
		}
	}

	return chosen, nil
}

// generateAllocID generates a unique allocation ID
func generateAllocID() (string, error) {
	// Generate a random ID with "alloc_" prefix
	bytes := make([]byte, 12)
	if _, err := crand.Read(bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("alloc_%s", hex.EncodeToString(bytes)), nil
}

// Stop gracefully shuts down all servers
func (s *Server) Stop() error {
	s.stopping.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shutdown HTTP server
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.slog().Error("HTTP server shutdown error", "error", err)
	}

	// Shutdown HTTPS server if running
	if s.httpsServer != nil {
		if err := s.httpsServer.Shutdown(ctx); err != nil {
			s.slog().Error("HTTPS server shutdown error", "error", err)
		}
	}

	if s.tagResolver != nil {
		s.tagResolver.Stop()
	}
	if s.hostUpdater != nil {
		s.hostUpdater.Stop()
	}
	if err := s.sshPool.Close(); err != nil {
		s.slog().Error("SSH pool close error", "error", err)
	}
	if s.db != nil {
		s.db.Close()
	}

	if s.stopCobble != nil {
		s.stopCobble()
	}

	s.slog().Debug("Servers stopped")
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
		s.slog().Error("Piper plugin not configured but proxy key received")
		return nil, "", false
	}

	proxyFingerprint := s.GetPublicKeyFingerprint(proxyKey)
	s.slog().Debug("Looking up proxy key", "fingerprint", proxyFingerprint[:16])

	originalUserKey, localAddress, exists := s.piperPlugin.lookupOriginalUserKey(proxyFingerprint)
	if !exists {
		s.slog().Debug("Proxy key not found or expired", "fingerprint", proxyFingerprint[:16])
		return nil, "", false // Not a proxy key or expired
	}

	s.slog().Debug("Found original user key for proxy key", "key_length", len(originalUserKey), "local_address", localAddress, "proxy_fingerprint", proxyFingerprint[:16])
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

	s.slog().Debug("Authenticating original user", "fingerprint", originalFingerprint, "username", username)

	// Look up the user by their original public key
	email, verified, err := s.GetEmailBySSHKey(ctx, originalKeyStr)
	if err != nil {
		s.slog().Error("Database error checking SSH key", "fingerprint", originalFingerprint, "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user has an alloc
		user, err := s.GetUserByEmail(ctx, email)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.slog().Error("Database error getting user", "email", email, "error", err)
		}

		if user != nil {
			alloc, err := s.getUserAlloc(ctx, user.UserID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				s.slog().Error("Database error getting alloc for user", "userID", user.UserID, "error", err)
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
	s.slog().Info("authenticateProxyUserWithLocalAddress", "username", username, "localAddress", localAddress, "keyBytes", len(originalUserKeyBytes))

	// Check for special container-logs username format and easter egg careers usernames
	if strings.HasPrefix(username, "container-logs:") || slices.Contains(boxname.JobsRelated, username) {
		s.slog().Info("Detected special container-logs username, bypassing normal auth", "username", username)
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
	randomPart := crand.Text()
	if len(randomPart) < 13 {
		return "", fmt.Errorf("random text too short: %d", len(randomPart))
	}
	return "usr" + randomPart[:13], nil
}

// getUserIDByPublicKey gets user_id from an SSH public key
func (s *Server) getUserIDByPublicKey(ctx context.Context, publicKey ssh.PublicKey) (string, error) {
	publicKeyStr := string(ssh.MarshalAuthorizedKey(publicKey))
	userID, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
		return queries.GetUserIDBySSHKey(ctx, publicKeyStr)
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
func (s *Server) GetUserByEmail(ctx context.Context, email string) (*exedb.User, error) {
	user, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.User, error) {
		return queries.GetUserByEmail(ctx, email)
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
	result, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.GetBoxSSHDetailsRow, error) {
		return queries.GetBoxSSHDetails(ctx, boxID)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query box SSH details: %v", err)
	}

	if result.SSHPort == nil || *result.SSHPort == 0 || len(result.SSHClientPrivateKey) == 0 {
		// SSH not set up for this box - this is for containers created before SSH support
		// TODO: Remove this code once all legacy containers are migrated
		log.Printf("Box %d missing SSH setup, initializing SSH on container", boxID)
		err := s.setupContainerSSH(ctx, boxID)
		if err != nil {
			return nil, fmt.Errorf("failed to setup SSH on legacy container: %v", err)
		}

		// Re-query after setup
		result, err = withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.GetBoxSSHDetailsRow, error) {
			return queries.GetBoxSSHDetails(ctx, boxID)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to re-query box SSH details after setup: %v", err)
		}
	}

	if result.SSHPort == nil || *result.SSHPort <= 0 {
		return nil, fmt.Errorf("invalid SSH port for box: %v", result.SSHPort)
	}
	sshPort := int(*result.SSHPort)

	if len(result.SSHClientPrivateKey) == 0 {
		return nil, fmt.Errorf("no SSH private key available for box after setup")
	}
	privateKeyStr := string(result.SSHClientPrivateKey)

	// Derive host public key from server identity key if available
	var hostKey string
	if len(result.SSHServerIdentityKey) > 0 {
		// Parse the server identity private key and extract the public key
		privKey, err := ssh.ParsePrivateKey(result.SSHServerIdentityKey)
		if err == nil {
			hostKey = string(ssh.MarshalAuthorizedKey(privKey.PublicKey()))
		}
		// If parsing fails, we'll just use empty host key (fallback to no validation)
	}

	// Default to root user if not specified
	user := "root"
	if result.SSHUser != nil && *result.SSHUser != "" {
		user = *result.SSHUser
	}

	return &exedb.SSHDetails{
		Port:       sshPort,
		PrivateKey: privateKeyStr,
		HostKey:    hostKey,
		Ctrhost:    &result.Ctrhost,
		User:       user,
	}, nil
}

// SSHIdentityKeyForBox implements boxKeyAuthority interface for llmgateway
func (s *Server) SSHIdentityKeyForBox(ctx context.Context, name string) (ssh.PublicKey, error) {
	key, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) ([]byte, error) {
		return queries.SSHKeyForBoxNamed(ctx, name)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to find box %s: %w", name, err)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("box %s has no SSH server identity key", name)
	}
	// Parse the private key to extract the public key
	privateKey, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH server identity key for box %s: %w", name, err)
	}
	// Return the public key in authorized_keys format
	return privateKey.PublicKey(), nil
}

// setupContainerSSH sets up SSH on a legacy container that was created before SSH support
// TODO: Remove this method once all legacy containers are migrated to have SSH
func (s *Server) setupContainerSSH(ctx context.Context, boxID int) error {
	// Get box details
	result, err := withRxRes(s, ctx, func(ctx context.Context, queries *exedb.Queries) (exedb.GetBoxDetailsForSetupRow, error) {
		return queries.GetBoxDetailsForSetup(ctx, boxID)
	})
	if err != nil {
		return fmt.Errorf("failed to get box details: %v", err)
	}

	containerID := ""
	if result.ContainerID != nil {
		containerID = *result.ContainerID
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
	err = s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return queries.UpdateBoxSSHDetails(ctx, exedb.UpdateBoxSSHDetailsParams{
			SSHServerIdentityKey: []byte(sshKeys.ServerIdentityKey),
			SSHAuthorizedKeys:    &sshKeys.AuthorizedKeys,
			SSHClientPrivateKey:  []byte(sshKeys.ClientPrivateKey),
			SSHPort:              func() *int64 { p := int64(sshKeys.SSHPort); return &p }(),
			ID:                   boxID,
		})
	})
	if err != nil {
		return fmt.Errorf("failed to update box SSH keys: %v", err)
	}

	log.Printf("SSH setup completed for box %d", boxID)
	return nil
}
