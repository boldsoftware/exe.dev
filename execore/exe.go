// Package exe implements the bulk of the exed server.
package execore

import (
	"cmp"
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
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
	"math"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	txttmpl "text/template"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/tender"
	"exe.dev/boxname"
	"exe.dev/container"
	docspkg "exe.dev/docs"
	"exe.dev/domz"
	"exe.dev/email"
	"exe.dev/exedb"
	exeletclient "exe.dev/exelet/client"
	"exe.dev/exens"
	"exe.dev/exeweb"
	"exe.dev/ghuser"
	"exe.dev/hll"
	"exe.dev/llmgateway"
	"exe.dev/logging"
	computeapi "exe.dev/pkg/api/exe/compute/v1"
	resourceapi "exe.dev/pkg/api/exe/resource/v1"
	"exe.dev/pow"
	"exe.dev/publicips"
	"exe.dev/region"
	securitypkg "exe.dev/security"
	"exe.dev/sqlite"
	"exe.dev/sshkey"
	"exe.dev/sshpool2"
	"exe.dev/stage"
	"exe.dev/tagresolver"
	templatespkg "exe.dev/templates"
	"exe.dev/tracing"
	"exe.dev/wildcardcert"
	emailverifier "github.com/AfterShip/email-verifier"
	sloghttp "github.com/samber/slog-http"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "modernc.org/sqlite"
	"tailscale.com/util/limiter"
)

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
	RouteKnown      bool
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
	TeamBoxes    []TeamBoxDisplayInfo // Team VMs (for team owners)
	SiteSessions []SiteSession
	ActivePage   string
	IsLoggedIn   bool
	// BasicUser is true if the user has no SSH keys, no boxes, and was created for login-with-exe.
	// These users should only see the profile tab and a "what is exe?" section.
	BasicUser   bool
	InviteCount int64

	// Billing information
	HasBilling    bool   // User has active billing (completed checkout)
	BillingStatus string // Billing status: "active", "canceled", "pending", or "" if no account

	// Credits (staging only)
	CreditBalance tender.Value

	// Shelley free credits (from llmgateway credit state)
	ShelleyFreeCreditRemainingPct float64
	HasShelleyFreeCreditPct       bool
	MonthlyCreditsResetAt         string // e.g. "00:00 on 01 Mar"

	// Auto-open share modal (from access request email link)
	ShareVM    string
	ShareEmail string
}

// TeamBoxDisplayInfo represents a team member's box for the dashboard
type TeamBoxDisplayInfo struct {
	Name         string
	CreatorEmail string
	Status       string
	ProxyURL     string
	SSHCommand   string
}

// SiteSession represents an active session cookie for a site hosted by exe
type SiteSession struct {
	Domain     string
	URL        string // Full URL with https://
	LastUsedAt string // Formatted time string
}

// SSHKey represents an SSH key for the user page
type SSHKey struct {
	UserID      string
	PublicKey   string
	Comment     string
	Fingerprint string
	AddedAt     *time.Time
	LastUsedAt  *time.Time
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

	// InviteCode is the invite code used during signup (from ssh username).
	// If set, it will be applied after user creation.
	InviteCode *exedb.InviteCode
}

// Close signals completion to the waiting SSH session.
// Safe to call multiple times; only the first call closes the channel.
func (v *EmailVerification) Close() {
	v.closeOnce.Do(func() {
		close(v.CompleteChan)
	})
}

// ipqsIPResult holds the relevant fields from an IPQS IP lookup.
type ipqsIPResult struct {
	RecentAbuse bool   `json:"recent_abuse"`
	CountryCode string `json:"country_code"`
}

// ipAbuseCacheEntry stores a cached IPQS IP lookup result.
type ipAbuseCacheEntry struct {
	result   ipqsIPResult
	cachedAt time.Time
}

const (
	ipAbuseCacheMaxEntries = 32000
	ipAbuseCacheTTL        = 24 * time.Hour

	// ipAbuseAllowUSBypass allows US-based IPs to bypass the abuse check during US business hours.
	ipAbuseAllowUSBypass = true
)

var (
	tzEastern = mustLoadLocation("America/New_York")
	tzPacific = mustLoadLocation("America/Los_Angeles")
)

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// Server implements both HTTP and SSH server functionality for exe.dev
type Server struct {
	env              stage.Env // prod, staging, local, test, etc.
	httpLn           *listener
	proxyLns         []*listener // Additional listeners for proxy ports
	httpsLn          *listener
	sshLn            *listener
	pluginLn         *listener
	exeproxServiceLn *listener
	piperdPort       int // what port sshpiperd is listening on, typically 2222
	// PublicIPs maps private (local address) IPs to public IP / domain / shard.
	PublicIPs map[netip.Addr]publicips.PublicIP
	// LobbyIP is the public IP for the lobby/REPL (ssh exe.dev), not associated with any shard.
	// Used for the apex domain (exe.xyz) DNS resolution.
	LobbyIP netip.Addr

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

	exeproxServiceServer  *grpc.Server
	exeproxServiceMetrics *grpcprom.ServerMetrics

	certManager         *autocert.Manager
	wildcardCertManager *wildcardcert.Manager

	// dnsServer is the embedded DNS nameserver for BoxHost (prod/staging only)
	dnsServer *exens.Server
	// lmtpServer handles inbound email delivery
	lmtpServer *LMTPServer

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

	// SSH connection pooling for HTTP proxying
	sshPool        *sshpool2.Pool
	transportCache *exeweb.TransportCache

	// In-memory state for active sessions (these don't need persistence)
	emailVerificationsMu sync.RWMutex
	emailVerifications   map[string]*EmailVerification // token -> email verification
	magicSecrets         *exeweb.MagicSecrets          // secret -> magic secret with expiration
	creationStreamsMu    sync.Mutex
	creationStreams      map[creationStreamKey]*CreationStream // (userID, hostname) -> creation stream

	// GitHub keys -> GitHub user info client
	// For expedited onboarding for existing GitHub users who show up with their GitHub SSH key
	githubUser *ghuser.Client

	// Email service
	emailSenders           *email.Senders
	fakeHTTPEmail          string // fake HTTP email server URL for sending emails (for e2e tests)
	postmarkStatsCollector *email.PostmarkStatsCollector
	bouncePoller           *email.PostmarkBouncePoller
	subscriptionPoller     *SubscriptionPoller

	// IPQS email quality service
	ipqsAPIKey string

	// IPQS IP abuse cache (random replacement, max 32k entries, 24h TTL)
	// TODO: put into db perhaps?
	ipAbuseCacheMu sync.Mutex
	ipAbuseCache   map[string]ipAbuseCacheEntry

	// Metrics
	metricsRegistry *prometheus.Registry
	sshMetrics      *SSHMetrics
	httpMetrics     *exeweb.HTTPMetrics
	signupMetrics   *SignupMetrics
	hllTracker      *hll.Tracker
	hllCollector    *hll.Collector

	docs     *docspkg.Handler
	security *securitypkg.Handler

	// HTML templates (parsed at startup)
	templates *template.Template

	startOnce   sync.Once
	startErr    error              // result of first start attempt
	startCancel context.CancelFunc // cancel function for start's context
	serveWg     sync.WaitGroup
	stopOnce    sync.Once
	stopChan    chan struct{} // closed by Stop to unblock start's select
	stopping    atomic.Bool

	// General purpose slogger
	log *slog.Logger
	// net/http server error logger
	netHTTPLogger *log.Logger

	// Slack feed for posting events and tracking new user signups
	slackFeed *logging.SlackFeed

	// Billing manager for subscription management
	billing *billing.Manager

	// Rate limiter for signup requests (5 per minute per IP)
	signupLimiter *limiter.Limiter[netip.Addr]

	// Rate limiter for /exec API requests, keyed by SSH key fingerprint
	execLimiter *limiter.Limiter[string]

	// Proof-of-work challenger for signup (when enabled)
	// The idea is that instead of an external captcha, we ask
	// the client to do some math. This will be annoying to implement
	// for a script-kiddie (though Claude Code would manage just fine),
	// and will cost CPU cycles, slowing down the user. This is currently
	// default off, and, also, NOT e1e TESTED! Before enabling, you
	// should test that it works in staging.
	//
	// Alas this is a half-measure because SSH sends you right to e-mail.
	// When SSH becomes a problem, we need SSH to send you a URL, which
	// THEN asks for your e-mail, and goes through the rest of this flow.
	signupPOW        *pow.Challenger
	signupPOWEnabled bool

	// Discord link secret for HMAC verification of Discord account linking
	discordLinkSecret string
}

// newSignupPOW creates a proof-of-work challenger with a random secret.
// Difficulty 16 means ~65k hashes on average (a few hundred ms on typical devices).
func newSignupPOW() *pow.Challenger {
	secret := make([]byte, 32)
	if _, err := crand.Read(secret); err != nil {
		panic("failed to generate random secret: " + err.Error())
	}
	return pow.NewChallenger(secret, 16, 2*time.Minute)
}

// exeletClient is information about an exelet, including its client.
type exeletClient struct {
	up     atomic.Bool
	addr   string
	region region.Region // region this exelet is in
	client *exeletclient.Client
	// usage and count are updated every 10 minutes,
	// so they can be old.
	usage atomic.Pointer[resourceapi.MachineUsage]
	count atomic.Int32 // instance count
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

// updateUsage updates the current exelet machine usage.
// This does not return an error; it just logs it
// and doesn't update the usage.
func (ec *exeletClient) updateUsage(ctx context.Context) {
	usage, err := ec.client.GetMachineUsage(ctx, &resourceapi.GetMachineUsageRequest{})
	if err != nil {
		slog.ErrorContext(ctx, "failed to update exelet machine usage", "addr", ec.addr, "error", err)
		ec.up.Store(false)
	} else {
		ec.up.Store(usage.Available)
		ec.usage.Store(usage.Usage)
	}

	count, err := ec.countInstances(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to count VM instances", "addr", ec.addr, "error", err)
	} else {
		ec.count.Store(int32(count))
	}
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

// isMainListenerPort reports whether port is the server's main HTTP or HTTPS port.
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

// bootCleanups are best-effort cleanup functions run at server startup.
// Running only on boot is sufficient: we deploy frequently and stale rows
// are low-cost, so periodic cleanup is not worth the complexity.
var bootCleanups = []struct {
	name string
	fn   func(ctx context.Context, slog *slog.Logger, db *sqlite.DB) error
}{
	{
		name: "old checkout params",
		fn: func(ctx context.Context, _ *slog.Logger, db *sqlite.DB) error {
			cutoff := sqlite.NormalizeTime(time.Now().Add(-48 * time.Hour))
			return exedb.WithTx1(db, ctx, (*exedb.Queries).DeleteOldCheckoutParams, cutoff)
		},
	},
}

func cleanupOnBoot(slog *slog.Logger, db *sqlite.DB) {
	ctx := context.Background()
	for _, c := range bootCleanups {
		if err := c.fn(ctx, slog, db); err != nil {
			slog.ErrorContext(ctx, "boot cleanup failed", "cleanup", c.name, "error", err)
		}
	}
}

// ServerConfig contains all configuration for creating a new Server.
//
//exe:completeinit
type ServerConfig struct {
	Logger             *slog.Logger
	HTTPAddr           string
	HTTPSAddr          string
	SSHAddr            string
	PluginAddr         string
	ExeproxServicePort int // port on tailscale IP address
	DBPath             string
	FakeEmailServer    string
	PiperdPort         int
	GHWhoAmIPath       string
	ExeletAddresses    []string
	Env                stage.Env
	Billing            *billing.Manager // optional billing manager override
	MetricsRegistry    *prometheus.Registry
	LMTPSocketPath     string // path to LMTP Unix socket; empty disables LMTP
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

	// Clean up stale data on boot.
	cleanupOnBoot(slog, db)

	// Initialize email senders
	emailSenders := email.NewSendersFromEnv(cfg.Env.MailgunDomain())
	if emailSenders.Any() == nil {
		slog.Info("no email provider configured, email verification will not work")
	}

	// Initialize Postmark stats collector and bounce poller
	var postmarkStatsCollector *email.PostmarkStatsCollector
	var bouncePoller *email.PostmarkBouncePoller
	if postmarkAPIKey := os.Getenv("POSTMARK_API_KEY"); postmarkAPIKey != "" {
		postmarkStatsCollector = email.NewPostmarkStatsCollector(postmarkAPIKey, slog)
		postmarkStatsCollector.Start()

		// Initialize bounce poller to sync bounces from Postmark to our database.
		// Polls every 10 minutes.
		bouncePoller = email.NewPostmarkBouncePoller(postmarkAPIKey, newBounceStore(db), slog, 10*time.Minute)
		bouncePoller.Start()
	}

	// Initialize IPQS API key for email quality checks
	ipqsAPIKey := os.Getenv("IPQS_API_KEY")
	if ipqsAPIKey == "" {
		slog.Info("IPQS_API_KEY not set, email quality checks disabled")
	}

	// Initialize Discord link secret for account linking
	discordLinkSecret := os.Getenv("EXE_LINK_SECRET")

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
		if cfg.Env.BehindTLSProxy {
			baseURL = fmt.Sprintf("https://%s:%d", cfg.Env.WebHost, httpLn.tcp.Port)
		} else {
			baseURL = fmt.Sprintf("http://%s:%d", cfg.Env.WebHost, httpLn.tcp.Port)
		}
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

	sshAddr := cfg.SSHAddr
	if cfg.Env.ListenOnTailscaleOnly {
		_, port, _ := net.SplitHostPort(cfg.SSHAddr)
		if port == "" {
			port = cfg.SSHAddr // handle bare port
		}
		sshAddr, err = cfg.Env.TailscaleListenAddr(port)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to determine SSH tailscale address: %v", err)
		}
	}
	sshLn, err := startListener(slog, "ssh", sshAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on SSH address %q: %w", sshAddr, err)
	}

	pluginAddr := cfg.PluginAddr
	if cfg.Env.ListenOnTailscaleOnly {
		_, port, _ := net.SplitHostPort(cfg.PluginAddr)
		if port == "" {
			port = cfg.PluginAddr
		}
		pluginAddr, err = cfg.Env.TailscaleListenAddr(port)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to determine piper plugin tailscale address: %v", err)
		}
	}
	pluginLn, err := startListener(slog, "plugin", pluginAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on piper plugin address %q: %w", pluginAddr, err)
	}

	exeproxAddr, err := cfg.Env.TailscaleListenAddr(strconv.Itoa(cfg.ExeproxServicePort))
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to determine exeprox service address: %v", err)
	}
	exeproxServiceLn, err := startListener(slog, "exeprox-service", exeproxAddr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to listen on exeprox service address %q: %w", exeproxAddr, err)
	}

	// Initialize metrics
	sshMetrics := NewSSHMetrics(cfg.MetricsRegistry)
	httpMetrics := exeweb.NewHTTPMetrics(cfg.MetricsRegistry)
	signupMetrics := NewSignupMetrics(cfg.MetricsRegistry)
	sqlite.RegisterSQLiteMetrics(cfg.MetricsRegistry)
	llmgateway.RegisterMetrics(cfg.MetricsRegistry)
	RegisterEntityMetrics(cfg.MetricsRegistry, db, slog)
	email.RegisterMetrics(cfg.MetricsRegistry, postmarkStatsCollector)
	exeproxServiceMetrics := registerExeproxMetrics(cfg.MetricsRegistry)

	// Initialize HLL unique user tracking
	hllStorage := newHLLStorage(db)
	hllTracker := hll.NewTracker(hllStorage)
	hllEvents := []string{"proxy", "shelley-proxy", "vm-login", "web-visit", "login-with-exe"}
	hllCollector := hll.NewCollector(hllTracker, hllEvents)
	if err := hllCollector.Register(cfg.MetricsRegistry); err != nil {
		hllTracker.Close()
		db.Close()
		return nil, fmt.Errorf("failed to register HLL metrics: %w", err)
	}

	// Initialize tag resolver for image tag resolution
	tagResolverInstance := tagresolver.New(db)

	// Initialize exelet clients
	exeletClients := make(map[string]*exeletClient)
	exeletClientMetrics := exeletclient.NewClientMetrics(cfg.MetricsRegistry)
	for _, addr := range cfg.ExeletAddresses {
		if addr == "" {
			continue
		}
		if _, ok := exeletClients[addr]; ok {
			slog.Error("exelet address specified more than once in exed config, skipping", "addr", addr)
			continue
		}

		// Parse region from exelet address
		exeletRegion, err := region.ParseExeletRegion(addr)
		if err != nil {
			slog.Error("failed to parse region from exelet address, skipping host", "addr", addr, "error", err)
			continue
		}
		if exeletRegion.VMHardLimit <= 0 || exeletRegion.VMSoftLimit <= 0 || exeletRegion.VMSoftLimit >= exeletRegion.VMHardLimit {
			slog.Error("region has invalid VM limits configured, skipping host", "addr", addr, "region", exeletRegion.Code, "hard", exeletRegion.VMHardLimit, "soft", exeletRegion.VMSoftLimit)
			continue
		}

		client, err := exeletclient.NewClient(addr,
			exeletclient.WithInsecure(),
			exeletclient.WithLogger(slog),
			exeletclient.WithClientMetrics(exeletClientMetrics))
		if err != nil {
			slog.Error("failed to create exelet client, skipping host", "addr", addr, "error", err)
			continue
		}

		ec := &exeletClient{
			addr:   addr,
			region: exeletRegion,
			client: client,
		}
		ec.up.Store(true)
		exeletClients[addr] = ec

		// Try to fetch system info to cache architecture and version.
		// Log but don't fail startup if this fails - the host may be temporarily unavailable.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err = client.GetSystemInfo(ctx, &computeapi.GetSystemInfoRequest{})
		cancel()
		if err != nil {
			slog.Error("failed to get system info from exelet, will retry later", "addr", addr, "error", err)
			ec.up.Store(false)
		} else {
			slog.Info("initialized exelet client", "addr", addr, "region", exeletRegion.Code, "arch", client.Arch(), "version", client.Version())
		}
	}
	slog.Info("exelet clients initialized", "count", len(exeletClients))

	docsStore, err := docspkg.Load(cfg.Env)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("loading docs: %w", err)
	}
	docsHandler := docspkg.NewHandler(docsStore, cfg.Env.ShowHiddenDocs)

	securityStore, err := securitypkg.Load(true)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("loading security bulletins: %w", err)
	}
	securityHandler := securitypkg.NewHandler(securityStore, false)

	// Parse all HTML templates at startup
	tmpl, err := templatespkg.Parse()
	if err != nil {
		db.Close()
		return nil, err
	}

	s := &Server{
		env:                    cfg.Env,
		httpLn:                 httpLn,
		httpsLn:                httpsLn,
		sshLn:                  sshLn,
		pluginLn:               pluginLn,
		exeproxServiceLn:       exeproxServiceLn,
		piperdPort:             cfg.PiperdPort,
		db:                     db,
		tagResolver:            tagResolverInstance,
		exeletClients:          exeletClients,
		sshPool:                &sshpool2.Pool{TTL: 10 * time.Minute, Metrics: sshpool2.NewMetrics(cfg.MetricsRegistry)},
		transportCache:         exeweb.NewTransportCache(5 * time.Minute),
		emailVerifications:     make(map[string]*EmailVerification),
		magicSecrets:           exeweb.NewMagicSecrets(),
		creationStreams:        make(map[creationStreamKey]*CreationStream),
		githubUser:             ghu,
		emailSenders:           emailSenders,
		fakeHTTPEmail:          cfg.FakeEmailServer,
		postmarkStatsCollector: postmarkStatsCollector,
		bouncePoller:           bouncePoller,
		ipqsAPIKey:             ipqsAPIKey,
		discordLinkSecret:      discordLinkSecret,
		PublicIPs:              map[netip.Addr]publicips.PublicIP{},

		metricsRegistry:       cfg.MetricsRegistry,
		sshMetrics:            sshMetrics,
		httpMetrics:           httpMetrics,
		signupMetrics:         signupMetrics,
		exeproxServiceMetrics: exeproxServiceMetrics,
		hllTracker:            hllTracker,
		hllCollector:          hllCollector,

		docs:      docsHandler,
		security:  securityHandler,
		templates: tmpl,
		stopChan:  make(chan struct{}),
		log:       slog,
		slackFeed: logging.NewSlackFeed(slog, cfg.Env),
		billing:   cfg.Billing,
		signupLimiter: &limiter.Limiter[netip.Addr]{
			Size:           10000,           // Track up to 10k IPs
			Max:            20,              // 20 requests max
			RefillInterval: time.Minute / 5, // Refill 1 token per 12 seconds
		},
		execLimiter: &limiter.Limiter[string]{
			Size:           10000,       // Track up to 10k SSH keys
			Max:            60,          // 60 requests burst
			RefillInterval: time.Second, // 1 request per second sustained
			Overdraft:      120,         // Cooldown if abused
		},
		signupPOW: newSignupPOW(),
	}

	docsHandler.SetTopbarFunc(func(r *http.Request) docspkg.TopbarData {
		td := docspkg.TopbarData{WebHost: cfg.Env.WebHost}
		if _, err := s.validateAuthCookie(r); err == nil {
			td.IsLoggedIn = true
		}
		return td
	})

	if s.billing == nil {
		s.billing = &billing.Manager{}
	}

	// Wire the billing manager's DB and Logger for credit operations.
	s.billing.DB = s.db
	s.billing.Logger = slog

	if !cfg.Env.SkipBilling {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.billing.InstallPrices(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("install billing prices: %w", err)
		}
	}

	// Initialize the limiters' internal caches by calling Allow once.
	// This avoids an unimportant but distracting /debug panic after each deployment.
	s.signupLimiter.Allow(netip.Addr{})
	s.execLimiter.Allow("")

	// Set up HTTP metrics host functions for in-flight label tracking
	s.httpMetrics.SetHostFuncs(s.isProxyRequest, func(host string) string {
		hostname, _, _ := net.SplitHostPort(host)
		if hostname == "" {
			hostname = host
		}
		return domz.Label(hostname, s.env.BoxHost)
	})

	// Initialize embedded DNS server for BoxHost (exe.xyz) in prod/staging
	if cfg.Env.DiscoverPublicIPs {
		s.dnsServer = exens.NewServer(s.db, s.log, cfg.Env.BoxHost, cfg.Env.WebHost)
	}

	// Initialize LMTP server for inbound email delivery
	if cfg.LMTPSocketPath != "" {
		s.lmtpServer = NewLMTPServer(s, cfg.LMTPSocketPath)
	}

	s.setupHTTPServer()
	s.setupHTTPSServer()
	s.setupProxyServers()
	s.setupSSHServer()
	s.setupExeproxServer()

	// Initialize billing subscription poller to sync subscription events from Stripe.
	if s.billing != nil && !cfg.Env.SkipBilling {
		s.subscriptionPoller = StartSubscriptionPoller(s.billing, slog)
	}

	s.ready.Add(1) // matched with final done at bottom of Start
	go func() {
		s.ready.Wait()
		// The following log line signals to e2e tests that they may proceed with using the server (better than sleeps!)
		s.slog().Info("server started", "url", baseURL)
	}()

	return s, nil
}

// initShardIPs sets up the IP resolver for mapping local IPs to public IP info.
// DiscoverPublicIPs=true: use EC2 metadata + regional IP shard tables.
// DiscoverPublicIPs=false: use 127.21.0.x where x is the shard number.
func (s *Server) initShardIPs(ctx context.Context) {
	defer s.logIPResolver()

	if len(s.PublicIPs) != 0 {
		// Already initialized (e.g., in tests)
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if !s.env.DiscoverPublicIPs {
		s.slog().InfoContext(ctx, "using dev IP resolver", "box_host", s.env.BoxHost)
		ips, err := publicips.LocalhostIPs(ctx, s.env.BoxHost, s.env.NumShards)
		if err != nil {
			s.slog().ErrorContext(ctx, "localhost IP setup failed", "error", err)
			return
		}
		s.PublicIPs = ips
		// For local dev, use 127.21.0.0 as the lobby IP
		s.LobbyIP = netip.AddrFrom4([4]byte{127, 21, 0, 0})
		return
	}

	// Production: combine EC2 metadata with regional IP shard tables.
	// EC2 metadata gives us private->public IP mappings for AWS.
	// aws_ip_shards + latitude_ip_shards give us public_ip->shard mappings.
	ips, err := s.loadPublicIPsFromDB(ctx)
	if err != nil {
		s.slog().ErrorContext(ctx, "public IP discovery failed", "error", err)
		return
	}
	s.PublicIPs = ips
}

// loadPublicIPsFromDB loads PublicIPs by combining EC2 metadata with regional IP shard tables.
// This avoids DNS lookups which would create a circular dependency with the DNS server.
//
// AWS IPs: Combines EC2 metadata (private->public) with aws_ip_shards (public->shard)
// to get private->shard mappings. AWS NATs public IPs to private IPs on the ENI.
//
// Latitude IPs: Adds latitude_ip_shards entries directly (public->shard) since
// Latitude doesn't NAT, so we see the public IP directly.
func (s *Server) loadPublicIPsFromDB(ctx context.Context) (map[netip.Addr]publicips.PublicIP, error) {
	// Get private->public IP mappings from EC2 metadata (no DNS lookups)
	ec2Mappings, err := publicips.EC2IPMappings(ctx)
	if err != nil {
		return nil, fmt.Errorf("EC2 metadata lookup failed: %w", err)
	}
	if len(ec2Mappings) == 0 {
		return nil, fmt.Errorf("no EC2 IP mappings found (not running on EC2?)")
	}

	// Get AWS shard->public_ip mappings from the database
	awsShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListAWSIPShards)
	if err != nil {
		return nil, fmt.Errorf("failed to list aws_ip_shards: %w", err)
	}
	if len(awsShards) == 0 {
		return nil, fmt.Errorf("aws_ip_shards table is empty; populate it before starting")
	}

	// Build AWS public_ip -> (shard, domain) lookup
	awsPublicToShard := make(map[netip.Addr]publicips.PublicIP, len(awsShards))
	for _, row := range awsShards {
		ip, err := netip.ParseAddr(row.PublicIP)
		if err != nil {
			s.slog().WarnContext(ctx, "invalid public IP in aws_ip_shards", "shard", row.Shard, "ip", row.PublicIP, "error", err)
			continue
		}
		awsPublicToShard[ip] = publicips.PublicIP{
			IP:     ip,
			Domain: publicips.ShardSub(int(row.Shard)) + "." + s.env.BoxHost,
			Shard:  int(row.Shard),
		}
	}

	// Combine AWS: for each EC2 mapping, look up the shard info by "public" IP...
	// but the public IP from our perspective is the private IP from EC2 metadata.
	result := make(map[netip.Addr]publicips.PublicIP, len(ec2Mappings)+s.env.NumShards)
	for _, mapping := range ec2Mappings {
		info, ok := awsPublicToShard[mapping.Public]
		if !ok {
			// This is the lobby IP - the public IP for ssh exe.dev / exe.xyz apex domain,
			// not associated with any box shard.
			s.LobbyIP = mapping.Public
			s.slog().InfoContext(ctx, "discovered lobby IP (no shard)", "public_ip", mapping.Public)
			continue
		}
		result[mapping.Private] = info
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no EC2 IPs matched aws_ip_shards table")
	}

	// Get Latitude shard->public_ip mappings from the database.
	// Latitude doesn't NAT, so sshpiper sees the public IP directly.
	// Add these directly to the result keyed by the public IP.
	latitudeShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListLatitudeIPShards)
	if err != nil {
		return nil, fmt.Errorf("failed to list latitude_ip_shards: %w", err)
	}
	for _, row := range latitudeShards {
		ip, err := netip.ParseAddr(row.PublicIP)
		if err != nil {
			s.slog().WarnContext(ctx, "invalid public IP in latitude_ip_shards", "shard", row.Shard, "ip", row.PublicIP, "error", err)
			continue
		}
		result[ip] = publicips.PublicIP{
			IP:     ip,
			Domain: publicips.ShardSub(int(row.Shard)) + "." + s.env.BoxHost,
			Shard:  int(row.Shard),
		}
	}

	return result, nil
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

// validateIPShards validates IP shard configuration:
//  1. All AWS shards should have corresponding local (private) IPs on this machine.
//  2. All serving shards (ip_shards) should match either AWS or Latitude IPs.
func (s *Server) validateIPShards(ctx context.Context) {
	awsShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListAWSIPShards)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to list aws_ip_shards for validation", "error", err)
		return
	}

	// For each aws_ip_shard, verify there's a PublicIPs entry with matching shard and IP.
	for _, awsShard := range awsShards {
		found := false
		for _, info := range s.PublicIPs {
			if info.Shard == int(awsShard.Shard) && info.IP.String() == awsShard.PublicIP {
				found = true
				break
			}
		}
		if !found {
			s.slog().ErrorContext(ctx, "aws_ip_shard not routable on this machine",
				"shard", awsShard.Shard,
				"public_ip", awsShard.PublicIP)
		}
	}

	// All serving shards should match either AWS or Latitude IP for that shard.
	servingShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListIPShards)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to list ip_shards for validation", "error", err)
		return
	}
	latitudeShards, err := withRxRes0(s, ctx, (*exedb.Queries).ListLatitudeIPShards)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to list latitude_ip_shards for validation", "error", err)
		return
	}

	awsByS := make(map[int64]string, len(awsShards))
	for _, row := range awsShards {
		awsByS[row.Shard] = row.PublicIP
	}
	latByS := make(map[int64]string, len(latitudeShards))
	for _, row := range latitudeShards {
		latByS[row.Shard] = row.PublicIP
	}

	for _, serving := range servingShards {
		shard := serving.Shard
		ip := serving.PublicIP
		if ip != awsByS[shard] && ip != latByS[shard] {
			s.slog().ErrorContext(ctx, "ip_shard serving IP doesn't match AWS or Latitude",
				"shard", shard,
				"serving_ip", ip,
				"aws_ip", awsByS[shard],
				"latitude_ip", latByS[shard])
		}
	}
}

// withRx executes a function with a read-only database transaction and exedb queries
func (s *Server) withRx(ctx context.Context, fn func(context.Context, *exedb.Queries) error) error {
	return exedb.WithRx(s.db, ctx, fn)
}

// withTx executes a function with a read-write database transaction and exedb queries
func (s *Server) withTx(ctx context.Context, fn func(context.Context, *exedb.Queries) error) error {
	return exedb.WithTx(s.db, ctx, fn)
}

// withRxRes0 executes a sqlc query with a read-only database transaction and no arguments, returning a value.
func withRxRes0[T any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context) (T, error)) (T, error) {
	return exedb.WithRxRes0(s.db, ctx, fn)
}

// withRxRes1 executes a sqlc query with a read-only database transaction and one argument, returning a value.
func withRxRes1[T, A any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context, A) (T, error), a A) (T, error) {
	return exedb.WithRxRes1(s.db, ctx, fn, a)
}

// withTx0 executes a sqlc query with a read-write database transaction and no arguments.
func withTx0(s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context) error) error {
	return exedb.WithTx0(s.db, ctx, fn)
}

// withTx1 executes a sqlc query with a read-write database transaction and one argument.
func withTx1[A any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context, A) error, a A) error {
	return exedb.WithTx1(s.db, ctx, fn, a)
}

// withTxRes0 executes a function with a read-write database transaction, returning a value.
func withTxRes0[T any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context) (T, error)) (T, error) {
	return exedb.WithTxRes0(s.db, ctx, fn)
}

// withTxRes1 executes a function with a read-write database transaction and one argument, returning a value.
func withTxRes1[T, A any](s *Server, ctx context.Context, fn func(*exedb.Queries, context.Context, A) (T, error), a A) (T, error) {
	return exedb.WithTxRes1(s.db, ctx, fn, a)
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

// bogusEmailDomains contains domains that should never receive emails.
// Includes RFC 2606 reserved domains and common typos of popular email providers.
// Note: domains without TLDs (localhost, invalid) are rejected by ParseAddress.
var bogusEmailDomains = []string{
	"example.com", "example.net", "example.org", "test.com", // RFC 2606 reserved domains
	"gmail.co", "gmial.com", "gmai.com", "gamil.com", "gnail.com", "gmail.con", "gmail.om",
	"hotmail.co", "hotmal.com", "hotmial.com",
	"outlok.com", "outloo.com", "outlook.co",
	"yahooo.com", "yaho.com", "yahoo.co",
	"icloud.co", "icoud.com",
}

// isBogusEmailDomain reports whether email is known to be undeliverable.
func isBogusEmailDomain(email string) bool {
	syntax := emailVerifier.ParseAddress(email)
	if !syntax.Valid {
		return false
	}
	return slices.Contains(bogusEmailDomains, syntax.Domain)
}

// sendEmail sends an email using the configured email service.
// emailType identifies the type of email being sent for logging and metrics.
func (s *Server) sendEmail(ctx context.Context, emailType email.Type, to, subject, body string) error {
	// Do not attempt to send to bogus domains (reserved or common typos).
	if isBogusEmailDomain(to) {
		s.slog().InfoContext(ctx, "silently dropping email to bogus domain", "to", to, "subject", subject, "type", emailType)
		return nil
	}

	// Do not attempt to send to an email address that has hard-bounced before. Best effort.
	// If bounced, silently pretend the email was sent (anti-fraud measure).
	bounced, _ := withRxRes1(s, ctx, (*exedb.Queries).IsEmailBounced, to)
	if bounced == 1 {
		s.slog().InfoContext(ctx, "silently dropping email to bounced address", "to", to, "subject", subject, "type", emailType)
		return nil
	}

	if s.fakeHTTPEmail != "" {
		err := s.sendFakeEmail(ctx, to, subject, body)
		if err != nil {
			s.slog().WarnContext(ctx, "failed to send fake email", "to", to, "subject", subject, "error", err)
		}
		return nil
	}

	// In dev mode, always just log the email
	if s.env.FakeEmail {
		s.slog().InfoContext(ctx, "DEV MODE: Would send email", "to", to, "subject", subject, "type", emailType, "body", body)
		return nil
	}

	// Check if email service is configured
	sender := s.emailSenders.Any()
	if sender == nil {
		return errNoEmailService
	}

	from := fmt.Sprintf("%s <support@%s>", s.env.WebHost, s.env.WebHost)
	err := sender.Send(ctx, emailType, from, to, subject, body)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to send email", "to", to, "subject", subject, "type", emailType, "error", err)
		// Record bounce/inactive recipient errors
		// This error is from Postmark that reads (in part):
		//   > 406 You tried to send to recipient(s) that have been marked as inactive.
		// TODO: add other substrings to check
		if strings.Contains(err.Error(), "marked as inactive") {
			// Best effort
			err := withTx1(s, ctx, (*exedb.Queries).InsertEmailBounce, exedb.InsertEmailBounceParams{
				Email:  to,
				Reason: err.Error(),
			})
			if err != nil {
				s.slog().ErrorContext(ctx, "failed to record email bounce", "email", to, "error", err)
			}
		}
	}
	return err
}

// sendFakeEmail sends an email to the fake HTTP email server
func (s *Server) sendFakeEmail(ctx context.Context, to, subject, body string) error {
	emailData := map[string]string{
		"to":      to,
		"subject": subject,
		"body":    body,
	}

	jsonData, err := json.Marshal(emailData)
	if err != nil {
		return fmt.Errorf("failed to marshal email data: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.fakeHTTPEmail, strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("failed to create fake email request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send fake email via HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("fake email server returned error: %s", resp.Status)
	}

	s.slog().InfoContext(ctx, "fake email sent successfully via HTTP", "to", to, "subject", subject)
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
func (s *Server) sendBoxCreatedEmail(ctx context.Context, to string, details newBoxDetails) {
	subject := fmt.Sprintf("exe.dev: created %s.exe.xyz", details.VMName)

	body := new(strings.Builder)
	if err := boxCreatedEmailTemplate.Execute(body, details); err != nil {
		s.slog().WarnContext(ctx, "failed to render box created email", "error", err)
		return
	}

	if err := s.sendEmail(ctx, email.TypeBoxCreated, to, subject, body.String()); err != nil {
		s.slog().WarnContext(ctx, "failed to send box created email", "to", to, "box", details.VMName, "error", err)
	}
}

// sendBoxMaintenanceEmail sends a notification email when a box is rebooted for system maintenance.
func (s *Server) sendBoxMaintenanceEmail(ctx context.Context, boxName string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	boxInfo, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxWithOwnerEmail, boxName)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to look up box owner for maintenance email", "box", boxName, "error", err)
		return
	}

	subject := fmt.Sprintf("exe.dev: system maintenance on %s", boxName)
	body := fmt.Sprintf("Your VM %s was rebooted as part of routine system maintenance. No action is required.\n\nIf you run into any issues please contact support@exe.dev.\n\nThanks!\n\nexe.dev support", boxName)

	if err := s.sendEmail(ctx, email.TypeBoxMaintenance, boxInfo.OwnerEmail, subject, body); err != nil {
		s.slog().WarnContext(ctx, "failed to send box maintenance email", "to", boxInfo.OwnerEmail, "box", boxName, "error", err)
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
	if originalUserKey, localAddress, clientAddr, isProxy := s.lookupEphemeralProxyKey(ctx, key); isProxy {
		s.slog().DebugContext(ctx, "Ephemeral proxy authentication detected", "user", user, "local_address", localAddress, "client_addr", clientAddr)
		return s.authenticateProxyUserWithLocalAddress(ctx, user, originalUserKey, localAddress, clientAddr)
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
		_, err := s.GetUserIDByEmail(ctx, email)
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
func (s *Server) checkEmailVerificationToken(ctx context.Context, token string) (exedb.EmailVerification, error) {
	row, err := withRxRes1(s, ctx, (*exedb.Queries).GetEmailVerificationByToken, token)
	if errors.Is(err, sql.ErrNoRows) {
		return exedb.EmailVerification{}, fmt.Errorf("invalid verification token")
	}
	if err != nil {
		return exedb.EmailVerification{}, fmt.Errorf("database error: %w", err)
	}

	// Check if token has expired
	if time.Now().After(row.ExpiresAt) {
		// Clean up expired token
		withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeleteEmailVerificationByToken, token)
		return exedb.EmailVerification{}, fmt.Errorf("verification token expired")
	}

	return row, nil
}

// validateEmailVerificationToken validates an email verification token, consumes it, and returns the verification record.
// If the verification has an associated invite code, it applies the invite code to the user.
func (s *Server) validateEmailVerificationToken(ctx context.Context, token string) (exedb.EmailVerification, error) {
	row, err := s.checkEmailVerificationToken(ctx, token)
	if err != nil {
		return exedb.EmailVerification{}, err
	}

	// Apply invite code if one was associated with this verification
	if row.InviteCodeID != nil {
		invite, err := withRxRes1(s, ctx, (*exedb.Queries).GetInviteCodeByID, *row.InviteCodeID)
		if err != nil {
			s.slog().ErrorContext(ctx, "failed to get invite code during verification", "error", err, "invite_code_id", *row.InviteCodeID)
		} else if invite.UsedByUserID == nil {
			// Only apply if not already used
			if err := s.applyInviteCode(ctx, &invite, row.UserID); err != nil {
				s.slog().ErrorContext(ctx, "failed to apply invite code during web verification", "error", err, "code", invite.Code)
			} else {
				s.slog().InfoContext(ctx, "invite code applied successfully via web", "code", invite.Code, "user_id", row.UserID, "plan_type", invite.PlanType)
			}
		}
	}

	// Clean up used token - use context.WithoutCancel to ensure cleanup completes even if client disconnects
	withTx1(s, context.WithoutCancel(ctx), (*exedb.Queries).DeleteEmailVerificationByToken, token)

	return row, nil
}

// validateAuthToken validates an authentication token and returns the user ID
func (s *Server) validateAuthToken(ctx context.Context, token, expectedSubdomain string) (string, error) {
	authToken, err := withRxRes1(s, ctx, (*exedb.Queries).GetAuthTokenInfo, token)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("invalid token")
	}
	if err != nil {
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
	return &box
}

// UserHasExeSudo reports whether if the user has exe root_support privilege.
func (s *Server) UserHasExeSudo(ctx context.Context, userID string) bool {
	isRootSupport, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserRootSupport, userID)
	return err == nil && isRootSupport == 1
}

// FindBoxForExeSudoer finds a box by name if the user is a root support user and the box has support access enabled.
// Returns nil if the user is not a root support user or the box doesn't have support access enabled.
func (s *Server) FindBoxForExeSudoer(ctx context.Context, userID, boxName string) *exedb.Box {
	if userID == "" || !boxname.IsValid(boxName) || !s.UserHasExeSudo(ctx, userID) {
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
	userID        string
	ctrhost       string
	name          string
	image         string
	noShard       bool
	region        string // region code (e.g., "pdx", "lax")
	allocatedCPUs uint64 // number of CPUs allocated to the VM
}

func (s *Server) preCreateBox(ctx context.Context, opts preCreateBoxOptions) (int, error) {
	// Validate box name
	if err := boxname.Valid(opts.name); err != nil {
		return 0, err
	}
	// Validate region code
	if _, err := region.ByCode(opts.region); err != nil {
		return 0, fmt.Errorf("invalid region: %w", err)
	}

	routes := exedb.DefaultRouteJSON()
	var boxID int
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var allocCPUs *int64
		if opts.allocatedCPUs > 0 {
			v := int64(opts.allocatedCPUs)
			allocCPUs = &v
		}
		id, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         opts.ctrhost,
			Name:            opts.name,
			Status:          "creating",
			Image:           opts.image,
			CreatedByUserID: opts.userID,
			Routes:          &routes,
			Region:          opts.region,
			AllocatedCpus:   allocCPUs,
		})
		if err != nil {
			return err
		}
		boxID = int(id)

		if opts.noShard {
			return nil
		}

		_, err = s.allocateIPShard(ctx, queries, opts.userID, boxID)
		return err
	})
	if err != nil {
		return 0, err
	}

	s.recordUserEventBestEffort(ctx, opts.userID, userEventCreatedBox)
	return boxID, nil
}

// cleanupPreCreatedBox removes a box and its IP shard allocation after a
// failed creation attempt. Unlike deleteBox, this does not attempt to delete
// a container (none exists yet) or track the deletion in deleted_boxes.
func (s *Server) cleanupPreCreatedBox(ctx context.Context, boxID int) error {
	ctx = context.WithoutCancel(ctx)
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.DeleteBoxIPShard(ctx, boxID); err != nil {
			return fmt.Errorf("deleting IP shard: %w", err)
		}
		return queries.DeleteBox(ctx, boxID)
	})
}

var errNoIPShardsAvailable = errors.New("no IP shards available")

func (s *Server) allocateIPShard(ctx context.Context, queries *exedb.Queries, userID string, boxID int) (int, error) {
	// Check if user is in a team - if so, use team-wide shard pool
	// Use queries directly since we're inside a transaction
	team, teamErr := queries.GetTeamForUser(ctx, userID)
	inTeam := teamErr == nil && team.TeamID != ""

	// Determine the effective max boxes limit.
	var limits *UserLimits
	if inTeam && team.Limits != nil {
		limits = ParseUserLimitsFromJSON(*team.Limits)
	} else {
		user, err := queries.GetUserWithDetails(ctx, userID)
		if err == nil {
			limits = ParseUserLimits(&user)
		}
	}
	maxBoxes := GetMaxBoxes(limits)

	var shards []int64
	var err error
	if inTeam {
		// User is in a team - get all shards used by team members
		shards, err = queries.ListIPShardsForTeam(ctx, userID)
		if err != nil {
			return 0, fmt.Errorf("failed to list IP shards for team: %w", err)
		}
	} else {
		// Individual user - get their personal shards
		shards, err = queries.ListIPShardsForUser(ctx, userID)
		if err != nil {
			return 0, fmt.Errorf("failed to list IP shards for user %s: %w", userID, err)
		}
	}

	// Enforce per-user/team box count limit (separate from shard capacity).
	if len(shards) >= maxBoxes {
		return 0, errNoIPShardsAvailable
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
	// Commit to the whole thing. Avoid partial deletions on client disconnect.
	ctx = context.WithoutCancel(ctx)

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

		// Drop any pooled SSH connections after deleting so proxy requests fail fast
		if box.SSHPort != nil {
			sshHost := exeweb.BoxSSHHost(s.slog(), box.Ctrhost)
			s.sshPool.DropConnectionsTo(sshHost, int(*box.SSHPort))
		}
	}

	// Delete from database and track in deleted_boxes
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		userID := box.CreatedByUserID

		// Delete IP shard allocation first
		if err := queries.DeleteBoxIPShard(ctx, box.ID); err != nil {
			return fmt.Errorf("deleting IP shard: %w", err)
		}

		// Delete auth cookies for all box subdomains (otherwise they show up on
		// users' profile pages and cause consternation). We explicitly delete the
		// three known subdomain patterns rather than using LIKE.
		// Errors here are logged but don't fail the deletion.
		for _, domain := range []string{
			s.env.BoxSub(box.Name),        // boxname.exe.cloud
			s.env.BoxXtermSub(box.Name),   // boxname.xterm.exe.cloud
			s.env.BoxShelleySub(box.Name), // boxname.shelley.exe.cloud
		} {
			if err := queries.DeleteAuthCookiesByDomain(ctx, domain); err != nil {
				s.slog().ErrorContext(ctx, "failed to delete auth cookies",
					"box_id", box.ID,
					"box_name", box.Name,
					"domain", domain,
					"error", err)
			}
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

	if err == nil {
		proxyChangeDeletedBox(box.Name)
	}

	return err
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

// getExeletClient looks up an exelet client by its full address.
func (s *Server) getExeletClient(host string) *exeletClient {
	return s.exeletClients[host]
}

// resolveExelet resolves a short exelet hostname to its full address and client.
// Accepts either a full address (tcp://host:9080) or short hostname (host).
// Returns empty string and nil if no match or if ambiguous (multiple matches).
func (s *Server) resolveExelet(host string) (string, *exeletClient) {
	// Try direct lookup first
	if client := s.exeletClients[host]; client != nil {
		return host, client
	}

	// Try to match by hostname
	var matchAddr string
	var matchClient *exeletClient
	for addr, client := range s.exeletClients {
		u, err := url.Parse(addr)
		if err != nil {
			continue
		}
		if u.Hostname() == host {
			if matchAddr != "" {
				// Ambiguous - multiple matches
				return "", nil
			}
			matchAddr = addr
			matchClient = client
		}
	}
	return matchAddr, matchClient
}

// Return a list of addresses of working exelet clients.
func (s *Server) exeletAddrs() []string {
	ret := make([]string, 0, len(s.exeletClients))
	for addr, client := range s.exeletClients {
		if client.up.Load() {
			ret = append(ret, addr)
		}
	}
	return ret
}

// exeletHostnames returns a list of short hostnames for working exelet clients.
func (s *Server) exeletHostnames() []string {
	ret := make([]string, 0, len(s.exeletClients))
	for addr, client := range s.exeletClients {
		if client.up.Load() {
			u, err := url.Parse(addr)
			if err != nil {
				ret = append(ret, addr)
				continue
			}
			ret = append(ret, u.Hostname())
		}
	}
	return ret
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

// stopBox stops a running box via the exelet and updates its status in the database.
// If the exelet is unavailable or the stop call fails, the box status is still
// updated to "stopped" in the database and the error is logged.
func (s *Server) stopBox(ctx context.Context, box exedb.Box) error {
	if box.ContainerID == nil {
		return s.updateBoxStatus(ctx, box.ID, "stopped")
	}

	ec := s.getExeletClient(box.Ctrhost)
	if ec != nil {
		if _, err := ec.client.StopInstance(ctx, &computeapi.StopInstanceRequest{ID: *box.ContainerID}); err != nil {
			s.slog().WarnContext(ctx, "failed to stop instance on exelet", "box", box.Name, "error", err)
		}
	} else {
		s.slog().WarnContext(ctx, "exelet not available for box stop", "box", box.Name, "ctrhost", box.Ctrhost)
	}

	if box.SSHPort != nil {
		sshHost := exeweb.BoxSSHHost(s.slog(), box.Ctrhost)
		s.sshPool.DropConnectionsTo(sshHost, int(*box.SSHPort))
	}

	return s.updateBoxStatus(ctx, box.ID, "stopped")
}

// Start starts HTTP, HTTPS (if configured), and SSH servers
func (s *Server) Start() error {
	if s.stopping.Load() {
		return fmt.Errorf("server is stopping")
	}
	s.slog().Info("Starting exed server")
	s.startOnce.Do(func() {
		s.startErr = s.start()
	})
	return s.startErr
}

func (s *Server) start() error {
	// Create a cancellable context for startup.
	// The cancel function is stored so Stop() can cancel the context
	// before stopping subsystems, ensuring background goroutines see
	// the cancellation before they try to log errors.
	ctx, cancel := context.WithCancel(context.Background())
	s.startCancel = cancel
	defer cancel()

	// Start embedded DNS server.
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

	// Pass lobby IP to DNS server for apex domain resolution
	if s.dnsServer != nil && s.LobbyIP.IsValid() {
		s.dnsServer.SetLobbyIP(s.LobbyIP)
	}

	if s.dnsServer != nil && len(s.PublicIPs) > 0 {
		s.validateIPShards(ctx)
	}

	// Start LMTP server for inbound email
	if s.lmtpServer != nil {
		if err := s.lmtpServer.Start(ctx); err != nil {
			s.slog().ErrorContext(ctx, "LMTP server failed to start", "error", err)
		} else {
			s.slog().InfoContext(ctx, "LMTP server started", "socket", s.lmtpServer.SocketPath())
		}
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

	// Initialize SSH server and piper plugin before launching goroutines
	// so that Stop() can always reach them.
	s.sshServer = NewSSHServer(s)
	sshHost := s.sshLn.tcp.IP.String()
	if s.sshLn.tcp.IP.IsUnspecified() {
		sshHost = "127.0.0.1"
	}
	s.piperPlugin = NewPiperPlugin(s, sshHost, s.sshLn.tcp.Port)

	// Start SSH server in a goroutine
	s.serveWg.Go(func() {
		if err := s.sshServer.Start(s.sshLn.ln); err != nil && !s.stopping.Load() {
			s.slog().ErrorContext(ctx, "SSH server startup failed", "error", err)
			cancel()
		}
	})

	// Start piper plugin server in a goroutine
	s.slog().InfoContext(ctx, "piper plugin server listening", "addr", s.pluginLn.addr, "port", s.pluginLn.tcp.Port)
	s.serveWg.Go(func() {
		if err := s.piperPlugin.Serve(s.pluginLn.ln); err != nil && !s.stopping.Load() {
			s.slog().ErrorContext(ctx, "Piper plugin server startup failed", "error", err)
			cancel()
		}
	})

	if s.env.AutoStartSSHPiper {
		// In dev mode, automatically start sshpiper if not already running
		go s.autoStartSSHPiper(ctx)

		s.slog().InfoContext(ctx, "SSH server started in local dev mode. Connect with:")
		s.slog().InfoContext(ctx, fmt.Sprintf("  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p %v localhost", s.sshLn.tcp.Port))
	}

	// Start exeprox grpc server. This lets exeprox clients contact
	// exed for the information they need.
	s.slog().InfoContext(ctx, "exeprox server listening", "addr", s.exeproxServiceLn.addr, "port", s.exeproxServiceLn.tcp.Port)
	go func() {
		if err := s.exeproxServiceServer.Serve(s.exeproxServiceLn.ln); err != nil {
			s.slog().ErrorContext(ctx, "Failed to start exeprox grpc server", "error", err)
			cancel()
		}
	}()

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

	s.updateExeletUsageHeartbeat(ctx)

	// Wait for interrupt signal or startup failure
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	s.ready.Done()

	select {
	case <-sigChan:
		s.slog().InfoContext(ctx, "Shutting down servers...")
		return s.Stop()
	case <-s.stopChan:
		return nil // Stop was called externally
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

// isBasicUser reports whether user is a "basic user" — created for login-with-exe,
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
func (s *Server) createUserRecord(ctx context.Context, queries *exedb.Queries, emailAddr string, createdForLoginWithExe bool) (string, error) {
	userID, err := generateUserID()
	if err != nil {
		return "", fmt.Errorf("failed to generate user ID: %w", err)
	}

	canonicalEmail, err := email.CanonicalizeEmail(emailAddr)
	if err != nil {
		return "", fmt.Errorf("failed to canonicalize email: %w", err)
	}

	if err := queries.InsertUser(ctx, exedb.InsertUserParams{
		UserID:                 userID,
		Email:                  emailAddr,
		CanonicalEmail:         &canonicalEmail,
		CreatedForLoginWithExe: createdForLoginWithExe,
		Region:                 region.Default().Code,
	}); err != nil {
		return "", fmt.Errorf("failed to create user: %w", err)
	}

	return userID, nil
}

// checkEmailQuality checks the email quality via IPQS and updates the user if disposable.
// This should be called after user creation, outside of any transaction.
// Returns nil if IPQS is disabled (no API key) or if the email is in the bypass list.
func (s *Server) checkEmailQuality(ctx context.Context, userID, email string) error {
	if s.ipqsAPIKey == "" {
		return nil
	}

	// Check if email is in the bypass list
	bypassed, err := withRxRes1(s, ctx, (*exedb.Queries).IsEmailQualityBypassed, email)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to check email quality bypass", "error", err, "email", email)
		// Continue with the check if we can't read the bypass table
	} else if bypassed == 1 {
		s.slog().InfoContext(ctx, "email quality check bypassed", "email", email)
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

// signupValidationParams specifies a new signup attempt.
//
//exe:completeinit
type signupValidationParams struct {
	ip               string // Client IP address
	email            string // Email address being registered
	source           string // "web", "ssh", or "mobile"
	trustedGitHubKey bool   // If true, bypass IP abuse check (user has verified GitHub SSH key with good standing)
	hasInviteCode    bool   // If true, bypass all abuse checks (user has a valid invite code)
}

// validateNewSignup checks whether a possibly new user may sign up; it returns a user-friendly error if not.
// If the user already exists, it always returns nil.
// This is the single chokepoint for new signup attempts (web, SSH, mobile).
// Rate limiting is handled separately, earlier in each flow, by checkSignupRateLimit.
func (s *Server) validateNewSignup(ctx context.Context, p signupValidationParams) error {
	_, err := s.GetUserIDByEmail(ctx, p.email)
	if err == nil {
		return nil // user exists, no checks needed
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.slog().ErrorContext(ctx, "failed to check existing user for signup validation", "error", err, "email", p.email)
		return errors.New("sign-up is temporarily unavailable")
	}
	if s.IsLoginCreationDisabled(ctx) {
		s.signupMetrics.IncBlocked("login_creation_disabled", p.source)
		s.recordSignupRejection(ctx, p, "login_creation_disabled", "")
		return errors.New("account creation is temporarily unavailable")
	}
	s.slog().InfoContext(ctx, "vetting new signup", "ip", p.ip, "email", p.email)
	sloghttp.AddContextAttributes(ctx, slog.String("email", p.email))

	// Check if email is in the bypass list
	bypassed, err := withRxRes1(s, ctx, (*exedb.Queries).IsEmailQualityBypassed, p.email)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to check email quality bypass for signup", "error", err, "email", p.email)
		// Continue with the check if we can't read the bypass table
	} else if bypassed == 1 {
		s.slog().InfoContext(ctx, "signup quality checks bypassed", "email", p.email)
		return nil
	}

	// Skip IP abuse check for users with trusted GitHub SSH keys
	if p.trustedGitHubKey {
		s.slog().InfoContext(ctx, "signup quality checks bypassed for trusted GitHub key", "email", p.email)
		return nil
	}

	// Skip all abuse checks for users with a valid invite code
	if p.hasInviteCode {
		s.slog().InfoContext(ctx, "signup quality checks bypassed for invite code", "email", p.email)
		return nil
	}

	if flagged, ipqsJSON := s.ipFlaggedForAbuse(ctx, p.ip); flagged {
		s.slog().InfoContext(ctx, "blocking signup due to recent_abuse", "ip", p.ip)
		s.signupMetrics.IncBlocked("ip_abuse", p.source)
		s.recordSignupRejection(ctx, p, "ip_abuse", ipqsJSON)
		return fmt.Errorf("unable to process signup (trace=%s, email=%s)", tracing.TraceIDFromContext(ctx), p.email)
	}
	return nil
}

// recordSignupRejection records a rejected signup attempt to the database.
// ipqsResponseJSON is optional and only provided when the rejection is due to IPQS IP abuse.
// Errors are logged but not returned since this is best-effort.
func (s *Server) recordSignupRejection(ctx context.Context, p signupValidationParams, reason, ipqsResponseJSON string) {
	var ipqsJSON *string
	if ipqsResponseJSON != "" {
		ipqsJSON = &ipqsResponseJSON
	}
	err := withTx1(s, ctx, (*exedb.Queries).InsertSignupRejection, exedb.InsertSignupRejectionParams{
		Email:            p.email,
		Ip:               p.ip,
		Reason:           reason,
		Source:           p.source,
		IpqsResponseJson: ipqsJSON,
	})
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to record signup rejection", "error", err, "email", p.email, "ip", p.ip, "reason", reason)
	}
}

// ipFlaggedForAbuse reports whether ip has recent abuse according to IPQS.
// Returns the flagged status and the raw JSON response (if available).
// Fails open: returns false on errors.
func (s *Server) ipFlaggedForAbuse(ctx context.Context, ip string) (flagged bool, ipqsResponseJSON string) {
	// Check if IP abuse filter is disabled via debug page
	if s.IsIPAbuseFilterDisabled(ctx) {
		return false, ""
	}
	if s.ipqsAPIKey == "" {
		return false, ""
	}
	if result, ok := s.cachedIPLookup(ip); ok {
		return result.isFlagged(), "" // no JSON available for cached results
	}

	// Call IPQS IP API with a timeout
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://www.ipqualityscore.com/api/json/ip/%s/%s", s.ipqsAPIKey, ip)
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		s.slog().ErrorContext(ctx, "failed to create IPQS IP request", "error", err, "ip", ip)
		return false, ""
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.slog().WarnContext(ctx, "IPQS IP request failed", "error", err, "ip", ip)
		return false, ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		s.slog().WarnContext(ctx, "failed to read IPQS IP response", "error", err, "ip", ip)
		return false, ""
	}

	var apiResp struct {
		Success bool `json:"success"`
		ipqsIPResult
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		s.slog().ErrorContext(ctx, "failed to parse IPQS IP response", "error", err, "ip", ip)
		return false, ""
	}

	if !apiResp.Success {
		s.slog().WarnContext(ctx, "IPQS IP returned unsuccessful response", "ip", ip)
		return false, ""
	}

	s.cacheIPLookup(ip, apiResp.ipqsIPResult)
	return apiResp.ipqsIPResult.isFlagged(), string(body)
}

// isFlagged reports whether this IP should be blocked from signup.
// Applies the US business hours bypass if enabled.
func (r ipqsIPResult) isFlagged() bool {
	if !r.RecentAbuse {
		return false
	}
	if ipAbuseAllowUSBypass && r.CountryCode == "US" && isUSBusinessHours(time.Now()) {
		return false
	}
	return true
}

// isUSBusinessHours reports whether t falls within US business hours:
// 7am Eastern to 8pm Pacific (covers the continental US workday).
func isUSBusinessHours(t time.Time) bool {
	return t.In(tzEastern).Hour() >= 7 && t.In(tzPacific).Hour() < 20
}

// cachedIPLookup returns the cached IPQS lookup result for ip.
func (s *Server) cachedIPLookup(ip string) (ipqsIPResult, bool) {
	s.ipAbuseCacheMu.Lock()
	defer s.ipAbuseCacheMu.Unlock()
	entry, ok := s.ipAbuseCache[ip]
	if !ok {
		return ipqsIPResult{}, false
	}
	if time.Since(entry.cachedAt) >= ipAbuseCacheTTL {
		delete(s.ipAbuseCache, ip)
		return ipqsIPResult{}, false
	}
	return entry.result, true
}

// cacheIPLookup stores an IPQS lookup result in the cache.
// Uses random replacement when the cache is full.
func (s *Server) cacheIPLookup(ip string, result ipqsIPResult) {
	s.ipAbuseCacheMu.Lock()
	defer s.ipAbuseCacheMu.Unlock()

	// Initialize cache if needed
	if s.ipAbuseCache == nil {
		s.ipAbuseCache = make(map[string]ipAbuseCacheEntry)
	}

	// If at capacity, evict a random entry
	if len(s.ipAbuseCache) >= ipAbuseCacheMaxEntries {
		// Pick a random key to evict
		for key := range s.ipAbuseCache {
			delete(s.ipAbuseCache, key)
			break // maps iterate in random order in Go
		}
	}

	s.ipAbuseCache[ip] = ipAbuseCacheEntry{
		result:   result,
		cachedAt: time.Now(),
	}
}

// QualityCheck specifies whether to run abuse/quality checks during signup.
type QualityCheck int

const (
	AllQualityChecks  QualityCheck = iota // Run all IPQS quality checks
	SkipQualityChecks                     // Skip quality checks (e.g., user has invite code)
)

// createUser creates a new user with their resource allocation.
// This is used for SSH registration flow, not login-with-exe.
func (s *Server) createUser(ctx context.Context, publicKey, email string, qc QualityCheck) (*exedb.User, error) {
	var user exedb.User

	// First create the user and allocation in the database
	err := s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		userID, err := s.createUserRecord(ctx, queries, email, false)
		if err != nil {
			return err
		}

		// Add the SSH key to ssh_keys table with generated comment.
		// New users start with next_ssh_key_number=1, so first key is "key-1".
		fingerprint, err := sshkey.Fingerprint(publicKey)
		if err != nil {
			return fmt.Errorf("failed to compute SSH key fingerprint: %w", err)
		}
		comment, err := nextSSHKeyComment(ctx, queries, userID)
		if err != nil {
			return fmt.Errorf("failed to generate SSH key comment: %w", err)
		}
		if err := queries.InsertSSHKey(ctx, exedb.InsertSSHKeyParams{
			UserID:      userID,
			PublicKey:   publicKey,
			Comment:     comment,
			Fingerprint: fingerprint,
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
	// Skip this check if user has an invite code
	if qc == AllQualityChecks {
		if err := s.checkEmailQuality(context.WithoutCancel(ctx), user.UserID, email); err != nil {
			s.slog().WarnContext(ctx, "email quality check failed", "error", err, "email", email)
		}
	} else {
		s.slog().InfoContext(ctx, "email quality check skipped for invite code user", "email", email)
	}

	// Resolve any pending shares for this email
	if err := s.resolvePendingShares(ctx, email, user.UserID); err != nil {
		return nil, fmt.Errorf("failed to resolve pending shares: %w", err)
	}

	// Resolve any pending team invites for this email
	if err := s.resolvePendingTeamInvites(ctx, email, user.UserID); err != nil {
		return nil, fmt.Errorf("failed to resolve pending team invites: %w", err)
	}

	return &user, nil
}

// Stop shuts down all servers immediately.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() { close(s.stopChan) })
	s.stopping.Store(true)
	if s.startCancel != nil {
		s.startCancel()
	}

	ctx := context.Background()

	if s.httpServer != nil {
		if err := s.httpServer.Close(); err != nil {
			s.slog().ErrorContext(ctx, "HTTP server close error", "error", err)
		}
	}

	if s.httpsServer != nil {
		if err := s.httpsServer.Close(); err != nil {
			s.slog().ErrorContext(ctx, "HTTPS server close error", "error", err)
		}
	}

	if s.exeproxServiceServer != nil {
		s.exeproxServiceServer.Stop()
	}

	if s.tagResolver != nil {
		s.tagResolver.Stop()
	}
	if s.postmarkStatsCollector != nil {
		s.postmarkStatsCollector.Stop()
	}
	if s.bouncePoller != nil {
		s.bouncePoller.Stop()
	}
	if s.subscriptionPoller != nil {
		s.subscriptionPoller.Stop()
	}
	if s.dnsServer != nil {
		s.dnsServer.Stop(ctx)
	}
	if s.lmtpServer != nil {
		s.lmtpServer.Stop(ctx)
	}
	if s.sshServer != nil {
		if err := s.sshServer.Stop(); err != nil {
			s.slog().ErrorContext(ctx, "SSH server close error", "error", err)
		}
	}
	if s.piperPlugin != nil {
		s.piperPlugin.Stop()
	}

	s.serveWg.Wait()

	s.transportCache.Close()
	if err := s.sshPool.Close(); err != nil {
		s.slog().ErrorContext(ctx, "SSH pool close error", "error", err)
	}
	if s.hllTracker != nil {
		if err := s.hllTracker.Close(); err != nil {
			s.slog().ErrorContext(ctx, "HLL tracker close error", "error", err)
		}
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
func (s *Server) lookupEphemeralProxyKey(ctx context.Context, proxyKey ssh.PublicKey) (originalKey []byte, localAddress, clientAddr string, exists bool) {
	// Get the original user key from the piper plugin
	// The piper plugin is always configured when SSH proxy is enabled
	if s.piperPlugin == nil {
		s.slog().ErrorContext(ctx, "Piper plugin not configured but proxy key received")
		return nil, "", "", false
	}

	proxyFingerprint := s.GetPublicKeyFingerprint(proxyKey)
	s.slog().DebugContext(ctx, "Looking up proxy key", "fingerprint", proxyFingerprint[:16])

	originalUserKey, localAddress, clientAddr, exists := s.piperPlugin.lookupOriginalUserKey(proxyFingerprint)
	if !exists {
		s.slog().DebugContext(ctx, "Proxy key not found or expired", "fingerprint", proxyFingerprint[:16])
		return nil, "", "", false // Not a proxy key or expired
	}

	s.slog().DebugContext(ctx, "Found original user key for proxy key", "key_length", len(originalUserKey), "local_address", localAddress, "client_addr", clientAddr, "proxy_fingerprint", proxyFingerprint[:16])
	return originalUserKey, localAddress, clientAddr, true
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
// and includes the local address for ipAllocator routing and client address for IPQS checks
func (s *Server) authenticateProxyUserWithLocalAddress(ctx context.Context, username string, originalUserKeyBytes []byte, localAddress, clientAddr string) (*ssh.Permissions, error) {
	s.slog().InfoContext(ctx, "authenticateProxyUserWithLocalAddress", "username", username, "localAddress", localAddress, "clientAddr", clientAddr, "keyBytes", len(originalUserKeyBytes))

	// Check for special container-logs username format and easter egg careers usernames
	if strings.HasPrefix(username, "container-logs:") || slices.Contains(boxname.JobsRelated, username) {
		s.slog().InfoContext(ctx, "Detected special container-logs username, bypassing normal auth", "username", username)
		// This is a special request to show container logs
		// We don't need to authenticate the user normally, just pass through
		// The SSH server will handle this specially
		return &ssh.Permissions{
			Extensions: map[string]string{
				"registered":  "true",
				"proxy_user":  username,
				"public_key":  "", // Empty key for special log display
				"client_addr": clientAddr,
			},
		}, nil
	}

	perms, err := s.authenticateProxyUser(ctx, username, originalUserKeyBytes)
	if err != nil {
		return nil, err
	}
	// Add client address to extensions for use in signup validation
	perms.Extensions["client_addr"] = clientAddr
	return perms, nil
}

// generateUserID creates a new user ID with "usr" prefix + 13 random characters
func generateUserID() (string, error) {
	randomPart := crand.Text()
	if len(randomPart) < 13 {
		return "", fmt.Errorf("random text too short: %d", len(randomPart))
	}
	return "usr" + randomPart[:13], nil
}

// getUserIDByPublicKey gets user_id from an SSH public key and updates last_used_at
func (s *Server) getUserIDByPublicKey(ctx context.Context, publicKey ssh.PublicKey) (string, error) {
	publicKeyStr := string(ssh.MarshalAuthorizedKey(publicKey))
	userID, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserIDBySSHKey, publicKeyStr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("user not found for public key")
	}
	if err != nil {
		return "", fmt.Errorf("database error: %w", err)
	}

	// Update last_used_at timestamp
	if err := withTx1(s, ctx, (*exedb.Queries).UpdateSSHKeyLastUsed, publicKeyStr); err != nil {
		return "", fmt.Errorf("failed to update SSH key last_used_at: %w", err)
	}

	return userID, nil
}

// GetUserByEmail retrieves a user by their canonical email address.
// The input email is canonicalized before lookup.
func (s *Server) GetUserByEmail(ctx context.Context, emailAddr string) (*exedb.User, error) {
	canonicalEmail, err := email.CanonicalizeEmail(emailAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid email: %w", err)
	}
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserByEmail, &canonicalEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("database error: %w", err)
	}
	return &user, nil
}

// GetUserIDByEmail retrieves a user ID by their canonical email address.
// The input email is canonicalized before lookup.
func (s *Server) GetUserIDByEmail(ctx context.Context, emailAddr string) (string, error) {
	canonicalEmail, err := email.CanonicalizeEmail(emailAddr)
	if err != nil {
		return "", fmt.Errorf("invalid email: %w", err)
	}
	return withRxRes1(s, ctx, (*exedb.Queries).GetUserIDByEmail, &canonicalEmail)
}

// canonicalEmailPtr returns a pointer to the canonicalized email, for use in queries within transactions.
func canonicalEmailPtr(emailAddr string) (*string, error) {
	canonical, err := email.CanonicalizeEmail(emailAddr)
	if err != nil {
		return nil, err
	}
	return &canonical, nil
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

// selectExeletClient selects an exelet client.
// If the user has VMs on an existing exelet that is not too loaded,
// it uses that. Otherwise, if a preferred exelet is configured and available,
// it uses that. Otherwise, it picks a less loaded exelet.
func (s *Server) selectExeletClient(ctx context.Context, userID string) (*exeletClient, string, error) {
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
			s.slog().DebugContext(ctx, "not selecting exelet because it is exe-ctr-02", "user", userID, "exelet", maxHost, "userVMCount", maxCnt)
			client = nil
		}

		var count int
		if client != nil {
			// Don't pick this VM if it is too loaded.
			count, err = client.countInstances(ctx)
			if err != nil {
				return nil, "", err
			}
			if count >= int(client.region.VMHardLimit) {
				s.slog().DebugContext(ctx, "not selecting exelet because it is over threshold", "user", userID, "exelet", maxHost, "userVMCount", maxCnt, "exeletVMCount", count)
				client = nil
			}
		}

		if client != nil {
			s.slog().DebugContext(ctx, "selecting exelet with most VMs for user", "user", userID, "exelet", maxHost, "userVMCount", maxCnt, "exeletVMCount", count)
			return client, maxHost, nil
		}
	}

	// Check for preferred exelet setting
	preferredAddr, err := withRxRes0(s, ctx, (*exedb.Queries).GetPreferredExelet)
	if err == nil && preferredAddr != "" {
		// Preferred exelet is configured, try to use it
		if client, ok := s.exeletClients[preferredAddr]; ok {
			s.slog().DebugContext(ctx, "selecting preferred exelet", "user", userID, "exelet", preferredAddr)
			return client, preferredAddr, nil
		}
		// Preferred exelet is not available, log error and fall back
		slog.ErrorContext(ctx, "preferred exelet not available, falling back to hash-based selection",
			"preferred_addr", preferredAddr,
			"available_addrs", s.exeletAddrs())
	}

	// Find the least loaded exelets.
	ecs := make([]*exeletClient, 0, len(s.exeletClients))
	for _, c := range s.exeletClients {
		if c.up.Load() {
			ecs = append(ecs, c)
		}
	}

	if len(ecs) == 0 {
		return nil, "", errors.New("no exelet clients available")
	}

	slices.SortFunc(ecs, exeletUsageCmp)

	// Find the set at the start that are considered equal.
	i := 1
	if i < len(ecs) && exeletUsageCmp(ecs[0], ecs[i]) == 0 {
		i++
	}
	ecs = ecs[:i]

	if len(ecs) == 1 {
		s.slog().DebugContext(ctx, "chose least loaded exelet", "exelet", ecs[0].addr, "exeletVMCount", ecs[0].count.Load())
		return ecs[0], ecs[0].addr, nil
	}

	hash := 0
	for _, c := range userID {
		hash = hash*31 + int(c)
	}
	if hash < 0 {
		hash = -hash
	}
	idx := hash % len(ecs)

	s.slog().DebugContext(ctx, "chose exelet", "exelet", ecs[idx].addr, "exeletVMCount", ecs[idx].count.Load(), "idx", idx, "equivalentExelets", len(ecs))

	return ecs[idx], ecs[idx].addr, nil
}

// updateUsageConcurrency is the number of exelets for which
// to update usage concurrently.
const updateUsageConcurrency = 3

// updateAllExeletUsage updates usage information for all exelets in parallel.
func (s *Server) updateAllExeletUsage(ctx context.Context) {
	concurrency := make(chan struct{}, updateUsageConcurrency)

	var wg sync.WaitGroup
	for _, ec := range s.exeletClients {
		wg.Add(1)
		go func() {
			concurrency <- struct{}{}
			defer func() {
				<-concurrency
				wg.Done()
			}()

			ec.updateUsage(ctx)
		}()
	}

	wg.Wait()

	s.autoThrottleVMCreation(ctx)
}

// updateUsageHeartbeat is how often to update exelet usage.
const updateUsageHeartbeat = 10 * time.Minute

// updateExeletUsageHeartbeat arranges to update usage information
// for all exelets every updateUsageHeartbeat.
func (s *Server) updateExeletUsageHeartbeat(ctx context.Context) {
	// Initial update.
	s.updateAllExeletUsage(ctx)

	go func() {
		ticker := time.NewTicker(updateUsageHeartbeat)
		for {
			select {
			case <-ticker.C:
				s.updateAllExeletUsage(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// extremeUsage provides usage measurements that indicate that
// an exelet is under heavy load, and should not be used for a new VM.
// If a field in extremeUsage is positive, then an exelet with
// a larger value for that field is under heavy load.
// If a field is negative, then an exelet with a smaller value
// for that field is under heavy load.
//
// If a field is zero in either extremeUsage or the input, it is ignored.
//
// These values are entirely heuristic and should be adjusted as we learn.
var extremeUsage = &resourceapi.MachineUsage{
	LoadAverage:  100,
	MemAvailable: -(1 << 20),  // value is KiB, this is 1 GiB
	SwapFree:     -(10 << 20), // value is KiB, this is 10 GiB
	DiskFree:     -(10 << 20), // value is KiB, this is 10 GiB
}

// isExtreme reports whether u indicates heavy load.
func isExtreme(u *resourceapi.MachineUsage) bool {
	uv := reflect.ValueOf(u).Elem()
	ev := reflect.ValueOf(extremeUsage).Elem()
	typ := uv.Type()
	for i := range typ.NumField() {
		// We ignore zero values in ev because some fields
		// don't matter. We ignore zero fields in uv to
		// avoid false positives when we fail to measure
		// some values.
		if uv.Field(i).IsZero() || ev.Field(i).IsZero() {
			continue
		}
		if ev.Field(i).CanInt() {
			ui := uv.Field(i).Int()
			ei := ev.Field(i).Int()
			if ei < 0 {
				if ui < -ei {
					slog.Debug("isExtreme", "field", typ.Field(i).Name, "ui", ui, "-ei", -ei)
					return true
				}
			} else {
				if ui > ei {
					slog.Debug("isExtreme", "field", typ.Field(i).Name, "ui", ui, "ei", ei)
					return true
				}
			}
		} else {
			uf := uv.Field(i).Float()
			ef := ev.Field(i).Float()
			if ef < 0 {
				if uf < -ef {
					slog.Debug("isExtreme", "field", typ.Field(i).Name, "uf", u, "-ef", -ef)
					return true
				}
			} else {
				if uf > ef {
					slog.Debug("isExtreme", "field", typ.Field(i).Name, "uf", uf, "ef", ef)
					return true
				}
			}
		}
	}
	return false
}

// Compare exelet usage to pick the one with the least load.
// Return -1 if the first exelet is preferred,
// 1 if the second is preferred,
// 0 if they are approximately the same.
func exeletUsageCmp(a, b *exeletClient) int {
	usageA := a.usage.Load()
	countA := a.count.Load()

	usageB := b.usage.Load()
	countB := b.count.Load()

	switch {
	case usageA == nil && usageB == nil:
		return 0
	case usageA == nil:
		return 1
	case usageB == nil:
		return -1
	}

	// First we check for extreme cases.
	extremeA := isExtreme(usageA) || countA+10 >= a.region.VMHardLimit
	extremeB := isExtreme(usageB) || countB+10 >= b.region.VMHardLimit
	switch {
	case extremeA && extremeB:
		return 0
	case extremeA:
		return 1
	case extremeB:
		return -1
	}

	// Round load average to a multiple of 4 for comparison.
	mult4 := func(avg float32) int {
		return (int(math.Round(float64(avg))) + 2) / 4
	}
	loadA := mult4(usageA.LoadAverage)
	loadB := mult4(usageB.LoadAverage)
	if r := cmp.Compare(loadA, loadB); r != 0 {
		return r
	}

	// Compare available memory in gigabytes.
	memA := usageA.MemAvailable >> 20
	memB := usageB.MemAvailable >> 20
	if r := cmp.Compare(memA, memB); r != 0 {
		return -r // negate to prefer larger value
	}

	// Compare free swap in gigabytes.
	swapA := usageA.SwapFree >> 20
	swapB := usageB.SwapFree >> 20
	if r := cmp.Compare(swapA, swapB); r != 0 {
		return -r // negate to prefer larger value
	}

	// Compare free disk in gigabytes.
	diskA := usageA.DiskFree >> 30
	diskB := usageB.DiskFree >> 30
	if r := cmp.Compare(diskA, diskB); r != 0 {
		return -r // negate to prefer larger value
	}

	// Compare network data in megabytes.
	netA := int64(math.Round(float64(usageA.RxBytesRate))) >> 20
	netB := int64(math.Round(float64(usageA.RxBytesRate))) >> 20
	if r := cmp.Compare(netA, netB); r != 0 {
		return r
	}

	netA = int64(math.Round(float64(usageA.TxBytesRate))) >> 20
	netB = int64(math.Round(float64(usageA.TxBytesRate))) >> 20
	if r := cmp.Compare(netA, netB); r != 0 {
		return r
	}

	// Compare VM counts in multiples of 32.
	if r := cmp.Compare((countA+16)/32, (countB+16)/32); r != 0 {
		return r
	}

	return 0
}

// autoThrottleVMCreation enables throttling if all exelets have hit the VM limit.
func (s *Server) autoThrottleVMCreation(ctx context.Context) {
	if len(s.exeletClients) == 0 {
		return
	}

	someBelowLimit := false
	for _, ec := range s.exeletClients {
		count := ec.count.Load()
		if count < ec.region.VMSoftLimit {
			// We still have capacity, nothing to do.
			return
		}
		if count < ec.region.VMHardLimit {
			someBelowLimit = true
		}
	}

	// Every exelet is at the warning level.

	enabledStr, err := withRxRes0(s, ctx, (*exedb.Queries).GetNewThrottleEnabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.ErrorContext(ctx, "failed to get throttle enabled", "error", err)
		return
	}
	if enabledStr == "true" {
		// VMs are currently throttled, nothing to do.
		return
	}

	if someBelowLimit {
		// There is still a bit of room. Warn.
		// Right now this will warn every updateUsageHeartbeat,
		// which is 10 minutes.
		slog.WarnContext(ctx, "all exelets near VM limit")
		s.slackFeed.ExeletCapacityWarning(ctx)
		return
	}

	// There is no room. Throttle.
	if err := withTx1(s, ctx, (*exedb.Queries).SetNewThrottleEnabled, "true"); err != nil {
		s.slog().ErrorContext(ctx, "auto-throttle: failed to enable throttle", "error", err)
		return
	}

	s.slog().ErrorContext(ctx, "auto-throttle: VM limit reached on all exelets, throttle enabled")
}
