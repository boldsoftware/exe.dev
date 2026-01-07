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
	"io"
	"log"
	"log/slog"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	txttmpl "text/template"
	"time"

	"exe.dev/billing"
	"exe.dev/boxname"
	"exe.dev/bsdns"
	"exe.dev/bsdns/alley53"
	"exe.dev/container"
	docspkg "exe.dev/docs"
	"exe.dev/domz"
	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	"exe.dev/exens"
	"exe.dev/ghuser"
	"exe.dev/llmgateway"
	"exe.dev/logging"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/publicips"
	"exe.dev/route53"
	"exe.dev/sqlite"
	"exe.dev/sshpool2"
	"exe.dev/stage"
	"exe.dev/tagresolver"
	templatespkg "exe.dev/templates"
	emailverifier "github.com/AfterShip/email-verifier"
	"github.com/keighl/postmark"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
)

//go:embed static
var staticFS embed.FS

// buildTime returns the VCS commit time from build info, or the process start time as fallback.
// Used as the modification time for embedded static files to enable HTTP caching.
var buildTime = sync.OnceValue(func() time.Time {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.time" {
				if t, err := time.Parse(time.RFC3339, setting.Value); err == nil {
					return t
				}
			}
		}
	}
	return time.Now()
})

// Region represents a geographical region where resources are allocated
type Region string

const (
	RegionAWSUSWest2 Region = "aws-us-west-2" // Default and only region for now
)

const (
	// Timeout for long-running operations like box creation and Shelley prompts
	longOperationTimeout = 30 * time.Minute
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
	stage.Env
	SSHCommand   string
	User         exedb.User
	SSHKeys      []SSHKey
	Passkeys     []PasskeyInfo
	Boxes        []BoxDisplayInfo
	SharedBoxes  []SharedBoxDisplayInfo
	SiteSessions []SiteSession
	ActivePage   string
	IsLoggedIn   bool
	// BasicUser is true if the user has no SSH keys, no boxes, and was created for login-with-exe.
	// These users should only see the profile tab and a "what is exe?" section.
	BasicUser bool
}

// SiteSession represents an active session cookie for a site hosted by exe
type SiteSession struct {
	Domain     string
	URL        string // Full URL with https://
	LastUsedAt string // Formatted time string
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
	closeOnce    sync.Once
	CreatedAt    time.Time
	IsNewAccount bool

	// UserID is pre-generated for the new user. It is set after email
	// verification but before user creation.
	UserID string

	// BillingID is the billing account ID created before Stripe checkout.
	// It is used as the Stripe customer ID.
	BillingID string

	// Err is set if billing checkout fails or is canceled. The SSH session
	// checks this after CompleteChan closes.
	Err error
}

// Close signals completion to the waiting SSH session.
// Safe to call multiple times; only the first call closes the channel.
func (v *EmailVerification) Close() {
	v.closeOnce.Do(func() {
		close(v.CompleteChan)
	})
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
	env        stage.Env // prod, staging, local, test, etc.
	httpLn     *listener
	proxyLns   []*listener // Additional listeners for proxy ports
	httpsLn    *listener
	sshLn      *listener
	pluginLn   *listener
	piperdPort int // what port sshpiperd is listening on, typically 2222
	// PublicIPs maps private (local address) IPs to public IP / domain / shard.
	PublicIPs map[netip.Addr]publicips.PublicIP

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

	// route53Provider answers ACME DNS challenges (only when UseRoute53)
	route53Provider *route53.DNSProvider
	// bsdns manages box shard DNS records (route53 or alley53)
	bsdns bsdns.Provider
	// dnsServer is the embedded DNS nameserver (only when UseRoute53)
	dnsServer *exens.Server

	// Testing hooks
	lookupCNAMEFunc func(context.Context, string) (string, error)
	lookupAFunc     func(context.Context, string, string) ([]netip.Addr, error)
	boxExistsFunc   func(context.Context, string) bool
	stopCobble      func()

	// Tailscale HTTPS (preloaded at startup)
	tsCertMu sync.Mutex
	tsCert   *tls.Certificate
	tsDomain string

	// Piper plugin for SSH proxy authentication
	piperPlugin *PiperPlugin

	// Database
	db *sqlite.DB

	// Image tag resolution
	tagResolver *tagresolver.TagResolver

	// Exelet management (for VM-based instances)
	exeletClients map[string]*exeletClient // addr -> client
	exeletAddrs   []string                 // list of exelet addresses

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

	// IPQS email quality service
	ipqsAPIKey string

	// Metrics
	metricsRegistry *prometheus.Registry
	sshMetrics      *SSHMetrics
	httpMetrics     *HTTPMetrics

	// Data isolation
	dataSubdir string // subdirectory under /data for container isolation

	docs *docspkg.Handler

	// HTML templates (parsed at startup)
	templates *template.Template

	stopping atomic.Bool

	// General purpose slogger
	log *slog.Logger
	// net/http server error logger
	netHTTPLogger *log.Logger

	// Slack feed for posting events and tracking new user signups
	slackFeed *logging.SlackFeed

	// Billing manager for subscription management
	billing *billing.Manager
}

// exeletClient wraps an exelet client with its address
type exeletClient struct {
	addr   string
	client *exeletclient.Client
}

// countInstances returns the number of instances on this exelet.
func (ec *exeletClient) countInstances(ctx context.Context) (int, error) {
	stream, err := ec.client.ListInstances(ctx, &computeapi.ListInstancesRequest{})
	if err != nil {
		return 0, err
	}
	count := 0
	for {
		_, err := stream.Recv()
		if err != nil {
			break
		}
		count++
	}
	return count, nil
}

func (s *Server) slog() *slog.Logger {
	if s.log != nil {
		return s.log
	}
	return slog.Default()
}

func (s *Server) netHTTPLog() *log.Logger {
	if s.netHTTPLogger == nil {
		w := &httpServerLogger{slogger: s.slog()}
		s.netHTTPLogger = log.New(w, "", 0)
	}
	return s.netHTTPLogger
}

// httpServerLogger routes net/http server errors through slogger.
// It suppresses noisy lines.
type httpServerLogger struct {
	slogger *slog.Logger
}

func (w *httpServerLogger) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	// In a random sample on Nov 17, 2025, this log type accounted for about 85% of all log lines.
	if strings.HasPrefix(msg, "http: TLS handshake error from ") {
		return len(p), nil
	}
	w.slogger.Debug("net/http server error", "msg", msg)
	return len(p), nil
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
	if addr == "" {
		return nil, errors.New("address is empty")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, getPortOwnerInfo(addr))
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	// this log line is important for e2e tests, they parse it to get port numbers!
	// ...except we silence it for proxy listening ports, which number in the thousands.
	if !strings.HasPrefix(typ, "proxy-") {
		slog.Info("listening", "type", typ, "addr", tcpAddr.String(), "port", tcpAddr.Port)
	}
	return &listener{
		origAddr: addr,
		ln:       ln,
		addr:     tcpAddr.String(),
		tcp:      tcpAddr,
	}, nil
}

// getPortOwnerInfo tries to identify what process is using a port.
// Returns a human-readable string with the PID and process name, or an error message.
func getPortOwnerInfo(addr string) string {
	// Extract port from addr (could be ":8080" or "0.0.0.0:8080" etc)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		// addr might just be a port number
		port = addr
	}

	cmd := exec.Command("lsof", "-i", ":"+port, "-sTCP:LISTEN", "-n", "-P")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("unable to determine port owner: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return "no process found"
	}

	// Parse lsof output: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
	// Skip the header line
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			command := fields[0]
			pid := fields[1]
			return fmt.Sprintf("pid=%s process=%s", pid, command)
		}
	}

	return "could not parse lsof output"
}

func (s *Server) servingHTTP() bool {
	return s.httpLn != nil && s.httpLn.tcp != nil
}

func (s *Server) servingHTTPS() bool {
	return s.httpsLn != nil && s.httpsLn.tcp != nil
}

// httpPort returns the HTTP listening port, or -1 if not listening.
func (s *Server) httpPort() int {
	if s.servingHTTP() {
		return s.httpLn.tcp.Port
	}
	return -1
}

// httpsPort returns the HTTPS listening port, or -1 if not listening.
func (s *Server) httpsPort() int {
	if s.servingHTTPS() {
		return s.httpsLn.tcp.Port
	}
	return -1
}

// isMainListenerPort returns true if the port is the server's main HTTP or HTTPS port.
func (s *Server) isMainListenerPort(port int) bool {
	return port == s.httpPort() || port == s.httpsPort()
}

// httpURLPort returns :PORT for use in http URLs.
// It returns an empty string if not listening on HTTP, or if the port is 80.
func (s *Server) httpURLPort() string {
	if !s.servingHTTP() || s.httpLn.tcp.Port == 80 {
		return ""
	}
	return fmt.Sprintf(":%d", s.httpLn.tcp.Port)
}

// httpsURLPort returns :PORT for use in https URLs.
// It returns an empty string if not listening on HTTPS, or if the port is 443.
func (s *Server) httpsURLPort() string {
	if !s.servingHTTPS() || s.httpsLn.tcp.Port == 443 {
		return ""
	}
	return fmt.Sprintf(":%d", s.httpsLn.tcp.Port)
}

// urlPort returns :PORT for use in URLs, according to useTLS.
func (s *Server) urlPort(useTLS bool) string {
	if useTLS {
		return s.httpsURLPort()
	}
	return s.httpURLPort()
}

func (s *Server) bestScheme() string {
	return schemeForTLS(s.servingHTTPS())
}

// bestURLPort returns :PORT for use in URLs, according to s.bestScheme().
func (s *Server) bestURLPort() string {
	return s.urlPort(s.servingHTTPS())
}

func runMigrations(slog *slog.Logger, dbPath string) error {
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database for migrations: %w", err)
	}
	defer rawDB.Close()
	// Checkpoint WAL before creating the pool or running migrations.
	// Added in Dec 2025 to enforce WAL cleanup on server start,
	// because we were having (as-yet-undiagnosed) connection leaks
	// leading to giant WAL files.
	// We can probably remove at some point in the future(?).
	if _, err := rawDB.Exec("PRAGMA wal_checkpoint(FULL)"); err != nil {
		return fmt.Errorf("failed to checkpoint WAL: %w", err)
	}
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

// ServerConfig contains all configuration for creating a new Server.
//
//exe:completeinit
type ServerConfig struct {
	Logger          *slog.Logger
	HTTPAddr        string
	HTTPSAddr       string
	SSHAddr         string
	PluginAddr      string
	DBPath          string
	FakeEmailServer string
	PiperdPort      int
	GHWhoAmIPath    string
	ExeletAddresses []string
	Env             stage.Env
	MetricsRegistry *prometheus.Registry
}

// NewServer creates a new Server instance with database and container management.
func NewServer(cfg ServerConfig) (*Server, error) {
	slog := cfg.Logger
	// Run db migrations with a raw connection (not a pool).
	if err := runMigrations(slog, cfg.DBPath); err != nil {
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	const nReaders = 16
	db, err := sqlite.New(cfg.DBPath, nReaders)
	if err != nil {
		return nil, fmt.Errorf("failed to create sqlite connection pool: %w", err)
	}

	slog.Debug("opened database connection pool", "dbPath", cfg.DBPath, "nReaders", nReaders)

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

	// Initialize IPQS API key for email quality checks
	ipqsAPIKey := os.Getenv("IPQS_API_KEY")
	if ipqsAPIKey == "" {
		slog.Info("IPQS_API_KEY not set, email quality checks disabled")
	}

	// Initialize GitHub User lookup client
	ghu, err := ghuser.New(os.Getenv("GITHUB_TOKEN"), cfg.GHWhoAmIPath)
	if err != nil {
		slog.Warn("failed to create GitHub user key lookup client", "error", err)
	}

	var baseURL string
	httpLn := unusedListener(cfg.HTTPAddr)
	if cfg.HTTPAddr != "" {
		// HTTP is configured, use http://localhost with the HTTP port
		httpLn, err = startListener(slog, "http", cfg.HTTPAddr)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to listen on HTTP address %q: %w", cfg.HTTPAddr, err)
		}
		baseURL = fmt.Sprintf("http://%s:%d", cfg.Env.WebHost, httpLn.tcp.Port)
		slog.Info("http server listening", "addr", httpLn.tcp.String(), "port", httpLn.tcp.Port)
	}

	httpsLn := unusedListener(cfg.HTTPSAddr)
	if cfg.HTTPSAddr != "" {
		httpsLn, err = startListener(slog, "https", cfg.HTTPSAddr)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to listen on HTTPS address %q: %w", cfg.HTTPSAddr, err)
		}
		baseURL = fmt.Sprintf("https://%s", cfg.Env.WebHost)
		if httpsLn.tcp.Port != 443 {
			baseURL += fmt.Sprintf(":%d", httpsLn.tcp.Port)
		}
	}

	sshLn, err := startListener(slog, "ssh", cfg.SSHAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on SSH address %q: %w", cfg.SSHAddr, err)
	}

	pluginLn, err := startListener(slog, "plugin", cfg.PluginAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on piper plugin address %q: %w", cfg.PluginAddr, err)
	}

	// Initialize metrics
	sshMetrics := NewSSHMetrics(cfg.MetricsRegistry)
	httpMetrics := NewHTTPMetrics(cfg.MetricsRegistry)
	sqlite.RegisterSQLiteMetrics(cfg.MetricsRegistry)
	llmgateway.RegisterMetrics(cfg.MetricsRegistry)
	RegisterEntityMetrics(cfg.MetricsRegistry, db, slog)

	// Initialize tag resolver for image tag resolution
	tagResolverInstance := tagresolver.New(db)

	// Initialize exelet clients
	exeletClients := make(map[string]*exeletClient)
	var validExeletAddrs []string
	exeletClientMetrics := exeletclient.NewClientMetrics(cfg.MetricsRegistry)
	for _, addr := range cfg.ExeletAddresses {
		if addr == "" {
			continue
		}
		client, err := exeletclient.NewClient(addr,
			exeletclient.WithInsecure(),
			exeletclient.WithLogger(slog),
			exeletclient.WithClientMetrics(exeletClientMetrics))
		if err != nil {
			slog.Warn("failed to create exelet client, skipping host", "addr", addr, "error", err)
			continue
		}

		// Try to fetch system info to cache architecture and version.
		// Log but don't fail startup if this fails - the host may be temporarily unavailable.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err = client.GetSystemInfo(ctx, &computeapi.GetSystemInfoRequest{})
		cancel()
		if err != nil {
			slog.Warn("failed to get system info from exelet, will retry later", "addr", addr, "error", err)
		}

		exeletClients[addr] = &exeletClient{
			addr:   addr,
			client: client,
		}
		validExeletAddrs = append(validExeletAddrs, addr)
		slog.Info("initialized exelet client", "addr", addr, "arch", client.Arch(), "version", client.Version())
	}
	slog.Info("exelet clients initialized", "count", len(validExeletAddrs))

	includeUnpublishedDocs := cfg.Env.ShowHiddenDocs
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
		env:                cfg.Env,
		httpLn:             httpLn,
		httpsLn:            httpsLn,
		sshLn:              sshLn,
		pluginLn:           pluginLn,
		piperdPort:         cfg.PiperdPort,
		db:                 db,
		tagResolver:        tagResolverInstance,
		exeletClients:      exeletClients,
		exeletAddrs:        validExeletAddrs,
		sshPool:            &sshpool2.Pool{TTL: 10 * time.Minute},
		emailVerifications: make(map[string]*EmailVerification),
		magicSecrets:       make(map[string]*MagicSecret),
		creationStreams:    make(map[creationStreamKey]*CreationStream),
		githubUser:         ghu,
		postmarkClient:     postmarkClient,
		fakeHTTPEmail:      cfg.FakeEmailServer,
		ipqsAPIKey:         ipqsAPIKey,
		PublicIPs:          map[netip.Addr]publicips.PublicIP{},

		metricsRegistry: cfg.MetricsRegistry,
		sshMetrics:      sshMetrics,
		httpMetrics:     httpMetrics,
		dataSubdir:      dataSubdir,

		docs:      docsHandler,
		templates: tmpl,
		log:       slog,
		slackFeed: logging.NewSlackFeed(slog, cfg.Env),
		billing:   cfg.Env.BillingClient(),
	}

	// Set up HTTP metrics host functions for in-flight label tracking
	s.httpMetrics.SetHostFuncs(s.isProxyRequest, func(host string) string {
		hostname, _, _ := net.SplitHostPort(host)
		if hostname == "" {
			hostname = host
		}
		return domz.Label(hostname, s.env.BoxHost)
	})

	// Initialize DNS providers (both ACME and box shard DNS)
	if cfg.Env.UseRoute53 {
		// Prod/staging: Use embedded DNS server for exe.xyz, Route53 for exe.dev
		// During transition, Route53 is also used for exe.xyz box CNAMEs
		s.route53Provider = route53.NewDNSProvider()
		s.dnsServer = exens.NewServer(s.db, s.log, cfg.Env.BoxHost, cfg.Env.WebHost)
		s.bsdns = s.route53Provider // Route53 for box CNAME updates during transition
	} else {
		// Use alley53 if available. If not, fall back to a no-op provider.
		c := alley53.NewClient("localhost:5380")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if c.IsRunning(ctx) {
			s.bsdns = c
		} else {
			s.bsdns = bsdns.Discard{}
		}
	}

	s.setupHTTPServer()
	s.setupHTTPSServer()
	s.setupProxyServers()
	s.setupSSHServer()

	s.ready.Add(1) // matched with final done at bottom of Start
	go func() {
		s.ready.Wait()
		// The following log line signals to e2e tests that they may proceed with using the server (better than sleeps!)
		s.slog().Info("server started", "url", baseURL)
	}()

	return s, nil
}

// initShardIPs sets up the IP resolver for mapping local IPs to public IP info.
// DiscoverPublicIPs=true: use EC2 metadata to discover private->public IP mappings (required because EC2 has a 1:1 NAT).
// DiscoverPublicIPs=false: use 127.21.0.x where x is the shard number.
func (s *Server) initShardIPs(ctx context.Context) {
	defer s.logIPResolver()

	if len(s.PublicIPs) != 0 {
		// Already initialized (e.g., in tests)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	discoverIPs := publicips.EC2IPs // EC2 metadata-based discovery
	if !s.env.DiscoverPublicIPs {
		discoverIPs = publicips.LocalhostIPs // 127.21.0.x
		s.slog().InfoContext(ctx, "using dev IP resolver", "box_host", s.env.BoxHost)
	}

	ips, err := discoverIPs(ctx, s.env.BoxHost)
	if err != nil {
		s.slog().ErrorContext(ctx, "public IP discovery failed", "error", err)
		return
	}
	s.PublicIPs = ips
}

func (s *Server) logIPResolver() {
	if len(s.PublicIPs) == 0 {
		s.slog().Warn("no public IP assignments discovered via metadata")
		return
	}

	assignments := make([]string, 0, len(s.PublicIPs))
	for privateAddr, info := range s.PublicIPs {
		assignments = append(assignments, fmt.Sprintf("%s->%s (%s)", privateAddr, info.IP, info.Domain))
	}
	slices.Sort(assignments)
	s.slog().Info("public IP assignments loaded", "assignments", assignments)
}

// validateIPShards checks that all IP shards in the DB have corresponding private IPs
// on this machine. Logs an error for each shard that is missing.
func (s *Server) validateIPShards(ctx context.Context) {
	dbShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListIPShards)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to list IP shards for validation", "error", err)
		return
	}

	// Build a set of shards we have on this machine
	localShards := make(map[int]bool)
	for _, info := range s.PublicIPs {
		localShards[info.Shard] = true
	}

	// Check each DB shard
	for _, dbShard := range dbShards {
		if !localShards[int(dbShard.Shard)] {
			s.slog().ErrorContext(ctx, "ip_shard in DB missing from this machine",
				"shard", dbShard.Shard,
				"public_ip", dbShard.PublicIp)
		}
	}
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

// withRxRes0 executes a sqlc query with a read-only database transaction and no arguments, returning a value.
func withRxRes0[T any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context) (T, error)) (T, error) {
	var result T
	err := s.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		result, err = fn(queries, ctx)
		return err
	})
	return result, err
}

// withRxRes1 executes a sqlc query with a read-only database transaction and one argument, returning a value.
func withRxRes1[T, A any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context, A) (T, error), a A) (T, error) {
	var result T
	err := s.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		result, err = fn(queries, ctx, a)
		return err
	})
	return result, err
}

// withTx0 executes a sqlc query with a read-write database transaction and no arguments.
func withTx0(s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context) error) error {
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return fn(queries, ctx)
	})
}

// withTx1 executes a sqlc query with a read-write database transaction and one argument.
func withTx1[A any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context, A) error, a A) error {
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		return fn(queries, ctx, a)
	})
}

// withTxRes0 executes a function with a read-write database transaction, returning a value.
func withTxRes0[T any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context) (T, error)) (T, error) {
	var result T
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		result, err = fn(queries, ctx)
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
	// Try to load existing host key from database (prod)
	hostKey, err := withRxRes0(s, ctx, (*exedb.Queries).GetSSHHostKey)
	privateKeyPEM := hostKey.PrivateKey

	if errors.Is(err, sql.ErrNoRows) {
		// No existing key, generate a new one (staging, local, test)
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
		publicKeyPEM := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))

		// Calculate fingerprint
		fingerprint := s.GetPublicKeyFingerprint(signer.PublicKey())

		// Store in database
		err = withTx1(s, ctx, (*exedb.Queries).UpsertSSHHostKey, exedb.UpsertSSHHostKeyParams{
			PrivateKey:  privateKeyPEM,
			PublicKey:   publicKeyPEM,
			Fingerprint: fingerprint,
		})
		if err != nil {
			return fmt.Errorf("failed to store host key: %w", err)
		}

		if err := s.installSSHHostKey(signer, nil); err != nil {
			return err
		}
		s.slog().DebugContext(ctx, "Generated and stored new SSH host key", "fingerprint", fingerprint)

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
		s.slog().DebugContext(ctx, "Loaded existing SSH host key", "fingerprint", fingerprint)
	}

	return nil
}

func (s *Server) knownHostsTarget(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("known hosts target host is empty")
	}
	if s.piperdPort == 0 {
		return "", errors.New("ssh piperd port is not configured")
	}

	if s.piperdPort != 22 {
		return fmt.Sprintf("[%s]:%d", host, s.piperdPort), nil
	}
	if host == "exe.dev" || host == s.env.BoxHost {
		return fmt.Sprintf("%s,*.%s", host, host), nil
	}
	return host, nil
}

// knownHostsLine returns the @cert-authority entry a user should add to known_hosts.
func (s *Server) knownHostsLine(ctx context.Context, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("known hosts target host is empty")
	}

	target, err := s.knownHostsTarget(host)
	if err != nil {
		return "", err
	}

	hostKey, err := withRxRes0(s, ctx, (*exedb.Queries).GetSSHHostKey)
	if err != nil {
		return "", fmt.Errorf("failed to load ssh host certificate: %w", err)
	}
	if hostKey.CertSig == nil {
		return "", errors.New("ssh host certificate has not been configured")
	}

	certData := strings.TrimSpace(*hostKey.CertSig)
	if certData == "" {
		return "", errors.New("ssh host certificate is empty")
	}

	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(certData))
	if err != nil {
		return "", fmt.Errorf("failed to parse ssh host certificate: %w", err)
	}

	cert, ok := pubKey.(*ssh.Certificate)
	if !ok {
		return "", fmt.Errorf("stored ssh host certificate is %T, want *ssh.Certificate", pubKey)
	}
	if cert.SignatureKey == nil {
		return "", errors.New("ssh host certificate is missing a signature key")
	}

	caKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert.SignatureKey)))
	if caKey == "" {
		return "", errors.New("derived ssh ca key is empty")
	}

	comment := fmt.Sprintf("%s ssh ca", host)
	if fields := strings.Fields(caKey); len(fields) <= 2 {
		caKey = fmt.Sprintf("%s %s", caKey, comment)
	}

	return fmt.Sprintf("@cert-authority %s %s", target, caKey), nil
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

var errNoEmailService = errors.New("email service not configured")

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
	if s.env.FakeEmail {
		s.slog().Info("DEV MODE: Would send email", "to", to, "subject", subject, "body", body)
		return nil
	}

	// Check if email service is configured
	if s.postmarkClient == nil {
		return errNoEmailService
	}

	// Use the existing sendVerificationEmail logic
	email := postmark.Email{
		From:     fmt.Sprintf("%s <support@%s>", s.env.WebHost, s.env.WebHost),
		To:       to,
		Subject:  subject,
		TextBody: body,
	}

	_, err := s.postmarkClient.SendEmail(email)
	if err != nil {
		s.slog().Warn("failed to send email", "to", to, "subject", subject, "error", err)
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

var boxCreatedEmailTemplate = txttmpl.Must(txttmpl.New("box-created").Parse(`You have created {{.VMName}}.exe.xyz

SSH:
{{.SSHCommand}}

App:
{{.ProxyAddr}}

{{- if .ShelleyURL }}

Shelley coding agent:
{{.ShelleyURL}}
{{- end }}

XTerm:
{{.XTermURL}}

VSCode:
{{.VSCodeURL}}

To prevent emails like this, pass the -no-email flag to new.
`))

// sendBoxCreatedEmail sends a confirmation email when a new box is created
func (s *Server) sendBoxCreatedEmail(to string, details newBoxDetails) {
	subject := fmt.Sprintf("exe.dev: created %s.exe.xyz", details.VMName)

	body := new(strings.Builder)
	if err := boxCreatedEmailTemplate.Execute(body, details); err != nil {
		s.slog().Warn("failed to render box created email", "error", err)
		return
	}

	if err := s.sendEmail(to, subject, body.String()); err != nil {
		s.slog().Warn("failed to send box created email", "to", to, "box", details.VMName, "error", err)
	}
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
	s.slog().DebugContext(ctx, "Authentication request", "user", user, "remote_addr", remoteAddr, "key_type", key.Type())

	// Check if this is a proxy connection from sshpiper
	s.slog().DebugContext(ctx, "Checking if key is a proxy key")
	if originalUserKey, localAddress, isProxy := s.lookupEphemeralProxyKey(key); isProxy {
		s.slog().DebugContext(ctx, "Ephemeral proxy authentication detected", "user", user, "local_address", localAddress)
		return s.authenticateProxyUserWithLocalAddress(ctx, user, originalUserKey, localAddress)
	} else {
		s.slog().DebugContext(ctx, "Not a proxy key, treating as direct user connection")
	}
	// Log non-proxy connections for monitoring - in production, all connections should come via proxy
	s.slog().WarnContext(ctx, "Direct connection to exed - should come via proxy", "remote_addr", remoteAddr)

	// First check if this key is already registered in ssh_keys table
	email, verified, err := s.GetEmailBySSHKey(ctx, publicKeyStr)
	if err != nil {
		s.slog().ErrorContext(ctx, "Database error checking SSH key", "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user exists
		_, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserIDByEmail, email)
		if err == nil {
			// User exists and has verified their email, they're fully registered
			return &ssh.Permissions{
				Extensions: map[string]string{
					"registered": "true",
					"email":      email,
					"public_key": publicKeyStr,
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

// checkEmailVerificationToken checks if an email verification token is valid without consuming it
func (s *Server) checkEmailVerificationToken(ctx context.Context, token string) (exedb.GetEmailVerificationByTokenRow, error) {
	row, err := withRxRes1(s, ctx, (*exedb.Queries).GetEmailVerificationByToken, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return exedb.GetEmailVerificationByTokenRow{}, fmt.Errorf("invalid verification token")
		}
		return exedb.GetEmailVerificationByTokenRow{}, fmt.Errorf("database error: %w", err)
	}

	// Check if token has expired
	if time.Now().After(row.ExpiresAt) {
		// Clean up expired token
		withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeleteEmailVerificationByToken, token)
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

	// Clean up used token - use context.WithoutCancel to ensure cleanup completes even if client disconnects
	withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeleteEmailVerificationByToken, token)

	return row.UserID, nil
}

// storeEmailVerification stores an email verification token
func (s *Server) storeEmailVerification(ctx context.Context, email, token string) error {
	var userID string
	var isNewUser bool

	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		// Check if user exists, create if not
		var err error
		userID, err = queries.GetUserIDByEmail(ctx, email)
		if errors.Is(err, sql.ErrNoRows) {
			// User doesn't exist, create them (mobile flow, not login-with-exe)
			userID, err = s.createUserRecord(ctx, queries, email, false)
			if err != nil {
				return err
			}
			isNewUser = true
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
	if err != nil {
		return err
	}

	// Check email quality for new users (outside the transaction)
	if isNewUser {
		if err := s.checkEmailQuality(context.WithoutCancel(ctx), userID, email); err != nil {
			s.slog().WarnContext(ctx, "email quality check failed", "error", err, "email", email)
		}
	}

	return nil
}

// validateEmailVerificationByToken validates verification using a token
func (s *Server) validateEmailVerificationByToken(ctx context.Context, token string) (string, error) {
	userID, err := withRxRes1(s, ctx, (*exedb.Queries).GetEmailVerificationByPartialToken, token)
	if err != nil {
		return "", fmt.Errorf("invalid or expired token")
	}

	// Consume the token
	err = withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeleteEmailVerificationByToken, token)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to delete email verification token", "error", err)
	}

	return userID, nil
}

// validateAuthToken validates an authentication token and returns the user ID
func (s *Server) validateAuthToken(ctx context.Context, token, expectedSubdomain string) (string, error) {
	authToken, err := withRxRes1(s, ctx, (*exedb.Queries).GetAuthTokenInfo, token)
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
	err = withTx1(s, ctx, (*exedb.Queries).UpdateAuthTokenUsedAt, token)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to mark token as used", "error", err)
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
	if !boxname.IsValid(boxName) {
		// s.slog().Info("invalid box name format", "box", boxName)
		return nil
	}

	// Check if box exists and belongs to the user
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithOwnerNamed, exedb.BoxWithOwnerNamedParams{
		Name:            boxName,
		CreatedByUserID: userID,
	})
	if err != nil {
		s.slog().InfoContext(ctx, "FindBoxByNameForUser: box not found", "box", boxName, "error", err)
		return nil
	}

	return &box
}

// FindBoxByIPShard finds a box by the local IP address shard for a given user.
// This enables `ssh vmname.exe.cloud` to work like `ssh vmname@exe.cloud`.
func (s *Server) FindBoxByIPShard(ctx context.Context, userID, localIP string) *exedb.Box {
	if userID == "" || localIP == "" {
		return nil
	}
	host := domz.StripPort(localIP)
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}

	info, ok := s.PublicIPs[addr]
	if !ok {
		s.slog().InfoContext(ctx, "FindBoxByIPShard found no info for addr", "user_id", userID, "localIP", localIP, "addr", addr, "available", slices.Collect(maps.Keys(s.PublicIPs)))
		return nil
	}

	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByUserAndShard, exedb.GetBoxByUserAndShardParams{
		UserID:  userID,
		IPShard: int64(info.Shard),
	})
	if err != nil {
		s.slog().InfoContext(ctx, "GetBoxByUserAndShard failed", "user_id", userID, "localIP", localIP, "shard", info.Shard, "error", err)
		return nil
	}
	s.slog().InfoContext(ctx, "FindBoxByIPShard found", "user_id", userID, "localIP", localIP, "shard", info.Shard, "box_name", box.Name)
	return &box
}

// FindBoxForSupportUser finds a box by name if the user is a root support user and the box has support access enabled.
// Returns nil if the user is not a root support user or the box doesn't have support access enabled.
func (s *Server) FindBoxForSupportUser(ctx context.Context, userID, boxName string) *exedb.Box {
	if userID == "" || !boxname.IsValid(boxName) {
		return nil
	}

	// Check if user is a root support user
	isRootSupport, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserRootSupport, userID)
	if err != nil || isRootSupport != 1 {
		return nil
	}

	// Look up box by name with support access enabled
	box, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxByNameWithSupportAccess, boxName)
	if err != nil {
		s.slog().InfoContext(ctx, "FindBoxForSupportUser: box not found or support access not enabled", "box", boxName, "error", err)
		return nil
	}

	s.slog().InfoContext(ctx, "FindBoxForSupportUser: root support user accessing box with support access", "box", boxName, "user_id", userID)
	return &box
}

func (s *Server) boxSSHPort() int {
	if s.piperdPort != 22 {
		return s.piperdPort
	}
	return 22
}

// boxSSHConnectionCommand returns the SSH command to connect to box boxName.
func (s *Server) boxSSHConnectionCommand(boxName string) string {
	dashP := ""
	if port := s.boxSSHPort(); port != 22 {
		dashP = fmt.Sprintf("-p %d ", port)
	}
	return "ssh " + dashP + s.env.BoxDest(boxName)
}

// replSSHConnectionCommand returns the SSH command to connect to the REPL server.
func (s *Server) replSSHConnectionCommand() string {
	var dashP string
	if s.piperdPort != 22 {
		dashP = fmt.Sprintf("-p %v ", s.piperdPort)
	}
	return "ssh " + dashP + s.env.ReplHost
}

// boxProxyAddress returns the HTTPS proxy address for a box.
func (s *Server) boxProxyAddress(boxName string) string {
	return fmt.Sprintf("%s://%s%s", s.bestScheme(), s.env.BoxSub(boxName), s.bestURLPort())
}

// xtermURL returns the terminal URL for a box.
func (s *Server) xtermURL(boxName string, useTLS bool) string {
	return fmt.Sprintf("%s://%s%s", schemeForTLS(useTLS), s.env.BoxXtermSub(boxName), s.urlPort(useTLS))
}

// shelleyURL returns the Shelley agent URL for a box (vm.shelley.exe.xyz).
func (s *Server) shelleyURL(boxName string) string {
	return fmt.Sprintf("%s://%s%s", s.bestScheme(), s.env.BoxShelleySub(boxName), s.bestURLPort())
}

// vscodeURL returns the VSCode remote SSH URL for a box.
func (s *Server) vscodeURL(boxName string) string {
	var colonP string
	if s.boxSSHPort() != 22 {
		colonP = fmt.Sprintf(":%d", s.boxSSHPort())
	}
	connStr := s.env.BoxDest(boxName) + colonP
	return fmt.Sprintf("vscode://vscode-remote/ssh-remote+%s/home/exedev?windowId=_blank", connStr)
}

// preCreateBox creates a box entry before the container is created, returns the box ID
//
//exe:completeinit
type preCreateBoxOptions struct {
	userID  string
	ctrhost string
	name    string
	image   string
	noShard bool
}

func (s *Server) preCreateBox(ctx context.Context, opts preCreateBoxOptions) (int, error) {
	// Validate box name
	if err := boxname.Valid(opts.name); err != nil {
		return 0, err
	}

	routes := exedb.DefaultRouteJSON()
	var boxID int
	var assignedShard int
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		id, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         opts.ctrhost,
			Name:            opts.name,
			Status:          "creating",
			Image:           opts.image,
			CreatedByUserID: opts.userID,
			Routes:          &routes,
		})
		if err != nil {
			return err
		}
		boxID = int(id)

		if opts.noShard {
			return nil
		}

		shard, err := s.allocateIPShard(ctx, queries, opts.userID, boxID)
		if err != nil {
			return err
		}
		assignedShard = shard

		return nil
	})
	if err != nil {
		return 0, err
	}

	if !opts.noShard {
		if err := s.createBoxShardDNSRecord(ctx, opts.name, assignedShard); err != nil {
			cleanupErr := s.rollbackBoxPreCreation(ctx, boxID)
			if cleanupErr != nil {
				s.slog().ErrorContext(ctx, "failed to roll back box after DNS error", "box_id", boxID, "cleanup_error", cleanupErr, "dns_error", err)
			}
			return 0, err
		}
	}

	s.recordUserEventBestEffort(ctx, opts.userID, userEventCreatedBox)
	return boxID, nil
}

var errNoIPShardsAvailable = errors.New("no IP shards available")

func (s *Server) allocateIPShard(ctx context.Context, queries *exedb.Queries, userID string, boxID int) (int, error) {
	shards, err := queries.ListIPShardsForUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("failed to list IP shards for user %s: %w", userID, err)
	}

	used := make([]bool, s.env.NumShards+1)
	for _, shard := range shards {
		if !s.env.ShardIsValid(int(shard)) {
			continue
		}
		used[int(shard)] = true
	}

	var assigned int
	for candidate := 1; candidate <= s.env.NumShards; candidate++ {
		if !used[candidate] {
			assigned = candidate
			break
		}
	}

	if assigned == 0 {
		return 0, errNoIPShardsAvailable
	}

	if err := queries.InsertBoxIPShard(ctx, exedb.InsertBoxIPShardParams{
		BoxID:   boxID,
		UserID:  userID,
		IPShard: int64(assigned),
	}); err != nil {
		return 0, fmt.Errorf("failed to assign IP shard for box %d: %w", boxID, err)
	}
	return assigned, nil
}

func (s *Server) createBoxShardDNSRecord(ctx context.Context, boxName string, shard int) error {
	if !s.env.ShardIsValid(shard) {
		return fmt.Errorf("invalid IP shard %d for box %s", shard, boxName)
	}

	dnsCtx, cancel := context.WithTimeout(ctx, 30*time.Second) // TODO: 30s seems like a lot
	defer cancel()

	start := time.Now()
	err := s.bsdns.UpsertBoxRecord(dnsCtx, s.env.BoxHost, boxName, shard)
	CommandLogAddDuration(ctx, "dns", time.Since(start))

	if err != nil {
		return fmt.Errorf("failed to create DNS record for box %s: %w", boxName, err)
	}
	return nil
}

func (s *Server) deleteBoxShardDNSRecord(ctx context.Context, boxName string, shard int) error {
	if !s.env.ShardIsValid(shard) {
		return fmt.Errorf("invalid IP shard %d for box %s", shard, boxName)
	}
	if s.bsdns == nil {
		return nil // no DNS provider configured, skip
	}

	dnsCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := s.bsdns.DeleteBoxRecord(dnsCtx, s.env.BoxHost, boxName, shard); err != nil {
		return fmt.Errorf("failed to delete DNS record for box %s: %w", boxName, err)
	}
	return nil
}

// isExeletNotFoundError checks if an error from exelet indicates the instance doesn't exist.
// This handles the case where execore's database has a ContainerID but the instance
// is no longer present on the exelet (e.g., after exelet data loss or failed creation).
func isExeletNotFoundError(err error) bool {
	s, ok := status.FromError(err)
	return ok && s.Code() == codes.NotFound
}

// deleteBox deletes a box and all associated resources (container, database records, DNS).
// This is the canonical deletion implementation used by both the REPL `rm` command and the debug page.
func (s *Server) deleteBox(ctx context.Context, box exedb.Box) error {
	// Get IP shard before deletion for DNS cleanup
	var ipShard int64
	if s.env.UseRoute53 {
		shard, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxIPShard, box.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to get IP shard for DNS cleanup: %w", err)
		}
		ipShard = shard
	}

	// Delete the instance if it exists
	if box.ContainerID != nil {
		exeletClient := s.getExeletClient(box.Ctrhost)
		if exeletClient == nil {
			return fmt.Errorf("exelet host not available for VM")
		}

		_, err := exeletClient.client.DeleteInstance(ctx, &computeapi.DeleteInstanceRequest{
			ID: *box.ContainerID,
		})
		if err != nil && !isExeletNotFoundError(err) {
			return fmt.Errorf("failed to delete instance: %w", err)
		}
	}

	// Delete from database and track in deleted_boxes
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		userID := box.CreatedByUserID

		// Delete IP shard allocation first
		if err := queries.DeleteBoxIPShard(ctx, box.ID); err != nil {
			return fmt.Errorf("deleting IP shard: %w", err)
		}

		err := queries.InsertDeletedBox(ctx, exedb.InsertDeletedBoxParams{
			ID:     int64(box.ID),
			UserID: userID,
		})
		if err != nil {
			return fmt.Errorf("tracking deletion: %w", err)
		}
		return queries.DeleteBox(ctx, box.ID)
	})
	if err != nil {
		return err
	}

	// Clean up DNS record
	if ipShard > 0 {
		if err := s.deleteBoxShardDNSRecord(ctx, box.Name, int(ipShard)); err != nil {
			s.slog().WarnContext(ctx, "failed to delete DNS record", "box", box.Name, "shard", ipShard, "error", err)
		}
	}

	return nil
}

func (s *Server) rollbackBoxPreCreation(ctx context.Context, boxID int) error {
	return s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		if err := queries.DeleteBoxIPShard(ctx, boxID); err != nil {
			return err
		}
		if err := queries.DeleteBox(ctx, boxID); err != nil {
			return err
		}
		return nil
	})
}

// updateBoxWithContainer updates a box with container info and SSH keys after container creation
func (s *Server) updateBoxWithContainer(ctx context.Context, boxID int, containerID, sshUser string, sshKeys *container.ContainerSSHKeys, sshPort int) error {
	return withTx1(s, ctx, (*exedb.Queries).UpdateBoxContainerAndStatus, exedb.UpdateBoxContainerAndStatusParams{
		ContainerID:          &containerID,
		Status:               "running",
		SSHServerIdentityKey: []byte(sshKeys.ServerIdentityKey),
		SSHAuthorizedKeys:    &sshKeys.AuthorizedKeys,
		SSHClientPrivateKey:  []byte(sshKeys.ClientPrivateKey),
		SSHPort:              func() *int64 { p := int64(sshPort); return &p }(),
		SSHUser:              &sshUser,
		ID:                   boxID,
	})
}

// isBoxNameAvailable checks if a box name is available for use.
// Errors are translated into false (unavailability).
func (s *Server) isBoxNameAvailable(ctx context.Context, name string) bool {
	if !boxname.IsValid(name) {
		return false
	}

	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithNameExists, name)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to check box name availability", "error", err, "box_name", name)
		return false
	}
	return box == 0
}

func (s *Server) boxByNameExists(ctx context.Context, name string) bool {
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxWithNameExists, name)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to check box name existence", "error", err, "box_name", name)
		return false
	}
	return box > 0
}

func (s *Server) boxExists(ctx context.Context, name string) bool {
	if s.boxExistsFunc != nil {
		return s.boxExistsFunc(ctx, name)
	}
	return s.boxByNameExists(ctx, name)
}

// getBoxesByHost gets all boxes (machines) that should be on a specific ctrhost
func (s *Server) getBoxesByHost(ctx context.Context, ctrhost string) ([]*exedb.Box, error) {
	boxResults, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxesByHost, ctrhost)
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

// getExeletClient looks up an exelet client by host address, trying all normalized variants.
// This handles cases where the configured address changed (e.g., ssh://host to tcp://ip).
func (s *Server) getExeletClient(host string) *exeletClient {
	if client := s.exeletClients[host]; client != nil {
		return client
	}
	return nil
}

// emailVerifier is used to check for disposable email domains.
var emailVerifier = emailverifier.NewVerifier()

// isValidEmail checks if an email address has valid syntax.
func isValidEmail(email string) bool {
	return emailverifier.IsAddressValid(email)
}

// isDisposableEmail checks if an email uses a disposable/anonymized email provider.
func isDisposableEmail(email string) bool {
	syntax := emailVerifier.ParseAddress(email)
	if !syntax.Valid {
		return false
	}
	return emailVerifier.IsDisposable(syntax.Domain)
}

const (
	containerListRetryInitialDelay = 2 * time.Second
	containerListRetryMaxDelay     = 20 * time.Second
	containerListRetryTimeout      = 3 * time.Minute
)

func (s *Server) listInstancesWithRetry(ctx context.Context, addr string, client *exeletclient.Client) ([]*computeapi.Instance, error) {
	delay := containerListRetryInitialDelay
	deadline := time.Now().Add(containerListRetryTimeout)
	attempt := 0
	var lastErr error

	for {
		attempt++
		var instances []*computeapi.Instance
		stream, err := client.ListInstances(ctx, &computeapi.ListInstancesRequest{})
		if err == nil {
			// Collect all instances from the stream
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					lastErr = err
					break
				}
				instances = append(instances, resp.Instance)
			}
			if lastErr == nil {
				if attempt > 1 {
					s.slog().InfoContext(ctx, "Successfully listed instances on exelet after retry", "addr", addr, "attempts", attempt)
				}
				return instances, nil
			}
		} else {
			lastErr = err
		}

		s.slog().WarnContext(ctx, "Failed to list instances on exelet", "addr", addr, "attempt", attempt, "error", lastErr)

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out listing instances on exelet %s after %d attempts: %w", addr, attempt, lastErr)
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while waiting for exelet %s instances: %w", addr, ctx.Err())
		}

		if delay < containerListRetryMaxDelay {
			delay *= 2
			if delay > containerListRetryMaxDelay {
				delay = containerListRetryMaxDelay
			}
		}
	}
}

// syncInstancesWithHosts synchronizes instances between the database and exelet hosts
// This ensures that database state matches actual instance state on exelet hosts
func (s *Server) syncInstancesWithHosts(ctx context.Context) error {
	if len(s.exeletClients) == 0 {
		s.slog().WarnContext(ctx, "No exelet hosts available for instance sync")
		return nil
	}

	s.slog().InfoContext(ctx, "Starting instance sync with exelet hosts", "hostCount", len(s.exeletClients))

	// Process each exelet host
	for addr, ec := range s.exeletClients {
		if err := s.syncInstancesForExelet(ctx, addr, ec.client); err != nil {
			s.slog().ErrorContext(ctx, "Failed to sync instances for exelet", "addr", addr, "error", err)
			// Continue with other hosts even if one fails
		}
	}

	s.slog().InfoContext(ctx, "Instance sync completed")
	return nil
}

// syncInstancesForExelet synchronizes instances for a specific exelet host
func (s *Server) syncInstancesForExelet(ctx context.Context, addr string, client *exeletclient.Client) error {
	// Get boxes from the database that should be on this exelet
	dbBoxes, err := s.getBoxesByHost(ctx, addr)
	if err != nil {
		return fmt.Errorf("failed to get boxes from database: %w", err)
	}

	// Get instances currently on the exelet, retrying while the host is restarting
	instances, err := s.listInstancesWithRetry(ctx, addr, client)
	if err != nil {
		return err
	}

	// Create map of instances by ID for easier lookup
	instanceMap := make(map[string]*computeapi.Instance)
	for _, inst := range instances {
		instanceMap[inst.ID] = inst
	}

	// Check each box and update database state to match exelet reality
	for _, box := range dbBoxes {
		// Skip boxes without container IDs (not yet created)
		if box.ContainerID == nil || *box.ContainerID == "" {
			continue
		}

		containerID := *box.ContainerID
		instance, exists := instanceMap[containerID]

		if exists {
			// Instance exists - update database status to match actual state
			var newStatus string
			switch instance.State {
			case computeapi.VMState_RUNNING:
				newStatus = "running"
			case computeapi.VMState_STOPPED:
				newStatus = "stopped"
			case computeapi.VMState_ERROR:
				newStatus = "failed"
			default:
				newStatus = strings.ToLower(instance.State.String())
			}

			if box.Status != newStatus {
				s.slog().InfoContext(ctx, "Updating box status to match instance",
					"box", box.Name,
					"oldStatus", box.Status,
					"newStatus", newStatus,
					"addr", addr)
				if err := s.updateBoxStatus(ctx, box.ID, newStatus); err != nil {
					s.slog().ErrorContext(ctx, "Failed to update box status", "box", box.Name, "error", err)
				}
			}

			// Remove from map to track orphans
			delete(instanceMap, containerID)
		} else {
			// Instance doesn't exist on exelet but is in database
			s.slog().WarnContext(ctx, "Instance not found on exelet, marking as failed",
				"box", box.Name,
				"containerID", containerID,
				"addr", addr)
			if err := s.updateBoxStatus(ctx, box.ID, "failed"); err != nil {
				s.slog().ErrorContext(ctx, "Failed to mark box as failed", "box", box.Name, "error", err)
			}
		}
	}

	// Handle instances that are on exelet but not in DB (potential orphans)
	for instanceID, inst := range instanceMap {
		// Log orphaned instances but don't delete immediately
		// This provides a grace period and allows for manual investigation
		s.slog().WarnContext(ctx, "Found potentially orphaned instance on exelet - NOT deleting automatically",
			"instanceID", instanceID,
			"name", inst.Name,
			"addr", addr,
			"state", inst.State.String())

		// TODO: In the future, we could:
		// 1. Track when orphans were first detected
		// 2. Only delete after a grace period (e.g., 24 hours)
		// 3. Try to match with recently deleted boxes in deleted_boxes table
		// 4. Send alerts about orphaned instances
	}

	return nil
}

// updateBoxStatus updates the status of a box in the database
func (s *Server) updateBoxStatus(ctx context.Context, boxID int, status string) error {
	return withTx1(s, ctx, (*exedb.Queries).UpdateBoxStatus, exedb.UpdateBoxStatusParams{
		Status: status,
		ID:     boxID,
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

	// Start embedded DNS server FIRST, before initShardIPs.
	// initShardIPs does DNS lookups for sNNN.exe.xyz to map public IPs to shards.
	// Starting DNS first avoids a deadlock when NS records point to this server.
	// The DNS server reads from the ip_shards DB table (populated on previous boots).
	if s.dnsServer != nil && s.env.DiscoverPublicIPs {
		dnsCtx, dnsCancel := context.WithTimeout(ctx, 10*time.Second)
		privateIPs, err := publicips.EC2PrivateIPs(dnsCtx)
		dnsCancel()
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to get private IPs for DNS server", "error", err)
		} else if len(privateIPs) > 0 {
			if err := s.dnsServer.Start(ctx, privateIPs); err != nil {
				s.slog().ErrorContext(ctx, "DNS server failed to start", "error", err)
			} else {
				s.slog().InfoContext(ctx, "embedded DNS server started", "ips", privateIPs)
			}
		}
	}

	s.initShardIPs(ctx)

	// Populate ip_shards table and validate (after initShardIPs populates PublicIPs)
	if s.dnsServer != nil && len(s.PublicIPs) > 0 {
		if err := exens.PopulateIPShards(ctx, s.db, s.log, s.PublicIPs); err != nil {
			s.slog().ErrorContext(ctx, "ip_shards population failed", "error", err)
		}

		// Validate that all DB shards have corresponding private IPs on this machine
		s.validateIPShards(ctx)
	}

	s.slackFeed.ServiceStarted(ctx, "exed")

	// Start HTTP server in a goroutine if configured
	if s.httpLn.ln != nil {
		go func() {
			s.slog().DebugContext(ctx, "HTTP server starting", "addr", s.httpLn)
			if err := s.httpServer.Serve(s.httpLn.ln); err != nil && err != http.ErrServerClosed {
				s.slog().ErrorContext(ctx, "HTTP server startup failed", "error", err)
				cancel()
			}
		}()
	}

	// Start HTTPS server in a goroutine if configured
	if s.httpsLn.ln != nil {
		go func() {
			host := s.env.WebHost
			if host == "" {
				host = "configured host"
			}
			s.slog().InfoContext(ctx, "HTTPS server starting with Let's Encrypt", "host", host, "addr", s.httpsLn)
			if err := s.httpsServer.ServeTLS(s.httpsLn.ln, "", ""); err != nil && err != http.ErrServerClosed {
				s.slog().ErrorContext(ctx, "HTTPS server startup failed", "error", err)
				cancel()
			}
		}()

		if s.wildcardCertManager != nil {
			s.slog().InfoContext(ctx, "Using DNS challenges for wildcard main domain certificate")
		}
	}

	// Start proxy listeners with the same handlers. Prefer https if it's available
	for _, proxyLn := range s.proxyLns {
		go func(ln *listener) {
			if s.httpsLn.ln != nil {
				// s.slog().Info("Proxy listener starting with HTTPS handler", "addr", ln.tcp.String())
				if err := s.httpsServer.ServeTLS(proxyLn.ln, "", ""); err != nil && err != http.ErrServerClosed {
					s.slog().ErrorContext(ctx, "Proxy listener startup failed (HTTPS)", "error", err, "addr", ln)
					cancel()
				}
			} else {
				s.slog().InfoContext(ctx, "Proxy listener starting with HTTP handler", "addr", ln.tcp.String())
				if err := s.httpServer.Serve(ln.ln); err != nil && err != http.ErrServerClosed {
					s.slog().ErrorContext(ctx, "Proxy listener startup failed (HTTP)", "error", err, "addr", ln)
					cancel()
				}
			}
		}(proxyLn)
	}

	// Start SSH server in a goroutine
	go func() {
		s.sshServer = NewSSHServer(s)
		if err := s.sshServer.Start(s.sshLn.ln); err != nil {
			s.slog().ErrorContext(ctx, "SSH server startup failed", "error", err)
			cancel()
		}
	}()

	// Start piper plugin server in a goroutine
	s.slog().InfoContext(ctx, "piper plugin server listening", "addr", s.pluginLn.addr, "port", s.pluginLn.tcp.Port)
	s.piperPlugin = NewPiperPlugin(s, s.sshLn.tcp.Port)
	go func() {
		if err := s.piperPlugin.Serve(s.pluginLn.ln); err != nil {
			s.slog().ErrorContext(ctx, "Piper plugin server startup failed", "error", err)
			cancel()
		}
	}()

	if s.env.AutoStartSSHPiper {
		// In dev mode, automatically start sshpiper if not already running
		go s.autoStartSSHPiper(ctx)

		s.slog().InfoContext(ctx, "SSH server started in local dev mode. Connect with:")
		s.slog().InfoContext(ctx, fmt.Sprintf("  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %v localhost", s.sshLn.tcp.Port))
	}

	// Sync instances with exelet hosts before accepting connections
	if len(s.exeletClients) > 0 {
		if err := s.syncInstancesWithHosts(ctx); err != nil {
			s.slog().ErrorContext(ctx, "Failed to sync instances with exelet hosts", "error", err)
			// Continue anyway - we can sync later
		}
	}

	// Start tag resolver for keeping image tag resolutions fresh
	if s.tagResolver != nil {
		s.slog().InfoContext(ctx, "Starting tag resolver for image tag management")
		s.tagResolver.Start(ctx)
	}

	// Wait for interrupt signal or startup failure
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	s.ready.Done()

	select {
	case <-sigChan:
		s.slog().InfoContext(ctx, "Shutting down servers...")
		return s.Stop()
	case <-ctx.Done():
		s.slog().ErrorContext(ctx, "Server startup failed, shutting down")
		s.Stop()
		return fmt.Errorf("server startup failed")
	}
}

// autoStartSSHPiper automatically starts sshpiper.sh in dev mode if that port isn't listening
func (s *Server) autoStartSSHPiper(ctx context.Context) {
	// Check if sshpiper is already running on the specified port
	if s.isPortListening(fmt.Sprintf("localhost:%d", s.piperdPort)) {
		s.slog().InfoContext(ctx, "sshpiper already running", "port", s.piperdPort)
		return
	}

	// Use the actual piper TCP address
	if s.pluginLn.tcp == nil {
		s.slog().ErrorContext(ctx, "Piper TCP address not available")
		return
	}

	piperPluginAddr := fmt.Sprintf("localhost:%d", s.pluginLn.tcp.Port)

	// First, wait for the piper plugin to be ready
	if !s.waitForPort(ctx, piperPluginAddr, 30*time.Second) {
		s.slog().ErrorContext(ctx, "Timed out waiting for piper plugin to start", "addr", piperPluginAddr)
		return
	}

	// Start sshpiper.sh with the piper plugin port
	s.slog().InfoContext(ctx, "Starting sshpiper.sh automatically in dev mode", "piperPluginPort", s.pluginLn.tcp.Port)

	cmd := exec.CommandContext(ctx, "./sshpiper.sh", fmt.Sprint(s.pluginLn.tcp.Port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		s.slog().ErrorContext(ctx, "Failed to start sshpiper.sh", "error", err)
		return
	}

	// Wait for the process in a separate goroutine
	go func() {
		if err := cmd.Wait(); err != nil {
			s.slog().ErrorContext(ctx, "sshpiper.sh exited with error", "error", err)
		} else {
			s.slog().InfoContext(ctx, "sshpiper.sh exited normally")
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
	email, err = withRxRes1(s, ctx, (*exedb.Queries).GetEmailBySSHKey, publicKeyStr)
	if errors.Is(err, sql.ErrNoRows) {
		// Check if key exists in pending_ssh_keys (unverified)
		email, err = withRxRes1(s, ctx, (*exedb.Queries).GetPendingSSHKeyEmailByPublicKey, publicKeyStr)
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return email, false, err // Key exists in pending_ssh_keys, so not verified
	}
	return email, true, err // Key exists in ssh_keys, so verified
}

// getUserByPublicKey retrieves a user by their SSH public key
func (s *Server) getUserByPublicKey(ctx context.Context, publicKeyStr string) (*exedb.User, error) {
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithSSHKey, publicKeyStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// isBasicUser returns true if the user is a "basic user" - created for login-with-exe,
// has no SSH keys, and has no boxes. These users should only see the profile tab.
func (s *Server) isBasicUser(ctx context.Context, user exedb.User, sshKeyCount int) bool {
	if !user.CreatedForLoginWithExe || sshKeyCount > 0 {
		return false
	}
	boxCount, err := withRxRes1(s, ctx, (*exedb.Queries).CountBoxesForUser, user.UserID)
	if err != nil {
		s.slog().ErrorContext(ctx, "Failed to count boxes for basic user check", "error", err, "user_id", user.UserID)
		return false
	}
	return boxCount == 0
}

// createUserRecord creates a user record and returns the new user ID.
// If createdForLoginWithExe is true, the user was created during the login flow
// when trying to log into a site hosted by exe (via proxy auth with return_host).
func (s *Server) createUserRecord(ctx context.Context, queries *exedb.Queries, email string, createdForLoginWithExe bool) (string, error) {
	userID, err := generateUserID()
	if err != nil {
		return "", fmt.Errorf("failed to generate user ID: %w", err)
	}

	if err := queries.InsertUser(ctx, exedb.InsertUserParams{
		UserID:                 userID,
		Email:                  email,
		CreatedForLoginWithExe: createdForLoginWithExe,
	}); err != nil {
		return "", fmt.Errorf("failed to create user: %w", err)
	}

	return userID, nil
}

// checkEmailQuality checks the email quality via IPQS and updates the user if disposable.
// This should be called after user creation, outside of any transaction.
// Returns nil if IPQS is disabled (no API key).
func (s *Server) checkEmailQuality(ctx context.Context, userID, email string) error {
	if s.ipqsAPIKey == "" {
		return nil
	}

	// Call IPQS API with a timeout
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://www.ipqualityscore.com/api/json/email/%s/%s", s.ipqsAPIKey, email)
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create IPQS request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("IPQS request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read IPQS response: %w", err)
	}

	// Save the response to the database
	if err := withTx1(s, context.Background(), (*exedb.Queries).InsertEmailAddressQuality, exedb.InsertEmailAddressQualityParams{
		Email:        email,
		ResponseJson: string(body),
	}); err != nil {
		return fmt.Errorf("failed to save email quality: %w", err)
	}

	// Parse response to check disposable flag
	var result struct {
		Success    bool `json:"success"`
		Disposable bool `json:"disposable"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("failed to parse IPQS response: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("IPQS returned unsuccessful response for %s", email)
	}

	// If disposable, disable VM creation for this user
	if result.Disposable {
		s.slog().InfoContext(ctx, "disposable email detected, disabling VM creation", "email", email, "user_id", userID)
		if err := withTx1(s, context.Background(), (*exedb.Queries).SetUserNewVMCreationDisabled, exedb.SetUserNewVMCreationDisabledParams{
			NewVmCreationDisabled: true,
			UserID:                userID,
		}); err != nil {
			return fmt.Errorf("failed to disable VM creation for user: %w", err)
		}
	}

	return nil
}

// createUser creates a new user with their resource allocation.
// This is used for SSH registration flow, not login-with-exe.
func (s *Server) createUser(ctx context.Context, publicKey, email string) (*exedb.User, error) {
	var user exedb.User

	// First create the user and allocation in the database
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())

		userID, err := s.createUserRecord(ctx, queries, email, false)
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

		user, err = queries.GetUserWithDetails(ctx, userID)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Check email quality and disable VM creation if disposable
	if err := s.checkEmailQuality(context.WithoutCancel(ctx), user.UserID, email); err != nil {
		s.slog().WarnContext(ctx, "email quality check failed", "error", err, "email", email)
	}

	// Resolve any pending shares for this email
	if err := s.resolvePendingShares(ctx, email, user.UserID); err != nil {
		return nil, fmt.Errorf("failed to resolve pending shares: %w", err)
	}

	return &user, nil
}

// Stop gracefully shuts down all servers
func (s *Server) Stop() error {
	s.stopping.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shutdown HTTP server
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			s.slog().ErrorContext(ctx, "HTTP server shutdown error", "error", err)
		}
	}

	// Shutdown HTTPS server if running
	if s.httpsServer != nil {
		if err := s.httpsServer.Shutdown(ctx); err != nil {
			s.slog().ErrorContext(ctx, "HTTPS server shutdown error", "error", err)
		}
	}

	if s.tagResolver != nil {
		s.tagResolver.Stop()
	}
	if s.dnsServer != nil {
		s.dnsServer.Stop(ctx)
	}
	if err := s.sshPool.Close(); err != nil {
		s.slog().ErrorContext(ctx, "SSH pool close error", "error", err)
	}
	if s.db != nil {
		s.db.Close()
	}

	if s.stopCobble != nil {
		s.stopCobble()
	}

	s.slog().DebugContext(ctx, "Servers stopped")
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

	s.slog().DebugContext(ctx, "Authenticating original user", "fingerprint", originalFingerprint, "username", username)

	// Look up the user by their original public key
	email, verified, err := s.GetEmailBySSHKey(ctx, originalKeyStr)
	if err != nil {
		s.slog().ErrorContext(ctx, "Database error checking SSH key", "fingerprint", originalFingerprint, "error", err)
	}

	if email != "" && verified {
		// This is a verified key, check if user exists
		user, err := s.GetUserByEmail(ctx, email)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.slog().ErrorContext(ctx, "Database error getting user", "email", email, "error", err)
		}

		if user != nil {
			// User is fully registered
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

// authenticateProxyUserWithLocalAddress authenticates a user through an ephemeral proxy connection
// and includes the local address for ipAllocator routing
func (s *Server) authenticateProxyUserWithLocalAddress(ctx context.Context, username string, originalUserKeyBytes []byte, localAddress string) (*ssh.Permissions, error) {
	s.slog().InfoContext(ctx, "authenticateProxyUserWithLocalAddress", "username", username, "localAddress", localAddress, "keyBytes", len(originalUserKeyBytes))

	// Check for special container-logs username format and easter egg careers usernames
	if strings.HasPrefix(username, "container-logs:") || slices.Contains(boxname.JobsRelated, username) {
		s.slog().InfoContext(ctx, "Detected special container-logs username, bypassing normal auth", "username", username)
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
	userID, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserIDBySSHKey, publicKeyStr)
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
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserByEmail, email)
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
	result, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxSSHDetails, boxID)
	if err != nil {
		return nil, fmt.Errorf("failed to query box SSH details: %v", err)
	}

	if result.SSHPort == nil || *result.SSHPort == 0 || len(result.SSHClientPrivateKey) == 0 {
		// SSH details should always be set during creation. If they're missing, it's an error.
		return nil, fmt.Errorf("SSH details missing for VM - VM may still be creating or creation failed")
	}

	sshPort := int(*result.SSHPort)
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

// selectExeletClient selects an exelet client. If a preferred exelet is configured and available,
// it uses that. Otherwise, falls back to hash-based selection using user ID.
func (s *Server) selectExeletClient(ctx context.Context, userID string) (*exeletClient, string, error) {
	if len(s.exeletAddrs) == 0 {
		return nil, "", fmt.Errorf("no exelet clients available")
	}

	// Check for existing VMs for this user.
	// If there are any, use the exelet that holds the most VMs.
	vms, err := withRxRes1(s, ctx, (*exedb.Queries).BoxesForUser, userID)
	if err != nil {
		return nil, "", err
	}
	m := make(map[string]int)
	var (
		maxHost string
		maxCnt  int
	)
	for i := range vms {
		host := vms[i].Ctrhost
		cnt := m[host]
		cnt++
		m[host] = cnt
		if cnt > maxCnt {
			maxHost, maxCnt = host, cnt
		} else if cnt == maxCnt && host < maxHost {
			// Make the choice predictable,
			// though it probably doesn't matter.
			maxHost = host
		}
	}
	if maxCnt > 0 {
		client := s.getExeletClient(maxHost)

		// Special case: don't pick exe-ctr-02 because it has
		// huge pages. TODO: Clean this up sometime.
		if strings.Contains(maxHost, "exe-ctr-02:") {
			s.slog().DebugContext(ctx, "not selecting exelet because it is exe-ctr-02", "user", userID, "host", maxHost, "userVMCount", maxCnt)
			client = nil
		}

		var count int
		if client != nil {
			// Don't pick this VM if it is too loaded.
			count, err = client.countInstances(ctx)
			if err != nil {
				return nil, "", err
			}
			if count >= autoThrottleVMLimit {
				s.slog().DebugContext(ctx, "not selecting exelet because it is over threshold", "user", userID, "host", maxHost, "userVMCount", maxCnt, "exeletVMCount", count)
				client = nil
			}
		}

		if client != nil {
			s.slog().DebugContext(ctx, "selecting exelet with most VMs for user", "user", userID, "host", maxHost, "userVMCount", maxCnt, "exeletVMCount", count)
			return client, maxHost, nil
		}
	}

	// Check for preferred exelet setting
	preferredAddr, err := withRxRes0(s, ctx, (*exedb.Queries).GetPreferredExelet)
	if err == nil && preferredAddr != "" {
		// Preferred exelet is configured, try to use it
		if client, ok := s.exeletClients[preferredAddr]; ok {
			return client, preferredAddr, nil
		}
		// Preferred exelet is not available, log error and fall back
		slog.ErrorContext(ctx, "preferred exelet not available, falling back to hash-based selection",
			"preferred_addr", preferredAddr,
			"available_addrs", s.exeletAddrs)
	}

	// Fall back to hash-based selection
	// TODO: consistent hashing? best-of-two random choices based on resource availability?
	hash := 0
	for _, c := range userID {
		hash = hash*31 + int(c)
	}
	if hash < 0 {
		hash = -hash
	}
	idx := hash % len(s.exeletAddrs)
	addr := s.exeletAddrs[idx]

	client, ok := s.exeletClients[addr]
	if !ok {
		return nil, "", fmt.Errorf("exelet client not found for address %s", addr)
	}

	return client, addr, nil
}

// autoThrottleVMLimit is the VM count threshold that triggers automatic throttling.
const autoThrottleVMLimit = 400

// autoThrottleVMCreation enables throttling if the preferred exelet has hit its VM limit.
func (s *Server) autoThrottleVMCreation(ctx context.Context) {
	preferredAddr, err := withRxRes0(s, ctx, (*exedb.Queries).GetPreferredExelet)
	if err != nil || preferredAddr == "" {
		slog.WarnContext(ctx, "autoThrottleVMCreation no preferred exelet configured", "error", err)
		return
	}
	ec, ok := s.exeletClients[preferredAddr]
	if !ok {
		s.slog().ErrorContext(ctx, "auto-throttle: preferred exelet client not found", "exelet", preferredAddr, "clients", s.exeletClients)
		return
	}

	count, err := ec.countInstances(ctx)
	if err != nil {
		s.slog().ErrorContext(ctx, "auto-throttle: failed to list instances", "exelet", preferredAddr, "error", err)
		return
	}

	if count < autoThrottleVMLimit {
		return
	}

	// There's a logical race here: multiple attempts could all write "true" here. No harm done, though.
	if err := withTx1(s, ctx, (*exedb.Queries).SetNewThrottleEnabled, "true"); err != nil {
		s.slog().ErrorContext(ctx, "auto-throttle: failed to enable throttle", "error", err)
		return
	}

	s.slog().ErrorContext(ctx, "auto-throttle: VM limit reached on preferred exelet, throttle enabled", "exelet", preferredAddr, "vm_count", count, "limit", autoThrottleVMLimit)
}
