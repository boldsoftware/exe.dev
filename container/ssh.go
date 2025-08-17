package container

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// ContainerSSHKeys holds the SSH key material for a container
type ContainerSSHKeys struct {
	ServerIdentityKey string // SSH server private key (PEM format)
	AuthorizedKeys    string // User certificate for authorized_keys (client auth)
	CAPublicKey       string // CA public key for mutual auth
	HostCertificate   string // Host certificate for host key validation
	ClientPrivateKey  string // Private key for connecting to container (PEM format)
	SSHPort           int    // SSH port for this container
}

// GenerateContainerSSHKeys generates all SSH key material needed for a container
// This is adapted from the generateSSHKeys function in bold/skaband/dockerhost
// TODO(philip): Not totally sure we need certs at all.
func GenerateContainerSSHKeys() (*ContainerSSHKeys, error) {
	// Generate server identity key (Ed25519)
	_, serverPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate server key: %w", err)
	}

	// Generate user/client key for connecting to container
	clientPub, clientPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate client key: %w", err)
	}

	// Generate CA key for mutual authentication
	caPub, caPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	// Convert to SSH format
	clientSSHPub, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		return nil, fmt.Errorf("failed to create client SSH public key: %w", err)
	}

	caSSHPub, err := ssh.NewPublicKey(caPub)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA SSH public key: %w", err)
	}

	// Generate server public key from server private key for host certificate
	serverSSHPub, err := ssh.NewPublicKey(serverPriv.Public())
	if err != nil {
		return nil, fmt.Errorf("failed to create server SSH public key: %w", err)
	}

	// Create host certificate signed by CA
	caSigner, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA signer: %w", err)
	}

	// Create host certificate for the SSH server
	hostCert := &ssh.Certificate{
		Key:             serverSSHPub,
		Serial:          1,
		CertType:        ssh.HostCert,
		// Use a descriptive key ID for host certificates
		KeyId:           "exe-container-host",
		ValidPrincipals: []string{"localhost", "127.0.0.1", "::1"},
		ValidAfter:      uint64(time.Now().Add(-1 * time.Hour).Unix()),
		// Set a long expiration time (1 year) for host certificates
		ValidBefore:     uint64(time.Now().Add(8760 * time.Hour).Unix()), // 1 year
	}

	// Create user certificate for client authentication
	userCert := &ssh.Certificate{
		Key:             clientSSHPub,
		Serial:          2,
		CertType:        ssh.UserCert,
		// Let's include the container-name.team-name here.
		KeyId:           "exe-user",
		ValidPrincipals: []string{"root"},
		ValidAfter:      uint64(time.Now().Add(-1 * time.Hour).Unix()),
		// We don't want expiration
		ValidBefore:     uint64(time.Now().Add(2160 * time.Hour).Unix()), // 90 days
		Permissions: ssh.Permissions{
			Extensions: map[string]string{
				"permit-pty":              "",
				"permit-agent-forwarding": "",
				"permit-port-forwarding":  "",
			},
		},
	}

	if err := hostCert.SignCert(crand.Reader, caSigner); err != nil {
		return nil, fmt.Errorf("failed to sign host certificate: %w", err)
	}

	if err := userCert.SignCert(crand.Reader, caSigner); err != nil {
		return nil, fmt.Errorf("failed to sign user certificate: %w", err)
	}

	// Marshal keys to PEM/SSH format
	serverPrivKeyPEM, err := ssh.MarshalPrivateKey(serverPriv, "exe container server key")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal server private key: %w", err)
	}

	clientPrivKeyPEM, err := ssh.MarshalPrivateKey(clientPriv, "exe container client key")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal client private key: %w", err)
	}

	return &ContainerSSHKeys{
		ServerIdentityKey: string(pem.EncodeToMemory(serverPrivKeyPEM)),
		AuthorizedKeys:    string(ssh.MarshalAuthorizedKey(clientSSHPub)), // Public key for authorized_keys file
		CAPublicKey:       string(ssh.MarshalAuthorizedKey(caSSHPub)),
		HostCertificate:   string(ssh.MarshalAuthorizedKey(hostCert)),
		ClientPrivateKey:  string(pem.EncodeToMemory(clientPrivKeyPEM)),
		// SSH port is always 22 inside containers (Docker maps to random host ports)
		SSHPort:           22,
	}, nil
}

// ParsePrivateKey parses a PEM-encoded private key string
func ParsePrivateKey(pemData string) (interface{}, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	switch block.Type {
	case "OPENSSH PRIVATE KEY":
		return ssh.ParseRawPrivateKey([]byte(pemData))
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported private key type: %s", block.Type)
	}
}

// CreateSSHSigner creates an SSH signer from a PEM-encoded private key
func CreateSSHSigner(pemData string) (ssh.Signer, error) {
	privKey, err := ParsePrivateKey(pemData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	return ssh.NewSignerFromKey(privKey)
}
