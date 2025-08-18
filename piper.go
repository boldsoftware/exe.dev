// Piper plugin implements the sshpiper plugin for exed SSH routing
package exe

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

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
	CreatedAt         time.Time // When this mapping was created (for expiration)
}

// PiperPlugin implements the sshpiper plugin interface
type PiperPlugin struct {
	server *Server
	addr   string

	// proxyKeyMappings maps SSH public key fingerprints of ephemeral proxy keys
	// to the original user's public key. This allows us to:
	// 1. Generate a unique proxy key for each user connection
	// 2. Look up the original user when exed receives the proxy key
	// 3. Expire old mappings to prevent memory leaks
	proxyKeyMappings map[string]*ProxyKeyMapping
	// RWMutex allows concurrent reads while protecting writes
	proxyKeyMutex sync.RWMutex
}

// NewPiperPlugin creates a new piper plugin instance
func NewPiperPlugin(server *Server, addr string) *PiperPlugin {
	p := &PiperPlugin{
		server:           server,
		addr:             addr,
		proxyKeyMappings: make(map[string]*ProxyKeyMapping),
	}

	// Start cleanup goroutine to remove expired proxy key mappings
	go p.cleanupExpiredMappings()

	return p
}

// Serve starts the sshpiper plugin gRPC server
func (p *PiperPlugin) Serve() error {
	config := libplugin.SshPiperPluginConfig{
		PublicKeyCallback:     p.handlePublicKeyAuth,
		VerifyHostKeyCallback: p.handleVerifyHostKey,
	}

	s := grpc.NewServer()

	lis, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", p.addr, err)
	}

	plugin, err := libplugin.NewFromGrpc(config, s, lis)
	if err != nil {
		return fmt.Errorf("failed to create plugin: %v", err)
	}

	log.Printf("[PIPER DEBUG] Starting sshpiper plugin on %s", p.addr)
	log.Printf("[PIPER DEBUG] Plugin server listening on %s", lis.Addr())

	err = plugin.Serve()
	if err != nil {
		log.Printf("[PIPER ERROR] Plugin server error: %v", err)
	}
	return err
}

// handlePublicKeyAuth handles public key authentication and routing decisions
func (p *PiperPlugin) handlePublicKeyAuth(conn libplugin.ConnMetadata, key []byte) (*libplugin.Upstream, error) {
	log.Printf("[PIPER DEBUG] Auth request - User: %s, RemoteAddr: %s", conn.User(), conn.RemoteAddr())

	// Parse the provided key
	pubKey, err := ssh.ParsePublicKey(key)
	if err != nil {
		log.Printf("[PIPER DEBUG] Failed to parse public key: %v", err)
		return nil, fmt.Errorf("failed to parse public key: %v", err)
	}

	// Get fingerprint and check if user is registered
	fingerprint := p.server.GetPublicKeyFingerprint(pubKey)
	log.Printf("[PIPER DEBUG] Key fingerprint: %s", fingerprint)

	email, verified, err := p.server.GetEmailBySSHKey(fingerprint)
	if err != nil {
		log.Printf("[PIPER DEBUG] Database error checking SSH key %s: %v", fingerprint, err)
	}
	log.Printf("[PIPER DEBUG] User lookup - email: %s, verified: %t", email, verified)

	registered := email != "" && verified
	username := conn.User()
	log.Printf("[PIPER DEBUG] Registered: %t, Username: %s", registered, username)

	// Check if this is a direct machine access attempt
	if username != "" && registered {
		log.Printf("[PIPER DEBUG] Checking for machine: %s", username)
		if machine := p.server.FindMachineByNameForUser(fingerprint, username); machine != nil {
			log.Printf("[PIPER DEBUG] Found machine %s (ID: %d), routing to container", machine.Name, machine.ID)
			return p.handleMachineAccess(machine, fingerprint)
		} else {
			log.Printf("[PIPER DEBUG] No machine found with name: %s", username)
		}
	}

	// For all other cases (interactive shell, registration, etc.),
	// route to exed directly on port 2223 using ephemeral proxy authentication
	log.Printf("[PIPER DEBUG] Routing to exed shell on port 2223")
	log.Printf("[PIPER DEBUG] User's public key length: %d bytes", len(key))

	// EPHEMERAL PROXY KEY APPROACH:
	// 1. Generate a unique, temporary private key for this connection
	// 2. Store a mapping: proxy_key_fingerprint -> original_user_public_key
	// 3. Send proxy private key to exed for authentication
	// 4. When exed sees the proxy key, it can look up the original user's key
	// 5. Mappings expire after a few minutes to prevent memory leaks

	proxyPrivateKeyPEM, proxyFingerprint, err := p.generateEphemeralProxyKey(key)
	if err != nil {
		log.Printf("[PIPER DEBUG] Failed to generate ephemeral proxy key: %v", err)
		return nil, fmt.Errorf("failed to generate ephemeral proxy key: %v", err)
	}

	log.Printf("[PIPER DEBUG] Generated ephemeral proxy key with fingerprint: %s", proxyFingerprint)

	upstream := &libplugin.Upstream{
		Host:     "127.0.0.1", // Use explicit IPv4 instead of localhost
		Port:     2223,
		UserName: username, // Use original username, not encoded
		Auth:     libplugin.CreatePrivateKeyAuth([]byte(proxyPrivateKeyPEM)),
	}

	log.Printf("[PIPER DEBUG] Returning upstream config: Host=%s, Port=%d, User=%s, AuthType=PrivateKey",
		upstream.Host, upstream.Port, upstream.UserName)
	log.Printf("[PIPER DEBUG] Private key length: %d bytes, starts with: %s",
		len(proxyPrivateKeyPEM), proxyPrivateKeyPEM[:50])

	return upstream, nil
}

// handleMachineAccess sets up routing to a specific machine container
func (p *PiperPlugin) handleMachineAccess(machine *Machine, fingerprint string) (*libplugin.Upstream, error) {
	log.Printf("[PIPER DEBUG] handleMachineAccess for machine %s (ID: %d)", machine.Name, machine.ID)

	if machine.ContainerID == nil {
		log.Printf("[PIPER DEBUG] Machine %s has no container ID", machine.Name)
		return nil, fmt.Errorf("machine %s is not running", machine.Name)
	}

	// Get SSH connection details from the database
	sshDetails, err := p.server.GetMachineSSHDetails(machine.ID)
	if err != nil {
		log.Printf("[PIPER DEBUG] Failed to get SSH details for machine %s: %v", machine.Name, err)
		return nil, fmt.Errorf("failed to get SSH details for machine %s: %v", machine.Name, err)
	}
	log.Printf("[PIPER DEBUG] Got SSH details for machine %s (port: %d)", machine.Name, sshDetails.Port)

	// Use SSH details from database instead of querying Docker
	// The container might be paused/stopped, but we have the port mapping in the database
	host := "localhost"
	if sshDetails.DockerHost != nil && *sshDetails.DockerHost != "" {
		// Parse docker host to extract hostname
		// Formats: tcp://hostname:port, ssh://hostname, or direct hostname
		dockerHost := *sshDetails.DockerHost
		if strings.HasPrefix(dockerHost, "tcp://") {
			// Extract hostname from tcp://hostname:port
			parts := strings.Split(strings.TrimPrefix(dockerHost, "tcp://"), ":")
			if len(parts) > 0 && parts[0] != "" {
				host = parts[0]
				log.Printf("[PIPER DEBUG] Using docker host %s from tcp format: %s", host, dockerHost)
			}
		} else if strings.HasPrefix(dockerHost, "ssh://") {
			// Extract hostname from ssh://hostname
			host = strings.TrimPrefix(dockerHost, "ssh://")
			log.Printf("[PIPER DEBUG] Using docker host %s from ssh format: %s", host, dockerHost)
		} else if dockerHost != "" && !strings.HasPrefix(dockerHost, "unix://") {
			// Direct hostname
			host = dockerHost
			log.Printf("[PIPER DEBUG] Using direct docker host %s", host)
		}
	}
	port := sshDetails.Port
	log.Printf("[PIPER DEBUG] Using database SSH details for machine %s: %s:%d", machine.Name, host, port)

	// Create upstream configuration for direct SSH to container
	log.Printf("[PIPER DEBUG] Creating upstream to container %s:%d as root", host, port)
	log.Printf("[PIPER DEBUG] Private key length: %d bytes", len(sshDetails.PrivateKey))
	if len(sshDetails.PrivateKey) > 50 {
		log.Printf("[PIPER DEBUG] Private key preview: %s...", sshDetails.PrivateKey[:50])
	}
	return &libplugin.Upstream{
		Host:     host, // Container host (from docker_host or localhost)
		Port:     int32(port),
		UserName: "root", // Containers use root user
		// TODO(philip): we know their host key, so we could just use it.
		IgnoreHostKey: true, // Skip host key verification for containers
		Auth:          libplugin.CreatePrivateKeyAuth([]byte(sshDetails.PrivateKey)),
	}, nil
}

// generateEphemeralProxyKey creates a temporary RSA key pair for this specific connection.
// It stores a mapping from the proxy key's fingerprint to the user's original public key,
// allowing exed to later identify the original user when it sees the proxy key.
//
// Returns: (privateKeyPEM, proxyKeyFingerprint, error)
func (p *PiperPlugin) generateEphemeralProxyKey(originalUserPublicKey []byte) (string, string, error) {
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
	log.Printf("[PIPER DEBUG] Generated proxy key - Type: %s, Fingerprint: %s",
		signer.PublicKey().Type(), proxyFingerprint[:16])

	// Validate the generated key by trying to parse it again
	if _, err := ssh.ParsePrivateKey(privateKeyPEMBytes); err != nil {
		log.Printf("[PIPER ERROR] Generated private key is invalid: %v", err)
		return "", "", fmt.Errorf("generated invalid private key: %v", err)
	}
	log.Printf("[PIPER DEBUG] Private key validation successful")

	// Store the mapping: proxy key fingerprint -> original user public key
	p.proxyKeyMutex.Lock()
	p.proxyKeyMappings[proxyFingerprint] = &ProxyKeyMapping{
		OriginalPublicKey: originalUserPublicKey,
		CreatedAt:         time.Now(),
	}
	p.proxyKeyMutex.Unlock()

	log.Printf("[PIPER DEBUG] Stored ephemeral proxy mapping: %s -> user key (%d bytes)",
		proxyFingerprint, len(originalUserPublicKey))

	return privateKeyPEM, proxyFingerprint, nil
}

// lookupOriginalUserKey retrieves the original user's public key from an ephemeral proxy key.
// This is called by exed when it sees a proxy key and needs to identify the original user.
func (p *PiperPlugin) lookupOriginalUserKey(proxyKeyFingerprint string) ([]byte, bool) {
	p.proxyKeyMutex.RLock()
	mapping, exists := p.proxyKeyMappings[proxyKeyFingerprint]
	p.proxyKeyMutex.RUnlock()

	if !exists {
		return nil, false
	}

	// Check if mapping has expired (15 minutes)
	if time.Since(mapping.CreatedAt) > 15*time.Minute {
		// Remove expired mapping
		p.proxyKeyMutex.Lock()
		delete(p.proxyKeyMappings, proxyKeyFingerprint)
		p.proxyKeyMutex.Unlock()
		return nil, false
	}

	return mapping.OriginalPublicKey, true
}

// handleVerifyHostKey handles host key verification for upstream connections
// TODO(philip): We could do host key checking here; I think we have all the
// relevant data.
func (p *PiperPlugin) handleVerifyHostKey(conn libplugin.ConnMetadata, hostname, netaddr string, key []byte) error {
	log.Printf("[PIPER DEBUG] VerifyHostKey called - hostname: %s, netaddr: %s, key length: %d", hostname, netaddr, len(key))

	log.Printf("[PIPER DEBUG] Accepting host key for %s", hostname)
	return nil // Accept the host key
}

// cleanupExpiredMappings runs periodically to remove expired proxy key mappings
func (p *PiperPlugin) cleanupExpiredMappings() {
	ticker := time.NewTicker(5 * time.Minute) // Run every 5 minutes
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		expiredKeys := make([]string, 0)

		p.proxyKeyMutex.RLock()
		for fingerprint, mapping := range p.proxyKeyMappings {
			if now.Sub(mapping.CreatedAt) > 15*time.Minute {
				expiredKeys = append(expiredKeys, fingerprint)
			}
		}
		if len(expiredKeys) > 0 {
			for _, key := range expiredKeys {
				delete(p.proxyKeyMappings, key)
			}
			log.Printf("[PIPER DEBUG] Cleaned up %d expired proxy key mappings", len(expiredKeys))
		}
		p.proxyKeyMutex.RUnlock()
	}
}
