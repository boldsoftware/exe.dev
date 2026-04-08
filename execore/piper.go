// Piper plugin implements the sshpiper plugin for exed SSH routing
package execore

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
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

	// expectedHostKeys maps connection IDs to their expected host keys for validation
	expectedHostKeys      map[string]*HostKeyMapping
	expectedHostKeysMutex sync.Mutex
}

// NewPiperPlugin creates a new piper plugin instance
func NewPiperPlugin(server *Server, host string, port int) *PiperPlugin {
	p := &PiperPlugin{
		server:           server,
		exedSSHHost:      host,
		exedSSHPort:      port,
		proxyKeyMappings: make(map[string]*ProxyKeyMapping),
		expectedHostKeys: make(map[string]*HostKeyMapping),
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

// vmAccessPrefix is the username prefix for name-based VM access.
// Usage: ssh vm+vmname@exe.dev
const vmAccessPrefix = "vm+"

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

// handleKeyboardInteractive tells users with no SSH key how to create one.
//
// OpenSSH falls through to keyboard-interactive when publickey auth doesn't
// succeed. A user who presented a key and got denied for a specific reason
// (wrong VM, VM not running, support access denied) never reaches this
// callback: handlePublicKeyAuth returns a Deny, which sshpiperd eager-sends
// as a banner and then terminates the connection without offering any
// further auth methods. So if we're here, the client never tried publickey
// at all — it genuinely has no key, and we should surface the "please run
// ssh-keygen" banner.
func (p *PiperPlugin) handleKeyboardInteractive(_ libplugin.ConnMetadata, _ libplugin.KeyboardInteractiveChallenge) (*libplugin.Upstream, error) {
	return nil, libplugin.Deny(
		"\nSSH keys are required to access exe.dev.\nPlease create a key with 'ssh-keygen -t ed25519' and try again.\n\n",
		"no SSH public key provided",
	)
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

	// Socket RTT injected by sshpiperd at connection time.
	if rttStr := conn.GetMeta("socket_rtt_us"); rttStr != "" {
		cl.add(slog.String("socket_rtt_us", rttStr))
	}

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

	// Check SSH key permissions (expiry and VM restrictions).
	publicKeyStr := string(ssh.MarshalAuthorizedKey(pubKey))
	keyPerms, err := p.server.getSSHKeyPermsByPublicKey(ctx, publicKeyStr)
	if errors.Is(err, errSSHKeyNotFound) {
		// Key not in ssh_keys table — normal for ephemeral proxy keys.
		keyPerms = nil
	} else if err != nil {
		slog.ErrorContext(ctx, "failed to look up SSH key permissions", "component", "piper-plugin", "key_fingerprint", keyFingerprint, "error", err)
		return nil, fmt.Errorf("internal error checking SSH key permissions")
	}
	if keyPerms != nil && keyPerms.IsExpired() {
		slog.WarnContext(ctx, "expired SSH key used", "component", "piper-plugin", "key_fingerprint", keyFingerprint)
		return nil, fmt.Errorf("SSH key has expired")
	}
	if keyPerms != nil {
		ctx = withSSHKeyPerms(ctx, keyPerms)
	}

	// Check for support access: ssh support+vmname@exe.cloud
	if supportBoxName, isSupport := strings.CutPrefix(username, supportAccessPrefix); isSupport {
		box := p.server.FindBoxForExeSudoer(ctx, userID, supportBoxName)
		if box == nil {
			slog.WarnContext(ctx, "support access denied", "component", "piper-plugin", "vm_name", supportBoxName, "user_id", userID)
			return nil, libplugin.Deny(
				fmt.Sprintf("Support access denied for VM %q.\n\nEither:\n- You don't have support privileges, or\n- The VM doesn't have support access enabled\n\n", supportBoxName),
				fmt.Sprintf("support access denied for VM %q by user %s", supportBoxName, userID),
			)
		}
		slog.InfoContext(ctx, "support access granted", "component", "piper-plugin", "vm_name", box.Name, "vm_id", box.ID, "support_user_id", userID)
		cl.add(slog.Bool("support_access", true))
		return p.handleBoxAccess(ctx, box, userID, connID)
	}

	// Name-based VM access: ssh vm+vmname@exe.dev
	// This replicates the exact same lookup chain as IP-shard-based routing
	// (FindBoxByIPShard → FindTeamBoxByIPShard → FindTeamSSHSharedBoxByIPShard)
	// but uses name-based lookups, avoiding IP shard exhaustion for large teams.
	if vmBoxName, isVM := strings.CutPrefix(username, vmAccessPrefix); isVM {
		// 1. User's own box / team admin accessing a member's box
		if box, _, err := p.server.FindAccessibleBox(ctx, userID, vmBoxName); err == nil {
			cl.add(slog.String("route", "by_vm_name"))
			return p.handleBoxAccess(ctx, box, userID, connID)
		}
		// 2. Team SSH sharing (box owner enabled team_ssh)
		if box := p.server.FindTeamSSHSharedBoxByName(ctx, userID, vmBoxName); box != nil {
			cl.add(slog.String("route", "by_vm_name_ssh_share"))
			return p.handleBoxAccess(ctx, box, userID, connID)
		}
		slog.WarnContext(ctx, "vm+ access denied", "component", "piper-plugin", "vm_name", vmBoxName, "user_id", userID)
		return nil, libplugin.Deny(
			fmt.Sprintf("Access denied for VM %q.\n\n", vmBoxName),
			fmt.Sprintf("vm+ access denied for VM %q by user %s", vmBoxName, userID),
		)
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

	// A VM-restricted key must not reach the exed REPL.
	if keyPerms != nil && keyPerms.VM != "" {
		slog.WarnContext(ctx, "VM-restricted SSH key tried to access REPL", "component", "piper-plugin", "allowed_vm", keyPerms.VM)
		return nil, fmt.Errorf("SSH key is restricted to VM %q", keyPerms.VM)
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

	// Enforce SSH key command, VM, and tag restrictions.
	if perms := getSSHKeyPerms(ctx); perms != nil {
		if !perms.AllowsDirectSSH() {
			slog.WarnContext(ctx, "SSH key command restriction denied direct SSH", "component", "piper-plugin", "vm_name", box.Name, "allowed_cmds", perms.Cmds)
			return nil, fmt.Errorf("SSH key does not allow direct SSH access")
		}
		if !perms.AllowsVM(box.Name) {
			slog.WarnContext(ctx, "SSH key VM restriction denied access", "component", "piper-plugin", "vm_name", box.Name, "allowed_vm", perms.VM)
			return nil, fmt.Errorf("SSH key is restricted to VM %q", perms.VM)
		}
		if !perms.AllowsBoxByTag(box.GetTags()) {
			slog.WarnContext(ctx, "SSH key tag restriction denied access", "component", "piper-plugin", "vm_name", box.Name, "required_tag", perms.Tag)
			return nil, fmt.Errorf("VM %q not found", box.Name)
		}
	}

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
		// The VM hasn't been provisioned yet (or its previous instance was
		// reaped). Same user-facing situation as a stopped VM: the user owns
		// it (handlePublicKeyAuth verified that before calling us) but there
		// is nothing to route to. Surface a denial banner so they see _why_
		// and how to investigate or delete it. Deny terminates the
		// connection so we don't fall back to the "please ssh-keygen"
		// keyboard-interactive banner.
		return nil, libplugin.Deny(
			p.stoppedVMBanner(box),
			fmt.Sprintf("VM %q is not running", box.Name),
		)
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
			return nil, internalErrorDenial(traceID, fmt.Sprintf("GetInstance: %v", err))
		}
		if instanceResp.Instance != nil {
			cl.add(slog.String("instance_state", instanceResp.Instance.State.String()))
			// If the instance is not running, there is no upstream to route to.
			// Deny the auth attempt with a banner that tells the user how to
			// fetch logs via the REPL. We deliberately don't fetch or embed
			// logs here — see stoppedVMBanner for why.
			if instanceResp.Instance.State != api.VMState_RUNNING && instanceResp.Instance.State != api.VMState_STARTING {
				emitPiperConnLog(ctx, connID, "SSH Connection to non-running VM",
					slog.String("log_type", "vm-ssh-connection-not-running"),
					slog.String("ctrhost", box.Ctrhost),
				)
				return nil, libplugin.Deny(
					p.stoppedVMBanner(box),
					fmt.Sprintf("VM %q is not running", box.Name),
				)
			}
		}
	}

	// Get SSH connection details from the database
	sshDetails, err := p.server.GetBoxSSHDetails(ctx, box.ID)
	if err != nil {
		traceID := tracing.TraceIDFromContext(ctx)
		slog.ErrorContext(ctx, "piper-plugin GetBoxSSHDetails failed",
			"vm_name", box.Name,
			"vm_id", box.ID,
			"user_id", userID,
			"conn_id", connID,
			"error", err,
		)
		return nil, internalErrorDenial(traceID, fmt.Sprintf("GetBoxSSHDetails for VM %s: %v", box.Name, err))
	}
	if sshDetails.HostKey == "" {
		traceID := tracing.TraceIDFromContext(ctx)
		slog.ErrorContext(ctx, "piper-plugin VM has no stored host key",
			"vm_name", box.Name,
			"vm_id", box.ID,
			"user_id", userID,
			"conn_id", connID,
		)
		return nil, internalErrorDenial(traceID, fmt.Sprintf("VM %s has no stored host key", box.Name))
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
}

// cleanupExpiredMappings runs periodically to remove expired proxy key
// mappings and host key mappings.
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

		if len(expiredKeys) > 0 || hostKeyCount > 0 {
			slog.Debug("Cleaned up expired mappings", "component", "piper-plugin", "proxy_keys", len(expiredKeys), "host_keys", hostKeyCount)
		}
	}
}

// stoppedVMBanner renders the denial banner shown to users who try to SSH
// into a VM that isn't running. It deliberately does NOT include the VM's
// logs: the banner is emitted by sshpiperd as an SSH_MSG_USERAUTH_BANNER
// on a failed auth attempt, which is a delicate place to exfiltrate data
// from (the structured auth state is still in flight, denial-carried
// banners get routed through a different code path than authenticated
// sessions, etc). Instead, we point the user at `vm-logs <name>`, a
// first-class REPL command that runs inside a properly authenticated
// session and enforces ownership / team access the same way every other
// VM command does.
func (p *PiperPlugin) stoppedVMBanner(box *exedb.Box) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\033[1;31mVM %q is not running.\033[0m\n\n", box.Name)
	fmt.Fprintf(&b, "To see why it failed, run:\n\n    \033[1m%s vm-logs %s\033[0m\n\n", p.server.replSSHConnectionCommand(), box.Name)
	fmt.Fprintf(&b, "To delete it, run:\n\n    \033[1m%s rm %s\033[0m\n\n", p.server.replSSHConnectionCommand(), box.Name)
	return b.String()
}

// internalErrorDenial returns an AuthDenialError with a generic
// "internal error" banner that includes the trace ID. We use this on every
// unexpected failure path in handlePublicKeyAuth so users get something they
// can paste into a bug report instead of a silent failure. Deny terminates
// the connection: there is no productive retry, and we'd rather not fall
// back to the keyboard-interactive "please ssh-keygen" banner after an
// internal error.
func internalErrorDenial(traceID, detail string) error {
	return libplugin.Deny(
		fmt.Sprintf("\n\033[1;31mInternal error.\033[0m\n\nPlease report this trace ID to support@exe.dev:\n\n    \033[1m%s\033[0m\n\n", traceID),
		fmt.Sprintf("internal error (trace %s): %s", traceID, detail),
	)
}
