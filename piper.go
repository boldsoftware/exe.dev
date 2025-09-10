// Piper plugin implements the sshpiper plugin for exed SSH routing
package exe

import (
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

	"exe.dev/container"
	"exe.dev/exedb"
	"github.com/tg123/sshpiper/libplugin"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
)

// ProxyKeyMapping stores the mapping between ephemeral proxy keys and original user keys
// This allows us to "round-trip" authentication: when a user connects with their key,
// we generate a temporary proxy key to authenticate to exed, then when exed sees that
// proxy key, we can look up the original user's key.
type ProxyKeyMapping struct {
	OriginalPublicKey []byte    // The user's original public key (SSH wire format)
	LocalAddress      string    // The local IP address the client connected to
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
func (p *PiperPlugin) getServerHostKey() (string, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()
	publicKey, err := withRxRes(p.server, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
		return queries.GetSSHHostPublicKey(ctx)
	})
	if err != nil {
		return "", fmt.Errorf("failed to get server host key: %w", err)
	}
	return publicKey, nil
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
	}

	s := grpc.NewServer()

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
	slog.Debug("NextAuthMethods request", "component", "piper-plugin", "user", conn.User(), "remote_addr", conn.RemoteAddr())
	// Always offer both publickey and keyboard-interactive
	return []string{"publickey", "keyboard-interactive"}, nil
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
	slog.Debug("Keyboard interactive auth request", "component", "piper-plugin", "user", conn.User(), "remote_addr", conn.RemoteAddr())

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
		_, err := client("",
			"SSH keys are required to access exe.dev.\nPlease create a key with 'ssh-keygen -t ed25519' and try again.\n\nPress Enter to close this connection.",
			"", false,
		)
		if err != nil {
			slog.Debug("Keyboard interactive challenge failed", "component", "piper-plugin", "error", err)
			return nil, err
		}
	} else {
		// Already shown message - just fail silently to avoid repeating
		slog.Debug("Keyboard interactive auth retry - skipping message display", "component", "piper-plugin", "conn_id", connID)
	}

	// Always return nil to deny access
	return nil, fmt.Errorf("SSH public key authentication is required")
}

// handlePublicKeyAuth handles public key authentication and routing decisions
func (p *PiperPlugin) handlePublicKeyAuth(conn libplugin.ConnMetadata, key []byte) (*libplugin.Upstream, error) {
	slog.Debug("Auth request", "component", "piper-plugin", "user", conn.User(), "remote_addr", conn.RemoteAddr())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use the connection's unique ID
	connID := conn.UniqueID()

	// Check if key is empty or nil - this happens when client has no keys configured
	if len(key) == 0 {
		slog.Debug("No public key provided", "component", "piper-plugin", "user", conn.User())
		return nil, fmt.Errorf("no public key provided")
	}

	// Parse the provided key
	pubKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		slog.Debug("Failed to parse public key", "component", "piper-plugin", "error", err)
		return nil, fmt.Errorf("failed to parse public key: %v", err)
	}

	// Get user info by public key directly
	userID, err := p.server.getUserIDByPublicKey(ctx, pubKey)
	if err != nil {
		slog.Debug("Database error checking SSH key", "component", "piper-plugin", "public_key", pubKey, "error", err)
	}
	slog.Debug("looked up user for ssh key", "component", "piper-plugin", "public_key", pubKey, "user_id", userID)

	// Special handling for local dev mode - allow any connection as localexe user
	if p.server.devMode == "local" && conn.User() == "localexe" && userID == "" {
		// Get the first user from the database for local dev
		userID, err = withRxRes(p.server, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
			return queries.GetFirstUserID(ctx)
		})
		if err == nil {
			slog.Debug("Using first user for local dev mode", "component", "piper-plugin", "user_id", userID)
		}
	}

	localAddress := conn.GetMeta("local_address")
	if localAddress != "" {
		localAddress, _, err = net.SplitHostPort(localAddress)
		if err != nil {
			slog.Error("spliting host and port", "component", "piper-plugin", "local_address", conn.GetMeta("local_address"), "error", err)
			return nil, err
		}
		slog.Info("Extracted local address", "component", "piper-plugin", "local_address", localAddress)
	}
	if localAddress == "" {
		localAddress = "127.0.0.1" // Default fallback
	}

	registered := userID != ""
	username := conn.User()
	slog.Debug("User status", "component", "piper-plugin", "registered", registered, "username", username, "user_id", userID)

	// Check if this is a direct box access attempt
	// In local dev mode, allow box access even without registration
	if username != "" && (registered || p.server.devMode == "local") {
		// If not registered but in local dev mode, use first user
		if !registered && p.server.devMode == "local" && userID == "" {
			userID, err = withRxRes(p.server, ctx, func(ctx context.Context, queries *exedb.Queries) (string, error) {
				return queries.GetFirstUserID(ctx)
			})
			if err == nil {
				slog.Debug("Using first user for local dev box access", "component", "piper-plugin", "user_id", userID)
				registered = true // Pretend they're registered for the checks below
			}
		}

		slog.Info("Checking for box", "component", "piper-plugin", "username", username, "user_id", userID, "registered", registered, "devMode", p.server.devMode)
		if box := p.server.FindBoxByNameForUser(ctx, userID, username); box != nil {
			slog.Info("Found box, routing to container", "component", "piper-plugin", "box_name", box.Name, "box_id", box.ID)
			return p.handleBoxAccess(box, userID, connID)
		} else {
			slog.Info("No box found with name", "component", "piper-plugin", "username", username, "user_id", userID)
		}
	}

	// For all other cases (interactive shell, registration, etc.),
	// route to exed directly using ephemeral proxy authentication
	slog.Debug("Routing to exed shell", "port", p.exedSSHPort, "component", "piper-plugin")
	slog.Debug("User's public key length", "component", "piper-plugin", "key_length_bytes", len(key))

	// EPHEMERAL PROXY KEY APPROACH:
	// 1. Generate a unique, temporary private key for this connection
	// 2. Store a mapping: proxy_key_fingerprint -> original_user_public_key
	// 3. Send proxy private key to exed for authentication
	// 4. When exed sees the proxy key, it can look up the original user's key
	// 5. Mappings expire after a few minutes to prevent memory leaks

	proxyPrivateKeyPEM, proxyFingerprint, err := p.generateEphemeralProxyKey(key, localAddress)
	if err != nil {
		slog.Debug("Failed to generate ephemeral proxy key", "component", "piper-plugin", "error", err)
		return nil, fmt.Errorf("failed to generate ephemeral proxy key: %v", err)
	}

	slog.Debug("Generated ephemeral proxy key with fingerprint", "component", "piper-plugin", "proxy_fingerprint", proxyFingerprint)

	upstream := &libplugin.Upstream{
		Host:     "127.0.0.1", // Use explicit IPv4 instead of localhost
		Port:     int32(p.exedSSHPort),
		UserName: username, // Use original username, not encoded
		// Host key validation is handled by VerifyHostKeyCallback
		IgnoreHostKey: false, // Enable host key validation
		Auth:          libplugin.CreatePrivateKeyAuth([]byte(proxyPrivateKeyPEM)),
	}

	slog.Debug("Returning upstream config", "component", "piper-plugin", "host", upstream.Host, "port", upstream.Port, "username", upstream.UserName, "auth_type", "PrivateKey")
	slog.Debug("Private key length", "component", "piper-plugin", "key_length_bytes", len(proxyPrivateKeyPEM), "key_preview", proxyPrivateKeyPEM[:50])

	return upstream, nil
}

// handleBoxAccess sets up routing to a specific box container
func (p *PiperPlugin) handleBoxAccess(box *exedb.Box, userID, connID string) (*libplugin.Upstream, error) {
	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()

	slog.Debug("handleBoxAccess for box", "component", "piper-plugin", "box_name", box.Name, "box_id", box.ID, "user_id", userID, "conn_id", connID)

	if box.ContainerID == nil {
		slog.Debug("Box has no container ID", "component", "piper-plugin", "box_name", box.Name)
		return nil, fmt.Errorf("box %s is not running", box.Name)
	}

	// Check if container is actually running
	if p.server.containerManager != nil {
		containerInfo, err := p.server.containerManager.GetContainer(ctx, box.AllocID, *box.ContainerID)
		slog.Info("Container status check",
			"component", "piper-plugin", "box_name", box.Name,
			"container_id", *box.ContainerID, "error", err,
			"status", string(containerInfo.Status),
		)
		if err == nil && containerInfo.Status != container.StatusRunning {
			// Container exists but isn't running - route to exed to show logs
			// Use a special username format that exed will recognize
			slog.Info("Container not running, routing to exed for error display",
				"component", "piper-plugin", "box_name", box.Name, "status", containerInfo.Status)

			// Generate ephemeral proxy key for auth to exed
			// Pass nil for original key since this is a special case
			proxyPrivateKeyPEM, _, err := p.generateEphemeralProxyKey(nil, "127.0.0.1")
			if err != nil {
				return nil, fmt.Errorf("failed to generate proxy key: %v", err)
			}

			// Use special username format: "container-logs:<allocID>:<containerID>:<boxName>"
			specialUsername := fmt.Sprintf("container-logs:%s:%s:%s", box.AllocID, *box.ContainerID, box.Name)

			return &libplugin.Upstream{
				Host:          "127.0.0.1",
				Port:          int32(p.exedSSHPort),
				UserName:      specialUsername,
				IgnoreHostKey: false,
				Auth:          libplugin.CreatePrivateKeyAuth([]byte(proxyPrivateKeyPEM)),
			}, nil
		}
	}

	// Get SSH connection details from the database
	sshDetails, err := p.server.GetBoxSSHDetails(ctx, box.ID)
	if err != nil {
		slog.Debug("Failed to get SSH details for box", "component", "piper-plugin", "box_name", box.Name, "error", err)
		return nil, fmt.Errorf("failed to get SSH details for box %s: %v", box.Name, err)
	}
	slog.Debug("SSH details for machine",
		"user_id", userID,
		"conn_id", connID,
		"component", "piper-plugin",
		"box_name", box.Name,
		"port", sshDetails.Port,
		"ctrhost", sshDetails.Ctrhost,
		"user", sshDetails.User,
	)

	// Use SSH details from database instead of querying Docker
	// The container might be paused/stopped, but we have the port mapping in the database
	host := "localhost"
	// In development mode, try to use host.docker.internal if it resolves
	// This handles the case where we're running inside a container (like Sketch)
	// and need to connect to Docker-published ports
	if p.server.devMode != "" {
		if _, err := net.LookupHost("host.docker.internal"); err == nil {
			host = "host.docker.internal"
			slog.Debug("Using host.docker.internal in dev mode", "component", "piper-plugin", "host", host)
		} else {
			slog.Debug("host.docker.internal not available, using localhost", "component", "piper-plugin", "error", err)
		}
	}
	if sshDetails.Ctrhost != nil && *sshDetails.Ctrhost != "" {
		// Parse docker host to extract hostname
		// Formats: tcp://hostname:port, ssh://hostname, or direct hostname
		ctrhost := *sshDetails.Ctrhost
		if strings.HasPrefix(ctrhost, "tcp://") {
			// Extract hostname from tcp://hostname:port
			parts := strings.Split(strings.TrimPrefix(ctrhost, "tcp://"), ":")
			if len(parts) > 0 && parts[0] != "" {
				host = parts[0]
				slog.Debug("Using container host from tcp format", "component", "piper-plugin", "host", host, "ctrhost", ctrhost)
			}
		} else if strings.HasPrefix(ctrhost, "ssh://") {
			// Extract hostname from ssh://[user@]hostname
			sshHost := strings.TrimPrefix(ctrhost, "ssh://")
			// Remove username if present
			if atIndex := strings.Index(sshHost, "@"); atIndex != -1 {
				host = sshHost[atIndex+1:]
			} else {
				host = sshHost
			}
			slog.Debug("Using container host from ssh format", "component", "piper-plugin", "host", host, "ctrhost", ctrhost)
		} else if ctrhost != "" && !strings.HasPrefix(ctrhost, "unix://") {
			// Direct hostname
			host = ctrhost
			slog.Debug("Using direct docker host", "component", "piper-plugin", "host", host)
		}
	}
	port := sshDetails.Port

	// In local dev mode with remote docker host via SSH, we use SSH tunneling
	// so containers are accessible via localhost
	if p.server.devMode != "" && sshDetails.Ctrhost != nil && strings.HasPrefix(*sshDetails.Ctrhost, "ssh://") {
		host = "localhost"
		slog.Debug("Using localhost for SSH tunnel in dev/test mode", "component", "piper-plugin", "original_host", *sshDetails.Ctrhost)
		// SSH tunnel should already be established by container package
	}

	slog.Debug("Using database SSH details for box", "component", "piper-plugin", "box_name", box.Name, "host", host, "port", port)

	// Create upstream configuration for direct SSH to container
	slog.Debug("Creating upstream to container as root", "component", "piper-plugin", "host", host, "port", port)
	slog.Debug("Private key length", "component", "piper-plugin", "key_length_bytes", len(sshDetails.PrivateKey))
	if len(sshDetails.PrivateKey) > 50 {
		slog.Debug("Private key preview", "component", "piper-plugin", "key_preview", sshDetails.PrivateKey[:50])
	}

	// Store the expected host key for this connection if available
	if sshDetails.HostKey != "" {
		p.storeExpectedHostKeyForConnection(connID, sshDetails.HostKey)
		slog.Debug("Stored expected host key for connection", "component", "piper-plugin", "box_name", box.Name, "conn_id", connID, "host_key", sshDetails.HostKey)
	}

	slog.Debug("directing piperd to connect", "host", host, "port", port, "user", sshDetails.User)
	return &libplugin.Upstream{
		Host:     host, // Container host (from ctrhost, host.docker.internal in dev mode, or localhost)
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
func (p *PiperPlugin) generateEphemeralProxyKey(originalUserPublicKey []byte, localAddress string) (string, string, error) {
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
	slog.Debug("Generated proxy key", "component", "piper-plugin", "key_type", signer.PublicKey().Type(), "fingerprint_preview", proxyFingerprint[:16])

	// Validate the generated key by trying to parse it again
	if _, err := ssh.ParsePrivateKey(privateKeyPEMBytes); err != nil {
		slog.Error("Generated private key is invalid", "component", "piper-plugin", "error", err)
		return "", "", fmt.Errorf("generated invalid private key: %v", err)
	}
	slog.Debug("Private key validation successful", "component", "piper-plugin")

	// Store the mapping: proxy key fingerprint -> original user public key + local address
	p.proxyKeyMutex.Lock()
	p.proxyKeyMappings[proxyFingerprint] = &ProxyKeyMapping{
		OriginalPublicKey: originalUserPublicKey,
		LocalAddress:      localAddress,
		CreatedAt:         time.Now(),
	}
	p.proxyKeyMutex.Unlock()

	slog.Debug("Stored ephemeral proxy mapping", "component", "piper-plugin", "proxy_fingerprint", proxyFingerprint, "user_key_length_bytes", len(originalUserPublicKey))

	return privateKeyPEM, proxyFingerprint, nil
}

// lookupOriginalUserKey retrieves the original user's public key and local address from an ephemeral proxy key.
// This is called by exed when it sees a proxy key and needs to identify the original user.
func (p *PiperPlugin) lookupOriginalUserKey(proxyKeyFingerprint string) ([]byte, string, bool) {
	p.proxyKeyMutex.RLock()
	mapping, exists := p.proxyKeyMappings[proxyKeyFingerprint]
	p.proxyKeyMutex.RUnlock()

	if !exists {
		return nil, "", false
	}

	// Check if mapping has expired (15 minutes)
	if time.Since(mapping.CreatedAt) > 15*time.Minute {
		// Remove expired mapping
		p.proxyKeyMutex.Lock()
		delete(p.proxyKeyMappings, proxyKeyFingerprint)
		p.proxyKeyMutex.Unlock()
		return nil, "", false
	}

	return mapping.OriginalPublicKey, mapping.LocalAddress, true
}

// handleVerifyHostKey validates the host key for container connections
// It uses the connection-scoped expected host key stored during connection setup
func (p *PiperPlugin) handleVerifyHostKey(conn libplugin.ConnMetadata, hostname, netaddr string, key []byte) error {
	slog.Debug("VerifyHostKey called", "component", "piper-plugin", "hostname", hostname, "netaddr", netaddr, "key_length", len(key))

	// Convert the received key to SSH authorized_keys format for comparison
	pubKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		slog.Debug("Failed to parse received host key", "component", "piper-plugin", "hostname", hostname, "error", err)
		return fmt.Errorf("invalid host key format: %w", err)
	}
	receivedKey := string(ssh.MarshalAuthorizedKey(pubKey))
	slog.Debug("Received host key", "component", "piper-plugin", "hostname", hostname, "received_key", receivedKey[:50]+"...")

	// Check if this is a box connection by looking up stored expected keys
	connID := conn.UniqueID()
	expectedKey, found := p.getExpectedHostKeyForConnection(connID)
	slog.Debug("Expected key lookup", "component", "piper-plugin", "conn_id", connID, "found", found)

	if found {
		// This is a box connection with a stored expected key
		if strings.TrimSpace(expectedKey) == strings.TrimSpace(receivedKey) {
			slog.Debug("Host key validation successful for box",
				"component", "piper-plugin",
				"conn_id", connID, "hostname", hostname,
			)
			return nil
		}
		// Key mismatch for box connection
		slog.Debug("Host key validation failed - box key mismatch",
			"component", "piper-plugin",
			"conn_id", connID, "hostname", hostname,
			"expected_key", expectedKey, "received_key", receivedKey,
		)
		return fmt.Errorf("host key validation failed for %s", hostname)
	}

	// No stored expected key - this is a connection to the main exed server
	// Validate against the server's host key from the database
	serverHostKey, err := p.getServerHostKey()
	if err != nil {
		slog.Debug("Failed to get server host key", "component", "piper-plugin", "error", err)
		return fmt.Errorf("host key validation failed for %s", hostname)
	}
	slog.Debug("Got server host key", "component", "piper-plugin", "hostname", hostname, "server_key", serverHostKey[:50]+"...")

	if strings.TrimSpace(serverHostKey) == strings.TrimSpace(receivedKey) {
		slog.Debug("Host key validation successful for exed server",
			"component", "piper-plugin", "hostname", hostname)
		return nil
	}

	// Neither box key nor server key matched
	slog.Debug("Host key validation failed - no matching key found",
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
