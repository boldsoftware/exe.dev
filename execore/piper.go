// Piper plugin implements the sshpiper plugin for exed SSH routing
package execore

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/exeweb"
	api "exe.dev/pkg/api/exe/compute/v1"
	"exe.dev/tracing"
	"github.com/tg123/sshpiper/libplugin"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// ProxyKeyMapping stores the mapping between ephemeral proxy keys and original user keys
// This allows us to "round-trip" authentication: when a user connects with their key,
// we generate a temporary proxy key to authenticate to exed, then when exed sees that
// proxy key, we can look up the original user's key.
type ProxyKeyMapping struct {
	OriginalPublicKey []byte    // The user's original public key (SSH wire format)
	LocalAddress      string    // The local IP address the client connected to
	ClientAddr        string    // The client's real remote address
	CreatedAt         time.Time // When this mapping was created (for expiration)
}

// HostKeyMapping stores the expected host key for a connection with expiration
type HostKeyMapping struct {
	HostKey   string    // The expected SSH host key in authorized_keys format
	CreatedAt time.Time // When this mapping was created (for expiration)
}

// piperConnLog accumulates slog attributes during SSH connection handling,
// similar to CommandLog for SSH commands and sloghttp for HTTP requests.
// It is stored in the context via withPiperConnLog/getPiperConnLog.
type piperConnLog struct {
	attrs []slog.Attr
	start time.Time
}

type piperConnLogKey struct{}

func withPiperConnLog(ctx context.Context) (context.Context, *piperConnLog) {
	cl := &piperConnLog{start: time.Now()}
	return context.WithValue(ctx, piperConnLogKey{}, cl), cl
}

func getPiperConnLog(ctx context.Context) *piperConnLog {
	if v, ok := ctx.Value(piperConnLogKey{}).(*piperConnLog); ok {
		return v
	}
	return nil
}

func (cl *piperConnLog) add(attrs ...slog.Attr) {
	cl.attrs = append(cl.attrs, attrs...)
}

// PiperPlugin implements the sshpiper plugin interface
type PiperPlugin struct {
	server      *Server
	exedSSHHost string // host for upstream connections to exed's SSH, e.g. "100.x.y.z"
	exedSSHPort int    // exed's SSH port, usually 2223

	grpcMu   sync.Mutex
	grpcSrv  *grpc.Server
	grpcDone bool

	// proxyKeyMappings maps SSH public key fingerprints of ephemeral proxy keys
	// to the original user's public key. This allows us to:
	// 1. Generate a unique proxy key for each user connection
	// 2. Look up the original user when exed receives the proxy key
	// 3. Expire old mappings to prevent memory leaks
	proxyKeyMappings map[string]*ProxyKeyMapping
	proxyKeyMutex    sync.Mutex

	// keyboardInteractiveShown tracks which connections have already seen the keyboard interactive message
	// This prevents showing the message multiple times when SSH clients retry authentication
	keyboardInteractiveShown map[string]bool
	keyboardInteractiveMutex sync.Mutex

	// expectedHostKeys maps connection IDs to their expected host keys for validation
	expectedHostKeys      map[string]*HostKeyMapping
	expectedHostKeysMutex sync.Mutex
}

// NewPiperPlugin creates a new piper plugin instance
func NewPiperPlugin(server *Server, host string, port int) *PiperPlugin {
	p := &PiperPlugin{
		server:                   server,
		exedSSHHost:              host,
		exedSSHPort:              port,
		proxyKeyMappings:         make(map[string]*ProxyKeyMapping),
		keyboardInteractiveShown: make(map[string]bool),
		expectedHostKeys:         make(map[string]*HostKeyMapping),
	}

	// Start cleanup goroutine to remove expired proxy key mappings
	go p.cleanupExpiredMappings()

	return p
}

// emitPiperConnLog emits the canonical log line for an SSH connection.
func emitPiperConnLog(ctx context.Context, connID, msg string, extra ...slog.Attr) {
	attrs := []any{
		"component", "piper-plugin",
		"conn_id", connID,
	}
	if cl := getPiperConnLog(ctx); cl != nil {
		attrs = append(attrs, "duration", time.Since(cl.start))
		for _, a := range cl.attrs {
			attrs = append(attrs, a.Key, a.Value.Any())
		}
	}
	for _, a := range extra {
		attrs = append(attrs, a.Key, a.Value.Any())
	}
	slog.InfoContext(ctx, msg, attrs...)
}

// storeExpectedHostKeyForConnection stores the expected host key for a connection with timestamp
func (p *PiperPlugin) storeExpectedHostKeyForConnection(connID, hostKey string) {
	p.expectedHostKeysMutex.Lock()
	defer p.expectedHostKeysMutex.Unlock()
	p.expectedHostKeys[connID] = &HostKeyMapping{
		HostKey:   hostKey,
		CreatedAt: time.Now(),
	}
}

// getExpectedHostKeyForConnection retrieves the expected host key for a connection
func (p *PiperPlugin) getExpectedHostKeyForConnection(connID string) (string, bool) {
	p.expectedHostKeysMutex.Lock()
	defer p.expectedHostKeysMutex.Unlock()
	mapping, exists := p.expectedHostKeys[connID]
	if !exists {
		return "", false
	}

	// Check if mapping has expired (5 minutes)
	if time.Since(mapping.CreatedAt) > 5*time.Minute {
		// Don't remove here to avoid lock upgrade, cleanup will handle it
		return "", false
	}

	return mapping.HostKey, true
}

// getServerHostKey retrieves the exed server's host key from the database
func (p *PiperPlugin) getServerHostKey() (string, *string, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()
	row, err := withRxRes0(p.server, ctx, (*exedb.Queries).GetSSHHostKey)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get server host key: %w", err)
	}
	return row.PublicKey, row.CertSig, nil
}

// ListenAndServe starts the sshpiper plugin gRPC server, listening on addr.
func (p *PiperPlugin) ListenAndServe(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", addr, err)
	}
	return p.Serve(lis)
}

// Serve starts the sshpiper plugin gRPC server, listening on ln.
func (p *PiperPlugin) Serve(lis net.Listener) error {
	slog.Debug("Starting sshpiper plugin gRPC server", "component", "piper-plugin", "addr", lis.Addr())
	config := libplugin.SshPiperPluginConfig{
		NextAuthMethodsCallback:     p.handleNextAuthMethods,
		PublicKeyCallback:           p.handlePublicKeyAuth,
		KeyboardInteractiveCallback: p.handleKeyboardInteractive,
		VerifyHostKeyCallback:       p.handleVerifyHostKey,
		BannerCallback:              p.handleBanner,
	}

	s := grpc.NewServer()

	p.grpcMu.Lock()
	if p.grpcDone {
		p.grpcMu.Unlock()
		s.Stop()
		return nil
	}
	p.grpcSrv = s
	p.grpcMu.Unlock()

	// Enable gRPC reflection for service discovery.
	// Use grpcurl to interact with this server: https://github.com/fullstorydev/grpcurl
	// Example: grpcurl -plaintext localhost:2224 list
	reflection.Register(s)

	plugin, err := libplugin.NewFromGrpc(config, s, lis)
	if err != nil {
		return fmt.Errorf("failed to create plugin: %v", err)
	}

	slog.Debug("Starting sshpiper plugin", "component", "piper-plugin", "addr", lis.Addr())
	slog.Debug("Plugin server listening", "component", "piper-plugin", "addr", lis.Addr())

	err = plugin.Serve()
	if err != nil {
		// Don't log "use of closed network connection" as error - it's expected during shutdown
		if !strings.Contains(err.Error(), "use of closed network connection") {
			slog.Error("Plugin server error", "component", "piper-plugin", "error", err)
		}
	}
	return err
}

// Stop gracefully stops the piper plugin gRPC server.
func (p *PiperPlugin) Stop() {
	p.grpcMu.Lock()
	defer p.grpcMu.Unlock()
	if p.grpcDone {
		return
	}
	p.grpcDone = true
	if p.grpcSrv != nil {
		p.grpcSrv.Stop()
	}
}

// handleNextAuthMethods advertises available authentication methods
func (p *PiperPlugin) handleNextAuthMethods(conn libplugin.ConnMetadata) ([]string, error) {
	// slog.Debug("NextAuthMethods request", "component", "piper-plugin", "user", conn.User(), "remote_addr", conn.RemoteAddr())
	// Always offer both publickey and keyboard-interactive
	return []string{"publickey", "keyboard-interactive"}, nil
}

// supportAccessPrefix is the username prefix for support access.
// Usage: ssh support+vmname@exe.cloud
const supportAccessPrefix = "support+"

// handleBanner returns an SSH banner shown before authentication.
// We use this to show a privacy warning for support access attempts.
func (p *PiperPlugin) handleBanner(conn libplugin.ConnMetadata) string {
	username := conn.User()
	if strings.HasPrefix(username, supportAccessPrefix) {
		return `

╔══════════════════════════════════════════════════════════════════════════╗
║                           EXE.DEV SUPPORT ACCESS                         ║
╠══════════════════════════════════════════════════════════════════════════╣
║  You are connecting to another user's VM.                                ║
║                                                                          ║
║  Respect their privacy.                                                  ║
╚══════════════════════════════════════════════════════════════════════════╝


`
	}
	return ""
}

// handleKeyboardInteractive provides a user-friendly message when public key auth fails
//
// OMG, let me tell you about SSH. We want to require the user to come with a key.
// If they come with a key, great, we'll register it, and so forth. If they don't,
// we need to send them an error message. SSH doesn't have a way to send them an error
// message that I could find.
//
// I tried using a Banner, but that shows up before auth, and it's noisy in the common
// case of actually using the service.
//
// I tried using "none" auth. That seemed great: say that you like public-key first and then
// none, and have a separate SSH server that just accepts none, sends a message, and closes.
// But, of course, SSH clients always try none first, and if that works, you always get the
// message.
//
// Anyway, that's why we're at keyboard interactive.
func (p *PiperPlugin) handleKeyboardInteractive(conn libplugin.ConnMetadata, client libplugin.KeyboardInteractiveChallenge) (*libplugin.Upstream, error) {
	// Use connection's unique ID to track if we've already shown the message
	connID := conn.UniqueID()

	p.keyboardInteractiveMutex.Lock()
	alreadyShown := p.keyboardInteractiveShown[connID]
	if !alreadyShown {
		p.keyboardInteractiveShown[connID] = true
	}
	p.keyboardInteractiveMutex.Unlock()

	if !alreadyShown {
		// First time - send helpful message about setting up SSH keys
		message := "SSH keys are required to access exe.dev.\nPlease create a key with 'ssh-keygen -t ed25519' and try again.\n\nPress Enter to close this connection."

		// Special case: support access attempt failed
		if supportBoxName, isSupport := strings.CutPrefix(conn.User(), supportAccessPrefix); isSupport {
			message = fmt.Sprintf("Support access denied for VM %q.\n\nEither:\n- You don't have support privileges, or\n- The VM doesn't have support access enabled\n\nPress Enter to close this connection.", supportBoxName)
		}

		_, err := client("", message, "", false)
		if err != nil {
			return nil, err
		}
	}

	// Always return nil to deny access
	return nil, fmt.Errorf("SSH public key authentication is required")
}

// handlePublicKeyAuth handles public key authentication and routing decisions
func (p *PiperPlugin) handlePublicKeyAuth(conn libplugin.ConnMetadata, key []byte) (*libplugin.Upstream, error) {
	ctx := tracing.ContextWithTraceID(context.Background(), tracing.GenerateTraceID())
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	connID := conn.UniqueID()
	ctx, cl := withPiperConnLog(ctx)

	username := conn.User()
	localAddress := cmp.Or(domz.StripPort(conn.LocalAddress()), "127.0.0.1")
	remoteAddr := conn.RemoteAddr()
	cl.add(
		slog.String("username", username),
		slog.String("remote_addr", remoteAddr),
		slog.String("local_address", localAddress),
	)

	// Check if key is empty or nil - this happens when client has no keys configured
	if len(key) == 0 {
		return nil, fmt.Errorf("no public key provided")
	}

	// Parse the provided key
	pubKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %v", err)
	}

	keyFingerprint := p.server.GetPublicKeyFingerprint(pubKey)
	cl.add(slog.String("key_fingerprint", keyFingerprint))

	// Get user info by public key directly
	userID, err := p.server.getUserIDByPublicKey(ctx, pubKey)
	if err != nil {
		slog.WarnContext(ctx, "Database error checking SSH key", "component", "piper-plugin", "key_fingerprint", keyFingerprint, "error", err)
	}
	cl.add(slog.String("user_id", userID))

	// Check for support access: ssh support+vmname@exe.cloud
	if supportBoxName, isSupport := strings.CutPrefix(username, supportAccessPrefix); isSupport {
		box := p.server.FindBoxForExeSudoer(ctx, userID, supportBoxName)
		if box == nil {
			slog.WarnContext(ctx, "support access denied", "component", "piper-plugin", "vm_name", supportBoxName, "user_id", userID)
			return nil, fmt.Errorf("support access denied: either you don't have support privileges, or VM %q doesn't have support access enabled", supportBoxName)
		}
		slog.InfoContext(ctx, "support access granted", "component", "piper-plugin", "vm_name", box.Name, "vm_id", box.ID, "support_user_id", userID)
		cl.add(slog.Bool("support_access", true))
		return p.handleBoxAccess(ctx, box, userID, connID)
	}

	// In test/local environments, it's useful to be able to access VMs by username,
	// so we don't have to do DNS nonsense.
	// We used to enable this everywhere, but it was an evergreen source of user confusion.
	if p.server.env.SSHCommandUsesAt && username != "" {
		if box := p.server.FindBoxByNameForUser(ctx, userID, username); box != nil {
			cl.add(slog.String("route", "by_name"))
			return p.handleBoxAccess(ctx, box, userID, connID)
		}
	}

	// IP-based box routing (like SNI but for SSH):
	// If `ssh vmname.exe.cloud` resolves to a shard IP (127.21.0.X), then look up the box by that shard/user combo.
	// This makes `ssh vmname.exe.cloud` work like `ssh vmname@exe.cloud`.
	// If this fails, try team owner fallback, then continue on with normal routing.
	if box := p.server.FindBoxByIPShard(ctx, userID, localAddress); box != nil {
		cl.add(slog.String("route", "by_ip_shard"))
		return p.handleBoxAccess(ctx, box, userID, connID)
	}

	// Team owner fallback: if user is a team owner, try to find a team member's box on this shard
	if box := p.server.FindTeamBoxByIPShard(ctx, userID, localAddress); box != nil {
		cl.add(slog.String("route", "by_team_ip_shard"))
		return p.handleBoxAccess(ctx, box, userID, connID)
	}

	// Team SSH sharing: if box owner has enabled team SSH, route team members
	if box := p.server.FindTeamSSHSharedBoxByIPShard(ctx, userID, localAddress); box != nil {
		cl.add(slog.String("route", "by_team_ssh_share"))
		return p.handleBoxAccess(ctx, box, userID, connID)
	}

	// Team SSH sharing by username: ssh boxname@exe.xyz
	// Not gated by SSHCommandUsesAt — scoped to team SSH shared boxes only.
	if box := p.server.FindTeamSSHSharedBoxByName(ctx, userID, username); box != nil {
		cl.add(slog.String("route", "by_team_ssh_name"))
		return p.handleBoxAccess(ctx, box, userID, connID)
	}

	// For all other cases (interactive shell, registration, etc.),
	// route to exed directly using ephemeral proxy authentication
	//
	// EPHEMERAL PROXY KEY APPROACH:
	// 1. Generate a unique, temporary private key for this connection
	// 2. Store a mapping: proxy_key_fingerprint -> original_user_public_key
	// 3. Send proxy private key to exed for authentication
	// 4. When exed sees the proxy key, it can look up the original user's key
	// 5. Mappings expire after a few minutes to prevent memory leaks
	clientAddr := conn.RemoteAddr()
	proxyPrivateKeyPEM, _, err := p.generateEphemeralProxyKey(ctx, key, localAddress, clientAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ephemeral proxy key: %v", err)
	}

	// This is a CANONICAL LOG LINE for SSH connections routed to the exed shell.
	emitPiperConnLog(ctx, connID, "SSH routing to exed shell",
		slog.String("log_type", "ssh_proxy_auth"),
	)

	return &libplugin.Upstream{
		Host:          p.exedSSHHost,
		Port:          int32(p.exedSSHPort),
		UserName:      username, // Use original username, not encoded
		IgnoreHostKey: false,    // Host key validation is handled by VerifyHostKeyCallback
		Auth:          libplugin.CreatePrivateKeyAuth([]byte(proxyPrivateKeyPEM)),
	}, nil
}

// handleBoxAccess sets up routing to a specific box container
func (p *PiperPlugin) handleBoxAccess(ctx context.Context, box *exedb.Box, userID, connID string) (*libplugin.Upstream, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cl := getPiperConnLog(ctx)
	cl.add(
		slog.String("vm_name", box.Name),
		slog.Int("vm_id", box.ID),
		slog.String("owner_user_id", box.CreatedByUserID),
	)

	// Track unique VM logins
	if p.server.hllTracker != nil && userID != "" {
		p.server.hllTracker.NoteEvent("vm-login", userID)
	}

	if box.ContainerID == nil {
		return nil, fmt.Errorf("VM %s is not running", box.Name)
	}
	cl.add(slog.String("container_id", *box.ContainerID))

	// Check if instance is actually running via exelet
	exeletClient := p.server.getExeletClient(box.Ctrhost)
	if exeletClient != nil {
		instanceResp, err := exeletClient.client.GetInstance(ctx, &api.GetInstanceRequest{
			ID: *box.ContainerID,
		})
		if err != nil {
			traceID := tracing.TraceIDFromContext(ctx)
			slog.ErrorContext(ctx, "piper-plugin GetInstance failed (exelet unreachable?)",
				"vm_name", box.Name,
				"vm_id", box.ID,
				"container_id", *box.ContainerID,
				"ctrhost", box.Ctrhost,
				"user_id", userID,
				"conn_id", connID,
				"error", err,
			)
			return nil, fmt.Errorf("internal error (trace: %s)", traceID)
		}
		if instanceResp.Instance != nil {
			cl.add(slog.String("instance_state", instanceResp.Instance.State.String()))
			// If instance is not running, route to exed for error display
			if instanceResp.Instance.State != api.VMState_RUNNING && instanceResp.Instance.State != api.VMState_STARTING {
				proxyPrivateKeyPEM, _, err := p.generateEphemeralProxyKey(ctx, nil, "127.0.0.1", "127.0.0.1")
				if err != nil {
					return nil, fmt.Errorf("failed to generate proxy key: %v", err)
				}

				specialUsername := fmt.Sprintf("container-logs:%s:%s:%s", box.CreatedByUserID, *box.ContainerID, box.Name)

				// Emit canonical line even for non-running instance route
				emitPiperConnLog(ctx, connID, "SSH Connection to VM",
					slog.String("log_type", "vm-ssh-connection"),
					slog.String("ctrhost", box.Ctrhost),
				)

				return &libplugin.Upstream{
					Host:          p.exedSSHHost,
					Port:          int32(p.exedSSHPort),
					UserName:      specialUsername,
					IgnoreHostKey: false,
					Auth:          libplugin.CreatePrivateKeyAuth([]byte(proxyPrivateKeyPEM)),
				}, nil
			}
		}
	}

	// Get SSH connection details from the database
	sshDetails, err := p.server.GetBoxSSHDetails(ctx, box.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get SSH details for VM %s: %v", box.Name, err)
	}
	if sshDetails.HostKey == "" {
		return nil, fmt.Errorf("VM %s has no stored host key", box.Name)
	}
	host := exeweb.BoxSSHHost(slog.Default(), box.Ctrhost)
	port := sshDetails.Port

	// This is a CANONICAL LOG LINE: wide event for SSH connections to VMs.
	emitPiperConnLog(ctx, connID, "SSH Connection to VM",
		slog.String("log_type", "vm-ssh-connection"),
		slog.Int("port", int(port)),
		slog.String("ctrhost", box.Ctrhost),
		slog.String("ssh_user", sshDetails.User),
		slog.String("box_host", host),
	)

	p.storeExpectedHostKeyForConnection(connID, sshDetails.HostKey)
	return &libplugin.Upstream{
		Host:          host, // Container host from ctrhost (direct via vzNAT in dev)
		Port:          int32(port),
		UserName:      sshDetails.User, // Use the user from the Docker image USER directive
		IgnoreHostKey: false,           // Host key validation is handled by VerifyHostKeyCallback
		Auth:          libplugin.CreatePrivateKeyAuth([]byte(sshDetails.PrivateKey)),
	}, nil
}

// generateEphemeralProxyKey creates a temporary ED25519 key pair for this specific connection.
// It stores a mapping from the proxy key's fingerprint to the user's original public key,
// allowing exed to later identify the original user when it sees the proxy key.
//
// Returns: (privateKeyPEM, proxyKeyFingerprint, error)
func (p *PiperPlugin) generateEphemeralProxyKey(ctx context.Context, originalUserPublicKey []byte, localAddress, clientAddr string) (string, string, error) {
	// Generate a new ED25519 private key for this connection
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate ED25519 key: %v", err)
	}

	// Convert to OpenSSH private key format
	privateKeyPEMBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal private key: %v", err)
	}
	privateKeyPEMBytes := pem.EncodeToMemory(privateKeyPEMBlock)
	privateKeyPEM := string(privateKeyPEMBytes)

	// Get the public key and its fingerprint
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to create signer: %v", err)
	}
	proxyFingerprint := p.server.GetPublicKeyFingerprint(signer.PublicKey())

	// Store the mapping: proxy key fingerprint -> original user public key + addresses
	p.proxyKeyMutex.Lock()
	p.proxyKeyMappings[proxyFingerprint] = &ProxyKeyMapping{
		OriginalPublicKey: originalUserPublicKey,
		LocalAddress:      localAddress,
		ClientAddr:        clientAddr,
		CreatedAt:         time.Now(),
	}
	p.proxyKeyMutex.Unlock()

	return privateKeyPEM, proxyFingerprint, nil
}

// lookupOriginalUserKey retrieves the original user's public key, local address, and client address from an ephemeral proxy key.
// This is called by exed when it sees a proxy key and needs to identify the original user.
func (p *PiperPlugin) lookupOriginalUserKey(proxyKeyFingerprint string) (originalKey []byte, localAddr, clientAddr string, exists bool) {
	p.proxyKeyMutex.Lock()
	mapping, ok := p.proxyKeyMappings[proxyKeyFingerprint]
	p.proxyKeyMutex.Unlock()

	if !ok {
		return nil, "", "", false
	}

	// Check if mapping has expired (15 minutes)
	if time.Since(mapping.CreatedAt) > 15*time.Minute {
		// Remove expired mapping
		p.proxyKeyMutex.Lock()
		delete(p.proxyKeyMappings, proxyKeyFingerprint)
		p.proxyKeyMutex.Unlock()
		return nil, "", "", false
	}

	return mapping.OriginalPublicKey, mapping.LocalAddress, mapping.ClientAddr, true
}

// handleVerifyHostKey validates the host key for container connections
// It uses the connection-scoped expected host key stored during connection setup
func (p *PiperPlugin) handleVerifyHostKey(conn libplugin.ConnMetadata, hostname, netaddr string, key []byte) error {
	ctx := tracing.ContextWithTraceID(context.Background(), tracing.GenerateTraceID())

	// Convert the received key to SSH authorized_keys format for comparison
	pubKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		return fmt.Errorf("invalid host key format: %w", err)
	}
	receivedKey := string(ssh.MarshalAuthorizedKey(pubKey))

	// Check if this is a box connection by looking up stored expected keys
	connID := conn.UniqueID()
	expectedKey, found := p.getExpectedHostKeyForConnection(connID)

	if found {
		// This is a box connection with a stored expected key
		if strings.TrimSpace(expectedKey) == strings.TrimSpace(receivedKey) {
			return nil
		}
		// Key mismatch for box connection
		slog.WarnContext(ctx, "Host key validation failed - box key mismatch",
			"component", "piper-plugin",
			"conn_id", connID, "hostname", hostname,
			"expected_key", expectedKey, "received_key", receivedKey,
		)
		return fmt.Errorf("host key validation failed for %s", hostname)
	}

	// No stored expected key - this is a connection to the main exed server.
	// Validate against the server's host key from the database.
	serverHostKey, serverCert, err := p.getServerHostKey()
	if err != nil {
		slog.WarnContext(ctx, "Failed to get server host key for verification", "component", "piper-plugin", "error", err)
		return fmt.Errorf("host key validation failed for %s", hostname)
	}

	trimmedReceived := strings.TrimSpace(receivedKey)
	trimmedExpectedKey := strings.TrimSpace(serverHostKey)

	if serverCert != nil {
		trimmedExpectedCert := strings.TrimSpace(*serverCert)
		if trimmedExpectedCert != "" {
			if trimmedExpectedCert == trimmedReceived {
				return nil
			}

			if cert, ok := pubKey.(*ssh.Certificate); ok {
				expectedKeyPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmedExpectedKey))
				if err != nil {
					expectedKeyPub, _, _, _, err = ssh.ParseAuthorizedKey([]byte(trimmedExpectedKey + "\n"))
				}
				if err == nil && bytes.Equal(cert.Key.Marshal(), expectedKeyPub.Marshal()) {
					return nil
				}
			}
		}
	}

	if trimmedExpectedKey == trimmedReceived {
		return nil
	}

	// Neither box key nor server key matched
	slog.WarnContext(ctx, "Host key validation failed - no matching key found",
		"component", "piper-plugin", "conn_id", connID, "hostname", hostname)
	return fmt.Errorf("host key validation failed for %s", hostname)
} // cleanupExpiredMappings runs periodically to remove expired proxy key mappings and keyboard interactive tracking
func (p *PiperPlugin) cleanupExpiredMappings() {
	ticker := time.NewTicker(5 * time.Minute) // Run every 5 minutes
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		expiredKeys := make([]string, 0)

		p.proxyKeyMutex.Lock()
		for fingerprint, mapping := range p.proxyKeyMappings {
			if now.Sub(mapping.CreatedAt) > 15*time.Minute {
				expiredKeys = append(expiredKeys, fingerprint)
			}
		}
		for _, key := range expiredKeys {
			delete(p.proxyKeyMappings, key)
		}
		p.proxyKeyMutex.Unlock()

		// Clean up keyboard interactive tracking (clear entire map periodically)
		// This is safe since it just tracks whether we've shown the message once per connection
		p.keyboardInteractiveMutex.Lock()
		keyboardCount := len(p.keyboardInteractiveShown)
		p.keyboardInteractiveShown = make(map[string]bool) // Clear entire map
		p.keyboardInteractiveMutex.Unlock()

		// Clean up expired host keys (remove ones older than 5 minutes)
		expiredHostKeys := make([]string, 0)
		p.expectedHostKeysMutex.Lock()
		for connID, mapping := range p.expectedHostKeys {
			if now.Sub(mapping.CreatedAt) > 5*time.Minute {
				expiredHostKeys = append(expiredHostKeys, connID)
			}
		}
		for _, connID := range expiredHostKeys {
			delete(p.expectedHostKeys, connID)
		}
		hostKeyCount := len(expiredHostKeys)
		p.expectedHostKeysMutex.Unlock()

		if len(expiredKeys) > 0 || keyboardCount > 0 || hostKeyCount > 0 {
			slog.Debug("Cleaned up expired mappings", "component", "piper-plugin", "proxy_keys", len(expiredKeys), "keyboard_connections", keyboardCount, "host_keys", hostKeyCount)
		}
	}
}
