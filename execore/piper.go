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

// PiperPlugin implements the sshpiper plugin interface
type PiperPlugin struct {
	server      *Server
	exedSSHPort int // exed's SSH port, usually 2223

	// proxyKeyMappings maps SSH public key fingerprints of ephemeral proxy keys
	// to the original user's public key. This allows us to:
	// 1. Generate a unique proxy key for each user connection
	// 2. Look up the original user when exed receives the proxy key
	// 3. Expire old mappings to prevent memory leaks
	proxyKeyMappings map[string]*ProxyKeyMapping
	// RWMutex allows concurrent reads while protecting writes
	proxyKeyMutex sync.RWMutex

	// keyboardInteractiveShown tracks which connections have already seen the keyboard interactive message
	// This prevents showing the message multiple times when SSH clients retry authentication
	keyboardInteractiveShown map[string]bool
	keyboardInteractiveMutex sync.RWMutex

	// expectedHostKeys maps connection IDs to their expected host keys for validation
	expectedHostKeys      map[string]*HostKeyMapping
	expectedHostKeysMutex sync.RWMutex
}

// NewPiperPlugin creates a new piper plugin instance
func NewPiperPlugin(server *Server, port int) *PiperPlugin {
	p := &PiperPlugin{
		server:                   server,
		exedSSHPort:              port,
		proxyKeyMappings:         make(map[string]*ProxyKeyMapping),
		keyboardInteractiveShown: make(map[string]bool),
		expectedHostKeys:         make(map[string]*HostKeyMapping),
	}

	// Start cleanup goroutine to remove expired proxy key mappings
	go p.cleanupExpiredMappings()

	return p
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
	p.expectedHostKeysMutex.RLock()
	defer p.expectedHostKeysMutex.RUnlock()
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
		slog.Error("Plugin server error", "component", "piper-plugin", "error", err)
	}
	return err
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
	ctx := tracing.ContextWithTraceID(context.Background(), tracing.GenerateTraceID())
	slog.DebugContext(ctx, "Keyboard interactive auth request", "component", "piper-plugin", "user", conn.User(), "remote_addr", conn.RemoteAddr())

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
			slog.DebugContext(ctx, "Keyboard interactive challenge failed", "component", "piper-plugin", "error", err)
			return nil, err
		}
	} else {
		// Already shown message - just fail silently to avoid repeating
		slog.DebugContext(ctx, "Keyboard interactive auth retry - skipping message display", "component", "piper-plugin", "conn_id", connID)
	}

	// Always return nil to deny access
	return nil, fmt.Errorf("SSH public key authentication is required")
}

// handlePublicKeyAuth handles public key authentication and routing decisions
func (p *PiperPlugin) handlePublicKeyAuth(conn libplugin.ConnMetadata, key []byte) (*libplugin.Upstream, error) {
	ctx := tracing.ContextWithTraceID(context.Background(), tracing.GenerateTraceID())
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	slog.DebugContext(ctx, "piper plugin public key auth request", "component", "piper-plugin", "user", conn.User(), "remote_addr", conn.RemoteAddr())

	// Check if key is empty or nil - this happens when client has no keys configured
	if len(key) == 0 {
		slog.DebugContext(ctx, "No public key provided", "component", "piper-plugin", "user", conn.User())
		return nil, fmt.Errorf("no public key provided")
	}

	// Parse the provided key
	pubKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		slog.DebugContext(ctx, "Failed to parse public key", "component", "piper-plugin", "error", err)
		return nil, fmt.Errorf("failed to parse public key: %v", err)
	}

	// Get user info by public key directly
	userID, err := p.server.getUserIDByPublicKey(ctx, pubKey)
	if err != nil {
		slog.DebugContext(ctx, "Database error checking SSH key", "component", "piper-plugin", "public_key", pubKey, "error", err)
	}
	slog.DebugContext(ctx, "looked up user for ssh key", "component", "piper-plugin", "public_key", pubKey, "user_id", userID)

	username := conn.User()
	localAddress := cmp.Or(domz.StripPort(conn.LocalAddress()), "127.0.0.1")
	slog.DebugContext(ctx, "piper public key auth user status", "component", "piper-plugin", "username", username, "user_id", userID, "local_address", localAddress)

	// Check for support access: ssh support+vmname@exe.cloud
	if supportBoxName, isSupport := strings.CutPrefix(username, supportAccessPrefix); isSupport {
		slog.InfoContext(ctx, "piper public key auth: support access attempt", "component", "piper-plugin", "box_name", supportBoxName, "user_id", userID)
		box := p.server.FindBoxForExeSudoer(ctx, userID, supportBoxName)
		if box == nil {
			slog.WarnContext(ctx, "piper public key auth: support access denied", "component", "piper-plugin", "box_name", supportBoxName, "user_id", userID)
			return nil, fmt.Errorf("support access denied: either you don't have support privileges, or VM %q doesn't have support access enabled", supportBoxName)
		}
		slog.InfoContext(ctx, "piper public key auth: support access granted", "component", "piper-plugin", "box_name", box.Name, "box_id", box.ID, "support_user_id", userID)
		return p.handleBoxAccess(ctx, box, userID, conn.UniqueID())
	}

	// In test/local environments, it's useful to be able to access VMs by username,
	// so we don't have to do DNS nonsense.
	// We used to enable this everywhere, but it was an evergreen source of user confusion.
	if p.server.env.SSHCommandUsesAt && username != "" {
		if box := p.server.FindBoxByNameForUser(ctx, userID, username); box != nil {
			slog.InfoContext(ctx, "piper public key auth found box by name, routing to box", "component", "piper-plugin", "box_name", box.Name, "box_id", box.ID, "ctrhost", box.Ctrhost, "port", box.SSHPort)
			return p.handleBoxAccess(ctx, box, userID, conn.UniqueID())
		}
		slog.InfoContext(ctx, "No box found with name", "component", "piper-plugin", "username", username, "user_id", userID)
	}

	// IP-based box routing (like SNI but for SSH):
	// If `ssh vmname.exe.cloud` resolves to a shard IP (127.21.0.X), then look up the box by that shard/user combo.
	// This makes `ssh vmname.exe.cloud` work like `ssh vmname@exe.cloud`.
	// If this fails, continue on with normal routing.
	slog.InfoContext(ctx, "piper public key auth checking for box by shard", "component", "piper-plugin", "user_id", userID, "local_address", localAddress)
	if box := p.server.FindBoxByIPShard(ctx, userID, localAddress); box != nil {
		slog.InfoContext(ctx, "piper pk auth found box by IP shard, routing to box", "component", "piper-plugin", "box_name", box.Name, "box_id", box.ID, "local_address", localAddress)
		return p.handleBoxAccess(ctx, box, userID, conn.UniqueID())
	}

	// For all other cases (interactive shell, registration, etc.),
	// route to exed directly using ephemeral proxy authentication
	slog.DebugContext(ctx, "Routing to exed shell", "port", p.exedSSHPort, "component", "piper-plugin")
	slog.DebugContext(ctx, "User's public key length", "component", "piper-plugin", "key_length_bytes", len(key))

	// EPHEMERAL PROXY KEY APPROACH:
	// 1. Generate a unique, temporary private key for this connection
	// 2. Store a mapping: proxy_key_fingerprint -> original_user_public_key
	// 3. Send proxy private key to exed for authentication
	// 4. When exed sees the proxy key, it can look up the original user's key
	// 5. Mappings expire after a few minutes to prevent memory leaks

	clientAddr := conn.RemoteAddr()
	proxyPrivateKeyPEM, proxyFingerprint, err := p.generateEphemeralProxyKey(ctx, key, localAddress, clientAddr)
	if err != nil {
		slog.DebugContext(ctx, "Failed to generate ephemeral proxy key", "component", "piper-plugin", "error", err)
		return nil, fmt.Errorf("failed to generate ephemeral proxy key: %v", err)
	}

	slog.DebugContext(ctx, "Generated ephemeral proxy key with fingerprint", "component", "piper-plugin", "proxy_fingerprint", proxyFingerprint)

	upstream := &libplugin.Upstream{
		Host:     "127.0.0.1", // Use explicit IPv4 instead of localhost
		Port:     int32(p.exedSSHPort),
		UserName: username, // Use original username, not encoded
		// Host key validation is handled by VerifyHostKeyCallback
		IgnoreHostKey: false, // Enable host key validation
		Auth:          libplugin.CreatePrivateKeyAuth([]byte(proxyPrivateKeyPEM)),
	}

	// slog.Debug("Returning upstream config", "component", "piper-plugin", "host", upstream.Host, "port", upstream.Port, "username", upstream.UserName, "auth_type", "PrivateKey")
	// slog.Debug("Private key length", "component", "piper-plugin", "key_length_bytes", len(proxyPrivateKeyPEM), "key_preview", proxyPrivateKeyPEM[:50])

	return upstream, nil
}

// handleBoxAccess sets up routing to a specific box container
func (p *PiperPlugin) handleBoxAccess(ctx context.Context, box *exedb.Box, userID, connID string) (*libplugin.Upstream, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	slog.DebugContext(ctx, "handleBoxAccess for box", "component", "piper-plugin", "box_name", box.Name, "box_id", box.ID, "user_id", userID, "conn_id", connID)

	// Track unique VM logins
	if p.server.hllTracker != nil && userID != "" {
		p.server.hllTracker.NoteEvent("vm-login", userID)
	}

	if box.ContainerID == nil {
		slog.DebugContext(ctx, "Box has no container ID", "component", "piper-plugin", "box_name", box.Name)
		return nil, fmt.Errorf("VM %s is not running", box.Name)
	}

	// Check if instance is actually running via exelet
	exeletClient := p.server.getExeletClient(box.Ctrhost)
	if exeletClient != nil {
		instanceResp, err := exeletClient.client.GetInstance(ctx, &api.GetInstanceRequest{
			ID: *box.ContainerID,
		})
		if err != nil {
			traceID := tracing.TraceIDFromContext(ctx)
			slog.ErrorContext(ctx, "piper-plugin GetInstance failed (exelet unreachable?)",
				"box_name", box.Name,
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
			slog.InfoContext(ctx, "piper-plugin instance status check",
				"box_name", box.Name,
				"container_id", *box.ContainerID,
				"state", instanceResp.Instance.State.String(),
			)
			// If instance is not running, route to exed for error display
			if instanceResp.Instance.State != api.VMState_RUNNING && instanceResp.Instance.State != api.VMState_STARTING {
				slog.InfoContext(ctx, "Instance not running, routing to exed for error display",
					"component", "piper-plugin", "box_name", box.Name, "state", instanceResp.Instance.State.String())

				proxyPrivateKeyPEM, _, err := p.generateEphemeralProxyKey(ctx, nil, "127.0.0.1", "127.0.0.1")
				if err != nil {
					return nil, fmt.Errorf("failed to generate proxy key: %v", err)
				}

				specialUsername := fmt.Sprintf("container-logs:%s:%s:%s", box.CreatedByUserID, *box.ContainerID, box.Name)

				return &libplugin.Upstream{
					Host:          "127.0.0.1",
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
		slog.DebugContext(ctx, "Failed to get SSH details for box", "component", "piper-plugin", "box_name", box.Name, "error", err)
		return nil, fmt.Errorf("failed to get SSH details for VM %s: %v", box.Name, err)
	}
	if sshDetails.HostKey == "" {
		slog.DebugContext(ctx, "Box has no stored host key", "component", "piper-plugin", "box_name", box.Name)
		return nil, fmt.Errorf("VM %s has no stored host key", box.Name)
	}
	host := box.SSHHost()
	port := sshDetails.Port
	// This is a CANONICAL LOG LINE, in the sense that this is a wide event to tell us about SSH connections.
	slog.InfoContext(ctx, "SSH Connection to VM",
		"user_id", userID,
		"conn_id", connID,
		"component", "piper-plugin",
		"box_name", box.Name,
		"log_type", "vm-ssh-connection",
		"port", port,
		"ctrhost", sshDetails.Ctrhost,
		"ssh_user", sshDetails.User,
		"box_host", host,
	)
	p.storeExpectedHostKeyForConnection(connID, sshDetails.HostKey)
	return &libplugin.Upstream{
		Host:     host, // Container host from ctrhost (direct via vzNAT in dev)
		Port:     int32(port),
		UserName: sshDetails.User, // Use the user from the Docker image USER directive
		// Host key validation is handled by VerifyHostKeyCallback
		IgnoreHostKey: false, // Enable host key validation
		Auth:          libplugin.CreatePrivateKeyAuth([]byte(sshDetails.PrivateKey)),
	}, nil
}

// generateEphemeralProxyKey creates a temporary RSA key pair for this specific connection.
// It stores a mapping from the proxy key's fingerprint to the user's original public key,
// allowing exed to later identify the original user when it sees the proxy key.
//
// Returns: (privateKeyPEM, proxyKeyFingerprint, error)
func (p *PiperPlugin) generateEphemeralProxyKey(ctx context.Context, originalUserPublicKey []byte, localAddress, clientAddr string) (string, string, error) {
	// Generate a new ED25519 private key for this connection (simpler and more reliable than RSA)
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
	slog.DebugContext(ctx, "Generated proxy key", "component", "piper-plugin", "key_type", signer.PublicKey().Type(), "fingerprint_preview", proxyFingerprint[:16])

	// Validate the generated key by trying to parse it again
	if _, err := ssh.ParsePrivateKey(privateKeyPEMBytes); err != nil {
		slog.ErrorContext(ctx, "Generated private key is invalid", "component", "piper-plugin", "error", err)
		return "", "", fmt.Errorf("generated invalid private key: %v", err)
	}
	slog.DebugContext(ctx, "Private key validation successful", "component", "piper-plugin")

	// Store the mapping: proxy key fingerprint -> original user public key + addresses
	p.proxyKeyMutex.Lock()
	p.proxyKeyMappings[proxyFingerprint] = &ProxyKeyMapping{
		OriginalPublicKey: originalUserPublicKey,
		LocalAddress:      localAddress,
		ClientAddr:        clientAddr,
		CreatedAt:         time.Now(),
	}
	p.proxyKeyMutex.Unlock()

	slog.DebugContext(ctx, "Stored ephemeral proxy mapping", "component", "piper-plugin", "proxy_fingerprint", proxyFingerprint, "user_key_length_bytes", len(originalUserPublicKey))

	return privateKeyPEM, proxyFingerprint, nil
}

// lookupOriginalUserKey retrieves the original user's public key, local address, and client address from an ephemeral proxy key.
// This is called by exed when it sees a proxy key and needs to identify the original user.
func (p *PiperPlugin) lookupOriginalUserKey(proxyKeyFingerprint string) (originalKey []byte, localAddr, clientAddr string, exists bool) {
	p.proxyKeyMutex.RLock()
	mapping, ok := p.proxyKeyMappings[proxyKeyFingerprint]
	p.proxyKeyMutex.RUnlock()

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
	slog.DebugContext(ctx, "VerifyHostKey called", "component", "piper-plugin", "hostname", hostname, "netaddr", netaddr, "key_length", len(key))

	// Convert the received key to SSH authorized_keys format for comparison
	pubKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		slog.DebugContext(ctx, "Failed to parse received host key", "component", "piper-plugin", "hostname", hostname, "error", err)
		return fmt.Errorf("invalid host key format: %w", err)
	}
	receivedKey := string(ssh.MarshalAuthorizedKey(pubKey))
	slog.DebugContext(ctx, "Received host key", "component", "piper-plugin", "hostname", hostname, "received_key", receivedKey[:50]+"...")

	// Check if this is a box connection by looking up stored expected keys
	connID := conn.UniqueID()
	expectedKey, found := p.getExpectedHostKeyForConnection(connID)
	slog.DebugContext(ctx, "Expected key lookup", "component", "piper-plugin", "conn_id", connID, "found", found)

	if found {
		// This is a box connection with a stored expected key
		if strings.TrimSpace(expectedKey) == strings.TrimSpace(receivedKey) {
			slog.DebugContext(ctx, "Host key validation successful for box",
				"component", "piper-plugin",
				"conn_id", connID, "hostname", hostname,
			)
			return nil
		}
		// Key mismatch for box connection
		slog.DebugContext(ctx, "Host key validation failed - box key mismatch",
			"component", "piper-plugin",
			"conn_id", connID, "hostname", hostname,
			"expected_key", expectedKey, "received_key", receivedKey,
		)
		return fmt.Errorf("host key validation failed for %s", hostname)
	}

	// No stored expected key - this is a connection to the main exed server
	// Validate against the server's host key from the database
	serverHostKey, serverCert, err := p.getServerHostKey()
	if err != nil {
		slog.DebugContext(ctx, "Failed to get server host key", "component", "piper-plugin", "error", err)
		return fmt.Errorf("host key validation failed for %s", hostname)
	}
	slog.DebugContext(ctx, "Got server host key", "component", "piper-plugin", "hostname", hostname, "server_key", serverHostKey[:50]+"...")

	trimmedReceived := strings.TrimSpace(receivedKey)
	trimmedExpectedKey := strings.TrimSpace(serverHostKey)

	if serverCert != nil {
		trimmedExpectedCert := strings.TrimSpace(*serverCert)
		if trimmedExpectedCert != "" {
			if trimmedExpectedCert == trimmedReceived {
				slog.DebugContext(ctx, "Host cert validation successful for exed server (cert match)",
					"component", "piper-plugin", "hostname", hostname)
				return nil
			}

			if cert, ok := pubKey.(*ssh.Certificate); ok {
				expectedKeyPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmedExpectedKey))
				if err != nil {
					expectedKeyPub, _, _, _, err = ssh.ParseAuthorizedKey([]byte(trimmedExpectedKey + "\n"))
				}
				if err == nil && bytes.Equal(cert.Key.Marshal(), expectedKeyPub.Marshal()) {
					slog.DebugContext(ctx, "Host cert validation successful for exed server (underlying key match)",
						"component", "piper-plugin", "hostname", hostname, "cert_key_id", cert.KeyId)
					return nil
				}
			}
		}
	}

	if trimmedExpectedKey == trimmedReceived {
		slog.DebugContext(ctx, "Host key validation successful for exed server",
			"component", "piper-plugin", "hostname", hostname)
		return nil
	}

	// Neither box key nor server key matched
	slog.DebugContext(ctx, "Host key validation failed - no matching key found",
		"component", "piper-plugin", "hostname", hostname)
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
