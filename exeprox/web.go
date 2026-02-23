package exeprox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"html/template"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"exe.dev/domz"
	"exe.dev/exeweb"
	"exe.dev/llmgateway"
	"exe.dev/metricsbag"
	"exe.dev/publicips"
	"exe.dev/stage"
	"exe.dev/tracing"
	"exe.dev/wildcardcert"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale"
)

// WebProxy implements HTTP/HTTPS proxying.
type WebProxy struct {
	proxy *Proxy

	env *stage.Env // prod, staging, etc.

	exedHTTPPort  int
	exedHTTPSPort int

	transportCache *exeweb.TransportCache

	httpLn     *listener
	httpServer *http.Server

	httpsLn     *listener
	httpsServer *http.Server

	httpMetrics *exeweb.HTTPMetrics

	proxyLns []*listener // additional listeners for specific ports

	certManager *autocert.Manager

	// Tailscale HTTPS (preloaded at startup)
	tsCertMu sync.Mutex
	tsCert   *tls.Certificate
	tsDomain string

	// publicIPs is a map from private local IP addresses to
	// public IP / domain / shard.
	publicIPs map[netip.Addr]publicips.PublicIP

	// lobbyIP is the public IP for the lobby/REPL (that is, ssh exe.dev),
	// not associated with any shard.
	lobbyIP netip.Addr

	netHTTPLogger *log.Logger // logger for http.Server

	templates *template.Template

	stopping atomic.Bool // reports whether stop was called
}

// setup initializes the WebProxy.
// Call setup, then start.
func (wp *WebProxy) setup() {
	wp.setupHTTPServer()
	wp.setupHTTPSServer()
	wp.setupProxyServers()
}

// prepareHandler returns the main HTTP handler.
func (wp *WebProxy) prepareHandler() http.Handler {
	llmg := wp.prepareLLMGateway()
	servMux := http.NewServeMux()
	servMux.Handle("/_/gateway/", llmg)
	servMux.Handle("/", wp)

	h := wp.httpMetrics.Wrap(servMux)
	h = metricsbag.Wrap(h)
	h = LoggerMiddleware(wp.lg())(h)
	h = RecoverHTTPMiddleware(wp.lg())(h)
	return h
}

// prepareLLMGateway returns an HTTP handler for LLM gateway requests.
func (wp *WebProxy) prepareLLMGateway() http.Handler {
	anthropicAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	fireworksAPIKey := os.Getenv("FIREWORKS_API_KEY")
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")

	llmg := llmgateway.NewGateway(wp.lg(),
		wp.exeproxData().LLMGateway(),
		llmgateway.APIKeys{
			Anthropic: anthropicAPIKey,
			Fireworks: fireworksAPIKey,
			OpenAI:    openaiAPIKey,
		},
		*wp.env,
	)
	return llmg
}

// setupHTTPServer configures the HTTP server.
func (wp *WebProxy) setupHTTPServer() {
	if wp.httpLn == nil {
		return
	}
	h := wp.prepareHandler()
	wp.httpServer = &http.Server{
		Addr:     wp.httpLn.addr(),
		Handler:  h,
		ErrorLog: wp.netHTTPLogger,
	}
}

// setupHTTPSServer configures the HTTPS server.
// It uses Let's Encrypt if enabled.
func (wp *WebProxy) setupHTTPSServer() {
	if wp.httpsLn == nil {
		return
	}

	wp.certManager = &autocert.Manager{
		Cache:      autocert.DirCache("certs"),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: wp.validateHostForTLSCert,
	}

	// TODO: Set up cobble for local development?

	wp.httpsServer = &http.Server{
		Addr:     wp.httpsLn.addr(),
		Handler:  wp.prepareHandler(),
		ErrorLog: wp.netHTTPLogger,
		TLSConfig: &tls.Config{
			GetCertificate: wp.getCertificate,
			NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
		},
		// ConnContext adds a trace_id to the connection context,
		// which becomes the parent context for all requests on this
		// connection. This ensures the same trace_id is used for TLS
		// handshake logging and subsequent HTTP request logging.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			traceID := tracing.GenerateTraceID()
			return tracing.ContextWithTraceID(ctx, traceID)
		},
	}

	wp.setupTailscale()
}

// setupTailscale sets up the Tailscale DNS name and certificate.
// If certs don't work, you might need to run the following in prod:
//
//	sudo tailscale set --operator=$USER
func (wp *WebProxy) setupTailscale() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tailscaleAcknowledgeUnstableAPI()

	var lc local.Client
	st, err := lc.Status(ctx)
	if err != nil || st == nil || st.Self == nil || st.Self.DNSName == "" {
		if err != nil {
			wp.lg().ErrorContext(ctx, "tailscale status unavailable", "error", err)
		} else {
			wp.lg().ErrorContext(ctx, "tailscale DNS name not found")
		}
		return
	}

	wp.tsDomain = domz.Canonicalize(st.Self.DNSName)

	// Try to eagerly fetch and cache cert, but it's optional.
	certPEM, keyPEM, err := lc.CertPair(ctx, wp.tsDomain)
	if err != nil {
		wp.lg().ErrorContext(ctx, "tailscale cert pair not preloaded", "error", err)
		return
	}

	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		wp.lg().ErrorContext(ctx, "tailscale x509 keypair parse error", "error", err)
		return
	}

	if len(c.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
			c.Leaf = leaf
		}
	}

	wp.tsCertMu.Lock()
	wp.tsCert = &c
	wp.tsCertMu.Unlock()

	wp.lg().InfoContext(ctx, "tailscale cert loaded", "domain", wp.tsDomain)
}

var tailscaleAcknowledgeUnstableAPI = sync.OnceFunc(func() {
	tailscale.I_Acknowledge_This_API_Is_Unstable = true
})

// start starts the web and proxy servers.
func (wp *WebProxy) start(ctx context.Context, cancel context.CancelFunc) error {
	wp.initShardIPs(ctx)

	if wp.httpLn != nil {
		go func() {
			wp.lg().DebugContext(ctx, "HTTP server starting", "addr", wp.httpLn)
			if err := wp.httpServer.Serve(wp.httpLn.ln); err != nil && err != http.ErrServerClosed {
				wp.lg().ErrorContext(ctx, "HTTP server startup failed", "error", err)
				cancel()
			}
		}()
	}

	if wp.httpsLn != nil {
		go func() {
			host := wp.env.WebHost
			if host == "" {
				host = "configured host"
			}
			wp.lg().DebugContext(ctx, "HTTPS server starting with Let's Encrypt", "host", host, "addr", wp.httpsLn)
			if err := wp.httpsServer.ServeTLS(wp.httpsLn.ln, "", ""); err != nil && err != http.ErrServerClosed {
				wp.lg().ErrorContext(ctx, "HTTPS server startup failed", "error", err)
				cancel()
			}
		}()
	}

	for _, proxyLn := range wp.proxyLns {
		go func(ln *listener) {
			if wp.httpsLn != nil {
				if err := wp.httpsServer.ServeTLS(proxyLn.ln, "", ""); err != nil && err != http.ErrServerClosed {
					wp.lg().ErrorContext(ctx, "Proxy listener startup failed (HTTPS)", "addr", ln, "error", err)
					cancel()
				}
			} else {
				wp.lg().InfoContext(ctx, "Proxy listener starting with HTTP handler", "addr", ln.addr())
				if err := wp.httpServer.Serve(ln.ln); err != nil && err != http.ErrServerClosed {
					wp.lg().ErrorContext(ctx, "Proxy listener startup failed (HTTP)", "addr", ln, "error", err)
					cancel()
				}
			}
		}(proxyLn)
	}

	return nil
}

// initShardIPS builds the mapping from local IPs to public IP info.
// If env.DiscoverPublicIPs is true, we use ExeproxData to fetch
// the EC2 metadata and IP shard tables from the exed database.
// If env.DiscoverPublicIPs is false, we are testing and use
// 127.21.0.x where x is the shard number.
func (wp *WebProxy) initShardIPs(ctx context.Context) {
	defer wp.logIPResolver()

	if len(wp.publicIPs) != 0 {
		// already initialized
		return
	}

	if !wp.env.DiscoverPublicIPs {
		wp.lg().InfoContext(ctx, "using dev IP resolver", "box_host", wp.env.BoxHost)
		ips, err := publicips.LocalhostIPs(ctx, wp.env.BoxHost, wp.env.NumShards)
		if err != nil {
			wp.lg().ErrorContext(ctx, "localhost IP setup failed", "error", err)
			return
		}
		wp.publicIPs = ips
		// For local dev, use 127.21.0.0 as the lobby IP
		wp.lobbyIP = netip.AddrFrom4([4]byte{127, 21, 0, 0})
		return
	}

	ips, lobbyIP, err := wp.exeproxData().PublicIPs(ctx)
	if err != nil {
		wp.lg().ErrorContext(ctx, "public IP discovery failed", "error", err)
		return
	}
	wp.publicIPs = ips
	wp.lobbyIP = lobbyIP
}

// logIPResolvre logs the public IPs.
func (wp *WebProxy) logIPResolver() {
	if len(wp.publicIPs) == 0 {
		wp.lg().Warn("no public IP assignments discovered via metadata")
		return
	}

	assignments := make([]string, 0, len(wp.publicIPs))
	for privateAddr, info := range wp.publicIPs {
		assignments = append(assignments, fmt.Sprintf("%s->%s (%s)", privateAddr, info.IP, info.Domain))
	}
	slices.Sort(assignments)
	wp.lg().Info("public IP assignments loaded", "assignments", assignments)
}

// validateHostForTLSCert checks if the given host is valid
// for TLS certificate issuance.
func (wp *WebProxy) validateHostForTLSCert(ctx context.Context, host string) error {
	// TODO: exeprox should probably never see a request to exe.dev.
	// Do we need to issue certificates here?
	host = domz.Canonicalize(host)
	if domz.FirstMatch(host, wp.env.BoxHost, wp.env.WebHost) != "" {
		return nil
	}
	if host == "exe.new" {
		return nil
	}
	if host == "bold.dev" {
		return nil
	}

	boxName, err := wp.resolveCustomDomainBoxName(ctx, host)
	if err != nil {
		return err
	}
	if boxName == "" {
		wp.lg().WarnContext(ctx, "hostPolicy: unable to resolve box name", "host", host, "boxName", boxName)
		return fmt.Errorf("unable to resolve VM for %s", host)
	}
	_, exists, err := wp.proxy.boxes.lookup(ctx, wp.proxy.exeproxData, boxName)
	if err != nil {
		wp.lg().WarnContext(ctx, "hostPolicy: failed to lookup box", "host", host, "boxName", boxName, "error", err)
		return err
	}
	if !exists {
		wp.lg().WarnContext(ctx, "hostPolicy: no box found for subdomain", "host", host, "boxName", boxName)
		return fmt.Errorf("box not found: %s", boxName)
	}
	return nil
}

// resolveCustomDomainBoxName determines the box name
// associated with a custom domain.
func (wp *WebProxy) resolveCustomDomainBoxName(ctx context.Context, host string) (string, error) {
	dr := exeweb.DomainResolver{
		Lg:        wp.lg(),
		Env:       wp.env,
		LobbyIP:   wp.lobbyIP,
		PublicIPs: wp.publicIPs,
	}
	return dr.ResolveCustomDomainBoxName(ctx, host)
}

// getCertificate is the single TLS certificate dispatcher for HTTPS.
// It serves:
// - Tailscale node certificate for the machine's Tailscale DNS name
// - Wildcard certificates for BoxHost (exe.xyz) via DNS-01 challenges
// - Standard autocert (TLS-ALPN-01) for WebHost (exe.dev) and custom domains
func (wp *WebProxy) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello == nil || hello.ServerName == "" {
		return nil, fmt.Errorf("no server name provided")
	}

	serverName := domz.Canonicalize(hello.ServerName)

	// 1) Serve Tailscale certificate for exact Tailscale DNS name
	if wp.tsDomain != "" && serverName == wp.tsDomain {
		cert, err := wp.tailscaleCertificate()
		if err != nil {
			return nil, fmt.Errorf("tailscale certificate not available for %s: %w", wp.tsDomain, err)
		}
		return cert, nil
	}

	// 2) BoxHost (exe.xyz) uses wildcard certs via DNS-01
	if domz.FirstMatch(serverName, wp.env.BoxHost) != "" {
		cert, err := wp.exeproxData().CertForDomain(hello.Context(), serverName)
		if strings.Contains(err.Error(), wildcardcert.ErrUnrecognizedDomain.Error()) {
			wp.lg().Debug("wildcard CertForDomain rejected unrecognized domain", "serverName", serverName, "error", err)
		} else if err != nil {
			wp.lg().Error("wildcard CertForDomain failed; giving up", "serverName", serverName, "error", err)
		}
		return cert, err
	}

	// 3) WebHost (exe.dev) and custom domains use standard autocert (TLS-ALPN-01)
	if wp.certManager == nil {
		wp.lg().Error("no certificate manager configured; was https enabled at startup?", "serverName", serverName)
		return nil, fmt.Errorf("no certificate manager configured for %s", serverName)
	}

	return wp.certManager.GetCertificate(hello)
}

func (wp *WebProxy) tailscaleCertificate() (*tls.Certificate, error) {
	if wp.tsDomain == "" {
		return nil, errors.New("tailscale domain not configured")
	}

	wp.tsCertMu.Lock()
	defer wp.tsCertMu.Unlock()
	if wp.tsCert != nil {
		return wp.tsCert, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tailscaleAcknowledgeUnstableAPI()
	lc := &tailscale.LocalClient{}
	certPEM, keyPEM, err := lc.CertPair(ctx, wp.tsDomain)
	if err != nil {
		return nil, fmt.Errorf("tailscale cert pair not available: %w", err)
	}

	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tailscale x509 keypair parse error: %w", err)
	}
	if len(c.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil {
			c.Leaf = leaf
		}
	}
	wp.tsCert = &c
	wp.lg().InfoContext(ctx, "tailscale cert loaded", "domain", wp.tsDomain)

	return wp.tsCert, nil
}

// stop shuts down all servers.
func (wp *WebProxy) stop(ctx context.Context) {
	wp.stopping.Store(true)

	if wp.httpServer != nil {
		if err := wp.httpServer.Close(); err != nil {
			wp.lg().ErrorContext(ctx, "HTTP server close error", "error", err)
		}
	}

	if wp.httpsServer != nil {
		if err := wp.httpsServer.Close(); err != nil {
			wp.lg().ErrorContext(ctx, "HTTPS server close error", "error", err)
		}
	}

	if wp.transportCache != nil {
		wp.transportCache.Close()
	}

	// TODO: close down the proxy ports?
}

// exeproxData is a helper method to return the exexproxData to use.
func (wp *WebProxy) exeproxData() ExeproxData {
	return wp.proxy.exeproxData
}

// lg is a helper method to return the logger to use.
func (wp *WebProxy) lg() *slog.Logger {
	return wp.proxy.lg
}

// httpLogger is a logger for http.Server.
// It suppresses noisy lines.
type httpLogger struct {
	lg *slog.Logger
}

func (hl httpLogger) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))

	// In a random sample on Nov 17, 2025,
	// this log type accounted for about 85% of all log lines.
	if strings.HasPrefix(msg, "http: TLS handshake error from ") {
		return len(p), nil
	}

	hl.lg.Debug("net/http server error", "msg", msg)
	return len(p), nil
}
