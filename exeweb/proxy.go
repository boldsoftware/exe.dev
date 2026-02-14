package exeweb

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"exe.dev/container"
	"exe.dev/sshpool2"
	"exe.dev/stage"

	"golang.org/x/crypto/ssh"
)

// ProxyServer handles proxy requests for both exed and exeprox.
// Data is handled via the ProxyData interface,
// which for exed talks to the database and for exeprox talks to exed.
type ProxyServer struct {
	Data        ProxyData
	Lg          *slog.Logger
	Env         *stage.Env
	SSHPool     *sshpool2.Pool
	HTTPMetrics *HTTPMetrics
}

// ProxyAuthResult contains the result of proxy authentication.
type ProxyAuthResult struct {
	UserID string // authenticated user ID

	// CtxRaw is non-nil if we authenticated using a token.
	// The raw bytes are passed to the VM's HTTP server.
	CtxRaw []byte
}

// ProxyToContainer proxies an HTTP request to a container
// via SSH port forwarding.
func (ps *ProxyServer) ProxyToContainer(w http.ResponseWriter, r *http.Request, box *BoxData, route BoxRoute, authResult *ProxyAuthResult) error {
	// Validate box has SSH credentials
	if len(box.SSHClientPrivateKey) == 0 || box.SSHPort == 0 {
		return fmt.Errorf("VM missing SSH credentials")
	}

	// Parse the SSH private key
	sshKey, err := container.CreateSSHSigner(string(box.SSHClientPrivateKey))
	if err != nil {
		return fmt.Errorf("failed to parse SSH private key: %w", err)
	}

	// Determine SSH host address from the box's ctrhost
	sshHost := BoxSSHHost(ps.Lg, box.Ctrhost)

	// Try to proxy to the configured port
	err = ps.proxyViaSSHPortForward(w, r, sshHost, box, sshKey, route.Port, authResult)
	if err != nil {
		return fmt.Errorf("failed to proxy to port %d: %w", route.Port, err)
	}

	return nil
}

// proxyViaSSHPortForward establishes an SSH connection and proxies the HTTP request directly
func (ps *ProxyServer) proxyViaSSHPortForward(w http.ResponseWriter, r *http.Request, sshHost string, box *BoxData, sshKey ssh.Signer, targetPort int, authResult *ProxyAuthResult) error {
	transport := ps.CreateSSHTunnelTransport(sshHost, box, sshKey)

	// Configure the reverse proxy using NewSingleHostReverseProxy
	targetURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", targetPort))
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = transport

	// Customize the director to add user headers and remove auth cookie
	defaultDirector := rp.Director
	rp.Director = func(req *http.Request) {
		defaultDirector(req)
		clearExeDevHeaders(req)
		req.Header.Del("Authorization")
		setForwardedHeaders(req, r)

		// Add user info headers if authenticated
		if authResult != nil {
			req.Header.Set("X-ExeDev-UserID", authResult.UserID)

			userData, exists, err := ps.Data.UserInfo(req.Context(), authResult.UserID)
			if err != nil {
				ps.Lg.ErrorContext(r.Context(), "failed to get user email for proxy headers", "error", err, "user_id", authResult.UserID)
			} else if exists {
				req.Header.Set("X-ExeDev-Email", userData.Email)
			}

			// If authenticated via token,
			// pass the ctx field as a header.
			// This allows the VM's HTTP server to
			// impose its own auth requirements.
			// We pass the raw bytes verbatim to
			// preserve exact formatting.
			if authResult.CtxRaw != nil {
				req.Header.Set("X-ExeDev-Token-Ctx", string(authResult.CtxRaw))
			}
		}

		// Remove login-with-exe-* cookies (port-specific proxy auth cookies)
		nCookies := len(req.Cookies())
		var cookies []*http.Cookie
		for _, c := range req.Cookies() {
			if !strings.HasPrefix(c.Name, "login-with-exe-") {
				cookies = append(cookies, c)
			}
		}
		if len(cookies) != nCookies {
			// Clear all cookies, re-add only the non-auth ones
			req.Header.Del("Cookie")
			for _, c := range cookies {
				req.AddCookie(c)
			}
		}
	}

	// Capture proxy errors and return them to the caller
	var proxyErr error
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		ps.Lg.DebugContext(r.Context(), "HTTP proxy error", "error", err, "target_port", targetPort)
		proxyErr = err
	}

	// Proxy the request
	rp.ServeHTTP(w, r)
	return proxyErr
}

// CreateSSHTunnelTransport creates an HTTP transport that
// tunnels through SSH to a container.
func (ps *ProxyServer) CreateSSHTunnelTransport(sshHost string, box *BoxData, sshKey ssh.Signer) *http.Transport {
	// Build an HTTP transport that dials through SSH
	// to the target on the SSH host.
	// The sshDialer uses the connection pool for SSH connections.
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			cfg := &ssh.ClientConfig{
				User:            box.SSHUser,
				Auth:            []ssh.AuthMethod{ssh.PublicKeys(sshKey)},
				HostKeyCallback: CreateHostKeyCallback(box.Name, box.SSHServerIdentityKey),
				Timeout:         30 * time.Second,
			}
			// Use a deadline that allows for stale
			// connection recovery:
			// - Each dial attempt is bounded to 500ms
			//   (in dialThroughClient);
			// - If first attempt hits a stale connection,
			//   it times out and is removed;
			// - Retries can establish a fresh connection
			//   (up to 3s for SSH dial).
			// Note: "port not bound" still fails fast
			// since connection refused is immediate.
			ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			conn, err := ps.SSHPool.DialWithRetries(ctx, network, addr, sshHost, box.SSHUser, box.SSHPort, sshKey, cfg, []time.Duration{
				50 * time.Millisecond,
				100 * time.Millisecond,
				200 * time.Millisecond,
			})
			if err != nil {
				return nil, fmt.Errorf("SSH dial failed: %w", err)
			}
			return &countingConn{Conn: conn, metrics: ps.HTTPMetrics}, nil
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// clearExeDevHeaders removes any X-ExeDev-* headers from
// the outbound proxy request.
// This prevents clients from spoofing authentication state via
// custom headers and reserves the entire X-ExeDev- namespace for our use.
func clearExeDevHeaders(req *http.Request) {
	if req == nil {
		return
	}
	for key := range req.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-exedev-") {
			req.Header.Del(key)
		}
	}
}

// setForwardedHeaders ensures downstream services are aware
// of the original request context.
// It sets X-Forwarded-Proto, X-Forwarded-Host, and
// X-Forwarded-For so apps can reconstruct the public URL.
func setForwardedHeaders(outgoing, incoming *http.Request) {
	if outgoing == nil || incoming == nil {
		return
	}

	outgoing.Header.Set("X-Forwarded-Proto", getScheme(incoming))

	if host := incoming.Host; host != "" {
		outgoing.Header.Set("X-Forwarded-Host", host)
	}

	existingXFF := strings.TrimSpace(incoming.Header.Get("X-Forwarded-For"))
	clientIP := ClientIPFromRemoteAddr(incoming.RemoteAddr)
	switch {
	case existingXFF != "" && clientIP != "":
		outgoing.Header.Set("X-Forwarded-For", existingXFF+", "+clientIP)
	case existingXFF != "":
		outgoing.Header.Set("X-Forwarded-For", existingXFF)
	case clientIP != "":
		outgoing.Header.Set("X-Forwarded-For", clientIP)
	}
}

// ClientIPFromRemoteAddr returns the host or IP address of a net address.
func ClientIPFromRemoteAddr(addr string) string {
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	if ip := net.ParseIP(addr); ip != nil {
		return ip.String()
	}
	return addr
}

// BoxSSHHost returns the SSH host to use to connect to a box on ctrHost.
func BoxSSHHost(lg *slog.Logger, ctrHost string) string {
	// TODO: maybe this is a url and needs to be stripped.
	// ctrHost is usually tcp://host:port/ of the exelet.
	// The VMs SSH server is mapped to a port (box.sshPort) on the same
	// host as the exelet, so we parse out the host part only.
	if u, err := url.Parse(ctrHost); err == nil && u.Host != "" {
		if host, _, err := net.SplitHostPort(u.Host); err == nil {
			return host
		} else {
			return u.Host
		}
	}
	// This should never happen, but since we're dealing with data
	// from the database, let's avoid crashing for now.
	lg.Error("Box Ctrhost is not a valid URL", "ctrhost", ctrHost)
	return ctrHost
}

// CreateHostKeyCallback creates a proper SSH host key
// validation callback that verifies the presented host key
// against a box's SSH server identity key.
func CreateHostKeyCallback(boxName string, sshServerIdentityKey []byte) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// Ensure we have an SSH server identity key.
		if len(sshServerIdentityKey) == 0 {
			return fmt.Errorf("no SSH server identity key available for box %s", boxName)
		}

		// Parse the server identity private key to
		// extract the public key.
		privKey, err := ssh.ParsePrivateKey(sshServerIdentityKey)
		if err != nil {
			return fmt.Errorf("failed to parse server identity key for box %s: %w", boxName, err)
		}

		// Compare the keys by comparing their marshaled bytes.
		if !bytes.Equal(key.Marshal(), privKey.PublicKey().Marshal()) {
			return fmt.Errorf("host key mismatch for %s: presented key does not match expected key for box %s", hostname, boxName)
		}

		return nil
	}
}

// countingConn wraps a net.Conn to count bytes read and written.
type countingConn struct {
	net.Conn
	metrics *HTTPMetrics
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.metrics.AddProxyBytes("in", n)
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.metrics.AddProxyBytes("out", n)
	}
	return n, err
}
